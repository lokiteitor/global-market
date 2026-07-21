# ADR-024 — SDK oficial de bots y Bot Orchestration Service

| Campo | Valor |
|---|---|
| **ID** | ADR-024 |
| **Fecha** | 2026-07-17 |
| **Estado** | Aceptado |
| **Desarrolla** | ADR-010 (bots como procesos externos con la API real) y GDD §13/§15.4 |

## Contexto

El mandato exige un **SDK oficial** como única forma soportada de construir bots, que abstraiga autenticación, conexión, eventos, comandos, estado, pathfinding, contratos, mercado y logística, consumiendo exclusivamente la API pública. El GDD fija además que la capitalización de bots es **emisión contabilizada del banco central** y que los bots de producción usan heurísticas auditables.

## Decisión

1. **`backend/pkg/botsdk`** es el SDK oficial y la única vía soportada para bots. En runtime consume **solo la API pública**: REST (contrato OpenAPI vigente) + WebSocket (ADR-023). Prohibido importar `internal/*` desde el código de runtime del SDK (los tests de integración del propio repo sí pueden, para levantar el gateway en proceso).
2. Superficie del SDK: `Client` (auth/sesión, reintentos con `Idempotency-Key`, rate-limit awareness con `Retry-After`), sub-APIs tipadas (ledger, board/publications/acceptances/contracts, world: catálogos/concesiones/edificios/producción, logistics: red/route-plans/rutas, fleet: vehículos/cargamentos/dispatch), cliente WS con reconexión y watermark, y helpers de decisión (espera de estados, paginación). **Extensible**: un arquetipo nuevo se implementa contra la interfaz `Behavior` (`Decide(ctx, *Client) error` invocado periódicamente) sin tocar el SDK.
3. **`cmd/bots`** es el Bot Orchestration Service (proceso aparte, ADR-010): gestiona el **ciclo de vida** — creación de cuentas `kind=bot` con credenciales, `bot_profiles` y **capitalización** (`bot_capitalization`: +cash/−emission) — mediante paquetes internos y BD, porque es una operación del banco central, **no un comando de juego**; y ejecuta la población (una goroutine por bot con tick jitterizado) donde **todo el gameplay pasa por el SDK** (igualdad de API literal: mismos endpoints, mismos rate limits). Densidad por configuración (`II_BOTS_*`) como válvula de carga (GDD §19).
4. **Arquetipos v1** (reglas fijas, heurísticas auditables — validación del loop económico, Fase 0 del GDD): productor de carbón (extrae y vende; atiende solicitudes de compra despachando camiones), productor de hierro (mantiene combustible publicando solicitudes de compra de carbón; extrae y vende), y comerciante (compra barato en el tablón y re-lista con margen). `freighter` y transformador industrial llegan con CCRI-Flete (Fase 2) y fases posteriores.
5. El retiro de bots (liquidación + absorción monetaria) se implementa junto al ciclo de embargo (Incremento 6).

## Consecuencias

- (+) Los bots ejercitan permanentemente el camino real (stress test vivo); ningún bot puede saltarse el mercado.
- (+) La política monetaria y la población de bots comparten libro (emisión asentada).
- (−) El orquestador tiene doble naturaleza (admin por BD + jugador por API); la frontera queda nítida: lifecycle=interno, gameplay=SDK.
