package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/franztoledo/logreg"
)

func TestMismasColumnas(t *testing.T) {
	casos := []struct {
		nombre   string
		a, b     []string
		esperado bool
	}{
		{"iguales", []string{"edad", "zona"}, []string{"edad", "zona"}, true},
		{"distinto orden", []string{"edad", "zona"}, []string{"zona", "edad"}, false},
		{"distinta longitud", []string{"edad"}, []string{"edad", "zona"}, false},
		{"ambos vacíos", nil, nil, true},
	}
	for _, caso := range casos {
		if obtenido := mismasColumnas(caso.a, caso.b); obtenido != caso.esperado {
			t.Errorf("%s: mismasColumnas(%v, %v) = %v, se esperaba %v",
				caso.nombre, caso.a, caso.b, obtenido, caso.esperado)
		}
	}
}

func TestGuardarReportes(t *testing.T) {
	carpeta := t.TempDir()
	opcionesParaPrueba := opciones{reportes: carpeta, nombre: "prueba"}
	reporte := reporteMetricas{
		Umbral:            0.5,
		MejorEpoca:        3,
		NumVariables:      2,
		ColumnasExcluidas: []string{"mes_sin"},
		FilasTrain:        100,
		FilasValidacion:   20,
		FilasPrueba:       20,
	}
	historial := []logreg.Progreso{
		{Epoca: 1, Perdida: 0.8, Duracion: 1500 * time.Millisecond},
		{
			Epoca:      2,
			Perdida:    0.6,
			Validacion: &logreg.Metricas{PerdidaLog: 0.55, AUC: 0.91},
			Duracion:   2200 * time.Millisecond,
		},
	}

	if err := guardarReportes(opcionesParaPrueba, reporte, historial); err != nil {
		t.Fatalf("guardarReportes: %v", err)
	}

	contenidoJSON, err := os.ReadFile(filepath.Join(carpeta, "prueba_metricas.json"))
	if err != nil {
		t.Fatalf("no se pudo leer el JSON: %v", err)
	}
	var guardado reporteMetricas
	if err := json.Unmarshal(contenidoJSON, &guardado); err != nil {
		t.Fatalf("el JSON no es válido: %v", err)
	}
	if guardado.Umbral != 0.5 || guardado.MejorEpoca != 3 || guardado.FilasTrain != 100 {
		t.Errorf("el JSON no conservó los campos: %+v", guardado)
	}
	if len(guardado.ColumnasExcluidas) != 1 || guardado.ColumnasExcluidas[0] != "mes_sin" {
		t.Errorf("columnas excluidas = %v", guardado.ColumnasExcluidas)
	}

	contenidoCSV, err := os.ReadFile(filepath.Join(carpeta, "prueba_historial.csv"))
	if err != nil {
		t.Fatalf("no se pudo leer el CSV: %v", err)
	}
	lineas := strings.Split(strings.TrimSpace(string(contenidoCSV)), "\n")
	esperado := []string{
		"epoca,perdida_train,perdida_valid,auc_valid,duracion_ms",
		"1,0.800000,0.000000,0.000000,1500",
		"2,0.600000,0.550000,0.910000,2200",
	}
	if len(lineas) != len(esperado) {
		t.Fatalf("el CSV tiene %d líneas, se esperaban %d: %q", len(lineas), len(esperado), lineas)
	}
	for indice := range esperado {
		if lineas[indice] != esperado[indice] {
			t.Errorf("línea %d = %q, se esperaba %q", indice, lineas[indice], esperado[indice])
		}
	}
}
