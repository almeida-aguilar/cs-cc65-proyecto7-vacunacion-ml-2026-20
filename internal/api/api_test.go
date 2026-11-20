package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/almeida-aguilar/cs-cc65-proyecto7-vacunacion-ml-2026-20/internal/almacen"
	"github.com/franztoledo/logreg"
	"github.com/gorilla/websocket"
)

func silencio(string, ...any) {}

// nuevoServidorDePrueba crea una API con repositorio en memoria y, si
// conModelo es true, un modelo de 2 variables donde la primera sube la probabilidad.
func nuevoServidorDePrueba(t *testing.T, conModelo bool, nodos []string) (*Servidor, *almacen.Memoria) {
	t.Helper()
	repo := almacen.NuevaMemoria()
	servidor, err := NuevoServidor(Opciones{Repo: repo, Nodos: nodos, Log: silencio})
	if err != nil {
		t.Fatalf("NuevoServidor: %v", err)
	}
	if conModelo {
		modelo := logreg.NuevoModelo(2)
		modelo.Pesos = []float64{2, -1}
		modelo.Umbral = 0.5
		servidor.FijarModelo(modelo, "ej-prueba")
	}
	return servidor, repo
}

// peticion ejecuta una petición contra el Handler sin abrir puertos.
func peticion(t *testing.T, servidor *Servidor, metodo, ruta, cuerpo string) *httptest.ResponseRecorder {
	t.Helper()
	solicitud := httptest.NewRequest(metodo, ruta, strings.NewReader(cuerpo))
	respuesta := httptest.NewRecorder()
	servidor.Handler().ServeHTTP(respuesta, solicitud)
	return respuesta
}

func TestSaludSinModelo(t *testing.T) {
	servidor, _ := nuevoServidorDePrueba(t, false, []string{"a:1", "b:2"})
	defer servidor.Cerrar()

	respuesta := peticion(t, servidor, "GET", "/health", "")
	if respuesta.Code != http.StatusOK {
		t.Fatalf("estado %d, se esperaba 200", respuesta.Code)
	}
	var salud respuestaSalud
	if err := json.Unmarshal(respuesta.Body.Bytes(), &salud); err != nil {
		t.Fatalf("JSON inválido: %v", err)
	}
	if salud.Estado != "ok" || salud.ModeloCargado || salud.NodosConfigurados != 2 {
		t.Errorf("salud inesperada: %+v", salud)
	}
	if respuesta.Header().Get("X-Latencia-ms") == "" {
		t.Error("falta la cabecera X-Latencia-ms")
	}
	if respuesta.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("falta la cabecera CORS")
	}
}

func TestPredecirSinModelo(t *testing.T) {
	servidor, _ := nuevoServidorDePrueba(t, false, nil)
	defer servidor.Cerrar()
	respuesta := peticion(t, servidor, "POST", "/predict", `{"entrada":[1,2]}`)
	if respuesta.Code != http.StatusServiceUnavailable {
		t.Errorf("estado %d, se esperaba 503", respuesta.Code)
	}
}

func TestPredecirValidaLaEntrada(t *testing.T) {
	servidor, _ := nuevoServidorDePrueba(t, true, nil)
	defer servidor.Cerrar()
	casos := map[string]string{
		"JSON roto":            `{"entrada":`,
		"cuerpo vacío":         ``,
		"pocas variables":      `{"entrada":[1]}`,
		"demasiadas variables": `{"entrada":[1,2,3]}`,
		"sin campo entrada":    `{}`,
	}
	for nombre, cuerpo := range casos {
		respuesta := peticion(t, servidor, "POST", "/predict", cuerpo)
		if respuesta.Code != http.StatusBadRequest {
			t.Errorf("%s: estado %d, se esperaba 400", nombre, respuesta.Code)
		}
		var cuerpoError errorAPI
		if err := json.Unmarshal(respuesta.Body.Bytes(), &cuerpoError); err != nil || cuerpoError.Error == "" {
			t.Errorf("%s: se esperaba un JSON con el campo error (%v)", nombre, err)
		}
	}
}

func TestPredecirYPersistir(t *testing.T) {
	servidor, repo := nuevoServidorDePrueba(t, true, nil)

	respuesta := peticion(t, servidor, "POST", "/predict", `{"entrada":[3,0]}`)
	if respuesta.Code != http.StatusOK {
		t.Fatalf("estado %d: %s", respuesta.Code, respuesta.Body.String())
	}
	var prediccion respuestaPrediccion
	if err := json.Unmarshal(respuesta.Body.Bytes(), &prediccion); err != nil {
		t.Fatalf("JSON inválido: %v", err)
	}
	esperada := logreg.Sigmoide(6)
	if diferencia := prediccion.Probabilidad - esperada; diferencia > 1e-9 || diferencia < -1e-9 {
		t.Errorf("probabilidad %v, se esperaba %v", prediccion.Probabilidad, esperada)
	}
	if prediccion.Clase != 1 || prediccion.Origen != almacen.OrigenCalculo || prediccion.EjecucionID != "ej-prueba" {
		t.Errorf("respuesta inesperada: %+v", prediccion)
	}

	servidor.Cerrar() // vacía la cola de persistencia
	guardadas, err := repo.UltimasPredicciones(context.Background(), 10)
	if err != nil || len(guardadas) != 1 {
		t.Fatalf("se esperaba 1 predicción guardada, hay %d (%v)", len(guardadas), err)
	}
	if guardadas[0].HashEntrada != almacen.HashEntrada([]float64{3, 0}) {
		t.Error("el hash guardado no coincide con la entrada")
	}
}

// Predicciones simultáneas: bajo -race comprueba el modelo compartido, la
// ventana de latencias y la cola de persistencia.
func TestPrediccionesConcurrentes(t *testing.T) {
	servidor, repo := nuevoServidorDePrueba(t, true, nil)
	const peticiones = 200
	var grupo sync.WaitGroup
	for indice := 0; indice < peticiones; indice++ {
		grupo.Add(1)
		go func(n int) {
			defer grupo.Done()
			cuerpo := fmt.Sprintf(`{"entrada":[%d,1]}`, n)
			if respuesta := peticion(t, servidor, "POST", "/predict", cuerpo); respuesta.Code != http.StatusOK {
				t.Errorf("estado %d", respuesta.Code)
			}
		}(indice)
	}
	grupo.Wait()
	servidor.Cerrar()

	guardadas, _ := repo.UltimasPredicciones(context.Background(), 0)
	if int64(len(guardadas))+servidor.descartadas.Load() != peticiones {
		t.Errorf("guardadas %d + descartadas %d != %d", len(guardadas), servidor.descartadas.Load(), peticiones)
	}
	if resumen := servidor.latencias.resumen(); resumen.Total != peticiones {
		t.Errorf("la ventana registró %d latencias, se esperaban %d", resumen.Total, peticiones)
	}
}

type cacheFalsa struct {
	mu        sync.Mutex
	datos     map[string]*almacen.Prediccion
	consultas int
}

func (c *cacheFalsa) Obtener(_ context.Context, hash string) (*almacen.Prediccion, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.consultas++
	return c.datos[hash], nil
}

func (c *cacheFalsa) Guardar(_ context.Context, p *almacen.Prediccion) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.datos[p.HashEntrada] = p
	return nil
}

func TestPrediccionUsaLaCache(t *testing.T) {
	cache := &cacheFalsa{datos: map[string]*almacen.Prediccion{}}
	servidor, err := NuevoServidor(Opciones{Cache: cache, Log: silencio})
	if err != nil {
		t.Fatal(err)
	}
	modelo := logreg.NuevoModelo(2)
	modelo.Pesos = []float64{1, 1}
	servidor.FijarModelo(modelo, "ej")

	primera := peticion(t, servidor, "POST", "/predict", `{"entrada":[1,1]}`)
	servidor.Cerrar() // la cola guarda la predicción en la caché
	var calculada respuestaPrediccion
	_ = json.Unmarshal(primera.Body.Bytes(), &calculada)
	if calculada.Origen != almacen.OrigenCalculo {
		t.Fatalf("la primera respuesta debía calcularse, origen %q", calculada.Origen)
	}

	// Se reutiliza el modelo con un servidor nuevo que comparte la caché.
	segundo, _ := NuevoServidor(Opciones{Cache: cache, Log: silencio})
	defer segundo.Cerrar()
	segundo.FijarModelo(modelo, "ej")
	acierto := peticion(t, segundo, "POST", "/predict", `{"entrada":[1,1]}`)
	var servida respuestaPrediccion
	_ = json.Unmarshal(acierto.Body.Bytes(), &servida)
	if servida.Origen != almacen.OrigenCache || servida.Probabilidad != calculada.Probabilidad {
		t.Errorf("se esperaba un acierto de caché, llegó %+v", servida)
	}
}

func TestDocumentacionYEspecificacion(t *testing.T) {
	servidor, _ := nuevoServidorDePrueba(t, false, nil)
	defer servidor.Cerrar()

	docs := peticion(t, servidor, "GET", "/docs", "")
	if docs.Code != http.StatusOK || !strings.Contains(docs.Body.String(), "swagger-ui") {
		t.Errorf("/docs no sirve Swagger UI (estado %d)", docs.Code)
	}
	especificacion := peticion(t, servidor, "GET", "/openapi.yaml", "")
	cuerpo := especificacion.Body.String()
	if especificacion.Code != http.StatusOK || !strings.HasPrefix(cuerpo, "openapi:") {
		t.Fatalf("/openapi.yaml inválido (estado %d)", especificacion.Code)
	}
	for _, ruta := range []string{"/health:", "/predict:", "/train:", "/metrics:", "/ws:"} {
		if !strings.Contains(cuerpo, ruta) {
			t.Errorf("la especificación no documenta %s", ruta)
		}
	}
}

func TestMetricas(t *testing.T) {
	servidor, repo := nuevoServidorDePrueba(t, true, []string{"a:1"})
	defer servidor.Cerrar()
	ctx := context.Background()
	ejecucion := &almacen.Ejecucion{Estado: almacen.EstadoTerminada, Inicio: time.Now()}
	_ = repo.InsertarEjecucion(ctx, ejecucion)
	_ = repo.InsertarMetrica(ctx, &almacen.Metrica{EjecucionID: ejecucion.ID, Epoca: 1, Perdida: 0.5})
	peticion(t, servidor, "POST", "/predict", `{"entrada":[1,1]}`)

	respuesta := peticion(t, servidor, "GET", "/metrics", "")
	if respuesta.Code != http.StatusOK {
		t.Fatalf("estado %d: %s", respuesta.Code, respuesta.Body.String())
	}
	var metricas respuestaMetricas
	if err := json.Unmarshal(respuesta.Body.Bytes(), &metricas); err != nil {
		t.Fatalf("JSON inválido: %v", err)
	}
	if !metricas.Modelo.Cargado || metricas.Modelo.Variables != 2 {
		t.Errorf("modelo inesperado: %+v", metricas.Modelo)
	}
	if metricas.UltimaEjecucion == nil || len(metricas.Metricas) != 1 {
		t.Errorf("faltan la última ejecución o sus métricas: %+v", metricas)
	}
	if metricas.API.Latencia.Total != 1 || metricas.API.CPUs == 0 {
		t.Errorf("métricas de API inesperadas: %+v", metricas.API)
	}
}

func TestEntrenarSinNodosYConflicto(t *testing.T) {
	servidor, _ := nuevoServidorDePrueba(t, false, nil)
	defer servidor.Cerrar()
	if respuesta := peticion(t, servidor, "POST", "/train", ``); respuesta.Code != http.StatusServiceUnavailable {
		t.Errorf("sin nodos: estado %d, se esperaba 503", respuesta.Code)
	}

	conNodos, _ := nuevoServidorDePrueba(t, false, []string{"127.0.0.1:1"})
	defer conNodos.Cerrar()
	conNodos.entrenando.Store(true)
	if respuesta := peticion(t, conNodos, "POST", "/train", `{}`); respuesta.Code != http.StatusConflict {
		t.Errorf("entrenamiento en curso: estado %d, se esperaba 409", respuesta.Code)
	}
	conNodos.entrenando.Store(false)
	if respuesta := peticion(t, conNodos, "POST", "/train", `{"epocas":-1}`); respuesta.Code != http.StatusBadRequest {
		t.Errorf("hiperparámetros negativos: estado %d, se esperaba 400", respuesta.Code)
	}
}

// levantarNodos inicia nodos TCP reales con particiones de un dataset sintético.
func levantarNodos(t *testing.T, cantidad int) []string {
	t.Helper()
	datos := logreg.DatosSinteticos(4000, []float64{1.5, -2.0}, -0.2, 42)
	var direcciones []string
	for indice, parte := range datos.Particionar(cantidad) {
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
	return direcciones
}

// Prueba de extremo a extremo: POST /train con 2 nodos TCP reales, progreso
// por WebSocket, métricas persistidas y predicción con el modelo entrenado.
func TestEntrenamientoDistribuidoDeExtremoAExtremo(t *testing.T) {
	nodos := levantarNodos(t, 2)
	servidor, repo := nuevoServidorDePrueba(t, false, nodos)
	defer servidor.Cerrar()
	web := httptest.NewServer(servidor.Handler())
	defer web.Close()

	enlace := "ws" + strings.TrimPrefix(web.URL, "http") + "/ws"
	cliente, _, err := websocket.DefaultDialer.Dial(enlace, nil)
	if err != nil {
		t.Fatalf("no se pudo abrir el WebSocket: %v", err)
	}
	defer cliente.Close()

	respuesta, err := http.Post(web.URL+"/train", "application/json", bytes.NewBufferString(`{"epocas":5,"lote":256}`))
	if err != nil {
		t.Fatal(err)
	}
	var iniciado respuestaEntrenamiento
	_ = json.NewDecoder(respuesta.Body).Decode(&iniciado)
	respuesta.Body.Close()
	if respuesta.StatusCode != http.StatusAccepted || iniciado.ID == "" {
		t.Fatalf("estado %d, respuesta %+v", respuesta.StatusCode, iniciado)
	}

	tipos := map[string]int{}
	_ = cliente.SetReadDeadline(time.Now().Add(20 * time.Second))
	for {
		var mensaje evento
		if err := cliente.ReadJSON(&mensaje); err != nil {
			t.Fatalf("el WebSocket se cerró antes de recibir el fin: %v (tipos %v)", err, tipos)
		}
		tipos[mensaje.Tipo]++
		if mensaje.Tipo == "error" {
			t.Fatalf("el entrenamiento falló: %v", mensaje.Datos)
		}
		if mensaje.Tipo == "fin" {
			break
		}
	}
	if tipos["conectado"] != 1 || tipos["inicio"] != 1 || tipos["progreso"] == 0 {
		t.Errorf("eventos inesperados: %v", tipos)
	}

	ejecucion, err := repo.ObtenerEjecucion(context.Background(), iniciado.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ejecucion.Estado != almacen.EstadoTerminada || ejecucion.Nodos != 2 || ejecucion.FilasTotales != 4000 {
		t.Errorf("ejecución inesperada: %+v", ejecucion)
	}
	metricas, _ := repo.MetricasDeEjecucion(context.Background(), iniciado.ID)
	if len(metricas) != tipos["progreso"] || len(metricas) == 0 {
		t.Errorf("se guardaron %d métricas y llegaron %d eventos de progreso", len(metricas), tipos["progreso"])
	}

	// El modelo entrenado ya sirve predicciones, y el WebSocket las difunde.
	prediccion := peticion(t, servidor, "POST", "/predict", `{"entrada":[1,-1]}`)
	if prediccion.Code != http.StatusOK {
		t.Fatalf("predicción con el modelo entrenado: estado %d: %s", prediccion.Code, prediccion.Body.String())
	}
	var mensaje evento
	if err := cliente.ReadJSON(&mensaje); err != nil || mensaje.Tipo != "prediccion" {
		t.Errorf("se esperaba un evento prediccion, llegó %+v (%v)", mensaje, err)
	}
}

func TestWebSocketRecibePredicciones(t *testing.T) {
	servidor, _ := nuevoServidorDePrueba(t, true, nil)
	defer servidor.Cerrar()
	web := httptest.NewServer(servidor.Handler())
	defer web.Close()

	cliente, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(web.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("no se pudo abrir el WebSocket: %v", err)
	}
	defer cliente.Close()
	_ = cliente.SetReadDeadline(time.Now().Add(5 * time.Second))

	var saludo evento
	if err := cliente.ReadJSON(&saludo); err != nil || saludo.Tipo != "conectado" {
		t.Fatalf("se esperaba el evento conectado, llegó %+v (%v)", saludo, err)
	}
	respuesta, err := http.Post(web.URL+"/predict", "application/json", strings.NewReader(`{"entrada":[1,1]}`))
	if err != nil {
		t.Fatal(err)
	}
	respuesta.Body.Close()

	var mensaje evento
	if err := cliente.ReadJSON(&mensaje); err != nil || mensaje.Tipo != "prediccion" {
		t.Errorf("se esperaba el evento prediccion, llegó %+v (%v)", mensaje, err)
	}
}

func TestPercentiles(t *testing.T) {
	var ventana ventanaLatencias
	for valor := 1; valor <= 100; valor++ {
		ventana.registrar(float64(valor))
	}
	resumen := ventana.resumen()
	if resumen.Mediana != 50 || resumen.P95 != 95 || resumen.Maxima != 100 || resumen.Muestras != 100 {
		t.Errorf("resumen inesperado: %+v", resumen)
	}
	if (&ventanaLatencias{}).resumen().Muestras != 0 {
		t.Error("una ventana vacía no debe tener muestras")
	}
}

// ---------------------------------------------------------------- lotes

func TestPredecirLoteIgualQueIndividual(t *testing.T) {
	servidor, repo := nuevoServidorDePrueba(t, true, nil)
	filas := [][]float64{{3, 0}, {0, 3}, {1, 1}}

	cuerpo, _ := json.Marshal(map[string]any{"columnas": []string{"a", "b"}, "filas": filas})
	respuesta := peticion(t, servidor, "POST", "/predict", string(cuerpo))
	if respuesta.Code != http.StatusOK {
		t.Fatalf("estado %d: %s", respuesta.Code, respuesta.Body.String())
	}
	var lote respuestaLote
	if err := json.Unmarshal(respuesta.Body.Bytes(), &lote); err != nil {
		t.Fatalf("JSON inválido: %v", err)
	}
	if lote.Total != 3 || len(lote.Predicciones) != 3 || lote.EjecucionID != "ej-prueba" {
		t.Fatalf("lote inesperado: %+v", lote)
	}

	// Cada fila debe dar lo mismo que la petición individual.
	positivas := 0
	for indice, fila := range filas {
		individual := peticion(t, servidor, "POST", "/predict", fmt.Sprintf(`{"entrada":[%v,%v]}`, fila[0], fila[1]))
		var unica respuestaPrediccion
		_ = json.Unmarshal(individual.Body.Bytes(), &unica)
		if diferencia := unica.Probabilidad - lote.Predicciones[indice].Probabilidad; diferencia > 1e-12 || diferencia < -1e-12 {
			t.Errorf("fila %d: lote %v, individual %v", indice+1, lote.Predicciones[indice].Probabilidad, unica.Probabilidad)
		}
		if unica.Clase != lote.Predicciones[indice].Clase {
			t.Errorf("fila %d: clase distinta entre lote e individual", indice+1)
		}
		positivas += unica.Clase
	}
	if lote.Positivas != positivas {
		t.Errorf("positivas %d, se esperaban %d", lote.Positivas, positivas)
	}

	servidor.Cerrar()
	guardadas, _ := repo.UltimasPredicciones(context.Background(), 0)
	if len(guardadas) != 6 { // 3 del lote + 3 individuales
		t.Errorf("se esperaban 6 predicciones guardadas, hay %d", len(guardadas))
	}
}

func TestPredecirLoteValidaLaPeticion(t *testing.T) {
	servidor, _ := nuevoServidorDePrueba(t, true, nil)
	defer servidor.Cerrar()
	muchas := make([][]float64, maxFilasLote+1)
	for indice := range muchas {
		muchas[indice] = []float64{1, 1}
	}
	excesivo, _ := json.Marshal(map[string]any{"filas": muchas})
	casos := map[string]struct{ cuerpo, fragmento string }{
		"entrada y filas":      {`{"entrada":[1,1],"filas":[[1,1]]}`, "no ambos"},
		"filas vacío":          {`{"filas":[]}`, "vacío"},
		"fila corta":           {`{"filas":[[1,1],[1]]}`, "la fila 2 tiene 1 variables"},
		"columnas incorrectas": {`{"columnas":["a"],"filas":[[1,1]]}`, "columnas"},
		"columnas con entrada": {`{"columnas":["a","b","c"],"entrada":[1,1]}`, "columnas"},
		"demasiadas filas":     {string(excesivo), "máximo"},
	}
	for nombre, caso := range casos {
		respuesta := peticion(t, servidor, "POST", "/predict", caso.cuerpo)
		if respuesta.Code != http.StatusBadRequest {
			t.Errorf("%s: estado %d, se esperaba 400", nombre, respuesta.Code)
			continue
		}
		var cuerpoError errorAPI
		_ = json.Unmarshal(respuesta.Body.Bytes(), &cuerpoError)
		if !strings.Contains(cuerpoError.Error, caso.fragmento) {
			t.Errorf("%s: el error %q no menciona %q", nombre, cuerpoError.Error, caso.fragmento)
		}
	}
}

func TestPredecirLoteAvisaPorWebSocket(t *testing.T) {
	servidor, _ := nuevoServidorDePrueba(t, true, nil)
	defer servidor.Cerrar()
	web := httptest.NewServer(servidor.Handler())
	defer web.Close()
	cliente, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(web.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cliente.Close()
	_ = cliente.SetReadDeadline(time.Now().Add(5 * time.Second))
	var saludo evento
	_ = cliente.ReadJSON(&saludo)

	respuesta, err := http.Post(web.URL+"/predict", "application/json", strings.NewReader(`{"filas":[[1,1],[2,2]]}`))
	if err != nil {
		t.Fatal(err)
	}
	respuesta.Body.Close()
	var mensaje evento
	if err := cliente.ReadJSON(&mensaje); err != nil || mensaje.Tipo != "prediccion_lote" {
		t.Errorf("se esperaba el evento prediccion_lote, llegó %+v (%v)", mensaje, err)
	}
}

// El archivo data/prueba/predicciones.json que genera src/generar_datos_prueba.py
// tiene la forma {"columnas": [...], "filas": [[...]]}: debe ser aceptado tal cual.
func TestPredecirAceptaElFormatoDeDatosDePrueba(t *testing.T) {
	servidor, _ := nuevoServidorDePrueba(t, true, nil)
	defer servidor.Cerrar()
	cuerpo := `{"columnas": ["edad", "dias_desde_inicio"], "filas": [[30, 100], [60, 400]]}`
	respuesta := peticion(t, servidor, "POST", "/predict", cuerpo)
	if respuesta.Code != http.StatusOK {
		t.Fatalf("estado %d: %s", respuesta.Code, respuesta.Body.String())
	}
}
