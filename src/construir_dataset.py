"""Construccion del dataset de entrenamiento a partir de data/personas/*.parquet.

Tres pasadas concurrentes sobre las 128 particiones (una por worker):
  Pasada 1: estadisticas globales (mediana de edad por grupo, conteos para
            target-encoding SOLO con train, conteos por zona para el muestreo).
  Pasada 2: imputacion, variables derivadas, muestreo estratificado por hash
            de UUID (reproducible) y cubo agregado para el EDA (poblacion completa).
  Final   : estandariza con parametros de train y escribe CSV numerico para Go.

Salidas (data/): dataset_entrenamiento.csv, scaler.json, muestra_legible.parquet,
                 cubo_eda.parquet, tabla_ruralidad.csv
"""
import argparse
import json
import sys
import time
from multiprocessing import Pool
from pathlib import Path

import numpy as np
import pandas as pd

sys.path.insert(0, str(Path(__file__).parent))
from enriquecer_rural import cargar_ruralidad, pct_rural_departamento
from limpieza_comun import EPOCA

INICIO_CAMPANA = (pd.Timestamp("2021-02-09") - EPOCA).days   # primeras vacunas en Peru
PORC_TRAIN = 80
SUAVIZADO_TE = 20.0
TAMANO_MUESTRA = 1_500_000
SEMILLA_HASH = np.uint64(11400714819323198485)
SEMILLA_HASH_SPLIT = np.uint64(14029467366897019727)

CORTES_EDAD = [0, 5, 12, 18, 30, 40, 50, 60, 70, 111]
ETIQ_EDAD = ["0-4", "5-11", "12-17", "18-29", "30-39", "40-49", "50-59", "60-69", "70+"]
FABRICANTES = ["PFIZER", "SINOPHARM", "ASTRAZENECA", "MODERNA", "DESCONOCIDO"]

# contexto global de cada worker (se fija con initializer)
CTX: dict = {}


def _init(ctx):
    CTX.update(ctx)


def _cargar_particion(archivo: Path) -> pd.DataFrame:
    df = pd.read_parquet(archivo)
    df = df[df["con_ventana"] == 1].drop(columns=["con_ventana"])
    # ruralidad por (departamento, provincia)
    df = df.merge(CTX["rural"], on=["departamento", "provincia"], how="left")
    sin_match = df["zona_rural"].isna()
    df["rural_sin_match"] = sin_match.astype("int8")
    df["pct_rural_provincia"] = df["pct_rural_provincia"].fillna(df["departamento"].map(CTX["pct_depto"]))
    df["pct_rural_provincia"] = df["pct_rural_provincia"].fillna(CTX["pct_global"]).astype("float32")
    df["zona_rural"] = np.where(sin_match, (df["pct_rural_provincia"] > 0.5).astype("int8"),
                                df["zona_rural"]).astype("int8")
    h = (df["uuid"].to_numpy().astype(np.uint64) * SEMILLA_HASH)
    df["u"] = (h >> np.uint64(11)).astype(np.float64) / float(2 ** 53)   # uniforme [0,1)
    # division train/valid con un hash DISTINTO al del muestreo (si no, el muestreo por u<frac sesgaria el split)
    h2 = (df["uuid"].to_numpy().astype(np.uint64) * SEMILLA_HASH_SPLIT) ^ np.uint64(0x9E3779B97F4A7C15)
    u2 = (h2 >> np.uint64(11)).astype(np.float64) / float(2 ** 53)
    df["train"] = ((u2 * 100) < PORC_TRAIN).astype("int8")
    return df


# ------------------------------ PASADA 1 ------------------------------------
def pasada1(archivo: Path):
    df = _cargar_particion(archivo)
    ok = df[df["edad_flag"] == 0]
    edad_hist = ok.assign(e=ok["edad"].astype(int)).groupby(["grupo_riesgo", "e"]).size()
    tr = df[df["train"] == 1]
    te_prov = tr.groupby(["departamento", "provincia"])["deserto"].agg(["size", "sum"])
    te_dist = tr.groupby(["departamento", "provincia", "distrito"])["deserto"].agg(["size", "sum"])
    zona = df.groupby("zona_rural").size()
    return {"edad_hist": edad_hist, "te_prov": te_prov, "te_dist": te_dist, "zona": zona,
            "sin_match": int(df["rural_sin_match"].sum()), "n": len(df),
            "sin_match_prov": df.loc[df["rural_sin_match"] == 1].groupby(["departamento", "provincia"]).size()}


def _sumar_series(lista):
    return pd.concat(lista).groupby(level=list(range(lista[0].index.nlevels))).sum()


def _sumar_df(lista):
    return pd.concat(lista).groupby(level=list(range(lista[0].index.nlevels))).sum()


# ------------------------------ PASADA 2 ------------------------------------
def pasada2(archivo: Path):
    df = _cargar_particion(archivo)

    # --- imputacion de edad (mediana del grupo de riesgo) ---
    imput = df["edad_flag"] > 0
    df["edad_imputada"] = imput.astype("int8")
    df["edad"] = np.where(imput, df["grupo_riesgo"].map(CTX["mediana_edad"]), df["edad"])
    df["edad"] = df["edad"].fillna(CTX["mediana_global"]).astype("float32")

    # --- variables derivadas ---
    f1 = EPOCA + pd.to_timedelta(df["fecha_d1"], unit="D")
    df["grupo_etario"] = pd.cut(df["edad"], CORTES_EDAD, labels=ETIQ_EDAD, right=False).astype(str)
    df["trimestre_d1"] = f1.dt.year.astype(str) + "T" + f1.dt.quarter.astype(str)
    df["dias_desde_inicio"] = (df["fecha_d1"] - INICIO_CAMPANA).clip(lower=0).astype("float32")
    mes = f1.dt.month.to_numpy()
    df["mes_sin"] = np.sin(2 * np.pi * mes / 12).astype("float32")
    df["mes_cos"] = np.cos(2 * np.pi * mes / 12).astype("float32")
    df["fab_agrupado"] = df["fabricante_d1"].where(df["fabricante_d1"].isin(FABRICANTES), "OTRO")
    df["sexo_masculino"] = (df["sexo"] == "MASCULINO").astype("int8")

    # --- target encoding con suavizado; leave-one-out para filas de train ---
    prior = CTX["prior"]
    for nombre, claves, tabla in (("te_provincia", ["departamento", "provincia"], CTX["te_prov"]),
                                  ("te_distrito", ["departamento", "provincia", "distrito"], CTX["te_dist"])):
        j = df[claves].merge(tabla, left_on=claves, right_index=True, how="left")
        n = j["size"].fillna(0).to_numpy()
        s = j["sum"].fillna(0).to_numpy()
        es_train = df["train"].to_numpy() == 1
        n = np.where(es_train, n - 1, n)
        s = np.where(es_train, s - df["deserto"].to_numpy(), s)
        df[nombre] = ((s + SUAVIZADO_TE * prior) / (n + SUAVIZADO_TE)).astype("float32")

    # --- cubo agregado para el EDA (poblacion completa con ventana) ---
    cubo = (df.groupby(["departamento", "provincia", "zona_rural", "sexo", "grupo_etario",
                        "fab_agrupado", "trimestre_d1"], observed=True)
              .agg(personas=("deserto", "size"), desertores=("deserto", "sum"),
                   edad_suma=("edad", "sum"), imputadas=("edad_imputada", "sum")).reset_index())
    cubo_dist = (df.groupby(["departamento", "provincia", "distrito", "zona_rural"], observed=True)
                   .agg(personas=("deserto", "size"), desertores=("deserto", "sum")).reset_index())

    # --- muestreo estratificado por zona (hash de UUID => reproducible) ---
    frac = df["zona_rural"].map(CTX["fraccion"]).to_numpy()
    muestra = df[df["u"].to_numpy() < frac].copy()
    muestra["peso_muestreo"] = (1.0 / frac[df["u"].to_numpy() < frac]).astype("float32")
    cols = ["uuid", "train", "deserto", "peso_muestreo", "edad", "edad_imputada", "sexo_masculino",
            "grupo_etario", "fab_agrupado", "departamento", "provincia", "distrito", "zona_rural",
            "pct_rural_provincia", "dias_desde_inicio", "mes_sin", "mes_cos",
            "te_provincia", "te_distrito", "trimestre_d1", "sexo"]
    return muestra[cols], cubo, cubo_dist


# ------------------------------ ORQUESTACION --------------------------------
def construir(carpeta_personas: Path, ruta_rural: Path, salida: Path, reportes: Path, workers: int):
    t0 = time.time()
    archivos = sorted(carpeta_personas.glob("personas_p*.parquet"))
    rural = cargar_ruralidad(ruta_rural)
    rural[["departamento", "provincia", "urbano", "rural", "pct_rural_provincia", "zona_rural"]] \
        .to_csv(salida / "tabla_ruralidad.csv", index=False)
    ctx = {"rural": rural[["departamento", "provincia", "pct_rural_provincia", "zona_rural"]],
           "pct_depto": pct_rural_departamento(rural).to_dict(),
           "pct_global": float(rural["rural"].sum() / rural["total"].sum())}

    # ----- pasada 1 -----
    with Pool(workers, initializer=_init, initargs=(ctx,)) as pool:
        res = pool.map(pasada1, archivos)
    edad_hist = _sumar_series([r["edad_hist"] for r in res])
    te_prov = _sumar_df([r["te_prov"] for r in res])
    te_dist = _sumar_df([r["te_dist"] for r in res])
    zona = _sumar_series([r["zona"] for r in res])
    sin_match_prov = _sumar_series([r["sin_match_prov"] for r in res if len(r["sin_match_prov"])]) \
        if any(len(r["sin_match_prov"]) for r in res) else pd.Series(dtype="int64")
    n_total = sum(r["n"] for r in res)
    n_sin_match = sum(r["sin_match"] for r in res)
    print(f"[pasada 1] personas con ventana={n_total:,} | sin match rural={n_sin_match:,} "
          f"({100 * n_sin_match / n_total:.3f}%) | {time.time() - t0:.0f}s", flush=True)

    # mediana de edad por grupo (desde histograma)
    mediana = {}
    for grupo, sub in edad_hist.groupby(level=0):
        edades = sub.droplevel(0).sort_index()
        acum = edades.cumsum()
        mediana[grupo] = float(edades.index[np.searchsorted(acum.to_numpy(), acum.iloc[-1] / 2)])
    global_h = edad_hist.groupby(level=1).sum().sort_index()
    cg = global_h.cumsum()
    mediana_global = float(global_h.index[np.searchsorted(cg.to_numpy(), cg.iloc[-1] / 2)])

    prior = float(te_prov["sum"].sum() / te_prov["size"].sum())
    n_rural, n_urbano = int(zona.get(1, 0)), int(zona.get(0, 0))
    obj_rural = min(n_rural, TAMANO_MUESTRA // 2)
    obj_urbano = min(n_urbano, TAMANO_MUESTRA - obj_rural)
    fraccion = {1: obj_rural / max(n_rural, 1), 0: obj_urbano / max(n_urbano, 1)}
    print(f"[muestreo] rural={n_rural:,} (frac {fraccion[1]:.4f}) urbano={n_urbano:,} (frac {fraccion[0]:.4f}) "
          f"prior deserto train={prior:.4f}", flush=True)

    ctx.update({"mediana_edad": mediana, "mediana_global": mediana_global, "prior": prior,
                "te_prov": te_prov, "te_dist": te_dist, "fraccion": fraccion})

    # ----- pasada 2 -----
    with Pool(workers, initializer=_init, initargs=(ctx,)) as pool:
        res2 = pool.map(pasada2, archivos)
    muestra = pd.concat([r[0] for r in res2], ignore_index=True)
    cubo = pd.concat([r[1] for r in res2], ignore_index=True)
    cubo_dist = pd.concat([r[2] for r in res2], ignore_index=True)
    claves = [c for c in cubo.columns if c not in ("personas", "desertores", "edad_suma", "imputadas")]
    cubo = cubo.groupby(claves, observed=True).sum().reset_index()
    cubo_dist = cubo_dist.groupby(["departamento", "provincia", "distrito", "zona_rural"]).sum().reset_index()
    cubo.to_parquet(salida / "cubo_eda.parquet", index=False)
    cubo_dist.to_parquet(salida / "cubo_distritos.parquet", index=False)
    print(f"[pasada 2] muestra={len(muestra):,} | cubo={len(cubo):,} filas | {time.time() - t0:.0f}s", flush=True)

    # ----- ensamblado final: numerico para Go -----
    muestra = muestra.sample(frac=1.0, random_state=42).reset_index(drop=True)
    muestra.to_parquet(salida / "muestra_legible.parquet", index=False)

    X = pd.DataFrame({"uuid": muestra["uuid"], "split": np.where(muestra["train"] == 1, "train", "valid")})
    num = muestra[["edad", "dias_desde_inicio", "te_provincia", "te_distrito"]].astype("float64")
    tr = muestra["train"] == 1
    scaler = {c: {"media": float(num.loc[tr, c].mean()), "desv": float(num.loc[tr, c].std() or 1.0)}
              for c in num.columns}
    for c in num.columns:
        X[c] = ((num[c] - scaler[c]["media"]) / scaler[c]["desv"]).round(6)
    for c in ("edad_imputada", "sexo_masculino", "zona_rural"):
        X[c] = muestra[c].astype("int8")
    X["pct_rural_provincia"] = muestra["pct_rural_provincia"].round(6)
    X["mes_sin"] = muestra["mes_sin"].round(6)
    X["mes_cos"] = muestra["mes_cos"].round(6)
    for g in ETIQ_EDAD:
        X[f"ge_{g}"] = (muestra["grupo_etario"] == g).astype("int8")
    for f in FABRICANTES + ["OTRO"]:
        X[f"fab_{f}"] = (muestra["fab_agrupado"] == f).astype("int8")
    for d in sorted(muestra["departamento"].unique()):
        X["dep_" + d.replace(" ", "_")] = (muestra["departamento"] == d).astype("int8")
    X["peso_muestreo"] = muestra["peso_muestreo"].round(4)
    X["deserto"] = muestra["deserto"].astype("int8")
    X.to_csv(salida / "dataset_entrenamiento.csv", sep=";", index=False)
    json.dump({"estandarizacion_train": scaler, "columnas": list(X.columns),
               "objetivo": "deserto", "peso": "peso_muestreo",
               "mediana_edad_por_grupo": mediana, "mediana_edad_global": mediana_global,
               "prior_te": prior, "suavizado_te": SUAVIZADO_TE, "fraccion_muestreo": {str(k): v for k, v in fraccion.items()}},
              open(salida / "scaler.json", "w", encoding="utf-8"), indent=2, ensure_ascii=False)

    resumen = {
        "personas_con_ventana": int(n_total), "sin_match_rural": int(n_sin_match),
        "provincias_sin_match": {f"{a}|{b}": int(v) for (a, b), v in sin_match_prov.items()},
        "n_rural": n_rural, "n_urbano": n_urbano, "muestra": int(len(muestra)),
        "tasa_deserto_muestra": float(X["deserto"].mean()),
        "tasa_deserto_poblacion": float(cubo["desertores"].sum() / cubo["personas"].sum()),
        "nulos_dataset_final": int(X.isna().sum().sum()), "columnas": len(X.columns),
        "uuid_unicos": bool(X["uuid"].is_unique), "segundos": time.time() - t0,
    }
    (reportes / "resumen_dataset.json").write_text(json.dumps(resumen, indent=2, ensure_ascii=False), encoding="utf-8")
    print(json.dumps(resumen, indent=2, ensure_ascii=False))


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    ap.add_argument("--personas", default="data/personas")
    ap.add_argument("--rural-xlsx", default="../reporte.xlsx")
    ap.add_argument("--salida", default="data")
    ap.add_argument("--reportes", default="reports")
    ap.add_argument("--workers", type=int, default=6)
    a = ap.parse_args()
    Path(a.reportes).mkdir(exist_ok=True)
    construir(Path(a.personas), Path(a.rural_xlsx), Path(a.salida), Path(a.reportes), a.workers)
