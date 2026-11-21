// Programa seed: inserta en MongoDB registros de prueba (una ejecución con sus
// métricas por época y varias predicciones) para mostrar el diseño de
// entidades del PC4 con mongosh. Usa por defecto la base "vacunas_prueba" para
// no mezclarse con datos reales.
package main

import (
	"context"
	"encoding/csv"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"strconv"
	"time"

	"github.com/almeida-aguilar/cs-cc65-proyecto7-vacunacion-ml-2026-20/internal/almacen"
	"github.com/franztoledo/logreg"
)

// opciones agrupa los flags de la línea de comandos.
type opciones struct {
	uri, base, historial string
	predicciones         int
}

func leerOpciones() opciones {
	var o opciones
	flag.StringVar(&o.uri, "mongo", "mongodb://localhost:27017", "URI de conexión a MongoDB")
	flag.StringVar(&o.base, "base", "vacunas_prueba", "nombre de la base de datos")
	flag.StringVar(&o.historial, "historial", "reports/go/completo_historial.csv", "historial real de entrenamiento (si no existe, se simula)")
	flag.IntVar(&o.predicciones, "predicciones", 20, "cantidad de predicciones de prueba")
	flag.Parse()
	return o
}

// historialEjemplo lee el historial real de Go; si falta, genera uno simulado.
func historialEjemplo(ruta string) []logreg.Progreso {
	archivo, err := os.Open(ruta)
	if err == nil {
		defer archivo.Close()
		filas, errLectura := csv.NewReader(archivo).ReadAll()
		if errLectura == nil && len(filas) > 1 {
			var historial []logreg.Progreso
			for _, fila := range filas[1:] { // epoca,perdida_train,perdida_valid,auc_valid,duracion_ms
				epoca, _ := strconv.Atoi(fila[0])
				perdida, _ := strconv.ParseFloat(fila[1], 64)
				milisegundos, _ := strconv.Atoi(fila[4])
				historial = append(historial, logreg.Progreso{
					Epoca: epoca, Perdida: perdida, Nodos: 2,
					Duracion: time.Duration(milisegundos) * time.Millisecond,
				})
			}
			return historial
		}
	}
	fmt.Println("Aviso: no se pudo leer", ruta, "- se usa un historial simulado")
	var historial []logreg.Progreso
	for epoca := 1; epoca <= 6; epoca++ {
		historial = append(historial, logreg.Progreso{
			Epoca: epoca, Perdida: 0.44 + 0.25/float64(epoca), Nodos: 2, Duracion: 130 * time.Millisecond,
		})
	}
	return historial
}

func main() {
	opciones := leerOpciones()
	ctx, cancelar := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelar()

	repositorio, err := almacen.NuevoMongo(ctx, opciones.uri, opciones.base)
	if err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
	defer repositorio.Cerrar(ctx)

	historial := historialEjemplo(opciones.historial)
	inicio := time.Now().UTC().Add(-time.Minute)
	var segundos float64
	for _, progreso := range historial {
		segundos += progreso.Duracion.Seconds()
	}
	ejecucion := &almacen.Ejecucion{
		Estado:        almacen.EstadoTerminada,
		Configuracion: almacen.ConfiguracionDesde(logreg.ConfiguracionPorDefecto()),
		Nodos:         2,
		FilasTotales:  1000000,
		MejorEpoca:    len(historial),
		Segundos:      segundos,
		Inicio:        inicio,
		Fin:           inicio.Add(time.Duration(segundos * float64(time.Second))),
	}
	if err := repositorio.InsertarEjecucion(ctx, ejecucion); err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
	for _, progreso := range historial {
		metrica := &almacen.Metrica{
			EjecucionID: ejecucion.ID,
			Epoca:       progreso.Epoca,
			Perdida:     progreso.Perdida,
			Nodos:       progreso.Nodos,
			DuracionMs:  float64(progreso.Duracion.Milliseconds()),
			EsMejor:     progreso.Epoca == len(historial),
			Fecha:       inicio.Add(time.Duration(progreso.Epoca) * time.Second),
		}
		if err := repositorio.InsertarMetrica(ctx, metrica); err != nil {
			fmt.Println("Error:", err)
			os.Exit(1)
		}
	}

	generador := rand.New(rand.NewSource(42))
	for indice := 0; indice < opciones.predicciones; indice++ {
		entrada := make([]float64, 5)
		for posicion := range entrada {
			entrada[posicion] = math.Round(generador.Float64()*100) / 100
		}
		probabilidad := generador.Float64()
		clase := 0
		if probabilidad >= 0.48 {
			clase = 1
		}
		prediccion := &almacen.Prediccion{
			EjecucionID:  ejecucion.ID,
			Entrada:      entrada,
			HashEntrada:  almacen.HashEntrada(entrada),
			Probabilidad: probabilidad,
			Clase:        clase,
			Umbral:       0.48,
			LatenciaMs:   1 + generador.Float64()*4,
			Origen:       almacen.OrigenCalculo,
			Fecha:        time.Now().UTC().Add(time.Duration(indice) * time.Millisecond),
		}
		if err := repositorio.InsertarPrediccion(ctx, prediccion); err != nil {
			fmt.Println("Error:", err)
			os.Exit(1)
		}
	}

	fmt.Printf("Base %q: ejecución %s, %d métricas y %d predicciones insertadas\n",
		opciones.base, ejecucion.ID, len(historial), opciones.predicciones)
}
