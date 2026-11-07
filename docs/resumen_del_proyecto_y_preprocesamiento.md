# Resumen del Proyecto: Clasificación y Seguimiento de la Vacunación Nacional (HIS-MINSA)

**Curso:** CC65 Programación Concurrente y Distribuida  
**Ciclo:** 2026-20  
**Caso Seleccionado:** N° 7 - Clasificación y Seguimiento de la Vacunación Nacional  

---

## 1. Problemática e Impacto Social

El proceso de inmunización a nivel nacional en el Perú enfrenta el reto del abandono de esquemas de vacunación (deserción tras la primera o segunda dosis), especialmente en zonas rurales o grupos poblacionales vulnerables.

* **Pregunta de Impacto Social:**  
  *¿Qué grupos poblacionales y zonas geográficas presentan mayor probabilidad de deserción en la aplicación de segundas o terceras dosis de vacunación para priorizar brigadas móviles y campañas de seguimiento?*
* **Objetivo Técnico:**  
  Implementar una solución concurrente y distribuida en **Go**, desplegada mediante **Docker Compose**, capaz de cargar masivamente registros de vacunación, entrenar un modelo de **Logistic Regression Distribuida** (mediante Descenso de Gradiente Estocástico / *Parameter Server* sobre sockets TCP) y exponer predicciones en tiempo real con baja latencia ($<100\text{ ms}$) a través de una API REST/WebSocket y una interfaz SPA.

---

## 2. Origen y Exploración Concurrente del Dataset

Se obtuvo el dataset oficial **Registro Nacional de Vacunación COVID-19** desde la Plataforma Nacional de Datos Abiertos del Perú (`datosabiertos.gob.pe`).

### Estrategia de Lectura Concurrente (Optimización de RAM)
Dado que el archivo descomprimido supera los $10\text{ GB}$ y el entorno de desarrollo contaba con una restricción de $\approx 3\text{ GB}$ de memoria RAM disponible, se diseñó un pipeline de exploración en Python aplicando paralelismo a nivel de procesos:

1. **Lectura por Bloques (*Chunking*):** División del flujo de entrada en bloques de $100,000$ registros utilizando el delimitador punto y coma (`;`) y codificación UTF-8 tolerante a fallos.
2. **Procesamiento Paralelo (`ProcessPoolExecutor`):** Distribución de los bloques entre los $16$ núcleos lógicos de la CPU para realizar el conteo de registros y detección de valores nulos de forma simultánea sin saturar la memoria principal.

### Resultados del Análisis Exploratorio
* **Tiempo de procesamiento total:** $277.69\text{ segundos}$.
* **Volumen total de registros:** $93,675,827\text{ filas}$.
* **Estructura (14 columnas):** `FECHA_CORTE`, `UUID`, `GRUPO_RIESGO`, `EDAD`, `SEXO`, `FECHA_VACUNACION`, `DOSIS`, `FABRICANTE`, `DIRESA`, `DEPARTAMENTO`, `PROVINCIA`, `DISTRITO`, `TIPO_EDAD`, `CLASIFICACION_VACUNA`.

| Columna | Registros Nulos | Porcentaje (%) | Acción Tomada |
| :--- | :---: | :---: | :--- |
| `FABRICANTE` | $52,294,146$ | $55.82\%$ | Filtrado (`dropna`) |
| `DEPARTAMENTO` / `PROVINCIA` / `DISTRITO` | $57,800$ | $0.06\%$ | Filtrado (`dropna`) |
| `CLASIFICACION_VACUNA` | $4,341$ | $0.00\%$ | Filtrado (`dropna`) |
| `EDAD` | $727$ | $0.00\%$ | Filtrado (`dropna`) |
| **Resto de columnas (9)** | $0$ | $0.00\%$ | Conservadas íntegras |

---

## 3. Limpieza y Muestreo Representativo

A pesar de que el dataset original cuenta con más de $41.3\text{ millones}$ de registros completamente íntegros (sin ningún valor nulo), manipular un archivo de varios gigabytes dificulta el control de versiones en GitHub y las pruebas iterativas locales en contenedores.

Para garantizar un entrenamiento limpio y cumplir estrictamente con los requisitos de volumen de la rúbrica ($>1,000,000\text{ de registros}$), se aplicó el siguiente procedimiento:

1. **Eliminación Estricta de Nulos:** Mediante lectura por bloques ($200,000\text{ filas}$ por iteración), se descartaron todos los registros con valores faltantes en cualquier atributo.
2. **Muestreo Aleatorio Reproducible (*Random Sampling*):** Se extrajo una muestra aleatoria representativa de **$1,500,000\text{ registros}$** (`random_state=42`) totalmente libre de valores nulos.
3. **Exportación:** El resultado se consolidó en el archivo `vacunas_covid_muestra_1.5M.csv` ($\approx 200\text{ MB}$), listo para ser ingerido por el cargador concurrente en Go (`goroutines` y `channels`).

---

## 4. Arquitectura de Destino (Siguiente Etapa en Go)

Con el dataset limpio de $1.5\text{ millones}$ de filas, la arquitectura distribuida se estructura en cuatro componentes contenerizados:

* **Parte A (Cargador Concurrente en Go):** Lectura concurrente del CSV mediante *Worker Pools* y mapeo al `VaccinationDTO`, derivando la variable objetivo `Deserto` ($0$ o $1$).
* **Parte B (Clúster ML Distribuido):** Nodos de cómputo en Go conectados por TCP que entrenan la **Regresión Logística** en paralelo enviando gradientes locales al coordinador.
* **Parte C (API Coordinadora):** Servidor HTTP/WebSocket en Go con persistencia en **MongoDB** y caché de métricas/predicciones en **Redis**.
* **Parte D (Frontend SPA):** Dashboard interactivo para visualizar métricas del clúster en tiempo real y simular el riesgo de deserción por región y grupo etario.
---

## Corrección (2026-11-06): la muestra por filas se reemplaza

La muestra `vacunas_covid_muestra_1.5M.csv` (muestreo aleatorio de filas/dosis) **no sirve para definir la deserción**: el 96 % de sus UUID aparece una sola vez. Además, `dropna` eliminaba el 55.8 % de los registros por `FABRICANTE` nulo (71 % de los desertores). El procedimiento vigente es el cargador concurrente a nivel de persona descrito en `reports/informe_limpieza_y_analisis.md` (dataset de 1,499,255 personas, deserción = 1ª dosis sin 2ª dosis).
