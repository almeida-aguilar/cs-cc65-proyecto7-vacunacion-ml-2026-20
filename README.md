# cs-cc65-proyecto7-vacunacion-ml-2026-20

## Parte A – Cargador concurrente y limpieza (Python)
Ver `reports/informe_limpieza_y_analisis.md` (procedimiento, evidencias e insights) y la sección "Reproducir" del informe.
Código en `src/`: `00_extraer.py`, `loader_concurrente.py`, `limpieza_comun.py`, `enriquecer_rural.py`, `construir_dataset.py`, `benchmark_workers.py`, `analisis_eda.py`. Los datos (`data/`) no se versionan.

### Datos (no versionados)
Los archivos de `data/` (CSV original de 10.6 GB, shards, personas, `dataset_entrenamiento.csv`, `scaler.json`, cubos y logs) pesan demasiado para git y están en `.gitignore`. Las evidencias que sí se versionan son el informe, las tablas, las figuras y las métricas de `reports/`.
- **Por ahora:** `dataset_entrenamiento.csv` (255 MB), `scaler.json`, `tabla_ruralidad.csv` y `muestra_legible.parquet` se comparten por Google Drive: _(pegar enlace aquí)_. Copiarlos en `data/` antes de ejecutar el resto del proyecto.
- **A evaluar:** cargarlos en MongoDB (ya previsto en la Parte C) o en un volumen de Docker para que el cargador en Go los lea sin depender del Drive.
