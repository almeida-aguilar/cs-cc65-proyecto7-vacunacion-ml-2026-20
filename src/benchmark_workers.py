"""Mide la fase MAP con 1..N workers sobre los primeros MB del archivo (evidencia de concurrencia)."""
import json
import shutil
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent))
from loader_concurrente import ejecutar

if __name__ == "__main__":
    entrada = Path("data/vacunas_covid.csv"); tmp = Path("data/bench")
    res = []
    for w in (1, 2, 4, 6, 8, 12):
        m = ejecutar(entrada, tmp, w, 32, 600, solo_map=True)
        res.append({"workers": w, "map_s": m["tiempos"]["map_s"], "filas": m["filas_parseadas"]})
        print(res[-1], flush=True)
    shutil.rmtree(tmp, ignore_errors=True)
    Path("reports/benchmark_workers.json").write_text(json.dumps(res, indent=2))
