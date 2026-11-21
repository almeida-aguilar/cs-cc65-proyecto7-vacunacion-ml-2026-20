"""Genera los datos pequeños de prueba del PC4 en data/prueba/.

Si existe data/modelo/train_raw.csv toma sus primeras N filas; si no, genera
datos sintéticos con la misma forma (columnas numéricas y `deserto` 0/1 al
final, ~8 % de positivos, semilla 42) y lo avisa. Las filas se reparten en
`nodo1.csv`, `nodo2.csv`, ... con la misma cabecera, para levantar los nodos ML
sin copiar el dataset completo. También escribe `predicciones.json` con 30
filas de ejemplo (sin la etiqueta) para medir la latencia de /predict.

Ejecutar siempre desde la raíz del repo. La carpeta data/ no se versiona.
"""
import argparse
import csv
import json
import math
import random
import sys
from pathlib import Path

SEMILLA = 42
FRACCION_POSITIVOS = 0.08
FILAS_PREDICCION = 30
COLUMNAS_SINTETICAS = [
    "edad", "dias_desde_inicio", "te_provincia", "te_distrito", "edad_imputada",
    "sexo_masculino", "zona_rural", "pct_rural_provincia", "mes_sin", "mes_cos",
]
ETIQUETA = "deserto"


def leer_reales(ruta: Path, filas: int):
    """Devuelve (cabecera, filas) con las primeras `filas` filas del CSV real."""
    with ruta.open(newline="", encoding="utf-8-sig") as archivo:
        lector = csv.reader(archivo)
        cabecera = next(lector)
        datos = []
        for fila in lector:
            if len(datos) >= filas:
                break
            datos.append(fila)
    return cabecera, datos


def generar_sinteticos(filas: int):
    """Datos sintéticos con la forma del dataset real (etiqueta al final).

    Usa solo la biblioteca estándar para no exigir pandas/numpy.
    """
    azar = random.Random(SEMILLA)
    registros = []
    for _ in range(filas):
        edad = azar.randint(5, 89)
        dias = azar.randint(0, 1399)
        te_provincia = min(max(azar.gauss(0.12, 0.04), 0.0), 1.0)
        te_distrito = min(max(azar.gauss(0.12, 0.05), 0.0), 1.0)
        edad_imputada = int(azar.random() < 0.05)
        sexo = azar.randint(0, 1)
        rural = int(azar.random() < 0.2)
        pct_rural = azar.random()
        mes = azar.randint(0, 11)
        # La deserción depende débilmente de algunas variables; el umbral se
        # recalibra abajo para que la tasa media sea FRACCION_POSITIVOS.
        puntaje = 8 * (te_provincia - 0.12) + 0.4 * rural - 0.004 * (edad - 40) + azar.gauss(0, 0.5)
        registros.append((puntaje, [
            edad, dias, round(te_provincia, 7), round(te_distrito, 7), edad_imputada, sexo, rural,
            round(pct_rural, 7), round(math.sin(2 * math.pi * mes / 12), 7),
            round(math.cos(2 * math.pi * mes / 12), 7),
        ]))
    puntajes = sorted(puntaje for puntaje, _ in registros)
    umbral = puntajes[int(len(puntajes) * (1 - FRACCION_POSITIVOS))]
    texto = [[f"{valor:g}" for valor in variables] + [str(int(puntaje >= umbral))]
             for puntaje, variables in registros]
    return COLUMNAS_SINTETICAS + [ETIQUETA], texto


def escribir_fragmentos(cabecera, filas, carpeta: Path, fragmentos: int):
    """Reparte las filas en bloques contiguos casi iguales."""
    rutas = []
    base, resto = divmod(len(filas), fragmentos)
    inicio = 0
    for indice in range(fragmentos):
        fin = inicio + base + (1 if indice < resto else 0)
        ruta = carpeta / f"nodo{indice + 1}.csv"
        with ruta.open("w", newline="", encoding="utf-8") as archivo:
            escritor = csv.writer(archivo, lineterminator="\n")
            escritor.writerow(cabecera)
            escritor.writerows(filas[inicio:fin])
        rutas.append((ruta, fin - inicio))
        inicio = fin
    return rutas


def escribir_predicciones(cabecera, filas, ruta: Path):
    """Guarda filas de ejemplo sin la etiqueta para probar /predict."""
    variables = cabecera[:-1]
    ejemplos = [[float(valor) for valor in fila[:-1]] for fila in filas[:FILAS_PREDICCION]]
    contenido = {"columnas": variables, "filas": ejemplos}
    ruta.write_text(json.dumps(contenido, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--filas", type=int, default=20000, help="filas a tomar o generar (defecto 20000)")
    parser.add_argument("--fragmentos", type=int, default=2, help="número de archivos nodoK.csv (defecto 2)")
    parser.add_argument("--entrada", default="data/modelo/train_raw.csv", help="CSV real de entrenamiento")
    parser.add_argument("--salida", default="data/prueba", help="carpeta de salida")
    args = parser.parse_args()

    if args.filas < FILAS_PREDICCION or args.fragmentos < 1 or args.filas < args.fragmentos:
        print(f"Error: --filas debe ser al menos {FILAS_PREDICCION} y no menor que --fragmentos (>= 1).")
        return 1

    entrada = Path(args.entrada)
    if entrada.exists():
        cabecera, filas = leer_reales(entrada, args.filas)
        print(f"Datos reales: {len(filas)} filas de {entrada}")
    else:
        print(f"AVISO: no existe {entrada}; se generan datos SINTÉTICOS (semilla {SEMILLA}, "
              f"~{FRACCION_POSITIVOS:.0%} de positivos) con la misma forma.")
        cabecera, filas = generar_sinteticos(args.filas)
    if len(filas) < args.fragmentos:
        print("Error: el CSV de entrada tiene menos filas que fragmentos.")
        return 1

    carpeta = Path(args.salida)
    carpeta.mkdir(parents=True, exist_ok=True)
    for ruta, cantidad in escribir_fragmentos(cabecera, filas, carpeta, args.fragmentos):
        print(f"  {ruta}: {cantidad} filas")
    escribir_predicciones(cabecera, filas, carpeta / "predicciones.json")
    positivos = sum(1 for fila in filas if float(fila[-1]) == 1.0)
    print(f"  {carpeta / 'predicciones.json'}: {FILAS_PREDICCION} filas de ejemplo ({len(cabecera) - 1} variables)")
    print(f"Positivos (deserto=1): {positivos / len(filas):.2%}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
