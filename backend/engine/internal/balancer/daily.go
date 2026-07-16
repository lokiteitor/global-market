package balancer

// Cargos diarios (frontera de día sim): salarios y mantenimiento de edificios
// en producción, canon de concesiones y nivel urbano por índice de suministro.

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"imperio/engine/internal/db"
	"imperio/engine/internal/ledger"
	"imperio/engine/internal/outbox"
)

const graceSimSeconds = 7 * 86400 // 7 días sim de gracia antes del embargo

// --- salarios y mantenimiento ---------------------------------------------------

func (p *Processor) wagesAndMaintenance(ctx context.Context, simNow int64) {
	// Edificios operativos con lotes en marcha: pagan salarios (de la receta
	// en curso) y mantenimiento del tipo de edificio.
	rows, err := p.Pool.Query(ctx, `
		SELECT DISTINCT b.id FROM world.buildings b
		  JOIN world.production_batches pb ON pb.building_id = b.id AND pb.status = 'running'
		 WHERE b.status = 'operational'`)
	if err != nil {
		p.Log.Error("balancer: query salarios", "err", err)
		return
	}
	ids, err := scanIDs(rows)
	if err != nil {
		p.Log.Error("balancer: scan salarios", "err", err)
		return
	}
	for _, id := range ids {
		id := id
		err := db.RunSerializable(ctx, p.Pool, func(tx pgx.Tx) error {
			return p.chargeBuilding(ctx, tx, id, simNow)
		})
		if err != nil {
			p.Log.Error("balancer: cargos de edificio", "building", id, "err", err)
		}
	}
}

func (p *Processor) chargeBuilding(ctx context.Context, tx pgx.Tx, buildingID uuid.UUID, simNow int64) error {
	var (
		owner           uuid.UUID
		status          string
		conditionPct    int
		maintenanceCost int64
		workers         int64
		baseSalary      *int64
	)
	err := tx.QueryRow(ctx, `
		SELECT b.owner_account_id, b.status, b.condition_pct, bt.maintenance_cost,
		       COALESCE((SELECT SUM(r.workers_required)
		                   FROM world.production_batches pb
		                   JOIN world.recipes r ON r.id = pb.recipe_id
		                  WHERE pb.building_id = b.id AND pb.status = 'running'), 0),
		       (SELECT c.base_salary FROM world.cities c WHERE c.region_id = b.region_id LIMIT 1)
		  FROM world.buildings b
		  JOIN world.building_types bt ON bt.id = b.building_type_id
		 WHERE b.id = $1 FOR UPDATE OF b`, buildingID).Scan(
		&owner, &status, &conditionPct, &maintenanceCost, &workers, &baseSalary)
	if err != nil {
		return err
	}
	if status != "operational" {
		return nil
	}
	cash, err := ledger.CashAccount(ctx, tx, owner)
	if err != nil {
		return err
	}

	// Salarios: workers × salario_base de la ciudad de su región (sink vía
	// NPCs en v1). Si no alcanza, los lotes en marcha pausan por impago.
	if workers > 0 && baseSalary != nil {
		wages := workers * *baseSalary
		balance, err := ledger.Balance(ctx, tx, cash)
		if err != nil {
			return err
		}
		if balance >= wages {
			if _, err := ledger.PostTx(ctx, tx, "wage", simNow, &buildingID,
				"Salarios diarios de producción", []ledger.Entry{
					{AccountID: cash, Amount: -wages},
					{AccountID: p.Bank.SinkAccountID, Amount: wages},
				}); err != nil {
				return err
			}
		} else {
			if _, err := tx.Exec(ctx, `
				UPDATE world.production_batches SET status = 'paused_no_workers', updated_at_sim = $2
				 WHERE building_id = $1 AND status = 'running'`, buildingID, simNow); err != nil {
				return err
			}
		}
	}

	// Mantenimiento: si no alcanza, el edificio se degrada.
	if maintenanceCost > 0 {
		balance, err := ledger.Balance(ctx, tx, cash)
		if err != nil {
			return err
		}
		if balance >= maintenanceCost {
			if _, err := ledger.PostTx(ctx, tx, "maintenance", simNow, &buildingID,
				"Mantenimiento diario de edificio", []ledger.Entry{
					{AccountID: cash, Amount: -maintenanceCost},
					{AccountID: p.Bank.SinkAccountID, Amount: maintenanceCost},
				}); err != nil {
				return err
			}
		} else {
			newCondition := conditionPct - 10
			if newCondition < 0 {
				newCondition = 0
			}
			newStatus := status
			if newCondition < 20 {
				newStatus = "abandoned"
			} else if newCondition < 50 {
				newStatus = "damaged"
			}
			if _, err := tx.Exec(ctx, `
				UPDATE world.buildings SET condition_pct = $2, status = $3, updated_at_sim = $4
				 WHERE id = $1`, buildingID, newCondition, newStatus, simNow); err != nil {
				return err
			}
			if newStatus != status {
				entity, loc, err := outbox.BuildingEntity(ctx, tx, buildingID)
				if err != nil {
					return err
				}
				if err := outbox.Insert(ctx, tx, "building", buildingID, "building.status_changed", simNow,
					outbox.Payload(entity, loc, map[string]any{"previous_status": status})); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// --- canon de concesiones --------------------------------------------------------

func (p *Processor) concessions(ctx context.Context, simNow int64) {
	rows, err := p.Pool.Query(ctx, `
		SELECT id FROM world.land_concessions
		 WHERE expires_at_sim <= $1 AND status IN ('active','delinquent','grace')`, simNow)
	if err != nil {
		p.Log.Error("balancer: query concesiones", "err", err)
		return
	}
	ids, err := scanIDs(rows)
	if err != nil {
		p.Log.Error("balancer: scan concesiones", "err", err)
		return
	}
	for _, id := range ids {
		id := id
		err := db.RunSerializable(ctx, p.Pool, func(tx pgx.Tx) error {
			return p.chargeConcession(ctx, tx, id, simNow)
		})
		if err != nil {
			p.Log.Error("balancer: canon", "concession", id, "err", err)
		}
	}
}

func (p *Processor) chargeConcession(ctx context.Context, tx pgx.Tx, concessionID uuid.UUID, simNow int64) error {
	var (
		holder       uuid.UUID
		canon        int64
		periodDays   int64
		expiresAtSim int64
		status       string
	)
	err := tx.QueryRow(ctx, `
		SELECT holder_account_id, canon_amount, period_sim_days, expires_at_sim, status
		  FROM world.land_concessions WHERE id = $1 FOR UPDATE`, concessionID).Scan(
		&holder, &canon, &periodDays, &expiresAtSim, &status)
	if err != nil {
		return err
	}
	if expiresAtSim > simNow || (status != "active" && status != "delinquent" && status != "grace") {
		return nil
	}
	cash, err := ledger.CashAccount(ctx, tx, holder)
	if err != nil {
		return err
	}
	balance, err := ledger.Balance(ctx, tx, cash)
	if err != nil {
		return err
	}
	if balance >= canon {
		if _, err := ledger.PostTx(ctx, tx, "canon", simNow, &concessionID,
			"Canon de concesión", []ledger.Entry{
				{AccountID: cash, Amount: -canon},
				{AccountID: p.Bank.SinkAccountID, Amount: canon},
			}); err != nil {
			return err
		}
		// Se renueva el periodo. Si venía de mora, la extensión parte de ahora
		// (expires_at_sim se reutilizó como marcador de gracia; comentado).
		newExpires := expiresAtSim + periodDays*86400
		if status != "active" || newExpires <= simNow {
			newExpires = simNow + periodDays*86400
		}
		_, err = tx.Exec(ctx, `
			UPDATE world.land_concessions SET status = 'active', expires_at_sim = $2, updated_at = now()
			 WHERE id = $1`, concessionID, newExpires)
		return err
	}

	// Impago: escala active → delinquent → grace → (7 días sim) → reverted.
	switch status {
	case "active":
		_, err = tx.Exec(ctx, `
			UPDATE world.land_concessions SET status = 'delinquent', updated_at = now() WHERE id = $1`,
			concessionID)
		return err
	case "delinquent":
		// Al entrar en gracia, expires_at_sim pasa a marcar el INICIO de la
		// gracia (no hay columna dedicada en v1; decisión comentada).
		_, err = tx.Exec(ctx, `
			UPDATE world.land_concessions SET status = 'grace', expires_at_sim = $2, updated_at = now()
			 WHERE id = $1`, concessionID, simNow)
		return err
	default: // grace
		if simNow < expiresAtSim+graceSimSeconds {
			return nil
		}
		if _, err := tx.Exec(ctx, `
			UPDATE world.land_concessions SET status = 'reverted', updated_at = now() WHERE id = $1`,
			concessionID); err != nil {
			return err
		}
		// Embargo: los edificios de la concesión pasan a 'seized' (GDD 11.2).
		bRows, err := tx.Query(ctx, `
			SELECT id, status FROM world.buildings WHERE concession_id = $1 AND status <> 'seized'
			 FOR UPDATE`, concessionID)
		if err != nil {
			return err
		}
		type b struct {
			id     uuid.UUID
			status string
		}
		var bs []b
		for bRows.Next() {
			var x b
			if err := bRows.Scan(&x.id, &x.status); err != nil {
				bRows.Close()
				return err
			}
			bs = append(bs, x)
		}
		bRows.Close()
		if err := bRows.Err(); err != nil {
			return err
		}
		for _, x := range bs {
			if _, err := tx.Exec(ctx, `
				UPDATE world.buildings SET status = 'seized', updated_at_sim = $2 WHERE id = $1`,
				x.id, simNow); err != nil {
				return err
			}
			entity, loc, err := outbox.BuildingEntity(ctx, tx, x.id)
			if err != nil {
				return err
			}
			if err := outbox.Insert(ctx, tx, "building", x.id, "building.status_changed", simNow,
				outbox.Payload(entity, loc, map[string]any{"previous_status": x.status})); err != nil {
				return err
			}
		}
		return nil
	}
}

// --- nivel urbano ------------------------------------------------------------------

func (p *Processor) cityLevels(ctx context.Context, simNow int64) {
	rows, err := p.Pool.Query(ctx,
		`SELECT id FROM world.cities WHERE supply_index > level * 1000`)
	if err != nil {
		p.Log.Error("balancer: query niveles", "err", err)
		return
	}
	ids, err := scanIDs(rows)
	if err != nil {
		p.Log.Error("balancer: scan niveles", "err", err)
		return
	}
	for _, id := range ids {
		id := id
		err := db.RunSerializable(ctx, p.Pool, func(tx pgx.Tx) error {
			var level int
			var supplyIndex float64
			if err := tx.QueryRow(ctx,
				`SELECT level, supply_index FROM world.cities WHERE id = $1 FOR UPDATE`, id).
				Scan(&level, &supplyIndex); err != nil {
				return err
			}
			if supplyIndex <= float64(level*1000) {
				return nil
			}
			if _, err := tx.Exec(ctx,
				`UPDATE world.cities SET level = level + 1, updated_at_sim = $2 WHERE id = $1`,
				id, simNow); err != nil {
				return err
			}
			// La demanda base crece un 50% al subir de nivel (×3/2 en entero).
			if _, err := tx.Exec(ctx, `
				UPDATE world.city_demand SET d0_per_sim_day = d0_per_sim_day * 3 / 2, updated_at_sim = $2
				 WHERE city_id = $1`, id, simNow); err != nil {
				return err
			}
			entity, loc, err := outbox.CityEntity(ctx, tx, id)
			if err != nil {
				return err
			}
			return outbox.Insert(ctx, tx, "city", id, "city.level_changed", simNow,
				outbox.Payload(entity, loc, map[string]any{"previous_level": level}))
		})
		if err != nil {
			p.Log.Error("balancer: nivel de ciudad", "city", id, "err", err)
		}
	}
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
