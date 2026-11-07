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
- Entrenar con 1,2 millones de filas usa unos **1,3 GB** (medido: 1,23 GB de heap y 1,32 GB de memoria del sistema).
- Con datos simulados o de prueba, usa siempre `-nombre prueba` en `cmd/entrenar` y `-salida reports/go/prueba_benchmark.csv` en `cmd/benchmark`: los archivos `reports/go/prueba*` están ignorados por git y así no se suben por error resultados que no son reales.
- `valid_raw.csv` se divide en dos mitades: la **validación** se usa para la parada temprana y para elegir el umbral, y las **métricas finales** se miden en la mitad de **prueba**, que no se usó para decidir nada.
- Si el AUC en prueba pasa de 0,95, el programa avisa de una posible fuga de datos.

### Resultados
Corridas con los datos reales: 1.199.305 filas de entrenamiento y 149.975 filas en cada mitad de `valid` (validación y prueba). Máquina: 16 CPUs, Windows, Go 1.27.1. Los archivos de origen están en `reports/go/`. Las métricas son sobre la **muestra del dataset**, donde la población rural está sobrerrepresentada, así que no deben leerse como el desempeño sobre toda la población.

#### (a) Modelo completo (50 variables)
Métricas en la mitad de **prueba** (datos no usados para elegir nada), con umbral 0,48 elegido en validación. Fuente: `completo_metricas.json` y `completo_historial.csv`.

| Métrica | Valor |
|---|---|
| AUC | 0,878 |
| Sensibilidad | 0,814 |
| Precisión | 0,236 |
| Especificidad | 0,788 |
| F1 | 0,366 |

- La mejor época fue la 5 y el entrenamiento se detuvo en la 8 por la parada temprana (paciencia 3).
- Solo cerca del 7,5 % de las personas deserta, por eso la precisión es baja aunque la sensibilidad sea alta: el umbral se eligió para detectar al menos el 80 % de los que desertan, y eso trae muchos falsos positivos.
- Los resultados en validación y en prueba son parecidos (AUC 0,875 y 0,878), así que no se ve sobreajuste a la mitad de validación.

#### (b) Con y sin variables de cohorte
Revisión de fuga de datos: se quitan `dias_desde_inicio`, `mes_sin` y `mes_cos` (47 variables). Métricas en **prueba**. Fuente: `completo_metricas.json` y `sin_cohorte_metricas.json`.

| Métrica | Completo (50 variables) | Sin cohorte (47 variables) |
|---|---|---|
| AUC | 0,878 | 0,833 |
| Sensibilidad | 0,814 | 0,809 |
| Precisión | 0,236 | 0,182 |
| F1 | 0,366 | 0,297 |
| Umbral | 0,48 | 0,46 |
| Mejor época | 5 | 6 |

- Sin las variables de cohorte el AUC baja de 0,878 a 0,833 y la precisión de 0,236 a 0,182: esas variables aportan información útil.
- En el modelo completo, `dias_desde_inicio` es la variable con más peso, por mucho margen sobre las demás. Quitar las tres variables de cohorte baja el AUC de 0,878 a 0,833, pero el modelo sigue siendo útil, y el AUC del modelo completo está lejos de 0,95, el valor a partir del cual el programa avisa de posible fuga. Es decir, las variables de cohorte aportan señal real, aunque dependen del momento de la campaña (corte de datos) y no se generalizarían a otra campaña.
- La sensibilidad es casi igual en los dos porque el umbral se elige para cumplir una sensibilidad mínima de 0,8.

#### (c) Benchmark de paralelización
Mediana de 3 repeticiones por cantidad de workers. Speedup = tiempo con 1 worker / tiempo con N workers. Fuente: `benchmark_workers_go.csv`.

| Etapa | Workers | Tiempo (s) | Speedup |
|---|---|---|---|
| carga | 1 | 1,686 | 1,00x |
| carga | 2 | 0,919 | 1,84x |
| carga | 4 | 0,516 | 3,27x |
| carga | 8 | 0,357 | 4,72x |
| carga | 16 | 0,349 | 4,84x |
| entrenamiento | 1 | 0,889 | 1,00x |
| entrenamiento | 2 | 0,543 | 1,64x |
| entrenamiento | 4 | 0,373 | 2,38x |
| entrenamiento | 8 | 0,310 | 2,87x |
| entrenamiento | 16 | 0,310 | 2,87x |

- La pérdida final del entrenamiento fue la misma con todas las cantidades de workers (diferencia relativa de 5,8e-14), así que el resultado no depende del número de goroutines.
- El speedup no es lineal por la ley de Amdahl: una parte del trabajo sigue siendo secuencial. En la carga, un solo productor lee el archivo del disco; en el entrenamiento, hay que sumar los gradientes de todos los workers antes de actualizar los pesos.
- A partir de 8 workers casi no hay mejora (carga 4,72x a 4,84x; entrenamiento 2,87x a 2,87x): la parte secuencial domina y el costo de coordinar más goroutines compensa lo que se gana.
