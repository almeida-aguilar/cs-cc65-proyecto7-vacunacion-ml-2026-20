"""Extrae vacunas_covid.csv desde VACUNAS_COVID.zip -> vacunas_covid.7z -> CSV.

Uso:
    python src/00_extraer.py --zip <ruta .zip> --salida data/
Comprueba el espacio libre antes de extraer y borra el .7z intermedio.
"""
import argparse
import os
import shutil
import time
import zipfile
from pathlib import Path

import py7zr

ESPACIO_MINIMO_GB = 12  # 7z (1.1 GB) + CSV (~10 GB)


def extraer(ruta_zip: Path, carpeta_salida: Path) -> Path:
    carpeta_salida.mkdir(parents=True, exist_ok=True)
    csv_final = carpeta_salida / "vacunas_covid.csv"
    if csv_final.exists():
        print(f"Ya existe {csv_final}, no se extrae de nuevo.")
        return csv_final

    libre_gb = shutil.disk_usage(carpeta_salida).free / 1e9
    print(f"Espacio libre: {libre_gb:.1f} GB (minimo {ESPACIO_MINIMO_GB} GB)")
    if libre_gb < ESPACIO_MINIMO_GB:
        raise SystemExit("Espacio insuficiente para extraer el CSV.")

    t0 = time.time()
    with zipfile.ZipFile(ruta_zip) as z:
        miembro = next(n for n in z.namelist() if n.endswith(".7z"))
        print(f"[1/2] Extrayendo {miembro} del zip...")
        with z.open(miembro) as origen, open(carpeta_salida / "vacunas_covid.7z", "wb") as destino:
            shutil.copyfileobj(origen, destino, 16 * 1024 * 1024)

    ruta_7z = carpeta_salida / "vacunas_covid.7z"
    print("[2/2] Descomprimiendo el .7z (puede tardar varios minutos)...")
    with py7zr.SevenZipFile(ruta_7z, "r") as sz:
        print("Contenido:", sz.getnames())
        sz.extractall(path=carpeta_salida)
    ruta_7z.unlink()

    # Si el 7z trae el CSV con otro nombre/subcarpeta, lo normalizamos.
    if not csv_final.exists():
        candidatos = list(carpeta_salida.rglob("*.csv"))
        if len(candidatos) == 1:
            os.replace(candidatos[0], csv_final)
    print(f"Listo en {time.time() - t0:.0f} s -> {csv_final} ({csv_final.stat().st_size / 1e9:.2f} GB)")
    return csv_final


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    ap.add_argument("--zip", default="../../../VACUNAS_COVID.zip")
    ap.add_argument("--salida", default="data")
    a = ap.parse_args()
    extraer(Path(a.zip), Path(a.salida))
