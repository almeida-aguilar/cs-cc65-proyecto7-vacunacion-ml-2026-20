# Protocolo TCP del clúster ML (PC4)

El protocolo ya está implementado en la dependencia
`github.com/franztoledo/logreg` v0.1.0 (`protocolo.go`, `coordinador.go` y
`nodo.go`). El proyecto no lo reimplementa: lo envuelve en
`internal/coordinador` y lo usan el `cmd/coordinador` y, más adelante, la API.

## Transporte

- TCP, un mensaje JSON por línea (`json.Encoder` / `json.Decoder`).
- Tiempo máximo por petición configurable (`-tiempo-espera`, por defecto 30 s).
- Mensajes: `INFO`, `CONFIGURAR`, `GRADIENTE`, `EVALUAR`, `FIN`, `OK`, `ERROR`.

## Secuencia

```mermaid
sequenceDiagram
    participant C as Coordinador (API)
    participant N1 as Nodo ML 1
    participant N2 as Nodo ML 2

    C->>N1: INFO
    C->>N2: INFO
    N1-->>C: OK {filas, variables, estadísticas}
    N2-->>C: OK {filas, variables, estadísticas}
    Note over C: Une estadísticas y calcula el escalador global
    C->>N1: CONFIGURAR {escalador, semilla}
    C->>N2: CONFIGURAR {escalador, semilla}
    N1-->>C: OK
    N2-->>C: OK
    loop cada paso de época
        C->>N1: GRADIENTE {pesos, lote}
        C->>N2: GRADIENTE {pesos, lote}
        N1-->>C: OK {gradiente local}
        N2-->>C: OK {gradiente local}
        Note over C: Suma gradientes y actualiza pesos (parameter server)
    end
    C->>N1: EVALUAR {pesos, umbral}
    C->>N2: EVALUAR {pesos, umbral}
    N1-->>C: OK {matriz, pérdida}
    N2-->>C: OK {matriz, pérdida}
    C->>N1: FIN
    C->>N2: FIN
```

## Concurrencia y seguridad

- `Coordinador.difundir` lanza **una goroutine por nodo** y espera todas las
  respuestas (barrera). Cada goroutine usa únicamente la conexión de su nodo.
- El estado compartido (conexiones, nodos activos y pesos) se protege con un
  `sync.Mutex`; los resultados vuelven por un canal, sin escritura compartida.
- Si un nodo falla, se marca inactivo y el entrenamiento continúa con los
  restantes, siempre que quede al menos `MinimoNodos`.
- Las pruebas `internal/coordinador/coordinador_test.go` levantan 2 nodos TCP
  reales y verifican equivalencia con el entrenamiento local bajo `-race`.

## Comando de verificación

```bash
mise run check   # o: go vet ./... && go test -race ./...
```
