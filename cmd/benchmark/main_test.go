package main

import (
	"math"
	"reflect"
	"testing"
)

func TestMediana(t *testing.T) {
	casos := []struct {
		entrada  []float64
		esperado float64
	}{
		{nil, 0},
		{[]float64{5}, 5},
		{[]float64{3, 1, 2}, 2},
		{[]float64{4, 1, 3, 2}, 2.5},
	}
	for _, c := range casos {
		if obtenido := mediana(c.entrada); obtenido != c.esperado {
			t.Errorf("mediana(%v) = %v, se esperaba %v", c.entrada, obtenido, c.esperado)
		}
	}
	original := []float64{3, 1, 2}
	mediana(original)
	if !reflect.DeepEqual(original, []float64{3, 1, 2}) {
		t.Error("mediana no debe modificar el slice recibido")
	}
}

func TestCalcularListaWorkers(t *testing.T) {
	casos := []struct {
		cpus     int
		esperado []int
	}{
		{1, []int{1}},
		{2, []int{1, 2}},
		{6, []int{1, 2, 4, 6}},
		{8, []int{1, 2, 4, 8}},
		{16, []int{1, 2, 4, 8, 16}},
	}
	for _, c := range casos {
		if obtenido := calcularListaWorkers(c.cpus); !reflect.DeepEqual(obtenido, c.esperado) {
			t.Errorf("calcularListaWorkers(%d) = %v, se esperaba %v", c.cpus, obtenido, c.esperado)
		}
	}
}

func TestCalcularSpeedup(t *testing.T) {
	if s := calcularSpeedup(10, 2.5); s != 4 {
		t.Errorf("speedup = %v, se esperaba 4", s)
	}
	if s := calcularSpeedup(10, 0); s != 0 {
		t.Errorf("con tiempo 0 el speedup debe ser 0, fue %v", s)
	}
}

func TestDiferenciaRelativa(t *testing.T) {
	if d := diferenciaRelativa(0.5, 0.5); d != 0 {
		t.Errorf("valores iguales: %v", d)
	}
	if d := diferenciaRelativa(100, 101); math.Abs(d-1.0/101) > 1e-12 {
		t.Errorf("diferencia relativa = %v", d)
	}
	if d := diferenciaRelativa(0, 0); d != 0 {
		t.Errorf("ceros: %v", d)
	}
}

func TestNuevaMedicionUsaPrimeraComoBase(t *testing.T) {
	primera := nuevaMedicion("carga", 1, 8, nil)
	if primera.speedup != 1 {
		t.Errorf("el primer caso debe tener speedup 1, fue %v", primera.speedup)
	}
	segunda := nuevaMedicion("carga", 2, 4, []medicion{primera})
	if segunda.speedup != 2 {
		t.Errorf("speedup = %v, se esperaba 2", segunda.speedup)
	}
}
