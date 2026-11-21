package almacen

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
)

// Memoria es un Repositorio en memoria para pruebas. Un mutex de lectura y
// escritura protege los mapas; los IDs se generan con un contador atómico.
type Memoria struct {
	mu           sync.RWMutex
	ejecuciones  map[string]Ejecucion
	metricas     []Metrica
	predicciones []Prediccion
	contador     atomic.Int64
}

// NuevaMemoria crea un repositorio vacío.
func NuevaMemoria() *Memoria {
	return &Memoria{ejecuciones: map[string]Ejecucion{}}
}

func (m *Memoria) nuevoID(prefijo string) string {
	return fmt.Sprintf("%s-%06d", prefijo, m.contador.Add(1))
}

// InsertarEjecucion implementa Repositorio.
func (m *Memoria) InsertarEjecucion(_ context.Context, ejecucion *Ejecucion) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ejecucion.ID == "" {
		ejecucion.ID = m.nuevoID("ej")
	}
	if _, existe := m.ejecuciones[ejecucion.ID]; existe {
		return fmt.Errorf("no se pudo insertar la ejecución %q: ya existe", ejecucion.ID)
	}
	m.ejecuciones[ejecucion.ID] = *ejecucion
	return nil
}

// ActualizarEjecucion implementa Repositorio.
func (m *Memoria) ActualizarEjecucion(_ context.Context, ejecucion *Ejecucion) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, existe := m.ejecuciones[ejecucion.ID]; !existe {
		return fmt.Errorf("no se pudo actualizar la ejecución %q: %w", ejecucion.ID, ErrNoEncontrado)
	}
	m.ejecuciones[ejecucion.ID] = *ejecucion
	return nil
}

// ObtenerEjecucion implementa Repositorio.
func (m *Memoria) ObtenerEjecucion(_ context.Context, id string) (*Ejecucion, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ejecucion, existe := m.ejecuciones[id]
	if !existe {
		return nil, fmt.Errorf("no se pudo leer la ejecución %q: %w", id, ErrNoEncontrado)
	}
	return &ejecucion, nil
}

// UltimasEjecuciones implementa Repositorio.
func (m *Memoria) UltimasEjecuciones(_ context.Context, limite int) ([]Ejecucion, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	lista := make([]Ejecucion, 0, len(m.ejecuciones))
	for _, ejecucion := range m.ejecuciones {
		lista = append(lista, ejecucion)
	}
	sort.Slice(lista, func(i, j int) bool { return lista[i].Inicio.After(lista[j].Inicio) })
	return recortar(lista, limite), nil
}

// InsertarMetrica implementa Repositorio.
func (m *Memoria) InsertarMetrica(_ context.Context, metrica *Metrica) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existente := range m.metricas {
		if existente.EjecucionID == metrica.EjecucionID && existente.Epoca == metrica.Epoca {
			return fmt.Errorf("no se pudo insertar la métrica: la época %d de %q ya existe",
				metrica.Epoca, metrica.EjecucionID)
		}
	}
	if metrica.ID == "" {
		metrica.ID = m.nuevoID("me")
	}
	m.metricas = append(m.metricas, *metrica)
	return nil
}

// MetricasDeEjecucion implementa Repositorio.
func (m *Memoria) MetricasDeEjecucion(_ context.Context, ejecucionID string) ([]Metrica, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var lista []Metrica
	for _, metrica := range m.metricas {
		if metrica.EjecucionID == ejecucionID {
			lista = append(lista, metrica)
		}
	}
	sort.Slice(lista, func(i, j int) bool { return lista[i].Epoca < lista[j].Epoca })
	return lista, nil
}

// InsertarPrediccion implementa Repositorio.
func (m *Memoria) InsertarPrediccion(_ context.Context, prediccion *Prediccion) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if prediccion.ID == "" {
		prediccion.ID = m.nuevoID("pr")
	}
	m.predicciones = append(m.predicciones, *prediccion)
	return nil
}

// UltimasPredicciones implementa Repositorio.
func (m *Memoria) UltimasPredicciones(_ context.Context, limite int) ([]Prediccion, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	lista := append([]Prediccion(nil), m.predicciones...)
	sort.SliceStable(lista, func(i, j int) bool { return lista[i].Fecha.After(lista[j].Fecha) })
	return recortar(lista, limite), nil
}

// PrediccionPorHash implementa Repositorio.
func (m *Memoria) PrediccionPorHash(_ context.Context, hash string) (*Prediccion, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var mejor *Prediccion
	for indice := range m.predicciones {
		candidata := &m.predicciones[indice]
		if candidata.HashEntrada == hash && (mejor == nil || candidata.Fecha.After(mejor.Fecha)) {
			mejor = candidata
		}
	}
	if mejor == nil {
		return nil, fmt.Errorf("no se encontró predicción para el hash %q: %w", hash, ErrNoEncontrado)
	}
	copia := *mejor
	return &copia, nil
}

// Cerrar implementa Repositorio; no hay recursos que liberar.
func (m *Memoria) Cerrar(context.Context) error { return nil }

// recortar limita la lista a limite elementos (limite <= 0 devuelve todo).
func recortar[T any](lista []T, limite int) []T {
	if limite > 0 && len(lista) > limite {
		return lista[:limite]
	}
	return lista
}
