# Guía de desarrollo — Imperio Industrial

Audiencia: cualquier desarrollador que se incorpora al proyecto. Complementa (no sustituye) al GDD v1.3, SAD v1.1 y FAD v1.1.

## 1. Principios operativos

- **El Makefile es el único punto de entrada** (ADR-016). Si ejecutas un comando a mano más de una vez, pertenece al Makefile.
- **Contract First**: `docs/api/openapi.yaml` es la frontera dura entre backend y frontend. Cambiarlo exige: bump de versión, `make generate` en ambos lados y `make contract-lint` verde. El frontend jamás escribe DTOs a mano.
- **Toda decisión estructural pasa por un ADR** en `docs/adr/` ANTES de implementarse. La documentación afectada se actualiza en el mismo cambio.
- **Flujo por funcionalidad**: analizar requisitos → identificar impactos → actualizar docs → diseñar → definir contratos → implementar → tests → validar → sincronizar docs.

## 2. Backend (Go)

- Módulo único `github.com/lokiteitor/global-market/backend`; bounded contexts en `internal/` (`auth`, `ledger`, `sim`, y próximamente `contracts`, `logistics`, `balancer`, `outbox`) **sin imports cruzados entre sí**: solo los composition roots de `cmd/*` componen módulos, y las dependencias cruzadas se expresan como interfaces definidas por el consumidor (p. ej. `ledger.Identity` la implementa el main del gateway con `auth.FromContext`).
- `internal/platform/` (config, logging slog JSON, métricas Prometheus, httpx con envelopes del contrato, pgxpool) y `internal/sim/simtime` son plataforma compartida importable por todos.
- **Dinero y stock**: `int64` (punto fijo) en Go, `BIGINT` en SQL, **string** en JSON. Prohibido float en cualquier magnitud económica.
- **Invariantes del ledger viven en SQL** (regla de oro, ADR-005): triggers de saldo, doble entrada diferida por activo, inmutabilidad append-only y funciones todo-o-nada. El código Go orquesta; la base garantiza.
- **sqlc solo genera código de queries** (`make backend-generate`), nunca esquema (ADR-020). El código generado (`internal/ledger/sqlcgen/`) se versiona; el CI comprueba que no hay drift.
- Config 12-factor con prefijo `II_*`; cada módulo lee sus variables en su `OptionsFromEnv()` con defaults documentados.
- Dependencias permitidas: pgx/v5, google/uuid, prometheus/client_golang, x/crypto. Cualquier otra exige justificación en el PR y, si es estructural, ADR.

### Migraciones (ADR-020)

- Se escriben **a mano** en `backend/db/migrations` como `NNNN_nombre.up.sql` + `NNNN_nombre.down.sql`; todo `up` tiene `down` reversible.
- `make migrate-create name=mi_cambio` genera la pareja; `make migrate-status` verifica checksums (una migración aplicada cuyo fichero cambió es un error).
- `make reset-db` ejecuta TODOS los down y re-aplica todo: los down se ejercitan continuamente. Rehúsa ejecutarse con `II_ENV=prod`.
- En producción, las migraciones se aplican solo en la ventana de mantenimiento diaria (ADR-003).

### Mundo procedural (Incremento 7)

- **`make worldgen`** genera proceduralmente el **mundo multi-región Fase 2** (biomas, ciudades, yacimientos, red ferroviaria/marítima y terminales intermodales) sobre `internal/worldgen`. Es **aditivo** (conserva Askadia (0,0) y su seed), **determinista** (misma `II_WORLD_SEED` ⇒ mismo mundo; value-noise propio + RNG sembrado por celda, **nunca** wall-clock) e **idempotente por clave natural** (re-ejecutarlo no duplica). **Requiere el seed corrido antes.** Paso **opcional** para levantar un mundo multi-región en local; sin él, el entorno usa el mundo mínimo de una región. No añade migraciones (opera tablas de `0003_world`). Variables `II_WORLD_SEED`/`II_WORLD_GRID`/`II_WORLD_REGION_SIZE_M` (defaults en `internal/worldgen/options.go`). Detalle operativo en `docs/runbooks/local.md`.

### Tests

- `make backend-test` ejecuta unit tests siempre; con `II_TEST_DATABASE_URL` apuntando a un servidor PG18 (el de dev vale: el usuario necesita `CREATEDB`) se habilitan integración y E2E, que crean **bases efímeras propias** y no ensucian la de desarrollo.
- `scripts/db-smoke.sh` valida las invariantes del ledger contra la BD de desarrollo (9 casos).

### Bots (ADR-024)

- **El SDK `pkg/botsdk` es la ÚNICA vía soportada para construir bots.** En runtime consume solo la API pública (REST del contrato + WS de ADR-023, ver `docs/api/ws-protocol.md`); prohibido importar `internal/*` desde su código de runtime (sus tests de integración sí pueden). Dinero/stock como strings del contrato — jamás float.
- **Añadir un arquetipo**: implementa la interfaz `Behavior` de `internal/bots` (`Name() string` + `Decide(ctx, *botsdk.Client, *State) error`), regístralo en la población del orquestador con su variable de población propia (`II_BOTS_*`) y **añádelo a `DensityArchetypes`** (`density_options.go`, mismo nombre que `Behavior.Name()`; y a `liquidityBackstop` solo si crea liquidez en el tablón). `Decide` es UNA pasada idempotente: el estado observable de la API manda, `State` solo cachea. Las heurísticas son **auditables**: cada decisión pasa por `decide()` (log slog con bot/arquetipo/decisión/motivo/ids + métrica `ii_bot_decisions_total`) — sin reglas implícitas.
- **`make bots`** arranca el Bot Orchestration Service (`cmd/bots`) contra el gateway local: aprovisiona la población (cuentas `kind=bot`, capitalización del banco central) y la ejecuta. Observabilidad propia en `II_BOTS_ADDR` (default `:8082`).
- **Los 5 arquetipos del GDD §13.2 (completo desde el Incremento 9)**: `coal_producer` e `iron_producer` (productores primarios), `trader` (comerciante/arbitrajista), `industrial_transformer` (compra insumos, funde acero y lo vende con margen sobre el coste unitario estimado de la receta; para la cola si el margen esperado es negativo) y `freighter` (no toca mercancía: valora solicitudes `kind=freight` del tablón, acepta las rentables, despacha su camión y cobra el flete al entregar — CCRI-Flete). Los cinco juegan SOLO por `pkg/botsdk`; sus umbrales se persisten como `behavior` JSON en `auth.bot_profiles` (auditables sin leer código).
- Variables `II_BOTS_*` (defaults en `internal/bots/options.go`): `II_BOTS_COAL_PRODUCERS` / `II_BOTS_IRON_PRODUCERS` / `II_BOTS_TRADERS` / `II_BOTS_TRANSFORMERS` / `II_BOTS_FREIGHTERS` (población **aprovisionada**, default 1 c/u), `II_BOTS_TRANSFORMER_MARGIN_BP` (margen de venta del transformador sobre el coste unitario estimado de sus insumos, default 2500 = +25%), `II_BOTS_FREIGHTER_MARGIN_BP` (margen exigido por el transportista sobre el coste estimado del trayecto, default 2000 = +20%), `II_BOTS_SECRET_SEED` (derivación reproducible de secretos), `II_BOTS_CAPITAL` (capitalización única, default 500000), `II_BOTS_TICK` (periodo de decisión, default `5s`, jitter ±20%), `II_BOTS_API_URL` (default `http://localhost:8080/api/v1`), `II_BOTS_ADDR`.

#### Densidad dinámica (GDD §13.4 modo 2 — la válvula de carga del §19)

El `DensityController` del orquestador ajusta **cuántos bots de cada arquetipo están ACTIVOS**: pausa y reanuda en caliente bots **ya aprovisionados** (conservan cuenta, capital, activos y contratos). No retira cuentas (eso es del `RetirementJob`, y solo por insolvencia) ni crea población: el techo es siempre lo aprovisionado por las variables de arriba. Es **lifecycle, no gameplay** — por eso lee la BD directamente, no el SDK.

- **Señales por ciclo** (una sola consulta): sesiones humanas vivas y comandos humanos recientes (**suben** la densidad: hacen falta contrapartes), lag de la outbox y cola de transbordo sin servir (**bajan** la densidad: el sistema no sigue el ritmo), y publicaciones vivas del tablón (**suben** la de los arquetipos de backstop de liquidez: productores y `trader`).
- **Prioridad**: si el factor de carga baja de 1, los bonos de actividad y cobertura **se descartan** y solo actúa el recorte — primero se protege al humano (GDD §19). Las subidas exigen superar la banda muerta —**salvo desde cero**: un arquetipo apagado reabre siempre, aunque el objetivo sea un solo bot, o la válvula se quedaría cerrada para siempre—; las bajadas se aplican de inmediato.
- **Auditoría**: cada ajuste emite un log INFO con la decisión completa (base, min/max, `activity_bp`/`load_bp`/`coverage_bp`, `load_governed`, target, delta) **y** las señales que lo produjeron: la decisión se reproduce desde el log. Métricas: `ii_bots_density_target{archetype}`, `ii_bots_density_adjustments_total{direction}`, `ii_bots_density_signal{signal}`, `ii_outbox_lag_observed`.
- Variables `II_BOTS_DENSITY_*` (defaults en `internal/bots/density_options.go`; la fórmula íntegra está en la cabecera de `internal/bots/density.go` y en §5.12 del SAD):

| Variable | Default | Efecto |
|---|---|---|
| `II_BOTS_DENSITY_ENABLED` | `true` | `false` deja la población arrancada fija (modo «mundo vivo» puro). |
| `II_BOTS_DENSITY_INTERVAL` | `30s` | Cadencia del lazo de control (exacta, sin jitter). |
| `II_BOTS_DENSITY_MIN` / `_MAX` | `0` / `0` | Suelo y techo de activos **por arquetipo**. Entero suelto (`1`) o lista (`coal_producer=1,trader=2`). `MAX=0` ⇒ lo aprovisionado. |
| `II_BOTS_DENSITY_BASE_BP` | `6000` | Fracción de lo aprovisionado que es la base en un mundo tranquilo y sano (60 %). |
| `II_BOTS_DENSITY_ACTIVITY_GAIN_BP` | `10000` | Ganancia máxima por actividad humana (+100 % sobre la base). |
| `II_BOTS_DENSITY_ACTIVITY_WINDOW` | `15m` | Ventana de comandos humanos que cuenta como actividad. |
| `II_BOTS_DENSITY_SESSIONS_REF` / `_COMMANDS_REF` | `8` / `32` | Valores que **saturan** el factor de actividad (manda el mayor de los dos ratios). |
| `II_BOTS_DENSITY_LAG_LOW` / `_LAG_HIGH` | `200` / `2000` | Rampa del lag de outbox: sano → saturado. El lag son los **eventos pendientes** del consumidor más retrasado **de los tipos que él declara consumir** (`outbox.consumer_cursors.event_types`), no la distancia a `max(seq)`: un consumidor al día vale 0 aunque el mundo lleve millones de eventos emitidos. |
| `II_BOTS_DENSITY_QUEUE_LOW` / `_QUEUE_HIGH` | `100` / `1000` | Rampa de la cola de transbordo. Manda la **peor** de las dos rampas. |
| `II_BOTS_DENSITY_LOAD_FLOOR_BP` | `2000` | Qué queda de la base con el sistema plenamente saturado (20 %). |
| `II_BOTS_DENSITY_COVERAGE_MIN` | `12` | Publicaciones vivas por debajo de las cuales el tablón se considera escaso (`0` desactiva la señal). |
| `II_BOTS_DENSITY_COVERAGE_GAIN_BP` | `5000` | Ganancia máxima del backstop de liquidez con el tablón vacío (+50 %). |
| `II_BOTS_DENSITY_MAX_STEP` | `2` | Suavizado: bots arrancados/parados por arquetipo y ciclo. |
| `II_BOTS_DENSITY_HYSTERESIS` | `1` | Banda muerta **solo al alza** (bajar no espera) y **solo con el arquetipo vivo**: desde 0 activos siempre reabre. |

### Stress test (`make stress`, GDD §13.4 modo 3 / §15.4)

`cmd/stress` es el **cluster de carga desacoplado**: aprovisiona N cuentas en un entorno de pruebas, las hace jugar **por la API pública vía `pkg/botsdk`** (mismos endpoints y rate limits que un humano) con comportamientos ligeros de alta frecuencia, mide y emite un informe. Es un binario **temporal**: no forma parte del mundo ni sustituye a los bots de producción. Escala **horizontalmente** lanzando varias instancias (una corrida se acota a 200 000 bots por proceso).

```bash
# 1) entorno de pruebas vivo (BD + gateway + engine) — p. ej. el local:
make dev && make backend

# 2) corrida (II_STRESS_API_URL es OBLIGATORIA y no tiene default):
II_STRESS_API_URL=http://localhost:8080/api/v1 \
II_STRESS_BOTS=200 II_STRESS_DURATION=120s make stress

# variante en contenedor (perfil compose `stress`, misma configuración y salvaguarda):
II_STRESS_API_URL=http://host.docker.internal:8080/api/v1 make stress-docker
```

`make help` recuerda la salvaguarda y un ejemplo completo. `make stress` corre el harness en el host (`go run ./cmd/stress`); `make stress-docker` lo hace en Docker y propaga el código de salida del contenedor.

**Salvaguarda de entorno (no negociable).** El GDD §13.4 exige que el stress test *nunca* toque el mundo de producción, y eso es código, no una nota del runbook: el harness **rehúsa arrancar** —antes de abrir el pool de BD— si (1) `II_STRESS_API_URL` no está definida (elegir el objetivo es siempre una decisión consciente: **sin default**), (2) `II_ENV` vale `prod`/`production`/`prd`/`live`, o (3) el host de la API **o el de la BD del provisioner** no casan la allowlist de entornos no productivos (`II_STRESS_ALLOW_HOSTS`; por defecto `localhost`, `127.0.0.1`, `::1`, `host.docker.internal`, `*.stress.*`, `staging.*` — definirla **sustituye** la lista, nunca relaja la negativa por `II_ENV`). Cada rechazo cita la regla que lo motiva. Además, **toda** cuenta creada lleva el prefijo `stress-<run_id>-…` y se retira al terminar (`II_STRESS_CLEANUP=true`), sin borrar nada del ledger (append-only). El **provisioning** es la única parte que toca la BD: el contrato no expone endpoint de registro, así que crear cuentas es *admin del entorno de pruebas*; todo lo demás es API pública.

| Variable | Default | Efecto |
|---|---|---|
| `II_STRESS_API_URL` | *(ninguno, **obligatoria**)* | Raíz de la API del entorno de pruebas (`http://host:8080/api/v1`). |
| `II_STRESS_BOTS` | `200` | Bots totales de la corrida (1..200 000). |
| `II_STRESS_MIX` | `producer=50,trader=30,freighter=10,transformer=10` | Mezcla por arquetipo de carga (pesos enteros; se normalizan). |
| `II_STRESS_RAMP` / `_DURATION` / `_TICK` | `30s` / `120s` / `1s` | Rampa de entrada, ventana de carga y periodo de acción por bot (jitter ±20 %). |
| `II_STRESS_WRITE_RATIO` | `0.3` | Fracción de acciones que son **escrituras** (publicar/cancelar/aceptar/planificar ruta). |
| `II_STRESS_STOCK_ENDOWMENT` | `10000` | Dotación de **stock** por cuenta (unidades de producto) que emite el provisioner (`production_output`: +stock_free / −world_source, y la fila física del almacén). Es lo que habilita el **lado vendedor** del harness: con solo capital, una cuenta no puede publicar `sell`/`freight` (exigen la mercancía en el almacén de origen) ni aceptar un `buy` (exige además ser dueño de ese almacén), así que su única contraparte aceptable sería una oferta `sell` **ajena y finita** y la aceptación se degradaría a cero al escalar la población. `0` la desactiva. |
| `II_STRESS_SELL_SHARE` | `0.5` | Fracción de las publicaciones del harness que salen como `sell` (el resto, `buy`). Mantiene contrapartes propias en el tablón **en proporción a la población**, que es lo que hace que la tasa de aceptación escale con los bots. |
| `II_STRESS_REPORT` | `stress-report.json` | Ruta del informe JSON, relativa al cwd (`make stress` corre desde `/backend` ⇒ `backend/stress-report.json`). La consola siempre imprime el resumen. |
| `II_STRESS_ADDR` | `:8083` | `/healthz`, `/readyz` y `/metrics` **propios del harness** (`ii_stress_*`). |
| `II_STRESS_CLEANUP` | `true` | Retirar las cuentas del run al terminar. |
| `II_STRESS_ALLOW_HOSTS` | *(lista por defecto)* | Allowlist de hosts no productivos (API **y** BD). |
| `II_STRESS_DATABASE_URL` | `II_DATABASE_URL` | BD del entorno de pruebas usada por el provisioner y el sondeo. |
| `II_STRESS_TARGET_METRICS` | `:8080` y `:8081` del host de la API | `/metrics` del sistema bajo prueba que se raspan al terminar. |
| `II_STRESS_RUN_ID` / `_CAPITAL` / `_SECRET_SEED` | derivado / `500000` / `dev-stress-seed` | Identidad de la corrida, capitalización por cuenta y derivación de secretos. |

**Cómo leer el informe.** Código de salida: `0` corrida sana · `1` error de configuración/ejecución (incluida la salvaguarda) · `2` **veredicto negativo**. El informe cruza dos fuentes independientes —lo que midió el harness desde fuera y lo que el sistema publica de sí mismo (`/metrics` + sondeo de BD)— y de ahí salen los **disparadores medidos** del SAD §13:

| Qué observar | Dónde | Umbral / lectura |
|---|---|---|
| **5xx y errores inesperados** | `verdict.server_errors` / `unexpected_errors` (tabla *Errores por status/código*) | Cualquier valor > 0 es **bloqueante**: el sistema rompió bajo esa carga. Los `429`, el cooldown anti-parpadeo, `PUBLICATION_EXHAUSTED` o `NO_ROUTE_FOUND` son **benignos** (backpressure y reglas de dominio, no degradación) y se cuentan aparte. |
| **Latencia del tablón** → *motor de búsqueda dedicado* | `operations[]` con `op="board_read"` → `latency.p50_ms/p95_ms/p99_ms` **y** `system.targets[].board_p95_ms` | Compara ambas: si sube la del harness pero no la servida por el gateway, el cuello está en la red o en el propio harness. Un p95 servido que crece con los bots activos, corrida tras corrida, es el disparador. |
| **Lag de la outbox** → *Kafka* | `system.database.outbox_pending` (lag real en la fuente, **medida de referencia**) y `system.targets[].outbox_consumer_lag` (`ii_outbox_consumer_lag` por consumidor) | Las **dos son un instante** —el del fin de la carga—, no el máximo de la corrida: para la evolución temporal mira Prometheus/Grafana. Si divergen manda la de BD: cubre **todos** los cursores y el gauge solo se refresca en el polling de cada consumidor (uno parado o en backoff publica su último valor, típicamente `0`); el informe lo señala con la línea *«manda la de BD»*. Debe volver a ~0 al terminar la carga; un lag que **crece monótonamente** corrida tras corrida y no drena es el disparador. |
| **Carga del proceso del engine** → *extracción de shards* (handoff ADR-015) | `system.targets[]`: `go_goroutines`, `process_cpu_seconds_total`, `process_resident_memory_bytes` | Relativízalo a los bots activos y a las ops/s de la corrida; el disparador es la **tendencia sostenida** entre corridas comparables, no una cifra suelta. |
| **Contención SERIALIZABLE** → *techo de escritura* | `system.targets[].tx_serialization_retries_delta` / `tx_serialization_exhausted_delta` (delta de `ii_tx_serialization_*` contra la línea base) y `verdict.target_tx_serialization_retries/_exhausted` | Los **reintentos** son ruido normal bajo carga (SSI bloquea por página y las claves UUIDv7 concentran las inserciones): míralos como tendencia, no como incidencia. Cada **presupuesto agotado** (`_exhausted` > 0) es una transacción revertida entera: o un `503 SERIALIZATION_CONFLICT` al cliente o —peor, porque nadie la recibe— un trabajo de fondo (lote de producción, liquidación de contrato vencido, sorteo) que se cae hasta el barrido siguiente. El veredicto lo saca como **ADVERTENCIA** explícita; no tumba la corrida (salida `0`: encontrar el techo es el objetivo del harness, no una rotura), pero es señal de techo y exige comparación entre corridas antes de subir carga. Un proceso que no registre la familia sale como *«SIN lectura»*: ausencia de medición, no de contención. |
| **Contexto económico** | `system.database`: publicaciones vivas/creadas, contratos confirmados y `contracts_per_second` | Valida que la carga fue *real* (hubo mercado) y no solo lecturas: un `write_ratio` alto sin contratos confirmados suele indicar bots sin caja o sin contrapartes. |
| **Representatividad del perfil** → *¿midió el camino caliente?* | `operations[].skipped` / `skip_reasons` y `verdict.unexercised_paths` (líneas **ATENCIÓN: el camino … NO SE EJERCITÓ / se ejercitó a medias**, las **primeras** del veredicto) | Una operación 100 % omitida **no se midió**: el informe queda hablando de lecturas y del camino corto de escritura aunque todo lo demás salga limpio. El caso típico es `accept` —la única que ejercita escrow + sorteo + contención SERIALIZABLE de verdad— quedándose sin ofertas `sell` que aceptar. Antes de comparar corridas, comprueba que `unexercised_paths` está vacío y que la omisión de `accept` no crece con los bots; si crece, sube `II_STRESS_STOCK_ENDOWMENT`/`II_STRESS_SELL_SHARE`. |

Guarda el JSON de cada corrida: las decisiones estructurales (Kafka, extracción de shards, motor de búsqueda) exigen ADR **con la medición que las justifica** (SAD §12), y la comparación entre corridas es esa medición.

## 3. Frontend (Nuxt 4)

- Paquete autónomo con **npm sin workspaces** (ADR-021). Kernel puro en `shared/` (Money BigInt, SimTime, uuidv7, Result, mini-i18n) y dominio en `domain/`: **jamás importan vue/nuxt/pinia** (regla de ESLint).
- `npm run gen:api` (o `make frontend-generate`) regenera `types/api.d.ts` desde el contrato. Si el contrato cambió, el typecheck falla ruidosamente hasta reconciliar.
- **Thin client** (FAD): el cliente presenta, nunca decide; el estado replicado solo se escribe aplicando respuestas/eventos del servidor; predicción optimista siempre marcada y reversible.
- Estilos: Sass propio por capas ITCSS (`app/assets/styles/`) + CSS Modules por componente. Prohibidos los frameworks CSS y las librerías de componentes. Tema oscuro por defecto, claro/oscuro conmutables (única preferencia persistida en localStorage).
- Textos SOLO vía `shared/i18n` (`locales/es.json`). El token de sesión vive solo en memoria.
- En dev, Nuxt proxya `/api` → `localhost:8080` (mismo origen, sin CORS); en producción lo hace Caddy.

### Cliente de juego (`/play`, Incremento 5)

Mapa Phaser top-down cenital (ADR-019) + HUD Vue. El estado implementado y sus simplificaciones conscientes están anotados en el FAD (notas «Estado implementado (Incremento 5)» en §11.9, §12.5, §13.6, §14, §16.3 y §26.1).

**Arquitectura de `game/`** (motor de render, sin Vue/Nuxt/Pinia/red; fronteras en ESLint):

- **Entrada única**: `game/index.ts` (`createGame`). La app lo carga perezosamente (`await import('~~/game')` en `GameCanvasHost.vue`): Phaser jamás entra en el bundle del portal.
- **`shared/geometry/grid.ts` — GridProjection**: ÚNICO punto de conversión mundo ↔ pantalla (la API habla metros planos `[x_m, y_m]`, SRID 0). 1 tile = 250 m = 32 px; mundo Askadia 50 000 × 50 000 m = 200 × 200 tiles; chunks de 32 × 32 tiles. Prohibida la matemática de proyección fuera de aquí.
- **`game/map/` — ChunkManager**: streaming + culling + LRU de chunks según viewport (lógica pura testeada en `chunk-logic.ts`). Terreno placeholder: suelo plano coloreado por bioma de región (el backend aún no expone terreno por tile).
- **`game/bridge/`**: deriva **view-models planos** (metros, solo lo que el sprite necesita) de lo VISIBLE, desde el puerto `WorldStateSource` que la app implementa sobre las stores Pinia (`app/composables/useWorldLive.ts`); diffs por identidad y ≤1 recomputación por frame.
- **Capas y renderers**: orden de dibujo fijo por capas (`LAYER_ORDER`: terreno → red → recursos → edificios → vehículos → efectos → overlays → etiquetas); un renderer con **pooling** por tipo de entidad en `game/entities/`, ensamblados en `game/world-live.ts`. Texturas **generadas en runtime** (`game/textures.ts`, claves lógicas, paleta espejo de los tokens Sass) — sin binarios de arte en esta fase.
- **Sincronización**: `app/composables/useGameSync.ts` — bootstrap por REST + deltas por la room WS `corp` (ADR-023); cada evento invalida y **re-consulta** por REST; hueco de `seq`/reconexión → re-bootstrap propio; vehículos extrapolados analíticamente con el `SimClock`.

**Añadir una entidad al mapa** (VM + textura + capa):

1. Exponer el dato en su store replicada (escrita solo con respuestas/eventos del servidor) y en el puerto `WorldStateSource` (`useWorldLive.ts`).
2. Definir su VM plano en `game/bridge/vm.ts` y su derivación desde el source en `game/bridge/derive.ts` (funciones puras, con test).
3. Registrar su textura runtime con clave lógica en `game/textures.ts` (o `game/entities/textures-extra.ts`).
4. Escribir el renderer con pooling en `game/entities/` y ensamblarlo en `game/world-live.ts` dentro de su capa (el orden de creación de containers es el z-order).

**Añadir un panel al HUD**: registra el nombre en `GAME_PANELS` (`app/stores/panels.store.ts`), crea el componente en `app/components/play/` sobre `FloatingPanel` + componentes base (`app/components/base/`), textos SOLO vía `t()` (`shared/i18n/locales/es.json`), botón en `HudSidebar.vue`; datos y comandos vía `useGameApis` → módulos `network/*.api` (patrón `auth.api.ts`) con mappers (los DTO no salen de `network/`); test de componente en `tests/nuxt/`.

**Smoke E2E (Playwright)**: `npm run test:e2e` en `/frontend`. **Prerrequisito: stack vivo** — backend dev en :8080 (`make dev` + `make backend`, opcionalmente `make bots`) y frontend dev en :3000 (`make frontend`); el spec se salta limpiamente si `/healthz` no responde. Flujo de solo lectura: login Demo → `/play` → mundo renderizado → HUD → panel Mercado, con screenshots en `tests/e2e-browser/`. No forma parte de `make test` (los tests unitarios no asumen backend corriendo).

## 4. Observabilidad

- Logging estructurado (slog JSON) con `request_id`; métricas Prometheus en ambos binarios (`ii_http_*`, `ii_sim_time_seconds`, `ii_rate_limited_total`, pool de BD) y en los procesos auxiliares (`ii_bot_*`/`ii_bots_density_*`/`ii_outbox_lag_observed` en `:8082`; `ii_stress_*` en `:8083`); dashboard base en Grafana (`infra/grafana/dashboards/`).
- Toda funcionalidad nueva incluye logging, manejo de errores tipados del contrato y métricas cuando aporte. No se considera terminada sin ello.

## 5. Definition of Done

Compila · tests verdes (unit + integración) · lint/fmt limpios · sin regresiones · documentación sincronizada · observabilidad · manejo de errores · sin deuda técnica gratuita. `make lint test build` es el gate mínimo local antes de commit.

## 6. Convención de commits

Conventional Commits (`feat:`, `fix:`, `refactor:`, `perf:`, `test:`, `docs:`, `build:`, `ci:`, `chore:`) con scope por contexto cuando aplique (`feat(contracts): …`). Un commit por incremento funcional verificado.
