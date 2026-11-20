package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/almeida-aguilar/cs-cc65-proyecto7-vacunacion-ml-2026-20/internal/cargador"
	"github.com/franztoledo/logreg"
)

// escribirCSVPrueba guarda un dataset sintético como CSV con la etiqueta al final.
func escribirCSVPrueba(t *testing.T, datos *logreg.Dataset) string {
	t.Helper()
	var contenido strings.Builder
	variables := datos.NumVariables()
	for columna := 0; columna < variables; columna++ {
		fmt.Fprintf(&contenido, "x%d,", columna)
	}
	contenido.WriteString("deserto\n")
	for fila := 0; fila < datos.Len(); fila++ {
		for columna := 0; columna < variables; columna++ {
			contenido.WriteString(strconv.FormatFloat(datos.X[fila][columna], 'g', -1, 64))
			contenido.WriteString(",")
		}
		contenido.WriteString(strconv.FormatFloat(datos.Y[fila], 'g', -1, 64))
		contenido.WriteString("\n")
	}
	ruta := filepath.Join(t.TempDir(), "datos.csv")
	if err := os.WriteFile(ruta, []byte(contenido.String()), 0o644); err != nil {
		t.Fatalf("no se pudo escribir el CSV de prueba: %v", err)
	}
	return ruta
}

func TestEscalamientoMismaPerdida(t *testing.T) {
	sintetico := logreg.DatosSinteticos(1200, []float64{1.2, -0.8, 0.5}, -0.1, 42)
	datos, _, _, err := cargador.CargarCSV(escribirCSVPrueba(t, sintetico), 2, nil, 0)
	if err != nil {
		t.Fatalf("CargarCSV: %v", err)
	}

	// 0 = todos los núcleos por nodo; 1 = un núcleo por nodo.
	for _, workersNodo := range []int{0, 1} {
		mediciones, err := medirEscalamiento(datos, []int{1, 2, 4}, 4, 1, workersNodo)
		if err != nil {
			t.Fatalf("medirEscalamiento con %d workers por nodo: %v", workersNodo, err)
		}
		if len(mediciones) != 3 {
			t.Fatalf("se esperaban 3 mediciones, hay %d", len(mediciones))
		}
		for indice, k := range []int{1, 2, 4} {
			m := mediciones[indice]
			if m.nodos != k || m.workersNodo != workersNodo || m.filas != datos.Len() || m.epocas != 4 {
				t.Errorf("medición %d inesperada: %+v", indice, m)
			}
			if m.segundos <= 0 {
				t.Errorf("los segundos con %d nodos deben ser positivos", k)
			}
		}
		if diferencia, err := verificarPerdidas(mediciones); err != nil {
			t.Errorf("la pérdida debe coincidir entre 1, 2 y 4 nodos con %d workers por nodo: %v (diferencia %.2e)",
				workersNodo, err, diferencia)
		}
	}
}

func TestVerificarPerdidasDetectaDiferencia(t *testing.T) {
	mediciones := []medicion{{nodos: 1, perdidaFinal: 0.5}, {nodos: 2, perdidaFinal: 0.5001}}
	if _, err := verificarPerdidas(mediciones); err == nil {
		t.Error("debe detectar pérdidas distintas")
	}
	if _, err := verificarPerdidas(nil); err == nil {
		t.Error("debe fallar sin mediciones")
	}
}

func TestEscribirCSV(t *testing.T) {
	ruta := filepath.Join(t.TempDir(), "sub", "prueba_escalamiento.csv")
	err := escribirCSV(ruta, []medicion{{nodos: 2, workersNodo: 1, filas: 100, epocas: 5, perdidaFinal: 0.25, segundos: 1.5}})
	if err != nil {
		t.Fatalf("escribirCSV: %v", err)
	}
	contenido, err := os.ReadFile(ruta)
	if err != nil {
		t.Fatalf("no se pudo leer el CSV: %v", err)
	}
	esperado := "nodos,workers_nodo,filas,epocas,perdida_final,segundos\n2,1,100,5,0.250000000000,1.5000\n"
	if string(contenido) != esperado {
		t.Errorf("contenido = %q, se esperaba %q", contenido, esperado)
	}
}
