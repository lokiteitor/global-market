# Architecture Decision Records — Imperio Industrial

Registro de decisiones estructurales del proyecto. Toda nueva decisión que modifique arquitectura, contrato o esquema **debe** registrarse aquí antes de implementarse, incluyendo la medición o el requisito que la justifica (SAD §12).

## Índice

### ADR-001 … ADR-015 — consolidados en el SAD

Los quince primeros ADRs (motor event-driven, sim-time 24×, ventana de mantenimiento, inventario como cuentas del ledger, invariantes en SQL, shards vs. Logistics, región=shard, monolito modular, Docker Compose como destino, bots con API real, ventana de sorteo, replay relajado, proceso único, garantía por publicación, handoff formal) están registrados en `docs/arquitectura_imperio_industrial.md` §12.2, con su origen en el Anexo B del GDD. Siguen vigentes salvo lo derogado explícitamente abajo.

### ADR-016+ — ficheros individuales en este directorio

| ADR | Título | Estado | Deroga/Modifica |
|---|---|---|---|
| [ADR-016](ADR-016-estructura-monorepo.md) | Estructura de monorepo con raíz fija y Makefile único | Aceptado | SAD §7, FAD §10.1 |
| [ADR-017](ADR-017-backend-unificado-go.md) | Backend unificado en Go (gateway incluido) | Aceptado | GDD #19 (dual stack), parte de ADR-005 |
| [ADR-018](ADR-018-postgresql-18-uuidv7.md) | PostgreSQL 18 y UUIDv7 nativo como ID universal | Aceptado | GDD §17.2 (ULID), PG16 |
| [ADR-019](ADR-019-vista-top-down.md) | Vista top-down cenital (90°) y geometría planar | Aceptado | GDD §1 (isométrico), FAD §16.2 |
| [ADR-020](ADR-020-migraciones-manuales.md) | Migraciones SQL manuales con runner propio; sqlc solo queries | Aceptado | Drizzle ORM (SAD §4.1/§6.1) |
| [ADR-021](ADR-021-frontend-autonomo.md) | Frontend autónomo sin workspaces; tipos generados del contrato | Aceptado | FAD §10.1/§10.7/§23.1 |

## Formato

Cada ADR usa el formato del SAD §12.1: ID, Fecha, Estado (Propuesto/Aceptado/Deprecado), Contexto, Decisión, Consecuencias, y qué documentos deroga o modifica. Los documentos normativos afectados se actualizan en el mismo cambio (documentación viva: nunca queda documentación obsoleta).
