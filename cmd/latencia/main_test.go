package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMedirContraServidor(t *testing.T) {
	var recibidas atomic.Int32
	servidor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recibidas.Add(1)
		cuerpo, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || string(cuerpo) != `{"filas":[[1,2]]}` {
			http.Error(w, "petición inesperada", http.StatusBadRequest)
			return
		}
		time.Sleep(2 * time.Millisecond)
		w.Write([]byte(`{"probabilidad":0.5}`))
	}))
	defer servidor.Close()

	muestras := medir(servidor.Client(), servidor.URL, http.MethodPost, []byte(`{"filas":[[1,2]]}`), 10, 3)
	if len(muestras) != 10 {
		t.Fatalf("se esperaban 10 muestras, hay %d", len(muestras))
	}
	if recibidas.Load() != 13 {
		t.Errorf("el servidor recibió %d peticiones, se esperaban 13 (10 + 3 de calentamiento)", recibidas.Load())
	}
	for _, m := range muestras {
		if m.estado != http.StatusOK || m.milisegundos < 2 {
			t.Errorf("muestra inesperada: %+v", m)
		}
	}

	r := resumir(muestras, servidor.URL, http.MethodPost)
	if r.Exitosas != 10 || !r.CumpleCienMs || r.MedianaMs > r.P95Ms || r.MinimoMs > r.MaximoMs {
		t.Errorf("resumen inconsistente: %+v", r)
	}

	carpeta := filepath.Join(t.TempDir(), "prueba")
	if err := guardar(carpeta, muestras, r); err != nil {
		t.Fatalf("guardar: %v", err)
	}
	csv, err := os.ReadFile(filepath.Join(carpeta, "latencia_api.csv"))
	if err != nil {
		t.Fatalf("falta el CSV: %v", err)
	}
	lineas := strings.Split(strings.TrimSpace(string(csv)), "\n")
	if lineas[0] != "peticion,milisegundos,estado" || len(lineas) != 11 {
		t.Errorf("CSV inesperado: cabecera %q, %d líneas", lineas[0], len(lineas))
	}
	contenido, err := os.ReadFile(filepath.Join(carpeta, "latencia_resumen.json"))
	if err != nil {
		t.Fatalf("falta el resumen: %v", err)
	}
	var leido resumen
	if err := json.Unmarshal(contenido, &leido); err != nil || leido.Peticiones != 10 {
		t.Errorf("resumen JSON inválido: %v %+v", err, leido)
	}
}

func TestResumirNoCumpleSiHayErrores(t *testing.T) {
	muestras := []muestra{{1, 5, 200}, {2, 6, 500}, {3, 7, 0}}
	r := resumir(muestras, "u", "POST")
	if r.Exitosas != 1 || r.CumpleCienMs {
		t.Errorf("con errores no debe cumplir: %+v", r)
	}
	if vacio := resumir(nil, "u", "POST"); vacio.Exitosas != 0 || vacio.CumpleCienMs {
		t.Errorf("sin muestras no debe cumplir: %+v", vacio)
	}
}

func TestResumirNoCumpleSiEsLento(t *testing.T) {
	r := resumir([]muestra{{1, 150, 200}, {2, 200, 200}}, "u", "POST")
	if r.CumpleCienMs {
		t.Errorf("mediana de 175 ms no cumple: %+v", r)
	}
}

func TestPercentil(t *testing.T) {
	valores := make([]float64, 100)
	for indice := range valores {
		valores[indice] = float64(indice + 1)
	}
	if p := percentil(valores, 95); p != 95 {
		t.Errorf("p95 = %v, se esperaba 95", p)
	}
	if p := percentil(nil, 95); p != 0 {
		t.Errorf("p95 de lista vacía = %v, se esperaba 0", p)
	}
}

func TestMedirServidorCaido(t *testing.T) {
	servidor := httptest.NewServer(http.NotFoundHandler())
	url := servidor.URL
	servidor.Close()
	muestras := medir(&http.Client{Timeout: time.Second}, url, http.MethodPost, nil, 2, 0)
	if len(muestras) != 2 || muestras[0].estado != 0 {
		t.Errorf("con el servidor caído el estado debe ser 0: %+v", muestras)
	}
}
