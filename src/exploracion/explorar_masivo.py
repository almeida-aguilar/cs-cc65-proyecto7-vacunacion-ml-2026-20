import pandas as pd
from concurrent.futures import ProcessPoolExecutor, as_completed
import os
import time

NOMBRE_ARCHIVO = "vacunas_covid.csv"  # Tu archivo de datos
DELIMITADOR = ";"                     # Separador confirmado: punto y coma
CHUNK_SIZE = 100_000                  # Bloques de 100k filas
MAX_WORKERS = os.cpu_count()          # Usa los 16 núcleos de tu CPU

def procesar_bloque(bloque_data):
    """Procesa cada chunk calculando filas y nulos"""
    index_bloque, chunk = bloque_data
    filas = len(chunk)
    nulos = chunk.isnull().sum()
    return filas, nulos

def explorar_csv_concurrente(filepath):
    if not os.path.exists(filepath):
        print(f"Error: No se encontró el archivo '{filepath}'.")
        return

    print("==================================================")
    print(f"Iniciando exploración concurrente de: {filepath}")
    print(f"Núcleos CPU detectados (Workers): {MAX_WORKERS}")
    print(f"Delimitador configurado: '{DELIMITADOR}'")
    print(f"Tamaño de bloque: {CHUNK_SIZE:,} filas")
    print("==================================================\n")

    tiempo_inicio = time.time()

    # 1. Inspección del encabezado
    primer_bloque = pd.read_csv(
        filepath, 
        nrows=5, 
        sep=DELIMITADOR,
        encoding='utf-8',
        encoding_errors='ignore',
        on_bad_lines='skip'
    )
    columnas = primer_bloque.columns.tolist()
    total_columnas = len(columnas)

    print(f"📋 Total de Columnas: {total_columnas}")
    print("📌 Nombres de las columnas:")
    for idx, col in enumerate(columnas, 1):
        print(f"  {idx}. {col}")
    print("\n--- Procesando filas y nulos en paralelo (Cuidando RAM) ---\n")

    total_filas = 0
    conteo_nulos_global = pd.Series(0, index=columnas, dtype='int64')

    # 2. Lectura masiva por Chunks con sep=';' explícito
    lectores_chunks = pd.read_csv(
        filepath, 
        sep=DELIMITADOR,            # <-- AQUÍ ESTABA EL DETALLE: sep=';' para el iterador
        chunksize=CHUNK_SIZE, 
        on_bad_lines='skip',       # Omite filas con errores de formato raros
        encoding='utf-8',
        encoding_errors='ignore',
        dtype=str                  # Procesa rápido como texto
    )

    with ProcessPoolExecutor(max_workers=MAX_WORKERS) as executor:
        futures = {
            executor.submit(procesar_bloque, (i, chunk)): i 
            for i, chunk in enumerate(lectores_chunks)
        }

        for future in as_completed(futures):
            filas_chunk, nulos_chunk = future.result()
            total_filas += filas_chunk
            conteo_nulos_global = conteo_nulos_global.add(nulos_chunk, fill_value=0)
            
            if total_filas % 1_000_000 < CHUNK_SIZE:
                print(f"  ➜ Procesados: {total_filas:,} registros...")

    tiempo_total = time.time() - tiempo_inicio

    # 3. Reporte Final
    print("\n==================================================")
    print("               RESUMEN DE EXPLORACIÓN             ")
    print("==================================================")
    print(f"⏱️ Tiempo total de procesamiento: {tiempo_total:.2f} segundos")
    print(f"📊 Total de registros válidos (Filas): {total_filas:,}")
    print(f"📐 Dimensiones totales: ({total_filas:,} filas, {total_columnas} columnas)")
    print("\n❓ Conteo de Valores Nulos/Faltantes por Columna:")
    
    df_nulos = pd.DataFrame({
        'Columna': conteo_nulos_global.index,
        'Nulos': conteo_nulos_global.values,
        'Porcentaje_Nulos (%)': ((conteo_nulos_global.values / total_filas) * 100).round(2)
    })
    
    print(df_nulos.to_string(index=False))
    print("\n✅ Exploración completada con éxito sin agotar la RAM.")

if __name__ == "__main__":
    explorar_csv_concurrente(NOMBRE_ARCHIVO)