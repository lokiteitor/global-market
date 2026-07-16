package logistics

// Avance analítico de vehículos: solo los hitos escriben (fin de segmento,
// llegada, avería, reparación). El progreso intermedio se deriva de
// (segment_entered_sim, duration) — GDD 1.1, ADR-IMPL-07.

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"imperio/engine/internal/core"
	"imperio/engine/internal/db"
	"imperio/engine/internal/ledger"
	"imperio/engine/internal/outbox"
)

const repairSimSeconds = 7200 // 2 h sim de reparación por avería

func (p *Processor) RunTransit(ctx context.Context, simNow int64) {
	p.runRepairs(ctx, simNow)
	p.runAdvances(ctx, simNow)
}

func (p *Processor) runRepairs(ctx context.Context, simNow int64) {
	rows, err := p.Pool.Query(ctx,
		`SELECT id FROM world.vehicles WHERE status = 'broken' AND repair_until_sim <= $1`, simNow)
	if err != nil {
		p.Log.Error("logistics: query reparaciones", "err", err)
		return
	}
	ids, err := scanIDs(rows)
	if err != nil {
		p.Log.Error("logistics: scan reparaciones", "err", err)
		return
	}
	for _, id := range ids {
		id := id
		err := db.RunSerializable(ctx, p.Pool, func(tx pgx.Tx) error {
			var status string
			var repairUntil *int64
			var onSegment *uuid.UUID
			if err := tx.QueryRow(ctx, `
				SELECT status, repair_until_sim, on_segment_id FROM world.vehicles
				 WHERE id = $1 FOR UPDATE`, id).Scan(&status, &repairUntil, &onSegment); err != nil {
				return err
			}
			if status != "broken" || repairUntil == nil || *repairUntil > simNow {
				return nil
			}
			if onSegment != nil {
				// Reanuda REINICIANDO el segmento actual (contrato compartido).
				if _, err := tx.Exec(ctx, `
					UPDATE world.vehicles
					   SET status = 'in_transit', repair_until_sim = NULL,
					       segment_entered_sim = $2, updated_at_sim = $2
					 WHERE id = $1`, id, simNow); err != nil {
					return err
				}
			} else {
				if _, err := tx.Exec(ctx, `
					UPDATE world.vehicles
					   SET status = 'idle', repair_until_sim = NULL, updated_at_sim = $2
					 WHERE id = $1`, id, simNow); err != nil {
					return err
				}
			}
			entity, loc, err := outbox.VehicleEntity(ctx, tx, id, simNow)
			if err != nil {
				return err
			}
			return outbox.Insert(ctx, tx, "vehicle", id, "vehicle.repaired", simNow,
				outbox.Payload(entity, loc, nil))
		})
		if err != nil {
			p.Log.Error("logistics: reparación", "vehicle", id, "err", err)
		}
	}
}

func (p *Processor) runAdvances(ctx context.Context, simNow int64) {
	rows, err := p.Pool.Query(ctx, `
		SELECT id FROM world.vehicles
		 WHERE status = 'in_transit'
		   AND segment_entered_sim + (advance_fn->>'duration_sim_seconds')::bigint <= $1`, simNow)
	if err != nil {
		p.Log.Error("logistics: query avances", "err", err)
		return
	}
	ids, err := scanIDs(rows)
	if err != nil {
		p.Log.Error("logistics: scan avances", "err", err)
		return
	}
	for _, id := range ids {
		id := id
		err := db.RunSerializable(ctx, p.Pool, func(tx pgx.Tx) error {
			return p.advanceOne(ctx, tx, id, simNow)
		})
		if err != nil {
			p.Log.Error("logistics: avance", "vehicle", id, "err", err)
		}
	}
}

func (p *Processor) advanceOne(ctx context.Context, tx pgx.Tx, vehicleID uuid.UUID, simNow int64) error {
	var (
		status       string
		wearPct      int
		fuel         int64
		enteredSim   *int64
		advanceFnRaw []byte
		vehSpeedKmh  int64
		fuelPer100km int64
	)
	err := tx.QueryRow(ctx, `
		SELECT v.status, v.wear_pct, v.fuel, v.segment_entered_sim, v.advance_fn,
		       vt.speed_kmh, vt.fuel_per_100km
		  FROM world.vehicles v
		  JOIN world.vehicle_types vt ON vt.id = v.vehicle_type_id
		 WHERE v.id = $1 FOR UPDATE OF v`, vehicleID).Scan(
		&status, &wearPct, &fuel, &enteredSim, &advanceFnRaw, &vehSpeedKmh, &fuelPer100km)
	if err != nil {
		return err
	}
	if status != "in_transit" || enteredSim == nil || len(advanceFnRaw) == 0 {
		return nil
	}
	var fn core.AdvanceFn
	if err := json.Unmarshal(advanceFnRaw, &fn); err != nil {
		return err
	}
	if *enteredSim+fn.DurationSimSeconds > simNow {
		return nil
	}
	arrivalSim := *enteredSim + fn.DurationSimSeconds

	if fn.LegIndex+1 < len(fn.Path) {
		// Siguiente segmento del camino: recalcula duración con la congestión actual.
		next := fn.Path[fn.LegIndex+1]
		var lengthM, baseSpeed int64
		var congestion float64
		if err := tx.QueryRow(ctx, `
			SELECT ls.length_m, ls.congestion_ema, nl.base_speed_kmh
			  FROM world.link_segments ls
			  JOIN world.network_links nl ON nl.id = ls.link_id
			 WHERE ls.id = $1`, next).Scan(&lengthM, &congestion, &baseSpeed); err != nil {
			return err
		}
		duration, speedEff := core.SegmentDuration(lengthM, baseSpeed, vehSpeedKmh, congestion)
		fn.LegIndex++
		fn.DurationSimSeconds = duration
		fn.SpeedKmhEff = speedEff
		fnJSON, err := json.Marshal(fn)
		if err != nil {
			return err
		}
		// t_entrada = fin real del segmento anterior (sin deriva por ticks).
		if _, err := tx.Exec(ctx, `
			UPDATE world.vehicles
			   SET on_segment_id = $2, segment_entered_sim = $3, advance_fn = $4, updated_at_sim = $5
			 WHERE id = $1`, vehicleID, next, arrivalSim, fnJSON, simNow); err != nil {
			return err
		}
		entity, loc, err := outbox.VehicleEntity(ctx, tx, vehicleID, simNow)
		if err != nil {
			return err
		}
		return outbox.Insert(ctx, tx, "vehicle", vehicleID, "vehicle.segment_entered", simNow,
			outbox.Payload(entity, loc, map[string]any{"segment_id": next.String()}))
	}

	// Último segmento terminado: llegada. Primero, tirada de avería
	// (probabilidad wear_pct × 0.001 por llegada).
	if p.Rand.Float64() < float64(wearPct)*0.001 {
		if _, err := tx.Exec(ctx, `
			UPDATE world.vehicles SET status = 'broken', repair_until_sim = $2, updated_at_sim = $3
			 WHERE id = $1`, vehicleID, simNow+repairSimSeconds, simNow); err != nil {
			return err
		}
		entity, loc, err := outbox.VehicleEntity(ctx, tx, vehicleID, simNow)
		if err != nil {
			return err
		}
		return outbox.Insert(ctx, tx, "vehicle", vehicleID, "vehicle.broken", simNow,
			outbox.Payload(entity, loc, nil))
	}

	// Combustible: consumo del trayecto completo (suelo 0; el repostaje no
	// bloquea en v1, ADR-IMPL-14). Desgaste: +1 por llegada, cap 100.
	var totalM int64
	for _, segID := range fn.Path {
		var l int64
		if err := tx.QueryRow(ctx,
			`SELECT length_m FROM world.link_segments WHERE id = $1`, segID).Scan(&l); err != nil {
			return err
		}
		totalM += l
	}
	fuelUsed := totalM * fuelPer100km / 100000 // m → km/100 × consumo
	newFuel := fuel - fuelUsed
	if newFuel < 0 {
		newFuel = 0
	}
	newWear := wearPct + 1
	if newWear > 100 {
		newWear = 100
	}
	if _, err := tx.Exec(ctx, `
		UPDATE world.vehicles
		   SET status = 'idle', at_node_id = $2, on_segment_id = NULL,
		       segment_entered_sim = NULL, advance_fn = NULL,
		       fuel = $3, wear_pct = $4, updated_at_sim = $5
		 WHERE id = $1`, vehicleID, fn.DestNodeID, newFuel, newWear, simNow); err != nil {
		return err
	}

	// Entrega de los cargamentos a bordo.
	rows, err := tx.Query(ctx, `
		SELECT id, product_id, quantity, contract_id, owner_account_id
		  FROM world.shipments WHERE vehicle_id = $1 AND status = 'in_transit' FOR UPDATE`, vehicleID)
	if err != nil {
		return err
	}
	type ship struct {
		id, product uuid.UUID
		qty         int64
		contractID  *uuid.UUID
		owner       uuid.UUID
	}
	var ships []ship
	for rows.Next() {
		var sh ship
		if err := rows.Scan(&sh.id, &sh.product, &sh.qty, &sh.contractID, &sh.owner); err != nil {
			rows.Close()
			return err
		}
		ships = append(ships, sh)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, sh := range ships {
		if err := p.deliverShipment(ctx, tx, sh.id, sh.product, sh.qty, sh.contractID, sh.owner, fn.DestNodeID, simNow); err != nil {
			return err
		}
	}

	entity, loc, err := outbox.VehicleEntity(ctx, tx, vehicleID, simNow)
	if err != nil {
		return err
	}
	return outbox.Insert(ctx, tx, "vehicle", vehicleID, "vehicle.arrived", simNow,
		outbox.Payload(entity, loc, map[string]any{"node_id": fn.DestNodeID.String()}))
}

// deliverShipment confirma la llegada física de un cargamento de contrato.
func (p *Processor) deliverShipment(ctx context.Context, tx pgx.Tx, shipmentID, product uuid.UUID, qty int64, contractID *uuid.UUID, owner, destNode uuid.UUID, simNow int64) error {
	destBuilding, err := nodeBuilding(ctx, tx, destNode)
	if err != nil {
		return err
	}
	if contractID == nil {
		// Cargamento sin contrato (no ocurre en v1): reposa en el nodo.
		_, err := tx.Exec(ctx, `
			UPDATE world.shipments SET status = 'delivered', vehicle_id = NULL, at_node_id = $2,
			       updated_at_sim = $3 WHERE id = $1`, shipmentID, destNode, simNow)
		return err
	}
	var (
		seller, origin uuid.UUID
		deadline       int64
		status         string
		agreed, deliv  int64
	)
	if err := tx.QueryRow(ctx, `
		SELECT seller_account_id, origin_node_id, deadline_sim, status,
		       quantity_agreed, quantity_delivered
		  FROM ledger.contracts WHERE id = $1 FOR UPDATE`, *contractID).Scan(
		&seller, &origin, &deadline, &status, &agreed, &deliv); err != nil {
		return err
	}

	onTime := status == "active" && simNow <= deadline
	if onTime {
		if _, err := tx.Exec(ctx, `
			UPDATE world.shipments SET status = 'delivered', vehicle_id = NULL, at_node_id = $2,
			       updated_at_sim = $3 WHERE id = $1`, shipmentID, destNode, simNow); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO ledger.contract_deliveries (contract_id, shipment_id, quantity, delivered_at_sim, on_time)
			VALUES ($1, $2, $3, $4, true)`, *contractID, shipmentID, qty, simNow); err != nil {
			return err
		}
		newDelivered := deliv + qty
		if _, err := tx.Exec(ctx, `
			UPDATE ledger.contracts SET quantity_delivered = $2, updated_at = now() WHERE id = $1`,
			*contractID, newDelivered); err != nil {
			return err
		}
		// Físico al edificio destino (un city_gate no tiene edificio: la
		// ciudad lo consume al liquidar).
		if destBuilding != nil {
			if _, err := tx.Exec(ctx, `
				INSERT INTO world.building_inventories (building_id, product_id, quantity, updated_at_sim)
				VALUES ($1, $2, $3, $4)
				ON CONFLICT (building_id, product_id)
				DO UPDATE SET quantity = world.building_inventories.quantity + $3, updated_at_sim = $4`,
				*destBuilding, product, qty, simNow); err != nil {
				return err
			}
		}
		centity, err := outbox.ContractEntity(ctx, tx, *contractID)
		if err != nil {
			return err
		}
		if err := outbox.Insert(ctx, tx, "contract", *contractID, "delivery.confirmed", simNow,
			outbox.Payload(centity, nil, map[string]any{
				"shipment_id": shipmentID.String(),
				"quantity":    qty,
			})); err != nil {
			return err
		}
		if newDelivered == agreed {
			// Cantidad completa: liquidación inmediata (fill 10000).
			return p.Settle(ctx, tx, *contractID, simNow)
		}
		return nil
	}

	// Llegada tardía. Si el vencimiento aún no lo liquidó, liquida primero
	// (libera lo no entregado en stock_free del vendedor en ORIGEN) para que
	// el traslado contable origen→destino tenga saldo del que salir.
	if status == "active" {
		if err := p.Settle(ctx, tx, *contractID, simNow); err != nil {
			return err
		}
	}
	originBuilding, err := nodeBuilding(ctx, tx, origin)
	if err != nil {
		return err
	}
	fromStock, err := ledger.EnsureStockFree(ctx, tx, seller, product, originBuilding)
	if err != nil {
		return err
	}
	toStock, err := ledger.EnsureStockFree(ctx, tx, seller, product, destBuilding)
	if err != nil {
		return err
	}
	if _, err := ledger.PostTx(ctx, tx, "transfer", simNow, contractID,
		"Stock liberado en destino: llegada fuera de plazo", []ledger.Entry{
			{AccountID: fromStock, Amount: -qty},
			{AccountID: toStock, Amount: qty},
		}); err != nil {
		return err
	}
	if destBuilding != nil {
		if _, err := tx.Exec(ctx, `
			INSERT INTO world.building_inventories (building_id, product_id, quantity, updated_at_sim)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (building_id, product_id)
			DO UPDATE SET quantity = world.building_inventories.quantity + $3, updated_at_sim = $4`,
			*destBuilding, product, qty, simNow); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `
		UPDATE world.shipments SET status = 'released_in_situ', vehicle_id = NULL, at_node_id = $2,
		       updated_at_sim = $3 WHERE id = $1`, shipmentID, destNode, simNow)
	return err
}
