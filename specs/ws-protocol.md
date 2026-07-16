# Protocolo WebSocket del Notification/Event Gateway — v1.0

**Estado:** Normativo. Este documento resuelve la cuestión abierta nº 1 del FAD (§27.5)
y el requisito de ADR-FE-004: el protocolo del WS está fuera de `openapi.yaml` y se
especifica aquí. Gateway (`backend/gateway`) y cliente (`frontend`) lo implementan 1:1.

- **Endpoint:** `GET /ws` (upgrade WebSocket) en el gateway (`:8080`; tras Caddy, `/ws`).
- **Formato:** frames de texto JSON, un objeto por frame. Campo discriminador `type`.
- **Modelo de sincronización:** rooms (áreas de interés) + snapshot inicial + patches
  incrementales + messages puntuales, según FAD §4.4/§12.
- **Orden:** el orden de entrega es el del socket (TCP). Cada room lleva un `seq`
  monotónico **por conexión y room** asignado por el gateway (el snapshot fija la base
  `seq = 0`; el primer patch es `seq = 1`). No hay detección de huecos intra-conexión:
  tras una reconexión el cliente re-hace `join` y recibe snapshot nuevo (resync).
- **Tiempo:** todo plazo va en sim-time (`sim_seconds`, enteros). Dinero y stock como
  strings de punto fijo, igual que en REST.

## 1. Handshake

1. El cliente abre `WS /ws` (sin token en la URL).
2. Primer frame obligatorio del cliente:

```json
{ "type": "hello", "token": "<token de sesión REST>" }
```

3. Respuesta del gateway (o cierre con `error` + close code 4401 si el token es inválido):

```json
{
  "type": "hello_ok",
  "account": { "id": "<uuid>", "name": "Aurora Corp", "kind": "human" },
  "sim": { "sim_seconds": 123456, "frozen": false },
  "server_time": "2026-07-15T10:00:00Z"
}
```

Cualquier frame previo a `hello` distinto de `hello` → `error` `NOT_AUTHENTICATED` y cierre.

## 2. Frames cliente → servidor

| `type` | Campos | Semántica |
|---|---|---|
| `hello` | `token` | Autenticación (solo como primer frame) |
| `join` | `room` | Suscribirse a una room. Responde `snapshot` de esa room |
| `leave` | `room` | Desuscribirse |
| `ping` | `t` (número libre del cliente) | Heartbeat; responde `pong` |

Un `join` a una room `viewport:` **reemplaza** cualquier viewport anterior de la conexión
(solo hay un viewport activo por conexión); el gateway emite el snapshot del nuevo bbox.

## 3. Rooms

| Room | Formato | Contenido |
|---|---|---|
| Corporación | `corp:<account_id>` | Todo lo propio: edificios, vehículos, cargamentos, publicaciones, contratos, cuentas del ledger. Solo la propia cuenta (403 → `error` `FORBIDDEN` si no coincide con la sesión) |
| Viewport | `viewport:<minLon>,<minLat>,<maxLon>,<maxLat>` | Entidades espaciales dentro del bbox: ciudades, edificios (de cualquier corporación) y vehículos en tránsito |
| Alertas | `alerts:<account_id>` | Solo `message` (resultado de sorteo, liquidaciones, avisos de mantenimiento). Sin snapshot de estado |

No existe room de tablón global: el tablón es pull por REST (`GET /contracts/board`).

## 4. Frames servidor → cliente

### 4.1 `snapshot`

Estado completo y autoritativo de la room en el momento del `join`.

```json
{
  "type": "snapshot",
  "room": "corp:<uuid>",
  "seq": 0,
  "sim_seconds": 123456,
  "data": {
    "buildings": [ { …Building… } ],
    "vehicles": [ { …Vehicle… } ],
    "shipments": [ { …Shipment… } ],
    "publications": [ { …Publication… } ],
    "contracts": [ { …Contract… } ],
    "ledger_accounts": [ { …LedgerAccount… } ]
  }
}
```

Para `viewport:` las claves de `data` son `cities`, `buildings`, `vehicles`.
Las formas de entidad son las mismas DTO que en REST (`specs/openapi.yaml`).

### 4.2 `patch`

Delta ordenado sobre el estado de una room.

```json
{
  "type": "patch",
  "room": "corp:<uuid>",
  "seq": 7,
  "sim_seconds": 123999,
  "ops": [
    { "op": "upsert", "entity": "vehicle", "id": "<uuid>", "data": { …Vehicle… } },
    { "op": "remove", "entity": "publication", "id": "<uuid>" }
  ]
}
```

`entity` ∈ `building | vehicle | shipment | publication | contract | ledger_account | city`.
`upsert` trae la entidad completa (no parcial). Aplicación idempotente en cliente.

### 4.3 `message`

Evento puntual sin estado persistente (deriva de `outbox.events`).

```json
{
  "type": "message",
  "room": "alerts:<uuid>",
  "event": "acceptance.resolved",
  "sim_seconds": 124100,
  "data": { "acceptance_id": "<uuid>", "status": "served", "contract_id": "<uuid>" }
}
```

### 4.4 `pong` y `error`

```json
{ "type": "pong", "t": 17, "sim_seconds": 124100, "frozen": false }
{ "type": "error", "code": "FORBIDDEN", "message": "…" }
```

`pong` es además la señal de re-sincronización del `SimClock` del cliente.

## 5. Tipos de evento (outbox → messages/patches)

El motor emite en `outbox.events` (`event_type`, `aggregate_type`, `aggregate_id`,
`payload`); el gateway hace polling por cursor (`consumer_name = 'notification_gateway'`)
y los traduce a `patch`/`message` según la room. Catálogo v1:

| `event_type` | Aggregate | Efecto en rooms |
|---|---|---|
| `publication.created` / `publication.cancelled` / `publication.window_closed` / `publication.expired` | publication | `patch` upsert en `corp:` del publicador |
| `acceptance.resolved` | acceptance | `message` en `alerts:` del aceptante; `patch` del contrato en `corp:` de ambas partes si fue servida |
| `contract.confirmed` / `contract.settled` | contract | `patch` upsert en `corp:` de comprador y vendedor; `message` en `alerts:` |
| `delivery.confirmed` | contract | `patch` upsert del contrato en `corp:` de las partes |
| `batch.completed` / `batch.paused` | production_batch | `patch` upsert del batch/edificio en `corp:` del dueño |
| `vehicle.departed` / `vehicle.segment_entered` / `vehicle.arrived` / `vehicle.broken` / `vehicle.repaired` | vehicle | `patch` upsert en `corp:` del dueño y en `viewport:` que contenga su posición |
| `building.status_changed` | building | `patch` upsert en `corp:` del dueño y `viewport:` |
| `city.level_changed` / `city.demand_updated` | city | `patch` upsert en `viewport:`; `message` informativo |
| `sim.frozen` / `sim.resumed` | world | `message` broadcast a todas las conexiones |

El `payload` de cada evento incluye la entidad completa serializada como su DTO REST
(clave `entity`) más campos propios del evento; así el gateway construye los `patch`
sin releer la base de datos.

## 6. Heartbeat y reconexión

- Cliente envía `ping` cada 15–30 s; si no hay `pong` en 2× el intervalo, considera la
  conexión muerta y reconecta con backoff exponencial + jitter (cap 30 s).
- Tras reconectar: `hello` → re-`join` de las rooms previas → snapshots nuevos
  (el estado local se reemplaza por subárbol; convergencia por construcción).
- Durante la ventana de mantenimiento el gateway responde `pong` con `frozen: true` y
  emite `message` `sim.frozen`/`sim.resumed`; el cliente congela su `SimClock`.
