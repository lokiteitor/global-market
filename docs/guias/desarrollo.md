# Guía de desarrollo — Imperio Industrial

Audiencia: cualquier desarrollador que se incorpora al proyecto. Complementa (no sustituye) al GDD v1.3, SAD v1.1 y FAD v1.1.

## 1. Principios operativos

- **El Makefile es el único punto de entrada** (ADR-016). Si ejecutas un comando a mano más de una vez, pertenece al Makefile.
- **Contract First**: `docs/api/openapi.yaml` es la frontera dura entre backend y frontend. Cambiarlo exige: bump de versión, `make generate` en ambos lados y `make contract-lint` verde. El frontend jamás escribe DTOs a mano.
- **Toda decisión estructural pasa por un ADR** en `docs/adr/` ANTES de implementarse. La documentación afectada se actualiza en el mismo cambio.
- **Flujo por funcionalidad**: analizar requisitos → identificar impactos → actualizar docs → diseñar → definir contratos → implementar → tests → validar → sincronizar docs.

## 2. Backend (Go)

- Módulo único `github.com/lokiteitor/global-market/backend`; bounded contexts en `internal/` (`auth`, `ledger`, `sim`, y próximamente `contracts`, `logistics`, `balancer`, `outbox`) **sin imports cruzados entre sí**: solo los composition roots de `cmd/*` componen módulos, y las dependencias cruzadas se expresan como interfaces definidas por el consumidor (p. ej. `ledger.Identity` la implementa el main del gateway con `auth.FromContext`).
- `internal/platform/` (config, logging slog JSON, métricas Prometheus, httpx con envelopes del contrato, pgxpool) y `internal/sim/simtime` son plataforma compartida importable por todos.
- **Dinero y stock**: `int64` (punto fijo) en Go, `BIGINT` en SQL, **string** en JSON. Prohibido float en cualquier magnitud económica.
- **Invariantes del ledger viven en SQL** (regla de oro, ADR-005): triggers de saldo, doble entrada diferida por activo, inmutabilidad append-only y funciones todo-o-nada. El código Go orquesta; la base garantiza.
- **sqlc solo genera código de queries** (`make backend-generate`), nunca esquema (ADR-020). El código generado (`internal/ledger/sqlcgen/`) se versiona; el CI comprueba que no hay drift.
- Config 12-factor con prefijo `II_*`; cada módulo lee sus variables en su `OptionsFromEnv()` con defaults documentados.
- Dependencias permitidas: pgx/v5, google/uuid, prometheus/client_golang, x/crypto. Cualquier otra exige justificación en el PR y, si es estructural, ADR.

### Migraciones (ADR-020)

- Se escriben **a mano** en `backend/db/migrations` como `NNNN_nombre.up.sql` + `NNNN_nombre.down.sql`; todo `up` tiene `down` reversible.
- `make migrate-create name=mi_cambio` genera la pareja; `make migrate-status` verifica checksums (una migración aplicada cuyo fichero cambió es un error).
- `make reset-db` ejecuta TODOS los down y re-aplica todo: los down se ejercitan continuamente. Rehúsa ejecutarse con `II_ENV=prod`.
- En producción, las migraciones se aplican solo en la ventana de mantenimiento diaria (ADR-003).

### Mundo procedural (Incremento 7)

- **`make worldgen`** genera proceduralmente el **mundo multi-región Fase 2** (biomas, ciudades, yacimientos, red ferroviaria/marítima y terminales intermodales) sobre `internal/worldgen`. Es **aditivo** (conserva Askadia (0,0) y su seed), **determinista** (misma `II_WORLD_SEED` ⇒ mismo mundo; value-noise propio + RNG sembrado por celda, **nunca** wall-clock) e **idempotente por clave natural** (re-ejecutarlo no duplica). **Requiere el seed corrido antes.** Paso **opcional** para levantar un mundo multi-región en local; sin él, el entorno usa el mundo mínimo de una región. No añade migraciones (opera tablas de `0003_world`). Variables `II_WORLD_SEED`/`II_WORLD_GRID`/`II_WORLD_REGION_SIZE_M` (defaults en `internal/worldgen/options.go`). Detalle operativo en `docs/runbooks/local.md`.

### Tests

- `make backend-test` ejecuta unit tests siempre; con `II_TEST_DATABASE_URL` apuntando a un servidor PG18 (el de dev vale: el usuario necesita `CREATEDB`) se habilitan integración y E2E, que crean **bases efímeras propias** y no ensucian la de desarrollo.
- `scripts/db-smoke.sh` valida las invariantes del ledger contra la BD de desarrollo (9 casos).

### Bots (ADR-024)

- **El SDK `pkg/botsdk` es la ÚNICA vía soportada para construir bots.** En runtime consume solo la API pública (REST del contrato + WS de ADR-023, ver `docs/api/ws-protocol.md`); prohibido importar `internal/*` desde su código de runtime (sus tests de integración sí pueden). Dinero/stock como strings del contrato — jamás float.
- **Añadir un arquetipo**: implementa la interfaz `Behavior` de `internal/bots` (`Name() string` + `Decide(ctx, *botsdk.Client, *State) error`), regístralo en la población del orquestador y dale variable de densidad propia (`II_BOTS_*`). `Decide` es UNA pasada idempotente: el estado observable de la API manda, `State` solo cachea. Las heurísticas son **auditables**: cada decisión pasa por `decide()` (log slog con bot/arquetipo/decisión/motivo/ids + métrica `ii_bot_decisions_total`) — sin reglas implícitas.
- **`make bots`** arranca el Bot Orchestration Service (`cmd/bots`) contra el gateway local: aprovisiona la población (cuentas `kind=bot`, capitalización del banco central) y la ejecuta. Observabilidad propia en `II_BOTS_ADDR` (default `:8082`).
- Variables `II_BOTS_*` (defaults en `internal/bots/options.go`): `II_BOTS_COAL_PRODUCERS` / `II_BOTS_IRON_PRODUCERS` / `II_BOTS_TRADERS` (densidad, default 1 c/u — la válvula de carga del GDD §19), `II_BOTS_SECRET_SEED` (derivación reproducible de secretos), `II_BOTS_CAPITAL` (capitalización única, default 500000), `II_BOTS_TICK` (periodo de decisión, default `5s`, jitter ±20%), `II_BOTS_API_URL` (default `http://localhost:8080/api/v1`), `II_BOTS_ADDR`.

## 3. Frontend (Nuxt 4)

- Paquete autónomo con **npm sin workspaces** (ADR-021). Kernel puro en `shared/` (Money BigInt, SimTime, uuidv7, Result, mini-i18n) y dominio en `domain/`: **jamás importan vue/nuxt/pinia** (regla de ESLint).
- `npm run gen:api` (o `make frontend-generate`) regenera `types/api.d.ts` desde el contrato. Si el contrato cambió, el typecheck falla ruidosamente hasta reconciliar.
- **Thin client** (FAD): el cliente presenta, nunca decide; el estado replicado solo se escribe aplicando respuestas/eventos del servidor; predicción optimista siempre marcada y reversible.
- Estilos: Sass propio por capas ITCSS (`app/assets/styles/`) + CSS Modules por componente. Prohibidos los frameworks CSS y las librerías de componentes. Tema oscuro por defecto, claro/oscuro conmutables (única preferencia persistida en localStorage).
- Textos SOLO vía `shared/i18n` (`locales/es.json`). El token de sesión vive solo en memoria.
- En dev, Nuxt proxya `/api` → `localhost:8080` (mismo origen, sin CORS); en producción lo hace Caddy.

### Cliente de juego (`/play`, Incremento 5)

Mapa Phaser top-down cenital (ADR-019) + HUD Vue. El estado implementado y sus simplificaciones conscientes están anotados en el FAD (notas «Estado implementado (Incremento 5)» en §11.9, §12.5, §13.6, §14, §16.3 y §26.1).

**Arquitectura de `game/`** (motor de render, sin Vue/Nuxt/Pinia/red; fronteras en ESLint):

- **Entrada única**: `game/index.ts` (`createGame`). La app lo carga perezosamente (`await import('~~/game')` en `GameCanvasHost.vue`): Phaser jamás entra en el bundle del portal.
- **`shared/geometry/grid.ts` — GridProjection**: ÚNICO punto de conversión mundo ↔ pantalla (la API habla metros planos `[x_m, y_m]`, SRID 0). 1 tile = 250 m = 32 px; mundo Askadia 50 000 × 50 000 m = 200 × 200 tiles; chunks de 32 × 32 tiles. Prohibida la matemática de proyección fuera de aquí.
- **`game/map/` — ChunkManager**: streaming + culling + LRU de chunks según viewport (lógica pura testeada en `chunk-logic.ts`). Terreno placeholder: suelo plano coloreado por bioma de región (el backend aún no expone terreno por tile).
- **`game/bridge/`**: deriva **view-models planos** (metros, solo lo que el sprite necesita) de lo VISIBLE, desde el puerto `WorldStateSource` que la app implementa sobre las stores Pinia (`app/composables/useWorldLive.ts`); diffs por identidad y ≤1 recomputación por frame.
- **Capas y renderers**: orden de dibujo fijo por capas (`LAYER_ORDER`: terreno → red → recursos → edificios → vehículos → efectos → overlays → etiquetas); un renderer con **pooling** por tipo de entidad en `game/entities/`, ensamblados en `game/world-live.ts`. Texturas **generadas en runtime** (`game/textures.ts`, claves lógicas, paleta espejo de los tokens Sass) — sin binarios de arte en esta fase.
- **Sincronización**: `app/composables/useGameSync.ts` — bootstrap por REST + deltas por la room WS `corp` (ADR-023); cada evento invalida y **re-consulta** por REST; hueco de `seq`/reconexión → re-bootstrap propio; vehículos extrapolados analíticamente con el `SimClock`.

**Añadir una entidad al mapa** (VM + textura + capa):

1. Exponer el dato en su store replicada (escrita solo con respuestas/eventos del servidor) y en el puerto `WorldStateSource` (`useWorldLive.ts`).
2. Definir su VM plano en `game/bridge/vm.ts` y su derivación desde el source en `game/bridge/derive.ts` (funciones puras, con test).
3. Registrar su textura runtime con clave lógica en `game/textures.ts` (o `game/entities/textures-extra.ts`).
4. Escribir el renderer con pooling en `game/entities/` y ensamblarlo en `game/world-live.ts` dentro de su capa (el orden de creación de containers es el z-order).

**Añadir un panel al HUD**: registra el nombre en `GAME_PANELS` (`app/stores/panels.store.ts`), crea el componente en `app/components/play/` sobre `FloatingPanel` + componentes base (`app/components/base/`), textos SOLO vía `t()` (`shared/i18n/locales/es.json`), botón en `HudSidebar.vue`; datos y comandos vía `useGameApis` → módulos `network/*.api` (patrón `auth.api.ts`) con mappers (los DTO no salen de `network/`); test de componente en `tests/nuxt/`.

**Smoke E2E (Playwright)**: `npm run test:e2e` en `/frontend`. **Prerrequisito: stack vivo** — backend dev en :8080 (`make dev` + `make backend`, opcionalmente `make bots`) y frontend dev en :3000 (`make frontend`); el spec se salta limpiamente si `/healthz` no responde. Flujo de solo lectura: login Demo → `/play` → mundo renderizado → HUD → panel Mercado, con screenshots en `tests/e2e-browser/`. No forma parte de `make test` (los tests unitarios no asumen backend corriendo).

## 4. Observabilidad

- Logging estructurado (slog JSON) con `request_id`; métricas Prometheus en ambos binarios (`ii_http_*`, `ii_sim_time_seconds`, `ii_rate_limited_total`, pool de BD); dashboard base en Grafana (`infra/grafana/dashboards/`).
- Toda funcionalidad nueva incluye logging, manejo de errores tipados del contrato y métricas cuando aporte. No se considera terminada sin ello.

## 5. Definition of Done

Compila · tests verdes (unit + integración) · lint/fmt limpios · sin regresiones · documentación sincronizada · observabilidad · manejo de errores · sin deuda técnica gratuita. `make lint test build` es el gate mínimo local antes de commit.

## 6. Convención de commits

Conventional Commits (`feat:`, `fix:`, `refactor:`, `perf:`, `test:`, `docs:`, `build:`, `ci:`, `chore:`) con scope por contexto cuando aplique (`feat(contracts): …`). Un commit por incremento funcional verificado.
