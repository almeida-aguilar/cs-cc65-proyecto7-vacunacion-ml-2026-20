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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/almeida-aguilar/cs-cc65-proyecto7-vacunacion-ml-2026-20/internal/almacen"
	"github.com/almeida-aguilar/cs-cc65-proyecto7-vacunacion-ml-2026-20/internal/api"
	"github.com/almeida-aguilar/cs-cc65-proyecto7-vacunacion-ml-2026-20/internal/cache"
)

// opciones agrupa los flags de la línea de comandos.
type opciones struct {
	direccion, modelo, nodos, mongo, base, redis string
	ttlRedis                                     time.Duration
	entrenamiento                                api.DefectosEntrenamiento
}

// entorno devuelve la variable de entorno o, si está vacía, el valor por defecto.
// Docker Compose configura la API con variables (docker-compose.yml); los flags
// tienen prioridad sobre ellas.
func entorno(nombre, porDefecto string) string {
	if valor := os.Getenv(nombre); valor != "" {
		return valor
	}
	return porDefecto
}

// direccionDeEscucha usa PUERTO_API (solo el número) cuando está definida.
func direccionDeEscucha() string {
	if puerto := os.Getenv("PUERTO_API"); puerto != "" {
		return ":" + puerto
	}
	return ":8080"
}

// duracionDeEntorno lee una duración (300s, 5m); si falta o es inválida usa el respaldo.
func duracionDeEntorno(nombre string, respaldo time.Duration) time.Duration {
	if duracion, err := time.ParseDuration(os.Getenv(nombre)); err == nil && duracion > 0 {
		return duracion
	}
	return respaldo
}

// enteroDeEntorno lee un entero positivo; 0 si falta o es inválido.
func enteroDeEntorno(nombre string) int {
	if valor, err := strconv.Atoi(os.Getenv(nombre)); err == nil && valor > 0 {
		return valor
	}
	return 0
}

// realDeEntorno lee un real positivo; 0 si falta o es inválido.
func realDeEntorno(nombre string) float64 {
	if valor, err := strconv.ParseFloat(os.Getenv(nombre), 64); err == nil && valor > 0 {
		return valor
	}
	return 0
}

func leerOpciones() opciones {
	var o opciones
	flag.StringVar(&o.direccion, "addr", direccionDeEscucha(), "dirección donde escucha la API (env PUERTO_API)")
	flag.StringVar(&o.modelo, "modelo", entorno("MODELO", "modelo.json"), "archivo del modelo, se carga al iniciar y se reescribe al entrenar (env MODELO)")
	flag.StringVar(&o.nodos, "nodos", entorno("NODOS", ""), "direcciones TCP de los nodos ML, separadas por comas (env NODOS, ej. nodo1:9001,nodo2:9001)")
	flag.StringVar(&o.mongo, "mongo", entorno("MONGO_URI", ""), "URI de MongoDB; vacío = repositorio en memoria (env MONGO_URI)")
	flag.StringVar(&o.base, "base", entorno("MONGO_BASE", "vacunas"), "nombre de la base de datos de MongoDB (env MONGO_BASE)")
	flag.StringVar(&o.redis, "redis", entorno("REDIS_URL", ""), "URL de Redis para la caché; vacío = sin caché (env REDIS_URL)")
	flag.DurationVar(&o.ttlRedis, "redis-ttl", duracionDeEntorno("REDIS_TTL", cache.TTLPrediccionPorDefecto), "vigencia de las predicciones en caché (env REDIS_TTL, ej. 300s)")
	flag.IntVar(&o.entrenamiento.Epocas, "epocas", enteroDeEntorno("EPOCAS"), "épocas por defecto de POST /train (env EPOCAS; 0 = 10)")
	flag.IntVar(&o.entrenamiento.Lote, "lote", enteroDeEntorno("LOTE"), "tamaño de lote por defecto de POST /train (env LOTE; 0 = 1024)")
	flag.Float64Var(&o.entrenamiento.PesoPositivo, "peso-positivo", realDeEntorno("PESO_POSITIVO"), "peso de la clase positiva por defecto de POST /train (env PESO_POSITIVO; 0 = 12.43)")
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

	// Redis es opcional: si no responde, la API sigue (la caché se suspende sola).
	var memoria api.Cache
	if opciones.redis != "" {
		redis, err := cache.NuevoRedis(opciones.redis, cache.Opciones{TTL: opciones.ttlRedis, Log: log.Printf})
		if err != nil {
			fmt.Println("Error:", err)
			os.Exit(1)
		}
		defer redis.Cerrar()
		if err := redis.Ping(ctx); err != nil {
			log.Printf("Aviso: %v; la API arranca sin caché hasta que Redis responda", err)
		}
		memoria = redis
		log.Printf("caché en Redis (TTL de predicciones %v)", opciones.ttlRedis)
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
		Cache:      memoria,

		Entrenamiento: opciones.entrenamiento,
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
