package coordinador

import (
	"context"
	"fmt"
	"math"
	"net"
	"testing"
	"time"

	"github.com/franztoledo/logreg"
)

func silencio(formato string, valores ...any) {}

// levantarNodos inicia dos nodos reales por TCP con particiones del mismo
// dataset sintético. Cada nodo recibe una copia porque logreg escala en sitio.
func levantarNodos(t *testing.T, cantidad int) ([]string, *logreg.Dataset) {
	t.Helper()
	datos := logreg.DatosSinteticos(4000, []float64{1.5, -2.0}, -0.2, 42)
	partes := datos.Particionar(cantidad)
	var direcciones []string
	for indice, parte := range partes {
		escucha, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("no se pudo abrir el puerto del nodo: %v", err)
		}
		nodo, err := logreg.NuevoNodo(fmt.Sprintf("nodo-%d", indice+1), parte.Copiar(), 2)
		if err != nil {
			t.Fatalf("no se pudo crear el nodo: %v", err)
		}
		nodo.Log = silencio
		go func() { _ = nodo.Servir(escucha) }()
		t.Cleanup(func() { _ = nodo.Detener() })
		direcciones = append(direcciones, escucha.Addr().String())
	}
	return direcciones, datos
}

func TestParameterServerConDosNodos(t *testing.T) {
	direcciones, datos := levantarNodos(t, 2)
	servidor, err := Nuevo(Opciones{
		Direcciones: direcciones,
		Epocas:      6,
		TamanoLote:  256,
		Silencioso:  true,
	})
	if err != nil {
		t.Fatalf("Nuevo: %v", err)
	}
	if err := servidor.Conectar(); err != nil {
		t.Fatalf("Conectar: %v", err)
	}
	defer servidor.Cerrar()

	if nodos := servidor.NodosActivos(); nodos != 2 {
		t.Errorf("se esperaban 2 nodos activos, hay %d", nodos)
	}
	if filas := servidor.FilasTotales(); filas != datos.Len() {
		t.Errorf("se esperaban %d filas, hay %d", datos.Len(), filas)
	}

	resultado, err := servidor.Entrenar(context.Background())
	if err != nil {
		t.Fatalf("Entrenar: %v", err)
	}
	if len(resultado.Historial) == 0 {
		t.Fatal("el entrenamiento no devolvió historial")
	}
	for _, progreso := range resultado.Historial {
		if progreso.Nodos != 2 {
			t.Errorf("época %d usó %d nodos, se esperaban 2", progreso.Epoca, progreso.Nodos)
		}
	}
	if math.IsNaN(resultado.Historial[len(resultado.Historial)-1].Perdida) {
		t.Error("la pérdida final es NaN")
	}

	metricas, err := servidor.Evaluar(resultado.Modelo)
	if err != nil {
		t.Fatalf("Evaluar: %v", err)
	}
	if metricas.N != datos.Len() {
		t.Errorf("la evaluación cubrió %d filas, se esperaban %d", metricas.N, datos.Len())
	}
}

func TestParameterServerIgualQueEntrenamientoLocal(t *testing.T) {
	direcciones, datos := levantarNodos(t, 2)
	opciones := Opciones{
		Direcciones:     direcciones,
		Epocas:          8,
		TamanoLote:      0, // lote completo: el resultado debe ser determinista
		TasaAprendizaje: 0.5,
		Semilla:         42,
		Silencioso:      true,
	}
	servidor, err := Nuevo(opciones)
	if err != nil {
		t.Fatalf("Nuevo: %v", err)
	}
	if err := servidor.Conectar(); err != nil {
		t.Fatalf("Conectar: %v", err)
	}
	defer servidor.Cerrar()

	distribuido, err := servidor.Entrenar(context.Background())
	if err != nil {
		t.Fatalf("Entrenar distribuido: %v", err)
	}
	local, err := logreg.Entrenar(context.Background(), datos.Copiar(), servidor.Configuracion)
	if err != nil {
		t.Fatalf("Entrenar local: %v", err)
	}

	if len(distribuido.Modelo.Pesos) != len(local.Modelo.Pesos) {
		t.Fatalf("dimensiones distintas: distribuido %d, local %d",
			len(distribuido.Modelo.Pesos), len(local.Modelo.Pesos))
	}
	for indice := range local.Modelo.Pesos {
		if diferencia := math.Abs(distribuido.Modelo.Pesos[indice] - local.Modelo.Pesos[indice]); diferencia > 1e-6 {
			t.Errorf("peso %d: distribuido %.8f vs local %.8f (diff %.2e)",
				indice, distribuido.Modelo.Pesos[indice], local.Modelo.Pesos[indice], diferencia)
		}
	}
}

func TestParameterServerToleraNodoInactivo(t *testing.T) {
	direcciones, _ := levantarNodos(t, 3)
	servidor, err := Nuevo(Opciones{
		Direcciones:  append(direcciones, "127.0.0.1:1"),
		Epocas:       3,
		TamanoLote:   256,
		MinimoNodos:  2,
		Reintentos:   0,
		TiempoEspera: time.Second,
		Silencioso:   true,
	})
	if err != nil {
		t.Fatalf("Nuevo: %v", err)
	}
	if err := servidor.Conectar(); err != nil {
		t.Fatalf("Conectar: %v", err)
	}
	defer servidor.Cerrar()

	if nodos := servidor.NodosActivos(); nodos != 3 {
		t.Errorf("se esperaban 3 nodos activos, hay %d", nodos)
	}
	resultado, err := servidor.Entrenar(context.Background())
	if err != nil {
		t.Fatalf("Entrenar con un nodo caído: %v", err)
	}
	ultimo := resultado.Historial[len(resultado.Historial)-1]
	if ultimo.Nodos < 2 {
		t.Errorf("al final quedaron %d nodos, se necesitaban al menos 2", ultimo.Nodos)
	}
}

func TestNuevoRechazaOpcionesInvalidas(t *testing.T) {
	if _, err := Nuevo(Opciones{}); err == nil {
		t.Error("debe rechazar la falta de direcciones")
	}
	if _, err := Nuevo(Opciones{Direcciones: []string{"127.0.0.1:9001"}, Epocas: -1}); err == nil {
		t.Error("debe rechazar épocas negativas")
	}
}
