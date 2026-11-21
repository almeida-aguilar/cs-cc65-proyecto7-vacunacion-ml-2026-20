package api

import (
	_ "embed"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

//go:embed openapi.yaml
var especificacionOpenAPI []byte

// intervaloMetricas es cada cuánto se envía la instantánea de métricas a cada
// cliente WebSocket (panel de administración del entregable 3).
const intervaloMetricas = 5 * time.Second

// actualizador convierte la petición HTTP en WebSocket. Se acepta cualquier
// origen porque la SPA se servirá desde otro puerto; al no haber cookies de
// sesión no hay riesgo de secuestro entre sitios.
var actualizador = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// websocket atiende GET /ws: envía en JSON las predicciones, el progreso de
// los entrenamientos y una instantánea de métricas cada pocos segundos.
// Solo una goroutine escribe en la conexión (requisito de gorilla/websocket).
func (s *Servidor) websocket(w http.ResponseWriter, r *http.Request) {
	conexion, err := actualizador.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade ya respondió con el error HTTP correspondiente
	}
	defer conexion.Close()

	eventos := s.hub.suscribir()
	defer s.hub.cancelar(eventos)

	// El lector detecta el cierre del cliente y también procesa los pong.
	cerrado := make(chan struct{})
	go func() {
		defer close(cerrado)
		for {
			if _, _, err := conexion.ReadMessage(); err != nil {
				return
			}
		}
	}()

	if err := conexion.WriteJSON(evento{Tipo: "conectado", Fecha: time.Now().UTC(), Datos: s.instantanea()}); err != nil {
		return
	}
	metricas := time.NewTicker(intervaloMetricas)
	defer metricas.Stop()
	latido := time.NewTicker(30 * time.Second)
	defer latido.Stop()

	for {
		select {
		case mensaje := <-eventos:
			if err := conexion.WriteMessage(websocket.TextMessage, mensaje); err != nil {
				return
			}
		case <-metricas.C:
			if err := conexion.WriteJSON(evento{Tipo: "metricas", Fecha: time.Now().UTC(), Datos: s.instantanea()}); err != nil {
				return
			}
		case <-latido.C:
			if err := conexion.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-cerrado:
			return
		case <-s.ctx.Done():
			_ = conexion.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseGoingAway, "servidor detenido"))
			return
		}
	}
}

// especificacion sirve el contrato OpenAPI en YAML.
func (s *Servidor) especificacion(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	_, _ = w.Write(especificacionOpenAPI)
}

// paginaSwagger carga Swagger UI desde un CDN y le indica el contrato local.
const paginaSwagger = `<!doctype html>
<html lang="es">
<head>
  <meta charset="utf-8">
  <title>API de deserción de segundas dosis</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5.17.14/swagger-ui.css">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5.17.14/swagger-ui-bundle.js"></script>
  <script>
    window.onload = function () {
      window.ui = SwaggerUIBundle({ url: "/openapi.yaml", dom_id: "#swagger-ui" });
    };
  </script>
</body>
</html>`

// documentacion sirve Swagger UI en GET /docs.
func (s *Servidor) documentacion(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(paginaSwagger))
}
