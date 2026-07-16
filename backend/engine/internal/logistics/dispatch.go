package logistics

// Auto-despacho (ADR-IMPL-13): para cada contrato activo con entrega física
// pendiente, el motor despacha vehículos 'idle' del vendedor situados en el
// nodo de origen hacia el destino por el camino más corto (peso = longitud ×
// congestión EMA).

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"imperio/engine/internal/core"
	"imperio/engine/internal/db"
	"imperio/engine/internal/outbox"
)

// SettleFunc la inyecta el módulo de contratos (main hace el cableado): evita
// un import cruzado logistics→contracts manteniendo un único camino de
// liquidación con el flujo de ciudades.
type SettleFunc func(ctx context.Context, tx pgx.Tx, contractID uuid.UUID, simNow int64) error

type Processor struct {
	Pool   *pgxpool.Pool
	Bank   core.BankRefs
	Log    *slog.Logger
	Rand   *rand.Rand
	Settle SettleFunc
}

func (p *Processor) RunDispatch(ctx context.Context, simNow int64) {
	// Contratos activos con entrega física (origen ≠ destino) y cantidad sin
	// cubrir (pactada − entregada − en tránsito).
	rows, err := p.Pool.Query(ctx, `
		SELECT c.id FROM ledger.contracts c
		 WHERE c.status = 'active' AND c.origin_node_id <> c.destination_node_id
		   AND c.quantity_agreed - c.quantity_delivered -
		       COALESCE((SELECT SUM(s.quantity) FROM world.shipments s
		                  WHERE s.contract_id = c.id AND s.status = 'in_transit'), 0) > 0`)
	if err != nil {
		p.Log.Error("logistics: query despachos", "err", err)
		return
	}
	ids, err := scanIDs(rows)
	if err != nil {
		p.Log.Error("logistics: scan despachos", "err", err)
		return
	}
	for _, id := range ids {
		id := id
		err := db.RunSerializable(ctx, p.Pool, func(tx pgx.Tx) error {
			return p.dispatchOne(ctx, tx, id, simNow)
		})
		if err != nil {
			p.Log.Error("logistics: despacho", "contract", id, "err", err)
		}
	}
}

func (p *Processor) dispatchOne(ctx context.Context, tx pgx.Tx, contractID uuid.UUID, simNow int64) error {
	var (
		seller, product, origin, dest uuid.UUID
		agreed, delivered             int64
		status                        string
	)
	err := tx.QueryRow(ctx, `
		SELECT seller_account_id, product_id, origin_node_id, destination_node_id,
		       quantity_agreed, quantity_delivered, status
		  FROM ledger.contracts WHERE id = $1 FOR UPDATE`, contractID).Scan(
		&seller, &product, &origin, &dest, &agreed, &delivered, &status)
	if err != nil {
		return err
	}
	if status != "active" || origin == dest {
		return nil
	}
	var inTransit int64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(quantity), 0) FROM world.shipments
		 WHERE contract_id = $1 AND status = 'in_transit'`, contractID).Scan(&inTransit); err != nil {
		return err
	}
	uncovered := agreed - delivered - inTransit
	if uncovered <= 0 {
		return nil
	}

	// Vehículos ociosos del vendedor en el nodo de origen (modo road en v1).
	type veh struct {
		id            uuid.UUID
		cargoCapacity int64
		speedKmh      int64
	}
	rows, err := tx.Query(ctx, `
		SELECT v.id, vt.cargo_capacity, vt.speed_kmh
		  FROM world.vehicles v
		  JOIN world.vehicle_types vt ON vt.id = v.vehicle_type_id
		 WHERE v.owner_account_id = $1 AND v.status = 'idle' AND v.at_node_id = $2
		   AND vt.mode = 'road'
		 ORDER BY vt.cargo_capacity DESC
		 FOR UPDATE OF v`, seller, origin)
	if err != nil {
		return err
	}
	var vehicles []veh
	for rows.Next() {
		var v veh
		if err := rows.Scan(&v.id, &v.cargoCapacity, &v.speedKmh); err != nil {
			rows.Close()
			return err
		}
		vehicles = append(vehicles, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(vehicles) == 0 {
		return nil
	}

	var unitVolume int64
	if err := tx.QueryRow(ctx,
		`SELECT unit_volume FROM world.products WHERE id = $1`, product).Scan(&unitVolume); err != nil {
		return err
	}
	edges, err := LoadGraph(ctx, tx, "road")
	if err != nil {
		return err
	}
	steps, ok := ShortestPath(edges, origin, dest)
	if !ok || len(steps) == 0 {
		p.Log.Warn("logistics: sin camino origen→destino", "contract", contractID,
			"origin", origin, "dest", dest)
		return nil
	}
	// Aplana los segmentos del camino en el orden de viaje.
	var segs []SegmentRef
	for _, st := range steps {
		segs = append(segs, st.Segments...)
	}
	segIDs := make([]uuid.UUID, len(segs))
	segStep := make([]int, len(segs)) // índice del step de cada segmento (para su base_speed)
	k := 0
	for si, st := range steps {
		for range st.Segments {
			segIDs[k] = segs[k].ID
			segStep[k] = si
			k++
		}
	}

	originBuilding, err := nodeBuilding(ctx, tx, origin)
	if err != nil {
		return err
	}
	loc, err := outbox.NodeLocation(ctx, tx, origin)
	if err != nil {
		return err
	}

	for _, v := range vehicles {
		if uncovered <= 0 {
			break
		}
		capUnits := v.cargoCapacity / unitVolume
		qty := min64(uncovered, capUnits)
		if qty <= 0 {
			continue
		}
		// El físico debe salir del almacén de origen (si el nodo tiene
		// edificio; en el fallback sin edificio se omite el físico).
		if originBuilding != nil {
			var physical int64
			err := tx.QueryRow(ctx, `
				SELECT quantity FROM world.building_inventories
				 WHERE building_id = $1 AND product_id = $2 FOR UPDATE`, *originBuilding, product).Scan(&physical)
			if errors.Is(err, pgx.ErrNoRows) {
				physical = 0
			} else if err != nil {
				return err
			}
			qty = min64(qty, physical)
			if qty <= 0 {
				p.Log.Warn("logistics: sin stock físico que cargar", "contract", contractID)
				break
			}
			if _, err := tx.Exec(ctx, `
				UPDATE world.building_inventories
				   SET quantity = quantity - $3, updated_at_sim = $4
				 WHERE building_id = $1 AND product_id = $2`, *originBuilding, product, qty, simNow); err != nil {
				return err
			}
		}

		var shipmentID uuid.UUID
		if err := tx.QueryRow(ctx, `
			INSERT INTO world.shipments (owner_account_id, product_id, quantity, contract_id,
			                             vehicle_id, status, updated_at_sim)
			VALUES ($1, $2, $3, $4, $5, 'in_transit', $6) RETURNING id`,
			seller, product, qty, contractID, v.id, simNow).Scan(&shipmentID); err != nil {
			return err
		}

		first := segs[0]
		duration, speedEff := core.SegmentDuration(first.LengthM, steps[segStep[0]].BaseSpeedKmh, v.speedKmh, first.Congestion)
		cid := contractID
		fn := core.AdvanceFn{
			Path:               segIDs,
			LegIndex:           0,
			DurationSimSeconds: duration,
			SpeedKmhEff:        speedEff,
			DestNodeID:         dest,
			ContractID:         &cid,
		}
		fnJSON, err := json.Marshal(fn)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE world.vehicles
			   SET status = 'in_transit', at_node_id = NULL, on_segment_id = $2,
			       segment_entered_sim = $3, advance_fn = $4, updated_at_sim = $3
			 WHERE id = $1`, v.id, first.ID, simNow, fnJSON); err != nil {
			return err
		}
		entity, _, err := outbox.VehicleEntity(ctx, tx, v.id, simNow)
		if err != nil {
			return err
		}
		if err := outbox.Insert(ctx, tx, "vehicle", v.id, "vehicle.departed", simNow,
			outbox.Payload(entity, loc, map[string]any{
				"contract_id": contractID.String(),
				"shipment_id": shipmentID.String(),
			})); err != nil {
			return err
		}
		uncovered -= qty
	}
	return nil
}

func nodeBuilding(ctx context.Context, q core.Querier, nodeID uuid.UUID) (*uuid.UUID, error) {
	var b *uuid.UUID
	if err := q.QueryRow(ctx,
		`SELECT building_id FROM world.network_nodes WHERE id = $1`, nodeID).Scan(&b); err != nil {
		return nil, err
	}
	return b, nil
}

func scanIDs(rows pgx.Rows) ([]uuid.UUID, error) {
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
