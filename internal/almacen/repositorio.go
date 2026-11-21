package almacen

import (
	"context"
	"errors"
)

// ErrNoEncontrado se devuelve cuando un documento buscado no existe.
var ErrNoEncontrado = errors.New("documento no encontrado")

// Repositorio es el contrato que usan la API (#29) y la caché (#31). Todas las
// implementaciones deben ser seguras para uso concurrente. Los identificadores
// vacíos se generan al insertar y quedan escritos en el documento recibido.
type Repositorio interface {
	// InsertarEjecucion guarda una nueva ejecución y asigna su ID si viene vacío.
	InsertarEjecucion(ctx context.Context, ejecucion *Ejecucion) error
	// ActualizarEjecucion reemplaza una ejecución existente (por ID).
	ActualizarEjecucion(ctx context.Context, ejecucion *Ejecucion) error
	// ObtenerEjecucion devuelve una ejecución o ErrNoEncontrado.
	ObtenerEjecucion(ctx context.Context, id string) (*Ejecucion, error)
	// UltimasEjecuciones devuelve las más recientes primero.
	UltimasEjecuciones(ctx context.Context, limite int) ([]Ejecucion, error)

	// InsertarMetrica guarda la métrica de una época.
	InsertarMetrica(ctx context.Context, metrica *Metrica) error
	// MetricasDeEjecucion devuelve las métricas de una ejecución ordenadas por época.
	MetricasDeEjecucion(ctx context.Context, ejecucionID string) ([]Metrica, error)

	// InsertarPrediccion guarda una predicción servida.
	InsertarPrediccion(ctx context.Context, prediccion *Prediccion) error
	// UltimasPredicciones devuelve las más recientes primero.
	UltimasPredicciones(ctx context.Context, limite int) ([]Prediccion, error)
	// PrediccionPorHash devuelve la predicción más reciente de una entrada
	// (fallback de la caché) o ErrNoEncontrado.
	PrediccionPorHash(ctx context.Context, hash string) (*Prediccion, error)

	// Cerrar libera los recursos de la conexión.
	Cerrar(ctx context.Context) error
}
