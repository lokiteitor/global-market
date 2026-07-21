package contracts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lokiteitor/global-market/backend/internal/outbox"
	"github.com/lokiteitor/global-market/backend/internal/platform/db"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// serveFreightAcceptance crea y confirma el contrato de flete de una aceptación
// servida (transportista) y ASIENTA LA CUSTODIA (stock_free del cargador →
// custody del contrato). Reutiliza la maquinaria del sorteo de bienes: la
// aceptación se resuelve como servida y su garantía sobrante se libera. Emite
// acceptance.resolved y freight.confirmed. Devuelve la cantidad efectivamente
// servida (0 si el cargador ya no tiene la carga en el origen: se libera la
// aceptación como no servida, sin crear contrato — el flete no puede cumplirse).
func (w *Worker) serveFreightAcceptance(ctx context.Context, r *Repo, tx pgx.Tx, p Publication, acc Acceptance, served int64, drawOrder int32, simNow simtime.SimTime, counts *sweepCounts) (int64, error) {
	if p.ProductID == nil || p.OriginNodeID == nil || p.DestinationNodeID == nil ||
		p.EscrowAccountID == nil || p.DeclaredValue == nil || acc.GuaranteeAccountID == nil {
		return 0, fmt.Errorf("%w: publicación de flete %s incompleta para confirmar", ErrValidation, p.ID)
	}
	product := *p.ProductID
	originNode, err := w.svc.warehouseNode(ctx, r, *p.OriginNodeID, "origen")
	if err != nil {
		return 0, err
	}
	originBuilding := *originNode.BuildingID

	// La carga debe seguir en el almacén de origen del cargador (no se congeló al
	// publicar). Si no está, el flete no puede cumplirse: se libera la aceptación.
	shipperStock, err := r.GetStockFreeAccount(ctx, p.PublisherAccountID, product, originBuilding)
	insufficient := false
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		insufficient = true
	case err != nil:
		return 0, fmt.Errorf("contracts: leyendo el stock del cargador para el flete %s: %w", p.ID, err)
	case shipperStock.Balance < served:
		insufficient = true
	}

	// Importes servidos: precio del flete (unit_price * served) y garantía del
	// transportista (bp * valor declarado proporcional a lo servido).
	value, _, err := lockAmounts(served, p.UnitPrice)
	if err != nil {
		return 0, err
	}
	declaredServed := freightDeclaredPortion(*p.DeclaredValue, served, p.QuantityTotal)
	guaranteeServed := freightGuarantee(declaredServed, w.svc.opts.FreightGuaranteeBP)
	if declaredServed <= 0 || guaranteeServed <= 0 {
		insufficient = true // no se puede crear un flete con valor/garantía nulos
	}

	if insufficient {
		w.svc.logger.Warn("flete no servido: el cargador no conserva la carga o el valor es nulo; aceptación liberada",
			slog.String("publication_id", p.ID.String()), slog.String("acceptance_id", acc.ID.String()),
			slog.String("acceptor", acc.AcceptorAccountID.String()))
		if err := w.releaseUnserved(ctx, r, tx, p, acc, drawOrder, simNow); err != nil {
			return 0, err
		}
		return 0, nil
	}

	// Cuentas espejo del freight_contract: escrow (cargador), garantía
	// (transportista) y custodia (la carga, atribuida al almacén de origen).
	freightID, err := newUUIDv7()
	if err != nil {
		return 0, err
	}
	toEscrow, err := r.CreateMirrorAccount(ctx, accountKindEscrow, p.PublisherAccountID, nil, nil, freightID)
	if err != nil {
		return 0, err
	}
	toGuarantee, err := r.CreateMirrorAccount(ctx, accountKindGuarantee, acc.AcceptorAccountID, nil, nil, freightID)
	if err != nil {
		return 0, err
	}
	toCustody, err := r.CreateMirrorAccount(ctx, accountKindCustody, p.PublisherAccountID, &product, &originBuilding, freightID)
	if err != nil {
		return 0, err
	}

	fc, err := r.InsertFreightContract(ctx, insertFreightContractParams{
		ID:                        freightID,
		PublicationID:             &p.ID,
		Channel:                   p.Channel,
		ShipperAccountID:          p.PublisherAccountID,
		CarrierAccountID:          acc.AcceptorAccountID,
		OriginNodeID:              *p.OriginNodeID,
		DestinationNodeID:         *p.DestinationNodeID,
		FreightPrice:              value,
		DeclaredValue:             declaredServed,
		DeadlineSim:               simNow + p.DeliverySimSeconds,
		EscrowAccountID:           toEscrow.ID,
		CarrierGuaranteeAccountID: toGuarantee.ID,
		CustodyAccountID:          toCustody.ID,
		ConfirmedAtSim:            simNow,
	})
	if err != nil {
		return 0, err
	}

	txID, err := newUUIDv7()
	if err != nil {
		return 0, err
	}
	entryIDs, err := newUUIDv7Batch(confirmFreightEntries)
	if err != nil {
		return 0, err
	}
	if err := r.ConfirmFreight(ctx, confirmFreightArgs{
		TxID:          txID,
		FreightID:     freightID,
		SimTime:       simNow,
		Quantity:      served,
		FreightPrice:  value,
		Guarantee:     guaranteeServed,
		FromEscrow:    *p.EscrowAccountID,
		FromGuarantee: *acc.GuaranteeAccountID,
		FromStockFree: shipperStock.ID,
		ToEscrow:      toEscrow.ID,
		ToGuarantee:   toGuarantee.ID,
		ToCustody:     toCustody.ID,
		EntryIDs:      entryIDs,
	}); err != nil {
		return 0, mapLedgerError(err)
	}

	// Resolver la aceptación servida y liberar la garantía sobrante (lo que
	// confirm_freight no movió: la parte no servida de la garantía bloqueada).
	if _, err := r.ServeAcceptance(ctx, acc.ID, served, drawOrder); err != nil {
		return 0, err
	}
	if err := w.svc.releaseAcceptanceCollateral(ctx, r, acc, simNow); err != nil {
		return 0, err
	}
	counts.freightConfirmed++

	if err := outbox.Emit(ctx, tx, int64(simNow), AggregateAcceptance, acc.ID, EventAcceptanceResolved, AcceptanceResolvedPayload{
		AcceptanceID:      acc.ID.String(),
		PublicationID:     p.ID.String(),
		AcceptorAccountID: acc.AcceptorAccountID.String(),
		Status:            string(AcceptanceServed),
		QuantityServed:    fixed(served),
		ContractID:        fc.ID.String(),
		ResolvedAtSim:     int64(simNow),
	}); err != nil {
		return 0, err
	}
	if err := outbox.Emit(ctx, tx, int64(simNow), AggregateFreightContract, fc.ID, EventFreightConfirmed, FreightConfirmedPayload{
		FreightContractID: fc.ID.String(),
		PublicationID:     p.ID.String(),
		Channel:           string(fc.Channel),
		ShipperAccountID:  fc.ShipperAccountID.String(),
		CarrierAccountID:  fc.CarrierAccountID.String(),
		ProductID:         product.String(),
		Quantity:          fixed(served),
		OriginNodeID:      fc.OriginNodeID.String(),
		DestinationNodeID: fc.DestinationNodeID.String(),
		FreightPrice:      fixed(fc.FreightPrice),
		DeclaredValue:     fixed(fc.DeclaredValue),
		DeadlineSim:       int64(fc.DeadlineSim),
		ConfirmedAtSim:    int64(fc.ConfirmedAtSim),
	}); err != nil {
		return 0, err
	}
	return served, nil
}

// ─── Barrido de fletes vencidos (fallo del transportista) ─────────────────────

// settleDueFreights localiza los fletes activos vencidos SIN entrega y liquida
// cada uno como fallo en su propia transacción (custodia liberada in situ en el
// origen, garantía repartida compensación/sink, flete reembolsado al cargador).
func (w *Worker) settleDueFreights(ctx context.Context) (int, error) {
	simNow := w.svc.sim.Now(ctx)
	ids, err := w.svc.repo.ListDueFreightIDs(ctx, simNow, int32(w.opts.BatchSize)) //nolint:gosec // acotado por Validate
	if err != nil {
		return 0, err
	}
	processed := 0
	for _, id := range ids {
		done, err := w.settleOneDueFreight(ctx, id, simNow)
		if err != nil {
			w.logger.Warn("contracts: fallo liquidando un flete vencido",
				slog.String("freight_contract_id", id.String()), slog.Any("error", err))
			continue
		}
		if done {
			processed++
		}
	}
	return processed, nil
}

// settleOneDueFreight liquida un flete vencido sin entrega en su propia
// transacción: fallo (delivered=0), custodia liberada in situ en el ORIGEN. Emite
// freight.expired_undelivered para que world detenga y libere el cargamento físico.
func (w *Worker) settleOneDueFreight(ctx context.Context, id uuid.UUID, simNow simtime.SimTime) (bool, error) {
	settled := false
	var counts *sweepCounts
	err := db.RunSerializable(ctx, w.svc.pool, func(tx pgx.Tx) error {
		settled = false
		counts = newSweepCounts()
		r := w.svc.repo.WithTx(tx)
		fc, err := r.LockDueFreight(ctx, id, simNow)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return nil // ya liquidado, entregado o tomado por otra instancia
		case err != nil:
			return err
		}
		// Fallo: la carga no llegó a destino; se libera in situ en el ORIGEN (Fase
		// 2: la ubicación física de referencia es el almacén de origen, como en el
		// CCRI de bienes), delivered=0 → garantía repartida compensación/sink.
		out, err := w.svc.settleFreightAndEmit(ctx, r, tx, fc, 0, fc.OriginNodeID, simNow)
		if err != nil {
			return err
		}
		counts.freightSettled[string(out.Status)]++
		w.svc.logFreightSettled(out, 0, "vencimiento sin entrega")

		if err := outbox.Emit(ctx, tx, int64(simNow), AggregateFreightContract, fc.ID, EventFreightExpiredUndelivered, FreightExpiredUndeliveredPayload{
			FreightContractID: fc.ID.String(),
			ExpiredAtSim:      int64(simNow),
		}); err != nil {
			return err
		}
		settled = true
		return nil
	})
	if err == nil && settled {
		w.flush(counts)
	}
	return settled, err
}
