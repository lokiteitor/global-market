# Bots — Bot Orchestration Service

**Estado:** documento vivo. Describe el funcionamiento real de `backend/bots` tal y como
está implementado. Complementa a `docs/desarrollo.md` (ADR-IMPL-11) y al GDD §13.1
(igualdad de API). Si cambias el comportamiento de los bots, actualiza este documento
en el mismo cambio.

## 1. Qué es y qué garantiza

El Bot Orchestration Service es un único proceso Node (TypeScript, sin dependencias de
runtime — `fetch` global de Node 22) que opera una población de cuentas `bot` contra el
gateway. Su regla fundacional es **ADR-IMPL-11: igualdad de API literal** — los bots
consumen EXCLUSIVAMENTE la API pública REST (`specs/openapi.yaml` v1.1.0), sin acceso a
la base de datos ni atajos internos. Todo lo que un bot hace lo puede hacer un jugador
humano, y viceversa: los bots son a la vez población económica del mundo y verificación
continua de que la API pública es suficiente para jugar.

Corolarios de diseño que se mantienen en todo el paquete:

- **Sin estado persistente propio.** Un bot no tiene base de datos: reconstruye su
  visión del mundo en cada tick leyendo la API (patrón *estado deseado*, ver §4). La
  única memoria en proceso es el coste medio de compra del arbitrajista (§5.3), que se
  pierde al reiniciar sin consecuencias (cae al `base_price` de catálogo como fallback).
- **Decisión pura, ejecución aparte.** Cada arquetipo separa `observe*` (solo lecturas
  HTTP) de `decide*` (función pura estado observable → acción, testeable con estados
  sintéticos sin red). El orquestador ejecuta la acción resultante.
- **Máximo 1 escritura por tick y por bot.** Un tick emite a lo sumo un comando POST.
  Esto acota el ritmo de mutación del mundo y hace el log de decisiones legible.
- **Aritmética entera estricta.** Dinero y stock viajan como strings decimales (JSON) y
  se operan como `bigint` (`toBig` valida con regex; `mulDiv` trunca). Los multiplicadores
  de precio son fracciones enteras (p. ej. ×1.1 = `×11n/10n`) — jamás floats.

## 2. Estructura del paquete

```
backend/bots/src/
  config.ts        # GATEWAY_URL, TICK_MS, jitter, DELIVERY_SIM_SECONDS, roster BOTS
  types.ts         # DTOs mínimos de la API pública que los bots consumen
  client.ts        # ApiClient: login, re-login en 401, backoff 429/503, Idempotency-Key
  catalog.ts       # catálogos estáticos (productos, tipos, recetas, regiones, ciudades)
  actions.ts       # unión BotAction + utilidades puras (toBig, mulDiv, geometría)
  orchestrator.ts  # bucle principal: tick → observar → decidir → ejecutar (main)
  archetypes/
    producer.ts     # extractor primario (mina de hierro)
    transformer.ts  # industria de transformación (alto horno)
    arbitrageur.ts  # comerciante sobre el tablón global
backend/bots/test/  # tests de las funciones de decisión (node:test + tsx)
```

## 3. El orquestador (`orchestrator.ts`)

Un `setInterval` global de `TICK_MS` (5000 ms reales por defecto, variable de entorno
`TICK_MS`) dispara, en cada tick y para cada bot, un paso con **jitter aleatorio**
individual de hasta `TICK_JITTER_MS` (1500 ms) para que los bots no golpeen la API en
tromba sincronizada.

Cada paso (`tickBot`) hace:

1. **Guard de reentrada**: si el paso anterior del mismo bot sigue en curso (`busy`),
   el tick se omite con log — nunca hay dos pasos concurrentes del mismo bot.
2. **Arranque perezoso**: en el primer tick (o tras un fallo de arranque) hace login y
   carga el catálogo estático una sola vez (§3.2).
3. **Observar → decidir**: llama al par `observe*`/`decide*` de su arquetipo.
4. **Ejecutar**: si la acción no es `none`, la traduce a su POST correspondiente
   (`execute`). Toda decisión, incluida `none` con su motivo, queda en el log con
   timestamp y nombre del bot.
5. **Tolerancia por acción**: cualquier error (red, 4xx/5xx, catálogo incompleto) se
   loguea y el bot lo reintenta de forma natural en el próximo tick al re-observar el
   estado. Un bot roto no afecta a los demás.

El proceso termina limpiamente con SIGINT/SIGTERM.

### 3.1 Acciones posibles (`actions.ts`)

La unión `BotAction` es el vocabulario completo de escritura de los bots:

| Acción | Endpoint | Notas |
|---|---|---|
| `none` | — | lleva `reason` para el log |
| `create_concession` | `POST /world/concessions` | parcela GeoJSON |
| `create_building` | `POST /world/buildings` | huella dentro de la concesión |
| `queue_batches` | `POST /world/buildings/{id}/production-batches` | receta + nº de lotes |
| `publish_sell` | `POST /contracts/publications` | `kind: sell`, `origin_node_id` |
| `publish_buy` | `POST /contracts/publications` | `kind: buy`, `destination_node_id` |
| `accept_publication` | `POST /contracts/publications/{id}/acceptances` | cantidad |

Toda publicación de los bots usa `min_lot: "1"` y pacta
`delivery_sim_seconds = 172 800` (2 días sim = 2 h reales al ratio 24×,
`DELIVERY_SIM_SECONDS` en `config.ts`).

### 3.2 Catálogo estático (`catalog.ts`)

Al arrancar, cada bot carga una vez productos, tipos de edificio, recetas, regiones y
ciudades, indexados por `code` (productos/tipos/recetas) o por `name`
(regiones/ciudades). Los arquetipos resuelven ahí sus referencias (`iron_ore`,
`blast_furnace`, `smelt_steel`, `Ferrópolis`, …) con `required()`, que falla con mensaje
claro si el seed no contiene lo esperado. El catálogo no se refresca: se asume estático
durante la vida del proceso.

### 3.3 Cliente HTTP (`client.ts`)

`ApiClient` implementa la disciplina de red que el gateway espera de cualquier cliente:

- **Sesión bearer**: `POST /auth/sessions` con `account_name`/`secret`; ante un 401
  posterior hace re-login transparente una única vez y reintenta.
- **Backoff en 429/503**: respeta la cabecera `Retry-After` si viene; si no, espera
  2 s × nº de intento, con máximo 2 reintentos. Nunca reintenta en tromba.
- **Idempotency-Key en todo POST**: un `crypto.randomUUID()` generado UNA vez por
  comando lógico y reutilizado en los reintentos internos del mismo comando — el
  gateway deduplica, así que un reintento tras timeout no duplica la escritura.
- Envoltorios `{ data, meta }` / `{ error }` del openapi; los errores se materializan
  como `ApiError` con status y código.

## 4. Patrón de los arquetipos: estado deseado por prioridades

Los dos arquetipos industriales (producer, transformer) no siguen un guion secuencial:
cada tick re-derivan **qué les falta para su estado deseado** y ejecutan la acción de
mayor prioridad pendiente. Esto los hace auto-reparadores: si una publicación expira,
un lote termina o el proceso se reinicia, la siguiente observación lo detecta y la
cadena de prioridades vuelve a actuar. Las condiciones de publicación comprueban que no
exista ya una publicación propia equivalente en el tablón (evita duplicar órdenes) y
que el bot conozca ya su nodo logístico.

Nota de implementación: la API no filtra nodos de red por edificio, así que `observe*`
lista los nodos de la región del edificio (`GET /logistics/network/nodes?region_id=…`)
y busca el que tenga su `building_id`. El stock libre vendible se lee del ledger
(`GET /ledger/accounts?kind=stock_free&product_id=…`, filtrando por el almacén propio),
mientras que los insumos físicos se leen del inventario del edificio
(`GET /world/buildings/{id}/inventory`).

## 5. Los tres arquetipos

El roster vive en `config.ts` y corresponde 1:1 con las cuentas `bot` del seed
(`backend/seeds/seed_world.sql`):

| Cuenta | Secret | Arquetipo |
|---|---|---|
| Bot Minero Norte | `botmineronorte` | producer |
| Bot Fundición Este | `botfundicioneste` | transformer |
| Bot Arbitraje Sur | `botarbitrajesur` | arbitrageur |

### 5.1 Producer — extractor primario (`archetypes/producer.ts`)

Estado deseado: una concesión sobre un yacimiento de `iron_ore`, una `iron_mine`
operativa en ella, cola `mine_iron` viva, carbón para el combustible y venta del
excedente. Prioridades de `decideProducer`, de mayor a menor:

1. **Sin concesión** → concesión cuadrada de ~0.01° de lado centrada en el primer
   yacimiento disponible de `iron_ore` (`GET /world/resource-deposits?only_available=true`).
2. **Sin mina** → construir `iron_mine` (huella ~0.004° de lado) en el centro de la parcela.
3. **Mina no operativa** → esperar (`none`).
4. **Combustible bajo** (`coal` en inventario < 10) y sin `buy` propio abierto →
   publicar `buy` de 20 `coal` a `base_price × 1.1` hacia su nodo.
5. **Cola muerta** (0 lotes `queued`/`running`) → encolar `mine_iron` ×5.
6. **Excedente** (`iron_ore` libre > 50) y sin `sell` propio abierto → publicar `sell`
   de 50 uds a `base_price × 0.9` desde su nodo.

Parámetros en `PRODUCER_PARAMS`.

### 5.2 Transformer — industria (`archetypes/transformer.ts`)

Estado deseado: concesión + `blast_furnace` desplazado 0.03° al este del centro de
Ferrópolis (para no pisar el casco urbano), insumos comprados, cola `smelt_steel` viva
y venta de `steel_ingot`. La receta consume por lote 8 `iron_ore` + 4 `coal` de insumo
+ 2 `coal` de combustible. Prioridades de `decideTransformer`:

1. **Sin concesión** → concesión junto a Ferrópolis.
2. **Sin alto horno** → construir `blast_furnace` en la parcela.
3. **No operativo** → esperar.
4. **`iron_ore` < 16** (2 lotes) y sin `buy` propio → `buy` 40 uds a `base × 1.1`.
5. **`coal` < 12** (2 lotes) y sin `buy` propio → `buy` 20 uds a `base × 1.1`.
6. **Cola muerta con insumos para ≥ 1 lote** → encolar `smelt_steel` ×5.
7. **`steel_ingot` libre > 20** y sin `sell` propio → `sell` 20 uds a `base × 1.05`.

Parámetros en `TRANSFORMER_PARAMS`.

### 5.3 Arbitrageur — comerciante (`archetypes/arbitrageur.ts`)

No produce: observa el tablón global completo (sells ordenados por precio ascendente,
buys descendente) y la demanda de todas las ciudades del catálogo
(`GET /world/cities/{id}/demand`). Dos reglas, en este orden:

1. **Realizar beneficio** — acepta la primera publicación `buy` ajena cuyo precio
   supere `1.2 ×` su coste medio de compra del producto (fallback: `base_price`),
   por `min(cantidad restante, stock propio)`. Al aceptar un `buy` el vendedor entrega
   físicamente (auto-despacho del motor, ADR-IMPL-13).
2. **Comprar barato** — acepta la primera publicación `sell` ajena cuyo precio esté por
   debajo del `0.7 ×` del mejor `current_price` de demanda urbana para ese producto
   (retirada in situ: el stock queda suyo en el almacén de origen, semántica CCRI v1).

**Gestión de riesgo**: nunca compromete más del 30 % de su cash en una operación de
compra (`maxSpend = cash × 3/10`), y respeta `min_lot` de la publicación.

**Memoria de coste** (`updateAvgCost`): media ponderada del precio de compra por
producto, actualizada de forma optimista al aceptar (si el sorteo no le adjudica, el
siguiente refresh de stock lo corrige de facto). Es la única pieza de estado en memoria
de todo el servicio.

Las comparaciones de precio usan la equivalencia entera (`precio×10 > coste×12` para
1.2×, `precio×10 < demanda×7` para 0.7×) — parámetros en `ARBITRAGEUR_PARAMS`.

## 6. Interacción con el resto del sistema

- Los bots publican y aceptan bajo la **semántica CCRI v1** común: las garantías quedan
  bloqueadas en cuentas espejo desde la publicación/aceptación, la ventana de sorteo
  dura 45 s wall y las entregas de `buy` las auto-despacha el motor (ADR-IMPL-13). Los
  bots no gestionan camiones ni rutas: confían en el auto-despacho.
- El circuito económico que forman entre los tres es el ciclo mínimo del mundo:
  el producer vende `iron_ore` barato y compra `coal`; el transformer compra ambos y
  vende `steel_ingot`; el arbitrageur compra donde hay descuento frente a la demanda
  urbana y revende con margen. `make verify` se apoya en este circuito para el smoke
  e2e.

## 7. Ejecución y configuración

```bash
make bots-install && make bots-run    # local, contra el gateway en :8080
make stack-up                          # perfil "full": los bots corren contenedorizados
```

Variables de entorno:

| Variable | Default | Efecto |
|---|---|---|
| `GATEWAY_URL` | `http://localhost:8080` | base de la API; añade `/api/v1` si falta |
| `TICK_MS` | `5000` | periodo del bucle (ms reales) |

El resto de parámetros (jitter, plazos, umbrales por arquetipo) son constantes en
`config.ts` y en los `*_PARAMS` de cada arquetipo.

## 8. Tests

Las funciones de decisión se testean sin red, con observaciones sintéticas:

```bash
cd backend/bots && node --import tsx --test test/producer.test.ts
cd backend/bots && node --import tsx --test test/transformer.test.ts
cd backend/bots && node --import tsx --test test/arbitrageur.test.ts
```

Cualquier cambio en una cadena de prioridades o en un umbral debe reflejarse en su test
y en las tablas de §5 de este documento.
