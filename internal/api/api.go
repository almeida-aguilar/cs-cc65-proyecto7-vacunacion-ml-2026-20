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

// CachePesos es la parte opcional de la caché que guarda el modelo vigente
// (pesos globales). Si la Cache la implementa, la API guarda el modelo al
// terminar cada entrenamiento y lo recupera al arrancar sin modelo.
type CachePesos interface {
	GuardarPesos(ctx context.Context, modelo *logreg.Modelo, ejecucionID string) error
	ObtenerPesos(ctx context.Context) (*logreg.Modelo, string, error)
}

// PesoPositivoPC3 es el peso de la clase positiva con el que se entrenó el
// modelo del PC3 (negativos/positivos del conjunto de entrenamiento).
const PesoPositivoPC3 = 12.43

// DefectosEntrenamiento son los hiperparámetros de POST /train cuando la
// petición no los indica. Los valores en cero usan los de fábrica.
type DefectosEntrenamiento struct {
	Epocas       int
	Lote         int
	Tasa         float64
	PesoPositivo float64
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
	// Cache es opcional (Redis). Si además implementa CachePesos, guarda el modelo.
	Cache Cache
	// Entrenamiento fija los valores por defecto de POST /train.
	Entrenamiento DefectosEntrenamiento
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
	origenes    [3]atomic.Int64 // predicciones por origen: cálculo, caché, MongoDB
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

	servidor.restaurarModeloDesdeCache()
	servidor.trabajos.Add(1)
	go servidor.persistirPredicciones()
	return servidor, nil
}

// restaurarModeloDesdeCache recupera los pesos guardados en la caché cuando no
// se cargó ningún modelo desde archivo (p. ej. tras reiniciar el contenedor).
func (s *Servidor) restaurarModeloDesdeCache() {
	pesos, ok := s.opciones.Cache.(CachePesos)
	if !ok || s.modelo != nil {
		return
	}
	ctx, cancelar := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelar()
	modelo, ejecucionID, err := pesos.ObtenerPesos(ctx)
	if err != nil {
		s.log("no se pudo recuperar el modelo de la caché: %v", err)
		return
	}
	if modelo != nil {
		s.modelo, s.ejecucionModelo = modelo, ejecucionID
		s.log("modelo recuperado de la caché (ejecución %s, %d variables)", ejecucionID, modelo.NumVariables())
	}
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

// maxFilasLote limita el tamaño de un lote en POST /predict.
const maxFilasLote = 1000

// peticionPrediccion admite una persona ("entrada") o varias ("filas", con
// "columnas" opcional para comprobar que el orden de variables es el esperado).
type peticionPrediccion struct {
	Entrada  []float64   `json:"entrada"`
	Filas    [][]float64 `json:"filas"`
	Columnas []string    `json:"columnas"`
}

type respuestaPrediccion struct {
	Probabilidad float64 `json:"probabilidad"`
	Clase        int     `json:"clase"`
	Umbral       float64 `json:"umbral"`
	LatenciaMs   float64 `json:"latencia_ms"`
	Origen       string  `json:"origen"`
	EjecucionID  string  `json:"ejecucion_id,omitempty"`
}

// prediccionLote es el resultado de una fila de un lote, en el mismo orden.
type prediccionLote struct {
	Probabilidad float64 `json:"probabilidad"`
	Clase        int     `json:"clase"`
	Origen       string  `json:"origen"`
}

// respuestaLote es la respuesta de POST /predict cuando se envían "filas".
type respuestaLote struct {
	Predicciones []prediccionLote `json:"predicciones"`
	Total        int              `json:"total"`
	Positivas    int              `json:"positivas"`
	Umbral       float64          `json:"umbral"`
	LatenciaMs   float64          `json:"latencia_ms"`
	EjecucionID  string           `json:"ejecucion_id,omitempty"`
}

// validar comprueba la forma de la petición contra el modelo vigente.
func (p *peticionPrediccion) validar(variables int) error {
	if p.Entrada != nil && p.Filas != nil {
		return fmt.Errorf("envía \"entrada\" (una persona) o \"filas\" (varias), no ambos")
	}
	if p.Columnas != nil && len(p.Columnas) != variables {
		return fmt.Errorf("\"columnas\" tiene %d nombres y el modelo espera %d variables", len(p.Columnas), variables)
	}
	if p.Filas == nil {
		if len(p.Entrada) != variables {
			return fmt.Errorf("se esperaban %d variables en \"entrada\", llegaron %d", variables, len(p.Entrada))
		}
		return nil
	}
	if len(p.Filas) == 0 {
		return fmt.Errorf("\"filas\" está vacío")
	}
	if len(p.Filas) > maxFilasLote {
		return fmt.Errorf("\"filas\" tiene %d filas y el máximo por petición es %d", len(p.Filas), maxFilasLote)
	}
	for indice, fila := range p.Filas {
		if len(fila) != variables {
			return fmt.Errorf("la fila %d tiene %d variables, se esperaban %d", indice+1, len(fila), variables)
		}
	}
	return nil
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
	if err := peticion.validar(modelo.NumVariables()); err != nil {
		fallo(w, http.StatusBadRequest, err.Error(), inicio)
		return
	}
	if peticion.Filas != nil {
		s.predecirLote(w, inicio, modelo, ejecucionID, peticion.Filas)
		return
	}

	hash := almacen.HashEntrada(peticion.Entrada)
	if previa := s.buscarPrevia(r.Context(), hash, ejecucionID); previa != nil {
		s.responderPrediccion(w, inicio, previa.Probabilidad, previa.Clase, previa.Umbral, previa.Origen, previa.EjecucionID)
		return
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
	s.encolar(almacen.Prediccion{
		EjecucionID: ejecucionID, Entrada: peticion.Entrada, HashEntrada: hash,
		Probabilidad: probabilidad, Clase: clase, Umbral: modelo.Umbral,
		LatenciaMs: latencia, Origen: almacen.OrigenCalculo, Fecha: time.Now().UTC(),
	})
}

// buscarPrevia busca una predicción ya calculada para la misma entrada: primero
// en la caché (Redis) y, si no está, en MongoDB (fallback). Solo sirve las del
// modelo vigente: tras reentrenar, las anteriores se ignoran. Si hay un acierto
// en MongoDB se vuelve a guardar en la caché. Los fallos de Redis o de MongoDB
// se registran y nunca impiden calcular la predicción.
func (s *Servidor) buscarPrevia(ctx context.Context, hash, ejecucionID string) *almacen.Prediccion {
	if s.opciones.Cache == nil {
		return nil
	}
	acierto, err := s.opciones.Cache.Obtener(ctx, hash)
	if err != nil {
		s.log("la caché falló, se consulta MongoDB: %v", err)
	}
	if acierto != nil && acierto.EjecucionID == ejecucionID {
		acierto.Origen = almacen.OrigenCache
		return acierto
	}

	guardada, err := s.repo.PrediccionPorHash(ctx, hash)
	switch {
	case errors.Is(err, almacen.ErrNoEncontrado):
		return nil
	case err != nil:
		s.log("el fallback a MongoDB falló, se calcula la predicción: %v", err)
		return nil
	case guardada.EjecucionID != ejecucionID:
		return nil
	}
	guardada.Origen = almacen.OrigenMongo
	if err := s.opciones.Cache.Guardar(ctx, guardada); err != nil {
		s.log("no se pudo repoblar la caché: %v", err)
	}
	return guardada
}

// contarOrigen suma n predicciones al contador del origen indicado.
func (s *Servidor) contarOrigen(origen string, n int64) {
	switch origen {
	case almacen.OrigenCache:
		s.origenes[1].Add(n)
	case almacen.OrigenMongo:
		s.origenes[2].Add(n)
	default:
		s.origenes[0].Add(n)
	}
}

// encolar envía la predicción a la cola de persistencia sin bloquear: si la
// cola está llena se prefiere perder el registro antes que frenar la respuesta.
func (s *Servidor) encolar(prediccion almacen.Prediccion) {
	select {
	case s.cola <- prediccion:
	default:
		s.descartadas.Add(1)
	}
}

// predecirLote calcula las probabilidades de todas las filas con
// Modelo.ProbabilidadLote, que reparte el trabajo entre goroutines (cada una
// escribe en su propio rango de la salida, sin mutex). La caché no se consulta
// en los lotes: calcular es más barato que una ida y vuelta por fila.
func (s *Servidor) predecirLote(w http.ResponseWriter, inicio time.Time, modelo *logreg.Modelo, ejecucionID string, filas [][]float64) {
	probabilidades, err := modelo.ProbabilidadLote(filas, 0)
	if err != nil {
		fallo(w, http.StatusBadRequest, err.Error(), inicio)
		return
	}
	respuesta := respuestaLote{
		Predicciones: make([]prediccionLote, len(filas)),
		Total:        len(filas),
		Umbral:       modelo.Umbral,
		EjecucionID:  ejecucionID,
	}
	ahora := time.Now().UTC()
	for indice, probabilidad := range probabilidades {
		clase := 0
		if probabilidad >= modelo.Umbral {
			clase = 1
			respuesta.Positivas++
		}
		respuesta.Predicciones[indice] = prediccionLote{Probabilidad: probabilidad, Clase: clase, Origen: almacen.OrigenCalculo}
	}
	respuesta.LatenciaMs = milisegundos(time.Since(inicio))
	s.latencias.registrar(respuesta.LatenciaMs)
	s.contarOrigen(almacen.OrigenCalculo, int64(len(filas)))
	responder(w, http.StatusOK, respuesta, inicio)
	s.hub.publicar("prediccion_lote", map[string]any{
		"total": respuesta.Total, "positivas": respuesta.Positivas,
		"umbral": respuesta.Umbral, "latencia_ms": respuesta.LatenciaMs,
	})

	for indice, fila := range filas {
		s.encolar(almacen.Prediccion{
			EjecucionID: ejecucionID, Entrada: fila, HashEntrada: almacen.HashEntrada(fila),
			Probabilidad: probabilidades[indice], Clase: respuesta.Predicciones[indice].Clase,
			Umbral: modelo.Umbral, LatenciaMs: respuesta.LatenciaMs / float64(len(filas)),
			Origen: almacen.OrigenCalculo, Fecha: ahora,
		})
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
	s.contarOrigen(origen, 1)
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

// origenesPredicciones cuenta de dónde salió cada predicción servida.
type origenesPredicciones struct {
	Calculadas int64 `json:"calculadas"`
	Cache      int64 `json:"cache"`
	MongoDB    int64 `json:"mongo"`
}

type infoAPI struct {
	CacheActiva          bool                 `json:"cache_activa"`
	Origenes             origenesPredicciones `json:"origenes"`
	Latencia             resumenLatencias     `json:"latencia"`
	ClientesWS           int                  `json:"clientes_ws"`
	PrediccionesPerdidas int64                `json:"predicciones_descartadas"`
	Goroutines           int                  `json:"goroutines"`
	CPUs                 int                  `json:"cpus"`
	MemoriaMB            float64              `json:"memoria_mb"`
	Segundos             float64              `json:"segundos_activo"`
}

// instantanea reúne las métricas de la API sin consultar la base de datos.
func (s *Servidor) instantanea() infoAPI {
	var memoria runtime.MemStats
	runtime.ReadMemStats(&memoria)
	return infoAPI{
		CacheActiva: s.opciones.Cache != nil,
		Origenes: origenesPredicciones{
			Calculadas: s.origenes[0].Load(), Cache: s.origenes[1].Load(), MongoDB: s.origenes[2].Load(),
		},
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
