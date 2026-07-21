# Protocolo WebSocket del Notification/Event Gateway — referencia para integradores

**Protocolo v1** · Decidido en [ADR-023](../adr/ADR-023-notification-gateway-ws.md) (el ADR decide; este documento explica el *cómo*) · Complementa el contrato REST [`openapi.yaml`](openapi.yaml) (v1.3.0), que declara el WS explícitamente fuera de su alcance.

Audiencia: cualquier cliente de la API pública — el cliente web (FAD ADR-FE-004, `GatewayTransportAdapter`) y el SDK de bots (`backend/pkg/botsdk`). Ambos hablan **exactamente** este protocolo; no hay canales privilegiados.

## 1. Qué es (y qué no es)

El Gateway distribuye por WebSocket los **eventos de dominio del outbox** (`outbox.events`, orden total por `seq`) a las corporaciones interesadas, agrupados en *rooms* (áreas de interés).

- **No hay snapshots por el socket ni replay histórico.** El estado inicial se obtiene **por REST** (pull); el socket entrega *deltas* a partir de un `watermark`. Reconectar implica re-consultar por REST.
- **El tablón global es pull** (C10 del FAD): se consulta con `GET /contracts/board`; el socket solo notifica los eventos que afectan a tu corporación.
- Entrega **at-least-once por conexión** desde el watermark; el cliente debe ser idempotente y detectar huecos por `seq` (ver §6).
- Los importes de dinero y las cantidades de stock viajan en los payloads **como strings** (invariante del contrato REST) — nunca floats.

## 2. Transporte

| Aspecto | Valor |
|---|---|
| Endpoint | `GET /api/v1/ws` (upgrade HTTP servido por el proceso *gateway*) |
| Frames | Mensajes de **texto** con un objeto **JSON** cada uno |
| Autenticación | **En banda**, con el primer frame (`auth`) — los navegadores no pueden fijar cabeceras en el upgrade |
| Origin | Sin cabecera `Origin` (clientes no-navegador, p. ej. el SDK) se acepta siempre; navegadores: mismo origen, o los patrones de `II_WS_ALLOWED_ORIGINS` |
| Librería del servidor | `github.com/coder/websocket` (dato informativo; el protocolo es JSON estándar) |

## 3. Ciclo de vida de la conexión

```
cliente                                servidor
  │ ── upgrade GET /api/v1/ws ──────────► │
  │ ── {"type":"auth","token":…} ───────► │   (≤ 5 s o cierre 4401)
  │ ◄── {"type":"auth_ok",…} ──────────── │
  │ ── {"type":"join","room":"corp"} ───► │
  │ ◄── {"type":"joined","watermark":N} ─ │
  │ … bootstrap del estado por REST …     │   (todo evento posterior llega con seq > N)
  │ ◄── {"type":"event",…} ────────────── │   (flujo de deltas)
```

1. **Upgrade** del socket.
2. **`auth` obligatorio como primer frame**, dentro del plazo (`II_WS_AUTH_TIMEOUT`, default 5 s). Si no llega a tiempo o el token es inválido, el servidor cierra con el código **4401**.
3. El servidor responde **`auth_ok`** con la cuenta y el sim-time actual.
4. El cliente hace **`join`** de la room `corp` y recibe **`joined`** con el `watermark` (último `seq` del outbox ya despachado).
5. El cliente **bootstrapea su estado por REST** sabiendo que todo evento posterior llegará por el socket con `seq > watermark`.
6. Fluyen frames **`event`** hasta el cierre.

**Keepalive.** El servidor emite ping WS *a nivel de protocolo* (no un frame JSON) cada `II_WS_PING_INTERVAL` (default 20 s) y cierra tras 2 fallos consecutivos. Además existe un ping/pong **de aplicación** opcional (frames `ping`/`pong` con `nonce` de eco) por si el cliente quiere medir la vuelta completa.

**Consumidor lento.** Cada conexión tiene un buffer de envío acotado (`II_WS_SEND_BUFFER` frames, default 256). Si se llena, el servidor cierra con **1013** (*Try Again Later*): el cliente reconecta y re-sincroniza por REST. Nunca se descartan eventos en silencio con la conexión abierta.

**Límite por cuenta.** Máximo `II_WS_MAX_CONNS_PER_ACCOUNT` conexiones simultáneas (default 4); el exceso recibe un frame `error` con código `TOO_MANY_CONNECTIONS` y cierre.

## 4. Frames cliente → servidor

| Frame | Semántica |
|---|---|
| `auth` | Primer frame obligatorio; autentica la sesión con el bearer de `POST /auth/login` |
| `join` | Suscripción a una room (v1: solo `corp`) |
| `leave` | Baja de una room |
| `ping` | Keepalive de aplicación opcional; el servidor devuelve `pong` con el mismo `nonce` |

```json
{"type":"auth","token":"b0c1d2e3-..."}
```

```json
{"type":"join","room":"corp"}
```

```json
{"type":"leave","room":"corp"}
```

```json
{"type":"ping","nonce":"427"}
```

Un frame que no sea JSON válido, con `type` desconocido o sin sus campos obligatorios recibe un `error` con código `BAD_FRAME`.

## 5. Frames servidor → cliente

**`auth_ok`** — sesión validada:

```json
{
  "type": "auth_ok",
  "account_id": "0197a3f2-7c31-7d10-a5b2-3f8e9c1d2e4f",
  "sim_time_seconds": 31104000
}
```

**`joined`** — suscripción confirmada, con el watermark:

```json
{"type": "joined", "room": "corp", "watermark": 18234}
```

**`event`** — evento de dominio del outbox. El `payload` viaja **tal cual se emitió** (dinero/stock como strings); su forma por `event_type` está documentada en `documentacion_base_de_datos.md`:

```json
{
  "type": "event",
  "room": "corp",
  "seq": 18240,
  "event_id": "0197a4c8-1f02-7e33-b0aa-9e5d6f7a8b9c",
  "event_type": "contract.settled",
  "sim_time": 31190400,
  "aggregate_type": "contract",
  "aggregate_id": "0197a4b0-55e1-7c44-8d21-1a2b3c4d5e6f",
  "payload": {
    "contract_id": "0197a4b0-55e1-7c44-8d21-1a2b3c4d5e6f",
    "product_id": "0197a001-0000-7000-8000-000000000021",
    "destination_region_id": "0197a001-0000-7000-8000-000000000001",
    "unit_price": "120",
    "quantity_agreed": "500",
    "quantity_delivered": "500",
    "fill_bp": 10000,
    "settled_at_sim": 31190400,
    "status": "settled"
  }
}
```

**`pong`** — respuesta al ping de aplicación:

```json
{"type": "pong", "nonce": "427"}
```

**`error`** — error de protocolo (no cierra necesariamente la conexión):

```json
{"type": "error", "code": "UNSUPPORTED_ROOM", "message": "la room \"viewport:...\" no existe en el protocolo v1"}
```

### Códigos del frame `error`

| `code` | Causa |
|---|---|
| `BAD_FRAME` | JSON inválido, `type` desconocido o campos obligatorios ausentes |
| `UNAUTHORIZED` | Token inválido o sesión expirada en el `auth` en banda |
| `UNSUPPORTED_ROOM` | `join`/`leave` de una room que el protocolo v1 no define |
| `TOO_MANY_CONNECTIONS` | La cuenta superó el máximo de conexiones simultáneas |
| `INTERNAL` | Fallo interno procesando el frame |

### Códigos de cierre WS

| Código | Significado |
|---|---|
| `4401` | Autenticación requerida: el frame `auth` no llegó a tiempo o el token es inválido |
| `1013` | Consumidor lento: buffer de envío lleno; reconecta y re-sincroniza por REST |
| `1000` | Cierre normal |

## 6. Watermark, huecos y re-sincronización

- El `watermark` del frame `joined` es el **último `seq` de outbox ya despachado** por el fan-out. Contrato: tras el `joined`, todo evento de tu interés llega con `seq > watermark`, en orden creciente.
- **Patrón de arranque**: conectar → `join` → leer `watermark` → **bootstrap por REST** (tablón, contratos propios, flota, etc.) → aplicar los `event` que lleguen. Un evento cuyo efecto ya esté reflejado en el bootstrap debe ser inocuo (idempotencia del lado cliente, FAD P6).
- **Detección de huecos**: si el cliente observa un salto en `seq` mayor del esperado *dentro de su interés*, no puede recuperar lo perdido por el socket (no hay replay): marca el estado como *stale* y **re-consulta por REST**. Ojo: los `seq` son globales del outbox — entre dos eventos de tu corporación puede haber `seq` intermedios de otras corporaciones; el hueco solo es significativo tras una desconexión o un `1013`.
- **Reconexión**: repetir el ciclo completo (`auth` → `join`); el `joined` nuevo trae un watermark nuevo y el cliente re-sincroniza por REST. El cliente WS del SDK (`botsdk.WSConn`) automatiza el re-join y publica el watermark nuevo en `Reconnected()`.
- La entrega es **at-least-once por conexión**: un mismo evento puede llegar repetido en escenarios de borde; deduplica por `event_id` o por efecto idempotente.

## 7. Rooms y enrutado por interés

El protocolo v1 define **una única room: `corp`** — los eventos de la propia corporación. `viewport:<bbox>` y `alerts` son extensiones **aditivas** futuras (el frame `join` ya las admite sintácticamente; hoy responden `UNSUPPORTED_ROOM`).

El fan-out lo alimenta el consumidor de outbox **`notification_gateway`** (proceso *gateway*). Enrutado v1:

| `event_type` | Rooms destino (corp de…) |
|---|---|
| `publication.created` / `.cancelled` / `.expired` | el publicador |
| `acceptance.registered` / `.resolved` | el aceptante **y** el publicador |
| `contract.confirmed` / `.delivered` / `.settled` / `.expired_undelivered` | el comprador **y** el vendedor |
| `shipment.created` / `.dispatched` / `.released` | el dueño del cargamento |
| `shipment.arrived` | el dueño **y** el comprador del contrato |
| `vehicle.purchased` / `.updated` / `.repositioned` / `.arrived` / `.broken` / `.stranded` | el dueño |
| `building.created` / `.updated` / `.upgraded` / `.constructed` | el dueño |
| `batch.queued` / `.completed` / `.paused` / `.cancelled` | el dueño |
| `concession.granted` / `.renewed` | el titular |
| `concession.transferred` | ambas partes del traspaso |

Cuando el payload no contiene las cuentas necesarias, el router las resuelve con lecturas puntuales a la BD (caché con TTL corto: la titularidad puede cambiar). El cursor del consumidor **avanza siempre** tras el fan-out: los sockets son efímeros y un cliente ausente bootstrapea por REST — la entrega hacia clientes no es (ni debe ser) exactly-once.

## 8. Límites y configuración del servidor

Variables `II_WS_*` (12-factor, leídas en `notify.OptionsFromEnv`):

| Variable | Default | Efecto |
|---|---|---|
| `II_WS_AUTH_TIMEOUT` | `5s` | Plazo del frame `auth` tras el upgrade (cierre `4401` al vencer) |
| `II_WS_PING_INTERVAL` | `20s` | Periodo del ping WS de protocolo (cierre tras 2 fallos) |
| `II_WS_SEND_BUFFER` | `256` | Frames de buffer de envío por conexión (lleno ⇒ cierre `1013`) |
| `II_WS_MAX_CONNS_PER_ACCOUNT` | `4` | Conexiones simultáneas por cuenta (`TOO_MANY_CONNECTIONS`) |
| `II_WS_ROUTER_INTERVAL` | `1s` | Polling del consumidor outbox en reposo (los lotes llenos se encadenan sin esperar) |
| `II_WS_ROUTE_CACHE_TTL` | `30s` | TTL de la caché de lookups de enrutado (0 la desactiva) |
| `II_WS_ALLOWED_ORIGINS` | *(vacío)* | Patrones de origen cross-origin de navegador permitidos (vacío = solo mismo origen) |

Observabilidad del Gateway (Prometheus, proceso gateway): `ii_ws_connections`, `ii_ws_frames_sent_total`, `ii_ws_slow_client_closes_total`, `ii_ws_events_routed_total`.

El rate limiting HTTP estándar (429 + `Retry-After`) aplica al upgrade como a cualquier petición; dentro del socket no hay rate limit adicional en v1 (los frames del cliente son de control, no comandos de juego — **todo comando va por REST**).

## 9. Clientes de referencia

- **SDK de bots** (`backend/pkg/botsdk`): `Client.Connect` → `WSConn` con `JoinCorp` (devuelve el watermark), `Events()` (canal de eventos tipados), `Reconnected()` (watermark nuevo tras re-join automático) y `Ping`. Es la implementación Go canónica del protocolo.
- **Cliente web** (FAD ADR-FE-004): `GatewayTransportAdapter` traduce este protocolo al modelo del puerto `NetworkTransport` (room/snapshot/patch/message) — los "snapshots" los sintetiza la ACL desde REST; cada frame `event` es un *patch*.
