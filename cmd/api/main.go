// Programa api: servidor HTTP que coordina el clúster de nodos ML, expone la
// predicción y persiste resultados en MongoDB. Ver /docs para Swagger.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/almeida-aguilar/cs-cc65-proyecto7-vacunacion-ml-2026-20/internal/almacen"
	"github.com/almeida-aguilar/cs-cc65-proyecto7-vacunacion-ml-2026-20/internal/api"
)

// opciones agrupa los flags de la línea de comandos.
type opciones struct {
	direccion, modelo, nodos, mongo, base string
}

func leerOpciones() opciones {
	var o opciones
	flag.StringVar(&o.direccion, "addr", ":8080", "dirección donde escucha la API")
	flag.StringVar(&o.modelo, "modelo", "modelo.json", "archivo del modelo (se carga al iniciar y se reescribe al entrenar)")
	flag.StringVar(&o.nodos, "nodos", "", "direcciones TCP de los nodos ML, separadas por comas (ej. nodo1:9001,nodo2:9001)")
	flag.StringVar(&o.mongo, "mongo", "", "URI de MongoDB (vacío = repositorio en memoria, sin persistencia)")
	flag.StringVar(&o.base, "base", "vacunas", "nombre de la base de datos de MongoDB")
	flag.Parse()
	return o
}

func main() {
	opciones := leerOpciones()
	ctx := context.Background()

	var repositorio almacen.Repositorio
	if opciones.mongo != "" {
		mongo, err := almacen.NuevoMongo(ctx, opciones.mongo, opciones.base)
		if err != nil {
			fmt.Println("Error:", err)
			os.Exit(1)
		}
		defer mongo.Cerrar(ctx)
		repositorio = mongo
		log.Printf("persistencia en MongoDB (base %q)", opciones.base)
	} else {
		log.Println("Aviso: sin -mongo, los datos viven solo en memoria y se pierden al detener la API")
	}

	var nodos []string
	for _, direccion := range strings.Split(opciones.nodos, ",") {
		if direccion = strings.TrimSpace(direccion); direccion != "" {
			nodos = append(nodos, direccion)
		}
	}

	servidor, err := api.NuevoServidor(api.Opciones{
		RutaModelo: opciones.modelo,
		Nodos:      nodos,
		Repo:       repositorio,
	})
	if err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}

	servidorHTTP := &http.Server{
		Addr:              opciones.direccion,
		Handler:           servidor.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	// Cierre ordenado: primero se deja de aceptar peticiones y luego se vacía
	// la cola de predicciones pendientes de guardar.
	senales := make(chan os.Signal, 1)
	signal.Notify(senales, os.Interrupt, syscall.SIGTERM)
	terminado := make(chan struct{})
	go func() {
		defer close(terminado)
		<-senales
		log.Println("cerrando la API...")
		ctxCierre, cancelar := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelar()
		if err := servidorHTTP.Shutdown(ctxCierre); err != nil {
			log.Println("error al cerrar el servidor HTTP:", err)
		}
		servidor.Cerrar()
	}()

	log.Printf("API escuchando en %s (nodos: %v) — documentación en /docs", opciones.direccion, nodos)
	if err := servidorHTTP.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
	<-terminado
}
