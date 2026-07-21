# ══════════════════════════════════════════════════════════════════════════
# Imperio Industrial — Backend (gateway | engine | stress), multi-stage
#
# Contexto de build: /backend (ver infra/docker-compose.yml).
#   docker build -f infra/docker/backend.Dockerfile --build-arg CMD=gateway backend/
#
# ARG CMD  → binario a compilar: gateway | engine | stress (cmd/<CMD>)
# ARG PORT → puerto expuesto (8080 gateway, 8081 engine, 8083 stress)
#
# GO_IMAGE debe cubrir la directiva `go` de backend/go.mod: la imagen oficial
# fija GOTOOLCHAIN=local, así que un tag por debajo NO descarga toolchain y el
# build falla con "go.mod requires go >= X".
# ══════════════════════════════════════════════════════════════════════════
ARG GO_IMAGE=golang:1.25

FROM ${GO_IMAGE} AS builder
ARG CMD=gateway
WORKDIR /src

# Cachear descarga de módulos por separado del código
COPY go.mod go.su[m] ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/app ./cmd/${CMD}

# Runtime mínimo, sin shell, usuario no root
FROM gcr.io/distroless/static-debian12:nonroot
ARG PORT=8080
COPY --from=builder /out/app /app
EXPOSE ${PORT}
ENTRYPOINT ["/app"]
