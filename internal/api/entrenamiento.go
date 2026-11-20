package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/almeida-aguilar/cs-cc65-proyecto7-vacunacion-ml-2026-20/internal/almacen"
	"github.com/almeida-aguilar/cs-cc65-proyecto7-vacunacion-ml-2026-20/internal/coordinador"
	"github.com/franztoledo/logreg"
)

// peticionEntrenamiento son los hiperparámetros opcionales de POST /train.
// Los campos en cero toman el valor por defecto.
type peticionEntrenamiento struct {
	Epocas       int     `json:"epocas"`
	Lote         int     `json:"lote"`
	Tasa         float64 `json:"tasa"`
	PesoPositivo float64 `json:"peso_positivo"`
	Semilla      int64   `json:"semilla"`
}

type respuestaEntrenamiento struct {
	ID     string `json:"id"`
	Estado string `json:"estado"`
	Nodos  int    `json:"nodos"`
}

// valoresPorDefecto completa los hiperparámetros que no se enviaron.
func (p *peticionEntrenamiento) valoresPorDefecto() {
	if p.Epocas == 0 {
		p.Epocas = 10
	}
	if p.Lote == 0 {
		p.Lote = 1024
	}
	if p.Tasa == 0 {
		p.Tasa = 0.1
	}
	if p.PesoPositivo == 0 {
		p.PesoPositivo = 1
	}
	if p.Semilla == 0 {
		p.Semilla = 42
	}
}

// entrenar atiende POST /train: registra la ejecución, lanza el entrenamiento
// distribuido en una goroutine y responde 202 con el ID. Solo se permite un
// entrenamiento a la vez (409 si ya hay uno).
func (s *Servidor) entrenar(w http.ResponseWriter, r *http.Request) {
	inicio := time.Now()
	var peticion peticionEntrenamiento
	if err := decodificar(w, r, &peticion, true); err != nil {
		fallo(w, http.StatusBadRequest, err.Error(), inicio)
		return
	}
	peticion.valoresPorDefecto()
	if peticion.Epocas < 0 || peticion.Lote < 0 || peticion.Tasa < 0 || peticion.PesoPositivo < 0 {
		fallo(w, http.StatusBadRequest, "los hiperparámetros no pueden ser negativos", inicio)
		return
	}
	if len(s.opciones.Nodos) == 0 {
		fallo(w, http.StatusServiceUnavailable, "no hay nodos ML configurados (flag -nodos)", inicio)
		return
	}
	if !s.entrenando.CompareAndSwap(false, true) {
		fallo(w, http.StatusConflict, "ya hay un entrenamiento en curso", inicio)
		return
	}

	opciones := coordinador.Opciones{
		Direcciones:       s.opciones.Nodos,
		Epocas:            peticion.Epocas,
		TamanoLote:        peticion.Lote,
		TasaAprendizaje:   peticion.Tasa,
		PesoClasePositiva: peticion.PesoPositivo,
		Semilla:           peticion.Semilla,
		MinimoNodos:       1,
		Reintentos:        3,
		EsperaReintento:   time.Second,
		TiempoEspera:      30 * time.Second,
		Silencioso:        true,
	}
	servidor, err := coordinador.Nuevo(opciones)
	if err != nil {
		s.entrenando.Store(false)
		fallo(w, http.StatusBadRequest, err.Error(), inicio)
		return
	}

	ejecucion := &almacen.Ejecucion{
		Estado:        almacen.EstadoEnCurso,
		Configuracion: almacen.ConfiguracionDesde(servidor.Configuracion),
		Nodos:         len(s.opciones.Nodos),
		Inicio:        time.Now().UTC(),
	}
	if err := s.repo.InsertarEjecucion(r.Context(), ejecucion); err != nil {
		s.entrenando.Store(false)
		fallo(w, http.StatusInternalServerError, fmt.Sprintf("no se pudo registrar la ejecución: %v", err), inicio)
		return
	}

	s.trabajos.Add(1)
	go s.ejecutarEntrenamiento(servidor, ejecucion)
	responder(w, http.StatusAccepted, respuestaEntrenamiento{
		ID: ejecucion.ID, Estado: ejecucion.Estado, Nodos: ejecucion.Nodos,
	}, inicio)
}

// ejecutarEntrenamiento corre el parameter server, guarda una métrica por
// época (y la difunde por WebSocket) y al terminar reemplaza el modelo vigente.
func (s *Servidor) ejecutarEntrenamiento(servidor *coordinador.Servidor, ejecucion *almacen.Ejecucion) {
	defer s.trabajos.Done()
	defer s.entrenando.Store(false)

	terminarConError := func(causa error) {
		s.log("entrenamiento %s fallido: %v", ejecucion.ID, causa)
		ejecucion.Estado = almacen.EstadoFallida
		ejecucion.Error = causa.Error()
		ejecucion.Fin = time.Now().UTC()
		ejecucion.Segundos = ejecucion.Fin.Sub(ejecucion.Inicio).Seconds()
		if err := s.repo.ActualizarEjecucion(s.ctx, ejecucion); err != nil {
			s.log("no se pudo actualizar la ejecución %s: %v", ejecucion.ID, err)
		}
		s.hub.publicar("error", ejecucion)
	}

	servidor.Configuracion.AlProgreso = func(progreso logreg.Progreso) {
		metrica := &almacen.Metrica{
			EjecucionID: ejecucion.ID,
			Epoca:       progreso.Epoca,
			Perdida:     progreso.Perdida,
			Nodos:       progreso.Nodos,
			DuracionMs:  milisegundos(progreso.Duracion),
			Validacion:  progreso.Validacion,
			EsMejor:     progreso.EsMejor,
			Fecha:       time.Now().UTC(),
		}
		if err := s.repo.InsertarMetrica(s.ctx, metrica); err != nil {
			s.log("no se pudo guardar la métrica de la época %d: %v", progreso.Epoca, err)
		}
		s.hub.publicar("progreso", metrica)
	}

	s.hub.publicar("inicio", ejecucion)
	if err := servidor.Conectar(); err != nil {
		terminarConError(fmt.Errorf("no se pudo conectar con los nodos: %w", err))
		return
	}
	defer servidor.Cerrar()
	ejecucion.Nodos = servidor.NodosActivos()
	ejecucion.FilasTotales = servidor.FilasTotales()

	resultado, err := servidor.Entrenar(s.ctx)
	if err != nil {
		terminarConError(fmt.Errorf("falló el entrenamiento: %w", err))
		return
	}
	evaluacion, err := servidor.Evaluar(resultado.Modelo)
	if err != nil {
		terminarConError(fmt.Errorf("falló la evaluación en los nodos: %w", err))
		return
	}

	s.mu.Lock()
	s.modelo = resultado.Modelo
	s.ejecucionModelo = ejecucion.ID
	s.evaluacion = evaluacion
	s.mu.Unlock()
	if s.opciones.RutaModelo != "" {
		if err := resultado.Modelo.Guardar(s.opciones.RutaModelo); err != nil {
			s.log("no se pudo guardar el modelo en %s: %v", s.opciones.RutaModelo, err)
		}
	}

	ejecucion.Estado = almacen.EstadoTerminada
	ejecucion.MejorEpoca = resultado.MejorEpoca
	ejecucion.Fin = time.Now().UTC()
	ejecucion.Segundos = resultado.DuracionTotal.Seconds()
	if err := s.repo.ActualizarEjecucion(s.ctx, ejecucion); err != nil {
		s.log("no se pudo actualizar la ejecución %s: %v", ejecucion.ID, err)
	}
	s.hub.publicar("fin", map[string]any{"ejecucion": ejecucion, "evaluacion": evaluacion})
}
