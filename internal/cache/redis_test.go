package cache

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/almeida-aguilar/cs-cc65-proyecto7-vacunacion-ml-2026-20/internal/almacen"
	"github.com/franztoledo/logreg"
)

// nuevoDePrueba crea una caché sobre un Redis en memoria (miniredis), sin
// servicios externos. Si REDIS_URL está definida, la prueba usa ese Redis real.
func nuevoDePrueba(t *testing.T, opciones Opciones) (*Redis, *miniredis.Miniredis) {
	t.Helper()
	if url := os.Getenv("REDIS_URL"); url != "" {
		cache, err := NuevoRedis(url, opciones)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cache.Cerrar() })
		limpiarClaves(t, cache)
		return cache, nil
	}
	servidor := miniredis.RunT(t)
	cache, err := NuevoRedis("redis://"+servidor.Addr()+"/0", opciones)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Cerrar() })
	return cache, servidor
}

// limpiarClaves borra solo las claves que usa la caché, para que una corrida
// contra un Redis real no dependa de datos de corridas anteriores.
func limpiarClaves(t *testing.T, cache *Redis) {
	t.Helper()
	ctx := context.Background()
	claves, err := cache.cliente.Keys(ctx, prefijoPrediccion+"*").Result()
	if err != nil {
		t.Fatalf("no se pudo listar las claves: %v", err)
	}
	claves = append(claves, clavePesos)
	if err := cache.cliente.Del(ctx, claves...).Err(); err != nil {
		t.Fatalf("no se pudo limpiar Redis: %v", err)
	}
}

func prediccionDePrueba(entrada ...float64) *almacen.Prediccion {
	return &almacen.Prediccion{
		ID: "p1", EjecucionID: "ej-1", Entrada: entrada, HashEntrada: almacen.HashEntrada(entrada),
		Probabilidad: 0.73, Clase: 1, Umbral: 0.48, Origen: almacen.OrigenCalculo,
		Fecha: time.Now().UTC().Truncate(time.Millisecond),
	}
}

func TestGuardarYObtenerPrediccion(t *testing.T) {
	cache, _ := nuevoDePrueba(t, Opciones{})
	ctx := context.Background()
	prediccion := prediccionDePrueba(1, 2, 3)

	if acierto, err := cache.Obtener(ctx, prediccion.HashEntrada); err != nil || acierto != nil {
		t.Fatalf("antes de guardar no debe haber acierto: %v, %v", acierto, err)
	}
	if err := cache.Guardar(ctx, prediccion); err != nil {
		t.Fatalf("Guardar: %v", err)
	}
	acierto, err := cache.Obtener(ctx, prediccion.HashEntrada)
	if err != nil || acierto == nil {
		t.Fatalf("Obtener: %v, %v", acierto, err)
	}
	if acierto.Probabilidad != 0.73 || acierto.Clase != 1 || acierto.EjecucionID != "ej-1" || len(acierto.Entrada) != 3 {
		t.Errorf("predicción recuperada incorrecta: %+v", acierto)
	}
}

func TestLaPrediccionExpiraConElTTL(t *testing.T) {
	cache, servidor := nuevoDePrueba(t, Opciones{TTL: 30 * time.Second})
	if servidor == nil {
		t.Skip("la prueba de expiración usa miniredis")
	}
	ctx := context.Background()
	prediccion := prediccionDePrueba(4, 5)
	_ = cache.Guardar(ctx, prediccion)

	if ttl := servidor.TTL(prefijoPrediccion + prediccion.HashEntrada); ttl != 30*time.Second {
		t.Errorf("TTL %v, se esperaban 30s", ttl)
	}
	servidor.FastForward(31 * time.Second)
	if acierto, _ := cache.Obtener(ctx, prediccion.HashEntrada); acierto != nil {
		t.Error("la predicción debía haber expirado")
	}
}

func TestGuardarYObtenerPesos(t *testing.T) {
	cache, servidor := nuevoDePrueba(t, Opciones{TTLPesos: time.Hour})
	ctx := context.Background()

	if modelo, id, err := cache.ObtenerPesos(ctx); err != nil || modelo != nil || id != "" {
		t.Fatalf("sin pesos guardados debe devolver nil: %v, %q, %v", modelo, id, err)
	}
	modelo := logreg.NuevoModelo(3)
	modelo.Pesos = []float64{0.5, -1.25, 2}
	modelo.Sesgo = -0.9
	modelo.Umbral = 0.48
	modelo.Escalador = &logreg.Escalador{Medias: []float64{1, 2, 3}, Desviaciones: []float64{1, 1, 2}}
	if err := cache.GuardarPesos(ctx, modelo, "ej-7"); err != nil {
		t.Fatalf("GuardarPesos: %v", err)
	}

	recuperado, id, err := cache.ObtenerPesos(ctx)
	if err != nil || recuperado == nil || id != "ej-7" {
		t.Fatalf("ObtenerPesos: %v, %q, %v", recuperado, id, err)
	}
	original, _ := modelo.Probabilidad([]float64{1, 2, 3})
	igual, _ := recuperado.Probabilidad([]float64{1, 2, 3})
	if original != igual || recuperado.Umbral != 0.48 {
		t.Errorf("el modelo recuperado predice distinto: %v vs %v (umbral %v)", original, igual, recuperado.Umbral)
	}
	if servidor != nil {
		if ttl := servidor.TTL(clavePesos); ttl != time.Hour {
			t.Errorf("TTL de pesos %v, se esperaba 1h", ttl)
		}
	}
}

// Si Redis cae, la primera llamada devuelve error y las siguientes se saltan a
// Redis sin esperar (cortacircuitos), de modo que /predict no se frena.
func TestCortacircuitosSiRedisCae(t *testing.T) {
	servidor := miniredis.RunT(t)
	var mu sync.Mutex
	avisos := 0
	cache, err := NuevoRedis("redis://"+servidor.Addr()+"/0", Opciones{
		Suspension: time.Minute, TiempoEspera: 50 * time.Millisecond,
		Log: func(string, ...any) { mu.Lock(); avisos++; mu.Unlock() },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Cerrar()
	ctx := context.Background()
	servidor.Close() // Redis cae

	if _, err := cache.Obtener(ctx, "x"); err == nil {
		t.Fatal("la primera consulta con Redis caído debía fallar")
	}
	inicio := time.Now()
	for intento := 0; intento < 100; intento++ {
		if acierto, err := cache.Obtener(ctx, "x"); acierto != nil || err != nil {
			t.Fatalf("suspendido: se esperaba (nil, nil), llegó %v, %v", acierto, err)
		}
		if err := cache.Guardar(ctx, prediccionDePrueba(1)); err != nil {
			t.Fatalf("suspendido: Guardar no debe fallar: %v", err)
		}
		if _, _, err := cache.ObtenerPesos(ctx); err != nil {
			t.Fatalf("suspendido: ObtenerPesos no debe fallar: %v", err)
		}
	}
	if espera := time.Since(inicio); espera > 20*time.Millisecond {
		t.Errorf("200+ llamadas suspendidas tardaron %v; no deben tocar la red", espera)
	}
	mu.Lock()
	defer mu.Unlock()
	if avisos != 1 {
		t.Errorf("se esperaba un solo aviso en el log, hubo %d", avisos)
	}
}

func TestSeReanudaTrasLaSuspension(t *testing.T) {
	cache, servidor := nuevoDePrueba(t, Opciones{Suspension: time.Second, TiempoEspera: 50 * time.Millisecond})
	if servidor == nil {
		t.Skip("usa miniredis")
	}
	ctx := context.Background()
	reloj := time.Now()
	cache.ahora = func() time.Time { return reloj }

	servidor.SetError("LOADING") // Redis responde con error
	if _, err := cache.Obtener(ctx, "x"); err == nil {
		t.Fatal("se esperaba error")
	}
	servidor.SetError("")
	if cache.disponible() {
		t.Fatal("debe estar suspendido")
	}
	reloj = reloj.Add(2 * time.Second)
	if !cache.disponible() {
		t.Fatal("debe reanudarse al pasar la suspensión")
	}
	if err := cache.Guardar(ctx, prediccionDePrueba(9)); err != nil {
		t.Errorf("tras reanudar debe guardar: %v", err)
	}
}

func TestURLInvalida(t *testing.T) {
	if _, err := NuevoRedis("http://no-es-redis", Opciones{}); err == nil {
		t.Error("se esperaba error con una URL que no es de Redis")
	}
}

func TestUsoConcurrente(t *testing.T) {
	// Espera holgada: abrir 50 conexiones en frío a la vez puede tardar más de 100 ms.
	cache, _ := nuevoDePrueba(t, Opciones{TiempoEspera: 2 * time.Second})
	ctx := context.Background()
	var grupo sync.WaitGroup
	for indice := 0; indice < 50; indice++ {
		grupo.Add(1)
		go func(n int) {
			defer grupo.Done()
			prediccion := prediccionDePrueba(float64(n), 1)
			if err := cache.Guardar(ctx, prediccion); err != nil {
				t.Errorf("Guardar: %v", err)
			}
			if acierto, err := cache.Obtener(ctx, prediccion.HashEntrada); err != nil || acierto == nil {
				t.Errorf("Obtener %d: %v, %v", n, acierto, err)
			}
		}(indice)
	}
	grupo.Wait()
}
