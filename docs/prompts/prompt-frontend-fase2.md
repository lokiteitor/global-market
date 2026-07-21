# Prompt — Actualización del cliente web a la Fase 2/3

> Copiar íntegro como instrucción inicial del agente.

---

Trabajas en el monorepo **Imperio Industrial** (`/home/ddelgado/git/lab/global-market`), un MMO de simulación económica con servidor autoritativo en Go y cliente web en Nuxt 4. Tu tarea es **poner el cliente web al día con el backend**: el servidor creció durante cuatro incrementos (flete, terminales, multi-región, ciudades, insolvencia) y hoy **ningún jugador humano puede tocar esas funciones desde la interfaz**.

## Antes de escribir una línea, lee

- `docs/frontend-architecture-document.md` (FAD v1.1) — **arquitectura vinculante del cliente**: capas, puertos, bridge, reglas duras. No lo contradigas; si crees que debe cambiar, escribe un ADR.
- `docs/api/openapi.yaml` (**contrato v1.5.0**) — la frontera dura. El cliente **jamás** escribe DTOs a mano: se generan con `npm run gen:api` a `frontend/types/api.d.ts`.
- `docs/api/ws-protocol.md` y `docs/adr/ADR-023-*` — protocolo WebSocket (rooms, watermark, resincronización).
- `docs/adr/ADR-019-vista-top-down.md` — vista cenital y **geometría planar en metros de mundo** (SRID 0), no lon/lat.
- `docs/adr/ADR-021-frontend-autonomo.md` — npm sin workspaces, prohibido Tailwind/Bootstrap/Bulma/Vuetify y toda librería de componentes.
- `docs/gdd.md` §5.3.2 (CCRI-Flete), §7.3 (terminales y slots), §5.6 (ciudades y demanda), §5.9/§11.2 (insolvencia y embargo), §9 (mundo multi-región).
- El código existente del cliente, que es tu plantilla de estilo: `frontend/network/` (`world.api.ts`, `market.api.ts`, `fleet.api.ts`, `logistics.api.ts`, `ledger.api.ts`, y el cliente REST con envelope/errores/idempotencia), `frontend/app/stores/` (patrón normalizado con `entity-collection.ts`), `frontend/app/components/play/` (paneles, inspectores, diálogos), `frontend/game/` (motor Phaser: chunks, bridge, capas, overlays), `frontend/shared/` (kernel: `money` con BigInt, `simtime`, `ids`, `i18n`).

## Qué falta exactamente (tu alcance)

**1. CCRI-Flete (GDD §5.3.2)** — el segundo tipo de contrato es invisible en la UI.
- Publicar una **solicitud de flete** (`kind: freight`): producto de la carga, cantidad, tarifa por unidad (`unit_price`), valor declarado, origen y destino, plazo. El escrow que se bloqueará debe mostrarse antes de confirmar.
- Aceptarla como **transportista**: mostrar la garantía que se bloqueará (sobre el valor declarado) y, si es posible, una estimación del trayecto con `POST /logistics/route-plans`.
- Panel de **mis contratos de flete** (`GET /contracts/freight-contracts?role=shipper|carrier`) con estado, fill y plazo.
- El transportista debe **ver y despachar** los cargamentos que transporta aunque no sean suyos (`GET /world/shipments` los incluye desde v1.4.1, con filtro `freight_contract_id`).

**2. Terminales y slots de prioridad (GDD §7.3)**
- Al seleccionar un nodo con terminal: inspector con capacidad de transbordo, cola actual y **slots** (`GET /world/terminals/{id}` y `/slots`).
- Comprar un slot (`POST /world/terminal-slots/{slotId}/purchase`), mostrando precio, `priority_tier` y vigencia; reflejar titularidad propia.

**3. Viaje en vacío (contrato v1.5.0)**
- `POST /world/vehicles/{vehicleId}/reposition`: reposicionar un vehículo `idle` **sin carga** por una ruta propia que empieza donde está. Sin esto, un vehículo entrega y queda varado donde no nace carga nueva. Intégralo en el panel de flota y en el inspector de vehículo (flujo guiado: plan de ruta → crear ruta → reposicionar).

**4. Mundo multi-región (GDD §9)**
- El mundo tiene ahora **9 regiones** (`make worldgen`) con enlaces **rail** y **sea** además de road. Verifica que el `ChunkManager` y la cámara navegan cómodamente entre regiones y **añade la forma de saltar de región** (selector o minimapa; el FAD §15.11/§16.9 describe el minimapa: marco Vue + contenido Phaser sobre `RenderTexture`).
- Los overlays deben distinguir **modo de enlace** (road/rail/sea) por color o trazo, no solo congestión.

**5. Ciudades y demanda (GDD §5.6)**
- Inspector de ciudad enriquecido: nivel, población, índice de suministro, radio de influencia y **curva de demanda por producto** (`GET /world/cities/{id}/demand`: `d0_per_sim_day`, `saturation_factor`, `current_price`, categoría desbloqueada). Es información estratégica de primer orden: indica dónde vender.

**6. Insolvencia y embargo (GDD §5.9/§11.2)**
- Estados visibles y honestos: edificio `damaged`/`abandoned`/`seized` con su `condition_pct`; concesión `delinquent`/`grace`/`reverted` con el vencimiento en sim-time y **aviso cuando se acerca**; lotes `paused_no_fuel`/`paused_no_workers` explicados en el inspector (no un badge mudo).

**7. Mercado más completo**
- Gráfico **OHLC** por producto y región (`GET /market/ohlc`) — el FAD lo pide en Canvas/SVG propio, sin librerías de gráficos.
- Contratos **privados** (`channel: private` con `counterparty_account_id`): publicar dirigido a una contraparte.
- Filtro por región de origen/destino en el tablón (el backend ya lo soporta).

## Reglas duras (del FAD; su incumplimiento es un fallo, no un detalle)

- **Thin client**: el cliente presenta, nunca decide. El estado replicado solo se escribe aplicando respuestas o eventos del servidor. Toda validación real es del servidor; en cliente solo validación *de forma*.
- **Dinero y cantidades** son strings de punto fijo del contrato, manipulados con `shared/money` (BigInt). **Prohibido** `parseFloat`/`Number()` sobre importes.
- **Sim-time** solo se convierte en el `SimClock`; ningún componente hace aritmética de tiempo por su cuenta.
- `shared/` y `domain/` **jamás** importan vue/nuxt/pinia. `game/` no importa `app/components`. Las reglas de ESLint lo verifican.
- **Todos los textos** vía `shared/i18n` (`locales/es.json`). Cero cadenas incrustadas en componentes.
- Estilos: **Sass propio** por capas ITCSS + CSS Modules por componente. Ninguna librería CSS ni de componentes.
- El token de sesión vive **solo en memoria**.
- TypeScript estricto, sin `any`.

## Flujo de trabajo exigido

Por cada funcionalidad: analizar → identificar impactos → **actualizar documentación** → diseñar → implementar → tests → validar → sincronizar documentación. Si detectas una inconsistencia entre el contrato y el backend, **detente, explícala y propón** antes de codear alrededor de ella (ya ocurrió una vez: el contrato decía que `product_id` no aplicaba a fletes y el servidor lo exigía; ganó el servidor y se corrigió el contrato con bump de versión).

## Definition of Done

Desde `frontend/`: `npm run gen:api` sin drift · `npm run typecheck` · `npm run lint` · `npm run test` · `npm run build` — **todos verdes**. Tests de componente para cada flujo nuevo (con stores y API doblados). El smoke Playwright (`npm run test:e2e`, requiere el stack vivo) sigue pasando y **se amplía** con al menos un flujo nuevo. Documentación sincronizada: FAD actualizado con lo implementado y sus simplificaciones conscientes; `docs/guias/desarrollo.md` si cambian convenciones.

## Cómo levantar el entorno

```bash
make dev          # PostgreSQL 18 + observabilidad + migraciones + seed
make worldgen     # mundo multi-región (necesario para probar rail/sea y regiones)
make backend      # gateway :8080 + engine :8081   [otra terminal]
make bots         # 5 arquetipos dando vida a la economía  [otra terminal]
make frontend     # cliente en :3000                [otra terminal]
```

Login de desarrollo: `Demo` / `demo-secret-dev` (y `Norte Trading` / `norte-secret-dev`). El cliente jugable vive en `http://localhost:3000/play`.

**Consejo de verificación**: con `make bots` corriendo, el tablón se llena solo y los camiones se mueven — es la mejor forma de ver tu UI contra datos reales en movimiento.
