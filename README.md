# Imperio Industrial

MMO de **simulación económica, industrial y logística** en un mundo único persistente que nunca se resetea. Decenas de miles de jugadores humanos y una población permanente de bots comparten el mismo mapa, el mismo mercado (tablón global de contratos CCRI) y las mismas reglas, sobre un servidor autoritativo.

Los tres invariantes que gobiernan todo el diseño:

1. **El valor económico nunca se pierde ni se duplica** — ledger de doble entrada ACID; las invariantes viven en la base de datos, no en código de aplicación.
2. **El coste de simulación es proporcional a los eventos, no a las entidades** — motor event-driven sin tick global; magnitudes continuas derivadas analíticamente bajo demanda.
3. **Fronteras lógicas firmes desde el día 1; topología física progresiva** — monolito modular; extracción a procesos separados solo contra medición.

## Estructura del monorepo (ADR-016)

```
/backend      Servidor completo en Go: gateway (REST+WS+auth), engine (simulación),
              orquestador de bots (cmd/bots), runner de migraciones, seed y SDK
              oficial de bots (pkg/botsdk). Módulo único con bounded contexts sin
              imports cruzados (internal/{auth,ledger,sim,...}).
/frontend     Cliente web autónomo: Nuxt 4 + Vue 3 + TypeScript estricto + Pinia +
              Sass propio (sin frameworks CSS) + Phaser (top-down, Incremento 5).
/infra        Docker Compose (PostgreSQL 18 + PostGIS, Prometheus, Grafana, Caddy),
              Dockerfiles y provisioning de dashboards.
/docs         Documentación viva: GDD v1.3, SAD v1.1, FAD v1.1, modelo de datos,
              ADRs (docs/adr/), contrato OpenAPI (docs/api/openapi.yaml), guías y runbooks.
/scripts      Scripts de apoyo invocados desde el Makefile.
/tools        Herramientas de desarrollo (lint del contrato OpenAPI).
Makefile      ÚNICO punto de entrada de tareas.
```

## Arranque rápido

Requisitos: Go ≥ 1.22 (con descarga automática de toolchain), Node 22 + npm, Docker + Compose v2.

```bash
make dev        # BD (PG18+PostGIS) + observabilidad + migraciones + seed
make backend    # gateway (:8080) + engine (:8081)      [otra terminal]
make frontend   # cliente Nuxt en http://localhost:3000  [otra terminal]
```

Credenciales de desarrollo tras el seed: corporación `Demo` / `demo-secret-dev`.

Stack completo en Docker: `make run` (perfil `full`, entra por Caddy en :80).

## Tareas principales

| Comando | Qué hace |
|---|---|
| `make help` | Lista completa de tareas |
| `make build` / `make test` / `make lint` / `make fmt` | Ciclo de vida de todo el monorepo |
| `make generate` | Codegen: sqlc (queries Go) + tipos TS del contrato |
| `make migrate-up` / `migrate-down` / `migrate-status` / `migrate-create name=x` / `reset-db` | Migraciones manuales con runner propio (ADR-020) |
| `make seed` | Datos mínimos de desarrollo (idempotente) |
| `make bots` | Bot Orchestration Service (`cmd/bots`, ADR-024): aprovisiona y ejecuta la población de bots jugando por la API pública vía `pkg/botsdk` (densidad con `II_BOTS_*`; métricas en :8082) |
| `make infra-core` / `infra-down` / `infra-logs` | Infraestructura local |

## Observabilidad

- Prometheus: <http://localhost:9090> · Grafana: <http://localhost:3001> (admin/admin, dashboard *Imperio — Backend Overview*)
- Métricas de los servicios: `:8080/metrics` (gateway) y `:8081/metrics` (engine)

## Documentación

- **Diseño y arquitectura**: [`docs/gdd.md`](docs/gdd.md) (normativo), [`docs/arquitectura_imperio_industrial.md`](docs/arquitectura_imperio_industrial.md), [`docs/frontend-architecture-document.md`](docs/frontend-architecture-document.md)
- **Modelo de datos**: [`docs/documentacion_base_de_datos.md`](docs/documentacion_base_de_datos.md) (fuente de verdad del esquema: `backend/db/migrations/`)
- **Contrato de API** (API-first): [`docs/api/openapi.yaml`](docs/api/openapi.yaml) (REST) · [`docs/api/ws-protocol.md`](docs/api/ws-protocol.md) (WebSocket `GET /api/v1/ws` del Notification/Event Gateway, ADR-023: eventos en tiempo real con bootstrap por REST + deltas)
- **Decisiones**: [`docs/adr/`](docs/adr/README.md)
- **Guías**: [`docs/guias/desarrollo.md`](docs/guias/desarrollo.md) · **Runbooks**: [`docs/runbooks/local.md`](docs/runbooks/local.md)

Ante discrepancia entre documentos prevalece el GDD; toda decisión estructural nueva exige un ADR previo.
