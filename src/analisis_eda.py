"""EDA y evidencias de limpieza. Lee reports/metricas_calidad.json, data/cubo_eda.parquet y
data/cubo_distritos.parquet; escribe tablas CSV y figuras PNG en reports/."""
import json
import math
from pathlib import Path

import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt
import numpy as np
import pandas as pd

R = Path("reports"); D = Path("data")
(R / "figuras").mkdir(parents=True, exist_ok=True)
(R / "tablas").mkdir(parents=True, exist_ok=True)

AZUL, NARANJA, GRIS, TINTA = "#2a6fb0", "#d9731a", "#b9bec6", "#2b2f36"
plt.rcParams.update({"font.size": 10, "axes.spines.top": False, "axes.spines.right": False,
                     "axes.edgecolor": GRIS, "axes.labelcolor": TINTA, "text.color": TINTA,
                     "xtick.color": TINTA, "ytick.color": TINTA, "axes.grid": True,
                     "grid.color": "#e8eaee", "grid.linewidth": 0.6, "axes.axisbelow": True,
                     "figure.dpi": 130})


def guardar(fig, nombre):
    fig.tight_layout()
    fig.savefig(R / "figuras" / nombre)
    plt.close(fig)


def tabla(df, nombre):
    df.to_csv(R / "tablas" / nombre, index=False)


m = json.loads((R / "metricas_calidad.json").read_text(encoding="utf-8"))
cubo = pd.read_parquet(D / "cubo_eda.parquet")
dist = pd.read_parquet(D / "cubo_distritos.parquet")
ins = {}

# ---------------------------------------------------------------- 1. calidad
n = m["filas_parseadas"]
nul = pd.DataFrame({"columna": list(m["nulos"]), "nulos": list(m["nulos"].values())})
nul["pct"] = (nul["nulos"] / n * 100).round(3)
nul = nul.sort_values("nulos", ascending=False)
tabla(nul, "01_nulos_por_columna.csv")
fig, ax = plt.subplots(figsize=(7, 3.6))
v = nul[nul.nulos > 0].iloc[::-1]
ax.barh(v["columna"], v["pct"], color=AZUL, height=0.55)
for y, p in enumerate(v["pct"]):
    ax.text(p, y, f" {p:.2f}%", va="center", fontsize=9)
ax.set_xlabel("% de filas nulas (93.7 M filas)"); ax.set_title("Valores nulos por columna", loc="left")
guardar(fig, "01_nulos.png")

rech = pd.Series(m["rechazos"]).rename("filas").reset_index().rename(columns={"index": "motivo"})
rech["pct"] = (rech["filas"] / n * 100).round(4)
tabla(rech, "02_rechazos_por_regla.csv")

edad_raw = np.array(m["edad_cruda_hist"]); edad_ok = np.array(m["edad_limpia_hist"])
fig, ax = plt.subplots(figsize=(7, 3.6))
ax.bar(range(len(edad_raw)), edad_raw, color=GRIS, width=1.0, label="Antes (EDAD cruda)")
ax.bar(range(len(edad_ok)), edad_ok, color=AZUL, width=0.8, label="Después (años, 0-110)")
ax.axvline(110, color=NARANJA, lw=1.2); ax.text(112, edad_raw.max() * 0.8, "límite 110", color=TINTA, fontsize=9)
ax.set_xlim(0, 150); ax.set_xlabel("Edad (años)"); ax.set_ylabel("Registros")
ax.legend(frameon=False); ax.set_title("Distribución de edad antes y después de la limpieza", loc="left")
guardar(fig, "02_edad_antes_despues.png")
ins["edad_outliers"] = {"nulos": m["edad_nula"], "outliers": m["edad_outlier"],
                        "crudos_mayor_110": int(edad_raw[111:].sum())}

dos = pd.Series(m["dosis_cruda"]).rename("filas").reset_index().rename(columns={"index": "dosis"})
tabla(dos, "03_dosis_cruda.csv")
fig, ax = plt.subplots(figsize=(7, 3.4))
ax.bar(dos["dosis"].astype(str), dos["filas"], color=[AZUL if 1 <= float(x) <= 5 else NARANJA for x in dos["dosis"]], width=0.6)
ax.set_yscale("log"); ax.set_xlabel("DOSIS (naranja = inválida)"); ax.set_ylabel("Registros (log)")
ax.set_title("Valores de DOSIS en el archivo original", loc="left")
guardar(fig, "03_dosis.png")

mes = pd.Series(m["fecha_mes_cruda"]); mes = mes[mes.index.str.match(r"^\d{6}$")].sort_index()
fig, ax = plt.subplots(figsize=(8, 3.4))
ax.plot(range(len(mes)), mes.values, color=AZUL, lw=2)
t = list(range(0, len(mes), 6)); ax.set_xticks(t); ax.set_xticklabels([mes.index[i] for i in t], rotation=60, fontsize=8)
ax.set_ylabel("Dosis registradas"); ax.set_title("Dosis aplicadas por mes (fecha de vacunación)", loc="left")
guardar(fig, "04_dosis_por_mes.png")

# ------------------------------------------------- 2. deserción (población)
N, DES = cubo["personas"].sum(), cubo["desertores"].sum()
tasa = DES / N
ins["global"] = {"personas": int(N), "desertores": int(DES), "tasa": float(tasa)}


def por(col, df=cubo, minimo=0):
    g = df.groupby(col, observed=True)[["personas", "desertores"]].sum().reset_index()
    g["tasa_desercion"] = g["desertores"] / g["personas"]
    return g[g["personas"] >= minimo]


def cramers_v(g):
    obs = np.column_stack([g["desertores"], g["personas"] - g["desertores"]]).astype(float)
    tot = obs.sum(); esp = obs.sum(1, keepdims=True) * obs.sum(0, keepdims=True) / tot
    chi2 = ((obs - esp) ** 2 / esp).sum()
    return math.sqrt(chi2 / tot / (min(obs.shape) - 1)), chi2


zona = por("zona_rural"); zona["zona"] = zona["zona_rural"].map({0: "Urbana", 1: "Rural"})
tabla(zona, "10_desercion_zona.csv")
tu, tr_ = zona.set_index("zona_rural").loc[0, "tasa_desercion"], zona.set_index("zona_rural").loc[1, "tasa_desercion"]
nu, nr = zona.set_index("zona_rural").loc[0, "personas"], zona.set_index("zona_rural").loc[1, "personas"]
se = math.sqrt(tr_ * (1 - tr_) / nr + tu * (1 - tu) / nu)
ins["rural_vs_urbano"] = {"tasa_rural": tr_, "tasa_urbana": tu, "riesgo_relativo": tr_ / tu,
                          "diferencia_pp": (tr_ - tu) * 100, "ic95_diff_pp": [(tr_ - tu - 1.96 * se) * 100, (tr_ - tu + 1.96 * se) * 100],
                          "personas_rural": int(nr), "personas_urbana": int(nu)}

v_tab = []
for col in ["zona_rural", "departamento", "grupo_etario", "sexo", "fab_agrupado", "trimestre_d1"]:
    g = por(col); v, c2 = cramers_v(g); v_tab.append((col, round(v, 4), round(c2, 1)))
vdf = pd.DataFrame(v_tab, columns=["variable", "cramers_v", "chi2"]).sort_values("cramers_v", ascending=False)
tabla(vdf, "11_asociacion_cramers_v.csv"); ins["cramers_v"] = vdf.to_dict("records")

fig, ax = plt.subplots(figsize=(5.2, 3.4))
ax.bar(zona["zona"], zona["tasa_desercion"] * 100, color=[AZUL, NARANJA], width=0.5)
for i, r in zona.reset_index(drop=True).iterrows():
    ax.text(i, r["tasa_desercion"] * 100 + 0.5, f'{r["tasa_desercion"]*100:.1f}%\n(n={r["personas"]/1e6:.1f} M)', ha="center", fontsize=9)
ax.set_ylim(0, zona["tasa_desercion"].max() * 100 * 1.25); ax.set_ylabel("% sin 2ª dosis")
ax.set_title("Deserción 1ª → 2ª dosis: zona rural vs urbana", loc="left", fontsize=10)
guardar(fig, "10_desercion_zona.png")

dep = por("departamento").merge(cubo.groupby("departamento").apply(
    lambda x: np.average(x["zona_rural"], weights=x["personas"]), include_groups=False).rename("prop_personas_rurales").reset_index())
dep = dep.sort_values("tasa_desercion"); tabla(dep.sort_values("tasa_desercion", ascending=False), "12_desercion_departamento.csv")
fig, ax = plt.subplots(figsize=(7, 6.2))
ax.barh(dep["departamento"], dep["tasa_desercion"] * 100, height=0.6,
        color=[NARANJA if p > 0.5 else AZUL for p in dep["prop_personas_rurales"]])
ax.axvline(tasa * 100, color=TINTA, lw=1, ls="--"); ax.text(tasa * 100, -0.9, f" promedio {tasa*100:.1f}%", fontsize=8)
ax.set_xlabel("% sin 2ª dosis"); ax.set_title("Deserción por departamento (naranja: mayoría de vacunados en provincias rurales)", loc="left", fontsize=9)
ax.tick_params(axis="y", labelsize=8)
guardar(fig, "12_desercion_departamento.png")

edad_o = ["0-4", "5-11", "12-17", "18-29", "30-39", "40-49", "50-59", "60-69", "70+"]
ge = por("grupo_etario").set_index("grupo_etario").loc[edad_o].reset_index()
ge_z = cubo.groupby(["grupo_etario", "zona_rural"])[["personas", "desertores"]].sum().reset_index()
ge_z["tasa"] = ge_z["desertores"] / ge_z["personas"]; tabla(ge_z, "13_desercion_grupo_etario_zona.csv")
fig, ax = plt.subplots(figsize=(7.5, 3.8))
for z, c, nm in ((0, AZUL, "Urbana"), (1, NARANJA, "Rural")):
    s = ge_z[ge_z.zona_rural == z].set_index("grupo_etario").loc[edad_o]
    ax.plot(edad_o, s["tasa"] * 100, color=c, lw=2, marker="o", ms=5, label=nm)
ax.set_ylabel("% sin 2ª dosis"); ax.legend(frameon=False); ax.set_xlabel("Grupo etario")
ax.set_title("Deserción por grupo etario y zona", loc="left")
guardar(fig, "13_desercion_edad_zona.png")

for col, f in (("sexo", "14_desercion_sexo.csv"), ("fab_agrupado", "15_desercion_fabricante.csv")):
    tabla(por(col).sort_values("tasa_desercion", ascending=False), f)

tri = por("trimestre_d1").sort_values("trimestre_d1"); tabla(tri, "16_desercion_trimestre.csv")
fig, ax = plt.subplots(figsize=(8, 3.6))
ax.plot(tri["trimestre_d1"], tri["tasa_desercion"] * 100, color=AZUL, lw=2, marker="o", ms=4)
ax.set_ylabel("% sin 2ª dosis"); ax.set_title("Deserción según el trimestre de la 1ª dosis", loc="left")
plt.setp(ax.get_xticklabels(), rotation=60, fontsize=8)
guardar(fig, "16_desercion_trimestre.png")

dist["tasa"] = dist["desertores"] / dist["personas"]
top = dist[dist["personas"] >= 500].sort_values("tasa", ascending=False).head(20)
tabla(top, "17_top20_distritos_desercion.csv")
prov = cubo.groupby(["departamento", "provincia", "zona_rural"])[["personas", "desertores"]].sum().reset_index()
prov["tasa"] = prov["desertores"] / prov["personas"]
tabla(prov.sort_values("tasa", ascending=False), "18_desercion_provincias.csv")
ins["top5_distritos"] = top.head(5)[["departamento", "provincia", "distrito", "personas", "tasa"]].to_dict("records")
ins["top5_prov_rurales"] = prov[(prov.zona_rural == 1) & (prov.personas >= 5000)].sort_values("tasa", ascending=False).head(5)[["departamento", "provincia", "personas", "tasa"]].to_dict("records")

# desbalance
fig, ax = plt.subplots(figsize=(4.6, 3.2))
ax.bar(["Completó 2ª dosis", "Desertó"], [(N - DES) / 1e6, DES / 1e6], color=[AZUL, NARANJA], width=0.5)
for i, (v_, p) in enumerate(((N - DES), 1 - tasa), (DES, tasa)) if False else enumerate(((N - DES, 1 - tasa), (DES, tasa))):
    ax.text(i, v_ / 1e6, f"{p*100:.1f}%", ha="center", va="bottom")
ax.set_ylabel("Millones de personas"); ax.set_title("Balance de clases (variable objetivo)", loc="left", fontsize=10)
guardar(fig, "19_balance_clases.png")

# imputaciones de edad
ins["edad_imputada_personas"] = int(cubo["imputadas"].sum())

# benchmark si existe
b = R / "benchmark_workers.json"
if b.exists():
    bm = pd.DataFrame(json.loads(b.read_text())); bm["speedup"] = bm["map_s"].iloc[0] / bm["map_s"]
    tabla(bm, "20_benchmark_workers.csv")
    fig, ax = plt.subplots(figsize=(5.6, 3.5))
    ax.plot(bm["workers"], bm["speedup"], color=AZUL, lw=2, marker="o", label="medido")
    ax.plot(bm["workers"], bm["workers"], color=GRIS, ls="--", label="ideal")
    ax.set_xlabel("Workers"); ax.set_ylabel("Speedup (fase MAP)"); ax.legend(frameon=False)
    ax.set_title("Escalabilidad del cargador concurrente", loc="left")
    guardar(fig, "20_benchmark.png")
    ins["benchmark"] = bm.to_dict("records")

(R / "insights_datos.json").write_text(json.dumps(ins, indent=2, ensure_ascii=False, default=float), encoding="utf-8")
print(json.dumps(ins, indent=2, ensure_ascii=False, default=float))
