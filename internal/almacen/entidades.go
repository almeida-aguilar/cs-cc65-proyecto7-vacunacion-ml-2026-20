// Package almacen define las entidades que persiste el sistema (ejecuciones de
// entrenamiento, métricas por época y predicciones) y el repositorio que las
// guarda. Hay dos implementaciones: MongoDB (nivel persistente del PC4) y una
// en memoria, usada en las pruebas para no depender de servicios externos.
package almacen

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"math"
	"time"

	"github.com/franztoledo/logreg"
)

// Nombres de las colecciones de MongoDB.
const (
	ColeccionEjecucion  = "ejecucion"
	ColeccionMetrica    = "metrica"
	ColeccionPrediccion = "prediccion"
)

// Estados posibles de una ejecución de entrenamiento.
const (
	EstadoEnCurso   = "en_curso"
	EstadoTerminada = "terminada"
	EstadoFallida   = "fallida"
)

// Origen de una predicción: de dónde salió la respuesta.
const (
	OrigenCalculo = "calculo"
	OrigenCache   = "cache"
	OrigenMongo   = "mongo"
)

// ConfiguracionEntrenamiento es la parte persistible de logreg.Configuracion.
// No se guarda la configuración original porque incluye un conjunto de datos y
// una función de progreso, que no se pueden serializar a BSON ni a JSON.
type ConfiguracionEntrenamiento struct {
	TasaAprendizaje   float64 `bson:"tasa_aprendizaje" json:"tasa_aprendizaje"`
	Decaimiento       float64 `bson:"decaimiento" json:"decaimiento"`
	Epocas            int     `bson:"epocas" json:"epocas"`
	TamanoLote        int     `bson:"tamano_lote" json:"tamano_lote"`
	L2                float64 `bson:"l2" json:"l2"`
	PesoClasePositiva float64 `bson:"peso_clase_positiva" json:"peso_clase_positiva"`
	Semilla           int64   `bson:"semilla" json:"semilla"`
	Tolerancia        float64 `bson:"tolerancia" json:"tolerancia"`
	Paciencia         int     `bson:"paciencia" json:"paciencia"`
	MejoraMinima      float64 `bson:"mejora_minima" json:"mejora_minima"`
}

// ConfiguracionDesde extrae los campos persistibles de la configuración de logreg.
func ConfiguracionDesde(cfg logreg.Configuracion) ConfiguracionEntrenamiento {
	return ConfiguracionEntrenamiento{
		TasaAprendizaje:   cfg.TasaAprendizaje,
		Decaimiento:       cfg.Decaimiento,
		Epocas:            cfg.Epocas,
		TamanoLote:        cfg.TamanoLote,
		L2:                cfg.L2,
		PesoClasePositiva: cfg.PesoClasePositiva,
		Semilla:           cfg.Semilla,
		Tolerancia:        cfg.Tolerancia,
		Paciencia:         cfg.Paciencia,
		MejoraMinima:      cfg.MejoraMinima,
	}
}

// Ejecucion representa una corrida de entrenamiento distribuido.
type Ejecucion struct {
	ID            string                     `bson:"_id" json:"id"`
	Estado        string                     `bson:"estado" json:"estado"`
	Configuracion ConfiguracionEntrenamiento `bson:"configuracion" json:"configuracion"`
	Nodos         int                        `bson:"nodos" json:"nodos"`
	FilasTotales  int                        `bson:"filas_totales" json:"filas_totales"`
	MejorEpoca    int                        `bson:"mejor_epoca" json:"mejor_epoca"`
	Segundos      float64                    `bson:"segundos" json:"segundos"`
	Error         string                     `bson:"error,omitempty" json:"error,omitempty"`
	Inicio        time.Time                  `bson:"inicio" json:"inicio"`
	Fin           time.Time                  `bson:"fin,omitempty" json:"fin,omitempty"`
}

// Metrica es el resultado de una época de una ejecución. Alimenta /metrics y
// el WebSocket de progreso.
type Metrica struct {
	ID          string           `bson:"_id" json:"id"`
	EjecucionID string           `bson:"ejecucion_id" json:"ejecucion_id"`
	Epoca       int              `bson:"epoca" json:"epoca"`
	Perdida     float64          `bson:"perdida" json:"perdida"`
	Nodos       int              `bson:"nodos" json:"nodos"`
	DuracionMs  float64          `bson:"duracion_ms" json:"duracion_ms"`
	Validacion  *logreg.Metricas `bson:"validacion,omitempty" json:"validacion,omitempty"`
	EsMejor     bool             `bson:"es_mejor" json:"es_mejor"`
	Fecha       time.Time        `bson:"fecha" json:"fecha"`
}

// Prediccion guarda una predicción individual servida por la API.
type Prediccion struct {
	ID           string    `bson:"_id" json:"id"`
	EjecucionID  string    `bson:"ejecucion_id" json:"ejecucion_id"`
	Entrada      []float64 `bson:"entrada" json:"entrada"`
	HashEntrada  string    `bson:"hash_entrada" json:"hash_entrada"`
	Probabilidad float64   `bson:"probabilidad" json:"probabilidad"`
	Clase        int       `bson:"clase" json:"clase"`
	Umbral       float64   `bson:"umbral" json:"umbral"`
	LatenciaMs   float64   `bson:"latencia_ms" json:"latencia_ms"`
	Origen       string    `bson:"origen" json:"origen"`
	Fecha        time.Time `bson:"fecha" json:"fecha"`
}

// HashEntrada calcula el SHA-256 (en hexadecimal) del vector de entrada. Es la
// clave de búsqueda en MongoDB y la clave de caché en Redis.
func HashEntrada(entrada []float64) string {
	suma := sha256.New()
	var bytes [8]byte
	for _, valor := range entrada {
		binary.LittleEndian.PutUint64(bytes[:], math.Float64bits(valor))
		suma.Write(bytes[:])
	}
	return hex.EncodeToString(suma.Sum(nil))
}
