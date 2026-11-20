# Imagen única del clúster ML (PC4). Cada servicio cambia solo el `command`.
# Go 1.22 coincide con go.mod, CI y mise: no subir la versión aquí.

# --- Etapa 1: compilación ---------------------------------------------------
FROM golang:1.22-alpine AS compilador
WORKDIR /src

# go.mod y go.sum primero: la descarga de módulos queda en caché de capas.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Compila todos los programas de ./cmd que existan (nodo, coordinador, entrenar,
# benchmark, seed y, cuando exista, api). Si un cmd falta, simplemente no se compila.
RUN set -eu; mkdir -p /app/bin; \
    for dir in cmd/*/; do \
      nombre=$(basename "$dir"); \
      if ls "$dir"*.go >/dev/null 2>&1; then \
        echo "compilando $nombre"; \
        CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "/app/bin/$nombre" "./cmd/$nombre"; \
      fi; \
    done

# --- Etapa 2: imagen final mínima -------------------------------------------
FROM alpine:3.20
# netcat-openbsd: lo usan los healthchecks (nc -z localhost 9001).
RUN apk add --no-cache netcat-openbsd \
    && adduser -D -u 10001 ml \
    && mkdir -p /app/reports/pc4 /app/reports/go \
    && chown -R ml:ml /app
WORKDIR /app
COPY --from=compilador /app/bin /app/bin
USER ml
EXPOSE 9001 8080
CMD ["/app/bin/nodo"]
