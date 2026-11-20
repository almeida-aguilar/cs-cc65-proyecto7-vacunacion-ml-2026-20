# Evidencias del PC4

Archivos que respaldan el informe del entregable 2 (ver
`docs/plan_entregable_2_pc4.md`, sección 5). Todo se ejecuta desde la raíz del
repo; las tareas `mise` funcionan igual en Windows, Linux y macOS.

| Archivo | Qué prueba | Cómo se genera | Estado |
|---|---|---|---|
| `escalamiento_cluster.csv` | Entrenamiento con 1, 2 y 4 nodos TCP, cada nodo con todos los núcleos (`workers_nodo = 0`): misma pérdida final (tolerancia relativa `1e-9`) y tiempo por configuración | `mise run pc4:escalamiento` (con `data/modelo/train_raw.csv`) | ✅ |
| `escalamiento_cluster_1core.csv` | Igual, pero con 1 núcleo por nodo (`workers_nodo = 1`): se ve el beneficio de repartir el trabajo | `mise run pc4:escalamiento -workers-nodo 1 -salida reports/pc4/escalamiento_cluster_1core.csv` | ✅ |
| `race_test.txt` | `go test -race -count=1 -v ./...` sin condiciones de carrera | `mise run pc4:race` (en Windows requiere gcc para `-race`) | ✅ |
| `latencia_api.csv` | Tiempo de cada petición a `/predict` (≥ 30, tras 3 de calentamiento) | `mise run data:prueba` y, con la API levantada, `mise run pc4:latencia` | ⏳ falta la API (#29) |
| `latencia_resumen.json` | Mediana, p95, mínimo, máximo y `cumple_100ms` de la latencia | Lo genera `pc4:latencia` junto al CSV | ⏳ falta la API (#29) |
| `postman_<endpoint>.png` | Una captura por endpoint (`/health`, `/predict`, `/train`, `/metrics`, `/docs`) con el tiempo de respuesta visible | Manual, con Postman, tras levantar la API | ⏳ falta la API (#29) |
| `docker_compose_ps.txt` | Contenedores del clúster en ejecución | `docker compose ps > reports/pc4/docker_compose_ps.txt` | ⏳ falta Docker Compose (#9) |
| `docker_logs.txt` | Registros del arranque de API y nodos | `docker compose logs > reports/pc4/docker_logs.txt` | ⏳ falta Docker Compose (#9) |
| `docker_servidor.json` | Reporte del parameter server dentro de Docker (`<nombre>_servidor.json` de `cmd/coordinador`) | Copiar el reporte generado por el coordinador del clúster | ⏳ falta Docker Compose (#9) |
| `mongosh.txt` | Consultas a MongoDB con los registros de prueba | Sesión de `mongosh` con `mise run db:up` y `mise run db:seed` (ver `docs/base_de_datos.md`) | ⏳ pendiente de guardar la salida |
| `redis_cli.txt` | Caché con TTL consultada con `redis-cli` | Sesión de `redis-cli` (#31) | ⏳ falta Redis (#31) |
| `kanban.png` | Tablero del proyecto con las tarjetas | Captura manual del tablero | ⏳ manual |

## Datos de prueba

`mise run data:prueba` crea en `data/prueba/` (ignorada por git) los archivos
`nodo1.csv` y `nodo2.csv` (primeras 20 000 filas de `data/modelo/train_raw.csv`
repartidas entre los dos nodos; si ese archivo no existe genera datos
sintéticos con la misma forma y lo avisa) y `predicciones.json`, con 30 filas de
ejemplo sin la etiqueta: `{"columnas": [...], "filas": [[...], ...]}`. Ese
archivo es el cuerpo que `pc4:latencia` envía a `/predict`.

## Notas

- **Escalamiento (1, 2 y 4 nodos, 1 199 305 filas, 30 épocas, 16 núcleos):**
  los nodos corren en el mismo proceso y la misma máquina.
  - Con todos los núcleos por nodo (`escalamiento_cluster.csv`) **no hay
    speedup**: 0.78, 0.71 y 0.70 s. Cada nodo ya usa toda la CPU, así que los
    nodos se la reparten y repartir las filas no ayuda.
  - Con 1 núcleo por nodo (`escalamiento_cluster_1core.csv`) **sí se ve el
    beneficio** de repartir el trabajo: 5.36, 2.75 y 1.48 s, es decir un
    speedup de 1.95x con 2 nodos y 3.63x con 4 (cada nodo trabaja con una
    parte de las filas y con su propio núcleo).
  - En ambos casos la **pérdida final es idéntica** (0.214779382251; diferencia
    relativa máxima ~4e-14, tolerancia `1e-9`): repartir las filas no cambia el
    modelo. Los tiempos son la mediana de 3 repeticiones y
    pueden variar entre máquinas.
- **Corridas de prueba:** usa nombres que empiecen por `prueba` (por ejemplo
  `-salida reports/pc4/prueba_escalamiento.csv` o `-salida prueba_latencia`);
  están ignorados por git. Los `.csv` reales de esta carpeta sí se versionan,
  los modelos `.json` no.
- **Latencia real:** `pc4:latencia` falla si ninguna petición responde 2xx; no
  se versionan cifras hasta medir contra la API verdadera.
