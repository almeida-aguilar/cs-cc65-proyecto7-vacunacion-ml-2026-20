# Limpieza y Análisis de Datos — Parte A (Cargador concurrente)

**Proyecto 7 – Clasificación y Seguimiento de la Vacunación Nacional (CC65, 2026-20)**
**Objetivo:** clasificación binaria de **deserción** = persona con 1ª dosis que nunca registra la 2ª dosis; foco en zonas rurales.
**Fuente:** Registro Nacional de Vacunación COVID-19 (datosabiertos.gob.pe), corte 2025-11-30 · Ruralidad: Censo 2017 INEI (`reporte.xlsx`).

---

## 1. Cambio de enfoque respecto a la muestra inicial

La muestra previa (`vacunas_covid_muestra_1.5M.csv`) se tomó por **filas** (una fila = una dosis) y solo con los primeros ~3 M de registros sin nulos. Comprobamos que **el 96 % de sus UUID aparece una sola vez** (1,442,106 únicos de 1.5 M), por lo que era imposible saber si alguien abandonó la 2ª dosis. Además, `dropna` eliminaba el 55.8 % de los registros solo por `FABRICANTE` nulo. Se rehízo el proceso sobre el archivo completo y **a nivel de persona (UUID)**.

## 2. Procedimiento (cargador concurrente)

Implementado en Python con `multiprocessing` (equivalente a goroutines + channels). Código: `src/loader_concurrente.py`, `src/limpieza_comun.py`.

| Etapa | Qué hace | Concurrencia |
|---|---|---|
| Productor | Divide el CSV de 10.64 GB en 318 rangos de ~32 MB alineados a salto de línea (solo `seek`, sin parsear) | Cola de tareas (`imap_unordered`) |
| **MAP** | Cada worker lee su rango, parsea, valida/limpia y reparte las filas por `UUID % 128` en shards Parquet | 6 workers |
| **REDUCE** | Cada worker procesa una partición completa (todas las dosis de las mismas personas): deduplica, arma el registro por persona y la variable objetivo | 6 workers |
| Construcción | Imputación, variables derivadas, ruralidad, target-encoding (solo train), muestreo y estandarización (`src/construir_dataset.py`) | 6 workers, 3 pasadas |
| Consolidador | Suma métricas de calidad de todos los workers (`reports/metricas_calidad.json`) | proceso principal |

**Integridad (evidencia):** 93,675,827 líneas leídas = 93,675,827 filas parseadas (0 malformadas) = 93,305,422 válidas + 370,405 rechazadas ✔.

**Rendimiento (evidencia, `figuras/20_benchmark.png`)** — fase MAP sobre los primeros 600 MB (5.54 M filas, mismo resultado con cualquier nº de workers):

| Workers | 1 | 2 | 4 | 6 | 8 | 12 |
|---|---|---|---|---|---|---|
| Tiempo (s) | 69.6 | 42.2 | 23.1 | 16.6 | 14.3 | 11.7 |
| Speedup | 1.0× | 1.65× | 3.0× | 4.2× | 4.9× | 6.0× |

Carga completa (93.7 M filas, 6 workers): MAP 236 s + REDUCE 118 s = **354 s**; el exploratorio previo tardó 278 s solo en contar nulos.

## 3. Revisión de nulos

(`tablas/01_nulos_por_columna.csv`, `figuras/01_nulos.png`; 93,675,827 filas)

| Columna | Nulos | % | Tratamiento |
|---|---:|---:|---|
| FABRICANTE | 52,294,146 | 55.83 | **No se elimina.** Categoría `DESCONOCIDO` (el nulo es informativo, ver §6) |
| DEPARTAMENTO / PROVINCIA / DISTRITO | 57,800 | 0.062 | Se descartan 57,690 filas (no se puede asignar zona rural) |
| CLASIFICACION_VACUNA | 4,341 | 0.005 | Columna casi constante (`M`), se excluye de las variables |
| EDAD | 727 | 0.001 | Se imputa con la **mediana del grupo de riesgo** y se marca `edad_imputada` |
| Resto (8) | 0 | 0 | — |

Columnas constantes o sin valor predictivo: `FECHA_CORTE` (siempre 20251130), `CLASIFICACION_VACUNA`, `DIRESA` (redundante con departamento).

## 4. Outliers e inconsistencias

(`tablas/02_rechazos_por_regla.csv`, `figuras/02_edad_antes_despues.png`, `figuras/03_dosis.png`)

| Regla | Filas afectadas | Tratamiento |
|---|---:|---|
| `DOSIS` fuera de 1–5 (valores 6, 8, 9) | 312,714 (0.33 %) | Fila rechazada. Incluye 306,284 con dosis 6 (posibles refuerzos válidos descartados por límite conservador; **no afecta** la variable objetivo, que solo usa dosis 1 y 2) y 6,430 con dosis 8/9 (errores) |
| Geografía nula | 57,690 | Rechazada |
| `FECHA_VACUNACION` fuera de [2020-09-01, 2025-11-30] | 1 | Rechazada |
| `EDAD` > 110 años | 236 | Se anula y se imputa (mediana del grupo); máx. original 134 |
| `EDAD` nula | 727 | Imputada. En total 521 personas del dataset final con edad imputada |
| `TIPO_EDAD` M/D | — | Edad convertida a años |
| Duplicados `(UUID, DOSIS)` | 616,590 | Se conserva el registro más antiguo |
| Persona con dosis ≥2 pero **sin dosis 1** | 399,818 | Excluida (historial incompleto) |
| Fecha dosis 2 anterior a dosis 1 | 4,841 | Se conservan (tienen 2ª dosis) y quedan marcadas `d2_antes_d1` |
| Sin ventana de observación (1ª dosis < 90 días antes del corte) | 3,324 | Excluidas para no etiquetar como desertor a quien aún está a tiempo |

Texto normalizado (mayúsculas, sin tildes, espacios colapsados, p. ej. `"20  A 29 AÑOS"`).

## 5. Variable objetivo y variables derivadas

- **Población:** 30,654,524 personas con dosis 1 válida y ≥ 90 días de observación.
- **`deserto` = 1** si la persona no registra dosis 2 en toda la base → **2,020,252 desertores (6.59 %)**. Clases desbalanceadas (`figuras/19_balance_clases.png`).
- **Ruralidad:** provincia rural si población rural > urbana (Censo 2017) y `pct_rural_provincia` continuo. 110 de 193 provincias del Excel son rurales.
- **Variables (solo información disponible en la 1ª dosis, sin fuga):** edad, `edad_imputada`, sexo, grupo etario (9 tramos), fabricante dosis 1 (agrupado), departamento (25), `zona_rural`, `pct_rural_provincia`, `dias_desde_inicio` de campaña, `mes_sin/mes_cos` (estacionalidad), `te_provincia`, `te_distrito` (target-encoding suavizado, **leave-one-out y calculado solo con train**).
- **No** se usan `n_dosis`, fechas posteriores ni `max_dosis` (fuga del objetivo).
- Estandarización (media/desv. de train) de edad, días y te_*; parámetros en `data/scaler.json`.
- **Split:** 80/20 determinista por un hash del UUID distinto al del muestreo (columna `split`): train 1,199,305 y valid 299,950.

**Dataset final** (`data/dataset_entrenamiento.csv`, no versionado): 1,499,255 personas × 54 columnas, **0 nulos**, UUID únicos, todo numérico para el cargador en Go. Muestra estratificada 50 % rural / 50 % urbana (por hash del UUID, reproducible) para reforzar el foco rural; la columna `peso_muestreo` permite recuperar las tasas poblacionales (deserción en la muestra 7.46 % vs 6.59 % real).

## 6. Análisis inicial e insights de negocio

Todas las cifras sobre la población completa (30.65 M personas). Tablas en `reports/tablas/`, figuras en `reports/figuras/`.

1. **La deserción rural es 1.4× la urbana:** 8.69 % vs 6.20 % (+2.49 pp, IC95 % 2.46–2.52; `10_*`). La brecha rural persiste en **todos** los grupos etarios mayores de 5 años (p. ej. 40–49 años: 5.0 % vs 3.0 %; `13_*`).
2. **La selva y la frontera concentran el abandono:** Madre de Dios 14.6 %, Loreto 12.9 %, Ucayali 11.6 %, Puno 10.3 %, Tumbes 9.3 % (`12_*`). Los distritos más críticos (≥ 500 vacunados): Ccarhuayo-Quispicanchi 46.7 %, Balsapuerto-Alto Amazonas 40.7 %, Cahuapanas-Datem del Marañón 40.3 % (`17_*`). Entre provincias rurales: Condorcanqui 23.0 %, Loreto 21.9 %, Datem del Marañón 19.7 %, Atalaya 19.7 %. **Candidatas a brigadas móviles.**
3. **El mayor predictor es cuándo se puso la 1ª dosis:** la deserción pasa de ~0.5–1.2 % (2021T1–T3) a 12.5 % (2022T1), 54 % (2022T3) y 77–84 % (2023) (`16_*`; Cramér's V = 0.49, el más alto). Las primeras dosis tardías son casos aislados que casi nunca completan el esquema. **Riesgo:** el modelo puede aprender cohorte más que comportamiento; conviene evaluar con validación temporal.
4. **Niños y adolescentes abandonan más:** 0–4 años 57 % (rural 51 %), 5–11 años 17 %; en adultos 2–5 % (`13_*`).
5. **El nulo de `FABRICANTE` es una señal:** desertan el 16.4 % de los que no lo tienen vs 3.0 % (Pfizer), 2.3 % (Sinopharm), 2.0 % (AstraZeneca) (`15_*`). Eliminar esos registros, como se hacía antes, **borraba 71 % de los desertores** (1.44 M de 2.02 M). Justifica no usar `dropna`.
6. **Hombres desertan más que mujeres:** 7.2 % vs 6.0 % (`14_*`), asociación débil (V = 0.02).
7. **Ranking de asociación (Cramér's V):** trimestre 0.49 > grupo etario 0.32 > fabricante 0.25 > departamento 0.077 > zona rural 0.036 > sexo 0.024 (`11_*`). La zona rural por sí sola es un predictor modesto, pero su efecto es consistente; el modelo debe combinarla con edad, cohorte y territorio.

## 7. Limitaciones

- **Madre de Dios no está en `reporte.xlsx`** (193 de 196 provincias: faltan Tambopata, Manu y Tahuamanu → 162,586 personas, 0.53 %). Provisionalmente se les asigna el % rural nacional y se clasifican como urbanas. Conviene completar esas filas del Excel con el Censo 2017 y repetir `construir_dataset.py`.
- El Excel viene del Censo 2017 y la clasificación rural/urbana es por provincia (no por distrito).
- Fallecidos, migrantes o quienes completaron esquema en otro sistema aparecen como desertores.
- Una dosis registrada con otro UUID (errores de identificación) infla la deserción.
- Dosis 6 descartadas por el límite de validación (sin efecto en el objetivo).
- Aún no se probó un modelo para comprobar ausencia de fuga (p. ej. AUC sospechosamente alto); las variables usadas solo dependen de la 1ª dosis, salvo la tasa por territorio (leave-one-out, solo train).

## 8. Reproducir

```bash
python src/00_extraer.py --zip ../../../VACUNAS_COVID.zip      # zip -> 7z -> CSV (10.6 GB)
python src/loader_concurrente.py --workers 6 --bloque-mb 32   # MAP + REDUCE (~6 min)
python src/construir_dataset.py --rural-xlsx ../reporte.xlsx  # dataset de entrenamiento
python src/benchmark_workers.py                               # speedup 1..12 workers
python src/analisis_eda.py                                    # tablas y figuras
```

## 9. Distribución de los datos
Los datos intermedios y finales (`data/`) no se versionan en git por su tamaño: el CSV original ocupa 10.6 GB, los shards 1.6 GB y `dataset_entrenamiento.csv` 255 MB. Los logs de ejecución tampoco se suben; sus cifras relevantes (tiempos, integridad, rechazos) están en este informe y en `reports/metricas_calidad.json`. Por el momento el dataset final se comparte por Google Drive. Queda por evaluar su carga en MongoDB o en un volumen Docker para el cargador en Go.
