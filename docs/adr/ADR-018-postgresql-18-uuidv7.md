# ADR-018 — PostgreSQL 18 y UUIDv7 nativo como identificador universal

| Campo | Valor |
|---|---|
| **ID** | ADR-018 |
| **Fecha** | 2026-07-16 |
| **Estado** | Aceptado |
| **Deroga** | GDD §17.2 (ULID con espacio de nombres por tipo) y el dominio `ulid_id`; sustituye PostgreSQL 16 por 18 |

## Contexto

El GDD/SAD v1.2 fijaba PostgreSQL 16 + PostGIS y ULID prefijados por tipo (`acc_…`, `ctr_…`) generados por la aplicación, con CHECKs de formato por tabla. El mandato de proyecto exige PostgreSQL 18 con **UUIDv7 nativo** y funciones nativas. PostgreSQL 18 incorpora `uuidv7()` en el core: identificadores ordenados temporalmente (índices B-tree sin fragmentación) con tipo binario `uuid` de 16 bytes.

## Decisión

1. **PostgreSQL 18 + PostGIS 3.6** es la única base de datos (una instancia, esquemas por dominio — se conservan ADR-004/008 y la regla de oro del ledger).
2. **Todos los identificadores de entidad son `uuid` (UUIDv7), planos y sin prefijo**, tanto en la base como en la API pública.
   - En BD: `id uuid PRIMARY KEY DEFAULT uuidv7()`. FKs `uuid → uuid`. Desaparecen el dominio `ulid_id` y los CHECKs `LIKE 'xxx_%'`.
   - Cuando la aplicación necesita el ID **antes** del INSERT (partidas del ledger pre-generadas para funciones todo-o-nada, claves de idempotencia), lo genera con UUIDv7 en Go (`github.com/google/uuid`).
   - En la API: `type: string, format: uuid`. Se **mantienen los schemas nominales** del contrato (`AccountId`, `ContractId`, `VehicleId`, …) para que el codegen produzca tipos distinguibles (branded types en TS, tipos wrapper en Go): el tipado por tipo de entidad se preserva en el sistema de tipos, no en el formato del string.
3. Excepción única que se conserva: `outbox.events.seq` sigue siendo `BIGINT IDENTITY` (el polling exige orden total barato); `event_id` pasa a `uuid`.
4. Los dominios `sim_time`, `money_amount` y `stock_qty` se conservan tal cual (BIGINT; dinero/stock siempre enteros punto fijo serializados como strings en la API — invariante del ledger, sin cambios).
5. Las **invariantes del ledger permanecen en SQL** (triggers de saldo, doble entrada diferida por activo, inmutabilidad append-only, funciones todo-o-nada `confirm_contract`/`settle_contract_prorata` con parámetros `uuid[]`). Esto no es "lógica innecesaria en la base": es el invariante nº 1 de la arquitectura y usa exclusivamente funciones nativas/plpgsql.
6. Particionado: no se introduce en Fases 0–1 (YAGNI); `ledger.entries` es el primer candidato cuando la medición lo exija, vía ADR (ya previsto en DB doc).
7. Imagen de referencia: `postgis/postgis:18-3.6` (validada en `/infra/docker-compose.yml`).

## Consecuencias

- (+) Generación nativa en BD y en app, orden temporal, joins e índices sobre 16 bytes binarios (vs 30 chars TEXT), sin CHECKs de formato por tabla.
- (+) Contrato más simple; el mandato "UUIDv7 nativo" se cumple literalmente.
- (−) Se pierde la legibilidad del tipo a simple vista en logs/URLs; se compensa con schemas nominales en el contrato, tipos wrapper y logging estructurado que siempre acompaña el ID con su campo (`account_id=…`).
- (−) Reescritura mecánica del contrato OpenAPI (patrones de ID → `format: uuid`) y de todos los DDL. Hecho en OpenAPI v1.1.0 y migraciones iniciales.
- La auditoría cruzada cross-schema (GDD §17.2) se conserva: los `reference_id` siguen siendo un único espacio global de UUIDs.
