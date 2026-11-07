// Programa entrenar: carga los CSV de entrenamiento y validación en paralelo,
// entrena una regresión logística local (goroutines, librería logreg),
// elige el umbral de decisión y guarda el modelo y los reportes.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/almeida-aguilar/cs-cc65-proyecto7-vacunacion-ml-2026-20/internal/cargador"
	"github.com/franztoledo/logreg"
)

// opciones agrupa los flags de la línea de comandos.
type opciones struct {
	train, valid     string
	workers, epocas  int
	lote             int
	tasa, l2         float64
	pesoPositivo     float64
	paciencia        int
	sensibilidadMin  float64
	sinColumnas      string
	maxFilas         int
	salida, reportes string
	nombre           string
}

// reporteMetricas es lo que se guarda en <nombre>_metricas.json.
type reporteMetricas struct {
	Configuracion      logreg.Configuracion `json:"configuracion"`
	MetricasValidacion *logreg.Metricas     `json:"metricas_validacion"`
	MetricasPrueba     *logreg.Metricas     `json:"metricas_prueba"`
	Umbral             float64              `json:"umbral"`
	MejorEpoca         int                  `json:"mejor_epoca"`
	SegundosCarga      float64              `json:"segundos_carga"`
	SegundosEntrena    float64              `json:"segundos_entrenamiento"`
	ColumnasExcluidas  []string             `json:"columnas_excluidas"`
	NumVariables       int                  `json:"num_variables"`
	FilasTrain         int                  `json:"filas_train"`
	FilasValidacion    int                  `json:"filas_validacion"`
	FilasPrueba        int                  `json:"filas_prueba"`
	MemoriaMB          memoria              `json:"memoria_mb"`
}

// memoria guarda el uso de memoria del programa, en megabytes.
type memoria struct {
	HeapAlloc float64 `json:"heap_alloc"` // bytes de heap en uso
	Sys       float64 `json:"sys"`        // memoria total pedida al sistema operativo
}

func medirMemoria() memoria {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	const mb = 1024 * 1024
	return memoria{HeapAlloc: float64(m.HeapAlloc) / mb, Sys: float64(m.Sys) / mb}
}

func leerOpciones() opciones {
	var o opciones
	flag.StringVar(&o.train, "train", "data/modelo/train_raw.csv", "CSV de entrenamiento")
	flag.StringVar(&o.valid, "valid", "data/modelo/valid_raw.csv", "CSV de validación")
	flag.IntVar(&o.workers, "workers", 0, "goroutines (0 = todos los núcleos)")
	flag.IntVar(&o.epocas, "epocas", 30, "máximo de épocas")
	flag.IntVar(&o.lote, "lote", 4096, "tamaño de mini-lote (0 = lote completo)")
	flag.Float64Var(&o.tasa, "tasa", 0.1, "tasa de aprendizaje")
	flag.Float64Var(&o.l2, "l2", 0, "regularización L2")
	flag.Float64Var(&o.pesoPositivo, "peso-positivo", 0, "peso de la clase positiva (0 = negativos/positivos del train)")
	flag.IntVar(&o.paciencia, "paciencia", 3, "épocas sin mejora antes de detener")
	flag.Float64Var(&o.sensibilidadMin, "sensibilidad-min", 0.8, "sensibilidad mínima para elegir el umbral")
	flag.StringVar(&o.sinColumnas, "sin-columnas", "", "columnas a excluir, separadas por comas")
	flag.IntVar(&o.maxFilas, "max-filas", 0, "limita las filas leídas de cada archivo (0 = todas)")
	flag.StringVar(&o.salida, "salida", "modelo.json", "archivo donde se guarda el modelo")
	flag.StringVar(&o.reportes, "reportes", "reports/go", "carpeta de reportes")
	flag.StringVar(&o.nombre, "nombre", "completo", "prefijo de los archivos de reporte")
	flag.Parse()
	return o
}

func main() {
	o := leerOpciones()

	var excluir []string
	if o.sinColumnas != "" {
		excluir = strings.Split(o.sinColumnas, ",")
	}

	// 1) Carga concurrente de train y valid
	fmt.Println("Cargando datos...")
	inicioCarga := time.Now()
	train, columnas, resumenTrain, err := cargador.CargarCSV(o.train, o.workers, excluir, o.maxFilas)
	if err != nil {
		fmt.Println("Error al cargar train:", err)
		return
	}
	imprimirResumen("train", o.train, resumenTrain)
	valid, columnasValid, resumenValid, err := cargador.CargarCSV(o.valid, o.workers, excluir, o.maxFilas)
	if err != nil {
		fmt.Println("Error al cargar valid:", err)
		return
	}
	imprimirResumen("valid", o.valid, resumenValid)
	segundosCarga := time.Since(inicioCarga).Seconds()

	// 2) train y valid deben tener exactamente las mismas columnas
	if !mismasColumnas(columnas, columnasValid) {
		fmt.Println("Error: train y valid tienen columnas distintas")
		return
	}
	fmt.Printf("Variables usadas: %d (excluidas: %v)\n", len(columnas), excluir)

	// valid se divide en dos mitades: "validacion" decide la parada temprana y
	// el umbral; "prueba" no se usa para decidir nada y solo sirve para medir.
	validacion, prueba := valid.Dividir(0.5, 42)
	fmt.Printf("Validación: %d filas | Prueba: %d filas\n", validacion.Len(), prueba.Len())

	// 3) Desbalance de clases
	proporcion := train.ProporcionPositivos()
	peso := o.pesoPositivo
	if peso == 0 {
		peso = 1
		if proporcion > 0 {
			peso = (1 - proporcion) / proporcion // negativos / positivos
		}
	}
	fmt.Printf("Positivos en train: %.2f%% | peso de la clase positiva: %.3f\n", proporcion*100, peso)

	// 4) Configuración
	cfg := logreg.ConfiguracionPorDefecto()
	cfg.Epocas = o.epocas
	cfg.TamanoLote = o.lote
	cfg.TasaAprendizaje = o.tasa
	cfg.L2 = o.l2
	cfg.Workers = o.workers
	cfg.PesoClasePositiva = peso
	cfg.Validacion = validacion
	cfg.Paciencia = o.paciencia
	cfg.MejoraMinima = 1e-4

	// 5) Una línea por época
	cfg.AlProgreso = func(p logreg.Progreso) {
		marca := ""
		if p.EsMejor {
			marca = "*"
		}
		perdidaValid, aucValid := math.NaN(), math.NaN()
		if p.Validacion != nil {
			perdidaValid, aucValid = p.Validacion.PerdidaLog, p.Validacion.AUC
		}
		fmt.Printf("Epoca %2d | perdida train %.4f | perdida valid %.4f | AUC valid %.4f | %v %s\n",
			p.Epoca, p.Perdida, perdidaValid, aucValid, p.Duracion.Round(time.Millisecond), marca)
	}

	// 6) Entrenamiento
	fmt.Println("\nEntrenando...")
	resultado, err := logreg.Entrenar(context.Background(), train, cfg)
	if err != nil {
		fmt.Println("Error al entrenar:", err)
		return
	}
	modelo := resultado.Modelo

	// El umbral se elige solo con la mitad de validación.
	probabilidades, err := modelo.ProbabilidadLote(validacion.X, o.workers)
	if err != nil {
		fmt.Println("Error al calcular probabilidades:", err)
		return
	}
	umbral, _ := logreg.MejorUmbralSegun(probabilidades, validacion.Y, logreg.CriterioSensibilidadMinima(o.sensibilidadMin))
	modelo.Umbral = umbral

	metricasValidacion, err := logreg.Evaluar(modelo, validacion, o.workers)
	if err != nil {
		fmt.Println("Error al evaluar en validación:", err)
		return
	}
	metricasPrueba, err := logreg.Evaluar(modelo, prueba, o.workers)
	if err != nil {
		fmt.Println("Error al evaluar en prueba:", err)
		return
	}
	fmt.Printf("\nEntrenamiento terminado en %v (mejor época: %d)\n", resultado.DuracionTotal.Round(time.Millisecond), resultado.MejorEpoca)
	fmt.Printf("Umbral elegido (con validación): %.2f\n", umbral)
	imprimirMetricas("Métricas en validación (usadas para parada temprana y umbral)", metricasValidacion)
	imprimirMetricas("Métricas en prueba (datos no usados para elegir nada)", metricasPrueba)

	// 7) Variables más influyentes
	imprimirTopPesos(modelo, columnas, 10)

	// 8) Advertencia de posible fuga de datos (sobre prueba)
	if metricasPrueba.AUC > 0.95 {
		fmt.Printf("\nADVERTENCIA: AUC en prueba = %.4f > 0.95. Posible fuga de datos: revisa que ninguna variable contenga información del resultado.\n", metricasPrueba.AUC)
	}

	// Memoria al final del programa
	mem := medirMemoria()
	fmt.Printf("\nMemoria: HeapAlloc %.1f MB | Sys %.1f MB\n", mem.HeapAlloc, mem.Sys)

	// 9) Guardar modelo y reportes
	modelo.NombresVariables = columnas
	if err := modelo.Guardar(o.salida); err != nil {
		fmt.Println("Error al guardar el modelo:", err)
		return
	}
	fmt.Println("\nModelo guardado en", o.salida)

	reporte := reporteMetricas{
		Configuracion:      cfg,
		MetricasValidacion: metricasValidacion,
		MetricasPrueba:     metricasPrueba,
		MemoriaMB:          mem,
		Umbral:             umbral,
		MejorEpoca:         resultado.MejorEpoca,
		SegundosCarga:      segundosCarga,
		SegundosEntrena:    resultado.DuracionTotal.Seconds(),
		ColumnasExcluidas:  excluir,
		NumVariables:       len(columnas),
		FilasTrain:         train.Len(),
		FilasValidacion:    validacion.Len(),
		FilasPrueba:        prueba.Len(),
	}
	if err := guardarReportes(o, reporte, resultado.Historial); err != nil {
		fmt.Println("Error al guardar los reportes:", err)
		return
	}
}

func imprimirResumen(nombre, ruta string, r cargador.Resumen) {
	fmt.Printf("  %s (%s): leídas %d | válidas %d | rechazadas %d | %v\n",
		nombre, ruta, r.FilasLeidas, r.FilasValidas, r.FilasRechazadas, r.Duracion.Round(time.Millisecond))
}

func mismasColumnas(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func imprimirMetricas(titulo string, m *logreg.Metricas) {
	fmt.Printf("\n%s:\n", titulo)
	fmt.Printf("  Exactitud     %.4f\n", m.Exactitud)
	fmt.Printf("  Precisión     %.4f\n", m.Precision)
	fmt.Printf("  Sensibilidad  %.4f\n", m.Sensibilidad)
	fmt.Printf("  Especificidad %.4f\n", m.Especificidad)
	fmt.Printf("  F1            %.4f\n", m.F1)
	fmt.Printf("  AUC           %.4f\n", m.AUC)
	fmt.Printf("  Pérdida log   %.4f\n", m.PerdidaLog)
	fmt.Println("\nMatriz de confusión:")
	fmt.Println("                  Pred. 0      Pred. 1")
	fmt.Printf("  Real 0 (no deserta) %9d  %9d\n", m.Matriz.VN, m.Matriz.FP)
	fmt.Printf("  Real 1 (deserta)    %9d  %9d\n", m.Matriz.FN, m.Matriz.VP)
}

// imprimirTopPesos muestra las variables con mayor |peso|. Los pesos están
// sobre datos escalados, así que sus magnitudes son comparables entre sí.
func imprimirTopPesos(modelo *logreg.Modelo, columnas []string, cantidad int) {
	orden := make([]int, len(columnas))
	for i := range orden {
		orden[i] = i
	}
	sort.Slice(orden, func(a, b int) bool {
		return math.Abs(modelo.Pesos[orden[a]]) > math.Abs(modelo.Pesos[orden[b]])
	})
	if cantidad > len(orden) {
		cantidad = len(orden)
	}
	fmt.Printf("\nTop %d variables por |peso|:\n", cantidad)
	for _, i := range orden[:cantidad] {
		efecto := "baja el riesgo"
		if modelo.Pesos[i] > 0 {
			efecto = "sube el riesgo"
		}
		fmt.Printf("  %-28s %+8.4f  %s\n", columnas[i], modelo.Pesos[i], efecto)
	}
}

func guardarReportes(o opciones, reporte reporteMetricas, historial []logreg.Progreso) error {
	if err := os.MkdirAll(o.reportes, 0o755); err != nil {
		return err
	}
	contenido, err := json.MarshalIndent(reporte, "", "  ")
	if err != nil {
		return err
	}
	rutaJSON := filepath.Join(o.reportes, o.nombre+"_metricas.json")
	if err := os.WriteFile(rutaJSON, contenido, 0o644); err != nil {
		return err
	}

	var sb strings.Builder
	sb.WriteString("epoca,perdida_train,perdida_valid,auc_valid,duracion_ms\n")
	for _, p := range historial {
		perdidaValid, aucValid := 0.0, 0.0
		if p.Validacion != nil {
			perdidaValid, aucValid = p.Validacion.PerdidaLog, p.Validacion.AUC
		}
		fmt.Fprintf(&sb, "%d,%.6f,%.6f,%.6f,%d\n", p.Epoca, p.Perdida, perdidaValid, aucValid, p.Duracion.Milliseconds())
	}
	rutaCSV := filepath.Join(o.reportes, o.nombre+"_historial.csv")
	if err := os.WriteFile(rutaCSV, []byte(sb.String()), 0o644); err != nil {
		return err
	}
	fmt.Println("Reportes guardados en", rutaJSON, "y", rutaCSV)
	return nil
}
