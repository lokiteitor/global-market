# ADR-020 — Migraciones SQL manuales con runner propio; sqlc solo para queries

| Campo | Valor |
|---|---|
| **ID** | ADR-020 |
| **Fecha** | 2026-07-16 |
| **Estado** | Aceptado |
| **Deroga** | Drizzle ORM como gestor del esquema `auth` (SAD §4.1/§6.1, DB doc); modifica la sección de migraciones del DB doc |

## Contexto

El mandato prohíbe herramientas de migración automática y generación automática de esquemas. El diseño anterior usaba Drizzle (auth) y el tooling de sqlc para aplicar DDL. Además, con ADR-017 el gateway es Go y Drizzle desaparece de todos modos. Una herramienta externa de migración (golang-migrate, atlas, …) sería una dependencia más para un problema que se resuelve con ~300 líneas de Go controladas por el proyecto.

## Decisión

1. **Todas las migraciones se escriben a mano en SQL** y viven en `/backend/db/migrations`. Nada genera esquema automáticamente.
2. Convención de ficheros: `NNNN_nombre.up.sql` y `NNNN_nombre.down.sql` (NNNN secuencial de 4 dígitos). Toda migración `up` tiene su `down` reversible; si una migración es genuinamente irreversible se declara con un `down` que falla explícitamente explicando por qué.
3. **Runner propio** en `/backend/cmd/migrate` (sin dependencias más allá de pgx):
   - Tabla de control `public.schema_migrations(version int, name text, checksum text, applied_at timestamptz)`.
   - Cada migración se aplica **dentro de una transacción**; una directiva `-- migrate:no-transaction` en cabecera la excluye (índices `CONCURRENTLY`, etc.).
   - `status` verifica **checksums SHA-256**: una migración aplicada cuyo fichero cambió es un error (reproducibilidad).
   - Subcomandos: `up`, `down [n]`, `status`, `create <nombre>`, `reset` (drop de esquemas de dominio + reaplicación completa; solo entornos no productivos, protegido por confirmación/env).
4. Makefile (punto de entrada único): `make migrate-up`, `make migrate-down`, `make migrate-create name=...`, `make migrate-status`, `make reset-db`.
5. Las migraciones se aplican en producción **solo durante la ventana de mantenimiento diaria** (ADR-003, sin cambios).
6. **sqlc se conserva exclusivamente como generador de código Go a partir de queries SQL escritas a mano** (`make generate`); nunca genera ni aplica esquema. El código generado se versiona en el repo.

## Consecuencias

- (+) Reproducibilidad total y auditabilidad del esquema; cero magia; el equipo controla el runner.
- (+) Compatible con la regla de oro del ledger: las invariantes (triggers, funciones) forman parte de las migraciones escritas a mano.
- (−) Coste de mantener el runner (~pequeño y estable) y de escribir `down` de cada migración.
- Los DDL de `specs/schemas/*.sql` se reescriben como migraciones iniciales (con los cambios de ADR-018/019) y `specs/` desaparece (ADR-016).
