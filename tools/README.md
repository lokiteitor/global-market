# /tools — herramientas de desarrollo

Herramientas de apoyo al desarrollo, siempre invocadas desde el `Makefile` raíz (ADR-016). Nunca contienen lógica de negocio.

| Herramienta | Propósito | Target |
|---|---|---|
| `openapi/` | Lint del contrato `docs/api/openapi.yaml` con Redocly CLI (Contract First: el contrato roto rompe el build) | `make contract-lint` |
