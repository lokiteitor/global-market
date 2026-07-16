// Package contracts ejecuta los flujos de mercado dirigidos por tiempo:
// cierre de ventanas de sorteo (wall-clock), confirmación de contratos con
// bloqueo triple, liquidaciones (fill completo, vencimiento pro-rata) y el
// flujo de consumo de las ciudades compradoras.
package contracts

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"imperio/engine/internal/core"
	"imperio/engine/internal/db"
	"imperio/engine/internal/ledger"
	"imperio/engine/internal/outbox"
)

// compensationBp: parte de la garantía incumplida que compensa al comprador
// (el resto va al sink). Valor v1 del contrato compartido.
const compensationBp = 5000

type Service struct {
	Pool *pgxpool.Pool
	Bank core.BankRefs
	Log  *slog.Logger
	Rand *rand.Rand
}

// RunDeadlines liquida pro-rata los contratos vencidos (deadline_sim <= now).
func (s *Service) RunDeadlines(ctx context.Context, simNow int64) {
	rows, err := s.Pool.Query(ctx,
		`SELECT id FROM ledger.contracts WHERE status = 'active' AND deadline_sim <= $1`, simNow)
	if err != nil {
		s.Log.Error("contracts: query vencimientos", "err", err)
		return
	}
	ids, err := scanIDs(rows)
	if err != nil {
		s.Log.Error("contracts: scan vencimientos", "err", err)
		return
	}
	for _, id := range ids {
		id := id
		err := db.RunSerializable(ctx, s.Pool, func(tx pgx.Tx) error {
			return s.SettleWithCityFlow(ctx, tx, id, simNow)
		})
		if err != nil {
			s.Log.Error("contracts: vencimiento", "contract", id, "err", err)
		}
	}
}

// SettleWithCityFlow liquida un contrato activo vía ledger.settle_contract_prorata
// y, si el comprador es una ciudad, consume el stock entregado y actualiza su
// curva de demanda. Idempotente: si el contrato ya no está activo, no hace nada.
// Se usa desde el sorteo (venta in situ), el vencimiento y la última entrega.
func (s *Service) SettleWithCityFlow(ctx context.Context, tx pgx.Tx, contractID uuid.UUID, simNow int64) error {
	var (
		buyer, seller, product, origin, dest uuid.UUID
		delivered                            int64
		status, buyerKind                    string
	)
	err := tx.QueryRow(ctx, `
		SELECT c.buyer_account_id, c.seller_account_id, c.product_id,
		       c.origin_node_id, c.destination_node_id, c.quantity_delivered, c.status, a.kind
		  FROM ledger.contracts c
		  JOIN auth.accounts a ON a.id = c.buyer_account_id
		 WHERE c.id = $1 FOR UPDATE OF c`, contractID).Scan(
		&buyer, &seller, &product, &origin, &dest, &delivered, &status, &buyerKind)
	if err != nil {
		return err
	}
	if status != "active" {
		return nil
	}

	destBuilding, err := nodeBuilding(ctx, tx, dest)
	if err != nil {
		return err
	}
	originBuilding, err := nodeBuilding(ctx, tx, origin)
	if err != nil {
		return err
	}
	// Comprador: stock libre en el almacén de DESTINO (en venta in situ,
	// destino == origen). Vendedor: liberación del no entregado en ORIGEN.
	buyerStock, err := ledger.EnsureStockFree(ctx, tx, buyer, product, destBuilding)
	if err != nil {
		return err
	}
	sellerRelease, err := ledger.EnsureStockFree(ctx, tx, seller, product, originBuilding)
	if err != nil {
		return err
	}
	sellerCash, err := ledger.CashAccount(ctx, tx, seller)
	if err != nil {
		return err
	}
	buyerCash, err := ledger.CashAccount(ctx, tx, buyer)
	if err != nil {
		return err
	}
	if err := ledger.SettleContractProrata(ctx, tx, contractID, simNow,
		sellerCash, buyerCash, buyerStock, s.Bank.SinkAccountID, sellerRelease, compensationBp); err != nil {
		return err
	}
	entity, err := outbox.ContractEntity(ctx, tx, contractID)
	if err != nil {
		return err
	}
	if err := outbox.Insert(ctx, tx, "contract", contractID, "contract.settled", simNow,
		outbox.Payload(entity, nil, nil)); err != nil {
		return err
	}
	if buyerKind == "city" && delivered > 0 {
		return s.cityConsume(ctx, tx, contractID, buyer, product, buyerStock, destBuilding, delivered, simNow)
	}
	return nil
}

// cityConsume: la ciudad consume el stock recibido (asiento 'consumption'),
// refuerza supply_ema/supply_index y recalcula el precio efectivo.
func (s *Service) cityConsume(ctx context.Context, tx pgx.Tx, contractID, cityAccount, product, cityStock uuid.UUID, destBuilding *uuid.UUID, qty, simNow int64) error {
	emission, err := ledger.EnsureEmissionStock(ctx, tx, s.Bank.BancoCentralID, product)
	if err != nil {
		return err
	}
	if _, err := ledger.PostTx(ctx, tx, "consumption", simNow, &contractID,
		"Consumo urbano del contrato", []ledger.Entry{
			{AccountID: cityStock, Amount: -qty},
			{AccountID: emission, Amount: qty},
		}); err != nil {
		return err
	}
	// El físico solo existe si el nodo destino tiene edificio (un city_gate
	// no lo tiene: la mercancía entra directamente en la ciudad).
	if destBuilding != nil {
		if _, err := tx.Exec(ctx, `
			UPDATE world.building_inventories
			   SET quantity = quantity - LEAST(quantity, $3), updated_at_sim = $4
			 WHERE building_id = $1 AND product_id = $2`, *destBuilding, product, qty, simNow); err != nil {
			return err
		}
	}

	var cityID uuid.UUID
	if err := tx.QueryRow(ctx,
		`SELECT id FROM world.cities WHERE account_id = $1`, cityAccount).Scan(&cityID); err != nil {
		return err
	}
	var (
		d0                               int64
		supplyEma                        float64
		basePrice, priceFloor, priceCeil int64
		class                            string
	)
	err = tx.QueryRow(ctx, `
		SELECT cd.d0_per_sim_day, cd.supply_ema, p.base_price, p.price_floor, p.price_ceiling, p.class
		  FROM world.city_demand cd
		  JOIN world.products p ON p.id = cd.product_id
		 WHERE cd.city_id = $1 AND cd.product_id = $2 FOR UPDATE OF cd`, cityID, product).Scan(
		&d0, &supplyEma, &basePrice, &priceFloor, &priceCeil, &class)
	if errors.Is(err, pgx.ErrNoRows) {
		// Producto sin curva de demanda en esta ciudad: solo consumo + índice.
		_, err = tx.Exec(ctx,
			`UPDATE world.cities SET supply_index = supply_index + $2, updated_at_sim = $3 WHERE id = $1`,
			cityID, qty, simNow)
		return err
	}
	if err != nil {
		return err
	}
	// Cantidad "normalizada a día": las compras de ciudad se pactan con plazo
	// de 1 día sim (86400), así el refuerzo qty ya es ≈ unidades/día. El decay
	// periódico del Balancer amortigua el resto (decisión v1 comentada).
	supplyEma += float64(qty)
	price, saturation := core.CityPrice(basePrice, priceFloor, priceCeil, d0, supplyEma, class)
	if _, err := tx.Exec(ctx, `
		UPDATE world.city_demand
		   SET supply_ema = $3, current_price = $4, saturation_factor = $5, updated_at_sim = $6
		 WHERE city_id = $1 AND product_id = $2`,
		cityID, product, supplyEma, price, saturation, simNow); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE world.cities SET supply_index = supply_index + $2, updated_at_sim = $3 WHERE id = $1`,
		cityID, qty, simNow); err != nil {
		return err
	}
	entity, err := outbox.CityDemandEntity(ctx, tx, cityID, product)
	if err != nil {
		return err
	}
	_, loc, err := outbox.CityEntity(ctx, tx, cityID)
	if err != nil {
		return err
	}
	return outbox.Insert(ctx, tx, "city", cityID, "city.demand_updated", simNow,
		outbox.Payload(entity, loc, nil))
}

func nodeBuilding(ctx context.Context, q core.Querier, nodeID uuid.UUID) (*uuid.UUID, error) {
	var b *uuid.UUID
	err := q.QueryRow(ctx,
		`SELECT building_id FROM world.network_nodes WHERE id = $1`, nodeID).Scan(&b)
	if err != nil {
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
