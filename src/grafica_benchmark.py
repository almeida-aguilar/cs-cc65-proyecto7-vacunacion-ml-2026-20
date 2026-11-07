"""Grafica del benchmark de paralelizacion en Go (speedup y tiempo vs workers).
Uso: python grafica_benchmark.py reports/go/benchmark_workers_go.csv reports/figuras/21_benchmark_go.png
"""
import csv, sys
import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt

entrada, salida = sys.argv[1], sys.argv[2]
datos = {"carga": [], "entrenamiento": []}
with open(entrada, encoding="utf-8") as f:
    for r in csv.DictReader(f):
        datos[r["etapa"]].append((int(r["workers"]), float(r["segundos"]), float(r["speedup"])))

AZUL, NARANJA, TEXTO, SUAVE, GRIS = "#2a78d6", "#eb6834", "#0b0b0b", "#52514e", "#d9d8d4"
COLOR = {"carga": AZUL, "entrenamiento": NARANJA}
NOMBRE = {"carga": "Carga del CSV", "entrenamiento": "Entrenamiento"}
plt.rcParams.update({"font.family": "DejaVu Sans", "text.color": TEXTO, "axes.edgecolor": GRIS})

fig, (a1, a2) = plt.subplots(1, 2, figsize=(11, 4.6), facecolor="#fcfcfb")
workers = [w for w, _, _ in datos["carga"]]
for ax in (a1, a2):
    ax.set_facecolor("#fcfcfb")
    ax.grid(axis="y", color=GRIS, linewidth=0.8); ax.set_axisbelow(True)
    for s in ("top", "right"): ax.spines[s].set_visible(False)
    ax.set_xscale("log", base=2); ax.set_xticks(workers); ax.set_xticklabels(workers)
    ax.minorticks_off(); ax.tick_params(colors=SUAVE)
    ax.set_xlabel("Workers (goroutines)", color=SUAVE)

a1.plot(workers, workers, linestyle=(0, (4, 3)), color=SUAVE, linewidth=1.5, label="Ideal (lineal)")
marcas = {"carga": "o", "entrenamiento": "s"}
for etapa, filas in datos.items():
    w = [x[0] for x in filas]; sp = [x[2] for x in filas]
    a1.plot(w, sp, color=COLOR[etapa], linewidth=2, marker=marcas[etapa], markersize=8,
            markeredgecolor="#fcfcfb", markeredgewidth=2, label=NOMBRE[etapa])
    a1.annotate(f"{sp[-1]:.2f}x", (w[-1], sp[-1]), textcoords="offset points", xytext=(8, -3), color=COLOR[etapa], fontsize=10)
    a1.annotate(f"{sp[-2]:.2f}x", (w[-2], sp[-2]), textcoords="offset points", xytext=(-4, 9 if etapa == "carga" else -16), color=COLOR[etapa], fontsize=9, ha="center")
a1.set_yscale("log", base=2); a1.set_yticks([1, 2, 4, 8, 16]); a1.set_yticklabels(["1x", "2x", "4x", "8x", "16x"])
a1.set_ylim(0.9, 18); a1.set_xlim(0.85, 21)
a1.set_title("Speedup", loc="left", fontsize=12, fontweight="bold")
a1.legend(frameon=False, loc="upper left", fontsize=9)

for etapa, filas in datos.items():
    w = [x[0] for x in filas]; t = [x[1] for x in filas]
    a2.plot(w, t, color=COLOR[etapa], linewidth=2, marker=marcas[etapa], markersize=8,
            markeredgecolor="#fcfcfb", markeredgewidth=2, label=NOMBRE[etapa])
    a2.annotate(f"{t[0]:.2f} s", (w[0], t[0]), textcoords="offset points", xytext=(8, 4), color=COLOR[etapa], fontsize=9)
    a2.annotate(f"{t[-1]:.2f} s", (w[-1], t[-1]), textcoords="offset points", xytext=(10, 6 if etapa == "carga" else -13), color=COLOR[etapa], fontsize=9)
a2.set_ylim(0, 1.9); a2.set_xlim(0.85, 24)
a2.set_title("Tiempo (mediana de 3 repeticiones)", loc="left", fontsize=12, fontweight="bold")
a2.set_ylabel("Segundos", color=SUAVE)
a2.legend(frameon=False, loc="upper right", fontsize=9)

fig.suptitle("Paralelización en Go: carga del CSV y entrenamiento (1,2 M filas, 50 variables)", x=0.01, ha="left", fontsize=13, fontweight="bold")
fig.text(0.01, 0.01, "Máquina: 16 CPUs, Windows. Speedup = tiempo con 1 worker / tiempo con N workers.", color=SUAVE, fontsize=8.5)
fig.tight_layout(rect=(0, 0.03, 1, 0.94))
fig.savefig(salida, dpi=160, facecolor=fig.get_facecolor())
print("ok", salida)
