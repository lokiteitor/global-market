package contracts

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lokiteitor/global-market/backend/internal/contracts/sqlcgen"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// Repo es la capa de acceso a datos del módulo sobre el código generado por
// sqlc: traduce entre las filas generadas y los tipos de dominio. No abre
// transacciones — el servicio decide el ámbito transaccional y deriva un Repo
// ligado a su transacción con WithTx.
type Repo struct {
	q  *sqlcgen.Queries
	db sqlcgen.DBTX
}

// NewRepo construye el repositorio sobre un pool o una transacción pgx.
func NewRepo(db sqlcgen.DBTX) *Repo {
	return &Repo{q: sqlcgen.New(db), db: db}
}

// WithTx devuelve un Repo que ejecuta sus queries dentro de tx.
func (r *Repo) WithTx(tx pgx.Tx) *Repo {
	return &Repo{q: r.q.WithTx(tx), db: tx}
}

// ─── Publicaciones ───────────────────────────────────────────────────────────

// insertPublicationParams son los parámetros de InsertPublication en términos
// del dominio (las ventanas wall-clock las calcula la BD con now()).
type insertPublicationParams struct {
	ID                    uuid.UUID
	Kind                  PublicationKind
	PublisherAccountID    uuid.UUID
	Channel               Channel
	CounterpartyAccountID *uuid.UUID
	ProductID             *uuid.UUID
	QuantityTotal         int64
	UnitPrice             int64
	MinLot                int64
	OriginNodeID          *uuid.UUID
	DestinationNodeID     *uuid.UUID
	DeliverySimSeconds    int64
	DrawWindowSeconds     int64
	CancelCooldownSeconds int64
	StockReserveAccountID *uuid.UUID
	GuaranteeAccountID    *uuid.UUID
	EscrowAccountID       *uuid.UUID
	PublishedAtSim        simtime.SimTime
}

// InsertPublication crea la publicación en estado draw_window.
func (r *Repo) InsertPublication(ctx context.Context, p insertPublicationParams) (Publication, error) {
	row, err := r.q.InsertPublication(ctx, sqlcgen.InsertPublicationParams{
		ID:                    p.ID,
		Kind:                  sqlcgen.LedgerPublicationKind(p.Kind),
		PublisherAccountID:    p.PublisherAccountID,
		Channel:               sqlcgen.LedgerContractChannel(p.Channel),
		CounterpartyAccountID: p.CounterpartyAccountID,
		ProductID:             p.ProductID,
		QuantityTotal:         p.QuantityTotal,
		UnitPrice:             p.UnitPrice,
		MinLot:                p.MinLot,
		OriginNodeID:          p.OriginNodeID,
		DestinationNodeID:     p.DestinationNodeID,
		DeliverySimSeconds:    p.DeliverySimSeconds,
		DrawWindowSeconds:     p.DrawWindowSeconds,
		CancelCooldownSeconds: p.CancelCooldownSeconds,
		StockReserveAccountID: p.StockReserveAccountID,
		GuaranteeAccountID:    p.GuaranteeAccountID,
		EscrowAccountID:       p.EscrowAccountID,
		PublishedAtSim:        int64(p.PublishedAtSim),
	})
	if err != nil {
		return Publication{}, fmt.Errorf("contracts: creando la publicación %s: %w", p.ID, err)
	}
	return toPublication(row), nil
}

// GetPublication devuelve una publicación por id; pgx.ErrNoRows si no existe.
func (r *Repo) GetPublication(ctx context.Context, id uuid.UUID) (Publication, error) {
	row, err := r.q.GetPublication(ctx, id)
	if err != nil {
		return Publication{}, err
	}
	return toPublication(row), nil
}

// GetPublicationForUpdate bloquea la fila de la publicación (SELECT FOR
// UPDATE) y la devuelve; pgx.ErrNoRows si no existe.
func (r *Repo) GetPublicationForUpdate(ctx context.Context, id uuid.UUID) (Publication, error) {
	row, err := r.q.GetPublicationForUpdate(ctx, id)
	if err != nil {
		return Publication{}, err
	}
	return toPublication(row), nil
}

// SetPublicationCancelled marca la publicación como cancelada.
func (r *Repo) SetPublicationCancelled(ctx context.Context, id uuid.UUID) (Publication, error) {
	row, err := r.q.SetPublicationCancelled(ctx, id)
	if err != nil {
		return Publication{}, fmt.Errorf("contracts: cancelando la publicación %s: %w", id, err)
	}
	return toPublication(row), nil
}

// SetPublicationMicroWindow abre la micro-ventana (now() + micro segundos).
func (r *Repo) SetPublicationMicroWindow(ctx context.Context, id uuid.UUID, microWindowSeconds int64) (Publication, error) {
	row, err := r.q.SetPublicationMicroWindow(ctx, sqlcgen.SetPublicationMicroWindowParams{
		ID:                 id,
		MicroWindowSeconds: microWindowSeconds,
	})
	if err != nil {
		return Publication{}, fmt.Errorf("contracts: abriendo la micro-ventana de %s: %w", id, err)
	}
	return toPublication(row), nil
}

// ListBoardPublications consulta el tablón con los filtros del contrato.
// afterKey/afterID es la clave keyset de la última fila de la página anterior
// (nil en la primera página).
func (r *Repo) ListBoardPublications(ctx context.Context, f BoardFilter, sort BoardSort, afterKey *int64, afterID *uuid.UUID, limit int32) ([]Publication, error) {
	rows, err := r.q.ListBoardPublications(ctx, sqlcgen.ListBoardPublicationsParams{
		Kind: sqlcgen.NullLedgerPublicationKind{
			LedgerPublicationKind: sqlcgen.LedgerPublicationKind(f.Kind),
			Valid:                 f.Kind != "",
		},
		ProductID:             f.ProductID,
		OriginRegionID:        f.OriginRegionID,
		DestinationRegionID:   f.DestinationRegionID,
		MinUnitPrice:          f.MinUnitPrice,
		MaxUnitPrice:          f.MaxUnitPrice,
		MinQuantityRemaining:  f.MinQuantityRemaining,
		MaxDeliverySimSeconds: f.MaxDeliverySimSeconds,
		Sort:                  string(sort),
		AfterKey:              afterKey,
		AfterID:               afterID,
		PageLimit:             limit,
	})
	if err != nil {
		return nil, fmt.Errorf("contracts: consultando el tablón: %w", err)
	}
	pubs := make([]Publication, len(rows))
	for i, row := range rows {
		pubs[i] = toPublication(row)
	}
	return pubs, nil
}

// CountBoardPublications cuenta las publicaciones visibles del tablón.
func (r *Repo) CountBoardPublications(ctx context.Context) (int64, error) {
	n, err := r.q.CountBoardPublications(ctx)
	if err != nil {
		return 0, fmt.Errorf("contracts: contando el tablón: %w", err)
	}
	return n, nil
}

// ─── Aceptaciones ────────────────────────────────────────────────────────────

// insertAcceptanceParams son los parámetros de InsertAcceptance.
type insertAcceptanceParams struct {
	ID                    uuid.UUID
	PublicationID         uuid.UUID
	AcceptorAccountID     uuid.UUID
	Quantity              int64
	StockReserveAccountID *uuid.UUID
	GuaranteeAccountID    *uuid.UUID
	EscrowAccountID       *uuid.UUID
}

// InsertAcceptance registra una aceptación pending_draw.
func (r *Repo) InsertAcceptance(ctx context.Context, p insertAcceptanceParams) (Acceptance, error) {
	row, err := r.q.InsertAcceptance(ctx, sqlcgen.InsertAcceptanceParams{
		ID:                    p.ID,
		PublicationID:         p.PublicationID,
		AcceptorAccountID:     p.AcceptorAccountID,
		Quantity:              p.Quantity,
		StockReserveAccountID: p.StockReserveAccountID,
		GuaranteeAccountID:    p.GuaranteeAccountID,
		EscrowAccountID:       p.EscrowAccountID,
	})
	if err != nil {
		return Acceptance{}, fmt.Errorf("contracts: registrando la aceptación %s: %w", p.ID, err)
	}
	return toAcceptance(row), nil
}

// GetAcceptance devuelve una aceptación por id; pgx.ErrNoRows si no existe.
func (r *Repo) GetAcceptance(ctx context.Context, id uuid.UUID) (Acceptance, error) {
	row, err := r.q.GetAcceptance(ctx, id)
	if err != nil {
		return Acceptance{}, err
	}
	return toAcceptance(row), nil
}

// ListPendingAcceptancesForUpdate bloquea y devuelve las aceptaciones
// pending_draw de una publicación, en orden de llegada.
func (r *Repo) ListPendingAcceptancesForUpdate(ctx context.Context, publicationID uuid.UUID) ([]Acceptance, error) {
	rows, err := r.q.ListPendingAcceptancesForUpdate(ctx, publicationID)
	if err != nil {
		return nil, fmt.Errorf("contracts: bloqueando las aceptaciones pendientes de %s: %w", publicationID, err)
	}
	accs := make([]Acceptance, len(rows))
	for i, row := range rows {
		accs[i] = toAcceptance(row)
	}
	return accs, nil
}

// ReleaseAcceptance resuelve una aceptación como no servida con el draw_order
// que exige la BD al salir de pending_draw.
func (r *Repo) ReleaseAcceptance(ctx context.Context, id uuid.UUID, drawOrder int32) (Acceptance, error) {
	row, err := r.q.ReleaseAcceptance(ctx, sqlcgen.ReleaseAcceptanceParams{ID: id, DrawOrder: &drawOrder})
	if err != nil {
		return Acceptance{}, fmt.Errorf("contracts: liberando la aceptación %s: %w", id, err)
	}
	return toAcceptance(row), nil
}

// ─── Cuentas del ledger ──────────────────────────────────────────────────────

// ledgerAccount es la vista mínima de una cuenta del ledger que este módulo
// necesita (saldo y coordenadas del activo).
type ledgerAccount struct {
	ID                  uuid.UUID
	OwnerAccountID      *uuid.UUID
	ProductID           *uuid.UUID
	WarehouseBuildingID *uuid.UUID
	Balance             int64
}

// CreateMirrorAccount crea una cuenta espejo del ledger (reference =
// publicación o aceptación que la motiva).
func (r *Repo) CreateMirrorAccount(ctx context.Context, kind string, owner uuid.UUID, product, warehouse *uuid.UUID, reference uuid.UUID) (ledgerAccount, error) {
	id, err := newUUIDv7()
	if err != nil {
		return ledgerAccount{}, err
	}
	row, err := r.q.CreateLedgerAccount(ctx, sqlcgen.CreateLedgerAccountParams{
		ID:                  id,
		Kind:                sqlcgen.LedgerAccountKind(kind),
		OwnerAccountID:      &owner,
		ProductID:           product,
		WarehouseBuildingID: warehouse,
		ReferenceID:         &reference,
	})
	if err != nil {
		return ledgerAccount{}, fmt.Errorf("contracts: creando la cuenta espejo %s de %s: %w", kind, reference, err)
	}
	return toLedgerAccount(row), nil
}

// GetLedgerAccount lee una cuenta del ledger; pgx.ErrNoRows si no existe.
func (r *Repo) GetLedgerAccount(ctx context.Context, id uuid.UUID) (ledgerAccount, error) {
	row, err := r.q.GetLedgerAccount(ctx, id)
	if err != nil {
		return ledgerAccount{}, err
	}
	return toLedgerAccount(row), nil
}

// GetCashAccount devuelve la caja de una corporación; pgx.ErrNoRows si no
// existe.
func (r *Repo) GetCashAccount(ctx context.Context, owner uuid.UUID) (ledgerAccount, error) {
	row, err := r.q.GetCashAccount(ctx, &owner)
	if err != nil {
		return ledgerAccount{}, err
	}
	return toLedgerAccount(row), nil
}

// GetStockFreeAccount devuelve la cuenta de stock libre de (dueño, producto,
// almacén); pgx.ErrNoRows si no existe.
func (r *Repo) GetStockFreeAccount(ctx context.Context, owner, product, warehouse uuid.UUID) (ledgerAccount, error) {
	row, err := r.q.GetStockFreeAccount(ctx, sqlcgen.GetStockFreeAccountParams{
		OwnerAccountID:      &owner,
		ProductID:           &product,
		WarehouseBuildingID: &warehouse,
	})
	if err != nil {
		return ledgerAccount{}, err
	}
	return toLedgerAccount(row), nil
}

// entryAmount es una partida de un asiento del ledger (importe con signo,
// nunca cero).
type entryAmount struct {
	AccountID uuid.UUID
	Amount    int64
}

// PostLedgerTransaction asienta cabecera + partidas dentro de la transacción
// SQL del Repo. Los triggers de 0004 aplican los saldos y garantizan la
// no-negatividad (inmediata) y la doble entrada por activo (diferida, en el
// COMMIT). Los IDs (UUIDv7) los genera la aplicación (ADR-018).
func (r *Repo) PostLedgerTransaction(ctx context.Context, kind string, simAt simtime.SimTime, reference uuid.UUID, description string, entries []entryAmount) error {
	txID, err := newUUIDv7()
	if err != nil {
		return err
	}
	var desc *string
	if description != "" {
		desc = &description
	}
	if err := r.q.InsertLedgerTransaction(ctx, sqlcgen.InsertLedgerTransactionParams{
		ID:          txID,
		Kind:        sqlcgen.LedgerTransactionKind(kind),
		SimTimeAt:   int64(simAt),
		ReferenceID: &reference,
		Description: desc,
	}); err != nil {
		return fmt.Errorf("contracts: asentando la cabecera %s de %s: %w", kind, reference, err)
	}
	for _, e := range entries {
		entryID, err := newUUIDv7()
		if err != nil {
			return err
		}
		if err := r.q.InsertLedgerEntry(ctx, sqlcgen.InsertLedgerEntryParams{
			ID:            entryID,
			TransactionID: txID,
			AccountID:     e.AccountID,
			Amount:        e.Amount,
		}); err != nil {
			return fmt.Errorf("contracts: asentando la partida de %s (cuenta %s): %w", reference, e.AccountID, err)
		}
	}
	return nil
}

// ─── Validaciones de mundo ───────────────────────────────────────────────────

// nodeBuilding es un nodo del grafo logístico con el edificio que lo respalda.
type nodeBuilding struct {
	ID            uuid.UUID
	RegionID      uuid.UUID
	BuildingID    *uuid.UUID
	BuildingOwner *uuid.UUID
}

// ProductExists comprueba la existencia de un producto del catálogo.
func (r *Repo) ProductExists(ctx context.Context, id uuid.UUID) (bool, error) {
	ok, err := r.q.ProductExists(ctx, id)
	if err != nil {
		return false, fmt.Errorf("contracts: consultando el producto %s: %w", id, err)
	}
	return ok, nil
}

// GetNodeBuilding devuelve un nodo con su edificio; pgx.ErrNoRows si el nodo
// no existe.
func (r *Repo) GetNodeBuilding(ctx context.Context, id uuid.UUID) (nodeBuilding, error) {
	row, err := r.q.GetNodeBuilding(ctx, id)
	if err != nil {
		return nodeBuilding{}, err
	}
	return nodeBuilding{
		ID:            row.ID,
		RegionID:      row.RegionID,
		BuildingID:    row.BuildingID,
		BuildingOwner: row.BuildingOwnerAccountID,
	}, nil
}

// DBNow devuelve el now() de la BD (las ventanas wall-clock se comparan
// siempre contra el reloj de la base).
func (r *Repo) DBNow(ctx context.Context) (time.Time, error) {
	t, err := r.q.DBNow(ctx)
	if err != nil {
		return time.Time{}, fmt.Errorf("contracts: leyendo now() de la BD: %w", err)
	}
	return t, nil
}

// ─── Barridos (parte B): sorteo, expiración, liquidación ─────────────────────

// ListDueDrawPublicationIDs lista las publicaciones con la ventana de sorteo o
// micro-ventana ya vencida (window_closes_at <= now() de la BD).
func (r *Repo) ListDueDrawPublicationIDs(ctx context.Context, limit int32) ([]uuid.UUID, error) {
	ids, err := r.q.ListDueDrawPublicationIDs(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("contracts: listando publicaciones con ventana vencida: %w", err)
	}
	return ids, nil
}

// LockDueDrawPublication bloquea la publicación si su ventana sigue vencida y
// no la tomó otra instancia (FOR UPDATE SKIP LOCKED). pgx.ErrNoRows si ya se
// resolvió o está bloqueada por otro worker.
func (r *Repo) LockDueDrawPublication(ctx context.Context, id uuid.UUID) (Publication, error) {
	row, err := r.q.LockDueDrawPublication(ctx, id)
	if err != nil {
		return Publication{}, err
	}
	return toPublication(row), nil
}

// ServeAcceptance resuelve una aceptación como servida con la cantidad servida
// y el draw_order sorteado.
func (r *Repo) ServeAcceptance(ctx context.Context, id uuid.UUID, servedQty int64, drawOrder int32) (Acceptance, error) {
	row, err := r.q.ServeAcceptance(ctx, sqlcgen.ServeAcceptanceParams{
		ID:             id,
		QuantityServed: servedQty,
		DrawOrder:      &drawOrder,
	})
	if err != nil {
		return Acceptance{}, fmt.Errorf("contracts: sirviendo la aceptación %s: %w", id, err)
	}
	return toAcceptance(row), nil
}

// SetPublicationDrawResult fija la cantidad restante y el estado resultante del
// sorteo (exhausted u open, con la ventana ya cerrada).
func (r *Repo) SetPublicationDrawResult(ctx context.Context, id uuid.UUID, remaining int64, status PublicationStatus) (Publication, error) {
	row, err := r.q.SetPublicationDrawResult(ctx, sqlcgen.SetPublicationDrawResultParams{
		ID:                id,
		QuantityRemaining: remaining,
		Status:            sqlcgen.LedgerPublicationStatus(status),
	})
	if err != nil {
		return Publication{}, fmt.Errorf("contracts: fijando el resultado del sorteo de %s: %w", id, err)
	}
	return toPublication(row), nil
}

// ListExpiredPublicationIDs lista las publicaciones abiertas cuyo TTL de
// sim-time venció.
func (r *Repo) ListExpiredPublicationIDs(ctx context.Context, ttlSimSeconds int64, simNow simtime.SimTime, limit int32) ([]uuid.UUID, error) {
	ids, err := r.q.ListExpiredPublicationIDs(ctx, sqlcgen.ListExpiredPublicationIDsParams{
		TtlSimSeconds: ttlSimSeconds,
		SimNow:        int64(simNow),
		PageLimit:     limit,
	})
	if err != nil {
		return nil, fmt.Errorf("contracts: listando publicaciones expiradas: %w", err)
	}
	return ids, nil
}

// LockExpiredPublication bloquea la publicación si sigue abierta y vencida por
// TTL. pgx.ErrNoRows si otra instancia la tomó o ya cambió de estado.
func (r *Repo) LockExpiredPublication(ctx context.Context, id uuid.UUID, ttlSimSeconds int64, simNow simtime.SimTime) (Publication, error) {
	row, err := r.q.LockExpiredPublication(ctx, sqlcgen.LockExpiredPublicationParams{
		ID:            id,
		TtlSimSeconds: ttlSimSeconds,
		SimNow:        int64(simNow),
	})
	if err != nil {
		return Publication{}, err
	}
	return toPublication(row), nil
}

// SetPublicationExpired marca la publicación como expirada.
func (r *Repo) SetPublicationExpired(ctx context.Context, id uuid.UUID) (Publication, error) {
	row, err := r.q.SetPublicationExpired(ctx, id)
	if err != nil {
		return Publication{}, fmt.Errorf("contracts: expirando la publicación %s: %w", id, err)
	}
	return toPublication(row), nil
}

// ListDueContractIDs lista los contratos activos cuyo plazo de entrega venció.
func (r *Repo) ListDueContractIDs(ctx context.Context, simNow simtime.SimTime, limit int32) ([]uuid.UUID, error) {
	ids, err := r.q.ListDueContractIDs(ctx, sqlcgen.ListDueContractIDsParams{
		SimNow:    int64(simNow),
		PageLimit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("contracts: listando contratos vencidos: %w", err)
	}
	return ids, nil
}

// LockDueContract bloquea el contrato si sigue activo y vencido. pgx.ErrNoRows
// si otra instancia lo tomó o ya se liquidó.
func (r *Repo) LockDueContract(ctx context.Context, id uuid.UUID, simNow simtime.SimTime) (Contract, error) {
	row, err := r.q.LockDueContract(ctx, sqlcgen.LockDueContractParams{ID: id, SimNow: int64(simNow)})
	if err != nil {
		return Contract{}, err
	}
	return toContract(row), nil
}

// ─── Contratos y entregas ────────────────────────────────────────────────────

// insertContractParams son los parámetros de InsertContract en términos del
// dominio.
type insertContractParams struct {
	ID                       uuid.UUID
	PublicationID            *uuid.UUID
	Channel                  Channel
	BuyerAccountID           uuid.UUID
	SellerAccountID          uuid.UUID
	ProductID                uuid.UUID
	QuantityAgreed           int64
	UnitPrice                int64
	OriginNodeID             uuid.UUID
	DestinationNodeID        uuid.UUID
	DeadlineSim              simtime.SimTime
	StockReserveAccountID    uuid.UUID
	SellerGuaranteeAccountID uuid.UUID
	EscrowAccountID          uuid.UUID
	ConfirmedAtSim           simtime.SimTime
}

// InsertContract crea el contrato en estado active con sus cuentas espejo.
func (r *Repo) InsertContract(ctx context.Context, p insertContractParams) (Contract, error) {
	row, err := r.q.InsertContract(ctx, sqlcgen.InsertContractParams{
		ID:                       p.ID,
		PublicationID:            p.PublicationID,
		Channel:                  sqlcgen.LedgerContractChannel(p.Channel),
		BuyerAccountID:           p.BuyerAccountID,
		SellerAccountID:          p.SellerAccountID,
		ProductID:                p.ProductID,
		QuantityAgreed:           p.QuantityAgreed,
		UnitPrice:                p.UnitPrice,
		OriginNodeID:             p.OriginNodeID,
		DestinationNodeID:        p.DestinationNodeID,
		DeadlineSim:              int64(p.DeadlineSim),
		StockReserveAccountID:    p.StockReserveAccountID,
		SellerGuaranteeAccountID: p.SellerGuaranteeAccountID,
		EscrowAccountID:          p.EscrowAccountID,
		ConfirmedAtSim:           int64(p.ConfirmedAtSim),
	})
	if err != nil {
		return Contract{}, fmt.Errorf("contracts: creando el contrato %s: %w", p.ID, err)
	}
	return toContract(row), nil
}

// SetContractQuantityDelivered acumula la cantidad entregada a tiempo.
func (r *Repo) SetContractQuantityDelivered(ctx context.Context, id uuid.UUID, delivered int64) (Contract, error) {
	row, err := r.q.SetContractQuantityDelivered(ctx, sqlcgen.SetContractQuantityDeliveredParams{
		ID:                id,
		QuantityDelivered: delivered,
	})
	if err != nil {
		return Contract{}, fmt.Errorf("contracts: acumulando la entrega del contrato %s: %w", id, err)
	}
	return toContract(row), nil
}

// GetContract devuelve un contrato por id; pgx.ErrNoRows si no existe.
func (r *Repo) GetContract(ctx context.Context, id uuid.UUID) (Contract, error) {
	row, err := r.q.GetContract(ctx, id)
	if err != nil {
		return Contract{}, err
	}
	return toContract(row), nil
}

// GetContractForAcceptance resuelve el contrato de una aceptación servida por
// (publicación, aceptante como comprador o vendedor); pgx.ErrNoRows si aún no
// hay contrato (no servida) o no existe.
func (r *Repo) GetContractForAcceptance(ctx context.Context, publicationID, acceptor uuid.UUID) (Contract, error) {
	row, err := r.q.GetContractForAcceptance(ctx, sqlcgen.GetContractForAcceptanceParams{
		PublicationID:     &publicationID,
		AcceptorAccountID: acceptor,
	})
	if err != nil {
		return Contract{}, err
	}
	return toContract(row), nil
}

// ListContracts lista los contratos de account con los filtros del contrato y
// la paginación keyset (id DESC). afterID es el id de la última fila de la
// página anterior (nil en la primera).
func (r *Repo) ListContracts(ctx context.Context, account uuid.UUID, f ContractFilter, afterID *uuid.UUID, limit int32) ([]Contract, error) {
	var role, status *string
	if f.Role != "" {
		s := string(f.Role)
		role = &s
	}
	if f.Status != "" {
		s := string(f.Status)
		status = &s
	}
	rows, err := r.q.ListContracts(ctx, sqlcgen.ListContractsParams{
		AccountID: account,
		Role:      role,
		Status:    status,
		ProductID: f.ProductID,
		AfterID:   afterID,
		PageLimit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("contracts: listando contratos de %s: %w", account, err)
	}
	contracts := make([]Contract, len(rows))
	for i, row := range rows {
		contracts[i] = toContract(row)
	}
	return contracts, nil
}

// InsertContractDelivery registra una entrega confirmada de un contrato.
func (r *Repo) InsertContractDelivery(ctx context.Context, id, contractID, shipmentID uuid.UUID, qty int64, deliveredAtSim simtime.SimTime, onTime bool) (ContractDelivery, error) {
	row, err := r.q.InsertContractDelivery(ctx, sqlcgen.InsertContractDeliveryParams{
		ID:             id,
		ContractID:     contractID,
		ShipmentID:     shipmentID,
		Quantity:       qty,
		DeliveredAtSim: int64(deliveredAtSim),
		OnTime:         onTime,
	})
	if err != nil {
		return ContractDelivery{}, fmt.Errorf("contracts: registrando la entrega del contrato %s: %w", contractID, err)
	}
	return toContractDelivery(row), nil
}

// ListContractDeliveries devuelve las entregas confirmadas de un contrato.
func (r *Repo) ListContractDeliveries(ctx context.Context, contractID uuid.UUID) ([]ContractDelivery, error) {
	rows, err := r.q.ListContractDeliveries(ctx, contractID)
	if err != nil {
		return nil, fmt.Errorf("contracts: listando las entregas del contrato %s: %w", contractID, err)
	}
	deliveries := make([]ContractDelivery, len(rows))
	for i, row := range rows {
		deliveries[i] = toContractDelivery(row)
	}
	return deliveries, nil
}

// InsertShipmentReleasedInSitu crea el cargamento de una retirada in situ
// (origen = destino): nace 'released_in_situ' en el nodo de destino, propiedad
// del comprador — la mercancía no se movió, cambió de dueño en el sitio.
func (r *Repo) InsertShipmentReleasedInSitu(ctx context.Context, owner, product uuid.UUID, qty int64, contractID, nodeID uuid.UUID, simNow simtime.SimTime) (uuid.UUID, error) {
	id, err := newUUIDv7()
	if err != nil {
		return uuid.Nil, err
	}
	sid, err := r.q.InsertShipment(ctx, sqlcgen.InsertShipmentParams{
		ID:             id,
		OwnerAccountID: owner,
		ProductID:      product,
		Quantity:       qty,
		ContractID:     &contractID,
		AtNodeID:       &nodeID,
		Status:         sqlcgen.WorldShipmentStatusReleasedInSitu,
		UpdatedAtSim:   int64(simNow),
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("contracts: creando el cargamento in situ del contrato %s: %w", contractID, err)
	}
	return sid, nil
}

// ─── Liquidación: funciones SQL todo-o-nada y cuentas de sistema ─────────────

// SQL de invocación de las funciones todo-o-nada del ledger (0004). Se llaman
// por Exec directo (no sqlc) para pasar el array de UUID pre-generados
// (google/uuid.UUID) tal cual, sin envoltorios de tipo.
const (
	sqlConfirmContract       = `SELECT ledger.confirm_contract($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`
	sqlSettleContractProrata = `SELECT ledger.settle_contract_prorata($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`
)

// confirmContractArgs agrupa las cuentas del bloqueo triple para
// ConfirmContract (origen según kind; destino = cuentas espejo del contrato).
type confirmContractArgs struct {
	TxID               uuid.UUID
	ContractID         uuid.UUID
	SimTime            simtime.SimTime
	Quantity           int64
	UnitPrice          int64
	FromStockAccount   uuid.UUID
	FromGuaranteeAcc   uuid.UUID
	FromEscrowAccount  uuid.UUID
	ToStockAccount     uuid.UUID
	ToGuaranteeAccount uuid.UUID
	ToEscrowAccount    uuid.UUID
	EntryIDs           []uuid.UUID // 6 IDs de partida pre-generados
}

// ConfirmContract invoca ledger.confirm_contract (bloqueo triple atómico).
func (r *Repo) ConfirmContract(ctx context.Context, a confirmContractArgs) error {
	if _, err := r.db.Exec(ctx, sqlConfirmContract,
		a.TxID, a.ContractID, int64(a.SimTime), a.Quantity, a.UnitPrice,
		a.FromStockAccount, a.FromGuaranteeAcc, a.FromEscrowAccount,
		a.ToStockAccount, a.ToGuaranteeAccount, a.ToEscrowAccount, a.EntryIDs,
	); err != nil {
		return fmt.Errorf("contracts: confirmando el contrato %s: %w", a.ContractID, err)
	}
	return nil
}

// settleContractArgs agrupa las cuentas de la liquidación pro-rata.
type settleContractArgs struct {
	TxID               uuid.UUID
	ContractID         uuid.UUID
	SimTime            simtime.SimTime
	SellerCash         uuid.UUID
	BuyerCash          uuid.UUID
	BuyerStock         uuid.UUID
	SinkAccount        uuid.UUID
	SellerStockRelease uuid.UUID
	CompensationBP     int32
	EntryIDs           []uuid.UUID // hasta 13 IDs de partida pre-generados
}

// SettleContractProrata invoca ledger.settle_contract_prorata (liquidación
// pro-rata: reparte escrow, garantía y stock según lo entregado a tiempo).
func (r *Repo) SettleContractProrata(ctx context.Context, a settleContractArgs) error {
	if _, err := r.db.Exec(ctx, sqlSettleContractProrata,
		a.TxID, a.ContractID, int64(a.SimTime),
		a.SellerCash, a.BuyerCash, a.BuyerStock, a.SinkAccount, a.SellerStockRelease,
		a.CompensationBP, a.EntryIDs,
	); err != nil {
		return fmt.Errorf("contracts: liquidando el contrato %s: %w", a.ContractID, err)
	}
	return nil
}

// GetSinkAccount devuelve la cuenta sink del banco central; pgx.ErrNoRows si el
// seed no la creó.
func (r *Repo) GetSinkAccount(ctx context.Context) (ledgerAccount, error) {
	row, err := r.q.GetSinkAccount(ctx)
	if err != nil {
		return ledgerAccount{}, err
	}
	return toLedgerAccount(row), nil
}

// EnsureStockFreeAccount localiza (o crea, on-demand) la cuenta stock_free de
// (dueño, producto, almacén): destino del stock entregado al comprador y de la
// liberación in situ del stock del vendedor en la liquidación.
func (r *Repo) EnsureStockFreeAccount(ctx context.Context, owner, product, warehouse uuid.UUID) (ledgerAccount, error) {
	acc, err := r.GetStockFreeAccount(ctx, owner, product, warehouse)
	switch {
	case err == nil:
		return acc, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return ledgerAccount{}, fmt.Errorf("contracts: consultando el stock_free de %s: %w", owner, err)
	}
	id, err := newUUIDv7()
	if err != nil {
		return ledgerAccount{}, err
	}
	row, err := r.q.CreateLedgerAccount(ctx, sqlcgen.CreateLedgerAccountParams{
		ID:                  id,
		Kind:                sqlcgen.LedgerAccountKindStockFree,
		OwnerAccountID:      &owner,
		ProductID:           &product,
		WarehouseBuildingID: &warehouse,
	})
	if err != nil {
		return ledgerAccount{}, fmt.Errorf("contracts: creando el stock_free de %s: %w", owner, err)
	}
	return toLedgerAccount(row), nil
}

// GetNodeByBuilding reconstruye el nodo del grafo respaldado por un edificio
// (el nodo de origen que aportó el aceptante-vendedor de una compra, no
// persistido como nodo sino vía el almacén de su cuenta espejo de stock).
func (r *Repo) GetNodeByBuilding(ctx context.Context, buildingID uuid.UUID) (uuid.UUID, uuid.UUID, error) {
	row, err := r.q.GetNodeByBuilding(ctx, &buildingID)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	return row.ID, row.RegionID, nil
}

// ─── Conversión de filas generadas a tipos de dominio ────────────────────────

func toPublication(row sqlcgen.LedgerPublication) Publication {
	return Publication{
		ID:                    row.ID,
		Kind:                  PublicationKind(row.Kind),
		PublisherAccountID:    row.PublisherAccountID,
		Channel:               Channel(row.Channel),
		CounterpartyAccountID: row.CounterpartyAccountID,
		ProductID:             row.ProductID,
		QuantityTotal:         row.QuantityTotal,
		QuantityRemaining:     row.QuantityRemaining,
		UnitPrice:             row.UnitPrice,
		MinLot:                row.MinLot,
		OriginNodeID:          row.OriginNodeID,
		DestinationNodeID:     row.DestinationNodeID,
		DeliverySimSeconds:    simtime.SimTime(row.DeliverySimSeconds),
		Status:                PublicationStatus(row.Status),
		WindowClosesAt:        row.WindowClosesAt,
		CancelCooldownUntil:   row.CancelCooldownUntil,
		StockReserveAccountID: row.StockReserveAccountID,
		GuaranteeAccountID:    row.GuaranteeAccountID,
		EscrowAccountID:       row.EscrowAccountID,
		PublishedAtSim:        simtime.SimTime(row.PublishedAtSim),
		CreatedAt:             row.CreatedAt,
		UpdatedAt:             row.UpdatedAt,
	}
}

func toAcceptance(row sqlcgen.LedgerPublicationAcceptance) Acceptance {
	return Acceptance{
		ID:                    row.ID,
		PublicationID:         row.PublicationID,
		AcceptorAccountID:     row.AcceptorAccountID,
		Quantity:              row.Quantity,
		QuantityServed:        row.QuantityServed,
		Status:                AcceptanceStatus(row.Status),
		DrawOrder:             row.DrawOrder,
		StockReserveAccountID: row.StockReserveAccountID,
		GuaranteeAccountID:    row.GuaranteeAccountID,
		EscrowAccountID:       row.EscrowAccountID,
		AcceptedAt:            row.AcceptedAt,
		ResolvedAt:            row.ResolvedAt,
	}
}

func toContract(row sqlcgen.LedgerContract) Contract {
	var settled *simtime.SimTime
	if row.SettledAtSim != nil {
		s := simtime.SimTime(*row.SettledAtSim)
		settled = &s
	}
	return Contract{
		ID:                       row.ID,
		PublicationID:            row.PublicationID,
		Channel:                  Channel(row.Channel),
		BuyerAccountID:           row.BuyerAccountID,
		SellerAccountID:          row.SellerAccountID,
		ProductID:                row.ProductID,
		QuantityAgreed:           row.QuantityAgreed,
		QuantityDelivered:        row.QuantityDelivered,
		UnitPrice:                row.UnitPrice,
		OriginNodeID:             row.OriginNodeID,
		DestinationNodeID:        row.DestinationNodeID,
		DeadlineSim:              simtime.SimTime(row.DeadlineSim),
		Status:                   ContractStatus(row.Status),
		FillBP:                   row.FillBp,
		StockReserveAccountID:    row.StockReserveAccountID,
		SellerGuaranteeAccountID: row.SellerGuaranteeAccountID,
		EscrowAccountID:          row.EscrowAccountID,
		ConfirmedAtSim:           simtime.SimTime(row.ConfirmedAtSim),
		SettledAtSim:             settled,
		CreatedAt:                row.CreatedAt,
	}
}

func toContractDelivery(row sqlcgen.LedgerContractDelivery) ContractDelivery {
	return ContractDelivery{
		ID:             row.ID,
		ContractID:     row.ContractID,
		ShipmentID:     row.ShipmentID,
		Quantity:       row.Quantity,
		DeliveredAtSim: simtime.SimTime(row.DeliveredAtSim),
		OnTime:         row.OnTime,
	}
}

func toLedgerAccount(row sqlcgen.LedgerAccount) ledgerAccount {
	return ledgerAccount{
		ID:                  row.ID,
		OwnerAccountID:      row.OwnerAccountID,
		ProductID:           row.ProductID,
		WarehouseBuildingID: row.WarehouseBuildingID,
		Balance:             row.Balance,
	}
}

// newUUIDv7 genera un UUIDv7 (ADR-018: los IDs los produce la aplicación).
func newUUIDv7() (uuid.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("contracts: generando UUIDv7: %w", err)
	}
	return id, nil
}
