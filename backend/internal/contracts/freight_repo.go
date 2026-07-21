package contracts

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lokiteitor/global-market/backend/internal/contracts/sqlcgen"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// ─── CCRI-Flete: acceso a datos ──────────────────────────────────────────────

// insertFreightContractParams son los parámetros de InsertFreightContract en
// términos del dominio.
type insertFreightContractParams struct {
	ID                        uuid.UUID
	PublicationID             *uuid.UUID
	Channel                   Channel
	ShipperAccountID          uuid.UUID
	CarrierAccountID          uuid.UUID
	OriginNodeID              uuid.UUID
	DestinationNodeID         uuid.UUID
	FreightPrice              int64
	DeclaredValue             int64
	DeadlineSim               simtime.SimTime
	EscrowAccountID           uuid.UUID
	CarrierGuaranteeAccountID uuid.UUID
	CustodyAccountID          uuid.UUID
	ConfirmedAtSim            simtime.SimTime
}

// InsertFreightContract crea el contrato de flete en estado active con sus
// cuentas espejo (escrow, garantía y custodia).
func (r *Repo) InsertFreightContract(ctx context.Context, p insertFreightContractParams) (FreightContract, error) {
	row, err := r.q.InsertFreightContract(ctx, sqlcgen.InsertFreightContractParams{
		ID:                        p.ID,
		PublicationID:             p.PublicationID,
		Channel:                   sqlcgen.LedgerContractChannel(p.Channel),
		ShipperAccountID:          p.ShipperAccountID,
		CarrierAccountID:          p.CarrierAccountID,
		OriginNodeID:              p.OriginNodeID,
		DestinationNodeID:         p.DestinationNodeID,
		FreightPrice:              p.FreightPrice,
		DeclaredValue:             p.DeclaredValue,
		DeadlineSim:               int64(p.DeadlineSim),
		EscrowAccountID:           p.EscrowAccountID,
		CarrierGuaranteeAccountID: p.CarrierGuaranteeAccountID,
		CustodyAccountID:          p.CustodyAccountID,
		ConfirmedAtSim:            int64(p.ConfirmedAtSim),
	})
	if err != nil {
		return FreightContract{}, fmt.Errorf("contracts: creando el contrato de flete %s: %w", p.ID, err)
	}
	return toFreightContract(row), nil
}

// GetFreightContract devuelve un contrato de flete por id; pgx.ErrNoRows si no
// existe.
func (r *Repo) GetFreightContract(ctx context.Context, id uuid.UUID) (FreightContract, error) {
	row, err := r.q.GetFreightContract(ctx, id)
	if err != nil {
		return FreightContract{}, err
	}
	return toFreightContract(row), nil
}

// GetFreightContractForUpdate bloquea el contrato de flete (SELECT FOR UPDATE);
// pgx.ErrNoRows si no existe.
func (r *Repo) GetFreightContractForUpdate(ctx context.Context, id uuid.UUID) (FreightContract, error) {
	row, err := r.q.GetFreightContractForUpdate(ctx, id)
	if err != nil {
		return FreightContract{}, err
	}
	return toFreightContract(row), nil
}

// ListFreightContracts lista los fletes de account con los filtros del contrato y
// la paginación keyset (id DESC).
func (r *Repo) ListFreightContracts(ctx context.Context, account uuid.UUID, f FreightContractFilter, afterID *uuid.UUID, limit int32) ([]FreightContract, error) {
	var role, status *string
	if f.Role != "" {
		s := string(f.Role)
		role = &s
	}
	if f.Status != "" {
		s := string(f.Status)
		status = &s
	}
	rows, err := r.q.ListFreightContracts(ctx, sqlcgen.ListFreightContractsParams{
		AccountID: account,
		Role:      role,
		Status:    status,
		AfterID:   afterID,
		PageLimit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("contracts: listando fletes de %s: %w", account, err)
	}
	out := make([]FreightContract, len(rows))
	for i, row := range rows {
		out[i] = toFreightContract(row)
	}
	return out, nil
}

// ListDueFreightIDs lista los fletes activos vencidos y SIN entrega (candidatos a
// fallar por vencimiento).
func (r *Repo) ListDueFreightIDs(ctx context.Context, simNow simtime.SimTime, limit int32) ([]uuid.UUID, error) {
	ids, err := r.q.ListDueFreightIDs(ctx, sqlcgen.ListDueFreightIDsParams{SimNow: int64(simNow), PageLimit: limit})
	if err != nil {
		return nil, fmt.Errorf("contracts: listando fletes vencidos: %w", err)
	}
	return ids, nil
}

// LockDueFreight bloquea un flete activo vencido (SKIP LOCKED); pgx.ErrNoRows si
// otra instancia lo tomó o ya se liquidó.
func (r *Repo) LockDueFreight(ctx context.Context, id uuid.UUID, simNow simtime.SimTime) (FreightContract, error) {
	row, err := r.q.LockDueFreight(ctx, sqlcgen.LockDueFreightParams{ID: id, SimNow: int64(simNow)})
	if err != nil {
		return FreightContract{}, err
	}
	return toFreightContract(row), nil
}

// InsertFreightDeliveryIfNew registra la entrega de un cargamento de flete de
// forma idempotente por (freight_contract_id, shipment_id). Devuelve
// inserted=false (sin error) si ya existía.
func (r *Repo) InsertFreightDeliveryIfNew(ctx context.Context, freightID, shipmentID uuid.UUID, qty int64, deliveredAtSim simtime.SimTime, onTime bool) (bool, error) {
	_, err := r.q.InsertFreightDeliveryIfNew(ctx, sqlcgen.InsertFreightDeliveryIfNewParams{
		FreightContractID: freightID,
		ShipmentID:        shipmentID,
		Quantity:          qty,
		DeliveredAtSim:    int64(deliveredAtSim),
		OnTime:            onTime,
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("contracts: registrando (idempotente) la entrega de flete %s: %w", freightID, err)
	}
	return true, nil
}

// ShipmentDestinationNode devuelve el nodo destino de un cargamento; pgx.ErrNoRows
// si el cargamento no existe.
func (r *Repo) ShipmentDestinationNode(ctx context.Context, shipmentID uuid.UUID) (*uuid.UUID, error) {
	node, err := r.q.ShipmentDestinationNode(ctx, shipmentID)
	if err != nil {
		return nil, err
	}
	return node, nil
}

// ─── Funciones todo-o-nada del flete (SQL de 0014, invocadas por Exec) ───────

const (
	sqlConfirmFreight       = `SELECT ledger.confirm_freight($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`
	sqlSettleFreightProrata = `SELECT ledger.settle_freight_prorata($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`
	confirmFreightEntries   = 6
	maxSettleFreightEntries = 9
)

// confirmFreightArgs agrupa las cuentas del bloqueo del flete (escrow del
// cargador, garantía del transportista y custodia).
type confirmFreightArgs struct {
	TxID          uuid.UUID
	FreightID     uuid.UUID
	SimTime       simtime.SimTime
	Quantity      int64
	FreightPrice  int64
	Guarantee     int64
	FromEscrow    uuid.UUID // escrow de la publicación (cargador)
	FromGuarantee uuid.UUID // garantía de la aceptación (transportista)
	FromStockFree uuid.UUID // stock_free del cargador en el origen
	ToEscrow      uuid.UUID
	ToGuarantee   uuid.UUID
	ToCustody     uuid.UUID
	EntryIDs      []uuid.UUID // 6 IDs de partida pre-generados
}

// ConfirmFreight invoca ledger.confirm_freight (escrow + garantía + custodia).
func (r *Repo) ConfirmFreight(ctx context.Context, a confirmFreightArgs) error {
	if _, err := r.db.Exec(ctx, sqlConfirmFreight,
		a.TxID, a.FreightID, int64(a.SimTime), a.Quantity, a.FreightPrice, a.Guarantee,
		a.FromEscrow, a.FromGuarantee, a.FromStockFree,
		a.ToEscrow, a.ToGuarantee, a.ToCustody, a.EntryIDs,
	); err != nil {
		return fmt.Errorf("contracts: confirmando el flete %s: %w", a.FreightID, err)
	}
	return nil
}

// settleFreightArgs agrupa las cuentas de la liquidación pro-rata del flete.
type settleFreightArgs struct {
	TxID           uuid.UUID
	FreightID      uuid.UUID
	SimTime        simtime.SimTime
	Delivered      int64 // unidades entregadas a tiempo (base del pago)
	ShipperCash    uuid.UUID
	CarrierCash    uuid.UUID
	SinkAccount    uuid.UUID
	ShipperGoods   uuid.UUID // stock_free del cargador donde la carga está físicamente
	CompensationBP int32
	EntryIDs       []uuid.UUID // hasta 9 IDs de partida pre-generados
}

// SettleFreightProrata invoca ledger.settle_freight_prorata (liquidación
// pro-rata del flete: custodia al cargador, flete/garantía repartidos).
func (r *Repo) SettleFreightProrata(ctx context.Context, a settleFreightArgs) error {
	if _, err := r.db.Exec(ctx, sqlSettleFreightProrata,
		a.TxID, a.FreightID, int64(a.SimTime), a.Delivered,
		a.ShipperCash, a.CarrierCash, a.SinkAccount, a.ShipperGoods,
		a.CompensationBP, a.EntryIDs,
	); err != nil {
		return fmt.Errorf("contracts: liquidando el flete %s: %w", a.FreightID, err)
	}
	return nil
}

// toFreightContract convierte la fila generada al tipo de dominio.
func toFreightContract(row sqlcgen.LedgerFreightContract) FreightContract {
	var settled *simtime.SimTime
	if row.SettledAtSim != nil {
		s := simtime.SimTime(*row.SettledAtSim)
		settled = &s
	}
	return FreightContract{
		ID:                        row.ID,
		PublicationID:             row.PublicationID,
		Channel:                   Channel(row.Channel),
		ShipperAccountID:          row.ShipperAccountID,
		CarrierAccountID:          row.CarrierAccountID,
		OriginNodeID:              row.OriginNodeID,
		DestinationNodeID:         row.DestinationNodeID,
		FreightPrice:              row.FreightPrice,
		DeclaredValue:             row.DeclaredValue,
		DeadlineSim:               simtime.SimTime(row.DeadlineSim),
		Status:                    ContractStatus(row.Status),
		FillBP:                    row.FillBp,
		EscrowAccountID:           row.EscrowAccountID,
		CarrierGuaranteeAccountID: row.CarrierGuaranteeAccountID,
		CustodyAccountID:          row.CustodyAccountID,
		ConfirmedAtSim:            simtime.SimTime(row.ConfirmedAtSim),
		SettledAtSim:              settled,
		CreatedAt:                 row.CreatedAt,
	}
}
