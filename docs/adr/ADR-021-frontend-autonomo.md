# ADR-021 — Frontend autónomo sin workspaces; tipos generados del contrato

| Campo | Valor |
|---|---|
| **ID** | ADR-021 |
| **Fecha** | 2026-07-16 |
| **Estado** | Aceptado |
| **Deroga** | FAD §10.1/§10.7 (`apps/web` + `packages/api-types`/`domain-kernel` con pnpm workspaces) y FAD §23.1 (pnpm como gestor) |

## Contexto

El FAD asumía un workspace pnpm con paquetes compartidos entre el gateway TS y el cliente. Con ADR-017 (backend 100% Go) ya no existe consumidor TS fuera del cliente, y el mandato prohíbe npm workspaces y exige carpetas raíz completamente independientes.

## Decisión

1. `/frontend` es un **paquete Node autónomo** (Nuxt 4 + Vue 3 + TypeScript estricto + Pinia + Sass). Sin workspaces de ningún tipo.
2. Gestor de paquetes: **npm** (presente en el entorno, sin instalación global adicional; la ventaja de pnpm era la eficiencia de workspaces, que ya no aplica). Lockfile `package-lock.json` versionado.
3. Los antiguos `packages/` se internalizan:
   - `packages/domain-kernel` → `/frontend/src/shared/` (Money, Quantity, SimTime, ids, Result, event-bus, geometry) — kernel puro sin dependencias de framework.
   - `packages/api-types` → **generación local**: `npm run gen:api` (invocado por `make generate`) ejecuta `openapi-typescript` contra `/docs/api/openapi.yaml` y emite tipos versionados dentro de `/frontend`. El frontend **nunca** escribe DTOs a mano (O5 del FAD intacto).
4. La independencia entre carpetas se entiende como **ausencia de acoplamiento en build/runtime**; la lectura del contrato en `/docs/api` durante `make generate` es el mecanismo contract-first orquestado por el Makefile, no una dependencia entre paquetes.
5. Si el contrato cambia y los tipos generados divergen, `make generate` + typecheck **fallan ruidosamente** (frontera dura del FAD O5, conservada).
6. Prohibiciones de estilo intactas y ampliadas por mandato: sin Tailwind, Bootstrap, Bulma, Vuetify ni ninguna librería de componentes/CSS utilitario. Todo el sistema visual es Sass propio (FAD §15).

## Consecuencias

- (+) Cero workspaces, cero acoplamiento de tooling entre backend y frontend; el contrato es la única frontera compartida.
- (+) npm sin instalaciones globales adicionales; onboarding inmediato.
- (−) Sin paquete compartido, los branded types del kernel existen solo en el cliente (el backend Go tiene sus propios tipos wrapper); la coherencia la garantiza el contrato, no un paquete común.
- FAD v1.1 reescribe §10 y §23.1 conforme a este ADR.
