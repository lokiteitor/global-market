# ADR-016 — Estructura de monorepo con raíz fija y Makefile como punto de entrada único

| Campo | Valor |
|---|---|
| **ID** | ADR-016 |
| **Fecha** | 2026-07-16 |
| **Estado** | Aceptado |
| **Supersede** | Estructura propuesta en Arquitectura §7 (`engine/`, `gateway/`, `bots/`, `stress/`, `deploy/`, `ops/`) y FAD §10.1 (`apps/web` + `packages/`) |

## Contexto

El SAD v1.0 proponía un monorepo con carpetas por desplegable (`engine/` Go, `gateway/` TS, `bots/`, `stress/`, `deploy/`) y el FAD un workspace pnpm con `apps/web` y `packages/`. El mandato de proyecto fija una raíz estable e independiente por área, con automatización centralizada.

## Decisión

La raíz del monorepo es **fija e inmutable**:

```
/backend      # todo el código de servidor (Go): gateway, engine, bots, SDK, migraciones
/frontend     # cliente web (Nuxt 4 + Vue 3 + TS + Pinia + Sass + Phaser)
/infra        # Dockerfiles, Docker Compose, Caddy, Prometheus, Grafana (provisioning y dashboards)
/docs         # documentación viva: GDD, SAD, FAD, DB, ADRs, API (contrato OpenAPI), runbooks, guías
/scripts      # scripts de apoyo (shell) invocados desde el Makefile
/tools        # herramientas de desarrollo (lint de contrato, utilidades de generación)
Makefile      # ÚNICO punto de entrada para tareas comunes
README.md
```

Reglas:

1. **Cada carpeta es completamente independiente**: sin imports cruzados entre `/backend` y `/frontend`; el único acoplamiento permitido es *contract-first* (ambos derivan del contrato `/docs/api/openapi.yaml` en tiempo de generación, nunca en runtime).
2. El **Makefile** orquesta todo (`build`, `test`, `lint`, `fmt`, `generate`, `run`, `dev`, `backend`, `frontend`, `infra`, `migrate-*`, `seed`, `clean`). Ningún flujo documentado depende de comandos manuales.
3. La carpeta `/specs` **se disuelve**: el contrato OpenAPI pasa a `/docs/api/openapi.yaml` (la documentación de API es documentación viva); los DDL de `specs/schemas/*.sql` dejan de ser "especificación duplicada" y se convierten en las **migraciones reales** de `/backend/db/migrations` (fuente única de verdad del esquema, DRY).
4. Los **bots y su SDK viven dentro de `/backend`** (`pkg/botsdk` como API pública del SDK, `cmd/bots` como orquestador, `internal/bots` para arquetipos): son Go y comparten toolchain, pero consumen exclusivamente la API pública del gateway (igualdad de API literal, ADR-010).
5. Módulo Go: `github.com/lokiteitor/global-market/backend`.
6. La infraestructura nunca se mezcla con código de negocio: todo artefacto de despliegue vive en `/infra`.

## Consecuencias

- (+) Raíz estable durante años; onboarding y automatización triviales; fronteras físicas alineadas con las lógicas.
- (+) Un solo lugar para el contrato (API-first) y un solo lugar para el esquema (migraciones).
- (−) Reescritura de las secciones de estructura en SAD §7 y FAD §10 (hecha en SAD v1.1 / FAD v1.1).
- (−) Las referencias históricas a `engine/migrations/` y `specs/` en los documentos quedan actualizadas por esta decisión.
