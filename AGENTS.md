# AGENTS.md

Guía para agentes y colaboradores de este repositorio. **Todo el contenido del
proyecto (comentarios, mensajes de error, reportes, documentación y nombres de
variables) está en español**: mantén ese idioma en cualquier cambio.

## 1. Qué es este repositorio

Proyecto de la asignatura **CC65 – Programación Concurrente y Distribuida**
(ciclo 2026-20), caso 7: clasificación y seguimiento de la vacunación nacional
(HIS-MINSA, Perú). El objetivo de ML es predecir la **deserción de segundas
dosis**: `deserto = 1` si la persona recibió la 1ª dosis y nunca la 2ª.

El trabajo está dividido en dos partes que se encadenan:

- **Parte A (Python):** exploración, limpieza concurrente del CSV original
  (~10.6 GB), construcción del dataset y exportación a CSV numérico.
- **Parte B (Go):** cargador concurrente de ese CSV, entrenamiento de regresión
  logística con la librería externa `github.com/franztoledo/logreg` v0.1.0 y
  benchmark de paralelización.

De la arquitectura "de destino" descrita en
`docs/resumen_del_proyecto_y_preprocesamiento.md`, el **parameter server por TCP
sí está implementado** en `internal/coordinador` y `cmd/coordinador` (PC4, ver
`docs/protocolo_pc4.md`). Docker Compose, MongoDB, Redis y la SPA **todavía no
están implementados**; no los des como existentes. La documentación vigente de la Parte A es
`reports/informe_limpieza_y_analisis.md`; el resumen con la muestra por filas
quedó corregido al final de `docs/resumen_del_proyecto_y_preprocesamiento.md`.

## 2. Reglas antes de tocar nada

1. Trabaja siempre **desde la raíz del repo**: los scripts Python y los
   programas Go usan rutas relativas (`data/`, `reports/`).
2. **`data/` no se versiona** (`.gitignore`): contiene el CSV original, shards,
   parquets, `dataset_entrenamiento.csv`, `scaler.json` y los CSV para Go. Los
   archivos finales se comparten por Google Drive (enlace en el README); para
   ejecutar el entrenamiento hay que copiarlos a `data/` primero.
3. **No subas datos, modelos ni resultados de pruebas.** Ya están ignorados:
   `data/`, `*.csv`, `*.parquet`, `modelo.json`, `modelo_*.json` y
   `reports/go/prueba*`. La excepción intencional son los reportes reales
   `reports/go/*.csv`, que sí se versionan (`!reports/go/*.csv` en
   `.gitignore`).
4. Para corridas de prueba usa siempre `-nombre prueba` en `cmd/entrenar` y
   `-salida reports/go/prueba_benchmark.csv` en `cmd/benchmark`, para no
   sobrescribir los resultados reales.
5. No inventes cifras de métricas. Los números publicados en el README
   provienen de `reports/go/*_metricas.json` y `reports/go/benchmark_workers_go.csv`.

## 3. Estructura

| Ruta | Contenido |
|---|---|
| `src/00_extraer.py` | Descomprime el zip/7z original en `data/vacunas_covid.csv` (requiere ≥12 GB libres). |
| `src/loader_concurrente.py` | Cargador Python productor → MAP → REDUCE con `multiprocessing.Pool`; reparte por `hash(UUID) % 128`, agrupa por persona y deriva `deserto`. |
| `src/limpieza_comun.py` | Reglas de validación compartidas; cada fila rechazada tiene **un solo** motivo (el primero que falla en `MOTIVOS_RECHAZO`). |
| `src/enriquecer_rural.py` | Cruce con la tabla de ruralidad. |
| `src/construir_dataset.py` | Tres pasadas sobre las particiones: estadísticas, imputación/derivadas/muestreo estratificado y ensamblado final. |
| `src/exportar_para_modelo.py` | Genera `data/modelo/{train,valid}_{raw,scaled}.csv` y `columnas.json`. |
| `src/benchmark_workers.py`, `src/grafica_benchmark.py` | Evidencia de speedup de la fase MAP y su gráfica. |
| `src/analisis_eda.py`, `src/exploracion/` | Tablas/figuras del EDA. |
| `internal/cargador/` | Cargador concurrente de CSV en Go + tests. |
| `internal/coordinador/` | Parameter server del clúster ML + tests con 2 nodos TCP. |
| `cmd/entrenar/` | Entrena, elige umbral, guarda modelo y reportes. |
| `cmd/benchmark/` | Mide carga y entrenamiento con 1, 2, 4, 8 y todos los núcleos. |
| `cmd/coordinador/` | Entrena distribuido y guarda modelo + reporte del servidor. |
| `reports/` | Informes, métricas, figuras y reportes de Go versionados. |
| `docs/` | Resumen del caso, preprocesamiento, plan PC4 y protocolo TCP. |

## 4. Comandos

```bash
# Go: comprobación rápida (no necesita data/)
go vet ./...
go test ./...
go test -race ./...          # en Windows necesita gcc; CI lo ejecuta en ubuntu

# Go: ejecución real (necesita data/modelo/*_raw.csv)
python src/exportar_para_modelo.py        # si aún no están los CSV para Go
go run ./cmd/entrenar
go run ./cmd/benchmark

# Pruebas con datos simulados: usar prefijo "prueba"
go run ./cmd/entrenar -nombre prueba -max-filas 10000
go run ./cmd/benchmark -max-filas 10000 -salida reports/go/prueba_benchmark.csv

# Python: pipeline completo (ver sección "Reproducir" del informe)
pip install -r requirements.txt
python src/00_extraer.py --zip ../../../VACUNAS_COVID.zip
python src/loader_concurrente.py --workers 6 --bloque-mb 32
python src/construir_dataset.py --rural-xlsx ../reporte.xlsx
python src/benchmark_workers.py
python src/analisis_eda.py
```

Requisitos: Go 1.22 o superior (CI usa 1.22) y Python con `requirements.txt`
(pandas, numpy, pyarrow, openpyxl, matplotlib, py7zr).

### Con mise (forma preferida)

`mise.toml` fija Go 1.22 y Python 3.12 (con `.venv` ignorado por git) y
`mise.lock` congela las versiones exactas; CI usa la misma configuración.
No instales Go/Python a mano:

```bash
mise trust && mise install --locked
mise run setup        # go mod download + pip install -r requirements.txt
mise run check        # vet + go test -race (igual que CI)
mise run test:cluster # pruebas del cluster con 2 nodos TCP, -race y sin caché
mise run pipeline     # pasos de datos en orden
mise run train        # reenvía flags: mise run train -max-filas 10000
mise run bench        # reenvía flags: mise run bench -salida reports/go/prueba_benchmark.csv
mise tasks ls         # descubre todas las tareas
```

`train:prueba` y `bench:prueba` ya usan los prefijos seguros. Si añades una
tarea, dale `description` en `mise.toml` y documenta el cambio en el README;
si cambias versiones de herramientas, regenera `mise.lock` con `mise lock`.
El pipeline y los reportes se encadenan con pasos `run = [{ task = ... }]`
porque el orden importa; `depends` se usa solo para tareas paralelas e
independientes como `vet` y `test:race`.

## 5. Parte B: invariantes del código Go

El módulo es `github.com/almeida-aguilar/cs-cc65-proyecto7-vacunacion-ml-2026-20`.

### Cargador (`internal/cargador`)

`CargarCSV(ruta, workers, excluir, maxFilas)` implementa
**productor → trabajadores → consolidador**:

- El productor lee una sola goroutine y envía bloques de `TamanoBloque = 10000`
  líneas por el canal `bloques`; al terminar hace `close(bloques)`.
- Los trabajadores hacen `for b := range bloques`, convierten a números y envían
  su propio resultado por `resultados`. **No hay mutex**: nadie escribe memoria
  compartida.
- Una goroutine aparte hace `wg.Wait()` y `close(resultados)`; cerrarlo desde un
  trabajador provocaría panic.
- El consolidador ordena los resultados por `indice` para **conservar el orden
  del archivo** sin importar el número de workers.
- Las filas con columnas de más/menos, valores no numéricos, NaN/Inf o etiqueta
  distinta de 0/1 se **rechazan y se cuentan**, sin detener la carga
  (`filas_leidas = filas_validas + filas_rechazadas`).
- La última columna es siempre la etiqueta `deserto` y nunca se excluye.
  `workers <= 0` usa `runtime.NumCPU()`; `maxFilas > 0` limita la lectura.

Si modificas el cargador, conserva estos invariantes y amplía los tests de
`cargador_test.go`: CRLF/BOM, filas malas, orden con varios bloques, equivalencia
1 vs 8 workers, exclusión de columnas y `maxFilas`.

### Entrenamiento y benchmark

- `cmd/entrenar` divide `valid_raw.csv` en dos mitades con semilla 42:
  **validación** sirve para parada temprana y para elegir el umbral
  (`CriterioSensibilidadMinima`, por defecto 0.8); **prueba** solo se usa para
  medir. Nunca uses la mitad de prueba para tomar decisiones.
- El peso de la clase positiva por defecto es `negativos/positivos` del train.
- Si el AUC en prueba supera 0.95, el programa avisa de posible fuga de datos.
- La revisión de fuga de cohorte se ejecuta con
  `-sin-columnas dias_desde_inicio,mes_sin,mes_cos -nombre sin_cohorte -salida modelo_sin_cohorte.json`.
- `cmd/benchmark` usa mediana de 3 repeticiones, calentamiento previo de caché,
  lote completo, semilla 42 y verifica que la pérdida final sea la misma con
  distinto número de workers (tolerancia relativa `1e-9`).
- Formatos de reporte: `<nombre>_metricas.json`, `<nombre>_historial.csv`
  (`epoca,perdida_train,perdida_valid,auc_valid,duracion_ms`) y
  `benchmark_workers_go.csv` (`etapa,workers,segundos,speedup`). Si cambias un
  formato, actualiza también el README y el informe.

## 6. Parte A: invariantes del pipeline Python

- El dataset original usa `;` y codificación UTF-8 tolerante a fallos; se lee
  por bloques alineados a saltos de línea (`calcular_rangos`).
- La edad se convierte a años según `TIPO_EDAD` (M → meses, D → días); nulos y
  outliers se marcan con `edad_flag` (0 ok, 1 nula, 2 outlier) en vez de
  descartarse.
- `VENTANA_DIAS = 90`: se exige observación desde la 1ª dosis hasta
  `FECHA_MAX = 2025-11-30` para clasificar deserción.
- El split train/valid y el muestreo usan **hashes distintos del UUID**
  (`SEMILLA_HASH_SPLIT` vs `SEMILLA_HASH`) para que el muestreo estratificado no
  sesgue el split; `random_state=42` en el barajado final.
- El target encoding usa solo train, con suavizado (`SUAVIZADO_TE = 20.0`) y
  leave-one-out para las filas de entrenamiento. No lo calcules con valid/prueba.
- `dataset_entrenamiento.csv` va sin escalar las continuas (la librería Go
  escala por su cuenta); `exportar_para_modelo.py` genera también las versiones
  escaladas y deja la etiqueta `deserto` en la **última columna**.
- Las métricas de calidad deben cuadrar: `líneas = parseadas + malformadas` y
  `parseadas = válidas + rechazadas` (`reports/metricas_calidad.json`).

## 7. Convenciones de estilo

- **Go:** `gofmt` obligatorio; nombres exportados en PascalCase (`CargarCSV`,
  `TamanoBloque`), resto en camelCase. Errores con `fmt.Errorf("...: %w", err)`
  y texto en español. Comentario de paquete y de funciones exportadas. Tests
  con `testing`, mensajes `t.Errorf`/`t.Fatalf` en español y CSV temporales con
  `t.TempDir()`.
- **Python:** nombres en `snake_case`, constantes en MAYÚSCULAS, docstrings de
  módulo y funciones en español, `argparse` con valores por defecto relativos a
  la raíz. Los helpers de `src/` se importan directamente (`from limpieza_comun
  import ...`); los scripts que lo necesitan insertan `src/` en `sys.path`.
- Evita nombres genéricos (`x`, `temp`) y variables sin uso. La plantilla de PR
  lo exige explícitamente.
- No cambies el formato de los archivos versionados en `reports/` sin regenerar
  también las figuras/tablas que dependen de ellos.

## 8. Flujo de trabajo

- Ramas: `main` (release), `develop`, `feature/*`, `hotfix/*`, `release/*`.
  Los PR se abren contra `develop` o `main` según corresponda.
- Mensajes de commit en español con prefijo: `add:`, `fix:`, `test:`, `docs:`
  (ver `git log`).
- Completa `.github/pull_request_template.md`: descripción, tarjeta/issue,
  tipo de cambio y checklist (`go build`, `go test -race`, nombres descriptivos).
- CI (`.github/workflows/go.yml`) ejecuta en cada push y PR:
  `go vet ./...` y `go test -race ./...` con Go 1.22. Los tests **no** dependen
  de `data/`, así que deben seguir funcionando sin los datos.

## 9. Checklist antes de entregar

- [ ] `gofmt -w` en los archivos Go modificados (o `mise run fmt`).
- [ ] `mise run check` pasa sin datos (equivale a `go vet ./...` y `go test -race ./...`).
- [ ] Si cambiaron herramientas o dependencias, `mise.lock` está actualizado.
- [ ] Si tocaste concurrencia, se verificó equivalencia de resultados con
      distinto número de workers y no hay condiciones de carrera.
- [ ] No se versionaron archivos de `data/`, modelos ni reportes `prueba*`.
- [ ] Comentarios, errores y documentación siguen en español.
- [ ] Métricas y cifras citadas concuerdan con los JSON/CSV de `reports/`.
- [ ] Se respeta la separación validación/prueba y no hay fuga de datos.
