# Imperio Industrial: Simulación Económica MMO
### Game Design Document (GDD) + Software Architecture Document (SAD)

**Versión:** 1.2.1
**Changelog:**
- **1.2.1** (2026-07-15): alineación técnica con la implementación v1 — §17.2 (ULID → UUIDv7 nativo de PostgreSQL 18, ADR-IMPL-01), §18.3 y Anexo de stack (Go+pgx sin sqlc, TS+Fastify+pg sin Drizzle, monorepo real, migraciones manuales — ADR-IMPL-02/03/04), nota final en Anexo B remitiendo a los ADR-IMPL de `docs/desarrollo.md`. El diseño de gameplay no cambia.
- **1.2**: revisada tras review de simplificación (ver Anexo B: registro de decisiones).
**Referencias de diseño:** Sim Companies, Simutrans, OpenTTD, Factorio
**Tipo:** MMO de simulación económica, industrial y logística en mundo único persistente

---

## Índice

1. Visión general
2. Gameplay principal
3. Loop principal del jugador
4. Mecánicas principales
5. Sistema económico
6. Sistema industrial
7. Sistema logístico
8. Gestión de flotas
9. Generación procedural del mundo
10. Sistema de recursos naturales
11. Modelo de infraestructura
12. Sistema de comercio
13. Diseño de bots e integración con jugadores humanos
14. Arquitectura multijugador
15. Arquitectura distribuida para simulación masiva
16. Modelo de entidades
17. Modelo de datos
18. Diseño del backend
19. Estrategia de escalabilidad
20. Riesgos técnicos
21. Roadmap de implementación por fases
22. Posibles expansiones futuras

---

## 1. Visión general

El juego propone un **mundo isométrico único, persistente y compartido** (Single Shared World) en el que decenas de miles de jugadores humanos y cientos de miles de agentes automatizados coexisten en una misma economía viva (dentro del techo de capacidad asumido en la sección 19). No hay servidores separados ni instancias: todos comparten el mismo mapa, el mismo mercado y las mismas reglas.

El pilar central no es la progresión narrativa ni el desbloqueo de contenido, sino la **optimización de sistemas complejos**: cadenas de producción, redes logísticas y estrategia de mercado. La profundidad emerge de la interacción entre estos tres sistemas, no de mecánicas artificiales de "árbol tecnológico".

El juego se dirige a un público que disfruta de la gestión, la optimización de procesos y la economía emergente (fans de Factorio, OpenTTD, EVE Online, Sim Companies), ofreciendo una capa social y competitiva sostenida por un mundo que nunca se reinicia.

**Pilares de diseño:**

- **Persistencia real**: el mundo nunca se resetea; las decisiones tienen consecuencias a largo plazo.
- **Economía emergente**: los precios y la escasez surgen de la oferta/demanda real, no de tablas fijas.
- **Logística física**: nada se teletransporta; todo bien debe recorrer una ruta real.
- **Progresión horizontal**: se compite por eficiencia y escala, no por acceso a contenido.
- **Bots como ciudadanos de primera clase**: usan las mismas APIs que un jugador humano, sin atajos, y residen de forma **permanente en el servidor de producción**, no solo en entornos de prueba.

### 1.1 Modelo de tiempo de la simulación

Todas las mecánicas del juego se miden contra un único reloj lógico: el **tiempo de simulación (sim-time)**. El tiempo real (wall-clock) solo se usa para sesiones, rate limiting y UI; ninguna regla de juego depende de él.

**Ratio tiempo de juego / tiempo real:**

| Unidad de juego | Tiempo real |
|---|---|
| 1 día de juego | 1 hora real |
| 1 estación (90 días de juego) | ~3,75 días reales |
| 1 año de juego (360 días) | 15 días reales |

Este ratio (24×) sostiene el loop de dos velocidades: en una sesión corta de 10 minutos reales han pasado ~4 horas de juego (la producción y los envíos avanzan visiblemente), y una estación completa —con su ciclo de demanda urbana— se vive en menos de una semana real. Los plazos de contratos, tiempos de producción y tiempos de viaje se definen y almacenan **siempre en sim-time**; la UI los muestra en ambas unidades.

**Motor event-driven (no tick global):**

- Cada shard mantiene una **cola de prioridad de eventos programados** (fin de lote de producción, llegada de vehículo a un hito, vencimiento de contrato, decaimiento de índice urbano). El coste de CPU es proporcional al número de **eventos que ocurren**, no al número de entidades: un edificio ocioso o un vehículo en un tramo largo sin incidencias no consumen ciclos.
- Las magnitudes continuas (posición de un vehículo en un enlace, progreso de un lote) se calculan **analíticamente bajo demanda**: se almacena `(estado_inicial, t_inicio, función_de_avance)` y la posición/progreso se deriva para cualquier `t` cuando alguien la observa. Solo los **hitos** (salida, llegada a nodo, cruce de frontera, avería, saturación de enlace) generan eventos y escrituras.
- No existe un tick global; cada shard avanza su sim-time procesando su cola. Un tick auxiliar de baja frecuencia puede existir para procesos agregados (recomputación de congestión por enlace cada 30–60 s de sim-time), implementado como evento recurrente más de la cola.

**Determinismo y recuperación (v1.2: replay rebajado de requisito a aspiración):**

- Se conservan las prácticas baratas del determinismo: RNG con semilla persistida como parte del estado, prohibido el wall-clock y cualquier fuente de entropía externa dentro de la lógica de simulación, orden estable de eventos dentro del shard (desempate por `(sim_time, sequence_number)`), y enteros/punto fijo para dinero y stock (nunca floats).
- El **replay bit a bit deja de ser requisito duro**: imponía una disciplina permanente sobre cada línea del motor (aritmética totalmente reproducible, cero concurrencia no determinista) cuyo costo difuso superaba su valor, dado que el valor económico ya está protegido por otra vía (el ledger).
- **Recuperación tras caída**: snapshot periódico del estado del shard (cada pocos minutos de wall-clock); recuperar = cargar el último snapshot. Se acepta un **RPO de minutos solo para el estado físico** (posiciones de vehículos, progreso de lotes) — imperceptible a ratio 24×, donde un envío dura horas de sim-time. El dinero, el stock comprometido y los contratos viven en el ledger ACID (15.3) y **no pierden nada**; tras recuperar, la reconciliación física↔contable detecta cualquier discrepancia. El replay determinista queda como herramienta opcional de diagnóstico de bugs de balance, no como mecanismo de durabilidad.

**Ventana de mantenimiento diaria:**

- El mundo opera con una **pausa programada diaria** (objetivo: 10–30 min, horario fijo UTC anunciado), durante la cual el sim-time de todos los shards queda **congelado de forma coordinada**. Precedente: EVE Online opera así desde hace dos décadas.
- Al medirse todos los plazos en sim-time, la pausa es **económicamente transparente**: ningún contrato vence, ningún lote se pierde, ningún vehículo llega tarde por causa de la ventana.
- La ventana habilita, sin ingeniería heroica: migraciones de esquema, despliegue de nuevas versiones de shards con estado, rebalanceo de regiones entre shards y snapshot global consistente (backup del mundo entero en un punto de sim-time común).

---

## 2. Gameplay principal

El jugador asume el rol de una **corporación industrial**. Su objetivo es construir, desde una posición inicial modesta, un imperio que abarque extracción de recursos, transformación industrial, logística y comercio.

**Actividades centrales:**

- Obtener terrenos en concesión en el mundo compartido (ver 11.1: todo el suelo es arrendamiento del sistema, renovable y traspasable; no existe propiedad perpetua del suelo).
- Construir y mejorar instalaciones productivas.
- Diseñar cadenas de suministro (desde materia prima hasta producto final).
- Construir y operar redes de transporte multimodal.
- Comerciar en el mercado global o mediante contratos privados.
- Cooperar informalmente con otros jugadores o competir directamente por regiones y rutas.

No hay condición de "victoria" única; el juego se plantea como un **sandbox competitivo continuo**, con temporadas de rankings (mayor throughput, mayor valor de imperio, mayor cuota de mercado en un bien) sin resetear el mundo.

---

## 3. Loop principal del jugador

```
┌─────────────────────────────────────────────────────────────┐
│  1. OBSERVAR                                                 │
│     - Estado del mercado (precios, órdenes, demanda)          │
│     - Estado de la propia infraestructura                     │
│     - Estado logístico (rutas, congestión, inventarios)        │
├─────────────────────────────────────────────────────────────┤
│  2. DECIDIR                                                   │
│     - ¿Invertir en más capacidad?                              │
│     - ¿Diversificar cadena productiva?                         │
│     - ¿Expandir red logística?                                 │
│     - ¿Comprar/vender en el mercado?                            │
├─────────────────────────────────────────────────────────────┤
│  3. EJECUTAR                                                  │
│     - Construir/mejorar edificios                              │
│     - Configurar recetas y colas de producción                 │
│     - Asignar flotas y rutas                                    │
│     - Colocar órdenes de compra/venta                           │
├─────────────────────────────────────────────────────────────┤
│  4. SIMULACIÓN CONTINUA (offline/online)                       │
│     - Producción avanza en tiempo real                          │
│     - Vehículos se mueven físicamente                            │
│     - Mercado se ajusta con la oferta/demanda global             │
├─────────────────────────────────────────────────────────────┤
│  5. RETROALIMENTACIÓN                                          │
│     - Ingresos/gastos, eficiencia, cuellos de botella            │
│     - Nuevas oportunidades de mercado                            │
│     - Vuelta a paso 1                                            │
└─────────────────────────────────────────────────────────────┘
```

El loop está diseñado para funcionar en **dos velocidades**: sesiones cortas (revisar y ajustar en 5-10 minutos) y sesiones largas (planificación de expansión, diseño de redes logísticas complejas). La simulación continúa mientras el jugador está desconectado.

---

## 4. Mecánicas principales

| Mecánica | Descripción |
|---|---|
| **Construcción** | Colocación de edificios en terrenos en concesión (11.1), con validación de espacio, acceso y recursos. |
| **Recetas de producción** | Cada edificio transforma insumos en productos según una receta con tiempo, energía y mano de obra. |
| **Niveles de capacidad** | Mejoras que multiplican líneas de producción, almacenamiento y eficiencia, sin desbloquear contenido nuevo. |
| **Rutas logísticas** | Definición de origen-destino, medio de transporte y horarios/frecuencia. |
| **Contratos de compraventa** | Acuerdos bilaterales respaldados por stock real, con garantías bloqueadas por el banco central y liquidación contra entrega física confirmada. |
| **Costo laboral regional** | El costo salarial de cada instalación se deriva de una fórmula según el nivel de la ciudad cercana y la saturación industrial de la región (5.7). |
| **Mantenimiento** | Degradación de edificios/vehículos que requiere inversión continua. |
| **Fiscalidad** | Impuestos regionales y tarifas que afectan la rentabilidad y sirven como palanca de balance económico. |
| **Especialización regional** | Distribución desigual de recursos que fuerza comercio interregional. |
| **Demanda de ciudades** | Las ciudades consumen bienes finales de forma continua, con una curva de demanda dinámica que sube o baja según la oferta reciente y el nivel de desarrollo de la ciudad (estacionalidad: expansión futura, sección 22). |

---

## 5. Sistema económico

### 5.1 Principios

La economía es **endógena entre agentes**: los precios de mercado surgen de transacciones reales entre humanos y bots, no de tablas de intercambio fijas. Existe una excepción deliberada y reconocida: las **ciudades actúan como anclas de precio administradas** (su curva de demanda parte de un `precio_base` de diseño, ver 5.6). Ese anclaje es el suelo/techo efectivo de la economía y, por tanto, **la principal palanca de balance del juego** — se declara como tal, con gobernanza explícita de sus ajustes, en lugar de fingir que no existe.

### 5.2 Componentes

- **Contratos de compraventa** como unidad económica atómica (ver 5.3): no existe un order book anónimo ni un motor de emparejamiento algorítmico; toda transacción es un acuerdo bilateral entre dos entidades identificadas (jugador, bot o ciudad).
- **Tablón de contratos**: espacio de descubrimiento donde se publican ofertas y demandas georreferenciadas (ubicación de origen/destino, cantidad, precio, plazo), para que ambas partes puedan evaluar el costo logístico real antes de aceptar, no solo el precio nominal.
- **Historial de contratos liquidados**: velas OHLC construidas a partir de contratos efectivamente cerrados (no de órdenes vivas), por producto y región, visibles para todos los jugadores como referencia de precio de mercado.
- **Costos estructurales**:
  - Costos logísticos (distancia, medio de transporte, congestión).
  - Costos laborales (salarios según región/nivel de automatización).
  - Costos energéticos (consumo de combustible por edificio; el combustible es un bien de mercado que viaja por logística, ver 5.8).
  - Costos de mantenimiento (degradación de edificios/vehículos).
- **Palancas macroeconómicas** (controladas por diseño/GMs o algoritmos de banco central del juego):
  - Impuestos y tarifas aduaneras entre regiones.
  - Emisión/absorción de moneda para controlar inflación/deflación.
  - Subsidios temporales a bienes estratégicos para evitar colapsos de mercado.

### 5.3 Contratos de Compraventa Respaldado por Inventario (CCRI)

El mercado no funciona mediante un order book anónimo, sino mediante **contratos bilaterales respaldados por stock real**, con el banco central del juego actuando como custodio de las garantías y árbitro de incumplimientos.

**Regla base:** solo se pueden publicar o aceptar contratos sobre **stock que ya existe físicamente** en un almacén del vendedor en el momento de la publicación/aceptación. No se permiten contratos sobre producción futura prometida; esto elimina el riesgo de que un vendedor incumpla por no haber podido fabricar a tiempo.

### 5.3.1 El tablón es único, global e interregional

No hay un tablón por región: existe **un único tablón de contratos, visible desde cualquier punto del mundo**. Cualquier jugador o bot, sin importar dónde esté ubicado, puede ver todas las ofertas y demandas publicadas en cualquier región, filtrando por producto, ubicación, precio o ventana de tiempo. Esto es precisamente lo que permite que la logística sea parte de la decisión: un comprador en la región A puede ver una oferta más barata en la región D y evaluar si el ahorro compensa el costo/tiempo de transporte, en vez de estar limitado a ofertas "locales".

**Publicación bilateral — ambos lados pueden publicar, no solo el vendedor:**

- **Ofertas de venta**: el vendedor publica una cantidad que ya tiene reservada/congelada en su almacén de origen, junto a precio y plazo. Su garantía en especie (el stock) y su garantía monetaria quedan bloqueadas **desde el momento de la publicación**, no solo al aceptar.
- **Solicitudes de compra**: el comprador también puede publicar en el mismo tablón una intención de compra (producto, cantidad, precio que está dispuesto a pagar, destino y plazo), con su **pago ya retenido en escrow desde el momento de publicar**, no solo al aceptar.

**Una garantía por publicación (v1.2):** cada publicación bloquea **íntegramente su propia garantía** (escrow monetario o stock congelado). La invariante clave —**toda publicación visible en el tablón es ejecutable al 100%**— se cumple así por construcción, sin contabilidad N:M de reservas en el ledger ni cancelaciones en cascada dentro de la transacción de aceptación (la ruta más crítica del sistema). Quien quiera explorar el mercado en varias regiones publica cantidades menores en cada una, o consigue más capital — lo cual es, además, gameplay. La **reserva compartida** (una garantía respaldando N publicaciones excluyentes) queda como expansión futura (sección 22), reintroducible de forma aditiva.

**Aceptación directa, total o parcial:** cualquier contraparte que vea la publicación puede **aceptarla directamente** (no hace falta negociar desde cero), y puede aceptar la **cantidad completa o una parcial**: aceptar K de N unidades divide la publicación en un contrato por K (con garantías proporcionales de ambos lados) y deja las N−K restantes publicadas. La aceptación empareja garantías ya bloqueadas, por lo que el contrato pasa de inmediato a la fase de ejecución logística. El publicador puede fijar opcionalmente un **lote mínimo de aceptación** para evitar micro-fragmentación de sus envíos.

**Ventana de sorteo (anti-sniping, v1.2):** las aceptaciones no se resuelven por orden de llegada. Toda publicación abre al publicarse una **ventana corta de aceptación** (30–60 segundos reales); al cierre, se **sortea un orden aleatorio entre todos los aceptantes** y se sirven en ese orden hasta agotar la cantidad (las garantías de los no servidos se liberan). Sobre una publicación ya madura (sin aceptaciones en su ventana inicial), la primera aceptación posterior abre una micro-ventana (15–30 s) en la que otras aceptaciones pueden concurrir antes del sorteo. Al no existir FIFO, **la latencia no otorga ninguna ventaja** — ni a los bots sobre los humanos, ni a scripts que suplanten cuentas humanas —, lo que elimina de raíz la necesidad de un sistema crítico de detección de automatización (esta decisión deroga la "ventana de prioridad humana" de la v1.1, ver Anexo B). Los bots conservan su función de backstop de liquidez: si nadie más acepta, el bot gana el sorteo en solitario.

**Cancelación de publicaciones:** una publicación no aceptada puede cancelarse sin coste (su garantía se libera), con un **cooldown breve anti-parpadeo** (p. ej. no se puede cancelar durante los primeros segundos tras publicar, ni republicar la misma oferta inmediatamente tras cancelarla) para impedir el "flickering" especulativo del tablón. La parte de una publicación ya convertida en contrato aceptado no es cancelable unilateralmente.

**Garantías bloqueadas (desde la publicación, no solo al aceptar):**

| Parte | Qué se bloquea | Propósito |
|---|---|---|
| **Vendedor** | La cantidad pactada queda **reservada/congelada** en su inventario (no puede revenderla ni destinarla a otro contrato) | Garantía en especie: elimina el riesgo de que el producto no exista |
| **Vendedor** | Una **garantía monetaria menor** (p. ej. 10% del valor del contrato) | Cubre el riesgo residual de que, aun teniendo el stock, la entrega física falle (logística cortada, vehículo perdido, cancelación de mala fe) |
| **Comprador** | El **100% del pago**, retenido en escrow por el banco central | Garantiza que el vendedor cobrará si cumple |

**Ciclo de vida del contrato:**

```
1. PUBLICACIÓN (con garantía propia ya bloqueada íntegramente)
   El vendedor publica una oferta de venta con su stock ya reservado y su
   garantía monetaria ya bloqueada; o el comprador publica una solicitud de
   compra con su pago ya retenido en escrow. Ambas visibles en el tablón
   global, o negociadas 1:1 entre partes conocidas.

2. ACEPTACIÓN (total o parcial; ventana de sorteo)
   Las aceptaciones concurren durante la ventana corta de la publicación;
   al cierre se sortea el orden entre los aceptantes y se sirven en ese
   orden hasta agotar la cantidad. Aceptar K de N unidades crea un contrato
   por K con garantías proporcionales y deja N−K publicadas. La garantía
   del aceptante se bloquea al aceptar y se libera si no resulta servido.

3. CONFIRMACIÓN ATÓMICA DE GARANTÍAS (el banco central actúa de custodio)
   Una única transacción en el ledger (ver 15.3 y 17.2) asienta a la vez:
   - Stock pactado movido a la cuenta de reserva del contrato.
   - Garantía monetaria del vendedor bloqueada.
   - Pago íntegro del comprador retenido en escrow.

4. EJECUCIÓN LOGÍSTICA
   El vendedor transporta físicamente la mercancía desde origen hasta
   destino dentro del plazo pactado (con flota propia o subcontratando
   fletes). El stock reservado viaja como CARGAMENTO etiquetado con el
   contract_id: deja de estar "en el almacén" y pasa a estar "en tránsito",
   sin dejar de estar reservado.

5. VERIFICACIÓN DE ENTREGA (acumulativa)
   El sistema logístico confirma cada llegada física parcial al nodo de
   destino y acumula la cantidad entregada dentro del plazo (un contrato
   puede cumplirse con varios envíos/vehículos).

6. LIQUIDACIÓN PRO-RATA (al completarse la cantidad o vencer el plazo)
   Se liquida lo efectivamente entregado a tiempo:
   a) La cantidad entregada se transfiere al comprador; el banco libera al
      vendedor el pago proporcional y la parte proporcional de su garantía.
      Fill del 100% = éxito pleno.
   b) Sobre la cantidad FALTANTE: el escrow proporcional vuelve íntegro al
      comprador; la garantía monetaria proporcional del vendedor se reparte
      —una parte compensa al comprador y otra se destruye como sanción
      (sink del banco central)—.
   c) El stock reservado no entregado se libera como stock libre del
      vendedor EN SU UBICACIÓN FÍSICA ACTUAL (almacén de origen, terminal
      intermedia o vehículo en tránsito, según dónde esté el cargamento).
      Nunca "regresa" instantáneamente al origen: nada se teletransporta,
      tampoco en los fallos.
```

**Transacciones instantáneas:** una compra en el mismo nodo, sin fricción logística, es simplemente un contrato con origen = destino y plazo mínimo; no requiere un mecanismo aparte.

**Garantía fija, sin sistema de reputación (v1.2):** la garantía monetaria del vendedor es un **porcentaje fijo** (10% del valor del contrato), igual para todas las cuentas. No existe en v1 un sistema de reputación (fill-rate) con efectos económicos ni informativos: un descuento de garantía por historial crea el incentivo a fabricar reputación con auto-contratos (wash-trading) y obliga a construir maquinaria anti-manipulación permanente (grafos de propiedad común entre cuentas, ponderación por diversidad de contrapartes). Se elimina el premio en lugar de vigilar al tramposo. La liquidación pro-rata ya acota el daño de un fallo parcial sin necesidad de historial. La reputación con efectos económicos queda como expansión futura (sección 22).

**Contratos privados vs. tablón abierto:** ambos usan exactamente el mismo mecanismo de garantías y liquidación; la única diferencia es si la oferta se publica abiertamente en el tablón (descubrible por cualquiera) o se negocia de forma directa y cerrada entre dos partes que ya se conocen (p. ej. relaciones de suministro estables entre socios comerciales habituales).

### 5.3.2 Contrato de Servicio de Transporte (CCRI-Flete)

Segundo y último tipo de contrato del sistema: permite subcontratar el transporte a terceros (jugadores transportistas o bots del arquetipo correspondiente) con las mismas garantías del CCRI de bienes.

- **Partes y garantías**: el **cargador** (dueño de la mercancía) paga el precio del flete a escrow del banco central; el **transportista** deposita una garantía monetaria proporcional al valor declarado de la carga.
- **Custodia asentada en el ledger**: al cargar, la mercancía pasa a una cuenta de **custodia** a nombre del contrato de flete — el transportista la lleva físicamente pero **no puede venderla ni destinarla a otro fin** (no es suya; el ledger lo impide contablemente). Esto permite componer fletes con CCRI de venta: un cargamento reservado por un contrato de venta de un tercero puede viajar en flota subcontratada sin romper ninguna garantía.
- **Publicación y liquidación**: los fletes se publican en el **mismo tablón** (filtrables como servicios), con la misma ventana de sorteo, aceptación parcial (por tramos o por tonelaje) y liquidación pro-rata contra confirmación de entrega. El fallo del transportista reparte su garantía entre compensación al cargador y sink.
- Los **slots de prioridad de terminales** (7.3) y los fletes componen el rol completo de "logística como servicio" prometido en las secciones 12 y 13.2.

### 5.4 Regionalización de precios

No existe un precio único global, pero sí un **tablón único global**: los precios varían naturalmente según la ubicación de cada oferta/solicitud, y el propio tablón —al ser interregional— es el mecanismo que hace visible esa variación y permite el **arbitraje logístico** (comprar barato en una región, transportar y vender en otra) como mecánica central de juego. El historial de contratos liquidados se agrega también por región para fines de referencia y estadística, y los "hubs" logísticos de alto tránsito tienden a converger en precios más estables por concentrar mayor volumen de ofertas visibles.

### 5.5 Control de inflación/deflación

- Sumideros de dinero (sinks): impuestos, mantenimiento, salarios de NPCs, garantías monetarias destruidas por incumplimiento de contratos.
- Fuentes de dinero (faucets): venta a compradores de última instancia (las ciudades, ver 5.6), contratos gubernamentales simulados, y —como mecanismo principal y contabilizado de emisión— la **capitalización de bots nuevos por el banco central** (ver 15.4: alta de bot = emisión asentada en el ledger; retiro de bot = absorción). El capital semilla de los jugadores nuevos es también un asiento de emisión explícito del banco central.
- Monitoreo continuo de masa monetaria vs. PIB simulado del mundo, con ajustes automáticos de tasas impositivas dentro de rangos predefinidos (similar a un banco central algorítmico).

### 5.6 Ciudades como consumidores finales y curva de demanda dinámica

Las **ciudades** son el sumidero final de la economía: no producen bienes industriales, solo los **consumen** para sostener y hacer crecer a su población. Son entidades permanentes del mundo (generadas junto con el mapa, ver sección 9) y constituyen, junto con los bots productores/comerciantes, el otro gran motor no-humano de la economía.

**Mecánica de demanda dinámica (inspirada en el modelo económico de Farming Simulator 25):**

- Cada ciudad mantiene una **curva de demanda por producto** que define cuánto está dispuesta a comprar y a qué precio, en vez de un precio fijo.
- **Oferta reciente vs. precio**: si un jugador (o varios) inunda a una ciudad con un producto (p. ej. alimentos, textiles, electrónica de consumo) por encima de su tasa de consumo, el **precio que la ciudad paga cae progresivamente**, reflejando saturación del mercado local. Si la oferta escasea, el precio sube, incentivando a los jugadores a redirigir producción hacia esa ciudad.
- **Estacionalidad (pospuesta, v1.2)**: los ciclos temporales de demanda (estaciones, festividades, eventos económicos) se retiran de la v1 y quedan como expansión futura (sección 22): en la Fase 1 ningún jugador tiene aún el capital de almacenamiento para explotarlos, y su retirada elimina un factor de la composición multiplicativa que la propia acotación de abajo señalaba como fuente de picos explotables.
- **Elasticidad en dos clases (v1.2)**: bienes **básicos** (alimentos, combustible) con curva inelástica (la ciudad sigue comprando aunque el precio suba, hasta un tope) y bienes de **lujo/secundarios** (electrónica avanzada, vehículos de consumo), elásticos y sensibles a saturación. Dos clases, no un parámetro de elasticidad por producto.

**Crecimiento de la ciudad y retroalimentación sobre la demanda:**

- Cada ciudad acumula un **índice de suministro histórico** (cantidad y variedad de bienes recibidos de forma sostenida en el tiempo).
- Al superar umbrales de este índice, la ciudad **sube de nivel** (población, huella urbana, número de distritos), lo que a su vez:
  - Incrementa la **demanda base** (`D0`) de los productos que ya consumía.
  - **Desplaza/ensancha la curva de demanda** hacia la derecha (tolera mayor volumen antes de saturarse) y hacia arriba (mayor disposición a pagar un precio base más alto).
  - **Desbloquea nuevas categorías de consumo** propias de ciudades más desarrolladas (p. ej. una ciudad de nivel bajo solo demanda alimentos y combustible básico; una ciudad de nivel alto demanda también electrónica, vehículos de consumo y bienes de lujo).
  - Aumenta la mano de obra disponible en la región para las industrias cercanas.
- Si una ciudad deja de recibir suministro constante (abandono logístico), su índice decae con el tiempo, pudiendo **bajar de nivel** y reducir su demanda, lo que introduce riesgo para los jugadores que dependen de esa ciudad como mercado.

**Modelo funcional simplificado:**

```
Demanda_efectiva(producto, ciudad, t) =
    D0(producto, nivel_ciudad)
    × factor_saturación(oferta_reciente(producto, ciudad, ventana_tiempo))

precio_pagado_por_ciudad(producto) =
    precio_base(producto) × (Demanda_efectiva / Oferta_reciente) ajustado por elasticidad
```

- `factor_saturación` decae cuando la oferta reciente supera la demanda base (excedente vendido a la ciudad), y se recupera con el tiempo si la oferta baja, evitando que los jugadores "spameen" una ciudad para explotar precios altos indefinidamente.
- **Acotación obligatoria**: todos los factores y el precio resultante están **acotados por clamps min/max por producto**, y `Oferta_reciente` se calcula como media móvil exponencial con un **suelo mínimo** (nunca cero) — sin estas cotas, una ciudad sin suministro reciente produciría precios que tienden a infinito.
- `D0(producto, nivel_ciudad)` crece de forma escalonada con cada nivel de ciudad, siguiendo una progresión similar a la de capacidad industrial (sección 6.3), pero aplicada a consumo en vez de producción.

**Integración logística:** al igual que cualquier otro comprador, una ciudad **no recibe bienes por arte de magia**: requiere que exista infraestructura de distribución conectada (almacenes, centros de distribución o terminales) dentro de su radio de influencia. Los jugadores venden a la ciudad mediante el mismo mecanismo de **contrato respaldado por inventario** (sección 5.3): la ciudad publica o acepta contratos de suministro urbano como cualquier otra entidad, con la particularidad de que su garantía de pago está siempre pre-fondeada por el banco central (una ciudad nunca incumple el pago), y su demanda/precio de aceptación varían según la curva descrita arriba.

**Rol económico:** las ciudades son el **único consumidor final** del sistema (v1.2: se elimina el antiguo arquetipo de bot "consumidor NPC", redundante con ellas) — una entidad de mundo con identidad propia, ubicación fija, nivel de desarrollo y su propia curva de demanda por producto, cuyo crecimiento es un objetivo estratégico observable por todos los jugadores de la región.

### 5.7 Costo laboral regional (fórmula, v1.2)

- Cada instalación requiere trabajadores de una ciudad en cuyo radio de influencia se encuentre. Los trabajadores **no se transportan**: son un recurso regional, no un bien logístico.
- La v1 **no simula un pool asignable ni una subasta salarial**: el costo laboral es una fórmula — `salario_efectivo = salario_base(nivel_ciudad) × factor_saturación(ocupación_industrial_regional)` — recalculada periódicamente por el Economy Balancer. Siempre hay trabajadores disponibles; en regiones industrialmente saturadas se paga mucho más (presión para automatizar —6.3—, desarrollar la ciudad o migrar a otra región).
- El bucle ciudad↔industria se conserva en ambas direcciones: hacer crecer una ciudad (5.6) reduce el `salario_base` efectivo y eleva el techo de industria local; la cualificación que exigen ciertas recetas se liga al nivel de la ciudad cercana (una receta avanzada requiere una ciudad de nivel suficiente).
- El **pool finito con asignación por prioridad salarial** (mercado laboral emergente, con plantillas sin cubrir y producción parada) queda como expansión futura (sección 22), a reintroducir si la fórmula resulta estratégicamente plana.

### 5.8 Energía: combustible in situ en v1; red eléctrica pospuesta a Fase 3

La v1 **no incluye red eléctrica** (decisión v1.2: era el mayor añadido de alcance de la v1, y su degradación acordada asciende a plan base):

- **Combustible in situ**: cada edificio con consumo energético tiene un almacén de combustible local y **consume combustible físico (carbón/fuel) entregado por logística**, como cualquier otro insumo — la energía no rompe el pilar logístico. El "precio de la energía" de una región es el precio de mercado del combustible más su costo real de transporte hasta cada edificio.
- **Sin combustible, la producción pausa**: la consecuencia visible para el jugador es la misma que la de un apagón, sin necesidad del subsistema de red.
- **Red eléctrica regional (Fase 3)**: el diseño acotado de la red se conserva íntegro como especificación para su activación en Fase 3 — centrales construibles (térmicas a combustible físico, hidroeléctricas), líneas de transmisión con huella y mantenimiento, mercado spot regional por orden de mérito (el precio de cierre lo pagan todos los despachados) y recorte rotatorio por prioridad inversa de precio ante déficit. Sin flujos de potencia realistas, sin pérdidas por distancia, sin almacenamiento; interconexiones interregionales y baterías siguen siendo expansión futura (sección 22).

### 5.9 Insolvencia: parada progresiva, nunca deuda

El saldo de una corporación **nunca baja de cero** y no existe crédito en la v1 (los préstamos son expansión futura: bancos, sección 22). Al agotarse el saldo, las obligaciones dejan de pagarse en cascada, cada una con su consecuencia ya definida:

```
saldo = 0
  1º Salarios impagados      → los trabajadores abandonan; producción parada
  2º Combustible/energía     → sin compras; edificios sin suministro pausan
  3º Mantenimiento impagado  → degradación progresiva (11.2)
  4º Canon impagado          → periodo de gracia → EMBARGO → subasta (11.2)
```

El jugador que regresa tras una ausencia larga encuentra **menos imperio, nunca una deuda**: sus obligaciones se saldaron con su patrimonio vía el ciclo de embargo. Las garantías de contratos ya bloqueadas no se ven afectadas (ya estaban apartadas en el ledger — el CCRI nunca depende de la solvencia futura de las partes).

---

## 6. Sistema industrial

### 6.1 Cadenas productivas

Las cadenas son fijas en estructura (recetas conocidas desde el inicio) pero **flexibles en configuración**: el jugador decide qué eslabones integrar verticalmente y cuáles comprar en el mercado.

Ejemplo simplificado:

```
Mina de Hierro → Mineral de Hierro
Mineral de Hierro + Carbón → Lingotes de Acero (Alto Horno)
Lingotes de Acero → Componentes (Planta de Componentes)
Componentes + Electrónica → Maquinaria (Fábrica de Maquinaria)
Maquinaria + Componentes → Vehículos (Ensambladora)
```

### 6.2 Atributos de cada instalación

- Receta(s) soportadas (algunas plantas son multi-receta con changeover time).
- Tiempo de producción por lote.
- Consumo de combustible por ciclo (energía, ver 5.8).
- Requerimiento de trabajadores (cantidad y nivel de cualificación).
- Capacidad de almacenamiento interno (buffer de insumos/productos).
- Cola de producción (encolar múltiples lotes/recetas).
- Nivel de mejora (1 a N), afectando líneas paralelas, velocidad y eficiencia.

### 6.3 Progresión por escala, no por desbloqueo

| Nivel | Líneas | Velocidad | Eficiencia energética | Automatización |
|---|---|---|---|---|
| 1 | 1 | Base | Base | Manual |
| 2 | 2 | +25% | +10% | Semi-automática |
| 3 | 4 | +60% | +25% | Automática |
| 4 | 8 | +120% | +40% | Totalmente automatizada |

Los costos de mejora crecen de forma no lineal (curva exponencial suavizada), forzando decisiones de inversión estratégica en vez de "mejorar todo siempre".

### 6.4 Especialización vs. integración vertical

El jugador puede:
- **Integrar verticalmente**: controlar toda la cadena, reduciendo dependencia del mercado pero aumentando capital inmovilizado y complejidad logística interna.
- **Especializarse**: dominar un solo eslabón con altísima eficiencia y vender/comprar el resto en el mercado, dependiendo de la robustez logística global.

---

## 7. Sistema logístico

### 7.1 Principio fundamental

**Ningún bien se mueve sin transporte físico.** Comprar en el mercado reserva el bien, pero la entrega requiere una ruta logística ejecutable con capacidad disponible.

### 7.2 Componentes

- **Nodos**: minas, fábricas, almacenes, puertos, estaciones, centros de distribución.
- **Enlaces**: carreteras, vías férreas, rutas marítimas — cada uno con capacidad máxima y velocidad. (El transporte aéreo se retira del alcance base en v1.2: expansión futura, sección 22.)
- **Vehículos**: camiones, trenes, barcos, cada uno con capacidad de carga, velocidad, consumo, autonomía y costo operativo.
- **Rutas**: secuencias de enlaces desde origen a destino, potencialmente multimodales (camión → tren → barco → camión), con transbordos en terminales intermodales.

### 7.3 Simulación de tránsito

- Cada vehículo tiene una posición física simulada a lo largo de su ruta (no teletransporte instantáneo al llegar la orden). Conforme al motor event-driven (sección 1.1), la posición se representa de forma **analítica** (`tramo + t_entrada + función de avance`) y solo los **hitos** (salida, llegada a nodo, cruce de frontera de región, avería, cambio de velocidad por congestión) generan eventos; la posición exacta se deriva bajo demanda para los clientes que observan el área.
- **La simulación de tránsito la ejecuta el shard espacial**: cada shard simula el movimiento, las averías y la congestión de los vehículos y enlaces dentro de su región (ver 15.1). Un enlace que cruza una frontera se divide en segmentos en el punto de cruce, y cada shard simula la congestión de su segmento.
- La **congestión** reduce la velocidad efectiva en enlaces sobrecargados (modelo tipo "flujo de tráfico" simplificado, similar a OpenTTD), recalculada como evento recurrente de baja frecuencia por enlace (cada 30–60 s de sim-time) y **suavizada con media móvil exponencial** para evitar oscilaciones de enrutamiento (estampidas de vehículos redirigiéndose todos al enlace "libre", congestionándolo, y vuelta a empezar).
- Los **transbordos** consumen tiempo y capacidad en terminales, y pueden generar colas. Los **enlaces son de uso común** (FIFO + congestión física; no existen reservas exclusivas de vía, eliminando el acaparamiento hostil de capacidad como vector de griefing); en cambio, las **terminales tienen dueño**, y este puede vender **slots de prioridad** de atraque/transbordo — el gameplay de "infraestructura como servicio" vive en los nodos, no en las vías.
- **Avería con carga contractual (v1.2: sin rescate en v1)**: un vehículo averiado se detiene en su posición, paga un costo de reparación y **reanuda tras un tiempo de reparación**; su cargamento (incluido el reservado por contrato) espera a bordo. La avería es tiempo perdido, no carga perdida — exactamente el riesgo residual que cubre la garantía monetaria del vendedor en el CCRI. El **rescate/transbordo** de la carga por otro vehículo del dueño o por un flete de terceros se introduce junto con el CCRI-Flete (Fase 2); solo la destrucción del vehículo por eventos extraordinarios (expansiones futuras: desastres) destruye el cargamento.

### 7.4 Planificación de rutas

- Los jugadores diseñan rutas manualmente o usan un asistente de "ruta óptima" que sugiere el camino de menor costo/tiempo dada la infraestructura existente.
- El pathfinding es **jerárquico** (estilo HPA*/contraction hierarchies): la grilla de macro-regiones actúa como nivel superior del grafo (rutas interregionales se planifican región-a-región y se refinan localmente), lo que evita ejecutar Dijkstra/A* plano sobre el grafo mundial en cada consulta. Los pesos usan la congestión **suavizada** (EMA) publicada por los shards, y la **replanificación solo ocurre en hitos** (llegada a nodo, evento de congestión severa), nunca de forma continua.
- Los bots usan el mismo motor de pathfinding, sin atajos privilegiados.
- El planificador expone **ETAs estimadas** (usadas para decidir plazos de contrato); son estimaciones informativas, no garantías: el riesgo de que la congestión real supere la prevista lo asume quien pactó el plazo (y lo cubre su garantía monetaria).

---

## 8. Gestión de flotas

- **Compra/venta de vehículos** en un mercado secundario (con depreciación).
- **Asignación de rutas fijas** (líneas regulares, como en OpenTTD) o **rutas dinámicas bajo demanda** (como servicios de carga a pedido).
- **Mantenimiento programado**: cada vehículo acumula desgaste; sin mantenimiento, aumenta la probabilidad de avería (parada no programada).
- **Combustible/energía**: consumo variable según carga, distancia y tipo de vehículo; el precio del combustible es también un bien de mercado.
- **Escalado de flotas**: los jugadores grandes gestionan cientos de vehículos mediante paneles de control agregados (vista de flota completa, alertas automáticas, reglas de reabastecimiento automático).

---

## 9. Generación procedural del mundo

- **Algoritmo base**: ruido Perlin/Simplex por capas para elevación, humedad y temperatura, determinando biomas (bosques, desiertos, montañas, océanos).
- **Ríos**: trazados por descenso de gradiente desde puntos de alta elevación hasta el mar, usado también para rutas fluviales de transporte.
- **Ciudades**: generadas en puntos de alta accesibilidad (cercanía a costa, ríos, llanuras), sirviendo como fuente de mano de obra y como **consumidores finales persistentes** de la economía. Cada ciudad nace con un nivel inicial bajo y crece con el tiempo en función del suministro sostenido que recibe (ver sección 5.6), expandiendo su huella urbana, su población y su radio de influencia logística.
- **División en regiones**: el mundo se particiona en una grilla de macro-regiones (p. ej. 500×500 celdas cada una), cada una gestionada por un shard de simulación — unidad lógica del motor; todos los shards corren en un único proceso hasta que la medición exija extraerlos (ver sección 15).
- **Determinismo**: el mundo se genera una única vez a partir de una semilla y luego se persiste; la generación procedural solo define el estado inicial, no se regenera dinámicamente.

---

## 10. Sistema de recursos naturales

- Distribución no uniforme de yacimientos (hierro, carbón, petróleo, minerales) y recursos renovables (madera, agua, tierra fértil), generados proceduralmente con ruido correlacionado a biomas (petróleo en desiertos/plataformas marinas, madera en bosques, etc.).
- Cada yacimiento tiene una **cantidad finita o tasa de regeneración** (recursos renovables como bosques se regeneran con el tiempo si no se sobreexplotan; los minerales son **estrictamente finitos y se agotan a cero**).
- **Modelo de agotamiento asumido (finito estricto + expansión de mapa):** la válvula frente al agotamiento global no es la regeneración ni la prospección de vetas ocultas, sino la **expansión territorial del mundo** (apertura periódica de nuevas regiones, ver Fase 4 en la sección 21). Consecuencias aceptadas por diseño:
  - Las regiones mineras **declinan y mueren económicamente** cuando sus vetas se agotan; la migración industrial resultante es gameplay intencional (auge y decadencia de regiones, ciudades que bajan de nivel al perder su base industrial).
  - Los ciclos de **land rush** hacia cada expansión son esperables; el régimen de concesiones (11.1) impide que los primeros colonos acaparen el territorio nuevo a perpetuidad.
  - La salud a largo plazo del mundo queda **acoplada al calendario de expansiones del estudio** — riesgo operativo asumido y registrado en la sección 20; requiere monitorizar el ritmo de agotamiento global (métrica del Economy Balancer) para planificar expansiones con antelación.
- La escasez local de un recurso incentiva el comercio interregional y la exploración de nuevas zonas, alimentando la mecánica de expansión territorial.

---

## 11. Modelo de infraestructura

Cada instalación construible tiene:

- **Huella espacial** (footprint) en el grid del mundo.
- **Requisitos de emplazamiento** (cercanía a recursos, agua, acceso vial; conexión eléctrica cuando la red se active en Fase 3, ver 5.8).
- **Conexiones** a la red logística (debe tener al menos un enlace de acceso).
- **Propiedad**: el edificio pertenece a un jugador/corporación. El suelo sobre el que se asienta es siempre una concesión del sistema (ver 11.1).
- **Estado**: operativa, en construcción, dañada, en mantenimiento, abandonada, en embargo (ver 11.2).

### 11.1 Régimen de suelo: concesiones del sistema

No existe propiedad perpetua del suelo. Todo terreno se obtiene como **concesión renovable del sistema** (plazo de referencia: 90 días de juego), con las siguientes reglas:

- El **canon de concesión** se paga periódicamente al sistema y es un **sink monetario** estructural (sección 5.5). Su precio varía por ubicación (cercanía a ciudades, recursos, infraestructura) y puede ajustarlo el Economy Balancer dentro de rangos predefinidos.
- La concesión es **traspasable entre jugadores** (mercado secundario de traspasos, con el sistema cobrando una tasa), pero no acumulable pasivamente: el impago sostenido produce **reversión automática** al sistema.
- Esto neutraliza el *land-banking* (acaparar suelo sin usarlo es un coste recurrente, no una inversión pasiva) y garantiza que el suelo liberado por jugadores inactivos **rota** hacia jugadores activos — condición necesaria en un mundo que nunca se resetea, y complemento del modelo de expansión territorial (sección 10): el territorio de las expansiones nuevas tampoco puede ser acaparado a perpetuidad por los primeros en llegar.

### 11.2 Ciclo de abandono, embargo y subasta

Cuando un jugador deja de pagar mantenimiento y/o canon de concesión:

```
OPERATIVA → (impago de mantenimiento) → DEGRADACIÓN progresiva
          → (impago sostenido)        → ABANDONADA (inoperativa)
          → (periodo de gracia)       → EMBARGO por el sistema
          → SUBASTA PÚBLICA vía CCRI
```

- En el **embargo**, el edificio y su contenido pasan a custodia del sistema. El **stock libre** del edificio se publica automáticamente en el tablón como **ofertas de venta del sistema** (origen: el propio almacén embargado, retirada in situ) — reutilizando el mecanismo CCRI estándar, sin mecánica nueva. Lo recaudado se aplica a las deudas del moroso y el remanente se destruye como sink.
- El **stock reservado por contratos vivos** sigue las reglas normales del CCRI: el contrato fallará por no-entrega y el stock se liberará in situ (sección 5.3), incorporándose entonces a la subasta.
- El **edificio** (y el traspaso de su concesión de suelo) se subasta igualmente; el jugador que vuelve tras una ausencia larga encuentra sus deudas saldadas con su patrimonio, no una deuda impagable.
- El **periodo de gracia** antes del embargo se calibra para distinguir vacaciones de abandono real (orden de magnitud: semanas reales, no días).

---

## 12. Sistema de comercio

Todo el comercio del juego se canaliza a través de un único mecanismo: el **Contrato de Compraventa Respaldado por Inventario (CCRI)** descrito en la sección 5.3. No existe un order book anónimo independiente; lo que varía es el **canal de descubrimiento** de la contraparte, no las reglas de liquidación:

- **Tablón de contratos (global e interregional)**: un único tablón, visible desde cualquier región, donde **tanto vendedores como compradores publican** —ofertas de venta con stock ya reservado, o solicitudes de compra con pago ya en escrow—, permitiendo comparar precio y costo logístico real antes de aceptar, y descubrir oportunidades de arbitraje entre regiones distantes.
- **Negociación directa/contratos privados**: acuerdos bilaterales cerrados entre dos partes que ya se conocen (p. ej. relaciones de suministro estables entre socios comerciales habituales), con condiciones a medida de precio, volumen, duración y penalizaciones, usando exactamente el mismo mecanismo de garantías y liquidación del tablón abierto.

**Regla central:** comprar ≠ recibir. Todo contrato exige stock real reservado por el vendedor y se liquida solo contra confirmación física de entrega, generando una capa de riesgo/oportunidad (fletes vía CCRI-Flete —ver 5.3.2—, intermediación logística como servicio para terceros, y la posibilidad de elegir contrapartes por conveniencia logística y no solo por el mejor precio nominal).

---

## 13. Diseño de bots e integración con jugadores humanos

### 13.1 Principio de igualdad de API

Los bots **no tienen acceso privilegiado**. Consumen exactamente la misma capa de API pública/interna que los clientes de jugadores humanos: construir, producir, colocar órdenes, asignar rutas, etc. Esto garantiza balance y permite usar bots como "stress test" fiel del sistema real.

### 13.2 Arquetipos de bots

- **Productores primarios**: extraen recursos naturales y venden materia prima al mercado.
- **Transformadores industriales**: compran insumos, producen bienes intermedios/finales.
- **Comerciantes/arbitrajistas**: detectan diferencias de precio interregional y ejecutan logística de arbitraje.
- **Transportistas**: ofrecen servicios de flete a otros agentes.

(v1.2: el antiguo arquetipo "consumidor NPC" se elimina por redundante — el consumo final es exclusivamente de las **ciudades**, ver 5.6.)

### 13.3 Toma de decisiones

- **Bots simples** (productores primarios): reglas heurísticas (umbrales de inventario, reabastecimiento automático).
- **Bots intermedios** (industriales): optimización basada en reglas + programación lineal simple para maximizar margen dado el estado del mercado.
- **Bots avanzados** (arbitrajistas): heurísticas más sofisticadas (detección de diferenciales de precio interregionales, optimización de rutas de arbitraje, gestión de capital). Se ejecutan de forma **permanente en el servidor de producción** como jugadores más de la economía, con los mismos límites de tasa y las mismas reglas que cualquier corporación humana.

### 13.4 Modos de operación de los bots

Los bots no están confinados a un entorno de pruebas: son **residentes permanentes del mundo en producción**, con tres modos de uso que conviven:

1. **Modo "mundo vivo" (producción, permanente)**: una población base de bots de todos los arquetipos (productores primarios, transformadores, comerciantes/arbitrajistas, transportistas) opera de forma continua en el servidor real, junto a los jugadores humanos, manteniendo la economía activa incluso con poca concurrencia humana y aportando profundidad competitiva incluso con alta concurrencia.
2. **Modo "densidad dinámica" (producción, permanente)**: el número y la agresividad de los bots activos se ajusta de forma continua según la actividad humana real en cada región, para evitar tanto la sobresaturación del mercado como la escasez de contrapartes comerciales.
3. **Modo "stress test" (entorno separado, temporal)**: instancias efímeras del motor de simulación con cientos de miles o millones de bots, corridas en un **entorno de pruebas independiente** (no en el mundo de producción), usadas exclusivamente para validar escalabilidad y balance de nuevas mecánicas *antes* de desplegarlas al servidor en vivo. Este modo es adicional al punto 1 y 2, no un sustituto: los bots de producción permanecen activos indefinidamente en el mundo real.

---

## 14. Arquitectura multijugador

### 14.1 Modelo cliente-servidor autoritativo

- El **servidor es la única fuente de verdad** (authoritative server); el cliente solo envía intenciones (comandos) y renderiza el estado recibido.
- Comunicación mediante:
  - **WebSocket** para eventos en tiempo real (movimiento de vehículos, actualizaciones de mercado, alertas).
  - **API REST** para operaciones no urgentes (construcción, configuración de recetas, contratos).
- **Interpolación en cliente** para suavizar el movimiento de vehículos entre actualizaciones de servidor (similar a técnicas de netcode de MMOs).

### 14.2 Sesión y presencia

- Cada jugador conectado se suscribe solo a los canales de eventos relevantes para su "área de interés" (su base, sus rutas activas, su región de mercado), evitando que el cliente reciba tráfico de todo el mundo.
- Los cambios fuera del área de interés se resuelven server-side y se reflejan de forma agregada (p. ej., "resumen de mercado" en lugar de cada transacción individual).
- **El tablón global no contradice el área de interés**: el tablón se consulta bajo demanda (petición/consulta con filtros), nunca por suscripción push a todos los eventos de mercado del mundo. Las suscripciones push se limitan al área de interés del jugador y a alertas explícitas que él configure (p. ej. "avísame si aparece acero por debajo de X en la región D").

---

## 15. Arquitectura distribuida para simulación masiva

### 15.1 Particionamiento del mundo (sharding espacial)

- El mundo se divide en **macro-regiones** (celdas de una grilla gruesa), cada una gestionada por un **shard de simulación**: una **unidad lógica** del motor (módulo con estado, cola de eventos y sim-time propios). **En el plan base todos los shards corren dentro de un único proceso** (v1.2): con el techo de capacidad declarado (sección 19) y el motor event-driven (coste ∝ eventos, no ∝ entidades), un proceso puede llevar el mundo entero; la separación en procesos dedicados es una **extracción medida** (18.4), no un compromiso de fase.
- **Decisión asumida: región de gameplay = unidad de sharding.** La región es a la vez jurisdicción de juego (impuestos, aduanas, estadísticas visibles) y unidad técnica de particionamiento; no se contempla subdividir una región caliente en celdas menores. Trade-off aceptado: un hotspot extremo (gran ciudad + yacimientos valiosos concentrando jugadores) solo puede mitigarse con (a) **escalado vertical** del shard de esa región, (b) **diseño del mapa** que disperse deliberadamente los atractores (recursos de primer nivel nunca concentrados en una sola región), y (c) **palancas fiscales como válvula de dispersión**: el Economy Balancer puede encarecer impuestos y canon de concesión en regiones saturadas, empujando la actividad nueva hacia regiones vecinas (congestion pricing). Riesgo registrado en la sección 20.
- Cada shard es responsable de:
  - Física/estado de edificios y recursos de su región.
  - **Simulación de tránsito completa** de los vehículos actualmente dentro de su región: movimiento, averías y congestión de los enlaces/segmentos de su territorio (los enlaces fronterizos se dividen en el punto de cruce y cada shard simula su segmento).
  - Eventos locales (congestión de tráfico local).
- El Logistics Service global **no simula tránsito**: es un servicio de planificación sin estado de movimiento (ver 18.1).

### 15.2 Fronteras y transferencia de entidades

- Gracias al motor event-driven (sección 1.1), el cruce de frontera es un **evento discreto**, no una sincronización continua. **Mientras todos los shards conviven en un único proceso (plan base v1.2), el cruce es un simple traspaso local entre colas de eventos** — sin protocolo de red. Para el despliegue multi-proceso (extracción medida, 18.4) queda **especificado pero no construido** el siguiente protocolo formal de handoff:

```
1. SELLADO   El shard A marca el vehículo como SELLADO (inmóvil para la
             simulación local, no acepta comandos del jugador) y emite
             transfer(transfer_id único e idempotente, estado completo
             del vehículo y su cargamento, sim-time del cruce).
2. COPIADO   El shard B persiste la entidad en estado INACTIVO y responde
             ACK(transfer_id). Reintentos con el mismo transfer_id son
             idempotentes (re-enviar nunca duplica).
3. ACTIVADO  Al recibir el ACK, A emite CONFIRM; B activa la entidad, que
             continúa su ruta desde el punto y sim-time exactos del cruce.
4. PURGADO   A elimina su copia. Si A muere antes de purgar, el replay de
             su log re-emite transfer(transfer_id) y B lo ignora (ya lo
             tiene): nunca hay dos copias activas.
```

- **El ledger de bienes actúa de árbitro**: el cargamento a bordo está representado como cuentas en el ledger (sección 15.3), de modo que una duplicación física accidental sería una violación contable detectable de inmediato (más stock físico que asientos), no un bug silencioso descubierto en una "reconciliación periódica".
- Durante el handoff (típicamente < 1 s) el vehículo es visible pero no comandable; el jugador no percibe la frontera.
- La asignación región→proceso se gestiona por **configuración explícita y versionada** (etcd solo si la coordinación manual se queda corta, ver Anexo); el mapa región→shard cambia solo durante la ventana de mantenimiento diaria (sección 1.1), nunca de forma concurrente con handoffs en vuelo.

### 15.3 Contratos y garantías como servicio separado

- No existe un motor de emparejamiento (matching engine) de order book; en su lugar, un **servicio de Contratos y Garantías** gestiona el ciclo de vida completo del CCRI (publicación en el tablón, bloqueo de garantías, aceptación, verificación de entrega vía el servicio de logística, y liquidación o sanción). Este servicio **no vive dentro de los shards espaciales**; es independiente, optimizado para consistencia transaccional fuerte (dado que mueve dinero y bloquea inventario real, similar a un sistema de custodia/escrow financiero más que a un exchange de alta frecuencia).
- **Tablón global sobre almacenamiento particionado**: aunque el tablón se presenta al jugador como un único espacio global e interregional, internamente se particiona por producto (y opcionalmente por macro-zona) para escribir/leer en paralelo sin cuellos de botella. Un **índice de búsqueda** con consistencia eventual agrega las particiones y responde consultas cross-región en baja latencia, de forma que un jugador en cualquier punto del mundo pueda encontrar ofertas o solicitudes publicadas en cualquier otra región. En las Fases 0–1 este índice es simplemente PostgreSQL con índices apropiados (ver 18.4); un motor dedicado (p. ej. Meilisearch con filtros por producto+ubicación) solo se adopta si la escala medida lo exige.
- **Inventario comprometible como cuentas del ledger**: el saldo "disponible para contratos" de cada almacén se modela como **cuentas de stock en el mismo ledger de doble entrada que el dinero** (partidas por `producto + almacén`, con cuentas espejo de reserva por contrato). Así, el bloqueo triple del CCRI (stock reservado + garantía monetaria + escrow) es **una única transacción ACID en una única base** — no una transacción distribuida entre el shard y el ledger. El shard espacial sigue siendo el dueño de la **física** del stock (en qué edificio o vehículo está, cuánto ocupa, cómo se mueve); la producción y el consumo asientan sus altas/bajas en el ledger mediante eventos, con **reconciliación periódica** entre el inventario físico del shard y las cuentas del ledger (toda discrepancia es una violación contable detectable, no una pérdida silenciosa).
- Las **garantías bloqueadas** (stock, garantía monetaria, escrow) requieren por tanto consistencia fuerte y viven en la base transaccional del ledger (sección 17), separada del índice de búsqueda, que solo necesita consistencia eventual para efectos de descubrimiento. (v1.2: sin reservas compartidas — cada publicación tiene su propia garantía íntegra, ver 5.3.1 —, la transacción de aceptación no arrastra cancelaciones en cascada de publicaciones hermanas.)

### 15.4 Simulación de bots masiva

- Los bots de **producción** (modo "mundo vivo" y "densidad dinámica") corren como **procesos externos al motor**, dentro del Bot Orchestration Service, y acceden al juego por **la misma API que los clientes humanos** — mismos endpoints, mismos rate limits lógicos — a través de un camino de red interno barato (conexiones multiplexadas, sin TLS/edge por bot). El shard no distingue si un comando proviene de humano o bot: la igualdad de API es literal, y los bots ejercitan permanentemente el camino real del sistema. Su volumen se escala junto con la carga del mundo.
- **Capitalización contabilizada por el banco central**: todo bot nace con capital **emitido explícitamente por el banco central** (asiento en el ledger — este es, de hecho, el mecanismo principal de emisión de moneda de 5.5); al retirar un bot, sus activos se liquidan por el ciclo estándar de embargo/subasta (11.2) y su efectivo restante se **destruye** (absorción). La densidad dinámica de bots y la política monetaria son así **una sola contabilidad**, visible para el Economy Balancer — nunca un grifo oculto.
- Los bots de **modo stress test** corren, de forma adicional y temporal, en un **cluster de simulación desacoplado** que se conecta a las mismas APIs pero puede escalarse horizontalmente de forma independiente al mundo de producción (útil para pruebas de carga masiva sin arriesgar el servidor en vivo). Este cluster no reemplaza a los bots de producción, que siguen activos de forma permanente.

### 15.5 Consistencia

- **Dentro de un shard**: estado autoritativo en memoria con orden estable de eventos (sección 1.1); la durabilidad la da el snapshot periódico (RPO de minutos aceptado solo para el estado físico, v1.2), con el ledger ACID como respaldo de todo valor económico — no una transacción por cambio de estado ni replay determinista obligatorio.
- **Consistencia eventual** entre shards y hacia el mercado global, propagando cambios de estado relevantes (llegada de mercancía, finalización de producción) por el bus de eventos (outbox sobre PostgreSQL en Fases 0–1; Kafka en Fase 2+, ver 18.4).
- El dinero y los bienes usan un modelo de "ledger" transaccional de doble entrada (15.3), con **consistencia fuerte ACID** para todo bloqueo de garantías, escrow y custodia — evitando duplicación o pérdida de recursos ante fallos parciales.

### 15.6 Diagrama de alto nivel

```
                     ┌────────────────────┐
                     │   API Gateway /     │
                     │   Autenticación      │
                     └─────────┬───────────┘
                               │
        ┌──────────────────────┼───────────────────────┐
        │                      │                        │
┌───────▼────────┐   ┌─────────▼─────────┐   ┌──────────▼─────────┐
│  Shards de      │   │  Servicio de       │   │  Servicio de        │
│  Simulación     │   │  Contratos y        │   │  Logística/Rutas     │
│  (por región,   │   │  Garantías          │   │  (pathfinding         │
│   incl. tránsito│   │  (tablón, escrow,    │   │   jerárquico, ETAs;   │
│   de vehículos) │   │   liquidación)       │   │   sin estado tránsito)│
└───────┬────────┘   └─────────┬─────────┘   └──────────┬─────────┘
        │                      │                        │
        └──────────────────────┼────────────────────────┘
                               │
                     ┌─────────▼───────────┐
                     │  Bus de eventos       │
                     │  (outbox → Kafka,     │
                     │   ver 18.4)           │
                     └─────────┬───────────┘
                               │
                     ┌─────────▼───────────┐
                     │  Capa de persistencia │
                     │  (bases distribuidas)  │
                     └───────────────────────┘
```

---

## 16. Modelo de entidades

| Entidad | Atributos clave |
|---|---|
| **Jugador/Corporación** | id, nombre, saldo |
| **Edificio** | id, tipo, ubicación (región+coordenadas), nivel, receta activa, cola de producción, estado, dueño |
| **Recurso natural (yacimiento)** | id, tipo, ubicación, cantidad restante/tasa de regeneración |
| **Vehículo** | id, tipo, capacidad, posición analítica (tramo + t_entrada + función de avance, ver 1.1), cargamentos a bordo, ruta asignada, combustible, estado de desgaste, dueño |
| **Ruta logística** | id, secuencia de enlaces, medio(s) de transporte, dueño, congestión estimada por tramo (informativa, no reservada) |
| **Enlace de red** | id, tipo (carretera/vía/marítima), nodos extremos, capacidad, velocidad base, congestión actual (EMA), región(es) que lo simulan (segmentado en fronteras) |
| **Terminal** | id, tipo (puerto/estación/intermodal/centro de distribución), dueño, capacidad de transbordo, cola actual, slots de prioridad vendibles |
| **Publicación de tablón** | id, tipo (oferta de venta/solicitud de compra), producto, cantidad restante, precio, origen o destino, plazo, publicador, reserva de garantía propia, lote mínimo de aceptación, estado de la ventana de sorteo (apertura/cierre, aceptantes en espera) |
| **Contrato de compraventa (CCRI)** | id, publicación de origen (si la hay), producto, cantidad pactada, cantidad entregada acumulada, precio, origen, destino, plazo (sim-time), partes (comprador/vendedor), cuentas de reserva en el ledger (stock reservado, garantía monetaria, escrow), estado (aceptado/en ejecución/liquidado con fill %), canal (tablón abierto/negociación directa) |
| **Cargamento (shipment)** | id, contrato asociado (si transporta stock reservado), producto, cantidad, vehículo actual, ubicación física actual (nodo o enlace+progreso), estado (en almacén/en tránsito/en terminal/entregado/liberado in situ) |
| **Bot** | id, arquetipo, parámetros de comportamiento, entidad "jugador" asociada (misma tabla que jugadores humanos) |
| **Concesión de suelo** | id, parcela(s), concesionario, canon periódico, vencimiento (sim-time), estado (vigente/morosa/en gracia/revertida), historial de traspasos |
| **Región del mundo** | id, límites geográficos, shard asignado, bioma predominante, parámetros fiscales (impuestos, aduanas, canon base) |
| **Contrato de flete (CCRI-Flete)** | id, cargador, transportista, cargamento(s) en custodia, origen, destino, precio del flete, plazo, escrow del cargador, garantía del transportista, estado, fill % |
| **Red eléctrica regional** *(Fase 3, ver 5.8)* | región, generadores conectados, líneas de transmisión, demanda agregada, precio spot del último tick, historial de despacho |
| **Ciudad** | id, ubicación, nivel, población, parámetros laborales (salario_base y cualificación según nivel, ver 5.7), índice de suministro histórico, radio de influencia logística, curvas de demanda activas por producto |

---

## 17. Modelo de datos

### 17.1 Una base de datos, esquemas por dominio

**PostgreSQL es la única base de datos del sistema** (una sola instancia en Fases 0–1, ver 18.4), con **esquemas separados por dominio** — no una arquitectura poliglota (v1.2: se elimina ese rótulo; instancias separadas solo si la escala medida lo exige):

- **Estado del mundo/edificios/entidades espaciales**: esquema por shard, con la extensión PostGIS para consultas geoespaciales.
- **Contratos, garantías (CCRI) y ledger financiero**: esquema transaccional (ACID estricta) — cada contrato bloquea simultáneamente inventario real y dinero; el ledger de doble entrada da trazabilidad completa de escrow, garantías, liquidaciones y saldos.
- **Historial de contratos liquidados/analítica**: agregaciones (velas OHLC, estadísticas de mercado) construidas a partir de contratos cerrados, no de órdenes vivas; la extensión TimescaleDB se adopta solo si el volumen de series lo justifica (a la escala de Fases 0–1, un `GROUP BY` por hora basta).
- **Eventos y mensajería entre módulos**: outbox sobre PostgreSQL en Fases 0–1; Kafka con schema registry solo en Fase 2+ y solo si el volumen lo exige (ver 18.4).

### 17.2 Identidad global y retención

- **Esquema de IDs global**: toda entidad usa un identificador único ordenable temporalmente, único en todo el mundo e independiente del esquema/base donde resida. El diseño original proponía **ULID** con espacio de nombres por tipo (`veh_...`, `ctr_...`, `crg_...`). *(v1 implementada: **UUIDv7 nativo de PostgreSQL 18** — columnas `uuid DEFAULT uuidv7()`; conserva la ordenabilidad temporal de ULID, y el namespacing por prefijo se sustituye por tipado en la capa de aplicación (branded types en el frontend) — ver ADR-IMPL-01 en `docs/desarrollo.md`. `specs/openapi.yaml` v1.1.0 usa `format: uuid`.)* Las referencias entre dominios (un contrato que apunta a un cargamento, un cargamento a un vehículo) son así auditables aunque las entidades vivan en esquemas distintos o migren entre shards en el futuro.
- **Retención y archivado** (el mundo nunca se resetea, los datos no pueden crecer sin cota):
  - **Agregados permanentes**: velas OHLC, estadísticas regionales e índices de ciudades se conservan para siempre (crecen lentamente).
  - **Detalle archivable**: contratos liquidados y movimientos raw del ledger se mueven a almacenamiento frío tras ~1 año de juego (≈15 días reales × 24 = calibrar según volumen real), conservando en caliente los saldos, los agregados y todo contrato/garantía vivo. El archivo frío permanece consultable para auditoría.
  - **Snapshots de shards**: retención escalonada (todos los del día, uno por día durante un mes, uno por mes después).

### 17.3 Consideraciones de consistencia

- Transacciones financieras: **ACID estricta**, sin excepciones (nunca se duplica ni desaparece dinero).
- Estado del mundo físico (posición de vehículos, producción): consistencia eventual aceptable con reconciliación periódica.
- Contratos (CCRI): consistencia fuerte en el bloqueo simultáneo de stock, garantía y escrow. Al modelarse el inventario comprometible como cuentas del propio ledger (ver 15.3), este bloqueo triple es **una única transacción ACID local** (o se asientan las tres partidas o ninguna) — no requiere 2PC ni sagas entre bases. El particionado del ledger para escalado horizontal se difiere hasta que los números lo exijan (ver sección 19); cuando llegue, la clave de partición será la **cuenta** (transferencias entre particiones vía saga), no la región del contrato.

---

## 18. Diseño del backend

### 18.1 Servicios principales

1. **Auth/Identity**: autenticación, sesiones, gestión de cuentas (jugadores y bots comparten el mismo modelo).
2. **World Simulation Service** (por shard): física de edificios, producción, recursos naturales.
3. **Contract Service**: tablón de contratos, negociación, bloqueo simultáneo de stock/garantías/escrow, verificación de entrega y liquidación o sanción, para **ambos tipos de contrato** (CCRI de bienes, 5.3, y CCRI-Flete, 5.3.2); historial de contratos liquidados.
4. **Logistics Service** (planificación, sin estado de tránsito): topología del grafo global de la red, pathfinding jerárquico, cálculo de ETAs a partir de los resúmenes de congestión que publican los shards. **No simula vehículos**: la simulación de tránsito y congestión pertenece a cada shard (ver 15.1); este servicio solo planifica y estima.
5. **Economy Balancer Service**: monitoreo macroeconómico (inflación/deflación, ajuste de impuestos y cánones) y cálculo de las curvas de demanda de ciudades (nivel, saturación por oferta). Actúa además como **agente decisor de las ciudades**: es el único responsable de decidir cuándo, cuánto y a qué precio publica cada ciudad sus solicitudes de compra, y las publica **a través de la API estándar del Contract Service** (una ciudad es, a efectos del mercado, una cuenta más — sin canal privilegiado). También recalcula periódicamente el costo laboral regional (fórmula de 5.7). (El tick del mercado spot eléctrico se incorpora en Fase 3 junto con la red, ver 5.8.)
6. **Bot Orchestration Service**: gestión del ciclo de vida de bots tanto en producción (población permanente, densidad dinámica) como en el entorno separado de stress test.
7. **Notification/Event Gateway**: distribución de eventos relevantes a clientes suscritos (WebSocket).

**Jobs de plataforma** (v1.2: no son servicios sino procesos batch/operativos; se listan aparte para no inflar el mapa de servicios):

- **World Persistence**: snapshots de shards y backups (operación crítica de fiabilidad: RPO/RTO definidos, ejecutada con prioridad y aislada de cargas batch; el snapshot global consistente se toma en la ventana de mantenimiento diaria).
- **Analytics**: agregación de métricas y estadísticas de mercado (carga batch de baja prioridad, separada deliberadamente de Persistence para que nunca compita con los snapshots de los que depende la recuperación del mundo).

### 18.2 Comunicación entre servicios

- **Síncrona** (REST/JSON): operaciones que requieren respuesta inmediata (colocar una orden, iniciar construcción).
- **Asíncrona** (bus de eventos): propagación de cambios de estado (llegada de mercancía, fin de producción, actualización de precios).

### 18.3 Stack tecnológico sugerido (orientativo, no vinculante)

- **Go** para el motor de simulación (shards) **y para el Contract Service**: el componente que mueve dinero se decide explícitamente en el mismo stack que el motor (tipado fuerte, mismo toolchain), no por descarte en el stack web. *(v1 implementada: el camino de comando del Contract Service —publicar, aceptar, construir, comprar…— corre en el gateway TypeScript como transacciones SQL `SERIALIZABLE`, con las invariantes en la base; el motor Go ejecuta todo lo dirigido por tiempo, incluidos sorteos y liquidaciones — ver ADR-IMPL-03 en `docs/desarrollo.md`.)*
- **Regla de oro del ledger (independiente del lenguaje):** toda invariante de dinero/stock (no-negatividad, doble entrada balanceada, bloqueo triple atómico del CCRI) vive **en la base de datos** — transacciones `SERIALIZABLE`, constraints y funciones SQL que asientan todo-o-nada. El código de aplicación orquesta; la base garantiza. Un bug de aplicación no puede romper la contabilidad.
- Framework web (Node.js/TypeScript) para gateway, autenticación y servicios de presentación menos sensibles a latencia. *(v1 implementada: Fastify 5.)*
- PostgreSQL como base única (esquemas separados por dominio; PostGIS y TimescaleDB son extensiones de la misma instalación). *(v1 implementada: PostgreSQL 18, con `uuidv7()` nativo — ver 17.2. Acceso a datos con SQL explícito: `pgx/v5` en Go y `pg` en TypeScript, sin sqlc ni Drizzle — ver ADR-IMPL-04 en `docs/desarrollo.md`.)*
- Caddy como reverse proxy.

### 18.4 Topología física por fases

Las **fronteras lógicas** de los 7 servicios y 2 jobs (18.1) son firmes desde el día 1; su **materialización física** es progresiva:

- **Fases 0–1: monolito modular.** Un número mínimo de desplegables: el motor (Go) con los módulos shard/contratos/logística/balancer tras fronteras internas estrictas (paquetes con interfaces, sin imports cruzados), el orquestador de bots como proceso aparte que consume la API igual que un cliente (coherente con 15.4), más el gateway web (TS). **Una sola instancia de PostgreSQL** con esquemas separados (ledger, mundo, analítica). La mensajería entre módulos usa una **outbox table + polling** en lugar de Kafka. Sin Redis, sin Meilisearch, sin etcd: el tablón se sirve desde PostgreSQL con índices apropiados, que a la escala de estas fases sobra.
- **Fase 2+: extracción medida.** Los módulos se extraen a procesos/servicios separados solo cuando la medición lo justifique, en este orden probable: shards a procesos propios (implementando entonces el protocolo de handoff de 15.2), Contract Service, gateway de notificaciones. La outbox se sustituye por un bus real solo si el volumen lo exige. Extraer un módulo con fronteras ya estrictas es una operación mecánica, no un rediseño.
- Los diagramas de la sección 15 describen la arquitectura **lógica**; no implican que cada caja sea un proceso separado desde el inicio.

---

## 19. Estrategia de escalabilidad

**Techo de capacidad explícito (decisión asumida):** la plataforma de despliegue definitiva es Docker Compose sobre un puñado de hosts administrados manualmente (ver anexo). Esto pone un **techo consciente** al pilar "mundo único masivo": el mundo crece hasta lo que quepa en esos hosts (con el diseño event-driven y bots eficientes, esto cubre holgadamente decenas de miles de agentes; el objetivo de "millones" queda condicionado). Si el juego desborda ese techo, la decisión a revisitar es la plataforma de despliegue — no la arquitectura: el sharding lógico y las fronteras de servicios se mantienen precisamente para que esa puerta siga abierta.

- **Escalado dentro del techo**: primero, escalado vertical del proceso único del motor (todos los shards lógicos en un proceso, ver 15.1); solo cuando la medición lo exija, extracción de shards a procesos repartidos entre los hosts (implementando entonces el handoff de 15.2); asignación región→proceso→host gestionada por configuración explícita y versionada.
- **Reubicación de shards en la ventana de mantenimiento** (aplicable tras la extracción a multi-proceso): mover una región de un host a otro es una operación **manual y planificada** que se ejecuta durante la ventana diaria (sim-time congelado, snapshot consistente): parar shard, mover estado, arrancar en el host destino. No existe migración en caliente ni rebalanceo automático — se renuncia a ellos deliberadamente a cambio de simplicidad operacional.
- **Escalado del servicio de Contratos**: una sola base transaccional para el ledger mientras el volumen lo permita (con las invariantes en SQL, ver 18.3); el particionado del ledger por cuenta queda diseñado conceptualmente (17.2) pero no se construye hasta necesitarlo.
- **Área de interés (interest management)**: los clientes solo reciben eventos relevantes a su contexto, reduciendo ancho de banda y carga de red proporcional al número de jugadores.
- **Bots como carga controlable**: los bots de producción (permanentes) escalan junto con el resto del shard, como parte normal de la carga del mundo; el modo stress test añade una capa adicional y separada (en hosts propios, temporales), usada para validar límites antes de exponer cambios a jugadores reales. La **densidad de bots es además la válvula de carga principal** dentro del techo fijo: ante saturación, se reduce población de bots antes que degradar la experiencia humana.
- **Colas y backpressure**: todos los servicios críticos (mercado, logística) deben implementar control de flujo para evitar cascadas de fallos bajo carga extrema.

---

## 20. Riesgos técnicos

| Riesgo | Descripción | Mitigación |
|---|---|---|
| **Inconsistencia entre shards** (aplica solo si se extraen a procesos separados) | Un bien "existe" en dos lugares tras un handoff fallido | Mientras los shards convivan en un proceso, el riesgo no existe (traspaso local); para la extracción queda especificado el protocolo SELLADO→COPIADO→ACTIVADO→PURGADO con `transfer_id` idempotente (15.2); el ledger de bienes como árbitro contable; reconciliación periódica como red de seguridad |
| **Colapso económico** | Inflación/deflación descontrolada por bots mal calibrados | Balancer económico con límites y alertas automáticas; pruebas exhaustivas en modo stress test antes de producción |
| **Congestión de red** | Millones de eventos por segundo saturan el bus de eventos | Particionamiento agresivo, agregación de eventos, priorización por relevancia para el jugador |
| **Abuso de bots** | Bots usados para manipular mercado o explotar bugs | Auditoría continua, límites de tasa (rate limiting) iguales para bots y humanos, detección de patrones anómalos |
| **Complejidad de balance de juego** | Con producción y logística abiertas desde el inicio, el balance es difícil de ajustar | Iteración con simulaciones masivas de bots antes de cada actualización mayor |
| **Costo de infraestructura** | Simulación masiva y persistente es costosa de operar | Techo de capacidad explícito (sección 19) con la densidad de bots como válvula de carga; diseño event-driven eficiente; stress test limitado a ventanas controladas en hosts temporales |
| **Latencia percibida** | Jugadores en regiones geográficas distintas del servidor experimentan lag | Edge gateways regionales, interpolación en cliente, arquitectura de eventos asíncrona |
| **Shard caliente sin subdivisión** (riesgo asumido) | Al ser la región de gameplay la unidad indivisible de sharding (15.1), un hotspot extremo no puede repartirse entre nodos | Escalado vertical del shard afectado; diseño de mapa que dispersa atractores; impuestos/canon como congestion pricing regional; monitorizar densidad por región con umbrales de alerta tempranos |
| **Agotamiento global acoplado al calendario de expansiones** (riesgo asumido) | Con minerales finitos estrictos (sección 10), la salud del mundo a largo plazo depende de abrir territorio nuevo a tiempo | Métrica de ritmo de agotamiento global en el Economy Balancer con proyección a 6-12 meses; pipeline de expansión territorial planificado con esa antelación; regiones en declive como gameplay comunicado, no como bug |
| **Extracción multi-proceso tardía** (riesgo asumido, v1.2) | Si el crecimiento desborda el proceso único del motor (15.1), el handoff se construiría bajo presión de producción | Protocolo ya especificado (15.2); umbrales de alerta de carga por shard lógico con meses de margen; la ventana de mantenimiento diaria simplifica la migración cuando llegue |
| **Pérdida de minutos de estado físico tras caída** (riesgo asumido, v1.2) | Al relajar el replay determinista (1.1), una caída puede rebobinar minutos de posiciones y progresos de lote | Snapshots frecuentes del shard; dinero, stock comprometido y contratos viven en el ledger ACID y no pierden nada; reconciliación física↔contable al recuperar |

---

## 21. Roadmap de implementación por fases

### Fase 0 — Prototipo técnico (validación de arquitectura)
- Motor de simulación de un solo shard con producción y logística básica.
- Contrato de compraventa simple de un solo producto (tablón + escrow básico).
- Bots rudimentarios (reglas fijas) para validar el loop económico.

### Fase 1 — Vertical slice jugable
- Cadena productiva completa (hierro → vehículos) en una región única.
- Logística terrestre (camiones) funcional con congestión básica.
- Tablón de contratos con múltiples productos, parciales y liquidación real contra entrega física.
- Costo laboral regional por fórmula (5.7); energía como combustible consumido in situ (5.8).
- Ciclo completo de insolvencia/embargo/subasta (5.9, 11.2).
- Cliente jugable (web) con loop completo de construcción/producción/venta.

### Fase 2 — Multi-región y logística avanzada
- Generación procedural del mundo completo con biomas y recursos distribuidos.
- Mundo multi-región completo en un único proceso (shards lógicos, 15.1); extracción a multi-proceso con handoff solo si la medición lo exige (18.4).
- Transporte ferroviario y marítimo.
- Contratos privados, contratos de flete (CCRI-Flete, 5.3.2) y slots de prioridad en terminales.

### Fase 3 — Escala masiva y bots avanzados
- Bot Orchestration Service con arquetipos completos.
- Modo stress test con cientos de miles de bots en entorno separado.
- Economy Balancer con ajuste automático de impuestos/inflación.
- Terminales intermodales.
- Red eléctrica regional (5.8): generación, transmisión y mercado spot por orden de mérito.

### Fase 4 — Pulido, temporadas y meta-juego social
- Rankings y temporadas (sin resetear el mundo).
- Herramientas avanzadas de análisis para jugadores (dashboards de eficiencia, comparativas).
- Expansión del mundo (nuevas regiones) y eventos dinámicos (crisis de recursos, desastres naturales).

---

## 22. Posibles expansiones futuras

- **Investigación y desarrollo opcional**: mejoras de eficiencia adicionales (no desbloqueo de contenido) vía I+D corporativo.
- **Bancos y préstamos**: crédito entre jugadores o del sistema, con interés y colateral (referenciado por 5.9: en la v1 no existe deuda; esta expansión la introduciría de forma controlada).
- **Mercado financiero derivado**: futuros y contratos de cobertura sobre precios de materias primas. *Nota de diseño:* un futuro es exactamente "producción prometida", lo que la regla base del CCRI (5.3) prohíbe deliberadamente; esta expansión requerirá relajar esa regla de forma controlada (p. ej. futuros solo liquidados en efectivo, nunca con entrega física obligatoria) — decisión consciente para entonces, no un descuido.
- **Interconexiones eléctricas interregionales y almacenamiento (baterías)**: extensión de la red regional de 5.8 (la propia red se activa en Fase 3).
- **Transporte aéreo**: cuarto modo logístico (aeropuertos, rutas aéreas, aviones) para carga de alto valor; retirado del alcance base en v1.2 por nicho económico mínimo en carga industrial.
- **Estacionalidad de la demanda urbana**: factor multiplicativo de épocas/estaciones sobre la curva de demanda de ciudades (5.6); retirado de la v1 en v1.2.
- **Mercado laboral con pool finito y subasta salarial**: sustituiría a la fórmula de costo de 5.7 por una asignación periódica por prioridad de oferta salarial, con plantillas sin cubrir y producción parada.
- **Reputación (fill-rate) con efectos económicos**: descuento de garantía por historial de cumplimiento; requiere la maquinaria anti-wash-trading (propiedad común entre cuentas, diversidad de contrapartes) — retirada en v1.2 al eliminarse el premio que la justificaba.
- **Reserva compartida de garantías**: una garantía respaldando N publicaciones mutuamente excluyentes (eficiencia de capital en el tablón); retirada en v1.2, reintroducible de forma aditiva sobre el CCRI.
- **Política regional**: jugadores que administran infraestructura pública (carreteras, puertos) a cambio de tarifas, introduciendo dinámicas de gobernanza económica (dependería de la expansión de consorcios).
- **Eventos dinámicos del mundo**: desastres naturales, crisis energéticas, embargos comerciales simulados que fuerzan adaptación logística.
- **Modo espectador/analista**: rol no productivo centrado en análisis de mercado y trading puro (broker), sin operar industrias propias.
- **Consorcios/alianzas formales**: entidades multi-jugador con cuenta propia, propiedad compartida de infraestructura, reglas de reparto de beneficios y capacidad de firmar contratos como una única entidad económica (retirado del alcance base; requiere diseñar gobernanza interna: roles, tesorería, expulsiones).
- **Bots con aprendizaje automático**: comportamiento adaptativo (aprendizaje por refuerzo, heurísticas evolutivas) para investigación de balance económico, probado en entorno separado (retirado del alcance base; los bots de producción usan heurísticas auditables).

---

## Notas finales

Este documento combina las perspectivas de diseño de juego (GDD) y arquitectura de software (SAD) de forma integrada porque el pilar central del juego —una economía físicamente restringida y persistente— depende directamente de decisiones de arquitectura distribuida (sharding, consistencia, mercado como servicio). Cualquier iteración de diseño de gameplay (nuevas recetas, nuevos medios de transporte) debe evaluarse también en términos de su impacto en la escalabilidad del sistema, y viceversa.


## Anexo: Stack tecnológico consolidado

**Fases 0–1 (monolito modular, ver 18.4) — actualizado a la implementación v1:**

- Motor de simulación (todo lo dirigido por tiempo: sim-clock, lotes, tránsito, sorteos, liquidaciones, balancer): **Go 1.22 + pgx/v5** con SQL explícito, **sin sqlc**. *(El diseño original asignaba también el camino de comando del Contract Service a Go + sqlc — ver ADR-IMPL-03 y ADR-IMPL-04 en `docs/desarrollo.md`.)*
- Gateway web / auth / API pública (REST de `specs/openapi.yaml` v1.1.0 + WS de `specs/ws-protocol.md`), incluido el camino de comando vía transacciones SQL `SERIALIZABLE`: **TypeScript + Fastify 5 + pg** (node-postgres) con SQL explícito, **sin Drizzle** (ADR-IMPL-04). *(El diseño original proponía Drizzle ORM.)*
- Estructura real del monorepo (**sin workspaces**): `Makefile` raíz + `backend/{engine, gateway, bots, migrations, seeds}` + `frontend/` (Nuxt 4 + Vue 3 + Pinia + Phaser 3 + Sass, npm) + `infra/` (docker-compose + Caddyfile) + `docs/` + `specs/`.
- Comunicación síncrona: REST/JSON.
- Comunicación asíncrona entre módulos: **outbox table + polling sobre PostgreSQL** (sin bus dedicado).
- Base de datos: **una sola instancia de PostgreSQL 18** (imagen `postgis/postgis:18-3.6`; `uuidv7()` nativo, ver 17.2) con esquemas separados por dominio; PostGIS (estado espacial) como extensión; TimescaleDB (series temporales/OHLC) solo si el volumen medido lo justifica (ver 17.1).
- Migraciones de esquema: ficheros SQL numerados en `backend/migrations`, aplicación **manual** vía Makefile (`make db-migrate`, tracking en `public.schema_migrations`); nada se aplica automáticamente al arrancar servicios (ADR-IMPL-02, coherente con la ventana de mantenimiento diaria de 1.1).
- Reverse proxy: Caddy.
- Despliegue: Docker Compose.
- Observabilidad: Prometheus, Grafana, Loki y Tempo.
- Verificación end-to-end reproducible: `make verify` (`infra/verify.sh`) — ciclo CCRI completo con bots y asserts contables.

**Fase 2+ (extracción medida, solo si los números lo exigen):**

- Shards como procesos independientes repartidos entre hosts; coordinación región→proceso por configuración explícita versionada (etcd solo si la coordinación manual se queda corta).
- Bus de eventos dedicado (Kafka) en sustitución de la outbox — con **schema registry y versionado de eventos obligatorios** desde su adopción.
- Redis (caché/pub-sub) y motor de búsqueda del tablón (p. ej. Meilisearch) solo si PostgreSQL deja de responder a la escala real medida.

**Decisión de despliegue (definitiva, asumida):** Docker Compose sobre un puñado de hosts administrados manualmente es el **destino**, no una fase transitoria. No hay autoescalado, ni orquestador, ni migración automática entre hosts; la reubicación de shards es manual y ocurre en la ventana de mantenimiento diaria (sección 19). Esta decisión impone el techo de capacidad descrito en la sección 19 y se revisitaría únicamente si el juego lo desborda de forma sostenida.


## Anexo B: Registro de decisiones de arquitectura (ADR resumido, v1.1)

Decisiones tomadas en la design review de la v1.1, con su trade-off asumido:

| # | Decisión | Alternativa descartada | Trade-off asumido |
|---|---|---|---|
| 1 | Motor **event-driven** (cola de eventos por shard; magnitudes continuas analíticas) | Tick global por entidad | Mayor disciplina de diseño; a cambio, coste ∝ eventos, no ∝ entidades |
| 2 | Ratio de tiempo **24×** (1 día juego = 1 h real) | Tiempo real 1:1 | Los plazos de contrato se expresan en sim-time; UI debe traducir siempre |
| 3 | Simulación **determinista por shard** (snapshot + replay, RNG sembrado, sin wall-clock) | Snapshots frecuentes sin determinismo | Restricciones de implementación (orden total, punto fijo) a cambio de RPO≈0 y bugs reproducibles |
| 4 | **Ventana de mantenimiento diaria** con sim-time congelado | 24/7 estricto | 10–30 min diarios de pausa a cambio de despliegues, migraciones y snapshots globales triviales |
| 5 | CCRI con **reserva compartida** (una garantía respalda N publicaciones excluyentes) | 100% bloqueado por publicación | Lógica de reserva compartida en el ledger a cambio de liquidez y eficiencia de capital |
| 6 | **Aceptación y entrega parciales** con liquidación pro-rata | Todo-o-nada | Contratos con estado de fill acumulativo a cambio de no fragmentar el tablón ni castigar la logística multivehículo |
| 7 | **Ventana de prioridad humana** (humanos t=0 FIFO; bots t+45s FIFO) | Subasta por lotes / FIFO puro | La detección de automatización en cuentas humanas pasa a ser sistema crítico de balance (riesgo en §20) |
| 8 | **Inventario comprometible como cuentas del ledger** (bloqueo triple = 1 transacción ACID) | Saga o 2PC entre shard y ledger | El shard cede la propiedad contable del stock; sincronización física↔contable por eventos + reconciliación |
| 9 | Stock de contrato fallido se libera **en su ubicación física actual** | Retorno al almacén de origen | Coherencia con el pilar físico; requiere la entidad Cargamento |
| 10 | **Shards simulan tránsito; Logistics Service solo planifica** | Servicio global de tránsito | La congestión de enlaces fronterizos se simula por segmentos |
| 11 | **Handoff formal por evento** (SELLADO→COPIADO→ACTIVADO→PURGADO, ledger como árbitro) | Terminales de frontera obligatorias | Protocolo por implementar y probar; a cambio, rutas largas multiregión sin fricción artificial |
| 12 | Capacidad de enlaces de **uso común**; prioridad vendible solo en terminales | Mercado de capacidad exclusiva de vías | Se renuncia a ese gameplay para eliminar el acaparamiento hostil de rutas |
| 13 | **Región de gameplay = unidad de sharding** (indivisible) | Quadtree técnico separado | Hotspots solo mitigables con escalado vertical, diseño de mapa y congestion pricing fiscal (riesgo en §20) |
| 14 | Minerales **finitos estrictos**; válvula = expansión territorial | Agotamiento asintótico + prospección | Salud del mundo acoplada al calendario de expansiones (riesgo en §20) |
| 15 | Suelo solo en **concesión del sistema** (canon como sink; reversión por impago) | Propiedad perpetua | No hay mercado inmobiliario de propiedad; sí de traspasos |
| 16 | Contenido embargado se liquida por **subasta pública vía CCRI** | Botín del reclamante | Menos incentivo al "buitreo"; el sistema actúa como vendedor |
| 17 | **Monolito modular** en Fases 0–1 (un Postgres, outbox, sin Kafka/Redis/Meilisearch/etcd) | Microservicios desde el inicio | La validación de la topología distribuida se pospone a la extracción medida |
| 18 | **Docker Compose como destino final** (techo de capacidad explícito) | Orquestador (K8s/Nomad) | El pilar "masivo" queda acotado al techo; revisitable solo si se desborda |
| 19 | **Contract Service en Go**, invariantes de dinero/stock **en SQL** | TypeScript por descarte | Dos stacks backend; la contabilidad no depende del lenguaje de aplicación |
| 20 | Insolvencia = **parada progresiva sin deuda** | Crédito del banco central | Los préstamos quedan para la expansión de bancos |
| 21 | **Mercado laboral**: pool regional finito con salario de mercado | Tabla de salarios fija | Asignación periódica por prioridad salarial; +1 subsistema |
| 22 | **Red eléctrica regional desde v1** (térmicas a combustible físico, spot por orden de mérito, sin interconexiones) | Combustible in situ sin red | Mayor alcance de v1 (riesgo en §20); degradación acordada: lanzar sin red y activarla en Fase 2 |
| 23 | **CCRI-Flete** como segundo tipo de contrato (custodia en el ledger) | Sin fletes de terceros en v1 | +1 tipo de contrato; habilita el rol de transportista puro |
| 24 | Bots como **procesos externos con la API real** (red interna multiplexada) | In-process en el shard | Coste de red interno a cambio de igualdad de API literal y stress test permanente del camino real |
| 25 | **Capitalización de bots = emisión monetaria del banco central**, contabilizada | Pool fijo de génesis | La política monetaria y la densidad de bots comparten libro |

**Decisiones v1.2 (review de simplificación):**

| # | Decisión | Deroga/modifica | Trade-off asumido |
|---|---|---|---|
| 26 | **Ventana de sorteo** en el tablón (orden aleatorio entre aceptantes; la latencia no vale nada) | Deroga #7 | Los humanos pierden la prioridad absoluta; a cambio desaparece la detección de automatización como sistema crítico de balance |
| 27 | **Garantía fija (10%), sin sistema de reputación** en v1 | Modifica 5.3 | Los veteranos fiables inmovilizan más capital; se elimina el incentivo al wash-trading y toda su maquinaria anti-manipulación |
| 28 | **Una garantía por publicación** (sin reserva compartida) | Deroga #5 | Explorar varias regiones exige más capital o menos cantidad por publicación; la aceptación no arrastra cancelaciones en cascada |
| 29 | **Red eléctrica pospuesta a Fase 3**; v1 = combustible in situ | Deroga #22 | Sin apagones ni rol de eléctrico puro hasta Fase 3; la degradación acordada asciende a plan base |
| 30 | **Costo laboral por fórmula** (sin pool ni subasta) | Deroga #21 | Sin "producción parada por falta de personal"; el efecto económico (regiones saturadas = caras) se conserva |
| 31 | **Estacionalidad de demanda pospuesta**; elasticidad en 2 clases | Modifica 5.6 | Menos textura temporal el primer año; un factor multiplicativo menos que balancear y explotar |
| 32 | **Replay bit a bit rebajado a aspiración**; snapshots periódicos + ledger como respaldo | Modifica #3 | RPO de minutos solo en estado físico; bugs de balance menos reproducibles |
| 33 | **Todos los shards en un único proceso**; handoff especificado pero no construido | Modifica #11 y #13 | Si el crecimiento desborda el proceso único, el multi-proceso se construye bajo presión (riesgo en §20) |
| 34 | **Arquetipo "consumidor NPC" eliminado**: la ciudad es el único consumidor final | Modifica 13.2 | Ninguno para el jugador; un concepto menos |
| 35 | **Transporte aéreo → expansiones** | Modifica §7 y §21 | Camión/tren/barco cubren la matriz costo/velocidad/volumen |
| 36 | **Avería = espera + reparación** (sin rescate en v1) | Modifica 7.3 | El rescate/transbordo llega con el CCRI-Flete (Fase 2) |
| 37 | **Clima eliminado** del alcance | Modifica 15.1 y 18.1 | Ninguno: ninguna mecánica lo consumía |

**Revisado y conservado deliberadamente en v1.2** (complejidad ratificada): mercado secundario de vehículos (§8), Economy Balancer como agente decisor de las ciudades (18.1), stack dual Go + TypeScript (#19), CCRI-Flete en Fase 2 (#23), slots de prioridad de terminales (#12), y aceptación/liquidación parciales (#6).

Retirados del alcance base en la revisión v1.1 (movidos a la sección 22 como expansiones futuras): **consorcios/alianzas formales** y **bots con aprendizaje automático** (los bots de producción usan exclusivamente heurísticas auditables). El diseño detallado de la toma de decisiones de los bots (13.3) se pospone deliberadamente.

**Nota (v1.2.1):** las decisiones de implementación v1 (identidad UUIDv7, reparto engine/gateway, emisión de stock por producto, auto-despacho logístico, reloj sim-time persistido, migraciones manuales, protocolo WS, etc.) se registran como **ADR-IMPL-01..14 en `docs/desarrollo.md`**, que es el documento autoritativo sobre *cómo está construido* el sistema.
