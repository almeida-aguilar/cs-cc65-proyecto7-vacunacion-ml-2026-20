// Programa coordinador: entrena una regresión logística de forma distribuida
// conectándose a los nodos ML por TCP y guarda el modelo y el reporte del
// parameter server. La API del PC4 reutilizará este mismo wrapper.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/almeida-aguilar/cs-cc65-proyecto7-vacunacion-ml-2026-20/internal/coordinador"
	"github.com/franztoledo/logreg"
)

// opciones agrupa los flags de la línea de comandos.
type opciones struct {
	nodos              string
	epocas, lote       int
	tasa, pesoPositivo float64
	semilla            int64
	minimoNodos        int
	reintentos         int
	esperaReintento    time.Duration
	tiempoEspera       time.Duration
	salida, reportes   string
	nombre             string
}

// reporteServidor resume la corriente distribuida para el informe PC4.
type reporteServidor struct {
	NodosActivos          int                  `json:"nodos_activos"`
	FilasTotales          int                  `json:"filas_totales"`
	Configuracion         logreg.Configuracion `json:"configuracion"`
	Metricas              *logreg.Metricas     `json:"metricas"`
	MejorEpoca            int                  `json:"mejor_epoca"`
	SegundosEntrenamiento float64              `json:"segundos_entrenamiento"`
}

func leerOpciones() opciones {
	var o opciones
	flag.StringVar(&o.nodos, "nodos", "", "direcciones TCP de los nodos, separadas por comas (ej. nodo1:9001,nodo2:9001)")
	flag.IntVar(&o.epocas, "epocas", 10, "máximo de épocas")
	flag.IntVar(&o.lote, "lote", 1024, "filas por mini-lote y nodo (0 = lote completo)")
	flag.Float64Var(&o.tasa, "tasa", 0.1, "tasa de aprendizaje")
	flag.Float64Var(&o.pesoPositivo, "peso-positivo", 1, "peso de la clase positiva")
	flag.Int64Var(&o.semilla, "semilla", 42, "semilla del entrenamiento")
	flag.IntVar(&o.minimoNodos, "min-nodos", 1, "nodos vivos necesarios para entrenar")
	flag.IntVar(&o.reintentos, "reintentos", 10, "reintentos al conectar con cada nodo")
	flag.DurationVar(&o.esperaReintento, "espera-reintento", 2*time.Second, "espera entre reintentos")
	flag.DurationVar(&o.tiempoEspera, "tiempo-espera", 30*time.Second, "tiempo máximo por petición TCP")
	flag.StringVar(&o.salida, "salida", "modelo_distribuido.json", "archivo donde se guarda el modelo")
	flag.StringVar(&o.reportes, "reportes", "reports/pc4", "carpeta de reportes")
	flag.StringVar(&o.nombre, "nombre", "distribuido", "prefijo del archivo de reporte")
	flag.Parse()
	return o
}

func main() {
	opciones := leerOpciones()
	if opciones.nodos == "" {
		fmt.Println("Error: falta -nodos con al menos una dirección TCP")
		os.Exit(1)
	}
	direcciones := strings.Split(opciones.nodos, ",")
	for indice, direccion := range direcciones {
		direcciones[indice] = strings.TrimSpace(direccion)
	}

	servidor, err := coordinador.Nuevo(coordinador.Opciones{
		Direcciones:       direcciones,
		Epocas:            opciones.epocas,
		TamanoLote:        opciones.lote,
		TasaAprendizaje:   opciones.tasa,
		PesoClasePositiva: opciones.pesoPositivo,
		Semilla:           opciones.semilla,
		MinimoNodos:       opciones.minimoNodos,
		Reintentos:        opciones.reintentos,
		EsperaReintento:   opciones.esperaReintento,
		TiempoEspera:      opciones.tiempoEspera,
	})
	if err != nil {
		fmt.Println("Error al configurar el clúster:", err)
		os.Exit(1)
	}

	fmt.Println("Conectando con los nodos...")
	if err := servidor.Conectar(); err != nil {
		fmt.Println("Error al conectar:", err)
		os.Exit(1)
	}
	defer servidor.Cerrar()
	fmt.Printf("Nodos activos: %d | filas totales: %d\n", servidor.NodosActivos(), servidor.FilasTotales())

	fmt.Println("Entrenando de forma distribuida...")
	inicio := time.Now()
	resultado, err := servidor.Entrenar(context.Background())
	if err != nil {
		fmt.Println("Error al entrenar:", err)
		os.Exit(1)
	}
	duracion := time.Since(inicio)
	for _, progreso := range resultado.Historial {
		fmt.Printf("Epoca %2d | pérdida %.4f | nodos %d | %v\n",
			progreso.Epoca, progreso.Perdida, progreso.Nodos, progreso.Duracion.Round(time.Millisecond))
	}

	fmt.Println("Evaluando en los nodos...")
	metricas, err := servidor.Evaluar(resultado.Modelo)
	if err != nil {
		fmt.Println("Error al evaluar:", err)
		os.Exit(1)
	}
	fmt.Printf("Exactitud %.4f | F1 %.4f | pérdida %.4f\n",
		metricas.Exactitud, metricas.F1, metricas.PerdidaLog)

	if err := resultado.Modelo.Guardar(opciones.salida); err != nil {
		fmt.Println("Error al guardar el modelo:", err)
		os.Exit(1)
	}
	reporte := reporteServidor{
		NodosActivos:          servidor.NodosActivos(),
		FilasTotales:          servidor.FilasTotales(),
		Configuracion:         servidor.Configuracion,
		Metricas:              metricas,
		MejorEpoca:            resultado.MejorEpoca,
		SegundosEntrenamiento: duracion.Seconds(),
	}
	if err := guardarReporte(opciones, reporte); err != nil {
		fmt.Println("Error al guardar el reporte:", err)
		os.Exit(1)
	}
	fmt.Println("Modelo guardado en", opciones.salida)
}

func guardarReporte(opciones opciones, reporte reporteServidor) error {
	if err := os.MkdirAll(opciones.reportes, 0o755); err != nil {
		return err
	}
	contenido, err := json.MarshalIndent(reporte, "", "  ")
	if err != nil {
		return err
	}
	ruta := filepath.Join(opciones.reportes, opciones.nombre+"_servidor.json")
	return os.WriteFile(ruta, contenido, 0o644)
}
