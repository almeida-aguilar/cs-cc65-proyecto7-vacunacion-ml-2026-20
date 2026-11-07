import pandas as pd
import numpy as np
import os
import time

# Configuración
ARCHIVO_ENTRADA = "vacunas_covid.csv"
ARCHIVO_SALIDA = "vacunas_covid_muestra_1.5M.csv"
DELIMITADOR = ";"
TAMANO_MUESTRA = 1_500_000
CHUNK_SIZE = 200_000  # Lectura eficiente en bloques de 200k filas

def generar_muestra_limpia():
    if not os.path.exists(ARCHIVO_ENTRADA):
        print(f"❌ Error: No se encontró el archivo '{ARCHIVO_ENTRADA}'.")
        return

    print("==================================================")
    print("      GENERACIÓN DE MUESTRA LIMPIA (1.5M)        ")
    print("==================================================")
    print(f"📁 Archivo origen: {ARCHIVO_ENTRADA}")
    print(f"🎯 Muestra objetivo: {TAMANO_MUESTRA:,} filas completas")
    print("==================================================\n")

    tiempo_inicio = time.time()
    
    # Lista para almacenar DataFrames limpios procesados por chunks
    chunks_limpios = []
    total_filas_limpias = 0

    print("⏳ Leyendo dataset por bloques y filtrando nulos...")

    # Reader por chunks
    lectores = pd.read_csv(
        ARCHIVO_ENTRADA,
        sep=DELIMITADOR,
        chunksize=CHUNK_SIZE,
        on_bad_lines='skip',
        encoding='utf-8',
        encoding_errors='ignore',
        dtype=str
    )

    for i, chunk in enumerate(lectores):
        # 1. Eliminar filas que contengan NULOS en cualquier columna (dropna)
        chunk_clean = chunk.dropna()

        if not chunk_clean.empty:
            chunks_limpios.append(chunk_clean)
            total_filas_limpias += len(chunk_clean)

        # Si ya juntamos más de 3M de filas limpias, podemos detener la lectura para ahorrar tiempo
        if total_filas_limpias >= 3_000_000:
            print(f"  ➜ Se han reunido {total_filas_limpias:,} filas limpias. Deteniendo lectura temprana para optimizar tiempo.")
            break

        if (i + 1) % 5 == 0:
            print(f"  ➜ Procesados {(i + 1) * CHUNK_SIZE:,} registros origen... ({total_filas_limpias:,} filas limpias acumuladas)")

    print("\n📦 Concatenando bloques limpios...")
    df_acumulado_limpio = pd.concat(chunks_limpios, ignore_index=True)
    
    filas_disponibles = len(df_acumulado_limpio)
    print(f"✅ Total de filas 100% sin nulos obtenidas: {filas_disponibles:,}")

    # 2. Extraer la muestra aleatoria de 1.5M
    if filas_disponibles < TAMANO_MUESTRA:
        print(f"⚠️ Atención: Se tomarán las {filas_disponibles:,} filas disponibles ya que son menores a 1.5M.")
        df_muestra = df_acumulado_limpio
    else:
        print(f"🎲 Seleccionando muestra aleatoria (Random Sample) de {TAMANO_MUESTRA:,} registros...")
        # random_state=42 para reproducibilidad de los datos en su equipo
        df_muestra = df_acumulado_limpio.sample(n=TAMANO_MUESTRA, random_state=42)

    # 3. Exportar a CSV
    print(f"💾 Guardando archivo final: '{ARCHIVO_SALIDA}'...")
    df_muestra.to_csv(ARCHIVO_SALIDA, sep=";", index=False, encoding='utf-8')

    tiempo_total = time.time() - tiempo_inicio

    print("\n==================================================")
    print("              PROCESO FINALIZADO                  ")
    print("==================================================")
    print(f"⏱️ Tiempo transcurrido: {tiempo_total:.2f} segundos")
    print(f"📄 Archivo generado: {ARCHIVO_SALIDA}")
    print(f"📊 Dimensiones del dataset resultante: {df_muestra.shape}")
    print("❓ Verificación de nulos en el nuevo CSV:")
    print(df_muestra.isnull().sum())
    print("==================================================")

if __name__ == "__main__":
    generar_muestra_limpia()