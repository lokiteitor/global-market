# Motor de simulación (Go) — build multi-stage, binario estático sin CGO.
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /engine ./cmd/engine

FROM alpine:3.20
RUN adduser -D -u 10001 imperio
USER imperio
COPY --from=build /engine /usr/local/bin/engine
# DATABASE_URL (obligatoria) y TIME_RATIO (opcional, solo dev) van por entorno.
ENTRYPOINT ["engine"]
