package power

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
	"github.com/lokiteitor/global-market/backend/internal/world/sqlcgen"
)

// Repo es la capa de acceso a datos del subpaquete sobre el código generado por
// sqlc (paquete compartido del contexto world). No abre transacciones — el
// servicio decide el aislamiento con db.RunSerializable y WithTx.
type Repo struct {
	q *sqlcgen.Queries
}

// NewRepo construye el repositorio sobre un pool o una transacción.
func NewRepo(db sqlcgen.DBTX) *Repo {
	return &Repo{q: sqlcgen.New(db)}
}

// WithTx devuelve una vista del repositorio ligada a la transacción.
func (r *Repo) WithTx(tx pgx.Tx) *Repo {
	return &Repo{q: r.q.WithTx(tx)}
}

// ─── Líneas ──────────────────────────────────────────────────────────────────

// RegionContainingLine resuelve la región cuyos bounds contienen el trazado
// íntegro; pgx.ErrNoRows si ninguna (cruza regiones o cae fuera del mundo).
func (r *Repo) RegionContainingLine(ctx context.Context, pathGeoJSON string) (uuid.UUID, string, error) {
	row, err := r.q.GetRegionContainingLine(ctx, pathGeoJSON)
	if err != nil {
		return uuid.Nil, "", err
	}
	return row.ID, row.Name, nil
}

// LineLengthM mide el trazado en metros de mundo.
func (r *Repo) LineLengthM(ctx context.Context, pathGeoJSON string) (int32, error) {
	n, err := r.q.LineLengthM(ctx, pathGeoJSON)
	if err != nil {
		return 0, fmt.Errorf("world/power: midiendo el trazado: %w", err)
	}
	return n, nil
}

// InsertLine da de alta la línea operativa con condición 100.
func (r *Repo) InsertLine(ctx context.Context, id, owner, region uuid.UUID, pathGeoJSON string, lengthM int32, simNow simtime.SimTime) error {
	err := r.q.InsertPowerLine(ctx, sqlcgen.InsertPowerLineParams{
		ID:             id,
		OwnerAccountID: owner,
		RegionID:       region,
		Path:           pathGeoJSON,
		LengthM:        lengthM,
		SimNow:         int64(simNow),
	})
	if err != nil {
		return fmt.Errorf("world/power: insertando la línea %s: %w", id, err)
	}
	return nil
}

// GetLine devuelve una línea; pgx.ErrNoRows si no existe.
func (r *Repo) GetLine(ctx context.Context, id uuid.UUID) (PowerLine, error) {
	row, err := r.q.GetPowerLine(ctx, id)
	if err != nil {
		return PowerLine{}, err
	}
	return PowerLine{
		ID:                      row.ID,
		OwnerAccountID:          row.OwnerAccountID,
		RegionID:                row.RegionID,
		PathGeoJSON:             row.PathGeojson,
		LengthM:                 row.LengthM,
		Status:                  string(row.Status),
		ConditionPct:            row.ConditionPct,
		MaintenancePaidUntilSim: row.MaintenancePaidUntilSim,
		UpdatedAtSim:            row.UpdatedAtSim,
	}, nil
}

// ListLines pagina el catálogo de líneas (keyset por id).
func (r *Repo) ListLines(ctx context.Context, regionID, afterID *uuid.UUID, limit int32) ([]PowerLine, error) {
	rows, err := r.q.ListPowerLines(ctx, sqlcgen.ListPowerLinesParams{
		RegionID:  regionID,
		AfterID:   afterID,
		PageLimit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("world/power: listando líneas: %w", err)
	}
	out := make([]PowerLine, 0, len(rows))
	for _, row := range rows {
		out = append(out, PowerLine{
			ID:                      row.ID,
			OwnerAccountID:          row.OwnerAccountID,
			RegionID:                row.RegionID,
			PathGeoJSON:             row.PathGeojson,
			LengthM:                 row.LengthM,
			Status:                  string(row.Status),
			ConditionPct:            row.ConditionPct,
			MaintenancePaidUntilSim: row.MaintenancePaidUntilSim,
			UpdatedAtSim:            row.UpdatedAtSim,
		})
	}
	return out, nil
}

// ─── Ofertas y pujas ─────────────────────────────────────────────────────────

// BuildingForPower devuelve dueño, estado y si el tipo es central.
func (r *Repo) BuildingForPower(ctx context.Context, id uuid.UUID) (sqlcgen.GetBuildingForPowerRow, error) {
	return r.q.GetBuildingForPower(ctx, id)
}

// UpsertOffer fija el precio de oferta de una central.
func (r *Repo) UpsertOffer(ctx context.Context, building uuid.UUID, unitPrice int64, simNow simtime.SimTime) error {
	err := r.q.UpsertPowerOffer(ctx, sqlcgen.UpsertPowerOfferParams{
		BuildingID: building, UnitPrice: unitPrice, SimNow: int64(simNow),
	})
	if err != nil {
		return fmt.Errorf("world/power: fijando la oferta de %s: %w", building, err)
	}
	return nil
}

// UpsertBid fija la puja máxima de un consumidor.
func (r *Repo) UpsertBid(ctx context.Context, building uuid.UUID, unitPrice int64, simNow simtime.SimTime) error {
	err := r.q.UpsertPowerBid(ctx, sqlcgen.UpsertPowerBidParams{
		BuildingID: building, UnitPrice: unitPrice, SimNow: int64(simNow),
	})
	if err != nil {
		return fmt.Errorf("world/power: fijando la puja de %s: %w", building, err)
	}
	return nil
}

// ─── Lecturas del contrato ───────────────────────────────────────────────────

// ListSpotTicks devuelve los últimos ticks de una región (más recientes primero).
func (r *Repo) ListSpotTicks(ctx context.Context, region uuid.UUID, beforeTickSim *int64, limit int32) ([]SpotTick, error) {
	rows, err := r.q.ListPowerSpotTicks(ctx, sqlcgen.ListPowerSpotTicksParams{
		RegionID: region, BeforeTickSim: beforeTickSim, PageLimit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("world/power: listando ticks del spot: %w", err)
	}
	out := make([]SpotTick, 0, len(rows))
	for _, row := range rows {
		out = append(out, SpotTick{
			RegionID:           row.RegionID,
			TickSim:            row.TickSim,
			IntervalSim:        row.IntervalSim,
			ClosingPrice:       row.ClosingPrice,
			DemandUnits:        row.DemandUnits,
			SuppliedUnits:      row.SuppliedUnits,
			CurtailedUnits:     row.CurtailedUnits,
			CurtailedBuildings: row.CurtailedBuildings,
		})
	}
	return out, nil
}

// ListDispatches devuelve el despacho/consumo de un edificio (más recientes primero).
func (r *Repo) ListDispatches(ctx context.Context, building uuid.UUID, beforeTickSim *int64, limit int32) ([]Dispatch, error) {
	rows, err := r.q.ListPowerDispatchesForBuilding(ctx, sqlcgen.ListPowerDispatchesForBuildingParams{
		BuildingID: building, BeforeTickSim: beforeTickSim, PageLimit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("world/power: listando despachos: %w", err)
	}
	out := make([]Dispatch, 0, len(rows))
	for _, row := range rows {
		out = append(out, Dispatch{
			RegionID:       row.RegionID,
			TickSim:        row.TickSim,
			BuildingID:     row.BuildingID,
			OwnerAccountID: row.OwnerAccountID,
			Role:           string(row.Role),
			Units:          row.Units,
			UnitPrice:      row.UnitPrice,
			Amount:         row.Amount,
		})
	}
	return out, nil
}

// ─── Ledger (mismo patrón que el resto del contexto world) ───────────────────

// ledgerAccount es una vista mínima de cuenta del ledger.
type ledgerAccount struct {
	ID      uuid.UUID
	Balance int64
}

// GetCashAccount devuelve la caja del dueño; pgx.ErrNoRows si no existe.
func (r *Repo) GetCashAccount(ctx context.Context, owner uuid.UUID) (ledgerAccount, error) {
	row, err := r.q.GetCashAccount(ctx, &owner)
	if err != nil {
		return ledgerAccount{}, err
	}
	return ledgerAccount{ID: row.ID, Balance: row.Balance}, nil
}

// GetSinkAccount devuelve la cuenta sink del banco central.
func (r *Repo) GetSinkAccount(ctx context.Context) (ledgerAccount, error) {
	row, err := r.q.GetSinkAccount(ctx)
	if err != nil {
		return ledgerAccount{}, err
	}
	return ledgerAccount{ID: row.ID, Balance: row.Balance}, nil
}

// entryAmount es una partida de un asiento del ledger (importe con signo).
type entryAmount struct {
	AccountID uuid.UUID
	Amount    int64
}

// PostLedgerTransaction asienta cabecera + partidas dentro de la transacción
// SQL del Repo (los triggers de 0004 garantizan saldo, no-negatividad y doble
// entrada). Los IDs (UUIDv7) los genera la aplicación (ADR-018).
func (r *Repo) PostLedgerTransaction(ctx context.Context, kind sqlcgen.LedgerTransactionKind, simAt simtime.SimTime, reference uuid.UUID, description string, entries []entryAmount) error {
	txID, err := newUUIDv7()
	if err != nil {
		return err
	}
	var desc *string
	if description != "" {
		desc = &description
	}
	ref := reference
	if err := r.q.InsertLedgerTransaction(ctx, sqlcgen.InsertLedgerTransactionParams{
		ID:          txID,
		Kind:        kind,
		SimTimeAt:   int64(simAt),
		ReferenceID: &ref,
		Description: desc,
	}); err != nil {
		return fmt.Errorf("world/power: asentando la cabecera %s de %s: %w", kind, reference, err)
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
			return fmt.Errorf("world/power: asentando la partida de %s (cuenta %s): %w", reference, e.AccountID, err)
		}
	}
	return nil
}

// newUUIDv7 genera un UUIDv7 (ADR-018: los IDs los produce la aplicación).
func newUUIDv7() (uuid.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("world/power: generando UUIDv7: %w", err)
	}
	return id, nil
}
