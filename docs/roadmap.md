# Roadmap — hasta la generación procedural del mundo

**Estado:** documento vivo (2026-07-16). Ordena el trabajo desde la v1 actual hasta la
generación procedural del mundo (GDD §9-10, Fase 2). Se apoya en el análisis de brechas
GDD ↔ implementación: el vertical slice económico (CCRI, ledger, tránsito físico,
demanda urbana) está completo; lo que falta se concentra en el cierre de ciclos de
largo plazo y en las capas macro/logística que hacen viable un mundo grande.

El orden no es arbitrario: cada etapa hace útil a la siguiente. No tiene sentido
generar un mundo de cientos de regiones si los yacimientos no se agotan, los impuestos
regionales no se cobran y el pathfinding no escala.

```
Etapa 1: agotamiento + impuestos + saturación laboral ─► el espacio importa económicamente
Etapa 2: analytics + balancer macro + snapshots       ─► se puede medir/balancear un mundo grande
Etapa 3: flete + multimodal + HPA*                    ─► se puede mover mercancía por un mundo grande
Etapa 4: generación procedural                        ─► el mundo grande existe
```

Cada ítem entra con su ADR-IMPL en `docs/desarrollo.md` y actualización de `specs/`
(openapi.yaml, ws-protocol.md, schemas/) en el mismo cambio (regla de docs vivos).

---

## Etapa 1 — Cerrar los ciclos de Fase 1

*El juego funciona a largo plazo en el mundo pequeño.* El mundo 2×2 actual es el banco
de pruebas: todo lo de esta etapa se valida barato aquí antes de escalar.

### 1.1 Agotamiento de yacimientos
Decrementar `resource_deposits.amount_remaining` en cada lote de extracción,
regeneración de renovables, evento `deposit.depleted` al outbox. Hoy la extracción
primaria produce sin decrementar nada.
**Por qué antes de la generación procedural:** sin agotamiento, la distribución de
recursos que genere el mundo no tiene consecuencias — no hay auge/decadencia regional
ni presión de expansión (GDD §9, §20).

### 1.2 Embargo → subasta pública vía CCRI (GDD §5.9/§11.2)
Hoy la cascada de insolvencia termina en edificios `seized` y ahí muere. Falta:
auto-publicar el stock de edificios embargados como ventas del sistema, subastar
edificio/concesión por el propio canal CCRI, y aplicar la recaudación a las deudas.
Cierra la rotación de suelo, imprescindible en un mundo que nunca se resetea.

### 1.3 Nivel de edificio con efecto real (GDD §6.3)
Subir de nivel cuesta `build_cost·2^nivel` pero `sim/production.go` ignora el nivel.
Aplicar la tabla de progresión: multiplicador de líneas, velocidad y eficiencia.
Da sentido a la progresión horizontal antes de que haya más mapa que colonizar.

### 1.4 Impuestos y aduanas aplicados
`regions.tax_rate_bp` y `customs_rate_bp` están sembrados y en el DTO pero ninguna
liquidación los cobra. Aplicar impuesto regional en liquidaciones y arancel en
contratos interregionales.
**Por qué antes de la generación procedural:** las regiones generadas se diferenciarán
por fiscalidad; la aduana es lo que hace que "dónde está cada cosa" importe.

### 1.5 Saturación laboral regional (GDD §5.7)
Hoy se cobra `workers × base_salary` plano. Añadir el factor de saturación por
ocupación industrial regional ("regiones saturadas = mano de obra cara"), que motiva
migrar/automatizar y crea el gradiente económico espacial que el mundo generado debe
explotar.

**Juez de la etapa:** extender `make verify` con asserts de cada ciclo (agotar un
yacimiento, ejecutar una subasta completa, liquidar con impuesto/arancel).

---

## Etapa 2 — Capa macro y observabilidad

*Poder medir el mundo antes de agrandarlo.*

### 2.1 Jobs de analytics
Escribir desde el engine las tablas hoy muertas: `analytics.market_ohlc` (materializar
lo que `GET /market/ohlc` recalcula al vuelo), `city_snapshots`, `region_stats`,
`economy_indicators`. Sin telemetría regional no se puede balancear un mundo grande ni
validar que el generador produce economías viables.

### 2.2 Economy Balancer macroeconómico (GDD §5.5)
Monitoreo de masa monetaria vs. PIB y ajuste automático de las palancas que la Etapa 1
activó: impuestos, aranceles, emisión/absorción anti-inflación, subsidios. Es la
"principal palanca de balance" del diseño; el balancer actual solo hace demanda urbana
y cargos diarios.

### 2.3 Snapshots de shard
Job World Persistence que escribe `world.shard_snapshots` (hoy sin escritor) con el
RPO del GDD §1.1/§18.1. Necesario antes de multiplicar el volumen de estado por 100.

---

## Etapa 3 — Logística que escala espacialmente

### 3.1 CCRI-Flete + arquetipo `freighter` (GDD §5.3.2)
El schema ya está tendido (`ledger.freight_contracts`, enum `freight`, arquetipo en
`auth.bot_archetype`); falta la lógica de motor (custodia, despacho subcontratado,
liquidación de flete) y desbloquear el `POST` en `gateway/src/routes/contracts.ts`.
En un mundo grande las distancias crecen y "logística como servicio" deja de ser
opcional. Completa el 4º arquetipo de bot.

### 3.2 Multimodal y transbordos (GDD §7.2-7.3)
Simular colas/tiempos de transbordo en terminales, dar efecto funcional a
`terminal_slots` (hoy comprables pero inertes), habilitar flujo rail/sea y eliminar el
`mode='road'` fijo del auto-despacho (`engine/internal/logistics/dispatch.go`).
Un mundo procedural con costas y ríos solo tiene sentido si barco y tren existen.

### 3.3 Pathfinding jerárquico (GDD §7.4)
Sustituir los dos Dijkstra planos duplicados (Go `logistics/graph.go` y TS
`routes/logistics.ts`) por HPA*/contraction hierarchies con la grilla de macro-regiones
como nivel superior — idealmente **una sola implementación** (el engine calcula, el
gateway consulta). Dijkstra plano no sobrevive a miles de nodos; es el cuello de
botella técnico directo del mundo grande.

---

## Etapa 4 — Generación procedural del mundo (GDD §9-10, Fase 2)

### 4.1 Generador determinista
Herramienta separada (`make world-gen SEED=...`) que genera el mundo **una sola vez** y
lo persiste, como manda el GDD (§9: procedural con semilla persistida, nunca
regenerado): ruido Perlin/Simplex → mapas de altura/humedad → biomas por región, ríos
por gradiente, costas.

### 4.2 Colocación algorítmica
Yacimientos según bioma con distribución deliberadamente desigual (GDD §5.6/§9),
ciudades en posiciones viables (agua, llanura, costa), y red vial/ferroviaria inicial
conectando ciudades. El grafo `network_nodes`/`network_links`/`link_segments` se
reutiliza tal cual: el schema ya está listo.

### 4.3 Validación económica del mundo generado
Usar la telemetría de la Etapa 2 + una corrida de bots para verificar que cada seed
produce una economía jugable: toda ciudad alcanzable, cadenas productivas cerrables,
precios estables. **El generador que no pasa el juez, no siembra.**

### 4.4 Convivencia con el seed actual
`backend/seeds/seed_world.sql` (mundo 2×2) queda como fixture de test — sigue siendo
el mundo de `make verify`; el generado es el de producción. `regions.opened_at_sim`
queda listo para la expansión territorial (Fase 4), fuera de este roadmap.

---

## Fuera de este roadmap (diferido, coherente con el GDD)

Expansión territorial y apertura de regiones (Fase 4), sharding multi-proceso con
handoff SELLADO→COPIADO→ACTIVADO (GDD §15.2), red eléctrica (Fase 3), estacionalidad,
mercado secundario de vehículos con depreciación, changeover multi-receta, bajada de
nivel de ciudades, densidad dinámica y modo stress-test de bots (§13.4), y la vista
isométrica del frontend (FE-6).
