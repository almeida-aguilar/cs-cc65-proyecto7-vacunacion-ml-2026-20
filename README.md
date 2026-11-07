# cs-cc65-proyecto7-vacunacion-ml-2026-20

## Parte A – Cargador concurrente y limpieza (Python)
Ver `reports/informe_limpieza_y_analisis.md` (procedimiento, evidencias e insights) y la sección "Reproducir" del informe.
Código en `src/`: `00_extraer.py`, `loader_concurrente.py`, `limpieza_comun.py`, `enriquecer_rural.py`, `construir_dataset.py`, `benchmark_workers.py`, `analisis_eda.py`. Los datos (`data/`) no se versionan.

### Datos (no versionados)
Los archivos de `data/` (CSV original de 10.6 GB, shards, personas, `dataset_entrenamiento.csv`, `scaler.json`, cubos y logs) pesan demasiado para git y están en `.gitignore`. Las evidencias que sí se versionan son el informe, las tablas, las figuras y las métricas de `reports/`.
- **Por ahora:** `dataset_entrenamiento.csv` (255 MB), `scaler.json`, `tabla_ruralidad.csv` y `muestra_legible.parquet` se comparten por Google Drive: _(pegar enlace aquí)_. Copiarlos en `data/` antes de ejecutar el resto del proyecto.
- **A evaluar:** cargarlos en MongoDB (ya previsto en la Parte C) o en un volumen de Docker para que el cargador en Go los lea sin depender del Drive.


## Parte B – Entrenamiento distribuido en Go

### Qué es
Regresión logística distribuida para predecir la **deserción de segundas dosis** (`deserto = 1` si la persona recibió la 1ª dosis y nunca la 2ª). El modelo viene de la librería [`github.com/franztoledo/logreg`](https://github.com/franztoledo/logreg) v0.1.0, que ya hace el entrenamiento paralelo con goroutines. Este repo aporta:
- el **cargador concurrente** de CSV (`internal/cargador`),
- el programa de **entrenamiento** (`cmd/entrenar`),
- el programa de **benchmark** de paralelización (`cmd/benchmark`).

### Estructura
```
internal/cargador/   cargador concurrente de CSV (productor, trabajadores, consolidador) y sus tests
cmd/entrenar/        entrena el modelo, elige el umbral y guarda modelo y reportes
cmd/benchmark/       mide tiempo y speedup de la carga y del entrenamiento según los workers
reports/go/          reportes generados por los programas de Go
data/modelo/         CSV de entrada para Go (no se versiona)
```

### Requisitos
- Go 1.22 o superior.
- Python, solo para generar los datos (los mismos requisitos de la Parte A, `requirements.txt`).
- En Windows, `go test -race` necesita `gcc`. Si no lo tienes, usa `go test ./...`; GitHub Actions ejecuta `go vet ./...` y `go test -race ./...` en cada push y pull request.

### Datos
`data/` no se versiona (ver "Datos (no versionados)" en la Parte A). Los CSV para Go se generan con:

```bash
python src/exportar_para_modelo.py
```

Esto crea `data/modelo/train_raw.csv` y `data/modelo/valid_raw.csv`: todo numérico, separado por comas, con encabezado, unas 50 variables y la etiqueta `deserto` (0/1) en la **última columna**. Se usan los archivos `_raw` (sin escalar) porque la librería escala sola.

### Cómo funciona el cargador
`cargador.CargarCSV` sigue el patrón productor → trabajadores → consolidador:
1. **Productor** (una goroutine): lee el archivo línea por línea y entrega **bloques de 10.000 líneas** por el canal `bloques`. Al terminar hace `close(bloques)` para avisar que no habrá más trabajo.
2. **Trabajadores** (N goroutines): cada uno toma bloques con `for b := range bloques`, convierte el texto a números y envía su resultado por el canal `resultados`.
3. **Consolidador** (la goroutine principal): recibe los resultados, los **ordena por número de bloque** para conservar el orden del archivo y arma el `Dataset`.

No hace falta `Mutex` porque cada trabajador solo escribe en su propio resultado y lo entrega por un canal; nadie modifica memoria compartida. El canal `resultados` lo cierra una goroutine aparte, después de `wg.Wait()`, para que se cierre solo cuando **todos** los trabajadores terminaron; si lo cerrara uno de ellos, otro podría escribir en un canal cerrado. Las filas con columnas de más o de menos, valores no numéricos o etiqueta distinta de 0/1 se **rechazan y se cuentan**, sin detener la carga.

### Cómo ejecutar
Entrenar el modelo completo; carga `train_raw.csv` y `valid_raw.csv`, entrena, imprime las métricas y genera `modelo.json`, `reports/go/completo_metricas.json` y `reports/go/completo_historial.csv`:
```bash
go run ./cmd/entrenar
```

Revisión de fuga de datos: entrena sin las variables de cohorte y compara el AUC con la corrida completa; genera `modelo_sin_cohorte.json`, `reports/go/sin_cohorte_metricas.json` y `reports/go/sin_cohorte_historial.csv`:
```bash
go run ./cmd/entrenar -sin-columnas dias_desde_inicio,mes_sin,mes_cos -nombre sin_cohorte -salida modelo_sin_cohorte.json
```

Benchmark de paralelización; mide carga y entrenamiento con 1, 2, 4, 8 y todos los núcleos, imprime la tabla de tiempos y speedup y genera `reports/go/benchmark_workers_go.csv`:
```bash
go run ./cmd/benchmark
```

Pruebas (no necesitan los datos):
```bash
go test ./...
```

### Parámetros (flags)
`cmd/entrenar`:

| Flag | Por defecto | Qué hace |
|---|---|---|
| `-train` | `data/modelo/train_raw.csv` | CSV de entrenamiento |
| `-valid` | `data/modelo/valid_raw.csv` | CSV de validación (se divide en validación y prueba) |
| `-workers` | `0` | Goroutines (0 = todos los núcleos) |
| `-epocas` | `30` | Máximo de épocas |
| `-lote` | `4096` | Tamaño de mini-lote (0 = lote completo) |
| `-tasa` | `0.1` | Tasa de aprendizaje |
| `-l2` | `0` | Regularización L2 |
| `-peso-positivo` | `0` | Peso de la clase positiva (0 = negativos/positivos del train) |
| `-paciencia` | `3` | Épocas sin mejora antes de detener |
| `-sensibilidad-min` | `0.8` | Sensibilidad mínima para elegir el umbral |
| `-sin-columnas` | vacío | Columnas a excluir, separadas por comas |
| `-max-filas` | `0` | Limita las filas leídas de cada archivo (0 = todas) |
| `-salida` | `modelo.json` | Archivo donde se guarda el modelo |
| `-reportes` | `reports/go` | Carpeta de reportes |
| `-nombre` | `completo` | Prefijo de los archivos de reporte |

`cmd/benchmark`:

| Flag | Por defecto | Qué hace |
|---|---|---|
| `-train` | `data/modelo/train_raw.csv` | CSV de entrenamiento |
| `-max-filas` | `0` | Limita las filas leídas (0 = todas) |
| `-repeticiones` | `3` | Repeticiones por cantidad de workers (se usa la mediana) |
| `-epocas` | `5` | Épocas fijas del entrenamiento |
| `-salida` | `reports/go/benchmark_workers_go.csv` | CSV con la tabla de resultados |

### Qué genera cada programa en `reports/go/`
- `<nombre>_metricas.json` (`cmd/entrenar`): configuración usada, métricas en validación y en prueba (`metricas_validacion`, `metricas_prueba`) con su matriz de confusión, número de filas de cada conjunto, umbral, mejor época, tiempos de carga y de entrenamiento, columnas excluidas y uso de memoria (`memoria_mb`).
- `<nombre>_historial.csv` (`cmd/entrenar`): una fila por época con `epoca,perdida_train,perdida_valid,auc_valid,duracion_ms`.
- `benchmark_workers_go.csv` (`cmd/benchmark`): `etapa,workers,segundos,speedup` para las etapas `carga` y `entrenamiento`.

### Notas importantes
- Entrenar con 1,2 millones de filas usa del orden de **1,5 a 2 GB de RAM**.
- Con datos simulados o de prueba, usa siempre `-nombre prueba` en `cmd/entrenar` y `-salida reports/go/prueba_benchmark.csv` en `cmd/benchmark`: los archivos `reports/go/prueba*` están ignorados por git y así no se suben por error resultados que no son reales.
- `valid_raw.csv` se divide en dos mitades: la **validación** se usa para la parada temprana y para elegir el umbral, y las **métricas finales** se miden en la mitad de **prueba**, que no se usó para decidir nada.
- Si el AUC en prueba pasa de 0,95, el programa avisa de una posible fuga de datos.

### Resultados
- Modelo completo (métricas en prueba, umbral, mejor época): Pendiente: se completa con las corridas sobre los datos reales.
- Comparación sin variables de cohorte (revisión de fuga de datos): Pendiente: se completa con las corridas sobre los datos reales.
- Benchmark de paralelización (tiempos y speedup): Pendiente: se completa con las corridas sobre los datos reales.
