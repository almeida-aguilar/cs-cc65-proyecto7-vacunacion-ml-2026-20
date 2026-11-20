package almacen

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// tiempoConexion limita el Ping inicial para fallar rápido si Mongo no responde.
const tiempoConexion = 10 * time.Second

// Mongo es el Repositorio persistente sobre MongoDB. El driver ya es seguro
// para uso concurrente (administra un pool de conexiones), así que no hace
// falta ningún mutex propio.
type Mongo struct {
	cliente *mongo.Client
	base    *mongo.Database
}

// NuevoMongo conecta con MongoDB, verifica la conexión con Ping y crea los
// índices (operación idempotente).
func NuevoMongo(ctx context.Context, uri, nombreBase string) (*Mongo, error) {
	cliente, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("no se pudo conectar con MongoDB: %w", err)
	}
	ctxPing, cancelar := context.WithTimeout(ctx, tiempoConexion)
	defer cancelar()
	if err := cliente.Ping(ctxPing, nil); err != nil {
		_ = cliente.Disconnect(ctx)
		return nil, fmt.Errorf("MongoDB no responde en %s: %w", uri, err)
	}
	repositorio := &Mongo{cliente: cliente, base: cliente.Database(nombreBase)}
	if err := repositorio.AsegurarIndices(ctx); err != nil {
		_ = cliente.Disconnect(ctx)
		return nil, err
	}
	return repositorio, nil
}

// AsegurarIndices crea los índices de las tres colecciones si no existen.
func (m *Mongo) AsegurarIndices(ctx context.Context) error {
	indices := map[string][]mongo.IndexModel{
		ColeccionEjecucion: {
			{Keys: bson.D{{Key: "inicio", Value: -1}}},
			{Keys: bson.D{{Key: "estado", Value: 1}, {Key: "inicio", Value: -1}}},
		},
		ColeccionMetrica: {
			{
				Keys:    bson.D{{Key: "ejecucion_id", Value: 1}, {Key: "epoca", Value: 1}},
				Options: options.Index().SetUnique(true),
			},
		},
		ColeccionPrediccion: {
			{Keys: bson.D{{Key: "fecha", Value: -1}}},
			{Keys: bson.D{{Key: "hash_entrada", Value: 1}, {Key: "fecha", Value: -1}}},
			{Keys: bson.D{{Key: "ejecucion_id", Value: 1}, {Key: "fecha", Value: -1}}},
		},
	}
	for coleccion, modelos := range indices {
		if _, err := m.base.Collection(coleccion).Indexes().CreateMany(ctx, modelos); err != nil {
			return fmt.Errorf("no se pudieron crear los índices de %q: %w", coleccion, err)
		}
	}
	return nil
}

// nuevoID genera un identificador único (ObjectID en hexadecimal).
func nuevoID() string { return primitive.NewObjectID().Hex() }

// InsertarEjecucion implementa Repositorio.
func (m *Mongo) InsertarEjecucion(ctx context.Context, ejecucion *Ejecucion) error {
	if ejecucion.ID == "" {
		ejecucion.ID = nuevoID()
	}
	if _, err := m.base.Collection(ColeccionEjecucion).InsertOne(ctx, ejecucion); err != nil {
		return fmt.Errorf("no se pudo insertar la ejecución: %w", err)
	}
	return nil
}

// ActualizarEjecucion implementa Repositorio.
func (m *Mongo) ActualizarEjecucion(ctx context.Context, ejecucion *Ejecucion) error {
	resultado, err := m.base.Collection(ColeccionEjecucion).ReplaceOne(ctx, bson.M{"_id": ejecucion.ID}, ejecucion)
	if err != nil {
		return fmt.Errorf("no se pudo actualizar la ejecución %q: %w", ejecucion.ID, err)
	}
	if resultado.MatchedCount == 0 {
		return fmt.Errorf("no se pudo actualizar la ejecución %q: %w", ejecucion.ID, ErrNoEncontrado)
	}
	return nil
}

// ObtenerEjecucion implementa Repositorio.
func (m *Mongo) ObtenerEjecucion(ctx context.Context, id string) (*Ejecucion, error) {
	var ejecucion Ejecucion
	err := m.base.Collection(ColeccionEjecucion).FindOne(ctx, bson.M{"_id": id}).Decode(&ejecucion)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, fmt.Errorf("no se pudo leer la ejecución %q: %w", id, ErrNoEncontrado)
	}
	if err != nil {
		return nil, fmt.Errorf("no se pudo leer la ejecución %q: %w", id, err)
	}
	return &ejecucion, nil
}

// UltimasEjecuciones implementa Repositorio.
func (m *Mongo) UltimasEjecuciones(ctx context.Context, limite int) ([]Ejecucion, error) {
	var lista []Ejecucion
	opciones := options.Find().SetSort(bson.D{{Key: "inicio", Value: -1}}).SetLimit(int64(limite))
	if err := buscar(ctx, m.base.Collection(ColeccionEjecucion), bson.M{}, opciones, &lista); err != nil {
		return nil, fmt.Errorf("no se pudieron listar las ejecuciones: %w", err)
	}
	return lista, nil
}

// InsertarMetrica implementa Repositorio.
func (m *Mongo) InsertarMetrica(ctx context.Context, metrica *Metrica) error {
	if metrica.ID == "" {
		metrica.ID = nuevoID()
	}
	if _, err := m.base.Collection(ColeccionMetrica).InsertOne(ctx, metrica); err != nil {
		return fmt.Errorf("no se pudo insertar la métrica de la época %d: %w", metrica.Epoca, err)
	}
	return nil
}

// MetricasDeEjecucion implementa Repositorio.
func (m *Mongo) MetricasDeEjecucion(ctx context.Context, ejecucionID string) ([]Metrica, error) {
	var lista []Metrica
	opciones := options.Find().SetSort(bson.D{{Key: "epoca", Value: 1}})
	if err := buscar(ctx, m.base.Collection(ColeccionMetrica), bson.M{"ejecucion_id": ejecucionID}, opciones, &lista); err != nil {
		return nil, fmt.Errorf("no se pudieron leer las métricas de %q: %w", ejecucionID, err)
	}
	return lista, nil
}

// InsertarPrediccion implementa Repositorio.
func (m *Mongo) InsertarPrediccion(ctx context.Context, prediccion *Prediccion) error {
	if prediccion.ID == "" {
		prediccion.ID = nuevoID()
	}
	if _, err := m.base.Collection(ColeccionPrediccion).InsertOne(ctx, prediccion); err != nil {
		return fmt.Errorf("no se pudo insertar la predicción: %w", err)
	}
	return nil
}

// UltimasPredicciones implementa Repositorio.
func (m *Mongo) UltimasPredicciones(ctx context.Context, limite int) ([]Prediccion, error) {
	var lista []Prediccion
	opciones := options.Find().SetSort(bson.D{{Key: "fecha", Value: -1}}).SetLimit(int64(limite))
	if err := buscar(ctx, m.base.Collection(ColeccionPrediccion), bson.M{}, opciones, &lista); err != nil {
		return nil, fmt.Errorf("no se pudieron listar las predicciones: %w", err)
	}
	return lista, nil
}

// PrediccionPorHash implementa Repositorio.
func (m *Mongo) PrediccionPorHash(ctx context.Context, hash string) (*Prediccion, error) {
	var prediccion Prediccion
	opciones := options.FindOne().SetSort(bson.D{{Key: "fecha", Value: -1}})
	err := m.base.Collection(ColeccionPrediccion).FindOne(ctx, bson.M{"hash_entrada": hash}, opciones).Decode(&prediccion)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, fmt.Errorf("no se encontró predicción para el hash %q: %w", hash, ErrNoEncontrado)
	}
	if err != nil {
		return nil, fmt.Errorf("no se pudo leer la predicción del hash %q: %w", hash, err)
	}
	return &prediccion, nil
}

// Cerrar implementa Repositorio.
func (m *Mongo) Cerrar(ctx context.Context) error {
	if err := m.cliente.Disconnect(ctx); err != nil {
		return fmt.Errorf("no se pudo cerrar la conexión con MongoDB: %w", err)
	}
	return nil
}

// Base expone la base de datos, útil para limpiar en las pruebas de integración.
func (m *Mongo) Base() *mongo.Database { return m.base }

// buscar ejecuta un Find y decodifica todos los resultados en destino.
func buscar(ctx context.Context, coleccion *mongo.Collection, filtro bson.M, opciones *options.FindOptions, destino any) error {
	cursor, err := coleccion.Find(ctx, filtro, opciones)
	if err != nil {
		return err
	}
	return cursor.All(ctx, destino)
}
