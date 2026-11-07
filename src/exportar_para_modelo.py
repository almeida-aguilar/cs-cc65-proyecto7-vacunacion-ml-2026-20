"""Adapta el dataset al formato de la libreria Go logreg (X [][]float64, Y []float64).

Genera en data/modelo/ (solo numerico, separador coma, encabezado, etiqueta `deserto` en la ULTIMA columna):
  train_raw.csv / valid_raw.csv       -> numericas SIN estandarizar (Entrenar() estandariza por si misma)
  train_scaled.csv / valid_scaled.csv -> numericas estandarizadas con media/desv. de train (scaler.json)
  columnas.json                       -> orden de columnas, indices continuos y parametros del escalador
Se excluyen uuid, split y peso_muestreo (la libreria no admite pesos por fila).
"""
import json
from pathlib import Path

import numpy as np
import pandas as pd

D = Path("data"); OUT = D / "modelo"; OUT.mkdir(exist_ok=True)
ETIQ_EDAD = ["0-4", "5-11", "12-17", "18-29", "30-39", "40-49", "50-59", "60-69", "70+"]
FAB = ["PFIZER", "SINOPHARM", "ASTRAZENECA", "MODERNA", "DESCONOCIDO", "OTRO"]
CONTINUAS = ["edad", "dias_desde_inicio", "te_provincia", "te_distrito"]

m = pd.read_parquet(D / "muestra_legible.parquet")
sc = json.loads((D / "scaler.json").read_text(encoding="utf-8"))["estandarizacion_train"]

X = pd.DataFrame(index=m.index)
for c in CONTINUAS:
    X[c] = m[c].astype("float64")
for c in ("edad_imputada", "sexo_masculino", "zona_rural"):
    X[c] = m[c].astype("int8")
X["pct_rural_provincia"] = m["pct_rural_provincia"].astype("float64")
X["mes_sin"] = m["mes_sin"].astype("float64"); X["mes_cos"] = m["mes_cos"].astype("float64")
for g in ETIQ_EDAD:
    X[f"ge_{g}"] = (m["grupo_etario"] == g).astype("int8")
for f in FAB:
    X[f"fab_{f}"] = (m["fab_agrupado"] == f).astype("int8")
for d in sorted(m["departamento"].unique()):
    X["dep_" + d.replace(" ", "_")] = (m["departamento"] == d).astype("int8")

Xs = X.copy()
for c in CONTINUAS:
    Xs[c] = (X[c] - sc[c]["media"]) / sc[c]["desv"]

y = m["deserto"].astype("int8"); tr = (m["train"] == 1).to_numpy()
for nombre, df in (("raw", X), ("scaled", Xs)):
    for parte, mask in (("train", tr), ("valid", ~tr)):
        out = df[mask].copy(); out["deserto"] = y[mask]
        out.to_csv(OUT / f"{parte}_{nombre}.csv", index=False, float_format="%.8g")

json.dump({"columnas_features": list(X.columns), "etiqueta": "deserto", "n_variables": X.shape[1],
           "indices_continuas": [list(X.columns).index(c) for c in CONTINUAS],
           "escalador_train_para_continuas": sc,
           "filas": {"train": int(tr.sum()), "valid": int((~tr).sum())}},
          open(OUT / "columnas.json", "w", encoding="utf-8"), indent=2)
print(X.shape, "train", int(tr.sum()), "valid", int((~tr).sum()), "nulos", int(X.isna().sum().sum()))
