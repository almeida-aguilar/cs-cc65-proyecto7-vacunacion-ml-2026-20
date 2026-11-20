// Package coordinador envuelve el parameter server de la librería logreg para
// el clúster de nodos ML del entregable PC4.
package coordinador

import (
	"context"
	"fmt"
	"time"

	"github.com/franztoledo/logreg"
)

// Opciones resume la configuración del clúster, ya venga de la API o de la
// línea de comandos.
type Opciones struct {
	Direcciones       []string
	Epocas            int
	TamanoLote        int
	TasaAprendizaje   float64
	PesoClasePositiva float64
	Semilla           int64
	MinimoNodos       int
	Reintentos        int
	EsperaReintento   time.Duration
	TiempoEspera      time.Duration
	Silencioso        bool
}

// Servidor administra las conexiones TCP con los nodos y la sincronización de
// pesos. Toda la sincronización la hace logreg.Coordinador con mutex y canales;
// este tipo solo fija la configuración y expone una interfaz estable para la API.
type Servidor struct {
	Remoto        *logreg.Coordinador
	Configuracion logreg.Configuracion
}

// Nuevo valida las opciones y crea el servidor sin abrir conexiones todavía.
func Nuevo(opciones Opciones) (*Servidor, error) {
	if len(opciones.Direcciones) == 0 {
		return nil, fmt.Errorf("se necesita al menos una dirección de nodo")
	}
	if opciones.Epocas < 0 || opciones.TamanoLote < 0 {
		return nil, fmt.Errorf("épocas y tamaño de lote no pueden ser negativos")
	}

	remoto := logreg.NuevoCoordinador(opciones.Direcciones)
	if opciones.MinimoNodos > 0 {
		remoto.MinimoNodos = opciones.MinimoNodos
	}
	if opciones.Reintentos >= 0 {
		remoto.Reintentos = opciones.Reintentos
	}
	if opciones.EsperaReintento > 0 {
		remoto.EsperaReintento = opciones.EsperaReintento
	}
	if opciones.TiempoEspera > 0 {
		remoto.TiempoEspera = opciones.TiempoEspera
	}
	if opciones.Silencioso {
		remoto.Log = func(formato string, valores ...any) {}
	}

	cfg := logreg.ConfiguracionPorDefecto()
	if opciones.Epocas > 0 {
		cfg.Epocas = opciones.Epocas
	}
	if opciones.TamanoLote > 0 {
		cfg.TamanoLote = opciones.TamanoLote
	} else {
		cfg.TamanoLote = 0 // lote completo
	}
	if opciones.TasaAprendizaje > 0 {
		cfg.TasaAprendizaje = opciones.TasaAprendizaje
	}
	if opciones.PesoClasePositiva > 0 {
		cfg.PesoClasePositiva = opciones.PesoClasePositiva
	}
	if opciones.Semilla != 0 {
		cfg.Semilla = opciones.Semilla
	}

	return &Servidor{Remoto: remoto, Configuracion: cfg}, nil
}

// Conectar abre las conexiones TCP y verifica que los nodos tengan el mismo
// número de variables. Reintenta según las opciones (útil en Docker Compose).
func (s *Servidor) Conectar() error {
	return s.Remoto.Conectar()
}

// Entrenar ejecuta el descenso de gradiente síncrono sobre el clúster.
func (s *Servidor) Entrenar(ctx context.Context) (*logreg.Resultado, error) {
	return s.Remoto.Entrenar(ctx, s.Configuracion)
}

// Evaluar pide a cada nodo su matriz de confusión y la consolida sin mover los
// datos al coordinador.
func (s *Servidor) Evaluar(modelo *logreg.Modelo) (*logreg.Metricas, error) {
	return s.Remoto.EvaluarEnNodos(modelo)
}

// Cerrar envía FIN a los nodos y libera las conexiones.
func (s *Servidor) Cerrar() {
	s.Remoto.Cerrar()
}

// NodosActivos devuelve cuántos nodos siguen respondiendo.
func (s *Servidor) NodosActivos() int {
	return s.Remoto.NodosActivos()
}

// FilasTotales devuelve la suma de filas de todos los nodos conectados.
func (s *Servidor) FilasTotales() int {
	return s.Remoto.FilasTotales()
}
