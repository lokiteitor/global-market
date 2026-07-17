# ADR-022 — Contrapartida física del ledger: cuentas `world_source` para altas/bajas de stock

| Campo | Valor |
|---|---|
| **ID** | ADR-022 |
| **Fecha** | 2026-07-16 |
| **Estado** | Aceptado |
| **Modifica** | Modelo contable del ledger (DB doc §ledger, migración `0004_ledger`): enum `ledger.account_kind` y constraints de `ledger.accounts` |

## Contexto

El ledger de doble entrada exige que cada asiento sume cero **por activo** (dinero, o cada producto) — trigger `assert_transaction_balanced`. Los tipos de transacción `production_output` (alta de stock producido/extraído) y `consumption` (baja por insumos, combustible o consumo urbano) existen desde el diseño, pero **no existe cuenta de contrapartida posible**: todas las cuentas de stock (`stock_free`, `stock_reserved`, `custody`) exigen `product_id NOT NULL` y saldo `>= 0`, y la única cuenta que puede ser negativa (`emission`) es exclusivamente monetaria. Resultado: **es contablemente imposible asentar producción o consumo de stock** — defecto del modelo detectado al implementar el Incremento 1 (el seed del mundo mínimo no podía fondear inventarios).

En un ledger cerrado algo debe poder ser negativo por *fiat*: `emission` cumple ese rol para el dinero (su saldo negativo = masa monetaria emitida, visible para el Economy Balancer).

## Decisión

1. Nuevo `ledger.account_kind`: **`world_source`** — cuenta de stock (una por producto, titular: banco central) que actúa como contrapartida física del mundo. **Única cuenta de stock que puede ser negativa**: su saldo negativo es exactamente el stock neto emitido al mundo de ese producto, simétrico a `emission` para el dinero.
2. Asientos canónicos:
   - **Producción/extracción** (`production_output`): `+N stock_free(corporación, producto, almacén)` / `−N world_source(producto)`.
   - **Consumo** (`consumption` — insumos, combustible, consumo final de ciudades): `+N world_source(producto)` / `−N stock_free(...)` (el stock "vuelve al mundo"/se destruye).
3. Constraints ajustadas en migración: `ck_accounts_non_negative` pasa a permitir negativo en `emission` **y** `world_source`; `ck_accounts_asset` clasifica `world_source` como cuenta de stock (`product_id NOT NULL`).
4. La coherencia física sigue intacta: el plano físico (yacimientos `remaining_amount`, `building_inventories`) y el contable se mueven juntos por eventos, con la reconciliación periódica ya diseñada (ADR-004). El agregado de `world_source` por producto se convierte en una métrica más del Balancer (stock total emitido vs. consumido).

## Consecuencias

- (+) La doble entrada por activo se mantiene estricta (suma cero siempre); producción y consumo quedan asentables sin excepciones al trigger.
- (+) Simetría conceptual dinero↔stock: `emission`/`world_source` son las dos únicas cuentas fiat, ambas del banco central, ambas legibles como masa emitida.
- (−) Una fila de cuenta por producto en el ledger (crecimiento trivial).
- El DB doc v1.2 documenta el kind y los asientos canónicos; el GDD no cambia (la mecánica de juego es idéntica; esto es contabilidad interna).
