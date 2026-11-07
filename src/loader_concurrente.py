"""Parte A - Cargador de datos concurrente (equivalente Python de goroutines + channels).

Arquitectura (productor / workers / consolidador):

  PRODUCTOR (proceso principal)
     divide el archivo en rangos de bytes alineados a salto de linea
     y los publica en la cola de tareas (Pool.imap_unordered = canal).
  FASE MAP  (N workers)
     cada worker lee SU rango, parsea, valida/limpia y reparte las filas por
     hash(UUID) % P en shards parquet. Devuelve sus metricas de calidad.
  FASE REDUCE (N workers)
     cada worker recibe una particion completa (todas las dosis de las mismas
     personas), deduplica y construye un registro por persona con DESERTO.
  CONSOLIDADOR (proceso principal)
     agrega metricas y escribe reports/metricas_calidad.json.

Uso:
    python src/loader_concurrente.py --input data/vacunas_covid.csv --workers 14
    python src/loader_concurrente.py --input data/vacunas_covid.csv --max-mb 1000 --workers 4 --salida data/piloto
"""
import argparse
import io
import json
import os
import shutil
import time
from multiprocessing import Pool
from pathlib import Path

import numpy as np
import pandas as pd

from limpieza_comun import (
    COLUMNAS_ORIGEN, DELIMITADOR, EPOCA, FECHA_MAX, limpiar_bloque, sumar_metricas,
)

PARTICIONES = 128
VENTANA_DIAS = 90   # dosis 1 debe tener >= 90 dias de observacion hasta FECHA_CORTE


# ----------------------------------------------------------------------------
# PRODUCTOR: rangos de bytes alineados a lineas completas
# ----------------------------------------------------------------------------
def calcular_rangos(ruta: Path, tam_bloque: int, max_bytes: int | None = None):
    """Devuelve [(inicio, fin), ...] sin parsear el archivo (solo seeks)."""
    real = ruta.stat().st_size
    tam = min(real, max_bytes) if max_bytes else real
    with open(ruta, "rb") as f:
        encabezado = f.readline()
        inicio = len(encabezado)
        rangos = []
        while inicio < tam:
            fin = min(inicio + tam_bloque, tam)
            if fin < real:
                f.seek(fin)
                f.readline()           # avanzar hasta el final de la linea actual
                fin = f.tell()
            rangos.append((inicio, fin))
            inicio = fin
            tam = max(tam, 0)
    return rangos


# ----------------------------------------------------------------------------
# FASE MAP
# ----------------------------------------------------------------------------
def tarea_map(args):
    idx, ruta, inicio, fin, carpeta_shards = args
    t0 = time.time()
    with open(ruta, "rb") as f:
        f.seek(inicio)
        datos = f.read(fin - inicio)
    lineas = datos.count(b"\n")
    df = pd.read_csv(
        io.BytesIO(datos), sep=DELIMITADOR, header=None, names=COLUMNAS_ORIGEN,
        dtype=str, on_bad_lines="skip", encoding="utf-8", encoding_errors="ignore",
    )
    del datos
    limpio, m = limpiar_bloque(df)
    m["lineas_bloque"] = lineas
    m["lineas_malformadas"] = max(lineas - len(df), 0)
    m["bytes"] = fin - inicio

    if len(limpio):
        part = (limpio["uuid"].to_numpy() % PARTICIONES).astype(np.int16)
        for p in np.unique(part):
            sub = limpio[part == p]
            destino = carpeta_shards / f"p{p:03d}"
            destino.mkdir(exist_ok=True)
            sub.to_parquet(destino / f"c{idx:05d}.parquet", index=False, compression="zstd")
    m["segundos"] = time.time() - t0
    return m


# ----------------------------------------------------------------------------
# FASE REDUCE: de dosis a persona
# ----------------------------------------------------------------------------
def tarea_reduce(args):
    p, carpeta_shards, carpeta_personas = args
    archivos = sorted((carpeta_shards / f"p{p:03d}").glob("*.parquet"))
    if not archivos:
        return {"particion": p, "personas": 0}
    df = pd.concat([pd.read_parquet(a) for a in archivos], ignore_index=True)
    filas_antes = len(df)

    # Duplicados (uuid, dosis): se conserva el registro mas antiguo
    df = df.sort_values(["uuid", "dosis", "fecha"], kind="stable")
    df = df.drop_duplicates(["uuid", "dosis"], keep="first")
    duplicados = filas_antes - len(df)

    d1 = df[df["dosis"] == 1].set_index("uuid")
    todas = df.groupby("uuid").agg(
        n_dosis=("dosis", "size"), max_dosis=("dosis", "max"), fecha_ultima=("fecha", "max"))
    d2 = df[df["dosis"] == 2].set_index("uuid")["fecha"].rename("fecha_d2")
    d3 = df[df["dosis"] == 3].set_index("uuid")["fecha"].rename("fecha_d3")

    sin_dosis1 = int(len(todas) - len(d1))     # tienen dosis >1 pero no la 1
    pers = d1.drop(columns=["dosis"]).rename(columns={"fecha": "fecha_d1", "fabricante": "fabricante_d1"})
    pers = pers.join(todas).join(d2).join(d3)

    corte = (FECHA_MAX - EPOCA).days
    pers["dias_observacion"] = corte - pers["fecha_d1"]
    pers["deserto"] = pers["fecha_d2"].isna().astype("int8")
    pers["d2_antes_d1"] = (pers["fecha_d2"] < pers["fecha_d1"]).astype("int8")
    pers["con_ventana"] = (pers["dias_observacion"] >= VENTANA_DIAS).astype("int8")
    pers = pers.reset_index()
    pers.to_parquet(carpeta_personas / f"personas_p{p:03d}.parquet", index=False, compression="zstd")

    return {
        "particion": p,
        "filas_entrada": filas_antes,
        "duplicados_uuid_dosis": duplicados,
        "personas_sin_dosis1": sin_dosis1,
        "personas": int(len(pers)),
        "personas_con_ventana": int(pers["con_ventana"].sum()),
        "deserto_con_ventana": int(pers.loc[pers["con_ventana"] == 1, "deserto"].sum()),
        "d2_antes_d1": int(pers["d2_antes_d1"].sum()),
    }


# ----------------------------------------------------------------------------
def ejecutar(ruta: Path, salida: Path, workers: int, tam_bloque_mb: int, max_mb: int | None,
             solo_map: bool = False):
    carpeta_shards = salida / "shards"
    carpeta_personas = salida / "personas"
    for c in (carpeta_shards, carpeta_personas):
        if c.exists():
            shutil.rmtree(c)
        c.mkdir(parents=True)

    t_inicio = time.time()
    rangos = calcular_rangos(ruta, tam_bloque_mb * 1024 * 1024, max_mb * 1024 * 1024 if max_mb else None)
    print(f"[productor] {len(rangos)} bloques de ~{tam_bloque_mb} MB | workers={workers}", flush=True)

    # ---------- MAP ----------
    t0 = time.time()
    metricas: dict = {}
    tareas = [(i, ruta, a, b, carpeta_shards) for i, (a, b) in enumerate(rangos)]
    hechos = 0
    with Pool(workers) as pool:
        for m in pool.imap_unordered(tarea_map, tareas):
            sumar_metricas(metricas, m)
            hechos += 1
            if hechos % 10 == 0 or hechos == len(tareas):
                print(f"  [map] {hechos}/{len(tareas)} bloques | filas={metricas['filas_parseadas']:,} "
                      f"| {time.time() - t0:.0f}s", flush=True)
    t_map = time.time() - t0

    t_reduce = 0.0
    resumen_reduce: dict = {}
    if not solo_map:
        # ---------- REDUCE ----------
        t0 = time.time()
        with Pool(workers) as pool:
            for r in pool.imap_unordered(
                    tarea_reduce, [(p, carpeta_shards, carpeta_personas) for p in range(PARTICIONES)]):
                r.pop("particion")
                sumar_metricas(resumen_reduce, r)
        t_reduce = time.time() - t0
        print(f"  [reduce] {resumen_reduce.get('personas', 0):,} personas | {t_reduce:.0f}s", flush=True)

    total = time.time() - t_inicio
    metricas["tiempos"] = {"map_s": t_map, "reduce_s": t_reduce, "total_s": total, "workers": workers}
    metricas["reduce"] = resumen_reduce
    metricas["bloques"] = len(rangos)

    # Integridad: leidas = parseadas + malformadas ; parseadas = validas + rechazos
    rech = sum(metricas.get("rechazos", {}).values())
    metricas["integridad"] = {
        "lineas": metricas["lineas_bloque"],
        "parseadas": metricas["filas_parseadas"],
        "malformadas": metricas["lineas_malformadas"],
        "validas": metricas["filas_validas"],
        "rechazadas": rech,
        "cuadra": bool(metricas["filas_parseadas"] == metricas["filas_validas"] + rech),
    }
    metricas.pop("segundos", None)
    return metricas


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    ap.add_argument("--input", default="data/vacunas_covid.csv")
    ap.add_argument("--salida", default="data")
    ap.add_argument("--workers", type=int, default=max(1, (os.cpu_count() or 2) - 2))
    ap.add_argument("--bloque-mb", type=int, default=64)
    ap.add_argument("--max-mb", type=int, default=None, help="leer solo los primeros N MB (piloto)")
    ap.add_argument("--metricas", default="reports/metricas_calidad.json")
    a = ap.parse_args()

    m = ejecutar(Path(a.input), Path(a.salida), a.workers, a.bloque_mb, a.max_mb)
    Path(a.metricas).parent.mkdir(parents=True, exist_ok=True)
    Path(a.metricas).write_text(json.dumps(m, indent=2, ensure_ascii=False), encoding="utf-8")
    print(json.dumps({k: m[k] for k in ("integridad", "tiempos")}, indent=2, ensure_ascii=False))
