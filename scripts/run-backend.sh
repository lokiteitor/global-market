#!/usr/bin/env bash
# Ejecuta gateway + engine en local con cierre limpio de ambos.
# Invocado desde el Makefile (make backend). Requiere la BD levantada (make infra-core).
set -euo pipefail

cd "$(dirname "$0")/../backend"

# En dev el frontend (:3000) conecta el WS DIRECTO al gateway (:8080) porque
# el devProxy de Nitro no proxya upgrades WebSocket; hay que permitir ese
# origen cross de navegador (default: solo mismo origen). Ver ADR-023 y
# frontend/nuxt.config.ts ($development.runtimeConfig.public.wsBase).
export II_WS_ALLOWED_ORIGINS="${II_WS_ALLOWED_ORIGINS:-localhost:3000}"

cleanup() {
  trap - TERM INT
  kill 0 2>/dev/null || true
  wait
}
trap cleanup TERM INT

go run ./cmd/engine &
go run ./cmd/gateway &

wait -n
cleanup
