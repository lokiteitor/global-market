# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Qué es esto

**Imperio Industrial**: MMO de simulación económica sobre un mundo único persistente. Monorepo
**sin workspaces** — el `Makefile` raíz es la única interfaz de comandos (`make help` lista todo).
El proyecto es *documentación-primero*: antes de tocar código lee `docs/desarrollo.md`
(decisiones de implementación ADR-IMPL-01..14 y estado real); el diseño de juego normativo es
`docs/gdd.md`. Ante discrepancia de diseño prevalece el GDD; sobre cómo está construido,
prevalecen `docs/desarrollo.md` y `specs/`.

**Regla de docs vivos**: cualquier cambio de comportamiento, esquema o contrato debe actualizar
en el mismo cambio `specs/` (openapi.yaml, ws-protocol.md, schemas/) y los docs afectados.
Las decisiones estructurales nuevas se registran como ADR-IMPL en `docs/desarrollo.md`.

## Comandos

```bash
make up                  # PostgreSQL 18 + Caddy (edge en :8000)
make db-migrate          # migraciones — SIEMPRE manuales, nunca automáticas
make db-seed             # mundo inicial (idempotente); db-reset recrea la BD vacía
make engine-run          # motor Go        (gateway-dev / frontend-dev / bots-run igual)
make build               # compila todo    (engine-build + gateway-build + frontend-build)
make test                # engine-test + gateway-test
make verify              # smoke e2e completo (infra/verify.sh, ~5 min, ciclo CCRI real)
make stack-build && make stack-up   # todo contenedorizado (perfil "full" del compose)
```

Tests individuales:

```bash
cd backend/engine  && go test ./internal/logistics/           # un paquete Go
cd backend/gateway && node --import tsx --test test/money.test.ts
cd backend/bots    && node --import tsx --test test/producer.test.ts
cd frontend        && npx vitest run tests/net/simclock.spec.ts
cd frontend        && npm run typecheck                        # vue-tsc estricto
```

Base de datos directa: `make db-psql` (o `docker compose -f infra/docker-compose.yml exec -T db psql -U imperio -d imperio`).
Puertos: edge `:8000` (Caddy, entrada canónica), gateway `:8080`, frontend `:3000`, PostgreSQL `localhost:5440`.
Credenciales del seed: `Aurora Corp`/`aurora` y bots (`Bot Minero Norte`/`botmineronorte`, etc.).

## Arquitectura (lo que hay que saber antes de tocar nada)

**Reparto de procesos (ADR-IMPL-03).** No hay API interna entre servicios; se coordinan por
PostgreSQL:
- `backend/gateway` (Fastify + pg) ejecuta el **camino de comando** (publicar, aceptar, construir,
  comprar) en transacciones `SERIALIZABLE` con retry en 40001. Implementa TODO
  `specs/openapi.yaml` (v1.1.0) y el WebSocket de `specs/ws-protocol.md`.
- `backend/engine` (Go + pgx) ejecuta lo **dirigido por tiempo**: avance de `world.sim_clock`
  (ratio 24×, 1 s wall = 24 s sim; `TIME_RATIO` solo para tests), fin de lotes, tránsito y
  auto-despacho, cierre de ventanas de sorteo, liquidaciones, balancer de ciudades. Scheduler
  dirigido por la base (consultas indexadas por vencimiento), no cola en memoria.
- `backend/bots` habla SOLO con la API pública HTTP (igualdad de API literal — sin atajos).

**El ledger es la fuente de verdad del valor y sus invariantes viven en SQL** (migración
`0004_ledger.sql`): doble entrada balanceada **por activo** (constraint trigger diferido al
COMMIT), no-negatividad salvo cuentas `emission`, partidas append-only inmutables, y funciones
todo-o-nada `ledger.confirm_contract` / `ledger.settle_contract_prorata`. El código de aplicación
orquesta; la base garantiza. Dinero/stock son `BIGINT` en BD y **strings decimales en JSON** —
jamás floats. La emisión de stock por producto (cuentas `emission` con `product_id`) es la
contrapartida de producción/consumo (ADR-IMPL-12).

**Migraciones**: `backend/migrations/` es el DDL canónico (PostgreSQL 18, `uuid DEFAULT uuidv7()`
nativo, sin ULID). `specs/schemas/*.sql` es su espejo de especificación y **debe mantenerse
sincronizado 1:1** al cambiar una migración. Nunca añadas auto-migración al arranque de servicios.

**Semántica CCRI v1** (afecta a gateway Y engine): publicación `sell` aceptada → contrato con
`destination = origin` que se liquida al confirmarse (retirada in situ); publicación `buy`
aceptada → el vendedor entrega físicamente y el motor auto-despacha sus camiones `idle` en el
nodo de origen (ADR-IMPL-13). Toda garantía queda bloqueada en cuentas espejo (con
`reference_id`) desde la publicación/aceptación; la ventana de sorteo (45 s wall) y el cooldown
anti-parpadeo (30 s) son las únicas mecánicas en tiempo real — todo otro plazo va en sim-time.

**Contrato engine↔gateway vía outbox**: el motor (y el gateway en sus comandos) insertan eventos
en `outbox.events` en la misma transacción que el cambio de estado, con
`payload.entity` = el DTO REST exacto del openapi (+ `location` en eventos espaciales). El
gateway los traduce a frames WS (rooms `corp:`/`viewport:`/`alerts:`) haciendo polling por cursor
(`consumer_name = 'notification_gateway'`) **sin releer la BD**. Si cambias un DTO, cambia las
dos puntas y el spec.

**Frontend** (`frontend/`, Nuxt 4 + Pinia + Phaser): thin client radical — no calcula reglas
económicas; el estado replicado solo se escribe vía acciones `apply*` (snapshot reemplaza
subárbol, patches idempotentes). Tipos branded en `app/lib/kernel/` (`Money`, `SimTime`,
`Id<T>`): prohibido `Number()`/`parseFloat` sobre importes y toda conversión de tiempo pasa por
el `SimClock`. Phaser vive tras `app/game/bridge.ts` y no importa stores ni componentes Vue;
`/play` es client-only. En dev directo a `:3000` el WS necesita `NUXT_PUBLIC_WS_URL`
(el devProxy de Nitro no hace upgrade) — `make frontend-dev` ya lo inyecta; vía el edge `:8000`
funciona same-origin.

**Al terminar cambios sustanciales**: `make verify` es el juez — resetea, migra, siembra,
levanta el stack y ejercita el ciclo CCRI completo con asserts contables exactos.
