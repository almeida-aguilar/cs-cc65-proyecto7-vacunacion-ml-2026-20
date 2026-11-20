package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/franztoledo/logreg"
)

func TestGuardarReporte(t *testing.T) {
	carpeta := filepath.Join(t.TempDir(), "anidada")
	opcionesParaPrueba := opciones{reportes: carpeta, nombre: "prueba"}
	reporte := reporteServidor{
		NodosActivos:          2,
		FilasTotales:          1200,
		Configuracion:         logreg.ConfiguracionPorDefecto(),
		Metricas:              &logreg.Metricas{N: 1200, Exactitud: 0.9},
		MejorEpoca:            4,
		SegundosEntrenamiento: 1.25,
	}

	if err := guardarReporte(opcionesParaPrueba, reporte); err != nil {
		t.Fatalf("guardarReporte: %v", err)
	}

	contenido, err := os.ReadFile(filepath.Join(carpeta, "prueba_servidor.json"))
	if err != nil {
		t.Fatalf("no se pudo leer el reporte: %v", err)
	}
	var guardado reporteServidor
	if err := json.Unmarshal(contenido, &guardado); err != nil {
		t.Fatalf("el JSON no es válido: %v", err)
	}
	if guardado.NodosActivos != 2 || guardado.FilasTotales != 1200 {
		t.Errorf("nodos/filas = %d/%d", guardado.NodosActivos, guardado.FilasTotales)
	}
	if guardado.Metricas == nil || guardado.Metricas.N != 1200 {
		t.Errorf("métricas = %+v", guardado.Metricas)
	}
	if guardado.MejorEpoca != 4 || guardado.SegundosEntrenamiento != 1.25 {
		t.Errorf("resumen = %+v", guardado)
	}
}
