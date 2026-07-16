package contracts

// Sorteo de ventanas (draw_window / micro_window): mecánica en TIEMPO REAL
// (wall-clock). Al cierre se baraja el orden de las aceptaciones pendientes y
// se sirven hasta agotar la cantidad restante (servicio parcial permitido);
// la latencia no otorga ventaja (ADR-011).

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"imperio/engine/internal/db"
	"imperio/engine/internal/ledger"
	"imperio/engine/internal/outbox"
)

type publication struct {
	ID                 uuid.UUID
	Kind               string
	Publisher          uuid.UUID
	Channel            string
	Product            uuid.UUID
	QuantityRemaining  int64
	UnitPrice          int64
	OriginNode         *uuid.UUID
	DestinationNode    *uuid.UUID
	DeliverySimSeconds int64
	StockReserveAcc    *uuid.UUID
	GuaranteeAcc       *uuid.UUID
	EscrowAcc          *uuid.UUID
}

type acceptance struct {
	ID              uuid.UUID
	Acceptor        uuid.UUID
	Quantity        int64
	StockReserveAcc *uuid.UUID
	GuaranteeAcc    *uuid.UUID
	EscrowAcc       *uuid.UUID
}

// RunDraws cierra las ventanas vencidas (window_closes_at <= now() wall).
func (s *Service) RunDraws(ctx context.Context, simNow int64) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id FROM ledger.publications
		 WHERE status IN ('draw_window','micro_window') AND window_closes_at <= now()`)
	if err != nil {
		s.Log.Error("contracts: query ventanas", "err", err)
		return
	}
	ids, err := scanIDs(rows)
	if err != nil {
		s.Log.Error("contracts: scan ventanas", "err", err)
		return
	}
	for _, id := range ids {
		id := id
		err := db.RunSerializable(ctx, s.Pool, func(tx pgx.Tx) error {
			return s.resolveDraw(ctx, tx, id, simNow)
		})
		if err != nil {
			s.Log.Error("contracts: sorteo", "publication", id, "err", err)
		}
	}
}

func (s *Service) resolveDraw(ctx context.Context, tx pgx.Tx, pubID uuid.UUID, simNow int64) error {
	var p publication
	var status string
	var windowClosesAt *time.Time
	p.ID = pubID
	err := tx.QueryRow(ctx, `
		SELECT kind, publisher_account_id, channel, product_id, quantity_remaining, unit_price,
		       origin_node_id, destination_node_id, delivery_sim_seconds,
		       stock_reserve_account_id, guarantee_account_id, escrow_account_id,
		       status, window_closes_at
		  FROM ledger.publications WHERE id = $1 FOR UPDATE`, pubID).Scan(
		&p.Kind, &p.Publisher, &p.Channel, &p.Product, &p.QuantityRemaining, &p.UnitPrice,
		&p.OriginNode, &p.DestinationNode, &p.DeliverySimSeconds,
		&p.StockReserveAcc, &p.GuaranteeAcc, &p.EscrowAcc, &status, &windowClosesAt)
	if err != nil {
		return err
	}
	if (status != "draw_window" && status != "micro_window") ||
		windowClosesAt == nil || windowClosesAt.After(time.Now()) {
		return nil // idempotencia: ya resuelta
	}

	// Carga y baraja las aceptaciones pendientes (orden aleatorio = sorteo).
	rows, err := tx.Query(ctx, `
		SELECT id, acceptor_account_id, quantity,
		       stock_reserve_account_id, guarantee_account_id, escrow_account_id
		  FROM ledger.publication_acceptances
		 WHERE publication_id = $1 AND status = 'pending_draw' FOR UPDATE`, pubID)
	if err != nil {
		return err
	}
	var accs []acceptance
	for rows.Next() {
		var a acceptance
		if err := rows.Scan(&a.ID, &a.Acceptor, &a.Quantity,
			&a.StockReserveAcc, &a.GuaranteeAcc, &a.EscrowAcc); err != nil {
			rows.Close()
			return err
		}
		accs = append(accs, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	s.Rand.Shuffle(len(accs), func(i, j int) { accs[i], accs[j] = accs[j], accs[i] })

	remaining := p.QuantityRemaining
	for i, a := range accs {
		drawOrder := i + 1
		served := min64(remaining, a.Quantity)
		var contractID *uuid.UUID
		if served > 0 {
			cid, err := s.confirmFromAcceptance(ctx, tx, &p, &a, served, simNow)
			if err != nil {
				return err
			}
			contractID = &cid
			remaining -= served
		}
		newStatus := "served"
		if served == 0 {
			newStatus = "released"
		}
		if _, err := tx.Exec(ctx, `
			UPDATE ledger.publication_acceptances
			   SET status = $2, quantity_served = $3, draw_order = $4, resolved_at = now()
			 WHERE id = $1`, a.ID, newStatus, served, drawOrder); err != nil {
			return err
		}
		// Libera los restos de las cuentas espejo de la aceptación: todo lo
		// que confirm_contract no movió (no servida, o servida parcialmente,
		// o residuos de redondeo de la garantía).
		if err := s.releaseMirrors(ctx, tx, a.Acceptor, a.ID, simNow,
			a.StockReserveAcc, a.GuaranteeAcc, a.EscrowAcc); err != nil {
			return err
		}
		entity, err := outbox.AcceptanceEntity(ctx, tx, a.ID, contractID)
		if err != nil {
			return err
		}
		extra := map[string]any{"acceptance_id": a.ID.String(), "status": newStatus}
		if contractID != nil {
			extra["contract_id"] = contractID.String()
		}
		if err := outbox.Insert(ctx, tx, "acceptance", a.ID, "acceptance.resolved", simNow,
			outbox.Payload(entity, nil, extra)); err != nil {
			return err
		}
	}

	// Actualiza la publicación: agotada o de vuelta a 'open' sin ventana.
	newStatus := "open"
	if remaining == 0 {
		newStatus = "exhausted"
	}
	if _, err := tx.Exec(ctx, `
		UPDATE ledger.publications
		   SET quantity_remaining = $2, status = $3, window_closes_at = NULL, updated_at = now()
		 WHERE id = $1`, pubID, remaining, newStatus); err != nil {
		return err
	}
	if newStatus == "exhausted" {
		// Sin cantidad restante las cuentas espejo de la publicación deben
		// quedar a cero; se liberan los residuos de redondeo al publicador.
		if err := s.releaseMirrors(ctx, tx, p.Publisher, p.ID, simNow,
			p.StockReserveAcc, p.GuaranteeAcc, p.EscrowAcc); err != nil {
			return err
		}
	}
	entity, err := outbox.PublicationEntity(ctx, tx, pubID)
	if err != nil {
		return err
	}
	return outbox.Insert(ctx, tx, "publication", pubID, "publication.window_closed", simNow,
		outbox.Payload(entity, nil, nil))
}

// confirmFromAcceptance crea el contrato del par publicación/aceptación con
// su bloqueo triple y, si es venta con retirada in situ, lo liquida ya.
func (s *Service) confirmFromAcceptance(ctx context.Context, tx pgx.Tx, p *publication, a *acceptance, served, simNow int64) (uuid.UUID, error) {
	contractID, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, err
	}

	// Semántica v1 (punto 4 del contrato compartido):
	//  - pub 'sell' aceptada: retirada in situ → destination = origin.
	//  - pub 'buy' aceptada: origin = almacén del vendedor (el de la cuenta de
	//    stock bloqueada al aceptar); destination = el de la publicación.
	var buyer, seller, originNode, destNode uuid.UUID
	var fromStock, fromGuarantee, fromEscrow uuid.UUID
	switch p.Kind {
	case "sell":
		seller, buyer = p.Publisher, a.Acceptor
		originNode = *p.OriginNode
		destNode = originNode
		fromStock, fromGuarantee, fromEscrow = *p.StockReserveAcc, *p.GuaranteeAcc, *a.EscrowAcc
	case "buy":
		buyer, seller = p.Publisher, a.Acceptor
		destNode = *p.DestinationNode
		on, err := sellerWarehouseNode(ctx, tx, a.StockReserveAcc)
		if err != nil {
			return uuid.Nil, err
		}
		if on == nil {
			// Sin nodo de almacén resoluble: degradación a entrega in situ en
			// destino (no debería ocurrir con datos del gateway; log y sigue).
			s.Log.Warn("contracts: aceptación sin nodo de almacén, origin=destination",
				"acceptance", a.ID)
			on = &destNode
		}
		originNode = *on
		fromStock, fromGuarantee, fromEscrow = *a.StockReserveAcc, *a.GuaranteeAcc, *p.EscrowAcc
	default:
		s.Log.Warn("contracts: kind de publicación no soportado en v1", "kind", p.Kind)
		return uuid.Nil, nil
	}

	originBuilding, err := nodeBuilding(ctx, tx, originNode)
	if err != nil {
		return uuid.Nil, err
	}
	// Cuentas espejo del contrato (bloqueo triple), reference_id = contrato.
	toStock, err := ledger.NewMirrorAccount(ctx, tx, "stock_reserved", seller, &p.Product, originBuilding, contractID)
	if err != nil {
		return uuid.Nil, err
	}
	toGuarantee, err := ledger.NewMirrorAccount(ctx, tx, "guarantee", seller, nil, nil, contractID)
	if err != nil {
		return uuid.Nil, err
	}
	toEscrow, err := ledger.NewMirrorAccount(ctx, tx, "escrow", buyer, nil, nil, contractID)
	if err != nil {
		return uuid.Nil, err
	}

	deadline := simNow + p.DeliverySimSeconds
	if _, err := tx.Exec(ctx, `
		INSERT INTO ledger.contracts
		    (id, publication_id, channel, buyer_account_id, seller_account_id, product_id,
		     quantity_agreed, unit_price, origin_node_id, destination_node_id, deadline_sim,
		     status, stock_reserve_account_id, seller_guarantee_account_id, escrow_account_id,
		     confirmed_at_sim)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'active',$12,$13,$14,$15)`,
		contractID, p.ID, p.Channel, buyer, seller, p.Product,
		served, p.UnitPrice, originNode, destNode, deadline,
		toStock, toGuarantee, toEscrow, simNow); err != nil {
		return uuid.Nil, err
	}
	if err := ledger.ConfirmContract(ctx, tx, contractID, simNow, served, p.UnitPrice,
		fromStock, fromGuarantee, fromEscrow, toStock, toGuarantee, toEscrow); err != nil {
		return uuid.Nil, err
	}
	entity, err := outbox.ContractEntity(ctx, tx, contractID)
	if err != nil {
		return uuid.Nil, err
	}
	if err := outbox.Insert(ctx, tx, "contract", contractID, "contract.confirmed", simNow,
		outbox.Payload(entity, nil, nil)); err != nil {
		return uuid.Nil, err
	}

	// Venta con retirada in situ: se liquida al confirmarse — el stock pasa
	// al comprador en ese mismo almacén (fill 10000).
	if originNode == destNode && p.Kind == "sell" {
		if err := s.settleInSitu(ctx, tx, contractID, seller, buyer, p.Product, originNode, served, simNow); err != nil {
			return uuid.Nil, err
		}
	}
	return contractID, nil
}

// settleInSitu registra la "entrega" inmediata (shipment ya entregado en el
// nodo de origen) y liquida el contrato completo.
func (s *Service) settleInSitu(ctx context.Context, tx pgx.Tx, contractID, seller, buyer, product, node uuid.UUID, qty, simNow int64) error {
	var shipmentID uuid.UUID
	// El cargamento nace ya entregado: nada se teletransporta, simplemente el
	// stock nunca salió del almacén (propiedad la dicta el ledger).
	err := tx.QueryRow(ctx, `
		INSERT INTO world.shipments (owner_account_id, product_id, quantity, contract_id,
		                             at_node_id, status, updated_at_sim)
		VALUES ($1, $2, $3, $4, $5, 'delivered', $6) RETURNING id`,
		buyer, product, qty, contractID, node, simNow).Scan(&shipmentID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ledger.contract_deliveries (contract_id, shipment_id, quantity, delivered_at_sim, on_time)
		VALUES ($1, $2, $3, $4, true)`, contractID, shipmentID, qty, simNow); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE ledger.contracts SET quantity_delivered = $2, updated_at = now() WHERE id = $1`,
		contractID, qty); err != nil {
		return err
	}
	entity, err := outbox.ContractEntity(ctx, tx, contractID)
	if err != nil {
		return err
	}
	if err := outbox.Insert(ctx, tx, "contract", contractID, "delivery.confirmed", simNow,
		outbox.Payload(entity, nil, map[string]any{
			"shipment_id": shipmentID.String(),
			"quantity":    qty,
		})); err != nil {
		return err
	}
	_ = seller
	return s.SettleWithCityFlow(ctx, tx, contractID, simNow)
}

// releaseMirrors devuelve al dueño lo que quede en las cuentas espejo de una
// publicación/aceptación (asiento 'publication_release'): garantía y escrow a
// caja; stock reservado a stock_free en su mismo almacén.
func (s *Service) releaseMirrors(ctx context.Context, tx pgx.Tx, owner, refID uuid.UUID, simNow int64, mirrors ...*uuid.UUID) error {
	for _, m := range mirrors {
		if m == nil {
			continue
		}
		var (
			kind      string
			product   *uuid.UUID
			warehouse *uuid.UUID
			balance   int64
		)
		if err := tx.QueryRow(ctx, `
			SELECT kind, product_id, warehouse_building_id, balance
			  FROM ledger.accounts WHERE id = $1 FOR UPDATE`, *m).Scan(&kind, &product, &warehouse, &balance); err != nil {
			return err
		}
		if balance <= 0 {
			continue
		}
		var target uuid.UUID
		var err error
		if kind == "stock_reserved" && product != nil {
			target, err = ledger.EnsureStockFree(ctx, tx, owner, *product, warehouse)
		} else {
			target, err = ledger.CashAccount(ctx, tx, owner)
		}
		if err != nil {
			return err
		}
		if _, err := ledger.PostTx(ctx, tx, "publication_release", simNow, &refID,
			"Liberación de garantías no servidas", []ledger.Entry{
				{AccountID: *m, Amount: -balance},
				{AccountID: target, Amount: balance},
			}); err != nil {
			return err
		}
	}
	return nil
}

// sellerWarehouseNode resuelve el nodo del almacén del vendedor a partir de la
// cuenta de stock bloqueada al aceptar (elegida por el aceptante).
func sellerWarehouseNode(ctx context.Context, tx pgx.Tx, stockAcc *uuid.UUID) (*uuid.UUID, error) {
	if stockAcc == nil {
		return nil, nil
	}
	var warehouse *uuid.UUID
	if err := tx.QueryRow(ctx,
		`SELECT warehouse_building_id FROM ledger.accounts WHERE id = $1`, *stockAcc).Scan(&warehouse); err != nil {
		return nil, err
	}
	if warehouse == nil {
		return nil, nil
	}
	var node uuid.UUID
	err := tx.QueryRow(ctx,
		`SELECT id FROM world.network_nodes WHERE building_id = $1 LIMIT 1`, *warehouse).Scan(&node)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &node, nil
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
