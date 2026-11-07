// Paquete cargador lee un CSV numérico grande de forma concurrente y lo
// convierte en un logreg.Dataset.
//
// Patrón usado: productor -> trabajadores -> consolidador.
//
//	productor (1 goroutine)  --bloques-->  trabajadores (N goroutines)  --resultados-->  consolidador (main)
//
// El archivo tiene ~1,2 millones de filas. Leer el disco es secuencial, pero
// convertir texto a números (ParseFloat) es la parte costosa y sí se puede
// repartir entre varios núcleos.
package cargador

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/franztoledo/logreg"
)

// TamanoBloque es la cantidad de líneas que el productor entrega a un
// trabajador de una sola vez. Con bloques grandes se reduce el costo de
// comunicar por el canal (una operación por 10.000 filas y no por fila).
const TamanoBloque = 10000

// Detallado activa mensajes por bloque. Está apagado por defecto para no
// inundar la consola con ~120 mensajes por archivo.
var Detallado = false

// Resumen de la carga (para mostrar como evidencia).
type Resumen struct {
	FilasLeidas     int
	FilasValidas    int
	FilasRechazadas int
	Duracion        time.Duration
}

// bloque es un grupo de líneas de texto con su número de orden.
type bloque struct {
	indice int
	lineas []string
}

// resultadoBloque es lo que devuelve un trabajador al terminar un bloque.
type resultadoBloque struct {
	indice     int
	X          [][]float64
	Y          []float64
	rechazadas int
}

// CargarCSV carga un CSV con encabezado, separado por comas, todo numérico y
// con la etiqueta (0/1) en la última columna.
//
//   - workers <= 0 usa runtime.NumCPU().
//   - excluir son nombres de columnas que no se usarán como variables.
//   - maxFilas > 0 limita la cantidad de filas de datos leídas (0 = todas).
//
// Devuelve el dataset, los nombres de las variables usadas, el resumen y un error.
func CargarCSV(ruta string, workers int, excluir []string, maxFilas int) (*logreg.Dataset, []string, Resumen, error) {
	var resumen Resumen
	inicio := time.Now()

	if workers <= 0 {
		workers = runtime.NumCPU()
	}

	archivo, err := os.Open(ruta)
	if err != nil {
		return nil, nil, resumen, fmt.Errorf("no se pudo abrir %s: %w", ruta, err)
	}
	defer archivo.Close()

	lector := bufio.NewScanner(archivo)
	lector.Buffer(make([]byte, 1024*1024), 1024*1024) // buffer de 1 MB

	// El encabezado se lee antes de lanzar goroutines: así los errores de
	// columnas se devuelven de inmediato y no hay nada que limpiar.
	if !lector.Scan() {
		if err := lector.Err(); err != nil {
			return nil, nil, resumen, fmt.Errorf("error leyendo %s: %w", ruta, err)
		}
		return nil, nil, resumen, fmt.Errorf("el archivo %s está vacío", ruta)
	}
	cabecera := nombresColumnas(lector.Text())
	usadas, columnas, err := columnasUsadas(cabecera, excluir)
	if err != nil {
		return nil, nil, resumen, err
	}

	bloques := make(chan bloque, workers)             // productor -> trabajadores
	resultados := make(chan resultadoBloque, workers) // trabajadores -> consolidador
	errLectura := make(chan error, 1)                 // con buffer: el productor no se bloquea al avisar

	// 1) PRODUCTOR: una sola goroutine lee el archivo (el disco es secuencial)
	// y reparte bloques de líneas.
	go func() {
		// Al terminar se cierra "bloques": avisa a los trabajadores que ya no
		// llegará más trabajo, así su "for range" termina solo.
		defer close(bloques)
		lineas := make([]string, 0, TamanoBloque)
		numero := 0
		leidas := 0
		for lector.Scan() {
			linea := lector.Text()
			if strings.TrimSpace(linea) == "" {
				continue // las líneas vacías (ej. salto final) no son filas
			}
			lineas = append(lineas, linea)
			leidas++
			if len(lineas) == TamanoBloque {
				bloques <- bloque{indice: numero, lineas: lineas}
				numero++
				lineas = make([]string, 0, TamanoBloque) // nuevo slice: el anterior ya es del trabajador
			}
			if maxFilas > 0 && leidas >= maxFilas {
				break
			}
		}
		if len(lineas) > 0 {
			bloques <- bloque{indice: numero, lineas: lineas}
		}
		// Se guarda el error ANTES de cerrar "bloques" (defer): así el
		// consolidador lo lee con seguridad cuando "resultados" se cierre.
		if err := lector.Err(); err != nil {
			errLectura <- err
		}
	}()

	// 2) TRABAJADORES: cada uno toma bloques del canal hasta que se cierre.
	var wg sync.WaitGroup
	for id := 1; id <= workers; id++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for b := range bloques {
				// Cada trabajador construye su PROPIO resultado y lo envía por
				// el canal. Nadie escribe en memoria compartida, por eso no
				// hace falta un Mutex.
				r := procesarBloque(b, len(cabecera), usadas)
				if Detallado {
					fmt.Printf("Trabajador %d : proceso bloque %d (%d filas) \n", id, b.indice, len(b.lineas))
				}
				resultados <- r
			}
		}(id)
	}

	// 3) Esta goroutine espera a TODOS los trabajadores y recién entonces
	// cierra "resultados". Si lo cerrara un trabajador, otro podría seguir
	// enviando a un canal cerrado (panic). Debe ser una goroutine aparte
	// porque main está ocupada consumiendo "resultados".
	go func() {
		wg.Wait()
		close(resultados)
	}()

	// 4) CONSOLIDADOR (goroutine principal): los bloques llegan en cualquier
	// orden, así que se guardan y luego se ordenan por índice.
	var partes []resultadoBloque
	for r := range resultados {
		partes = append(partes, r)
	}

	select {
	case err := <-errLectura:
		return nil, nil, resumen, fmt.Errorf("error leyendo %s: %w", ruta, err)
	default:
	}

	sort.Slice(partes, func(i, j int) bool { return partes[i].indice < partes[j].indice })

	var X [][]float64
	var Y []float64
	for _, p := range partes {
		X = append(X, p.X...)
		Y = append(Y, p.Y...)
		resumen.FilasRechazadas += p.rechazadas
	}
	resumen.FilasValidas = len(X)
	resumen.FilasLeidas = resumen.FilasValidas + resumen.FilasRechazadas

	datos, err := logreg.NuevoDataset(X, Y)
	if err != nil {
		return nil, nil, resumen, fmt.Errorf("no se pudo crear el dataset: %w", err)
	}
	resumen.Duracion = time.Since(inicio)
	return datos, columnas, resumen, nil
}

// nombresColumnas separa el encabezado y quita el BOM y el '\r'.
func nombresColumnas(linea string) []string {
	linea = strings.TrimPrefix(linea, "\xef\xbb\xbf")
	linea = strings.TrimRight(linea, "\r")
	nombres := strings.Split(linea, ",")
	for i := range nombres {
		nombres[i] = strings.TrimSpace(nombres[i])
	}
	return nombres
}

// columnasUsadas devuelve las posiciones de las columnas que serán variables
// y sus nombres. La última columna es la etiqueta y nunca es variable.
func columnasUsadas(cabecera []string, excluir []string) ([]int, []string, error) {
	if len(cabecera) < 2 {
		return nil, nil, fmt.Errorf("el encabezado necesita al menos una variable y la etiqueta")
	}
	etiqueta := len(cabecera) - 1
	descartar := make(map[string]bool)
	for _, nombre := range excluir {
		nombre = strings.TrimSpace(nombre)
		if nombre == "" {
			continue
		}
		existe := false
		for i, c := range cabecera {
			if c == nombre {
				existe = true
				if i == etiqueta {
					return nil, nil, fmt.Errorf("no se puede excluir la etiqueta %q", nombre)
				}
			}
		}
		if !existe {
			return nil, nil, fmt.Errorf("la columna a excluir %q no existe en el archivo", nombre)
		}
		descartar[nombre] = true
	}

	var usadas []int
	var columnas []string
	for i := 0; i < etiqueta; i++ {
		if !descartar[cabecera[i]] {
			usadas = append(usadas, i)
			columnas = append(columnas, cabecera[i])
		}
	}
	if len(usadas) == 0 {
		return nil, nil, fmt.Errorf("no quedan variables después de excluir columnas")
	}
	return usadas, columnas, nil
}

// procesarBloque convierte las líneas de un bloque en números. Las filas
// malformadas se cuentan como rechazadas y no detienen la carga.
func procesarBloque(b bloque, numColumnas int, usadas []int) resultadoBloque {
	r := resultadoBloque{indice: b.indice}
	r.X = make([][]float64, 0, len(b.lineas))
	r.Y = make([]float64, 0, len(b.lineas))
	for _, linea := range b.lineas {
		fila, etiqueta, ok := parsearFila(linea, numColumnas, usadas)
		if !ok {
			r.rechazadas++
			continue
		}
		r.X = append(r.X, fila)
		r.Y = append(r.Y, etiqueta)
	}
	return r
}

// parsearFila devuelve las variables y la etiqueta, o ok=false si la fila es inválida.
func parsearFila(linea string, numColumnas int, usadas []int) ([]float64, float64, bool) {
	linea = strings.TrimRight(linea, "\r")
	campos := strings.Split(linea, ",")
	if len(campos) != numColumnas {
		return nil, 0, false
	}
	fila := make([]float64, len(usadas))
	for k, pos := range usadas {
		valor, ok := parsearNumero(campos[pos])
		if !ok {
			return nil, 0, false
		}
		fila[k] = valor
	}
	etiqueta, ok := parsearNumero(campos[numColumnas-1])
	if !ok || (etiqueta != 0 && etiqueta != 1) {
		return nil, 0, false
	}
	return fila, etiqueta, true
}

// parsearNumero convierte texto a float64 y rechaza NaN e infinitos.
func parsearNumero(texto string) (float64, bool) {
	valor, err := strconv.ParseFloat(strings.TrimSpace(texto), 64)
	if err != nil || math.IsNaN(valor) || math.IsInf(valor, 0) {
		return 0, false
	}
	return valor, true
}
