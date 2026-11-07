"""Tabla de ruralidad por provincia a partir de reporte.xlsx (Censo 2017, INEI).

Regla: provincia RURAL si poblacion rural > poblacion urbana (pct_rural > 0.5).
Tambien se conserva pct_rural como variable continua.
"""
import re
import sys
from pathlib import Path

import openpyxl
import pandas as pd

sys.path.insert(0, str(Path(__file__).parent))
from limpieza_comun import normalizar_escalar

# Correcciones de nombres del Excel respecto al dataset de vacunacion
ALIAS_DEPARTAMENTO = {"LA LBERTAD": "LA LIBERTAD"}
ALIAS_PROVINCIA = {}


def _num(x) -> float:
    return 0.0 if x in (None, "-", "") else float(x)


def cargar_ruralidad(ruta_xlsx: Path) -> pd.DataFrame:
    ws = openpyxl.load_workbook(ruta_xlsx, read_only=True, data_only=True).active
    filas = []
    for fila in ws.iter_rows(values_only=True):
        etiqueta = fila[1]
        if not isinstance(etiqueta, str):
            continue
        e = etiqueta.strip()
        if e.upper() == "TOTAL":
            continue
        if "CONSTITUCIONAL DEL CALLAO" in e.upper():
            dep, prov = "CALLAO", "CALLAO"
        else:
            m = re.match(r"^(.*?),\s*provincia:\s*(.*)$", e, flags=re.I)
            if not m:
                continue
            dep, prov = normalizar_escalar(m.group(1)), normalizar_escalar(m.group(2))
        dep = ALIAS_DEPARTAMENTO.get(dep, dep)
        prov = ALIAS_PROVINCIA.get(prov, prov)
        urbano, rural = _num(fila[2]), _num(fila[3])
        filas.append((dep, prov, urbano, rural))
    df = pd.DataFrame(filas, columns=["departamento", "provincia", "urbano", "rural"])
    df["total"] = df["urbano"] + df["rural"]
    df["pct_rural_provincia"] = (df["rural"] / df["total"]).astype("float32")
    df["zona_rural"] = (df["rural"] > df["urbano"]).astype("int8")
    return df


def pct_rural_departamento(rur: pd.DataFrame) -> pd.Series:
    g = rur.groupby("departamento")[["urbano", "rural"]].sum()
    return (g["rural"] / (g["rural"] + g["urbano"])).astype("float32").rename("pct_rural_depto")


if __name__ == "__main__":
    ruta = Path(sys.argv[1] if len(sys.argv) > 1 else "../reporte.xlsx")
    t = cargar_ruralidad(ruta)
    print(t.shape, "provincias |", int(t.zona_rural.sum()), "rurales")
    print(t.groupby("departamento").zona_rural.agg(["size", "sum"]))
