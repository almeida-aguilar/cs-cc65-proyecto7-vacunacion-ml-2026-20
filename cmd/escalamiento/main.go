// Programa escalamiento: mide cómo cambia el entrenamiento distribuido al usar
// 1, 2 y 4 nodos ML. Los nodos se levantan en este mismo proceso, en puertos
// locales libres, y reciben las mismas filas repartidas en fragmentos. Con lote
// completo y la misma semilla la pérdida final debe coincidir entre todas las
// configuraciones; el programa lo verifica y lo imprime.
package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/almeida-aguilar/cs-cc65-proyecto7-vacunacion-ml-2026-20/internal/cargador"
	"github.com/almeida-aguilar/cs-cc65-proyecto7-vacunacion-ml-2026-20/internal/coordinador"
	"github.com/franztoledo/logreg"
)

// toleranciaPerdida es la diferencia relativa máxima aceptada entre las
// pérdidas finales (igual que en cmd/benchmark).
const toleranciaPerdida = 1e-9

// semilla fija el entrenamiento para que sea reproducible.
const semilla = 42

// medicion es una fila del CSV de salida.
type medicion struct {
	nodos        int
	filas        int
	epocas       int
	workersNodo  int
	perdidaFinal float64
	segundos     float64
}

// levantarNodos crea un nodo por fragmento, cada uno con su listener en un
// puerto local libre. Devuelve las direcciones y una función que los detiene.
func levantarNodos(fragmentos []*logreg.Dataset, workersNodo int) ([]string, func(), error) {
	var nodos []*logreg.Nodo
	detener := func() {
		for _, nodo := range nodos {
			_ = nodo.Detener()
		}
	}
	var direcciones []string
	for indice, fragmento := range fragmentos {
		nodo, err := logreg.NuevoNodo(fmt.Sprintf("nodo-%d", indice+1), fragmento, workersNodo)
		if err != nil {
			detener()
			return nil, nil, fmt.Errorf("no se pudo crear el nodo %d: %w", indice+1, err)
		}
		nodo.Log = func(string, ...any) {}
		escucha, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			detener()
			return nil, nil, fmt.Errorf("no se pudo abrir el puerto del nodo %d: %w", indice+1, err)
		}
		nodos = append(nodos, nodo)
		go func() { _ = nodo.Servir(escucha) }()
		direcciones = append(direcciones, escucha.Addr().String())
	}
	return direcciones, detener, nil
}

// entrenarConNodos reparte una copia de los datos entre k nodos, entrena de
// forma distribuida y devuelve la pérdida final y los segundos de entrenamiento.
func entrenarConNodos(datos *logreg.Dataset, k, epocas, workersNodo int) (float64, float64, error) {
	direcciones, detener, err := levantarNodos(datos.Copiar().Particionar(k), workersNodo)
	if err != nil {
		return 0, 0, err
	}
	defer detener()

	servidor, err := coordinador.Nuevo(coordinador.Opciones{
		Direcciones:     direcciones,
		Epocas:          epocas,
		TamanoLote:      0, // lote completo: el resultado no depende del reparto
		TasaAprendizaje: 0.5,
		Semilla:         semilla,
		Reintentos:      0,
		Silencioso:      true,
	})
	if err != nil {
		return 0, 0, fmt.Errorf("no se pudo configurar el clúster: %w", err)
	}
	if err := servidor.Conectar(); err != nil {
		return 0, 0, fmt.Errorf("no se pudo conectar con los nodos: %w", err)
	}
	defer servidor.Cerrar()
	if servidor.NodosActivos() != k || servidor.FilasTotales() != datos.Len() {
		return 0, 0, fmt.Errorf("clúster incompleto: %d nodos y %d filas (se esperaban %d y %d)",
			servidor.NodosActivos(), servidor.FilasTotales(), k, datos.Len())
	}

	inicio := time.Now()
	resultado, err := servidor.Entrenar(context.Background())
	if err != nil {
		return 0, 0, fmt.Errorf("falló el entrenamiento con %d nodos: %w", k, err)
	}
	segundos := time.Since(inicio).Seconds()
	if len(resultado.Historial) == 0 {
		return 0, 0, fmt.Errorf("el entrenamiento con %d nodos no produjo historial", k)
	}
	return resultado.Historial[len(resultado.Historial)-1].Perdida, segundos, nil
}

// medirEscalamiento entrena con cada cantidad de nodos y usa la mediana de los
// tiempos de varias repeticiones. La pérdida se toma de la última repetición.
func medirEscalamiento(datos *logreg.Dataset, listaNodos []int, epocas, repeticiones, workersNodo int) ([]medicion, error) {
	var mediciones []medicion
	for _, k := range listaNodos {
		var tiempos []float64
		var perdida float64
		for rep := 0; rep < repeticiones; rep++ {
			p, segundos, err := entrenarConNodos(datos, k, epocas, workersNodo)
			if err != nil {
				return nil, err
			}
			perdida = p
			tiempos = append(tiempos, segundos)
		}
		sort.Float64s(tiempos)
		mediciones = append(mediciones, medicion{
			nodos:        k,
			filas:        datos.Len(),
			epocas:       epocas,
			workersNodo:  workersNodo,
			perdidaFinal: perdida,
			segundos:     tiempos[len(tiempos)/2],
		})
	}
	return mediciones, nil
}

// verificarPerdidas comprueba que todas las pérdidas coincidan con la primera
// (diferencia relativa <= toleranciaPerdida) y devuelve la mayor diferencia.
func verificarPerdidas(mediciones []medicion) (float64, error) {
	if len(mediciones) == 0 {
		return 0, fmt.Errorf("no hay mediciones")
	}
	referencia := mediciones[0].perdidaFinal
	mayor := 0.0
	for _, m := range mediciones[1:] {
		relativa := math.Abs(m.perdidaFinal-referencia) / math.Max(math.Abs(referencia), 1e-300)
		mayor = math.Max(mayor, relativa)
		if relativa > toleranciaPerdida {
			return mayor, fmt.Errorf("la pérdida con %d nodos (%.12f) difiere de la de %d nodos (%.12f): diferencia relativa %.2e",
				m.nodos, m.perdidaFinal, mediciones[0].nodos, referencia, relativa)
		}
	}
	return mayor, nil
}

// escribirCSV guarda la tabla de resultados, creando la carpeta si falta.
func escribirCSV(ruta string, mediciones []medicion) error {
	if err := os.MkdirAll(filepath.Dir(ruta), 0o755); err != nil {
		return fmt.Errorf("no se pudo crear la carpeta de salida: %w", err)
	}
	archivo, err := os.Create(ruta)
	if err != nil {
		return fmt.Errorf("no se pudo crear %s: %w", ruta, err)
	}
	fmt.Fprintln(archivo, "nodos,workers_nodo,filas,epocas,perdida_final,segundos")
	for _, m := range mediciones {
		fmt.Fprintf(archivo, "%d,%d,%d,%d,%.12f,%.4f\n", m.nodos, m.workersNodo, m.filas, m.epocas, m.perdidaFinal, m.segundos)
	}
	return archivo.Close()
}

func main() {
	csv := flag.String("csv", "data/modelo/train_raw.csv", "CSV numérico con la etiqueta deserto en la última columna")
	maxFilas := flag.Int("max-filas", 0, "limita las filas leídas (0 = todas)")
	workersNodo := flag.Int("workers-nodo", 0, "goroutines de cada nodo para el gradiente local (0 = todos los núcleos)")
	epocas := flag.Int("epocas", 30, "épocas fijas del entrenamiento")
	repeticiones := flag.Int("repeticiones", 3, "repeticiones por cantidad de nodos (se usa la mediana)")
	salida := flag.String("salida", "reports/pc4/escalamiento_cluster.csv", "CSV con la tabla de resultados (para pruebas usa prefijo prueba)")
	flag.Parse()

	if *repeticiones < 1 || *epocas < 1 || *workersNodo < 0 {
		fmt.Println("Error: -repeticiones y -epocas deben ser al menos 1 y -workers-nodo no puede ser negativo")
		os.Exit(1)
	}

	datos, columnas, resumen, err := cargador.CargarCSV(*csv, 0, nil, *maxFilas)
	if err != nil {
		fmt.Println("Error al cargar el CSV:", err)
		os.Exit(1)
	}
	fmt.Printf("Datos: %d filas válidas, %d rechazadas, %d variables\n",
		resumen.FilasValidas, resumen.FilasRechazadas, len(columnas))

	mediciones, err := medirEscalamiento(datos, []int{1, 2, 4}, *epocas, *repeticiones, *workersNodo)
	if err != nil {
		fmt.Println("Error en la medición:", err)
		os.Exit(1)
	}

	fmt.Printf("Workers por nodo: %d (0 = todos los núcleos, %d disponibles)\n", *workersNodo, runtime.NumCPU())
	fmt.Println("nodos | workers_nodo | filas | épocas | pérdida final   | segundos")
	for _, m := range mediciones {
		fmt.Printf("%5d | %12d | %5d | %6d | %.12f | %.4f\n", m.nodos, m.workersNodo, m.filas, m.epocas, m.perdidaFinal, m.segundos)
	}

	diferencia, errVerificacion := verificarPerdidas(mediciones)
	if errVerificacion != nil {
		fmt.Println("ERROR de equivalencia:", errVerificacion)
	} else {
		fmt.Printf("Pérdida final idéntica entre 1, 2 y 4 nodos (diferencia relativa máxima %.2e, tolerancia %.0e)\n",
			diferencia, toleranciaPerdida)
	}

	if err := escribirCSV(*salida, mediciones); err != nil {
		fmt.Println("Error al guardar:", err)
		os.Exit(1)
	}
	fmt.Println("Resultados guardados en", *salida)
	if errVerificacion != nil {
		os.Exit(1)
	}
}
