// Programa benchmark: mide cuánto mejora el tiempo al usar más workers
// (goroutines) en dos etapas: la carga del CSV y el entrenamiento.
// Además verifica que el resultado del entrenamiento no cambie con el
// número de workers.
package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/almeida-aguilar/cs-cc65-proyecto7-vacunacion-ml-2026-20/internal/cargador"
	"github.com/franztoledo/logreg"
)

// toleranciaPerdida es la diferencia relativa máxima aceptada entre las
// pérdidas finales. Sumar números decimales en otro orden cambia los últimos
// dígitos, por eso no se exige igualdad exacta.
const toleranciaPerdida = 1e-9

// medicion es una fila de la tabla final.
type medicion struct {
	etapa    string
	workers  int
	segundos float64
	speedup  float64
}

func main() {
	train := flag.String("train", "data/modelo/train_raw.csv", "CSV de entrenamiento")
	maxFilas := flag.Int("max-filas", 0, "limita las filas leídas (0 = todas)")
	repeticiones := flag.Int("repeticiones", 3, "repeticiones por cantidad de workers (se usa la mediana)")
	epocas := flag.Int("epocas", 5, "épocas fijas del entrenamiento")
	salida := flag.String("salida", "reports/go/benchmark_workers_go.csv", "CSV con la tabla de resultados")
	flag.Parse()

	if *repeticiones < 1 {
		fmt.Println("Error: -repeticiones debe ser al menos 1")
		return
	}

	fmt.Printf("CPUs: %d | Sistema: %s | Go: %s\n", runtime.NumCPU(), runtime.GOOS, runtime.Version())
	listaWorkers := calcularListaWorkers(runtime.NumCPU())
	fmt.Println("Workers a probar:", listaWorkers)

	// Etapa 1: carga
	cargas, err := medirCarga(*train, *maxFilas, listaWorkers, *repeticiones)
	if err != nil {
		fmt.Println("Error en el benchmark de carga:", err)
		return
	}

	// Etapa 2: entrenamiento (los datos se cargan una sola vez)
	datos, _, resumen, err := cargador.CargarCSV(*train, 0, nil, *maxFilas)
	if err != nil {
		fmt.Println("Error al cargar los datos de entrenamiento:", err)
		return
	}
	fmt.Printf("\nDatos para entrenar: %d filas, %d variables\n", resumen.FilasValidas, datos.NumVariables())
	entrenamientos, err := medirEntrenamiento(datos, listaWorkers, *repeticiones, *epocas)
	if err != nil {
		fmt.Println("Error en el benchmark de entrenamiento:", err)
		return
	}

	todas := append(cargas, entrenamientos...)
	imprimirTabla(todas)
	if err := escribirCSV(*salida, todas); err != nil {
		fmt.Println("Error al escribir el CSV:", err)
		return
	}
	fmt.Println("\nTabla guardada en", *salida)
}

// calcularListaWorkers devuelve 1, 2, 4, 8 y cpus, sin repetidos, ordenados
// y sin pasar de cpus.
func calcularListaWorkers(cpus int) []int {
	candidatos := []int{1, 2, 4, 8, cpus}
	vistos := make(map[int]bool)
	var lista []int
	for _, w := range candidatos {
		if w >= 1 && w <= cpus && !vistos[w] {
			vistos[w] = true
			lista = append(lista, w)
		}
	}
	sort.Ints(lista)
	return lista
}

// mediana devuelve el valor central (o el promedio de los dos centrales).
// No modifica el slice recibido.
func mediana(valores []float64) float64 {
	if len(valores) == 0 {
		return 0
	}
	copia := append([]float64(nil), valores...)
	sort.Float64s(copia)
	medio := len(copia) / 2
	if len(copia)%2 == 1 {
		return copia[medio]
	}
	return (copia[medio-1] + copia[medio]) / 2
}

// calcularSpeedup es el tiempo con 1 worker dividido entre el tiempo con N.
func calcularSpeedup(tiempoBase, tiempo float64) float64 {
	if tiempo <= 0 {
		return 0
	}
	return tiempoBase / tiempo
}

// diferenciaRelativa mide |a-b| respecto al tamaño de los valores.
func diferenciaRelativa(a, b float64) float64 {
	denominador := math.Max(math.Abs(a), math.Abs(b))
	if denominador == 0 {
		return 0
	}
	return math.Abs(a-b) / denominador
}

// medirCarga mide cuánto tarda CargarCSV con cada cantidad de workers.
func medirCarga(ruta string, maxFilas int, listaWorkers []int, repeticiones int) ([]medicion, error) {
	fmt.Println("\n== Carga del CSV ==")
	// Calentamiento: la primera lectura llena la caché del sistema operativo
	// y distorsionaría la medición del primer caso. Se descarta.
	if _, _, _, err := cargador.CargarCSV(ruta, 1, nil, maxFilas); err != nil {
		return nil, err
	}

	var resultados []medicion
	for _, workers := range listaWorkers {
		var tiempos []float64
		for i := 0; i < repeticiones; i++ {
			_, _, resumen, err := cargador.CargarCSV(ruta, workers, nil, maxFilas)
			if err != nil {
				return nil, err
			}
			tiempos = append(tiempos, resumen.Duracion.Seconds())
		}
		resultados = append(resultados, nuevaMedicion("carga", workers, mediana(tiempos), resultados))
		fmt.Printf("  workers %2d: %.3f s (mediana de %d)\n", workers, resultados[len(resultados)-1].segundos, repeticiones)
	}
	return resultados, nil
}

// medirEntrenamiento entrena con la misma configuración y distinta cantidad
// de workers, y verifica que la pérdida final sea la misma.
func medirEntrenamiento(datos *logreg.Dataset, listaWorkers []int, repeticiones, epocas int) ([]medicion, error) {
	fmt.Println("\n== Entrenamiento ==")
	var resultados []medicion
	var perdidaReferencia float64
	var diferenciaMaxima float64

	for indice, workers := range listaWorkers {
		cfg := logreg.ConfiguracionPorDefecto()
		cfg.Workers = workers
		cfg.Epocas = epocas
		cfg.TamanoLote = 0 // lote completo: el gradiente se reparte entre los workers
		cfg.Tolerancia = 0 // sin parada anticipada: todas las corridas hacen las mismas épocas
		cfg.Semilla = 42
		cfg.Validacion = nil

		var tiempos []float64
		for i := 0; i < repeticiones; i++ {
			// Entrenar copia los datos escalados; se libera la memoria de la
			// corrida anterior para que el recolector no ensucie la medición.
			runtime.GC()
			resultado, err := logreg.Entrenar(context.Background(), datos, cfg)
			if err != nil {
				return nil, err
			}
			tiempos = append(tiempos, resultado.DuracionTotal.Seconds())

			perdida := resultado.Historial[len(resultado.Historial)-1].Perdida
			if indice == 0 && i == 0 {
				perdidaReferencia = perdida
			}
			diferenciaMaxima = math.Max(diferenciaMaxima, diferenciaRelativa(perdidaReferencia, perdida))
		}
		resultados = append(resultados, nuevaMedicion("entrenamiento", workers, mediana(tiempos), resultados))
		fmt.Printf("  workers %2d: %.3f s (mediana de %d)\n", workers, resultados[len(resultados)-1].segundos, repeticiones)
	}

	fmt.Printf("\nPérdida final de referencia: %.12f | diferencia relativa máxima: %.3g\n", perdidaReferencia, diferenciaMaxima)
	if diferenciaMaxima < toleranciaPerdida {
		fmt.Printf("OK: mismo resultado con %v workers (sin condiciones de carrera)\n", listaWorkers)
	} else {
		fmt.Printf("AVISO: la pérdida final cambia entre cantidades de workers (diferencia relativa %.3g > %.0e). Hay que revisarlo.\n",
			diferenciaMaxima, toleranciaPerdida)
	}
	return resultados, nil
}

// nuevaMedicion calcula el speedup respecto a la primera medición de la
// misma etapa (la de 1 worker).
func nuevaMedicion(etapa string, workers int, segundos float64, anteriores []medicion) medicion {
	base := segundos
	if len(anteriores) > 0 {
		base = anteriores[0].segundos
	}
	return medicion{etapa: etapa, workers: workers, segundos: segundos, speedup: calcularSpeedup(base, segundos)}
}

func imprimirTabla(filas []medicion) {
	fmt.Println("\nEtapa          | Workers | Tiempo (s) | Speedup")
	fmt.Println("---------------+---------+------------+--------")
	for _, f := range filas {
		fmt.Printf("%-14s | %7d | %10.3f | %6.2fx\n", f.etapa, f.workers, f.segundos, f.speedup)
	}
}

func escribirCSV(ruta string, filas []medicion) error {
	if err := os.MkdirAll(filepath.Dir(ruta), 0o755); err != nil {
		return err
	}
	var sb strings.Builder
	sb.WriteString("etapa,workers,segundos,speedup\n")
	for _, f := range filas {
		fmt.Fprintf(&sb, "%s,%d,%.6f,%.4f\n", f.etapa, f.workers, f.segundos, f.speedup)
	}
	return os.WriteFile(ruta, []byte(sb.String()), 0o644)
}
