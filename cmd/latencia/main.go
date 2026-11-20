// Programa latencia: mide el tiempo de respuesta de un endpoint HTTP (por
// defecto /predict de la API) con N peticiones y escribe el detalle en CSV y el
// resumen (mediana, p95, mínimo, máximo) en JSON. Las peticiones de
// calentamiento no se registran.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// umbralMs es el requisito de latencia de la API (mediana < 100 ms).
const umbralMs = 100.0

// muestra es el resultado de una petición.
type muestra struct {
	peticion     int
	milisegundos float64
	estado       int // código HTTP; 0 si la petición no llegó a responder
}

// resumen se guarda en latencia_resumen.json.
type resumen struct {
	URL          string  `json:"url"`
	Metodo       string  `json:"metodo"`
	Peticiones   int     `json:"peticiones"`
	Exitosas     int     `json:"exitosas"`
	MedianaMs    float64 `json:"mediana_ms"`
	P95Ms        float64 `json:"p95_ms"`
	MinimoMs     float64 `json:"minimo_ms"`
	MaximoMs     float64 `json:"maximo_ms"`
	UmbralMs     float64 `json:"umbral_ms"`
	CumpleCienMs bool    `json:"cumple_100ms"`
}

// medir envía calentamiento+n peticiones y devuelve las n últimas medidas.
func medir(cliente *http.Client, url, metodo string, cuerpo []byte, n, calentamiento int) []muestra {
	var muestras []muestra
	for indice := 0; indice < calentamiento+n; indice++ {
		peticion, err := http.NewRequest(metodo, url, bytes.NewReader(cuerpo))
		estado := 0
		inicio := time.Now()
		if err == nil {
			peticion.Header.Set("Content-Type", "application/json")
			var respuesta *http.Response
			if respuesta, err = cliente.Do(peticion); err == nil {
				_, _ = io.Copy(io.Discard, respuesta.Body) // incluye la descarga completa en el tiempo
				respuesta.Body.Close()
				estado = respuesta.StatusCode
			}
		}
		milisegundos := float64(time.Since(inicio).Microseconds()) / 1000
		if indice >= calentamiento {
			muestras = append(muestras, muestra{peticion: indice - calentamiento + 1, milisegundos: milisegundos, estado: estado})
		}
	}
	return muestras
}

// percentil usa el método del rango más cercano sobre una lista ordenada.
func percentil(ordenados []float64, p float64) float64 {
	if len(ordenados) == 0 {
		return 0
	}
	posicion := int(math.Ceil(p/100*float64(len(ordenados)))) - 1
	if posicion < 0 {
		posicion = 0
	}
	return ordenados[posicion]
}

// resumir calcula las estadísticas con las peticiones exitosas (2xx).
func resumir(muestras []muestra, url, metodo string) resumen {
	r := resumen{URL: url, Metodo: metodo, Peticiones: len(muestras), UmbralMs: umbralMs}
	var tiempos []float64
	for _, m := range muestras {
		if m.estado >= 200 && m.estado < 300 {
			tiempos = append(tiempos, m.milisegundos)
		}
	}
	r.Exitosas = len(tiempos)
	if len(tiempos) == 0 {
		return r
	}
	sort.Float64s(tiempos)
	n := len(tiempos)
	if n%2 == 1 {
		r.MedianaMs = tiempos[n/2]
	} else {
		r.MedianaMs = (tiempos[n/2-1] + tiempos[n/2]) / 2
	}
	r.P95Ms = percentil(tiempos, 95)
	r.MinimoMs = tiempos[0]
	r.MaximoMs = tiempos[n-1]
	r.CumpleCienMs = r.MedianaMs < umbralMs && r.Exitosas == r.Peticiones
	return r
}

// guardar escribe el CSV de detalle y el JSON de resumen junto a él.
func guardar(carpeta string, muestras []muestra, r resumen) error {
	if err := os.MkdirAll(carpeta, 0o755); err != nil {
		return fmt.Errorf("no se pudo crear la carpeta de salida: %w", err)
	}
	var csv bytes.Buffer
	csv.WriteString("peticion,milisegundos,estado\n")
	for _, m := range muestras {
		fmt.Fprintf(&csv, "%d,%.3f,%d\n", m.peticion, m.milisegundos, m.estado)
	}
	if err := os.WriteFile(filepath.Join(carpeta, "latencia_api.csv"), csv.Bytes(), 0o644); err != nil {
		return err
	}
	contenido, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(carpeta, "latencia_resumen.json"), append(contenido, '\n'), 0o644)
}

func main() {
	url := flag.String("url", "http://localhost:8080/predict", "URL del endpoint a medir")
	n := flag.Int("n", 30, "número de peticiones medidas")
	calentamiento := flag.Int("calentamiento", 3, "peticiones previas que no se registran")
	cuerpo := flag.String("cuerpo", "data/prueba/predicciones.json", "archivo JSON enviado como cuerpo")
	metodo := flag.String("metodo", http.MethodPost, "método HTTP")
	carpeta := flag.String("salida", "reports/pc4", "carpeta de salida (para pruebas usa un nombre que empiece por prueba)")
	flag.Parse()

	if *n < 1 {
		fmt.Println("Error: -n debe ser al menos 1")
		os.Exit(1)
	}
	datos, err := os.ReadFile(*cuerpo)
	if err != nil {
		fmt.Println("Error al leer el cuerpo (genéralo con: mise run data:prueba):", err)
		os.Exit(1)
	}

	fmt.Printf("Midiendo %s %s: %d peticiones (+%d de calentamiento)\n", *metodo, *url, *n, *calentamiento)
	muestras := medir(&http.Client{Timeout: 10 * time.Second}, *url, *metodo, datos, *n, *calentamiento)
	r := resumir(muestras, *url, *metodo)
	fmt.Printf("Exitosas %d/%d | mediana %.2f ms | p95 %.2f ms | mín %.2f ms | máx %.2f ms | cumple < 100 ms: %t\n",
		r.Exitosas, r.Peticiones, r.MedianaMs, r.P95Ms, r.MinimoMs, r.MaximoMs, r.CumpleCienMs)

	if err := guardar(*carpeta, muestras, r); err != nil {
		fmt.Println("Error al guardar:", err)
		os.Exit(1)
	}
	fmt.Println("Resultados guardados en", *carpeta)
	if r.Exitosas == 0 {
		fmt.Println("Error: ninguna petición respondió con éxito; ¿está levantada la API?")
		os.Exit(1)
	}
}
