# ══════════════════════════════════════════════════════════════════════════
# Imperio Industrial — Backend (gateway | engine), multi-stage
#
# Contexto de build: /backend (ver infra/docker-compose.yml).
#   docker build -f infra/docker/backend.Dockerfile --build-arg CMD=gateway backend/
#
# ARG CMD  → binario a compilar: gateway | engine (cmd/<CMD>)
# ARG PORT → puerto expuesto (8080 gateway, 8081 engine)
# ══════════════════════════════════════════════════════════════════════════
ARG GO_IMAGE=golang:1.24

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
