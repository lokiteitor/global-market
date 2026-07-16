# =============================================================================
# Imperio Industrial — Makefile raíz del monorepo
#
# Estructura: backend/ (engine Go, gateway TS, bots TS, migrations, seeds),
#             frontend/ (Nuxt 4), infra/ (docker compose, Caddy), docs/, specs/.
#
# Las migraciones NUNCA se aplican automáticamente: usa `make db-migrate`.
# =============================================================================

SHELL := /bin/bash

COMPOSE      := docker compose -f infra/docker-compose.yml
DB_PORT      ?= 5440
DB_USER      := imperio
DB_NAME      := imperio
DATABASE_URL ?= postgres://imperio:imperio@localhost:$(DB_PORT)/$(DB_NAME)
PSQL         := $(COMPOSE) exec -T db psql -v ON_ERROR_STOP=1 -U $(DB_USER) -d $(DB_NAME)
MIGRATIONS   := backend/migrations
SEEDS        := backend/seeds

GATEWAY_PORT  ?= 8080
FRONTEND_PORT ?= 3000

.DEFAULT_GOAL := help

# ── Ayuda ────────────────────────────────────────────────────────────────────

.PHONY: help
help: ## Muestra esta ayuda
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

# ── Infraestructura ──────────────────────────────────────────────────────────

.PHONY: up
up: ## Levanta la infraestructura (PostgreSQL 18 + Caddy)
	$(COMPOSE) up -d --wait

.PHONY: down
down: ## Para la infraestructura (conserva datos)
	$(COMPOSE) down

.PHONY: destroy
destroy: ## Para la infraestructura y BORRA el volumen de datos
	$(COMPOSE) down -v

.PHONY: logs
logs: ## Sigue los logs de la infraestructura
	$(COMPOSE) logs -f

# ── Stack contenedorizado (perfil "full") ────────────────────────────────────

.PHONY: stack-build
stack-build: ## Construye las imágenes Docker (engine, gateway, bots, frontend)
	$(COMPOSE) --profile full build

.PHONY: stack-up
stack-up: ## Levanta el stack completo en contenedores (migra antes: make db-migrate)
	$(COMPOSE) --profile full up -d
	@echo "stack completo en http://localhost:8000 (edge Caddy)"

.PHONY: stack-down
stack-down: ## Para y elimina solo los contenedores de aplicación (deja db+caddy)
	$(COMPOSE) --profile full rm -sf engine gateway bots frontend

# ── Base de datos (migraciones MANUALES, nunca automáticas) ─────────────────

.PHONY: db-migrate
db-migrate: ## Aplica las migraciones pendientes de backend/migrations (manual)
	@$(PSQL) -q -c "CREATE TABLE IF NOT EXISTS public.schema_migrations (filename text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());"
	@applied=0; \
	for f in $$(ls $(MIGRATIONS)/*.sql | sort); do \
		name=$$(basename $$f); \
		done_already=$$($(PSQL) -tA -c "SELECT 1 FROM public.schema_migrations WHERE filename = '$$name'"); \
		if [ "$$done_already" = "1" ]; then continue; fi; \
		echo "==> aplicando $$name"; \
		$(PSQL) --single-transaction -f - < $$f || exit 1; \
		$(PSQL) -q -c "INSERT INTO public.schema_migrations (filename) VALUES ('$$name')"; \
		applied=$$((applied+1)); \
	done; \
	echo "migraciones aplicadas: $$applied"

.PHONY: db-status
db-status: ## Muestra qué migraciones están aplicadas y cuáles pendientes
	@$(PSQL) -q -c "CREATE TABLE IF NOT EXISTS public.schema_migrations (filename text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());"
	@for f in $$(ls $(MIGRATIONS)/*.sql | sort); do \
		name=$$(basename $$f); \
		done_already=$$($(PSQL) -tA -c "SELECT 1 FROM public.schema_migrations WHERE filename = '$$name'"); \
		if [ "$$done_already" = "1" ]; then echo "  [x] $$name"; else echo "  [ ] $$name"; fi; \
	done

.PHONY: db-seed
db-seed: ## Carga el seed del mundo inicial (idempotente)
	$(PSQL) --single-transaction -f - < $(SEEDS)/seed_world.sql
	@echo "seed aplicado"

.PHONY: db-reset
db-reset: ## DESTRUYE la base de datos y la recrea vacía (sin migrar)
	$(COMPOSE) exec -T db psql -v ON_ERROR_STOP=1 -U $(DB_USER) -d postgres \
		-c "DROP DATABASE IF EXISTS $(DB_NAME) WITH (FORCE);" \
		-c "CREATE DATABASE $(DB_NAME);"
	@echo "base de datos recreada; ejecuta 'make db-migrate' y 'make db-seed'"

.PHONY: db-psql
db-psql: ## Abre un psql interactivo contra la base de datos
	$(COMPOSE) exec db psql -U $(DB_USER) -d $(DB_NAME)

# ── Backend: motor Go ────────────────────────────────────────────────────────

.PHONY: engine-build
engine-build: ## Compila el motor de simulación (Go)
	cd backend/engine && go build ./...

.PHONY: engine-run
engine-run: ## Arranca el motor de simulación
	cd backend/engine && DATABASE_URL="$(DATABASE_URL)" go run ./cmd/engine

.PHONY: engine-test
engine-test: ## Tests del motor
	cd backend/engine && go test ./...

.PHONY: engine-vet
engine-vet: ## go vet del motor
	cd backend/engine && go vet ./...

# ── Backend: gateway TypeScript ──────────────────────────────────────────────

.PHONY: gateway-install
gateway-install: ## Instala dependencias del gateway
	cd backend/gateway && npm install

.PHONY: gateway-build
gateway-build: ## Compila el gateway (tsc estricto)
	cd backend/gateway && npm run build

.PHONY: gateway-run
gateway-run: ## Arranca el gateway (REST + WebSocket) en :$(GATEWAY_PORT)
	cd backend/gateway && DATABASE_URL="$(DATABASE_URL)" PORT=$(GATEWAY_PORT) npm run start

.PHONY: gateway-dev
gateway-dev: ## Gateway en modo desarrollo (recarga)
	cd backend/gateway && DATABASE_URL="$(DATABASE_URL)" PORT=$(GATEWAY_PORT) npm run dev

.PHONY: gateway-test
gateway-test: ## Tests del gateway
	cd backend/gateway && npm test

# ── Backend: bots ────────────────────────────────────────────────────────────

.PHONY: bots-install
bots-install: ## Instala dependencias del orquestador de bots
	cd backend/bots && npm install

.PHONY: bots-run
bots-run: ## Arranca la población de bots contra la API pública
	cd backend/bots && GATEWAY_URL="http://localhost:$(GATEWAY_PORT)" npm run start

# ── Frontend ─────────────────────────────────────────────────────────────────

.PHONY: frontend-install
frontend-install: ## Instala dependencias del frontend
	cd frontend && npm install

.PHONY: frontend-dev
frontend-dev: ## Frontend en modo desarrollo en :$(FRONTEND_PORT)
	cd frontend && NUXT_PUBLIC_WS_URL="ws://localhost:$(GATEWAY_PORT)/ws" npm run dev

.PHONY: frontend-build
frontend-build: ## Build de producción del frontend
	cd frontend && npm run build

# ── Agregados ────────────────────────────────────────────────────────────────

.PHONY: install
install: gateway-install bots-install frontend-install ## Instala todas las dependencias JS

.PHONY: build
build: engine-build gateway-build frontend-build ## Compila todo el monorepo

.PHONY: test
test: engine-test gateway-test ## Ejecuta todos los tests

.PHONY: verify
verify: ## Smoke test end-to-end (requiere stack levantado y migrado)
	bash infra/verify.sh
