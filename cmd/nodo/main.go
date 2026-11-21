// Programa nodo: sirve un fragmento del dataset por TCP para que el
// coordinador (parameter server) calcule los gradientes de forma distribuida.
// El nodo escucha conexiones; la reconexión ante caídas la hace el coordinador.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/almeida-aguilar/cs-cc65-proyecto7-vacunacion-ml-2026-20/internal/cargador"
	"github.com/franztoledo/logreg"
)

// opciones agrupa los flags de la línea de comandos.
type opciones struct {
	servidor    string
	id          string
	csv         string
	maxFilas    int
	workers     int
	sinColumnas string
}

// leerOpciones parsea los flags. -servidor es la dirección de escucha del nodo
// (no del coordinador) y -nodo es su identificador en el clúster.
func leerOpciones() opciones {
	var o opciones
	flag.StringVar(&o.servidor, "servidor", ":9001", "dirección TCP donde el nodo escucha al coordinador")
	flag.StringVar(&o.id, "nodo", "nodo-1", "identificador del nodo dentro del clúster")
	flag.StringVar(&o.csv, "csv", "data/modelo/train_raw.csv", "CSV numérico con el fragmento de datos del nodo")
	flag.IntVar(&o.maxFilas, "max-filas", 0, "limita las filas leídas (0 = todas)")
	flag.IntVar(&o.workers, "workers", 0, "goroutines para el gradiente local (0 = todos los núcleos)")
	flag.StringVar(&o.sinColumnas, "sin-columnas", "", "columnas a excluir, separadas por comas")
	flag.Parse()
	return o
}

// nuevoNodoDesdeCSV carga un fragmento con el cargador concurrente y crea el
// nodo logreg. Devuelve también las columnas usadas y el resumen de la carga
// para mostrarlos como evidencia al arrancar.
func nuevoNodoDesdeCSV(ruta, id string, workers, maxFilas int, excluir []string) (*logreg.Nodo, []string, cargador.Resumen, error) {
	datos, columnas, resumen, err := cargador.CargarCSV(ruta, workers, excluir, maxFilas)
	if err != nil {
		return nil, nil, resumen, fmt.Errorf("no se pudo cargar el fragmento: %w", err)
	}
	nodo, err := logreg.NuevoNodo(id, datos, workers)
	if err != nil {
		return nil, nil, resumen, fmt.Errorf("no se pudo crear el nodo: %w", err)
	}
	return nodo, columnas, resumen, nil
}

// registrarSenales detiene el nodo de forma limpia con Ctrl+C o SIGTERM.
func registrarSenales(nodo *logreg.Nodo) {
	senales := make(chan os.Signal, 1)
	signal.Notify(senales, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-senales
		fmt.Println("\nCerrando el nodo...")
		if err := nodo.Detener(); err != nil {
			fmt.Println("Error al cerrar el nodo:", err)
		}
	}()
}

func main() {
	opciones := leerOpciones()
	var excluir []string
	if opciones.sinColumnas != "" {
		excluir = strings.Split(opciones.sinColumnas, ",")
	}

	nodo, columnas, resumen, err := nuevoNodoDesdeCSV(
		opciones.csv, opciones.id, opciones.workers, opciones.maxFilas, excluir)
	if err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}

	fmt.Printf("Nodo %s | filas válidas %d | rechazadas %d | variables %d | carga %v\n",
		nodo.ID, resumen.FilasValidas, resumen.FilasRechazadas, len(columnas),
		resumen.Duracion.Round(time.Millisecond))
	fmt.Printf("Variables: %v\nExcluidas: %v\n", columnas, excluir)

	nodo.Log = func(formato string, valores ...any) {
		fmt.Printf("[%s %s] %s\n", time.Now().Format("15:04:05.000"), nodo.ID, fmt.Sprintf(formato, valores...))
	}
	registrarSenales(nodo)

	fmt.Printf("Escuchando en %s. Esperando peticiones del coordinador (Ctrl+C para detener).\n", opciones.servidor)
	if err := nodo.Escuchar(opciones.servidor); err != nil {
		fmt.Println("Error al escuchar:", err)
		os.Exit(1)
	}
}
