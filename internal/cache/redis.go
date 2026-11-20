// Package cache implementa el nivel de caché del PC4 sobre Redis: últimas
// predicciones (clave pred:<hash>) y pesos del modelo vigente (modelo:pesos),
// ambos con TTL configurable. Redis nunca debe romper ni frenar a la API: los
// tiempos de espera son cortos y, tras un fallo, un cortacircuitos deja de
// consultar a Redis durante unos segundos (la API sigue con MongoDB o calcula).
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/almeida-aguilar/cs-cc65-proyecto7-vacunacion-ml-2026-20/internal/almacen"
	"github.com/franztoledo/logreg"
	"github.com/redis/go-redis/v9"
)

// Claves y valores por defecto.
const (
	prefijoPrediccion = "pred:"
	clavePesos        = "modelo:pesos"

	TTLPrediccionPorDefecto = 5 * time.Minute
	TTLPesosPorDefecto      = 24 * time.Hour
	SuspensionPorDefecto    = 5 * time.Second
	EsperaPorDefecto        = 100 * time.Millisecond
)

// Opciones configura el cliente. Los valores en cero toman el valor por defecto.
type Opciones struct {
	// TTL de las predicciones en caché.
	TTL time.Duration
	// TTLPesos es la vigencia de los pesos del modelo guardados.
	TTLPesos time.Duration
	// Suspension es cuánto tiempo se deja de consultar a Redis tras un fallo.
	Suspension time.Duration
	// TiempoEspera limita conectar, leer y escribir (mantiene /predict < 100 ms).
	TiempoEspera time.Duration
	// Log recibe los avisos; nil los descarta.
	Log func(formato string, valores ...any)
}

// Redis es la caché sobre Redis. Es seguro para uso concurrente.
type Redis struct {
	cliente    *redis.Client
	ttl        time.Duration
	ttlPesos   time.Duration
	suspension time.Duration
	log        func(string, ...any)

	hasta atomic.Int64 // instante (UnixNano) hasta el que Redis está suspendido
	ahora func() time.Time
}

// NuevoRedis crea el cliente a partir de una URL (redis://host:6379/0). No abre
// la conexión: si Redis está caído al arrancar, la caché se activa cuando vuelva.
func NuevoRedis(url string, opciones Opciones) (*Redis, error) {
	configuracion, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("URL de Redis inválida: %w", err)
	}
	if opciones.TTL <= 0 {
		opciones.TTL = TTLPrediccionPorDefecto
	}
	if opciones.TTLPesos <= 0 {
		opciones.TTLPesos = TTLPesosPorDefecto
	}
	if opciones.Suspension <= 0 {
		opciones.Suspension = SuspensionPorDefecto
	}
	if opciones.TiempoEspera <= 0 {
		opciones.TiempoEspera = EsperaPorDefecto
	}
	configuracion.DialTimeout = opciones.TiempoEspera
	configuracion.ReadTimeout = opciones.TiempoEspera
	configuracion.WriteTimeout = opciones.TiempoEspera
	configuracion.MaxRetries = -1 // sin reintentos: el cortacircuitos decide
	configuracion.MinIdleConns = 4 // conexiones precalentadas: abrirlas en frío cuesta más que el tiempo de espera

	registro := opciones.Log
	if registro == nil {
		registro = func(string, ...any) {}
	}
	return &Redis{
		cliente:    redis.NewClient(configuracion),
		ttl:        opciones.TTL,
		ttlPesos:   opciones.TTLPesos,
		suspension: opciones.Suspension,
		log:        registro,
		ahora:      time.Now,
	}, nil
}

// Ping comprueba la conexión (se usa solo para informar al arrancar).
func (r *Redis) Ping(ctx context.Context) error {
	if err := r.cliente.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("Redis no responde: %w", err)
	}
	return nil
}

// Cerrar libera las conexiones.
func (r *Redis) Cerrar() error { return r.cliente.Close() }

// disponible indica si se puede consultar a Redis (no está suspendido).
func (r *Redis) disponible() bool { return r.ahora().UnixNano() >= r.hasta.Load() }

// suspender abre el cortacircuitos tras un fallo y lo avisa una sola vez.
func (r *Redis) suspender(err error) {
	if r.disponible() {
		r.log("Redis falló, se suspende %v (la API sigue sin caché): %v", r.suspension, err)
	}
	r.hasta.Store(r.ahora().Add(r.suspension).UnixNano())
}

// Obtener devuelve la predicción en caché o (nil, nil) si no hay acierto o
// Redis está suspendido.
func (r *Redis) Obtener(ctx context.Context, hash string) (*almacen.Prediccion, error) {
	if !r.disponible() {
		return nil, nil
	}
	datos, err := r.cliente.Get(ctx, prefijoPrediccion+hash).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		r.suspender(err)
		return nil, fmt.Errorf("no se pudo leer la predicción de Redis: %w", err)
	}
	var prediccion almacen.Prediccion
	if err := json.Unmarshal(datos, &prediccion); err != nil {
		return nil, fmt.Errorf("la predicción guardada en Redis es inválida: %w", err)
	}
	return &prediccion, nil
}

// Guardar escribe la predicción con el TTL configurado.
func (r *Redis) Guardar(ctx context.Context, prediccion *almacen.Prediccion) error {
	if !r.disponible() {
		return nil
	}
	datos, err := json.Marshal(prediccion)
	if err != nil {
		return fmt.Errorf("no se pudo serializar la predicción: %w", err)
	}
	if err := r.cliente.Set(ctx, prefijoPrediccion+prediccion.HashEntrada, datos, r.ttl).Err(); err != nil {
		r.suspender(err)
		return fmt.Errorf("no se pudo guardar la predicción en Redis: %w", err)
	}
	return nil
}

// pesosGuardados es el documento que se guarda en modelo:pesos.
type pesosGuardados struct {
	EjecucionID string         `json:"ejecucion_id"`
	Fecha       time.Time      `json:"fecha"`
	Modelo      *logreg.Modelo `json:"modelo"`
}

// GuardarPesos guarda el modelo vigente (pesos, sesgo, escalador y umbral) para
// que la API lo recupere tras reiniciarse. Usa un TTL propio, más largo.
func (r *Redis) GuardarPesos(ctx context.Context, modelo *logreg.Modelo, ejecucionID string) error {
	if !r.disponible() {
		return nil
	}
	datos, err := json.Marshal(pesosGuardados{EjecucionID: ejecucionID, Fecha: r.ahora().UTC(), Modelo: modelo})
	if err != nil {
		return fmt.Errorf("no se pudo serializar el modelo: %w", err)
	}
	if err := r.cliente.Set(ctx, clavePesos, datos, r.ttlPesos).Err(); err != nil {
		r.suspender(err)
		return fmt.Errorf("no se pudieron guardar los pesos en Redis: %w", err)
	}
	return nil
}

// ObtenerPesos devuelve el modelo guardado y la ejecución que lo produjo, o
// (nil, "", nil) si no hay ninguno.
func (r *Redis) ObtenerPesos(ctx context.Context) (*logreg.Modelo, string, error) {
	if !r.disponible() {
		return nil, "", nil
	}
	datos, err := r.cliente.Get(ctx, clavePesos).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, "", nil
	}
	if err != nil {
		r.suspender(err)
		return nil, "", fmt.Errorf("no se pudieron leer los pesos de Redis: %w", err)
	}
	var guardados pesosGuardados
	if err := json.Unmarshal(datos, &guardados); err != nil || guardados.Modelo == nil {
		return nil, "", fmt.Errorf("los pesos guardados en Redis son inválidos: %v", err)
	}
	return guardados.Modelo, guardados.EjecucionID, nil
}
