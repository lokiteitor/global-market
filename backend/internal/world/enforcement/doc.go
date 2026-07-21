// Package enforcement implementa la CASCADA DE INSOLVENCIA del bounded context
// world (Incremento 6a, GDD 5.9 y 11.2): el motor que materializa los dos
// últimos escalones de "saldo = 0, nunca deuda" —degradación por mantenimiento
// impagado (3º) y ciclo canon → gracia → embargo → subasta (4º)—. Es el lado de
// CONSECUENCIA FÍSICA de la insolvencia: el saldo NUNCA baja de 0 (lo garantiza
// el trigger del ledger; el motor cobra SOLO lo disponible), y las obligaciones
// impagadas se saldan con el patrimonio (degradación del edificio, reversión del
// suelo), jamás como deuda.
//
// # Fronteras (SAD §7 / ADR-006)
//
// world no importa contracts/market/auth. El motor:
//   - COBRA mantenimiento y canon como SINK (cash → sink, transacciones
//     'maintenance'/'canon' del ledger) con queries sqlc propias del contexto
//     (internal/world/sqlcgen) contra ledger.*, exactamente como land/production.
//     Las invariantes de dinero (no-negatividad, doble entrada, inmutabilidad)
//     las garantizan los triggers de 0004_ledger.
//   - PUBLICA el stock embargado por EVENTO (outbox building.seized), nunca por
//     import: la liquidación del stock (ofertas de venta del sistema) es dominio
//     de contracts (consumidor system_liquidator).
//
// Cada entidad se procesa en SU PROPIA transacción SERIALIZABLE
// (platform/db.RunSerializable), bloqueada con FOR UPDATE SKIP LOCKED, con el
// outbox.Emit en la misma tx: varias instancias del motor pueden correr en
// paralelo sin pisarse. La idempotencia se apoya en los ESTADOS como guarda (no
// re-embargar un 'seized', no re-revertir un 'reverted'). El reloj es el
// SimSource inyectado; el bucle cierra con ctx.
//
// # Máquina de estados del EDIFICIO (world.building_status) — rama mantenimiento
//
//		operational ──(impago parcial/total de mantenimiento; condición > umbral)──▶ damaged
//		damaged     ──(mantenimiento al día de nuevo; condición recuperada a 100)──▶ operational
//		operational/damaged ──(condición ≤ II_ABANDON_CONDITION_PCT)─────────────▶ abandoned
//		abandoned   ──(gracia agotada + EMBARGO)─────────────────────────────────▶ seized
//
//	  - El barrido de mantenimiento cobra maintenance_cost × días-sim vencidos
//	    (cash → sink), cobrando SOLO lo disponible. Si cubre todo: avanza
//	    maintenance_paid_until_sim y RECUPERA condición (+2/día-sim hasta 100;
//	    'damaged' vuelve a 'operational' al llegar a 100). Si NO cubre: cobra los
//	    días que pueda, DEGRADA la condición (−II_DEGRADE_PCT_PER_SIM_DAY por día
//	    impagado, mín 0) y marca 'damaged'. Al cruzar el umbral de abandono pasa a
//	    'abandoned', PARA su producción (lotes running → paused_no_workers) y fija
//	    maintenance_paid_until_sim = simNow (arranca el conteo de gracia).
//	  - maintenance_paid_until_sim = "obligaciones liquidadas hasta": cada día
//	    vencido se salda EXACTAMENTE una vez (en efectivo o por degradación), así
//	    que el marcador avanza por TODOS los días vencidos —nunca hay deuda ni
//	    doble degradación—. En un edificio 'abandoned' el marcador es el INSTANTE
//	    del abandono (no acumula mantenimiento): base del periodo de gracia.
//	  - 'abandoned' y 'seized' NO se barren para mantenimiento (estados terminales
//	    de la rama). 'under_construction' e 'in_maintenance' quedan fuera del
//	    barrido (aún no operativo / mantenimiento manual del jugador).
//
// # Máquina de estados de la CONCESIÓN (world.concession_status) — rama canon
//
//		active     ──(periodo vencido; canon impagable)──▶ delinquent   [fija grace_until_sim = simNow + II_SEIZE_GRACE_SIM_SECONDS]
//		active     ──(periodo vencido; canon cobrado)────▶ active       [expires_at_sim += periodo; grace_until_sim = NULL]
//		delinquent ──(grace_until_sim vencido)───────────▶ grace        [marcada para embargo]
//		grace      ──(EMBARGO)───────────────────────────▶ reverted     [suelo libre]
//		<cualquiera ≠ reverted> ──(EMBARGO por edificio abandonado)──▶ reverted
//
//	  - El periodo de gracia (semanas reales → sim-time, II_SEIZE_GRACE_SIM_SECONDS)
//	    se SIRVE en el estado 'delinquent' (grace_until_sim guarda el vencimiento);
//	    'grace' es el marcador transitorio "embargo pendiente" que el barrido de
//	    embargo procesa a 'reverted'. Decisión de diseño documentada: usa los
//	    cuatro valores del enum de forma coherente sin inventar estados (ADR-020).
//
// # EMBARGO (unifica ambas ramas)
//
// El barrido de embargo revierte una concesión y congela TODOS sus edificios en
// una sola tx. Entra al conjunto de embargo una concesión si:
//   - está en 'grace' (rama canon), o
//   - tiene algún edificio 'abandoned' con la gracia agotada
//     (simNow − maintenance_paid_until_sim ≥ II_SEIZE_GRACE_SIM_SECONDS; rama
//     mantenimiento) — reverting el suelo aunque el canon estuviese al día
//     (GDD 11.2: el embargo del inmueble abandonado revierte su suelo).
//
// Por cada edificio no 'seized' de la concesión el embargo (6a):
//  1. lee su stock LIBRE (cuentas stock_free del ledger en su almacén),
//  2. EMITE building.seized con ese stock y su origin_node_id (retirada in situ;
//     el stock NO se mueve aquí — lo publicará/moverá la liquidación de contracts),
//     con reason = "abandoned" si el edificio estaba abandonado, "canon_reverted"
//     en otro caso,
//  3. lo congela (status = 'seized': incomandable, no produce) y para sus lotes.
//
// Después revierte la concesión (status = 'reverted') y EMITE concession.reverted.
// La parcela queda LIBRE: POST /world/concessions solo valida solape contra
// concesiones activas, así que otro jugador puede volver a pedirla.
//
// El reclamo físico completo del edificio en pie (demolición / traspaso intacto a
// otro jugador vía subasta con pujas) es refinamiento de Fase 2; en 6a el embargo
// (i) congela el edificio, (ii) publica su stock vía CCRI del sistema y (iii)
// revierte el suelo.
//
// # Flota
//
// El opex del vehículo (vehicle_types.operating_cost_per_day) se cobra por
// día-sim (cash → sink) cobrando SOLO lo disponible; los días que no puede pagar
// se condonan (sin deuda). El vehículo no tiene condición aquí: su degradación /
// avería las maneja el motor de tránsito (internal/world/fleet).
package enforcement
