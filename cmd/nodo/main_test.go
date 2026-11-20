package main

import (
	"context"
	"fmt"
	"math"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/almeida-aguilar/cs-cc65-proyecto7-vacunacion-ml-2026-20/internal/coordinador"
	"github.com/franztoledo/logreg"
)

// escribirCSV guarda un dataset como CSV numérico con la etiqueta al final.
func escribirCSV(t *testing.T, datos *logreg.Dataset, nombre string) string {
	t.Helper()
	var contenido strings.Builder
	variables := datos.NumVariables()
	for indice := 0; indice < variables; indice++ {
		if indice > 0 {
			contenido.WriteString(",")
		}
		fmt.Fprintf(&contenido, "x%d", indice)
	}
	contenido.WriteString(",deserto\n")
	for fila := 0; fila < datos.Len(); fila++ {
		for columna := 0; columna < variables; columna++ {
			if columna > 0 {
				contenido.WriteString(",")
			}
			contenido.WriteString(strconv.FormatFloat(datos.X[fila][columna], 'g', -1, 64))
		}
		contenido.WriteString(",")
		contenido.WriteString(strconv.FormatFloat(datos.Y[fila], 'g', -1, 64))
		contenido.WriteString("\n")
	}

	ruta := filepath.Join(t.TempDir(), nombre+".csv")
	if err := os.WriteFile(ruta, []byte(contenido.String()), 0o644); err != nil {
		t.Fatalf("no se pudo escribir el CSV de prueba: %v", err)
	}
	return ruta
}

func TestNuevoNodoDesdeCSV(t *testing.T) {
	datos := logreg.DatosSinteticos(300, []float64{1.5, -2.0}, -0.2, 42)
	ruta := escribirCSV(t, datos, "fragmento")

	nodo, columnas, resumen, err := nuevoNodoDesdeCSV(ruta, "nodo-prueba", 2, 0, nil)
	if err != nil {
		t.Fatalf("nuevoNodoDesdeCSV: %v", err)
	}
	if nodo.ID != "nodo-prueba" {
		t.Errorf("ID = %q, se esperaba %q", nodo.ID, "nodo-prueba")
	}
	if resumen.FilasValidas != datos.Len() || resumen.FilasRechazadas != 0 {
		t.Errorf("resumen = %+v, se esperaban %d filas válidas", resumen, datos.Len())
	}
	esperado := []string{"x0", "x1"}
	if len(columnas) != len(esperado) {
		t.Fatalf("columnas = %v, se esperaba %v", columnas, esperado)
	}
	for indice := range esperado {
		if columnas[indice] != esperado[indice] {
			t.Errorf("columna %d = %q, se esperaba %q", indice, columnas[indice], esperado[indice])
		}
	}

	if _, _, _, err := nuevoNodoDesdeCSV(filepath.Join(t.TempDir(), "no_existe.csv"), "nodo", 1, 0, nil); err == nil {
		t.Error("debe fallar si el CSV no existe")
	}
}

// levantarNodosDesdeCSV crea un nodo real por fragmento, con listener propio.
func levantarNodosDesdeCSV(t *testing.T, fragmentos []*logreg.Dataset) []string {
	t.Helper()
	var direcciones []string
	for indice, fragmento := range fragmentos {
		ruta := escribirCSV(t, fragmento, fmt.Sprintf("fragmento-%d", indice+1))
		nodo, _, _, err := nuevoNodoDesdeCSV(ruta, fmt.Sprintf("nodo-%d", indice+1), 2, 0, nil)
		if err != nil {
			t.Fatalf("no se pudo crear el nodo %d: %v", indice+1, err)
		}
		nodo.Log = func(string, ...any) {}
		escucha, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("no se pudo abrir el puerto del nodo %d: %v", indice+1, err)
		}
		go func() { _ = nodo.Servir(escucha) }()
		t.Cleanup(func() { _ = nodo.Detener() })
		direcciones = append(direcciones, escucha.Addr().String())
	}
	return direcciones
}

func TestDosNodosIgualQueUnWorker(t *testing.T) {
	datos := logreg.DatosSinteticos(2000, []float64{1.2, -0.8, 0.5}, -0.1, 42)
	direcciones := levantarNodosDesdeCSV(t, datos.Particionar(2))

	servidor, err := coordinador.Nuevo(coordinador.Opciones{
		Direcciones:     direcciones,
		Epocas:          6,
		TamanoLote:      0, // lote completo: el resultado es determinista
		TasaAprendizaje: 0.5,
		Semilla:         42,
		Reintentos:      0,
		Silencioso:      true,
	})
	if err != nil {
		t.Fatalf("coordinador.Nuevo: %v", err)
	}
	if err := servidor.Conectar(); err != nil {
		t.Fatalf("Conectar: %v", err)
	}
	defer servidor.Cerrar()

	if nodos := servidor.NodosActivos(); nodos != 2 {
		t.Fatalf("se esperaban 2 nodos activos, hay %d", nodos)
	}
	if filas := servidor.FilasTotales(); filas != datos.Len() {
		t.Fatalf("el clúster tiene %d filas, se esperaban %d", filas, datos.Len())
	}

	distribuido, err := servidor.Entrenar(context.Background())
	if err != nil {
		t.Fatalf("Entrenar distribuido: %v", err)
	}
	local, err := logreg.Entrenar(context.Background(), datos.Copiar(), servidor.Configuracion)
	if err != nil {
		t.Fatalf("Entrenar local: %v", err)
	}

	t.Log("época | pérdida 2 nodos | pérdida 1 worker")
	for indice := range distribuido.Historial {
		t.Logf("%5d | %.9f      | %.9f",
			distribuido.Historial[indice].Epoca,
			distribuido.Historial[indice].Perdida,
			local.Historial[indice].Perdida)
	}

	ultimaDist := distribuido.Historial[len(distribuido.Historial)-1]
	ultimaLocal := local.Historial[len(local.Historial)-1]
	if math.Abs(ultimaDist.Perdida-ultimaLocal.Perdida) > 1e-9 {
		t.Errorf("pérdida final: 2 nodos %.12f vs 1 worker %.12f (diff %.2e)",
			ultimaDist.Perdida, ultimaLocal.Perdida, math.Abs(ultimaDist.Perdida-ultimaLocal.Perdida))
	}
	for indice := range local.Modelo.Pesos {
		if diferencia := math.Abs(distribuido.Modelo.Pesos[indice] - local.Modelo.Pesos[indice]); diferencia > 1e-9 {
			t.Errorf("peso %d: 2 nodos %.12f vs 1 worker %.12f (diff %.2e)",
				indice, distribuido.Modelo.Pesos[indice], local.Modelo.Pesos[indice], diferencia)
		}
	}
}
