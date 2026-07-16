# Guía de desarrollo — Imperio Industrial

**Estado:** documento vivo. Describe la implementación real del monorepo y las
decisiones tomadas al construirla (ADR-IMPL). Ante discrepancia de *diseño*, prevalece
el GDD (`docs/gdd.md`); ante discrepancia sobre *cómo está construido*, prevalece este
documento y los `specs/`.

## 1. Estructura del monorepo (sin workspaces)

```
Makefile           # orquestación de todos los comandos
backend/
  engine/          # motor de simulación (Go 1.22, pgx): sim-time, producción,
                   # tránsito, sorteos y liquidaciones CCRI, balancer, outbox
  gateway/         # API pública (TypeScript, Fastify, pg): REST de specs/openapi.yaml
                   # + WebSocket según specs/ws-protocol.md
  bots/            # Bot Orchestration Service (TypeScript): consume la API pública
  migrations/      # migraciones SQL canónicas — aplicación MANUAL (make db-migrate)
  seeds/           # seed_world.sql: mundo inicial determinista e idempotente
frontend/          # cliente web (Nuxt 4 + Vue 3 + Phaser 3 + Pinia + Sass)
infra/             # docker-compose.yml (postgis/postgis:18-3.6 + caddy:2), Caddyfile
docs/              # documentación viva
specs/             # openapi.yaml (REST), ws-protocol.md (WS), schemas/*.sql (DDL espejo)
```

## 2. Comandos (Makefile raíz)

| Comando | Qué hace |
|---|---|
| `make up` / `make down` / `make destroy` | Infraestructura (PostgreSQL 18 + Caddy). `destroy` borra el volumen |
| `make db-migrate` | Aplica migraciones pendientes de `backend/migrations` (manual, con tracking en `public.schema_migrations`) |
| `make db-status` | Migraciones aplicadas vs pendientes |
| `make db-seed` | Carga `backend/seeds/seed_world.sql` (idempotente) |
| `make db-reset` | Recrea la BD vacía (luego `db-migrate` + `db-seed`) |
| `make db-psql` | psql interactivo dentro del contenedor |
| `make engine-build/run/test/vet` | Motor Go |
| `make gateway-install/build/dev/run` | Gateway Fastify (`:8080`) |
| `make bots-install/run` | Población de bots |
| `make frontend-install/dev/build` | Cliente Nuxt (`:3000`) |
| `make stack-build` | Construye las imágenes Docker (engine, gateway, bots, frontend — Dockerfiles multi-stage con su `.dockerignore` por contexto) |
| `make stack-up` / `make stack-down` | Stack de aplicación contenedorizado (perfil `full` del compose). Las migraciones siguen siendo manuales: `make db-migrate` antes de `stack-up` |
| `make install` / `make build` / `make test` / `make verify` | Agregados |

Edge unificado de desarrollo: `http://localhost:8000` (Caddy → `/api/*` y `/ws` al
gateway; el resto al dev server del frontend). PostgreSQL expuesto en `localhost:5440`.

`DATABASE_URL` por defecto: `postgres://imperio:imperio@localhost:5440/imperio`.

## 3. Decisiones de implementación (ADR-IMPL)

| ID | Decisión | Sustituye/afecta | Motivo |
|---|---|---|---|
| ADR-IMPL-01 | **PostgreSQL 18 + `uuidv7()` nativo** como identificador universal (columnas `uuid DEFAULT uuidv7()`). Se elimina el dominio `ulid_id` y los prefijos por tipo (`acc_…`). | GDD 17.2, ADR de IDs, `openapi.yaml` (ahora `format: uuid`, v1.1.0) | Mandato del proyecto. UUIDv7 conserva la ordenabilidad temporal que aportaba ULID; el namespacing por prefijo se pierde y se compensa con tipado en app (branded types en el frontend) |
| ADR-IMPL-02 | **Migraciones manuales**: ficheros numerados en `backend/migrations`, runner en el Makefile (psql `--single-transaction` + tabla `public.schema_migrations`). Nada se aplica automáticamente al arrancar servicios. | Doc BD §Operación | Mandato del proyecto; coherente con la ventana de mantenimiento del GDD |
| ADR-IMPL-03 | **Reparto de procesos**: el **gateway** ejecuta el camino de comando (publicar, aceptar, construir, comprar…) directamente contra SQL en transacciones `SERIALIZABLE` — las invariantes viven en la base (triggers/funciones), así que ningún proceso puede romper la contabilidad. El **motor Go** ejecuta todo lo dirigido por tiempo: avance del sim-clock, fin de lotes, tránsito y averías, cierre de ventanas de sorteo, liquidación de vencimientos, balancer y emisión de eventos outbox. | Arquitectura §5.1 (Contract Service en Go) | Evita una API interna engine↔gateway en Fases 0–1; la regla de oro (ADR-005: "la base garantiza, la aplicación orquesta") es la que protege el valor. El sorteo y las liquidaciones — los flujos que mueven dinero por tiempo — siguen en Go |
| ADR-IMPL-04 | **Acceso a datos**: `pgx/v5` con SQL explícito en el motor (sin sqlc) y `pg` (node-postgres) con SQL explícito en el gateway (sin Drizzle). | Arquitectura §6.1 | Menos toolchain de codegen; el SQL del dominio ya vive en la base. Revisable cuando el volumen de queries lo pida |
| ADR-IMPL-05 | **Credenciales**: tabla nueva `auth.account_credentials(account_id, secret_hash)` con sha256 hex (nivel dev; la spec original no almacenaba credenciales pese a definir `POST /auth/sessions`). Tokens de sesión: aleatorios, se guarda solo su sha256 (como ya definía la spec). | `01_auth.sql` | Hueco de la spec detectado en implementación. Endurecer (argon2/scrypt) antes de producción real |
| ADR-IMPL-06 | **Reloj sim-time persistido**: tabla nueva `world.sim_clock` (fila única: `sim_seconds`, `frozen`). El motor lo avanza a ratio 24× (1 s wall = 24 s sim); el gateway lo lee para `meta.sim_time`. Formato legible: `AÑO-DDD-HH:MM` con `año = días_totales/360 + 1` y `DDD = día del año 001..360`. | GDD 1.1 | El sim-time debe sobrevivir reinicios y congelarse en mantenimiento |
| ADR-IMPL-07 | **Scheduler dirigido por la base**: en lugar de una cola de prioridad en memoria, el motor descubre eventos vencidos con consultas indexadas por vencimiento (fin de lote = `started_at_sim + batch_sim_seconds`, llegada = `segment_entered_sim + duración(tramo)`, `window_closes_at`, `deadline_sim`). El coste sigue siendo ∝ eventos vencidos (índices parciales sobre colas vivas). | GDD 1.1 (cola de prioridad) | Durabilidad gratis (sin snapshots de cola), idempotencia de reinicio, y el mismo perfil de coste |
| ADR-IMPL-08 | **Protocolo WS definido** en `specs/ws-protocol.md` (hello/join/leave/ping; rooms `corp:`/`viewport:`/`alerts:`; snapshot+patch+message; seq por conexión y room; resync por re-join). | FAD §4.4 / ADR-FE-004 / §27.5 | Era la dependencia inter-equipo bloqueante del FAD |
| ADR-IMPL-09 | **Idempotencia de comandos**: tabla `auth.idempotency_keys(key uuid, account_id, endpoint, response jsonb)`; el gateway reproduce la respuesta almacenada ante una clave repetida. Cabecera `Idempotency-Key` opcional (openapi v1.1.0). | FAD §12.8 | Reintentos de red sin doble ejecución |
| ADR-IMPL-10 | **Balancer como agente de ciudades dentro del motor**: publica solicitudes de compra de las ciudades llamando a las mismas rutinas SQL de publicación que usa la API pública (mismo camino contable, garantía pre-fondeada por el banco central), no vía HTTP contra el gateway. | GDD 18.1 §5 (API estándar) | Un salto HTTP interno no añade garantía alguna; la igualdad relevante (mismas reglas contables y de mercado) se conserva. Revisable en Fase 2+ |
| ADR-IMPL-11 | **Bots por la API pública real** (HTTP contra el gateway), sin atajos — la igualdad de API literal del GDD se verifica con ellos. | GDD 13.1 | Principio de diseño intocable |
| ADR-IMPL-12 | **Emisión de stock por producto**: las cuentas `emission` pueden llevar `product_id` (génesis físico). `production_output` = `stock_free +N / emission(producto) −N`; `consumption` al revés. Índices únicos: una emisión monetaria y una génesis por producto. | `03_ledger.sql` (ck_accounts_asset) | Hueco de la spec: con el constraint original era imposible asentar producción/consumo sin romper la doble entrada por activo. El saldo negativo de cada génesis = stock físico neto minteado (auditable como la masa monetaria) |
| ADR-IMPL-13 | **Auto-despacho logístico v1**: al confirmarse un contrato, el motor crea los cargamentos y despacha automáticamente vehículos `idle` del vendedor situados en el nodo de origen hacia el destino por el camino más corto (congestión EMA). Las rutas (`PATCH /world/vehicles`) quedan como líneas regulares manuales. | GDD §7/§8 (sin comando REST de despacho) | La spec REST no define comandos de carga/despacho; sin auto-despacho el ciclo CCRI no sería jugable en v1. Revisable en Fase 2 (despacho manual + CCRI-Flete) |
| ADR-IMPL-14 | **Simplificaciones físicas v1**: el combustible de recetas se consume como un insumo más del inventario del edificio (`buildings.fuel_stock` queda reservado para el modelo in-situ completo); la construcción dura un tiempo fijo (4 h sim) hasta `operational`; el repostaje de vehículos no bloquea (el combustible decrementa con suelo 0). | GDD 5.8, 6.2 | Mantener el vertical slice jugable; cada uno tiene su hueco de evolución documentado |

## 4. Flujo de datos implementado

```
frontend (Nuxt/Phaser)
   │  REST /api/v1/*  (comandos + pull)          WS /ws (rooms, snapshot/patch/message)
   ▼                                              ▲
gateway (Fastify) ── transacciones SERIALIZABLE ──┤ polling outbox (cursor)
   ▼                                              │
PostgreSQL 18 (auth · world · ledger · analytics · outbox)  ← triggers = invariantes
   ▲
engine (Go) — bucle 1s: sim_clock 24×, lotes, tránsito, sorteos, vencimientos,
              balancer/ciudades, salarios/mantenimiento/canon → outbox
   ▲
bots (TS) ── HTTP → gateway (misma API pública que los humanos)
```

## 5. Convenciones transversales

- **Dinero/stock**: `BIGINT` en BD, **strings decimales** en JSON. Prohibido float.
- **Sim-time**: enteros de segundos (`sim_seconds`) en BD/WS; `meta.sim_time` legible +
  `meta.sim_time_seconds` canónico en REST.
- **Errores REST**: `{ error: { code, message, details } }` con los códigos documentados
  en `specs/openapi.yaml` (`INSUFFICIENT_COLLATERAL`, `PUBLICATION_EXHAUSTED`, …).
- **Serialización de entidades**: los DTO del WS son los mismos del REST.
- **Reintentos por serialización**: los comandos del gateway reintentan ante SQLSTATE
  `40001` (hasta 3 veces).
- **Ventana de sorteo**: 45 s reales; micro-ventana 20 s; cooldown anti-parpadeo 30 s.
  (Valores dev dentro de los rangos del GDD 5.3.1.)

## 6. Estado de implementación

Se actualiza al cerrar cada fase. Ver historial en git.

- [x] Infra: compose PG18+PostGIS/Caddy, Makefile, migraciones manuales
- [x] Migraciones + seed validados sobre PostgreSQL 18 (uuidv7, invariantes del ledger con smoke)
- [x] Motor Go (sim, contratos con sorteo y liquidación, auto-despacho/tránsito, balancer, outbox) — smoke: ciclo CCRI completo contra BD real con ledger balanceado
- [x] Gateway (REST completo de openapi v1.1.0 + WS de ws-protocol.md + idempotencia 0007) — smoke: 15/15 tests + flujo curl/WS, incluida liquidación cruzada con el motor
- [x] Bots (productor, transformador, arbitrajista con heurísticas puras testeadas 36/36)
- [x] Frontend (kernel probado, capa de red WS/REST con reconexión, 11 stores Pinia, mundo Phaser top-down con interpolación, HUD + 6 paneles, guard de sesión) — 94/94 tests, typecheck y build verdes
- [x] Verificación end-to-end del ciclo CCRI (`make verify`: infra+builds+CCRI scripted+bots+invariantes+WS, PASS reproducible)
- [x] Auditoría adversarial de integridad contable (SQL fino sobre BD post-verify). Dos bugs corregidos:
  1. `ledger.confirm_contract` / `ledger.settle_contract_prorata` insertaban partidas de importe 0
     cuando la garantía del 10% (o su prorrateo/compensación) redondeaba a 0 (valor del tramo < 10),
     violando `CHECK (amount <> 0)` → el contrato ni se confirmaba ni se liquidaba jamás (fondos
     congelados). Fix: asientos condicionados a importe ≠ 0 (el residuo de división entera sigue
     yendo SIEMPRE al sink). Ficheros: `backend/migrations/0004_ledger.sql` + espejo
     `specs/schemas/03_ledger.sql`.
  2. `DELETE /contracts/publications/:id` con aceptaciones `pending_draw` dejaba su colateral
     (escrow / stock+garantía) bloqueado para siempre: el motor solo sortea publicaciones en
     `draw_window`/`micro_window` y la publicación pasaba a `cancelled` sin resolverlas. Fix: el
     cancel libera el colateral de cada aceptación pendiente (asiento `publication_release`), la
     marca `released` (draw_order 0) y emite `acceptance.resolved`. Ficheros:
     `backend/gateway/src/routes/contracts.ts` + nota en `specs/openapi.yaml`.
- [x] Stack completo verificado por el edge Caddy `:8000` (login REST + WS con snapshot + frontend
  servido). Nota de infra: Caddy corre en `network_mode: host` (el bridge de Docker hacia el host
  queda bloqueado por firewalls típicos); en dev directo a `:3000`, el WS usa el override
  `NUXT_PUBLIC_WS_URL` (el devProxy de Nitro no hace upgrade de WebSocket) — ya lo inyecta
  `make frontend-dev`.
- [x] Contenedorización (perfil `full`): Dockerfiles multi-stage + `.dockerignore` por contexto
  para engine (alpine, binario estático), gateway (deps de producción), bots (solo dist) y
  frontend (output de Nitro). Verificado: 4 imágenes construyen y el stack contenedorizado
  completo sirve login/WS/frontend por el edge, con los bots operando dentro del contenedor.
