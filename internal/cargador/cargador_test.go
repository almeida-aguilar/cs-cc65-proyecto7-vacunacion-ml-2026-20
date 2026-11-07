package cargador

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// escribirCSV crea un archivo temporal con el contenido dado y devuelve su ruta.
func escribirCSV(t *testing.T, contenido string) string {
	t.Helper()
	ruta := filepath.Join(t.TempDir(), "datos.csv")
	if err := os.WriteFile(ruta, []byte(contenido), 0o644); err != nil {
		t.Fatalf("no se pudo crear el CSV de prueba: %v", err)
	}
	return ruta
}

const csvBasico = "edad,sexo,zona,deserto\n" +
	"30,1,0,0\n" +
	"45,0,1,1\n" +
	"60,1,1,0\n"

func TestCargaCorrecta(t *testing.T) {
	ruta := escribirCSV(t, csvBasico)
	datos, columnas, resumen, err := CargarCSV(ruta, 2, nil, 0)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if !reflect.DeepEqual(columnas, []string{"edad", "sexo", "zona"}) {
		t.Errorf("columnas = %v", columnas)
	}
	if datos.Len() != 3 || datos.NumVariables() != 3 {
		t.Fatalf("dimensiones = %d x %d, se esperaba 3 x 3", datos.Len(), datos.NumVariables())
	}
	if datos.X[1][0] != 45 || datos.X[2][2] != 1 {
		t.Errorf("valores incorrectos: %v", datos.X)
	}
	if !reflect.DeepEqual(datos.Y, []float64{0, 1, 0}) {
		t.Errorf("etiquetas = %v", datos.Y)
	}
	if resumen.FilasLeidas != 3 || resumen.FilasValidas != 3 || resumen.FilasRechazadas != 0 {
		t.Errorf("resumen = %+v", resumen)
	}
}

func TestFinalesDeLineaWindowsYBOM(t *testing.T) {
	contenido := "\xef\xbb\xbfedad,sexo,zona,deserto\r\n30,1,0,0\r\n45,0,1,1\r\n"
	ruta := escribirCSV(t, contenido)
	datos, columnas, resumen, err := CargarCSV(ruta, 2, nil, 0)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if columnas[0] != "edad" {
		t.Errorf("el BOM no se quitó: %q", columnas[0])
	}
	if datos.Len() != 2 || resumen.FilasRechazadas != 0 {
		t.Errorf("filas = %d, resumen = %+v", datos.Len(), resumen)
	}
	if datos.Y[1] != 1 {
		t.Errorf("la etiqueta con \\r no se leyó bien: %v", datos.Y)
	}
}

func TestFilasMalasSeRechazan(t *testing.T) {
	contenido := "edad,sexo,zona,deserto\n" +
		"30,1,0,0\n" +
		"abc,1,0,0\n" + // texto en una variable
		"40,1,1\n" + // columnas de menos
		"50,1,1,0,9\n" + // columnas de más
		"35,0,0,2\n" + // etiqueta inválida
		"NaN,0,0,1\n" + // no es un número finito
		"45,0,1,1\n"
	ruta := escribirCSV(t, contenido)
	datos, _, resumen, err := CargarCSV(ruta, 2, nil, 0)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if datos.Len() != 2 {
		t.Errorf("filas válidas = %d, se esperaban 2", datos.Len())
	}
	if resumen.FilasLeidas != 7 || resumen.FilasValidas != 2 || resumen.FilasRechazadas != 5 {
		t.Errorf("resumen = %+v", resumen)
	}
}

// csvGrande genera más filas que un bloque para probar varios bloques.
func csvGrande(filas int) string {
	var sb strings.Builder
	sb.WriteString("a,b,deserto\n")
	for i := 0; i < filas; i++ {
		fmt.Fprintf(&sb, "%d,%d,%d\n", i, i*2, i%2)
	}
	return sb.String()
}

func TestMismoResultadoConUnoYOchoWorkers(t *testing.T) {
	filas := 2*TamanoBloque + 1234 // 3 bloques, el último incompleto
	ruta := escribirCSV(t, csvGrande(filas))
	d1, _, r1, err := CargarCSV(ruta, 1, nil, 0)
	if err != nil {
		t.Fatalf("error con 1 worker: %v", err)
	}
	d8, _, r8, err := CargarCSV(ruta, 8, nil, 0)
	if err != nil {
		t.Fatalf("error con 8 workers: %v", err)
	}
	if d1.Len() != filas || d8.Len() != filas {
		t.Fatalf("filas: 1 worker = %d, 8 workers = %d, esperadas %d", d1.Len(), d8.Len(), filas)
	}
	if !reflect.DeepEqual(d1.X, d8.X) || !reflect.DeepEqual(d1.Y, d8.Y) {
		t.Error("el resultado (o el orden) cambia según el número de workers")
	}
	// El orden original del archivo debe conservarse.
	for i := 0; i < filas; i++ {
		if d8.X[i][0] != float64(i) {
			t.Fatalf("fila %d fuera de orden: %v", i, d8.X[i])
		}
	}
	if r1.FilasValidas != r8.FilasValidas {
		t.Errorf("resúmenes distintos: %+v vs %+v", r1, r8)
	}
}

func TestExcluir(t *testing.T) {
	ruta := escribirCSV(t, csvBasico)
	datos, columnas, _, err := CargarCSV(ruta, 2, []string{"sexo"}, 0)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if !reflect.DeepEqual(columnas, []string{"edad", "zona"}) {
		t.Errorf("columnas = %v", columnas)
	}
	if datos.NumVariables() != 2 || datos.X[1][0] != 45 || datos.X[1][1] != 1 {
		t.Errorf("valores tras excluir: %v", datos.X)
	}
	if _, _, _, err := CargarCSV(ruta, 2, []string{"no_existe"}, 0); err == nil {
		t.Error("excluir una columna inexistente debía dar error")
	}
	if _, _, _, err := CargarCSV(ruta, 2, []string{"deserto"}, 0); err == nil {
		t.Error("excluir la etiqueta debía dar error")
	}
}

func TestMaxFilas(t *testing.T) {
	ruta := escribirCSV(t, csvGrande(25000))
	datos, _, resumen, err := CargarCSV(ruta, 4, nil, 12345)
	if err != nil {
		t.Fatalf("error inesperado: %v", err)
	}
	if datos.Len() != 12345 || resumen.FilasLeidas != 12345 {
		t.Errorf("filas = %d, resumen = %+v; se esperaban 12345", datos.Len(), resumen)
	}
}

func TestArchivoInexistenteYVacio(t *testing.T) {
	if _, _, _, err := CargarCSV(filepath.Join(t.TempDir(), "no.csv"), 1, nil, 0); err == nil {
		t.Error("un archivo inexistente debía dar error")
	}
	if _, _, _, err := CargarCSV(escribirCSV(t, ""), 1, nil, 0); err == nil {
		t.Error("un archivo vacío debía dar error")
	}
}
