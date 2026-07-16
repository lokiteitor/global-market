# Imperio Industrial — Cliente Web (frontend)

Thin client del MMO de simulación económica (Nuxt 4 + Vue 3 + TypeScript estricto + Pinia + Phaser 3 + Sass). El cliente **nunca decide reglas económicas** (P1): envía intenciones por REST/WS y refleja el estado autoritativo del servidor. Referencia de arquitectura: `../docs/frontend-architecture-document.md` (FAD).

## Comandos

```sh
npm install        # dependencias (npm sin workspaces)
npm run dev        # dev server en :3000 (proxy /api y /ws → gateway :8080)
npm run test       # vitest (unit + componentes, sin backend)
npm run build      # build de producción (SSR híbrido; /play es client-only)
npm run typecheck  # vue-tsc estricto
```

No requiere backend para desarrollar UI/tests: la suite usa dobles del puerto de red. El flujo completo contra el gateway (:8080) se prueba en la fase de integración del proyecto.

## Arquitectura de carpetas (mapa al FAD)

```
app/
  lib/kernel/     # capa 1 — núcleo puro: money (BigInt, punto fijo), simtime
                  #   (ratio 24x, formato A-DDD-HH:MM), ids, result, projection,
                  #   event-bus tipado (§19). Sin dependencias de framework.
  lib/api/        # capa 2 — contrato REST (specs/openapi.yaml v1.1.0): DTOs
                  #   (types/requests), HttpClient + RestApi tipado; envoltura
                  #   {data,meta}/{error}; Idempotency-Key automática en comandos.
  lib/net/        # capa 2 — tiempo real: puerto NetworkTransport (§4.4),
                  #   GatewayTransportAdapter (specs/ws-protocol.md: hello/join/
                  #   ping, reconexión con backoff, re-join), SimClock (ÚNICO
                  #   punto de conversión sim↔wall, P5) y pipeline de sync
                  #   (snapshot/patch/message → stores, dedup por seq).
  stores/         # capa 3 — Pinia, una store por bounded context (§9/§20):
                  #   session, sim, world, cities, buildings, fleet, shipments,
                  #   market, finance, notifications, ui. El estado replicado
                  #   solo se escribe vía acciones apply* idempotentes.
  game/           # capa 4 — mundo Phaser tras el puerto WorldRenderer (§11):
                  #   escenas World/Overlay, bridge stores→render, cinemática.
                  #   No importa Vue ni stores fuera del bridge (O2).
  components/     # capa 5 — UI Vue: base/ (UI kit propio en Sass, sin
                  #   frameworks CSS), hud/ (TopBar/SideBar/BottomBar/Inspector),
                  #   panels/ (mercado, construcción, producción, flota,
                  #   finanzas, ciudades), game/GameCanvasHost.vue (ÚNICO
                  #   componente autorizado a tocar game/).
  composables/    # capa 5 — useApi, useSession, useSimClock, useConnection,
                  #   useRooms, useOwnership (Observable vs Comandable, §5.3)…
  pages/          # capa 6 — rutas: / (portal), /login, /lobby, /settings y
                  #   /play (client-only; ensambla HUD + canvas + red).
  layouts/        # default (portal), auth (login), game (grid HUD §15.3).
  middleware/     # auth.ts — guard de sesión (sin token → /login).
  plugins/        # 02.network.client (composición DI de la capa de red),
                  #   03.simclock.client (ticker de la vista del reloj).
  assets/styles/  # ITCSS/7-1: settings (tokens --ii-*), generic, elements,
                  #   objects, themes (oscuro).
tests/            # vitest: kernel, net (transporte/simclock/sync con dobles),
                  #   game (bridge/cinemática con renderer fake), ui (happy-dom).
```

Comunicación entre capas: `game/` y `components/` **leen** estado por stores y **emiten** intenciones por el event bus tipado (`lib/kernel/event-bus.ts`); la red escribe en las stores dueñas vía el pipeline de sync. Dinero y cantidades son *strings de punto fijo* manejados solo con los helpers BigInt de `lib/kernel/money.ts` (jamás `parseFloat`/`Number`).

## Simplificaciones v1 (aceptadas, documentadas en el código)

- **Proyección top-down** del mundo (lon/lat → px con escala lineal, `lib/kernel/projection.ts`); la vista isométrica queda para FE-6.
- **Gráficos Phaser procedurales** (`Graphics`/`generateTexture`): sin atlases ni assets binarios.
- **Token de sesión en memoria + sessionStorage** (solo dev); el guard de auth decide solo en cliente. Endurecimiento (cookies httpOnly/BFF) pospuesto.
- **Cinemática de vehículos derivada**: el DTO v1.1.0 no trae inicio/duración de segmento; se derivan de `updated_at_sim`, la geometría del link y `base_speed_kmh·congestion_ema` (ver `game/bridge.ts`).
- **Parcela/footprint por inputs numéricos** en BuildPanel (el picking sobre el mapa llega con FE-6).
- **OHLC solo tabla** (sin gráfico Canvas).
- **Panel de gestión acoplado en posición fija** sobre el canvas; el WindowManager (paneles flotantes/redimensionables, §15.5) queda pendiente.
- **Sin store de red logística** (nodos/enlaces) ni de concesiones/rutas: BuildPanel/FleetPanel hacen pull local y el bridge degrada a `position.location`; el `getNetwork` del bridge se cableará cuando exista la store.

## Puertos (docs/desarrollo.md)

- Frontend dev: `:3000` (Nuxt). Proxy dev: `/api/*` y `/ws` → gateway `:8080`.
- En producción el edge (Caddy) enruta `/api` y `/ws`; el cliente usa rutas relativas (`runtimeConfig.public.apiBase = /api/v1`, `wsPath = /ws`).
