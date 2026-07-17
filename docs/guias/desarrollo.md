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

### Tests

- `make backend-test` ejecuta unit tests siempre; con `II_TEST_DATABASE_URL` apuntando a un servidor PG18 (el de dev vale: el usuario necesita `CREATEDB`) se habilitan integración y E2E, que crean **bases efímeras propias** y no ensucian la de desarrollo.
- `scripts/db-smoke.sh` valida las invariantes del ledger contra la BD de desarrollo (9 casos).

## 3. Frontend (Nuxt 4)

- Paquete autónomo con **npm sin workspaces** (ADR-021). Kernel puro en `shared/` (Money BigInt, SimTime, uuidv7, Result, mini-i18n) y dominio en `domain/`: **jamás importan vue/nuxt/pinia** (regla de ESLint).
- `npm run gen:api` (o `make frontend-generate`) regenera `types/api.d.ts` desde el contrato. Si el contrato cambió, el typecheck falla ruidosamente hasta reconciliar.
- **Thin client** (FAD): el cliente presenta, nunca decide; el estado replicado solo se escribe aplicando respuestas/eventos del servidor; predicción optimista siempre marcada y reversible.
- Estilos: Sass propio por capas ITCSS (`app/assets/styles/`) + CSS Modules por componente. Prohibidos los frameworks CSS y las librerías de componentes. Tema oscuro por defecto, claro/oscuro conmutables (única preferencia persistida en localStorage).
- Textos SOLO vía `shared/i18n` (`locales/es.json`). El token de sesión vive solo en memoria.
- En dev, Nuxt proxya `/api` → `localhost:8080` (mismo origen, sin CORS); en producción lo hace Caddy.

## 4. Observabilidad

- Logging estructurado (slog JSON) con `request_id`; métricas Prometheus en ambos binarios (`ii_http_*`, `ii_sim_time_seconds`, `ii_rate_limited_total`, pool de BD); dashboard base en Grafana (`infra/grafana/dashboards/`).
- Toda funcionalidad nueva incluye logging, manejo de errores tipados del contrato y métricas cuando aporte. No se considera terminada sin ello.

## 5. Definition of Done

Compila · tests verdes (unit + integración) · lint/fmt limpios · sin regresiones · documentación sincronizada · observabilidad · manejo de errores · sin deuda técnica gratuita. `make lint test build` es el gate mínimo local antes de commit.

## 6. Convención de commits

Conventional Commits (`feat:`, `fix:`, `refactor:`, `perf:`, `test:`, `docs:`, `build:`, `ci:`, `chore:`) con scope por contexto cuando aplique (`feat(contracts): …`). Un commit por incremento funcional verificado.
