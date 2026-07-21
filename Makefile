# ══════════════════════════════════════════════════════════════════════════
# Imperio Industrial — Makefile: ÚNICO punto de entrada de tareas (ADR-016)
# ══════════════════════════════════════════════════════════════════════════
SHELL := /bin/bash
.DEFAULT_GOAL := help

COMPOSE ?= docker compose -f infra/docker-compose.yml
GO      ?= go
NPM     ?= npm

# 12-factor: configuración por entorno; valores por defecto = entorno local dev
export II_DATABASE_URL ?= postgres://imperio:imperio@localhost:5432/imperio?sslmode=disable

# ─── Meta ────────────────────────────────────────────────────────────────
.PHONY: help
help: ## Lista las tareas disponibles
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

# ─── Ciclo de vida global ────────────────────────────────────────────────
.PHONY: build test lint fmt generate clean
build: backend-build frontend-build ## Compila backend y frontend
test: backend-test frontend-test ## Ejecuta todos los tests
lint: backend-lint frontend-lint contract-lint ## Linters de todo el monorepo
fmt: backend-fmt frontend-fmt ## Formatea todo el código
generate: backend-generate frontend-generate ## Codegen: sqlc (queries) + tipos del contrato
clean: ## Elimina artefactos de build
	rm -rf backend/bin frontend/.output frontend/.nuxt

# ─── Ejecución ───────────────────────────────────────────────────────────
.PHONY: dev run backend bots frontend
dev: infra-core migrate-up seed ## Prepara el entorno local: BD+observabilidad, esquema y seed
	@echo ""
	@echo "Entorno listo. En terminales separados:"
	@echo "  make backend    # gateway + engine (go run)"
	@echo "  make frontend   # cliente Nuxt en modo dev"
run: ## Levanta el stack completo en Docker (perfil full)
	$(COMPOSE) --profile full up -d --build
backend: ## Ejecuta gateway + engine en local
	./scripts/run-backend.sh
bots: ## Ejecuta el Bot Orchestration Service en local
	cd backend && $(GO) run ./cmd/bots
frontend: ## Ejecuta el frontend en modo dev
	cd frontend && $(NPM) run dev

# ─── Backend (Go) ────────────────────────────────────────────────────────
.PHONY: backend-build backend-test backend-lint backend-fmt backend-generate
backend-build: ## Compila todos los paquetes y binarios del backend
	cd backend && $(GO) build ./...
	cd backend && mkdir -p bin && $(GO) build -o bin/ ./cmd/...
backend-test: ## Tests del backend (II_TEST_DATABASE_URL habilita los de integración: usan una BD efímera propia en ese servidor, requiere CREATEDB)
	cd backend && $(GO) test ./...
backend-lint: ## gofmt (check) + go vet
	@cd backend && out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt pendiente en:"; echo "$$out"; exit 1; fi
	cd backend && $(GO) vet ./...
backend-fmt:
	cd backend && gofmt -w .
backend-generate: ## sqlc: código Go tipado desde queries SQL (nunca esquema, ADR-020)
	cd backend && $(GO) generate ./...

# ─── Frontend (Nuxt) ─────────────────────────────────────────────────────
.PHONY: frontend-install frontend-build frontend-test frontend-lint frontend-fmt frontend-generate
frontend-install:
	cd frontend && $(NPM) install
frontend-build: frontend-install ## Build de producción del cliente
	cd frontend && $(NPM) run build
frontend-test: frontend-install
	cd frontend && $(NPM) run test
frontend-lint: frontend-install
	cd frontend && $(NPM) run lint
frontend-fmt: frontend-install
	cd frontend && $(NPM) run fmt
frontend-generate: frontend-install ## Tipos TS generados desde docs/api/openapi.yaml (ADR-021)
	cd frontend && $(NPM) run gen:api

# ─── Contrato (API First) ────────────────────────────────────────────────
.PHONY: contract-lint
contract-lint: ## Linter del contrato OpenAPI
	cd tools/openapi && $(NPM) install --silent && $(NPM) run lint

# ─── Infraestructura ─────────────────────────────────────────────────────
.PHONY: infra infra-core infra-down infra-logs
infra: infra-core ## Alias de infra-core
infra-core: ## PostgreSQL 18 + Prometheus + Grafana en Docker
	$(COMPOSE) --profile core --profile obs up -d
infra-down: ## Detiene todos los contenedores del proyecto
	$(COMPOSE) --profile full --profile core --profile obs down
infra-logs:
	$(COMPOSE) logs -f

# ─── Base de datos (ADR-020: migraciones manuales, runner propio) ────────
.PHONY: migrate-up migrate-down migrate-status migrate-create reset-db seed
migrate-up: ## Aplica las migraciones pendientes
	cd backend && $(GO) run ./cmd/migrate up
migrate-down: ## Revierte la última migración (o n=N para varias)
	cd backend && $(GO) run ./cmd/migrate down $(if $(n),$(n),1)
migrate-status: ## Estado y verificación de checksums de migraciones
	cd backend && $(GO) run ./cmd/migrate status
migrate-create: ## Crea una migración vacía: make migrate-create name=mi_cambio
	cd backend && $(GO) run ./cmd/migrate create $(name)
reset-db: ## Destruye los esquemas de dominio y reaplica todo (solo dev)
	cd backend && $(GO) run ./cmd/migrate reset
seed: ## Datos mínimos de desarrollo (cuentas de sistema, demo, reloj del mundo)
	cd backend && $(GO) run ./cmd/seed
