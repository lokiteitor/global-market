# ADR-023 — Protocolo del Notification/Event Gateway (WebSocket)

| Campo | Valor |
|---|---|
| **ID** | ADR-023 |
| **Fecha** | 2026-07-17 |
| **Estado** | Aceptado |
| **Resuelve** | Pregunta abierta nº 1 del FAD (§27.5) y la elección de librería WS diferida en ADR-017 §5 |

## Contexto

El SAD define un Notification/Event Gateway que distribuye eventos por WebSocket con *interest management*; el contrato OpenAPI lo declara explícitamente fuera de su alcance. El FAD (ADR-FE-004) exige acordar el protocolo antes de su Fase 4, y el SDK de bots (ADR-024) lo necesita ya. El motor es event-driven con outbox: los eventos existen con orden total (`outbox.events.seq`).

## Decisión

### Transporte y librería

1. Endpoint: `GET /api/v1/ws` (upgrade HTTP) servido por el **gateway Go**.
2. Librería: **`github.com/coder/websocket`** — mínima, context-aware, mantenida activamente; se añade a las dependencias permitidas de ADR-017.

### Protocolo v1 (frames JSON de texto)

**Cliente → servidor**

| Frame | Semántica |
|---|---|
| `{"type":"auth","token":"<bearer>"}` | **Primer frame obligatorio** (≤5 s o cierre `4401`). Los navegadores no pueden fijar cabeceras en el upgrade; la autenticación va en banda. |
| `{"type":"join","room":"corp"}` | Suscripción a una room. **v1 define una única room: `corp`** (los eventos de la propia corporación). `viewport:<bbox>` y `alerts` son extensiones aditivas futuras. |
| `{"type":"leave","room":"corp"}` | Baja de la room. |
| `{"type":"ping","nonce":"..."}` | Keepalive de aplicación opcional. |

**Servidor → cliente**

| Frame | Semántica |
|---|---|
| `{"type":"auth_ok","account_id":uuid,"sim_time_seconds":N}` | Sesión validada. |
| `{"type":"joined","room":"corp","watermark":N}` | `watermark` = último `seq` de outbox ya despachado. El cliente hace **bootstrap por REST** y sabe que todo evento posterior llegará por el socket con `seq > watermark`. |
| `{"type":"event","room":"corp","seq":N,"event_id":uuid,"event_type":"contract.settled","sim_time":N,"aggregate_type":"contract","aggregate_id":uuid,"payload":{...}}` | Evento de dominio; `payload` es el del outbox tal cual (dinero/stock como strings). |
| `{"type":"pong","nonce":"..."}` / `{"type":"error","code","message"}` | Respuestas de control. |

Ping WS a nivel de protocolo cada ~20 s; cierre ante 2 fallos. Consumidor lento: buffer acotado por conexión; si se llena, cierre `1013` (el cliente re-sincroniza por REST al reconectar).

### Modelo de sincronización (coherente con FAD ADR-FE-004)

- **No hay snapshots por el socket ni replay histórico**: el estado inicial se obtiene por REST (pull); el socket entrega deltas desde el `watermark`. La ACL del cliente (`GatewayTransportAdapter`) sintetiza sus "snapshots" desde REST — exactamente el rol de una ACL.
- **Entrega at-least-once por conexión** desde el watermark; el cliente detecta huecos por `seq` creciente y re-sincroniza vía REST (idempotencia del lado cliente, FAD P6).
- El fan-out lo alimenta el consumidor outbox **`notification_gateway`** (en el proceso gateway). Su cursor **avanza siempre** tras el fan-out (los sockets son efímeros; un cliente ausente bootstrapea por REST — no es exactly-once hacia clientes, y no debe serlo).

### Enrutado por interés (room `corp`)

| `event_type` | Rooms destino |
|---|---|
| `publication.*` | corp del publicador |
| `acceptance.*` | corp del aceptante y del publicador |
| `contract.*` | corps de comprador y vendedor |
| `shipment.*` | corp del dueño (+ comprador en `shipment.arrived`) |
| `vehicle.*` / `building.*` / `batch.*` | corp del dueño |
| `concession.*` | corp del titular |

Cuando el payload no contiene las cuentas necesarias, el router las resuelve con lecturas puntuales a la BD (el gateway ya tiene acceso de lectura).

> **Nota (2026-07-21):** esta tabla es la del momento de la decisión. Los incrementos
> posteriores la ampliaron aditivamente sin alterar el protocolo: `freight.*` (cargador y
> transportista, CCRI-Flete), `shipment.at_terminal` (dueño), `power.curtailed` y
> `power_line.abandoned` (ADR-025). La referencia viva del enrutado es
> [`ws-protocol.md` §7](../api/ws-protocol.md) y `backend/internal/notify/router.go`
> (`RoutedEventTypes`).

## Consecuencias

- (+) Protocolo mínimo, extensible aditivamente (rooms nuevas), coherente con "el tablón es pull" (C10 del FAD) y con el motor event-driven.
- (+) El mismo protocolo sirve a cliente web y SDK de bots (igualdad literal, ADR-010).
- (−) Sin replay histórico: reconectar implica re-pull REST; asumido (es el patrón snapshot+deltas del propio backend).
- El FAD §4.4/§12 se considera **acordado**; su nota "requiere acordar el protocolo" queda satisfecha por este ADR.
