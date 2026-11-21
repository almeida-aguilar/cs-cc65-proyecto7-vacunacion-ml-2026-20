"""Ejecuta `go test -race -count=1 -v ./...` y guarda stdout y stderr juntos en
reports/pc4/race_test.txt (evidencia del PC4), mostrándolos también en pantalla.

Es multiplataforma: no usa `tee` ni redirecciones del shell. Devuelve el mismo
código de salida que `go test`. Ejecutar desde la raíz del repo.
"""
import argparse
import os
import subprocess
import sys
from pathlib import Path


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--salida", default="reports/pc4/race_test.txt", help="archivo de evidencia")
    parser.add_argument("paquetes", nargs="*", default=["./..."], help="paquetes Go (defecto ./...)")
    args = parser.parse_args()

    comando = ["go", "test", "-race", "-count=1", "-v", *args.paquetes]
    print("$", " ".join(comando), flush=True)
    salida = Path(args.salida)
    salida.parent.mkdir(parents=True, exist_ok=True)

    with salida.open("w", encoding="utf-8", newline="\n") as archivo:
        archivo.write("$ " + " ".join(comando) + "\n")
        proceso = subprocess.Popen(
            comando, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
            text=True, encoding="utf-8", errors="replace",
            env={**os.environ, "CGO_ENABLED": "1"},  # -race necesita cgo (y gcc en Windows)
        )
        for linea in proceso.stdout:
            sys.stdout.write(linea)
            archivo.write(linea)
        codigo = proceso.wait()
        archivo.write(f"\ncódigo de salida: {codigo}\n")

    print(f"\nSalida guardada en {salida} (código {codigo})")
    return codigo


if __name__ == "__main__":
    sys.exit(main())
