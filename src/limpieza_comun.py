"""Reglas de limpieza y validacion compartidas por el cargador concurrente.

Cada fila rechazada recibe UN solo motivo (el primero que falla, en el orden de
MOTIVOS_RECHAZO) para que: filas_leidas = filas_validas + sum(rechazos).
"""
import re
import unicodedata

import numpy as np
import pandas as pd

COLUMNAS_ORIGEN = [
    "FECHA_CORTE", "UUID", "GRUPO_RIESGO", "EDAD", "SEXO", "FECHA_VACUNACION",
    "DOSIS", "FABRICANTE", "DIRESA", "DEPARTAMENTO", "PROVINCIA", "DISTRITO",
    "TIPO_EDAD", "CLASIFICACION_VACUNA",
]
DELIMITADOR = ";"

# Limites de validacion
DOSIS_MIN, DOSIS_MAX = 1, 5
EDAD_MAX_ANIOS = 110
FECHA_MIN = pd.Timestamp("2020-09-01")   # inicio de ensayos clinicos en Peru
FECHA_MAX = pd.Timestamp("2025-11-30")   # FECHA_CORTE del dataset
EPOCA = pd.Timestamp("1970-01-01")

MOTIVOS_RECHAZO = [
    "uuid_invalido",
    "dosis_invalida",
    "fecha_invalida",
    "fecha_fuera_rango",
    "geografia_nula",
]

COLUMNAS_SALIDA = [
    "uuid", "dosis", "fecha", "edad", "edad_flag", "sexo", "grupo_riesgo",
    "fabricante", "departamento", "provincia", "distrito",
]


def normalizar_texto(serie: pd.Series) -> pd.Series:
    """Mayusculas, sin tildes, sin espacios repetidos ni en los extremos."""
    s = serie.astype("string").str.upper()
    s = s.str.normalize("NFKD").str.replace(r"[̀-ͯ]", "", regex=True)
    s = s.str.replace(r"\s+", " ", regex=True).str.strip()
    return s


def normalizar_escalar(texto: str) -> str:
    t = unicodedata.normalize("NFKD", str(texto).upper())
    t = "".join(c for c in t if not unicodedata.combining(c))
    return re.sub(r"\s+", " ", t).strip()


def limpiar_bloque(df: pd.DataFrame) -> tuple[pd.DataFrame, dict]:
    """Valida y limpia un bloque (todas las columnas como str).

    Devuelve (filas_validas, metricas). Las metricas son sumables entre bloques.
    """
    n = len(df)
    m: dict = {"filas_parseadas": n}

    # --- perfil crudo (antes de limpiar) ---
    m["nulos"] = df.isna().sum().to_dict()
    m["dosis_cruda"] = df["DOSIS"].value_counts(dropna=False).to_dict()
    m["tipo_edad"] = df["TIPO_EDAD"].value_counts(dropna=False).to_dict()
    m["clasificacion"] = df["CLASIFICACION_VACUNA"].value_counts(dropna=False).to_dict()
    m["fecha_corte"] = df["FECHA_CORTE"].value_counts(dropna=False).to_dict()
    m["fabricante_crudo"] = df["FABRICANTE"].value_counts(dropna=False).to_dict()
    edad_cruda = pd.to_numeric(df["EDAD"], errors="coerce")
    m["edad_cruda_hist"] = np.bincount(
        np.clip(edad_cruda.dropna().astype(int).to_numpy(), 0, 200), minlength=201
    ).tolist()
    m["fecha_mes_cruda"] = df["FECHA_VACUNACION"].str[:6].value_counts(dropna=False).to_dict()

    # --- conversiones ---
    uuid = pd.to_numeric(df["UUID"], errors="coerce")
    dosis = pd.to_numeric(df["DOSIS"], errors="coerce")
    fecha = pd.to_datetime(df["FECHA_VACUNACION"], format="%Y%m%d", errors="coerce")

    # --- motivo de rechazo (primero que falla) ---
    motivo = pd.Series("", index=df.index, dtype="object")

    def marcar(condicion: pd.Series, nombre: str):
        sel = condicion & (motivo == "")
        motivo[sel] = nombre

    marcar(uuid.isna(), "uuid_invalido")
    marcar(dosis.isna() | (dosis < DOSIS_MIN) | (dosis > DOSIS_MAX) | (dosis % 1 != 0), "dosis_invalida")
    marcar(fecha.isna(), "fecha_invalida")
    marcar((fecha < FECHA_MIN) | (fecha > FECHA_MAX), "fecha_fuera_rango")
    geo_nula = df["DEPARTAMENTO"].isna() | df["PROVINCIA"].isna() | df["DISTRITO"].isna()
    marcar(geo_nula, "geografia_nula")

    m["rechazos"] = motivo[motivo != ""].value_counts().to_dict()

    ok = motivo == ""
    d = df[ok]

    # --- edad en anios segun TIPO_EDAD; outliers/nulos -> NaN con bandera ---
    edad = edad_cruda[ok]
    tipo = d["TIPO_EDAD"].str.upper()
    edad_anios = edad.where(tipo != "M", edad / 12.0).where(tipo != "D", edad / 365.0)
    edad_flag = pd.Series(0, index=d.index, dtype="int8")   # 0 ok, 1 nula, 2 outlier
    edad_flag[edad_anios.isna()] = 1
    fuera = edad_anios.notna() & ((edad_anios < 0) | (edad_anios > EDAD_MAX_ANIOS))
    edad_flag[fuera] = 2
    edad_anios = edad_anios.mask(fuera)
    m["edad_nula"] = int((edad_flag == 1).sum())
    m["edad_outlier"] = int((edad_flag == 2).sum())
    m["edad_limpia_hist"] = np.bincount(
        np.clip(edad_anios.dropna().astype(int).to_numpy(), 0, 200), minlength=201
    ).tolist()

    sexo = normalizar_texto(d["SEXO"]).fillna("DESCONOCIDO")
    fab = normalizar_texto(d["FABRICANTE"]).fillna("DESCONOCIDO")
    m["fabricante_nulo_imputado"] = int(d["FABRICANTE"].isna().sum())

    out = pd.DataFrame({
        "uuid": uuid[ok].astype("int64"),
        "dosis": dosis[ok].astype("int8"),
        "fecha": ((fecha[ok] - EPOCA).dt.days).astype("int32"),
        "edad": edad_anios.astype("float32"),
        "edad_flag": edad_flag,
        "sexo": sexo,
        "grupo_riesgo": normalizar_texto(d["GRUPO_RIESGO"]).fillna("DESCONOCIDO"),
        "fabricante": fab,
        "departamento": normalizar_texto(d["DEPARTAMENTO"]),
        "provincia": normalizar_texto(d["PROVINCIA"]),
        "distrito": normalizar_texto(d["DISTRITO"]),
    })[COLUMNAS_SALIDA]
    m["filas_validas"] = len(out)
    return out, m


def sumar_metricas(acum: dict, nuevo: dict) -> dict:
    """Suma recursivamente metricas (enteros, listas numericas y dicts)."""
    for k, v in nuevo.items():
        if isinstance(v, dict):
            sub = acum.setdefault(k, {})
            for kk, vv in v.items():
                sub[str(kk)] = sub.get(str(kk), 0) + vv
        elif isinstance(v, list):
            a = acum.setdefault(k, [0] * len(v))
            for i, x in enumerate(v):
                a[i] += x
        else:
            acum[k] = acum.get(k, 0) + v
    return acum
