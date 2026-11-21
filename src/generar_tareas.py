"""Genera docs/tareas.md a partir de los issues abiertos etiquetados pc4.

GitHub Issues es la fuente de verdad del tablero Kanban; este snapshot existe
para que las personas y los agentes puedan consultar el trabajo sin red.
"""

from __future__ import annotations

import json
import subprocess
from datetime import datetime, timezone
from pathlib import Path

ETIQUETA = "pc4"
SALIDA = Path("docs/tareas.md")
CAMPOS = "number,title,url,labels,updatedAt"


def obtener_issues() -> list[dict]:
    """Devuelve los issues abiertos de PC4 usando la CLI de GitHub."""
    resultado = subprocess.run(
        [
            "gh",
            "issue",
            "list",
            "--state",
            "open",
            "--label",
            ETIQUETA,
            "--limit",
            "100",
            "--json",
            CAMPOS,
        ],
        check=True,
        capture_output=True,
        text=True,
    )
    return json.loads(resultado.stdout)


def etiquetas_secundarias(issue: dict) -> str:
    """Formatea las etiquetas del issue, excepto la propia etiqueta pc4."""
    nombres = [
        etiqueta["name"]
        for etiqueta in issue["labels"]
        if etiqueta["name"] != ETIQUETA
    ]
    return ", ".join(f"`{nombre}`" for nombre in nombres)


def generar_markdown(issues: list[dict]) -> str:
    """Construye el contenido del snapshot en formato Markdown."""
    ordenados = sorted(issues, key=lambda issue: issue["number"])
    fecha = datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M UTC")
    lineas = [
        "# Tareas del entregable 2 (PC4)",
        "",
        f"> Generado el {fecha} desde GitHub Issues (etiqueta `{ETIQUETA}`).",
        "> **No editar a mano**: ejecuta `python src/generar_tareas.py`.",
        "",
        "## Checklist",
        "",
    ]
    for issue in ordenados:
        etiquetas = etiquetas_secundarias(issue)
        sufijo = f" — {etiquetas}" if etiquetas else ""
        lineas.append(
            f"- [ ] #{issue['number']} {issue['title']}{sufijo} — {issue['url']}"
        )
    lineas.extend(
        [
            "",
            "## Instrucciones",
            "",
            "1. Elegir **un** elemento del checklist y leer el issue con "
            "`gh issue view <N>`.",
            "2. Crear rama `feature/<slug>-<N>` desde `develop` y trabajar solo ese issue.",
            "3. PR a `develop` con `Closes #N`, plantilla completada y evidencia en "
            "`reports/pc4/`.",
            "4. Verificar con `mise run check` (o `go vet ./... && go test -race ./...`).",
            "5. Regenerar este archivo con `python src/generar_tareas.py`.",
            "",
            "El plan completo está en `docs/plan_entregable_2_pc4.md`.",
        ]
    )
    return "\n".join(lineas) + "\n"


def main() -> None:
    """Regenera el snapshot de tareas."""
    issues = obtener_issues()
    SALIDA.write_text(generar_markdown(issues), encoding="utf-8")
    print(f"Generado {SALIDA} con {len(issues)} tareas.")


if __name__ == "__main__":
    main()
