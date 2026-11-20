package api

import (
	"encoding/json"
	"sort"
	"sync"
	"time"
)

// evento es el mensaje JSON que se envía por WebSocket.
type evento struct {
	Tipo  string    `json:"tipo"`
	Fecha time.Time `json:"fecha"`
	Datos any       `json:"datos"`
}

// hub reparte eventos a los clientes WebSocket. Cada suscriptor tiene su
// propio canal con búfer; si un cliente es lento se descarta el evento para ese
// cliente en lugar de bloquear a quien publica (la ruta de /predict).
type hub struct {
	mu           sync.Mutex
	suscriptores map[chan []byte]struct{}
}

func nuevoHub() *hub {
	return &hub{suscriptores: map[chan []byte]struct{}{}}
}

// suscribir registra un cliente y devuelve su canal de eventos.
func (h *hub) suscribir() chan []byte {
	canal := make(chan []byte, 64)
	h.mu.Lock()
	h.suscriptores[canal] = struct{}{}
	h.mu.Unlock()
	return canal
}

// cancelar elimina al cliente. El canal no se cierra: lo abandona el lector.
func (h *hub) cancelar(canal chan []byte) {
	h.mu.Lock()
	delete(h.suscriptores, canal)
	h.mu.Unlock()
}

// clientes devuelve cuántos clientes WebSocket hay conectados.
func (h *hub) clientes() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.suscriptores)
}

// publicar serializa el evento y lo envía sin bloquear a todos los clientes.
func (h *hub) publicar(tipo string, datos any) {
	mensaje, err := json.Marshal(evento{Tipo: tipo, Fecha: time.Now().UTC(), Datos: datos})
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for canal := range h.suscriptores {
		select {
		case canal <- mensaje:
		default: // cliente lento: se pierde el evento, no se frena a la API
		}
	}
}

// ventanaLatencias guarda las últimas latencias de /predict para calcular
// mediana y percentil 95 sin crecer sin límite.
type ventanaLatencias struct {
	mu       sync.Mutex
	valores  []float64
	posicion int
	total    int64
}

const capacidadVentana = 1000

func (v *ventanaLatencias) registrar(milisegundos float64) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.valores) < capacidadVentana {
		v.valores = append(v.valores, milisegundos)
	} else {
		v.valores[v.posicion] = milisegundos
		v.posicion = (v.posicion + 1) % capacidadVentana
	}
	v.total++
}

// resumenLatencias es la estadística que expone /metrics.
type resumenLatencias struct {
	Muestras int     `json:"muestras"`
	Total    int64   `json:"total_predicciones"`
	Mediana  float64 `json:"mediana_ms"`
	P95      float64 `json:"p95_ms"`
	Maxima   float64 `json:"maxima_ms"`
}

func (v *ventanaLatencias) resumen() resumenLatencias {
	v.mu.Lock()
	copia := append([]float64(nil), v.valores...)
	total := v.total
	v.mu.Unlock()

	resultado := resumenLatencias{Muestras: len(copia), Total: total}
	if len(copia) == 0 {
		return resultado
	}
	sort.Float64s(copia)
	resultado.Mediana = percentil(copia, 0.5)
	resultado.P95 = percentil(copia, 0.95)
	resultado.Maxima = copia[len(copia)-1]
	return resultado
}

// percentil devuelve el percentil p (0..1) de una lista ordenada.
func percentil(ordenados []float64, p float64) float64 {
	indice := int(p*float64(len(ordenados))+0.5) - 1
	if indice < 0 {
		indice = 0
	}
	if indice >= len(ordenados) {
		indice = len(ordenados) - 1
	}
	return ordenados[indice]
}
