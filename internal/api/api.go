// Package api expone el modelo y el clúster ML como una API HTTP en Go:
// /health, /predict, /train, /metrics, /docs (Swagger) y /ws (WebSocket).
// Es el coordinador del clúster: entrena enviando tareas a los nodos por TCP
// (internal/coordinador), guarda resultados en MongoDB (internal/almacen) y
// deja un punto de enganche (Cache) para Redis.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/almeida-aguilar/cs-cc65-proyecto7-vacunacion-ml-2026-20/internal/almacen"
	"github.com/franztoledo/logreg"
)

// limiteCuerpo evita que una petición consuma memoria sin control.
const limiteCuerpo = 1 << 20

// tamanoColaPersistencia es el búfer de predicciones pendientes de guardar.
const tamanoColaPersistencia = 1024

// Cache es el punto de enganche para Redis (issue #31). Obtener devuelve
// (nil, nil) cuando no hay acierto; los errores de la caché nunca deben
// romper una predicción.
type Cache interface {
	Obtener(ctx context.Context, hash string) (*almacen.Prediccion, error)
	Guardar(ctx context.Context, prediccion *almacen.Prediccion) error
}

// Opciones configura el servidor de la API.
type Opciones struct {
	// RutaModelo es el JSON del modelo: se carga al iniciar si existe y se
	// reescribe tras cada entrenamiento. Vacío = solo en memoria.
	RutaModelo string
	// Nodos son las direcciones TCP de los nodos ML (host:puerto).
	Nodos []string
	// Repo persiste ejecuciones, métricas y predicciones. Nil usa memoria.
	Repo almacen.Repositorio
	// Cache es opcional (Redis).
	Cache Cache
	// Log recibe los mensajes de diagnóstico; nil usa log.Printf.
	Log func(formato string, valores ...any)
}

// Servidor agrupa el estado compartido de la API. El modelo se protege con un
// RWMutex: muchas lecturas concurrentes (/predict) y una escritura al terminar
// un entrenamiento. El resto del estado usa canales y variables atómicas.
type Servidor struct {
	opciones Opciones
	repo     almacen.Repositorio
	log      func(string, ...any)

	mu              sync.RWMutex
	modelo          *logreg.Modelo
	ejecucionModelo string
	evaluacion      *logreg.Metricas

	cola        chan almacen.Prediccion // predicciones por guardar (no bloquea /predict)
	descartadas atomic.Int64
	entrenando  atomic.Bool
	latencias   ventanaLatencias
	hub         *hub

	ctx      context.Context
	cancelar context.CancelFunc
	trabajos sync.WaitGroup
	cierre   sync.Once
	inicio   time.Time
}

// NuevoServidor crea el servidor, carga el modelo si existe y arranca el
// trabajador que persiste las predicciones.
func NuevoServidor(opciones Opciones) (*Servidor, error) {
	repo := opciones.Repo
	if repo == nil {
		repo = almacen.NuevaMemoria()
	}
	registro := opciones.Log
	if registro == nil {
		registro = log.Printf
	}
	ctx, cancelar := context.WithCancel(context.Background())
	servidor := &Servidor{
		opciones: opciones,
		repo:     repo,
		log:      registro,
		cola:     make(chan almacen.Prediccion, tamanoColaPersistencia),
		hub:      nuevoHub(),
		ctx:      ctx,
		cancelar: cancelar,
		inicio:   time.Now(),
	}

	if opciones.RutaModelo != "" {
		modelo, err := logreg.CargarModelo(opciones.RutaModelo)
		switch {
		case err == nil:
			servidor.modelo = modelo
			registro("modelo cargado desde %s (%d variables)", opciones.RutaModelo, modelo.NumVariables())
		case errors.Is(err, os.ErrNotExist):
			registro("no existe %s: la API arranca sin modelo hasta que se llame a /train", opciones.RutaModelo)
		default:
			cancelar()
			return nil, fmt.Errorf("no se pudo cargar el modelo %s: %w", opciones.RutaModelo, err)
		}
	}

	servidor.trabajos.Add(1)
	go servidor.persistirPredicciones()
	return servidor, nil
}

// FijarModelo reemplaza el modelo vigente (usado tras entrenar y en pruebas).
func (s *Servidor) FijarModelo(modelo *logreg.Modelo, ejecucionID string) {
	s.mu.Lock()
	s.modelo = modelo
	s.ejecucionModelo = ejecucionID
	s.mu.Unlock()
}

// modeloActual devuelve el modelo vigente y la ejecución que lo produjo. Los
// modelos de logreg son de solo lectura al predecir, así que se pueden usar
// sin mantener el cerrojo.
func (s *Servidor) modeloActual() (*logreg.Modelo, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.modelo, s.ejecucionModelo
}

// Cerrar cancela el entrenamiento en curso, vacía la cola de predicciones y
// espera a las goroutinas internas.
func (s *Servidor) Cerrar() {
	s.cierre.Do(func() {
		s.cancelar()
		// Quien llama debe haber dejado de aceptar peticiones (http.Server.Shutdown)
		// antes de Cerrar: enviar a una cola cerrada provocaría un panic.
		close(s.cola)
	})
	s.trabajos.Wait()
}

// persistirPredicciones guarda en el repositorio (y en la caché) cada
// predicción de la cola. Corre en una sola goroutine: /predict solo envía al
// canal, así la latencia no depende de la base de datos.
func (s *Servidor) persistirPredicciones() {
	defer s.trabajos.Done()
	for prediccion := range s.cola {
		copia := prediccion
		if err := s.repo.InsertarPrediccion(context.Background(), &copia); err != nil {
			s.log("no se pudo guardar la predicción: %v", err)
		}
		if s.opciones.Cache != nil {
			if err := s.opciones.Cache.Guardar(context.Background(), &copia); err != nil {
				s.log("no se pudo guardar en la caché: %v", err)
			}
		}
	}
}

// Handler devuelve el enrutador HTTP con CORS habilitado (la SPA del
// entregable 3 vivirá en otro origen).
func (s *Servidor) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.salud)
	mux.HandleFunc("POST /predict", s.predecir)
	mux.HandleFunc("POST /train", s.entrenar)
	mux.HandleFunc("GET /metrics", s.metricas)
	mux.HandleFunc("GET /ws", s.websocket)
	mux.HandleFunc("GET /docs", s.documentacion)
	mux.HandleFunc("GET /openapi.yaml", s.especificacion)
	return conCORS(mux)
}

// conCORS añade las cabeceras CORS y atiende las peticiones OPTIONS.
func conCORS(siguiente http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		siguiente.ServeHTTP(w, r)
	})
}

// errorAPI es el cuerpo JSON de todas las respuestas de error.
type errorAPI struct {
	Error string `json:"error"`
}

// responder escribe un JSON con el estado y la latencia medida desde inicio.
func responder(w http.ResponseWriter, estado int, cuerpo any, inicio time.Time) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Latencia-ms", fmt.Sprintf("%.3f", milisegundos(time.Since(inicio))))
	w.WriteHeader(estado)
	_ = json.NewEncoder(w).Encode(cuerpo)
}

func fallo(w http.ResponseWriter, estado int, mensaje string, inicio time.Time) {
	responder(w, estado, errorAPI{Error: mensaje}, inicio)
}

func milisegundos(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

// decodificar lee el cuerpo JSON con límite de tamaño. Un cuerpo vacío es
// válido cuando opcional es true.
func decodificar(w http.ResponseWriter, r *http.Request, destino any, opcional bool) error {
	r.Body = http.MaxBytesReader(w, r.Body, limiteCuerpo)
	err := json.NewDecoder(r.Body).Decode(destino)
	if errors.Is(err, io.EOF) && opcional {
		return nil
	}
	if err != nil {
		return fmt.Errorf("el cuerpo no es un JSON válido: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------- /health

type respuestaSalud struct {
	Estado            string  `json:"estado"`
	ModeloCargado     bool    `json:"modelo_cargado"`
	Variables         int     `json:"variables"`
	Umbral            float64 `json:"umbral"`
	NodosConfigurados int     `json:"nodos_configurados"`
	Entrenando        bool    `json:"entrenando"`
	Segundos          float64 `json:"segundos_activo"`
}

func (s *Servidor) salud(w http.ResponseWriter, r *http.Request) {
	inicio := time.Now()
	modelo, _ := s.modeloActual()
	respuesta := respuestaSalud{
		Estado:            "ok",
		ModeloCargado:     modelo != nil,
		NodosConfigurados: len(s.opciones.Nodos),
		Entrenando:        s.entrenando.Load(),
		Segundos:          time.Since(s.inicio).Seconds(),
	}
	if modelo != nil {
		respuesta.Variables = modelo.NumVariables()
		respuesta.Umbral = modelo.Umbral
	}
	responder(w, http.StatusOK, respuesta, inicio)
}

// --------------------------------------------------------------- /predict

type peticionPrediccion struct {
	Entrada []float64 `json:"entrada"`
}

type respuestaPrediccion struct {
	Probabilidad float64 `json:"probabilidad"`
	Clase        int     `json:"clase"`
	Umbral       float64 `json:"umbral"`
	LatenciaMs   float64 `json:"latencia_ms"`
	Origen       string  `json:"origen"`
	EjecucionID  string  `json:"ejecucion_id,omitempty"`
}

func (s *Servidor) predecir(w http.ResponseWriter, r *http.Request) {
	inicio := time.Now()
	modelo, ejecucionID := s.modeloActual()
	if modelo == nil {
		fallo(w, http.StatusServiceUnavailable, "no hay un modelo cargado: entrena uno con POST /train", inicio)
		return
	}

	var peticion peticionPrediccion
	if err := decodificar(w, r, &peticion, false); err != nil {
		fallo(w, http.StatusBadRequest, err.Error(), inicio)
		return
	}
	if len(peticion.Entrada) != modelo.NumVariables() {
		fallo(w, http.StatusBadRequest,
			fmt.Sprintf("se esperaban %d variables en \"entrada\", llegaron %d", modelo.NumVariables(), len(peticion.Entrada)), inicio)
		return
	}

	hash := almacen.HashEntrada(peticion.Entrada)
	if s.opciones.Cache != nil {
		if acierto, err := s.opciones.Cache.Obtener(r.Context(), hash); err != nil {
			s.log("la caché falló, se calcula la predicción: %v", err)
		} else if acierto != nil {
			s.responderPrediccion(w, inicio, acierto.Probabilidad, acierto.Clase, acierto.Umbral, almacen.OrigenCache, acierto.EjecucionID)
			return
		}
	}

	probabilidad, err := modelo.Probabilidad(peticion.Entrada)
	if err != nil {
		fallo(w, http.StatusBadRequest, err.Error(), inicio)
		return
	}
	clase := 0
	if probabilidad >= modelo.Umbral {
		clase = 1
	}
	latencia := s.responderPrediccion(w, inicio, probabilidad, clase, modelo.Umbral, almacen.OrigenCalculo, ejecucionID)

	prediccion := almacen.Prediccion{
		EjecucionID: ejecucionID, Entrada: peticion.Entrada, HashEntrada: hash,
		Probabilidad: probabilidad, Clase: clase, Umbral: modelo.Umbral,
		LatenciaMs: latencia, Origen: almacen.OrigenCalculo, Fecha: time.Now().UTC(),
	}
	select {
	case s.cola <- prediccion:
	default: // cola llena: se prefiere perder el registro antes que frenar la respuesta
		s.descartadas.Add(1)
	}
}

// responderPrediccion envía la respuesta, registra la latencia y avisa por
// WebSocket. Devuelve la latencia en milisegundos.
func (s *Servidor) responderPrediccion(w http.ResponseWriter, inicio time.Time, probabilidad float64, clase int, umbral float64, origen, ejecucionID string) float64 {
	latencia := milisegundos(time.Since(inicio))
	respuesta := respuestaPrediccion{
		Probabilidad: probabilidad, Clase: clase, Umbral: umbral,
		LatenciaMs: latencia, Origen: origen, EjecucionID: ejecucionID,
	}
	s.latencias.registrar(latencia)
	responder(w, http.StatusOK, respuesta, inicio)
	s.hub.publicar("prediccion", respuesta)
	return latencia
}

// ---------------------------------------------------------------- /metrics

type respuestaMetricas struct {
	Modelo          infoModelo         `json:"modelo"`
	Cluster         infoCluster        `json:"cluster"`
	API             infoAPI            `json:"api"`
	UltimaEjecucion *almacen.Ejecucion `json:"ultima_ejecucion"`
	Metricas        []almacen.Metrica  `json:"metricas"`
	Evaluacion      *logreg.Metricas   `json:"evaluacion,omitempty"`
}

type infoModelo struct {
	Cargado     bool    `json:"cargado"`
	Variables   int     `json:"variables"`
	Umbral      float64 `json:"umbral"`
	EjecucionID string  `json:"ejecucion_id,omitempty"`
}

type infoCluster struct {
	NodosConfigurados int      `json:"nodos_configurados"`
	Nodos             []string `json:"nodos"`
	Entrenando        bool     `json:"entrenando"`
}

type infoAPI struct {
	Latencia             resumenLatencias `json:"latencia"`
	ClientesWS           int              `json:"clientes_ws"`
	PrediccionesPerdidas int64            `json:"predicciones_descartadas"`
	Goroutines           int              `json:"goroutines"`
	CPUs                 int              `json:"cpus"`
	MemoriaMB            float64          `json:"memoria_mb"`
	Segundos             float64          `json:"segundos_activo"`
}

// instantanea reúne las métricas de la API sin consultar la base de datos.
func (s *Servidor) instantanea() infoAPI {
	var memoria runtime.MemStats
	runtime.ReadMemStats(&memoria)
	return infoAPI{
		Latencia:             s.latencias.resumen(),
		ClientesWS:           s.hub.clientes(),
		PrediccionesPerdidas: s.descartadas.Load(),
		Goroutines:           runtime.NumGoroutine(),
		CPUs:                 runtime.NumCPU(),
		MemoriaMB:            float64(memoria.Alloc) / (1 << 20),
		Segundos:             time.Since(s.inicio).Seconds(),
	}
}

func (s *Servidor) metricas(w http.ResponseWriter, r *http.Request) {
	inicio := time.Now()
	modelo, ejecucionID := s.modeloActual()
	s.mu.RLock()
	evaluacion := s.evaluacion
	s.mu.RUnlock()

	respuesta := respuestaMetricas{
		Cluster: infoCluster{
			NodosConfigurados: len(s.opciones.Nodos),
			Nodos:             s.opciones.Nodos,
			Entrenando:        s.entrenando.Load(),
		},
		API:        s.instantanea(),
		Metricas:   []almacen.Metrica{},
		Evaluacion: evaluacion,
	}
	if modelo != nil {
		respuesta.Modelo = infoModelo{Cargado: true, Variables: modelo.NumVariables(), Umbral: modelo.Umbral, EjecucionID: ejecucionID}
	}

	ultimas, err := s.repo.UltimasEjecuciones(r.Context(), 1)
	if err != nil {
		fallo(w, http.StatusInternalServerError, fmt.Sprintf("no se pudieron leer las ejecuciones: %v", err), inicio)
		return
	}
	if len(ultimas) > 0 {
		respuesta.UltimaEjecucion = &ultimas[0]
		metricas, err := s.repo.MetricasDeEjecucion(r.Context(), ultimas[0].ID)
		if err != nil {
			fallo(w, http.StatusInternalServerError, fmt.Sprintf("no se pudieron leer las métricas: %v", err), inicio)
			return
		}
		if metricas != nil {
			respuesta.Metricas = metricas
		}
	}
	responder(w, http.StatusOK, respuesta, inicio)
}
