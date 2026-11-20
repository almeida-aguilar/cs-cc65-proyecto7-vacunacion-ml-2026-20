// Programa latencia: mide la latencia de POST /predict contra la API en
// marcha y escribe mediana, p95 y máximo en un CSV (evidencia del PC4: la API
// debe responder en menos de 100 ms).
package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// opciones agrupa los flags de la línea de comandos.
type opciones struct {
	url, salida   string
	peticiones    int
	calentamiento int
}

func leerOpciones() opciones {
	var o opciones
	flag.StringVar(&o.url, "url", "http://localhost:8080", "dirección base de la API")
	flag.StringVar(&o.salida, "salida", "reports/pc4/latencia_api.csv", "CSV de salida")
	flag.IntVar(&o.peticiones, "n", 100, "peticiones medidas (mínimo recomendado: 30)")
	flag.IntVar(&o.calentamiento, "calentamiento", 10, "peticiones previas que no se miden")
	flag.Parse()
	return o
}

// variablesDelModelo consulta /health para saber cuántas variables espera la API.
func variablesDelModelo(cliente *http.Client, url string) (int, error) {
	respuesta, err := cliente.Get(url + "/health")
	if err != nil {
		return 0, fmt.Errorf("no se pudo consultar /health: %w", err)
	}
	defer respuesta.Body.Close()
	var salud struct {
		ModeloCargado bool `json:"modelo_cargado"`
		Variables     int  `json:"variables"`
	}
	if err := json.NewDecoder(respuesta.Body).Decode(&salud); err != nil {
		return 0, fmt.Errorf("respuesta de /health inválida: %w", err)
	}
	if !salud.ModeloCargado {
		return 0, fmt.Errorf("la API no tiene un modelo cargado: entrena uno con POST /train")
	}
	return salud.Variables, nil
}

// predecir envía una petición y devuelve la latencia extremo a extremo.
func predecir(cliente *http.Client, url string, cuerpo []byte) (time.Duration, error) {
	inicio := time.Now()
	respuesta, err := cliente.Post(url+"/predict", "application/json", bytes.NewReader(cuerpo))
	if err != nil {
		return 0, err
	}
	defer respuesta.Body.Close()
	_, _ = io.Copy(io.Discard, respuesta.Body)
	duracion := time.Since(inicio)
	if respuesta.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("estado HTTP %d", respuesta.StatusCode)
	}
	return duracion, nil
}

func percentil(ordenados []float64, p float64) float64 {
	indice := int(p*float64(len(ordenados))+0.5) - 1
	if indice < 0 {
		indice = 0
	}
	if indice >= len(ordenados) {
		indice = len(ordenados) - 1
	}
	return ordenados[indice]
}

func main() {
	opciones := leerOpciones()
	if opciones.peticiones <= 0 {
		fmt.Println("Error: -n debe ser mayor que cero")
		os.Exit(1)
	}
	// Un cliente reutilizado mantiene la conexión abierta (keep-alive), como lo
	// haría Postman o la SPA.
	cliente := &http.Client{Timeout: 5 * time.Second}

	variables, err := variablesDelModelo(cliente, opciones.url)
	if err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
	entrada := make([]float64, variables)
	for indice := range entrada {
		entrada[indice] = float64(indice%7) / 10
	}
	cuerpo, _ := json.Marshal(map[string]any{"entrada": entrada})

	for intento := 0; intento < opciones.calentamiento; intento++ {
		if _, err := predecir(cliente, opciones.url, cuerpo); err != nil {
			fmt.Println("Error en el calentamiento:", err)
			os.Exit(1)
		}
	}
	milisegundos := make([]float64, 0, opciones.peticiones)
	for intento := 0; intento < opciones.peticiones; intento++ {
		duracion, err := predecir(cliente, opciones.url, cuerpo)
		if err != nil {
			fmt.Println("Error en la petición", intento+1, ":", err)
			os.Exit(1)
		}
		milisegundos = append(milisegundos, float64(duracion)/float64(time.Millisecond))
	}
	sort.Float64s(milisegundos)
	mediana := percentil(milisegundos, 0.5)
	p95 := percentil(milisegundos, 0.95)
	maxima := milisegundos[len(milisegundos)-1]

	if err := os.MkdirAll(filepath.Dir(opciones.salida), 0o755); err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
	archivo, err := os.Create(opciones.salida)
	if err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
	defer archivo.Close()
	escritor := csv.NewWriter(archivo)
	formato := func(valor float64) string { return strconv.FormatFloat(valor, 'f', 3, 64) }
	_ = escritor.Write([]string{"endpoint", "peticiones", "variables", "mediana_ms", "p95_ms", "maxima_ms", "cumple_100ms"})
	_ = escritor.Write([]string{"POST /predict", strconv.Itoa(len(milisegundos)), strconv.Itoa(variables),
		formato(mediana), formato(p95), formato(maxima), strconv.FormatBool(mediana < 100)})
	escritor.Flush()
	if err := escritor.Error(); err != nil {
		fmt.Println("Error al escribir el CSV:", err)
		os.Exit(1)
	}

	fmt.Printf("POST /predict (%d peticiones, %d variables): mediana %.3f ms | p95 %.3f ms | máxima %.3f ms\n",
		len(milisegundos), variables, mediana, p95, maxima)
	fmt.Println("CSV guardado en", opciones.salida)
}
