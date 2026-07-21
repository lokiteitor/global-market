# ADR-025 — Red eléctrica regional (Fase 3): convivencia con el combustible in situ y materialización del mercado spot

| Campo | Valor |
|---|---|
| **ID** | ADR-025 |
| **Fecha** | 2026-07-21 |
| **Estado** | Aceptado |
| **Desarrolla** | GDD §5.8 (red eléctrica regional, especificación conservada para Fase 3), §6.2, §11, §18.1; ADR-022 (`world_source`), ADR-019 (geometría planar), ADR-020 (migraciones manuales) |
| **Modifica** | Enums `world.batch_status` (+`paused_no_power`) y `ledger.transaction_kind` (+`power_spot`) vía migración `0017_electric_grid` |

## Contexto

La v1 resolvió la energía como **combustible físico consumido in situ** (GDD §5.8, decisión v1.2 #29): cada receta declara `fuel_product_id`/`fuel_per_batch`, el combustible llega por logística y sin combustible la producción pausa (`paused_no_fuel`). El GDD conservó **íntegra** la especificación de la red eléctrica para su activación en Fase 3: centrales construibles (térmicas a combustible físico, hidroeléctricas), líneas de transmisión con huella y mantenimiento, **mercado spot regional por orden de mérito con precio de cierre uniforme** (no pay-as-bid) y **recorte rotatorio por prioridad inversa de precio** ante déficit. Quedan explícitamente fuera: flujos de potencia realistas, pérdidas por distancia, almacenamiento e interconexiones interregionales (GDD §22). El tick del spot pertenece al **Economy Balancer** (GDD §18.1 nº 5).

La especificación deja a la implementación tres huecos que este ADR cierra: **(a)** cómo convive la red con el modelo de combustible el día que se activa (qué edificios consumen electricidad, si un edificio convive con ambas fuentes, si la activación es por región o global); **(b)** qué significa "los que menos pagan" en el recorte cuando el precio de cierre es uniforme; **(c)** qué significa "conectado" sin flujos de potencia, y dónde puede emplazarse una hidroeléctrica en un mundo que aún no tiene ríos (worldgen v1.7 los dejó pendientes; el agua existe solo como biomas `coast`/`ocean` a granularidad de región).

## Decisión

### 1. Convivencia: la fuente de energía es un atributo de la RECETA; la electrificación es aditiva y opcional

- `world.recipes` gana **`power_per_hour`** (`stock_qty`, default 0): unidades de energía por **hora de sim-time** que la receta consume **mientras su lote está activo**. Es el análogo eléctrico de `fuel_per_batch` expresado como potencia (el spot vende energía por intervalo, no por lote; la conversión a "consumo por ciclo" del GDD §6.2 es `power_per_hour × duración_del_lote`).
- **Ninguna receta existente cambia**: todas siguen quemando combustible in situ exactamente como hoy. La electricidad entra al catálogo como **recetas nuevas** (p. ej. `smelt_steel_electric` para el alto horno, sin combustible y con `power_per_hour > 0`). Un jugador se electrifica **eligiendo la receta activa** de su edificio; ambos modelos conviven indefinidamente en el mundo y en el mismo edificio multi-receta. No hay migración forzosa ni interruptor global.
- Una receta puede declarar **combustible Y electricidad a la vez** (conjunción: ambos son necesarios — p. ej. un horno que necesita carbón como reductor y electricidad de proceso). Nunca es disyunción ("uno u otro"): la fuente dual conmutable queda como expansión futura.
- **La activación es orgánica y por región**: el mercado spot de una región existe en cuanto hay líneas operativas en ella; la demanda existe donde hay recetas eléctricas activas. No hay flag de activación: una región sin red simplemente no despacha y sus lotes eléctricos pausan (`paused_no_power`), igual que un lote sin carbón pausa hoy.

### 2. Lado de demanda: puja por edificio con default, para materializar el recorte por prioridad inversa de precio

El recorte "empezando por los que menos pagan" exige disposición a pagar **diferenciada** por consumidor; el precio de cierre uniforme no la da. Se materializa con una **puja máxima por edificio** (`world.power_bids.unit_price`, ajustable por el dueño vía API; default global `II_POWER_DEFAULT_BID_PRICE`). La puja es (a) el **orden de prioridad** del recorte (menor puja = primero en recortarse) y (b) el **techo personal**: nadie paga por encima de su puja (los consumidores se sirven en orden de puja descendente y el precio de cierre nunca supera la puja del último servido). El pago efectivo de **todos** los servidos es el **precio de cierre uniforme** (oferta del generador marginal), como exige el GDD.

**Rotación**: entre pujas iguales, el recorte prefiere al edificio recortado **menos recientemente** (`world.buildings.last_curtailed_at_sim` ascendente), de modo que el castigo rota entre ciclos y no recae siempre en los mismos.

**Insolvencia sin deuda (GDD §5.9)**: un consumidor cuya caja no cubre su pago máximo posible (`puja × energía`, presupuesto acumulado por corporación dentro del tick) queda **excluido de la demanda** de ese tick — sin compra, sin deuda, edificio sin suministro, lote en `paused_no_power`. El trigger `ck_accounts_non_negative` sigue siendo la garantía última.

### 3. Conectividad: pool regional por radio a una línea operativa (sin flujos)

Un edificio (generador o consumidor) **participa en el mercado de su región** si su ubicación está a ≤ `II_POWER_CONNECT_RADIUS_M` (`ST_DWithin`, SRID 0 en metros) de **alguna línea de transmisión operativa de esa región**. Las líneas materializan así su papel de "conectar generadores y consumidores dentro de una región" sin inventar flujos de potencia: el pool es único por región **porque el mercado spot es por región** (GDD §5.8); el análisis de componentes conexas sería una fidelidad física que la propia especificación excluye. Las interconexiones interregionales siguen fuera (una línea debe caer íntegra dentro de los `bounds` de su región).

### 4. Líneas de transmisión: infraestructura lineal PROPIA sin concesión, con mantenimiento como sink

- Nueva tabla **`world.power_lines`**: dueño, región, `path geometry(LineString,0)`, `length_m`, `condition_pct`, `status` (`operational`/`abandoned`), `maintenance_paid_until_sim`. La huella espacial del GDD §11 es el trazado; su "área de servicio" es el radio de conexión.
- **No requieren concesión de suelo**: una línea cruza muchas parcelas y suelo público, como las carreteras (que ya son infraestructura sin concesión). El papel anti-acaparamiento del canon lo cumple aquí el **mantenimiento periódico**: coste de construcción proporcional a la longitud (sink, kind `maintenance`, como el `build_cost` de edificios) y **mantenimiento por día-sim proporcional a la longitud** (`II_POWER_LINE_MAINT_PER_KM_DAY`), cobrado por el barrido de `world/enforcement` con el patrón exacto de edificios: se cobra **solo lo disponible**, los días impagados degradan `condition_pct` (mismos `II_DEGRADE_PCT_PER_SIM_DAY`/`II_ABANDON_CONDITION_PCT`), cada día vencido se salda exactamente una vez, y al cruzar el umbral la línea pasa a **`abandoned`: deja de conducir** (desconecta a quien dependía de ella) y es terminal. Sin embargo ni subasta: el valor de una línea es su trazado; su fila se conserva por auditoría.

### 5. Centrales: tipos de edificio estándar con parámetros de generación en tabla propia

- Nueva tabla **`world.power_plant_types`** (`building_type_id` PK): `capacity` (unidades de energía por hora-sim a nivel 1; el nivel multiplica por `level_curve.capacity_mult`, default = nivel), `fuel_product_id`/`fuel_per_unit` (térmicas; `NULL`/0 en hidro). Las centrales son `world.building_types` normales: concesión, huella, `build_cost`/`maintenance_cost` (su mantenimiento ya lo cobra el barrido de edificios existente), estado y cascada de insolvencia estándar. No tienen recetas ni lotes: **su generación la gobierna el despacho del spot**.
- **Térmicas**: capacidad ofertable limitada por el combustible en su almacén local (mínimo de los dos planos, físico y `stock_free`, como `consumable` en producción): `min(capacidad_nivel, combustible/fuel_per_unit)`. **Sin combustible → no despachan** (GDD §5.8 literal). El quemado por despacho asienta `consumption` contra `world_source` (ADR-022) y mueve físico+espejo (`building_inventories`, `fuel_stock`) en la misma tx.
- **Hidroeléctricas**: emplazamiento restringido por la nueva regla server-side **`requires_biome`** (`placement_rules`, p. ej. `{"requires_biome":["coast"]}`): la región de la concesión debe tener uno de los biomas listados. Es la materialización honesta de "ríos/agua" **hoy**: el agua solo existe como bioma regional (`coast`); cuando worldgen incorpore ríos (pendiente de v1.7), la regla se extenderá a proximidad de río de forma aditiva. Sin combustible, coste marginal ~0: su oferta barata abre el orden de mérito.
- **Oferta**: el dueño fija el precio de oferta por central (`world.power_offers.unit_price`, vía API). **Sin oferta explícita la central no participa** (participar en el mercado es una decisión del jugador, como publicar en el tablón).

### 6. El tick del spot: worker del Economy Balancer, bucketizado en sim-time, un asiento por región y tick

- **`PowerWorker` en `internal/balancer`** (GDD §18.1: el tick del spot es del Balancer). Bucle wall-clock (`II_POWER_SPOT_SWEEP_INTERVAL`) que procesa **cada región con líneas operativas** cuyo bucket venció: `tick_sim = floor(simNow / II_POWER_SPOT_INTERVAL_SIM) × intervalo` (default 3600 sim = 1 hora-sim). Idempotente por PK `(region_id, tick_sim)`; los buckets perdidos **no se recuperan** (la electricidad no es almacenable: un tick perdido es energía no comerciada, sin efecto contable). Cada región en su propia tx `SERIALIZABLE` con `outbox.Emit` en la misma tx.
- **Casación** (función pura, testeable): oferta ordenada por mérito (precio asc); demanda por puja desc (rotación como desempate). Se sirven consumidores **enteros** (un edificio a medias no produce) mientras la oferta acumulada cuya siguiente unidad marginal cueste ≤ su puja alcance; los generadores despachan parcialmente. **Precio de cierre = oferta del generador marginal despachado**; lo pagan todos los consumidores servidos y lo cobran todos los generadores despachados (uniforme, no pay-as-bid — regla explícita del GDD).
- **Asiento**: una única transacción del ledger **`power_spot`** por región y tick con N+M partidas (`−cierre×energía` en la caja de cada consumidor servido, `+cierre×despacho` en la de cada generador), balanceada por construcción (Σ energía servida = Σ despacho); más un asiento `consumption` por térmica despachada (combustible → `world_source`). Es una **redistribución cash→cash** (ni faucet ni sink): la masa monetaria no cambia. La electricidad **no es un producto del ledger** (no es stock almacenable ni transportable): solo mueven valor el dinero y el combustible; el plano físico del despacho queda en `world.power_spot_ticks`/`world.power_dispatches`.
- **Efecto sobre producción**: el propio tick **pausa** (`paused_no_power`, evento `power.curtailed`) los lotes en marcha de los consumidores no servidos (recorte, insolvencia) **cerrando además su cobertura residual** (`powered_until_sim = tick`, tasa 0 — sin ese cierre, la gracia del tick anterior dejaría al barrido de producción reanudar y completar con energía no comprada), y **reanuda** los `paused_no_power` de los servidos, marcando `powered_until_sim = tick + intervalo × 1.5` (el medio intervalo de gracia absorbe el desfase wall-clock entre buckets) y `powered_rate` = el `power_per_hour` **facturado**. El motor de producción exige `powered_until_sim > simNow` **y** `power_per_hour ≤ powered_rate` al cerrar un lote eléctrico (guarda doble contra producción sin haber comprado energía: la segunda condición impide cambiar a mitad de intervalo a un lote de carga mayor pagando la menor) y reanuda pausados por sí mismo si la receta dejó de ser eléctrica o la cobertura sigue vigente a la tasa facturada. Los tres estados de pausa cuentan como lote activo del edificio (invariante de un lote en curso: encolar durante la pausa no promueve un segundo lote).

### 7. Nuevos valores de enum (migración `0017_electric_grid`, patrón 0008)

- **`world.batch_status` + `paused_no_power`**: se exige estado nuevo (no se reutiliza `paused_no_fuel`) porque el remedio del jugador es **distinto** (traer carbón por logística vs. asegurar suministro eléctrico regional: subir puja, construir generación o líneas) y el recorte rotatorio debe ser observable como tal. Es el mismo peldaño 2º de la cascada de GDD §5.9 ("Combustible/energía"), con su misma garantía: pausa, nunca deuda.
- **`ledger.transaction_kind` + `power_spot`**: el asiento multi-parte del spot es un flujo económico de primer orden que el Balancer debe poder monitorizar por kind (como `wage`, `canon`, `transfer`); reutilizar `transfer` (bilateral, cash→cash con fee) ocultaría el mercado eléctrico en la auditoría. Precedente de extensión: 0008 (`-- migrate:no-transaction`, `ADD VALUE IF NOT EXISTS`, down que falla explícitamente si existen filas — el ledger es append-only).

### 8. Fronteras de módulo

| Pieza | Dónde | Por qué |
|---|---|---|
| Líneas, ofertas, pujas, endpoints de red | `internal/world/power` (subpaquete de `world`, `world/sqlcgen`) | Activos físicos del mundo, como buildings/fleet |
| Mantenimiento de líneas | `internal/world/enforcement` | Mismo barrido y filosofía de la cascada (3º escalón) |
| Tick del spot (casación, pagos, quemado, pausa/reanudación) | `internal/balancer` (`PowerWorker`, queries propias) | GDD §18.1 asigna el tick al Economy Balancer; escribe cross-schema con queries sqlc propias ("la frontera es de código Go, no de esquema"), sin importar `world` ni `contracts` |
| Guarda `powered_until_sim` y reanudaciones | `internal/world/production` | El motor de lotes es suyo |

Sin imports cruzados nuevos; la integración inversa (UI/consumidores) va por outbox: `power.spot_cleared` (agregado `region`), `power.curtailed` (agregado `building`), `power_line.created` (agregado `power_line`).

## Consecuencias

- (+) La activación de la red **no rompe nada**: cero edificios cambian de comportamiento el día 0; la electrificación es una decisión de receta por jugador, y el arbitraje combustible↔electricidad se vuelve gameplay (el precio spot compite contra el precio del carbón más su flete).
- (+) Todas las reglas duras se conservan: doble entrada por activo (el asiento `power_spot` balancea por construcción), no-negatividad, pausa sin deuda, coste ∝ eventos (un tick de baja frecuencia por región, sin bucle por entidad), event-driven puro.
- (+) La térmica crea demanda estructural de carbón (nuevo sink físico vía `world_source`) sin tocar el mercado CCRI.
- (−) Dos huecos del mundo quedan como deuda registrada: **ríos** (hidro restringida a bioma `coast` hasta que worldgen los genere) y **visualización de líneas en el cliente** (el contrato las expone; el render es del incremento de frontend).
- (−) La puja de demanda con default es una simplificación consciente del "menos pagan": sin ella el recorte uniforme sería arbitrario; se registra como nota de implementación en GDD §5.8.
- (−) Buckets de spot perdidos por caída del engine no se retro-liquidan (energía no almacenable): pérdida de gameplay momentánea, nunca contable.

Documentos actualizados en el mismo cambio: `documentacion_base_de_datos.md` (sección v1.10: tablas, asientos, parámetros), `arquitectura_imperio_industrial.md` (§5.13 materialización), `gdd.md` §5.8 (nota de implementación), contrato OpenAPI v1.6.0 (changelog).
