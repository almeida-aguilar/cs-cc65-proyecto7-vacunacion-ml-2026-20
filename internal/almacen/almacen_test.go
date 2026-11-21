package almacen

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/franztoledo/logreg"
)

// fabrica crea un repositorio limpio para una prueba.
type fabrica func(t *testing.T) Repositorio

// fabricas devuelve la memoria siempre y Mongo solo si MONGO_URI está definida,
// de modo que CI (sin Mongo) siga en verde.
func fabricas() map[string]fabrica {
	resultado := map[string]fabrica{
		"memoria": func(t *testing.T) Repositorio { return NuevaMemoria() },
	}
	uri := os.Getenv("MONGO_URI")
	if uri == "" {
		return resultado
	}
	resultado["mongo"] = func(t *testing.T) Repositorio {
		nombre := fmt.Sprintf("vacunas_test_%d", time.Now().UnixNano())
		repositorio, err := NuevoMongo(context.Background(), uri, nombre)
		if err != nil {
			t.Fatalf("no se pudo conectar con MongoDB: %v", err)
		}
		t.Cleanup(func() {
			_ = repositorio.Base().Drop(context.Background())
			_ = repositorio.Cerrar(context.Background())
		})
		return repositorio
	}
	return resultado
}

// porCadaRepositorio ejecuta la misma prueba contra todas las implementaciones.
func porCadaRepositorio(t *testing.T, prueba func(t *testing.T, repo Repositorio)) {
	for nombre, crear := range fabricas() {
		t.Run(nombre, func(t *testing.T) { prueba(t, crear(t)) })
	}
}

func ejecucionDePrueba(inicio time.Time) *Ejecucion {
	return &Ejecucion{
		Estado:        EstadoEnCurso,
		Configuracion: ConfiguracionDesde(logreg.ConfiguracionPorDefecto()),
		Nodos:         2,
		FilasTotales:  1000,
		Inicio:        inicio,
	}
}

func TestEjecucionInsertarObtenerActualizar(t *testing.T) {
	porCadaRepositorio(t, func(t *testing.T, repo Repositorio) {
		ctx := context.Background()
		ejecucion := ejecucionDePrueba(time.Now().UTC().Truncate(time.Millisecond))
		if err := repo.InsertarEjecucion(ctx, ejecucion); err != nil {
			t.Fatalf("no se pudo insertar: %v", err)
		}
		if ejecucion.ID == "" {
			t.Fatal("el ID debía generarse al insertar")
		}

		ejecucion.Estado = EstadoTerminada
		ejecucion.MejorEpoca = 4
		ejecucion.Segundos = 1.5
		if err := repo.ActualizarEjecucion(ctx, ejecucion); err != nil {
			t.Fatalf("no se pudo actualizar: %v", err)
		}
		leida, err := repo.ObtenerEjecucion(ctx, ejecucion.ID)
		if err != nil {
			t.Fatalf("no se pudo leer: %v", err)
		}
		if leida.Estado != EstadoTerminada || leida.MejorEpoca != 4 || leida.Nodos != 2 {
			t.Errorf("ejecución leída incorrecta: %+v", leida)
		}
		if leida.Configuracion.Epocas != ejecucion.Configuracion.Epocas {
			t.Errorf("la configuración no se conservó: %+v", leida.Configuracion)
		}
	})
}

func TestEjecucionNoEncontrada(t *testing.T) {
	porCadaRepositorio(t, func(t *testing.T, repo Repositorio) {
		ctx := context.Background()
		if _, err := repo.ObtenerEjecucion(ctx, "no-existe"); !errors.Is(err, ErrNoEncontrado) {
			t.Errorf("se esperaba ErrNoEncontrado al leer, llegó %v", err)
		}
		inexistente := ejecucionDePrueba(time.Now())
		inexistente.ID = "no-existe"
		if err := repo.ActualizarEjecucion(ctx, inexistente); !errors.Is(err, ErrNoEncontrado) {
			t.Errorf("se esperaba ErrNoEncontrado al actualizar, llegó %v", err)
		}
	})
}

func TestUltimasEjecucionesOrdenYLimite(t *testing.T) {
	porCadaRepositorio(t, func(t *testing.T, repo Repositorio) {
		ctx := context.Background()
		base := time.Now().UTC().Truncate(time.Millisecond)
		for desplazamiento := 0; desplazamiento < 5; desplazamiento++ {
			ejecucion := ejecucionDePrueba(base.Add(time.Duration(desplazamiento) * time.Minute))
			if err := repo.InsertarEjecucion(ctx, ejecucion); err != nil {
				t.Fatalf("no se pudo insertar: %v", err)
			}
		}
		ultimas, err := repo.UltimasEjecuciones(ctx, 3)
		if err != nil {
			t.Fatalf("no se pudo listar: %v", err)
		}
		if len(ultimas) != 3 {
			t.Fatalf("se esperaban 3 ejecuciones, llegaron %d", len(ultimas))
		}
		if !ultimas[0].Inicio.After(ultimas[1].Inicio) || !ultimas[1].Inicio.After(ultimas[2].Inicio) {
			t.Errorf("las ejecuciones no vienen de más reciente a más antigua: %v", ultimas)
		}
	})
}

func TestMetricasOrdenadasPorEpocaYUnicas(t *testing.T) {
	porCadaRepositorio(t, func(t *testing.T, repo Repositorio) {
		ctx := context.Background()
		for _, epoca := range []int{3, 1, 2} {
			metrica := &Metrica{EjecucionID: "ej-1", Epoca: epoca, Perdida: 1 / float64(epoca), Nodos: 2, Fecha: time.Now()}
			if err := repo.InsertarMetrica(ctx, metrica); err != nil {
				t.Fatalf("no se pudo insertar la época %d: %v", epoca, err)
			}
		}
		if err := repo.InsertarMetrica(ctx, &Metrica{EjecucionID: "otra", Epoca: 1}); err != nil {
			t.Fatalf("no se pudo insertar la métrica de otra ejecución: %v", err)
		}
		if err := repo.InsertarMetrica(ctx, &Metrica{EjecucionID: "ej-1", Epoca: 2}); err == nil {
			t.Error("se esperaba error al repetir (ejecución, época)")
		}

		lista, err := repo.MetricasDeEjecucion(ctx, "ej-1")
		if err != nil {
			t.Fatalf("no se pudo leer: %v", err)
		}
		if len(lista) != 3 {
			t.Fatalf("se esperaban 3 métricas, llegaron %d", len(lista))
		}
		for indice, metrica := range lista {
			if metrica.Epoca != indice+1 {
				t.Errorf("posición %d: época %d, se esperaba %d", indice, metrica.Epoca, indice+1)
			}
		}
	})
}

func TestPrediccionPorHash(t *testing.T) {
	porCadaRepositorio(t, func(t *testing.T, repo Repositorio) {
		ctx := context.Background()
		entrada := []float64{1, 2.5, 0, 1}
		hash := HashEntrada(entrada)
		base := time.Now().UTC().Truncate(time.Millisecond)
		for indice, probabilidad := range []float64{0.2, 0.9} {
			prediccion := &Prediccion{
				EjecucionID: "ej-1", Entrada: entrada, HashEntrada: hash,
				Probabilidad: probabilidad, Clase: indice, Umbral: 0.48,
				LatenciaMs: 1.5, Origen: OrigenCalculo,
				Fecha: base.Add(time.Duration(indice) * time.Second),
			}
			if err := repo.InsertarPrediccion(ctx, prediccion); err != nil {
				t.Fatalf("no se pudo insertar: %v", err)
			}
		}
		encontrada, err := repo.PrediccionPorHash(ctx, hash)
		if err != nil {
			t.Fatalf("no se pudo leer por hash: %v", err)
		}
		if encontrada.Probabilidad != 0.9 {
			t.Errorf("debía devolver la más reciente (0.9), llegó %v", encontrada.Probabilidad)
		}
		if len(encontrada.Entrada) != len(entrada) {
			t.Errorf("la entrada no se conservó: %v", encontrada.Entrada)
		}
		if _, err := repo.PrediccionPorHash(ctx, "otro"); !errors.Is(err, ErrNoEncontrado) {
			t.Errorf("se esperaba ErrNoEncontrado, llegó %v", err)
		}
		ultimas, err := repo.UltimasPredicciones(ctx, 1)
		if err != nil || len(ultimas) != 1 || ultimas[0].Probabilidad != 0.9 {
			t.Errorf("UltimasPredicciones incorrecta: %v, %v", ultimas, err)
		}
	})
}

func TestInsercionesConcurrentes(t *testing.T) {
	porCadaRepositorio(t, func(t *testing.T, repo Repositorio) {
		ctx := context.Background()
		const hilos = 20
		var grupo sync.WaitGroup
		errores := make(chan error, hilos)
		for indice := 0; indice < hilos; indice++ {
			grupo.Add(1)
			go func(epoca int) {
				defer grupo.Done()
				errores <- repo.InsertarMetrica(ctx, &Metrica{EjecucionID: "concurrente", Epoca: epoca, Fecha: time.Now()})
			}(indice)
		}
		grupo.Wait()
		close(errores)
		for err := range errores {
			if err != nil {
				t.Errorf("error en inserción concurrente: %v", err)
			}
		}
		lista, err := repo.MetricasDeEjecucion(ctx, "concurrente")
		if err != nil || len(lista) != hilos {
			t.Errorf("se esperaban %d métricas, llegaron %d (%v)", hilos, len(lista), err)
		}
	})
}

func TestHashEntrada(t *testing.T) {
	a := HashEntrada([]float64{1, 2, 3})
	if a != HashEntrada([]float64{1, 2, 3}) {
		t.Error("el hash debe ser determinista")
	}
	if a == HashEntrada([]float64{1, 2, 4}) {
		t.Error("entradas distintas deben dar hashes distintos")
	}
	if len(a) != 64 {
		t.Errorf("SHA-256 en hexadecimal debe tener 64 caracteres, tiene %d", len(a))
	}
}
