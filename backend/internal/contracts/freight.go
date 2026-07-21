package contracts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lokiteitor/global-market/backend/internal/outbox"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// accountKindCustody es el kind de cuenta del ledger que sostiene la carga en
// custodia de un flete (enum ledger.account_kind de 0004): el transportista la
// lleva físicamente pero no puede venderla (no está en su stock_free).
const accountKindCustody = "custody"

// freightDeclaredPortion calcula la parte proporcional del valor declarado que
// corresponde a qty de un total de totalQty unidades: floor(declaredTotal*qty/
// totalQty). math/big evita el desbordamiento del producto intermedio.
func freightDeclaredPortion(declaredTotal, qty, totalQty int64) int64 {
	if totalQty <= 0 {
		return 0
	}
	n := new(big.Int).Mul(big.NewInt(declaredTotal), big.NewInt(qty))
	n.Quo(n, big.NewInt(totalQty))
	return n.Int64()
}

// freightGuarantee calcula la garantía del transportista: floor(declaredPortion
// * bp / 10000). math/big evita el desbordamiento del producto intermedio.
func freightGuarantee(declaredPortion int64, bp int) int64 {
	n := new(big.Int).Mul(big.NewInt(declaredPortion), big.NewInt(int64(bp)))
	n.Quo(n, big.NewInt(bpDenominator))
	return n.Int64()
}

// ─── Consultas ───────────────────────────────────────────────────────────────

// ListFreightContracts devuelve los contratos de flete en los que la corporación
// autenticada es cargador (role shipper) o transportista (role carrier), con los
// filtros del contrato y el cursor keyset de la siguiente página.
func (s *Service) ListFreightContracts(ctx context.Context, account uuid.UUID, f FreightContractFilter) ([]FreightContract, string, error) {
	if f.Role != "" && !f.Role.Valid() {
		return nil, "", fmt.Errorf("%w: role inválido %q", ErrValidation, f.Role)
	}
	if f.Status != "" && !f.Status.Valid() {
		return nil, "", fmt.Errorf("%w: status inválido %q", ErrValidation, f.Status)
	}
	limit := normalizeLimit(f.Limit)
	var afterID *uuid.UUID
	if f.Cursor != "" {
		id, err := decodeContractCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		afterID = &id
	}
	rows, err := s.repo.ListFreightContracts(ctx, account, f, afterID, limit+1)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > int(limit) {
		rows = rows[:limit]
		next = encodeContractCursor(rows[len(rows)-1].ID)
	}
	return rows, next, nil
}

// GetFreightContract devuelve el detalle de un flete. Solo visible para sus
// partes (cargador y transportista): ErrNotFreightParty en caso contrario.
func (s *Service) GetFreightContract(ctx context.Context, viewer, id uuid.UUID) (FreightContract, error) {
	fc, err := s.repo.GetFreightContract(ctx, id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return FreightContract{}, fmt.Errorf("%w (%s)", ErrFreightContractNotFound, id)
	case err != nil:
		return FreightContract{}, fmt.Errorf("contracts: consultando el flete %s: %w", id, err)
	}
	if !fc.IsParty(viewer) {
		return FreightContract{}, fmt.Errorf("%w (%s)", ErrNotFreightParty, id)
	}
	return fc, nil
}

// ─── Liquidación pro-rata del flete (compartida por consumer y barrido) ──────

// settleFreightAndEmit liquida un flete pro-rata con ledger.settle_freight_prorata
// y emite freight.settled con el estado final (settled|failed). Corre DENTRO de la
// transacción del llamante (tx) sobre un Repo ya ligado a ella (r), con el flete ya
// bloqueado FOR UPDATE. La comparte el freight_settler (entrega: la custodia va al
// cargador en el DESTINO, delivered = lo entregado a tiempo) y el barrido de
// vencimiento (fallo: la custodia se libera in situ en el ORIGEN, delivered = 0).
// goodsNode es el nodo donde la carga está FÍSICAMENTE al liquidar.
func (s *Service) settleFreightAndEmit(ctx context.Context, r *Repo, tx pgx.Tx, fc FreightContract, delivered int64, goodsNode uuid.UUID, simNow simtime.SimTime) (FreightContract, error) {
	custody, err := r.GetLedgerAccount(ctx, fc.CustodyAccountID)
	if err != nil {
		return FreightContract{}, fmt.Errorf("contracts: leyendo la custodia del flete %s: %w", fc.ID, err)
	}
	if custody.ProductID == nil {
		return FreightContract{}, fmt.Errorf("contracts: la custodia del flete %s no tiene producto", fc.ID)
	}
	total := custody.Balance

	goods, err := s.warehouseNode(ctx, r, goodsNode, "ubicación de la carga")
	if err != nil {
		return FreightContract{}, err
	}
	shipperGoods, err := r.EnsureStockFreeAccount(ctx, fc.ShipperAccountID, *custody.ProductID, *goods.BuildingID)
	if err != nil {
		return FreightContract{}, err
	}
	shipperCash, err := r.GetCashAccount(ctx, fc.ShipperAccountID)
	if err != nil {
		return FreightContract{}, fmt.Errorf("contracts: caja del cargador %s: %w", fc.ShipperAccountID, err)
	}
	carrierCash, err := r.GetCashAccount(ctx, fc.CarrierAccountID)
	if err != nil {
		return FreightContract{}, fmt.Errorf("contracts: caja del transportista %s: %w", fc.CarrierAccountID, err)
	}
	sink, err := r.GetSinkAccount(ctx)
	if err != nil {
		return FreightContract{}, fmt.Errorf("contracts: cuenta sink del banco central: %w", err)
	}

	txID, err := newUUIDv7()
	if err != nil {
		return FreightContract{}, err
	}
	entryIDs, err := newUUIDv7Batch(maxSettleFreightEntries)
	if err != nil {
		return FreightContract{}, err
	}
	if err := r.SettleFreightProrata(ctx, settleFreightArgs{
		TxID:           txID,
		FreightID:      fc.ID,
		SimTime:        simNow,
		Delivered:      delivered,
		ShipperCash:    shipperCash.ID,
		CarrierCash:    carrierCash.ID,
		SinkAccount:    sink.ID,
		ShipperGoods:   shipperGoods.ID,
		CompensationBP: int32(s.opts.FreightCompensationBP), //nolint:gosec // 0..10000 por Validate
		EntryIDs:       entryIDs,
	}); err != nil {
		return FreightContract{}, mapLedgerError(err)
	}

	settled, err := r.GetFreightContract(ctx, fc.ID)
	if err != nil {
		return FreightContract{}, fmt.Errorf("contracts: releyendo el flete liquidado %s: %w", fc.ID, err)
	}
	fill := 0
	if settled.FillBP != nil {
		fill = int(*settled.FillBP)
	}
	if err := outbox.Emit(ctx, tx, int64(simNow), AggregateFreightContract, settled.ID, EventFreightSettled, FreightSettledPayload{
		FreightContractID: settled.ID.String(),
		ShipperAccountID:  settled.ShipperAccountID.String(),
		CarrierAccountID:  settled.CarrierAccountID.String(),
		QuantityTotal:     fixed(total),
		QuantityDelivered: fixed(delivered),
		FreightPrice:      fixed(settled.FreightPrice),
		FillBP:            fill,
		SettledAtSim:      int64(simNow),
		Status:            string(settled.Status),
	}); err != nil {
		return FreightContract{}, err
	}
	return settled, nil
}

// logFreightSettled registra la liquidación de un flete (traza uniforme).
func (s *Service) logFreightSettled(fc FreightContract, delivered int64, reason string) {
	s.logger.Info("flete liquidado",
		slog.String("freight_contract_id", fc.ID.String()),
		slog.String("status", string(fc.Status)),
		slog.Int64("delivered", delivered),
		slog.String("reason", reason))
}
