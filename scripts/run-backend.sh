#!/usr/bin/env bash
# Ejecuta gateway + engine en local con cierre limpio de ambos.
# Invocado desde el Makefile (make backend). Requiere la BD levantada (make infra-core).
set -euo pipefail

cd "$(dirname "$0")/../backend"

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
