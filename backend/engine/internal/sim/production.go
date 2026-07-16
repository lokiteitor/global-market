// Package sim ejecuta la producción dirigida por tiempo: fin de obra de
// edificios, arranque y fin de lotes, consumo/emisión de stock (ADR-IMPL-12 y
// ADR-IMPL-14). Cada unidad de trabajo corre en su propia transacción
// SERIALIZABLE; un fallo se registra y no bloquea al resto.
package sim

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"imperio/engine/internal/core"
	"imperio/engine/internal/db"
	"imperio/engine/internal/ledger"
	"imperio/engine/internal/outbox"
)

const constructionSimSeconds = 14400 // 4 h sim de obra (ADR-IMPL-14)

type Processor struct {
	Pool *pgxpool.Pool
	Bank core.BankRefs
	Log  *slog.Logger
}

func (p *Processor) Run(ctx context.Context, simNow int64) {
	p.finishConstruction(ctx, simNow)
	p.finishBatches(ctx, simNow)
	p.startBatches(ctx, simNow)
}

// --- fin de obra -------------------------------------------------------------

func (p *Processor) finishConstruction(ctx context.Context, simNow int64) {
	ids := p.collect(ctx, `SELECT id FROM world.buildings
		WHERE status = 'under_construction' AND updated_at_sim + $2 <= $1`, simNow, constructionSimSeconds)
	for _, id := range ids {
		id := id
		err := db.RunSerializable(ctx, p.Pool, func(tx pgx.Tx) error {
			var updatedAtSim int64
			var status string
			if err := tx.QueryRow(ctx,
				`SELECT status, updated_at_sim FROM world.buildings WHERE id = $1 FOR UPDATE`, id).
				Scan(&status, &updatedAtSim); err != nil {
				return err
			}
			if status != "under_construction" || updatedAtSim+constructionSimSeconds > simNow {
				return nil // otro proceso lo resolvió: idempotencia
			}
			if _, err := tx.Exec(ctx,
				`UPDATE world.buildings SET status = 'operational', updated_at_sim = $2 WHERE id = $1`,
				id, simNow); err != nil {
				return err
			}
			entity, loc, err := outbox.BuildingEntity(ctx, tx, id)
			if err != nil {
				return err
			}
			return outbox.Insert(ctx, tx, "building", id, "building.status_changed", simNow,
				outbox.Payload(entity, loc, map[string]any{"previous_status": "under_construction"}))
		})
		if err != nil {
			p.Log.Error("sim: fin de obra", "building", id, "err", err)
		}
	}
}

// --- fin de lote -------------------------------------------------------------

func (p *Processor) finishBatches(ctx context.Context, simNow int64) {
	ids := p.collect(ctx, `
		SELECT pb.id FROM world.production_batches pb
		JOIN world.recipes r ON r.id = pb.recipe_id
		WHERE pb.status = 'running' AND pb.started_at_sim + r.batch_sim_seconds <= $1`, simNow)
	for _, id := range ids {
		id := id
		err := db.RunSerializable(ctx, p.Pool, func(tx pgx.Tx) error {
			return p.finishOne(ctx, tx, id, simNow)
		})
		if err != nil {
			p.Log.Error("sim: fin de lote", "batch", id, "err", err)
		}
	}
}

func (p *Processor) finishOne(ctx context.Context, tx pgx.Tx, batchID uuid.UUID, simNow int64) error {
	var (
		buildingID, recipeID, owner uuid.UUID
		queued, done                int
		status                      string
		startedAtSim                *int64
		batchSimSeconds             int64
	)
	err := tx.QueryRow(ctx, `
		SELECT pb.building_id, pb.recipe_id, pb.batches_queued, pb.batches_done, pb.status,
		       pb.started_at_sim, r.batch_sim_seconds, b.owner_account_id
		  FROM world.production_batches pb
		  JOIN world.recipes r ON r.id = pb.recipe_id
		  JOIN world.buildings b ON b.id = pb.building_id
		 WHERE pb.id = $1 FOR UPDATE OF pb`, batchID).Scan(
		&buildingID, &recipeID, &queued, &done, &status, &startedAtSim, &batchSimSeconds, &owner)
	if err != nil {
		return err
	}
	if status != "running" || startedAtSim == nil || *startedAtSim+batchSimSeconds > simNow {
		return nil // idempotencia: ya lo resolvió otra pasada
	}

	// Produce las salidas: inventario físico + / asiento production_output
	// (stock_free + / emission(producto) −). ADR-IMPL-12.
	outputs, err := p.ingredients(ctx, tx, recipeID, "output", uuid.Nil, 0)
	if err != nil {
		return err
	}
	var entries []ledger.Entry
	for prod, qty := range outputs {
		if err := upsertInventory(ctx, tx, buildingID, prod, qty, simNow); err != nil {
			return err
		}
		stockFree, err := ledger.EnsureStockFree(ctx, tx, owner, prod, &buildingID)
		if err != nil {
			return err
		}
		emission, err := ledger.EnsureEmissionStock(ctx, tx, p.Bank.BancoCentralID, prod)
		if err != nil {
			return err
		}
		entries = append(entries, ledger.Entry{AccountID: stockFree, Amount: qty},
			ledger.Entry{AccountID: emission, Amount: -qty})
	}
	if len(entries) > 0 {
		if _, err := ledger.PostTx(ctx, tx, "production_output", simNow, &batchID,
			"Salida de lote de producción", entries); err != nil {
			return err
		}
	}

	done++
	if done < queued {
		// Intenta arrancar el siguiente lote de la misma orden de inmediato.
		started, pausedNoFuel, err := p.tryConsumeInputs(ctx, tx, buildingID, recipeID, owner, simNow)
		if err != nil {
			return err
		}
		newStatus := "queued"
		var newStarted *int64
		if started {
			newStatus = "running"
			newStarted = &simNow
		} else if pausedNoFuel {
			newStatus = "paused_no_fuel"
		}
		_, err = tx.Exec(ctx, `
			UPDATE world.production_batches
			   SET batches_done = $2, status = $3, started_at_sim = $4, updated_at_sim = $5
			 WHERE id = $1`, batchID, done, newStatus, newStarted, simNow)
		return err
	}

	// Orden completada.
	if _, err := tx.Exec(ctx, `
		UPDATE world.production_batches
		   SET batches_done = $2, status = 'completed', started_at_sim = NULL, updated_at_sim = $3
		 WHERE id = $1`, batchID, done, simNow); err != nil {
		return err
	}
	entity, err := outbox.BatchEntity(ctx, tx, batchID)
	if err != nil {
		return err
	}
	return outbox.Insert(ctx, tx, "production_batch", batchID, "batch.completed", simNow,
		outbox.Payload(entity, nil, map[string]any{"building_id": buildingID.String()}))
}

// --- arranque de lote ---------------------------------------------------------

// startBatches arranca (o reintenta) el lote de menor queue_position de cada
// edificio operativo sin lote en curso.
func (p *Processor) startBatches(ctx context.Context, simNow int64) {
	rows, err := p.Pool.Query(ctx, `
		SELECT DISTINCT ON (pb.building_id) pb.id
		  FROM world.production_batches pb
		  JOIN world.buildings b ON b.id = pb.building_id
		 WHERE b.status = 'operational'
		   AND pb.status IN ('queued','paused_no_fuel','paused_no_workers')
		   AND NOT EXISTS (SELECT 1 FROM world.production_batches r
		                    WHERE r.building_id = pb.building_id AND r.status = 'running')
		 ORDER BY pb.building_id, pb.queue_position, pb.created_at`)
	if err != nil {
		p.Log.Error("sim: query arranques", "err", err)
		return
	}
	ids, err := scanIDs(rows)
	if err != nil {
		p.Log.Error("sim: scan arranques", "err", err)
		return
	}
	for _, id := range ids {
		id := id
		err := db.RunSerializable(ctx, p.Pool, func(tx pgx.Tx) error {
			return p.startOne(ctx, tx, id, simNow)
		})
		if err != nil {
			p.Log.Error("sim: arranque de lote", "batch", id, "err", err)
		}
	}
}

func (p *Processor) startOne(ctx context.Context, tx pgx.Tx, batchID uuid.UUID, simNow int64) error {
	var (
		buildingID, recipeID, owner uuid.UUID
		status, bStatus             string
	)
	err := tx.QueryRow(ctx, `
		SELECT pb.building_id, pb.recipe_id, pb.status, b.status, b.owner_account_id
		  FROM world.production_batches pb
		  JOIN world.buildings b ON b.id = pb.building_id
		 WHERE pb.id = $1 FOR UPDATE OF pb`, batchID).Scan(&buildingID, &recipeID, &status, &bStatus, &owner)
	if err != nil {
		return err
	}
	if bStatus != "operational" ||
		(status != "queued" && status != "paused_no_fuel" && status != "paused_no_workers") {
		return nil
	}
	// Un lote 'paused_no_workers' ya consumió sus insumos al arrancar: se
	// reanuda sin volver a consumir. Se reinicia el temporizador (el progreso
	// durante la pausa se pierde: decisión v1, comentada).
	if status == "paused_no_workers" {
		_, err = tx.Exec(ctx, `
			UPDATE world.production_batches
			   SET status = 'running', started_at_sim = $2, updated_at_sim = $2
			 WHERE id = $1`, batchID, simNow)
		return err
	}
	started, pausedNoFuel, err := p.tryConsumeInputs(ctx, tx, buildingID, recipeID, owner, simNow)
	if err != nil {
		return err
	}
	switch {
	case started:
		_, err = tx.Exec(ctx, `
			UPDATE world.production_batches
			   SET status = 'running', started_at_sim = $2, updated_at_sim = $2
			 WHERE id = $1`, batchID, simNow)
	case pausedNoFuel && status != "paused_no_fuel":
		_, err = tx.Exec(ctx, `
			UPDATE world.production_batches SET status = 'paused_no_fuel', updated_at_sim = $2
			 WHERE id = $1`, batchID, simNow)
	case !pausedNoFuel && status == "paused_no_fuel":
		// Ya hay combustible pero falta otro insumo: vuelve a la cola.
		_, err = tx.Exec(ctx, `
			UPDATE world.production_batches SET status = 'queued', updated_at_sim = $2
			 WHERE id = $1`, batchID, simNow)
	}
	return err
}

// tryConsumeInputs verifica insumos (ingredientes 'input' + combustible como
// un insumo más, ADR-IMPL-14) en inventario físico Y en stock_free del dueño;
// si alcanzan, los consume (inventario − y asiento 'consumption'). Devuelve
// (arrancó, pausaPorCombustible).
func (p *Processor) tryConsumeInputs(ctx context.Context, tx pgx.Tx, buildingID, recipeID, owner uuid.UUID, simNow int64) (bool, bool, error) {
	var fuelProduct *uuid.UUID
	var fuelPerBatch int64
	if err := tx.QueryRow(ctx,
		`SELECT fuel_product_id, fuel_per_batch FROM world.recipes WHERE id = $1`, recipeID).
		Scan(&fuelProduct, &fuelPerBatch); err != nil {
		return false, false, err
	}
	fuelID := uuid.Nil
	if fuelProduct != nil && fuelPerBatch > 0 {
		fuelID = *fuelProduct
	}
	needed, err := p.ingredients(ctx, tx, recipeID, "input", fuelID, fuelPerBatch)
	if err != nil {
		return false, false, err
	}
	if len(needed) == 0 {
		return true, false, nil // receta sin insumos (extracción primaria)
	}

	// Comprueba disponibilidad física y contable de todos los insumos.
	for prod, qty := range needed {
		var physical int64
		err := tx.QueryRow(ctx,
			`SELECT quantity FROM world.building_inventories
			  WHERE building_id = $1 AND product_id = $2 FOR UPDATE`, buildingID, prod).Scan(&physical)
		if errors.Is(err, pgx.ErrNoRows) {
			physical = 0
		} else if err != nil {
			return false, false, err
		}
		var free int64
		err = tx.QueryRow(ctx, `
			SELECT balance FROM ledger.accounts
			 WHERE kind = 'stock_free' AND owner_account_id = $1 AND product_id = $2
			   AND warehouse_building_id = $3`, owner, prod, buildingID).Scan(&free)
		if errors.Is(err, pgx.ErrNoRows) {
			free = 0
		} else if err != nil {
			return false, false, err
		}
		if physical < qty || free < qty {
			// Si el insumo que falta es el combustible: pausa específica.
			return false, prod == fuelID, nil
		}
	}

	// Consume: inventario físico − y asiento 'consumption'
	// (stock_free − / emission(producto) +). ADR-IMPL-12.
	var entries []ledger.Entry
	for prod, qty := range needed {
		if _, err := tx.Exec(ctx, `
			UPDATE world.building_inventories
			   SET quantity = quantity - $3, updated_at_sim = $4
			 WHERE building_id = $1 AND product_id = $2`, buildingID, prod, qty, simNow); err != nil {
			return false, false, err
		}
		stockFree, err := ledger.EnsureStockFree(ctx, tx, owner, prod, &buildingID)
		if err != nil {
			return false, false, err
		}
		emission, err := ledger.EnsureEmissionStock(ctx, tx, p.Bank.BancoCentralID, prod)
		if err != nil {
			return false, false, err
		}
		entries = append(entries, ledger.Entry{AccountID: stockFree, Amount: -qty},
			ledger.Entry{AccountID: emission, Amount: qty})
	}
	if _, err := ledger.PostTx(ctx, tx, "consumption", simNow, &recipeID,
		"Consumo de insumos de lote", entries); err != nil {
		return false, false, err
	}
	return true, false, nil
}

// ingredients suma los ingredientes de un rol; con fuelID != Nil añade el
// combustible como un insumo más del inventario.
func (p *Processor) ingredients(ctx context.Context, tx pgx.Tx, recipeID uuid.UUID, role string, fuelID uuid.UUID, fuelQty int64) (map[uuid.UUID]int64, error) {
	rows, err := tx.Query(ctx,
		`SELECT product_id, quantity FROM world.recipe_ingredients WHERE recipe_id = $1 AND role = $2`,
		recipeID, role)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[uuid.UUID]int64{}
	for rows.Next() {
		var prod uuid.UUID
		var qty int64
		if err := rows.Scan(&prod, &qty); err != nil {
			return nil, err
		}
		out[prod] += qty
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if fuelID != uuid.Nil && fuelQty > 0 {
		out[fuelID] += fuelQty
	}
	return out, nil
}

func upsertInventory(ctx context.Context, tx pgx.Tx, buildingID, productID uuid.UUID, delta, simNow int64) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO world.building_inventories (building_id, product_id, quantity, updated_at_sim)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (building_id, product_id)
		DO UPDATE SET quantity = world.building_inventories.quantity + $3, updated_at_sim = $4`,
		buildingID, productID, delta, simNow)
	if err != nil {
		return fmt.Errorf("inventario %s/%s: %w", buildingID, productID, err)
	}
	return nil
}

func (p *Processor) collect(ctx context.Context, sql string, args ...any) []uuid.UUID {
	rows, err := p.Pool.Query(ctx, sql, args...)
	if err != nil {
		p.Log.Error("sim: query vencidos", "err", err)
		return nil
	}
	ids, err := scanIDs(rows)
	if err != nil {
		p.Log.Error("sim: scan vencidos", "err", err)
		return nil
	}
	return ids
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
