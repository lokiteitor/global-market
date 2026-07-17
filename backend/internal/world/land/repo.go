package land

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
	"github.com/lokiteitor/global-market/backend/internal/world/sqlcgen"
)

// Repo es la capa de acceso a datos del subpaquete sobre el código generado por
// sqlc (paquete compartido del contexto world). Traduce entre las filas
// generadas y los tipos de dominio; no abre transacciones — el servicio decide
// el ámbito transaccional y deriva un Repo ligado a su transacción con WithTx.
type Repo struct {
	q *sqlcgen.Queries
}

// NewRepo construye el repositorio sobre un pool o una transacción pgx.
func NewRepo(db sqlcgen.DBTX) *Repo {
	return &Repo{q: sqlcgen.New(db)}
}

// WithTx devuelve un Repo que ejecuta sus queries dentro de tx.
func (r *Repo) WithTx(tx pgx.Tx) *Repo {
	return &Repo{q: r.q.WithTx(tx)}
}

// ─── Concesiones ─────────────────────────────────────────────────────────────

// ListConcessions lista las concesiones de un titular con los filtros del
// contrato y la paginación keyset (id ASC). afterID es la última fila de la
// página anterior (nil en la primera).
func (r *Repo) ListConcessions(ctx context.Context, holder uuid.UUID, f ConcessionFilter, afterID *uuid.UUID, limit int32) ([]Concession, error) {
	rows, err := r.q.ListConcessions(ctx, sqlcgen.ListConcessionsParams{
		HolderAccountID: holder,
		Status: sqlcgen.NullWorldConcessionStatus{
			WorldConcessionStatus: sqlcgen.WorldConcessionStatus(f.Status),
			Valid:                 f.Status != "",
		},
		RegionID:  f.RegionID,
		AfterID:   afterID,
		PageLimit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("world/land: listando concesiones de %s: %w", holder, err)
	}
	out := make([]Concession, len(rows))
	for i, row := range rows {
		out[i] = concessionFrom(row.ID, row.RegionID, row.HolderAccountID, row.Parcel,
			row.CanonAmount, row.PeriodSimDays, row.ExpiresAtSim, row.Status, row.GrantedAtSim)
	}
	return out, nil
}

// GetConcession devuelve una concesión por id; pgx.ErrNoRows si no existe.
func (r *Repo) GetConcession(ctx context.Context, id uuid.UUID) (Concession, error) {
	row, err := r.q.GetConcession(ctx, id)
	if err != nil {
		return Concession{}, err
	}
	return concessionFrom(row.ID, row.RegionID, row.HolderAccountID, row.Parcel,
		row.CanonAmount, row.PeriodSimDays, row.ExpiresAtSim, row.Status, row.GrantedAtSim), nil
}

// GetConcessionForUpdate bloquea la fila (FOR UPDATE) y la devuelve; pgx.ErrNoRows
// si no existe.
func (r *Repo) GetConcessionForUpdate(ctx context.Context, id uuid.UUID) (Concession, error) {
	row, err := r.q.GetConcessionForUpdate(ctx, id)
	if err != nil {
		return Concession{}, err
	}
	return concessionFrom(row.ID, row.RegionID, row.HolderAccountID, row.Parcel,
		row.CanonAmount, row.PeriodSimDays, row.ExpiresAtSim, row.Status, row.GrantedAtSim), nil
}

// RegionParcelWithin comprueba región + contención de la parcela y devuelve el
// canon_base regional; pgx.ErrNoRows si la región no existe.
func (r *Repo) RegionParcelWithin(ctx context.Context, regionID uuid.UUID, parcelGeoJSON string) (within bool, canonBase int64, err error) {
	row, err := r.q.RegionParcelWithin(ctx, sqlcgen.RegionParcelWithinParams{
		ParcelGeojson: parcelGeoJSON,
		RegionID:      regionID,
	})
	if err != nil {
		return false, 0, err
	}
	return row.Within, row.CanonBase, nil
}

// ConcessionParcelOverlaps indica si la parcela se solapa con una concesión
// activa de la región.
func (r *Repo) ConcessionParcelOverlaps(ctx context.Context, regionID uuid.UUID, parcelGeoJSON string) (bool, error) {
	overlaps, err := r.q.ConcessionParcelOverlaps(ctx, sqlcgen.ConcessionParcelOverlapsParams{
		RegionID:      regionID,
		ParcelGeojson: parcelGeoJSON,
	})
	if err != nil {
		return false, fmt.Errorf("world/land: comprobando solape de parcela en %s: %w", regionID, err)
	}
	return overlaps, nil
}

// insertConcessionParams son los parámetros de InsertConcession en términos del
// dominio.
type insertConcessionParams struct {
	ID            uuid.UUID
	RegionID      uuid.UUID
	Holder        uuid.UUID
	ParcelGeoJSON string
	CanonAmount   int64
	PeriodSimDays int32
	ExpiresAtSim  simtime.SimTime
	GrantedAtSim  simtime.SimTime
}

// InsertConcession crea la concesión activa.
func (r *Repo) InsertConcession(ctx context.Context, p insertConcessionParams) (Concession, error) {
	row, err := r.q.InsertConcession(ctx, sqlcgen.InsertConcessionParams{
		ID:              p.ID,
		RegionID:        p.RegionID,
		HolderAccountID: p.Holder,
		ParcelGeojson:   p.ParcelGeoJSON,
		CanonAmount:     p.CanonAmount,
		PeriodSimDays:   p.PeriodSimDays,
		ExpiresAtSim:    int64(p.ExpiresAtSim),
		GrantedAtSim:    int64(p.GrantedAtSim),
	})
	if err != nil {
		return Concession{}, fmt.Errorf("world/land: creando la concesión %s: %w", p.ID, err)
	}
	return concessionFrom(row.ID, row.RegionID, row.HolderAccountID, row.Parcel,
		row.CanonAmount, row.PeriodSimDays, row.ExpiresAtSim, row.Status, row.GrantedAtSim), nil
}

// RenewConcession extiende el vencimiento en extendSimSeconds de sim-time.
func (r *Repo) RenewConcession(ctx context.Context, id uuid.UUID, extendSimSeconds int64) (Concession, error) {
	row, err := r.q.RenewConcession(ctx, sqlcgen.RenewConcessionParams{
		ExtendSimSeconds: extendSimSeconds,
		ID:               id,
	})
	if err != nil {
		return Concession{}, fmt.Errorf("world/land: renovando la concesión %s: %w", id, err)
	}
	return concessionFrom(row.ID, row.RegionID, row.HolderAccountID, row.Parcel,
		row.CanonAmount, row.PeriodSimDays, row.ExpiresAtSim, row.Status, row.GrantedAtSim), nil
}

// SetConcessionHolder cambia el titular (traspaso).
func (r *Repo) SetConcessionHolder(ctx context.Context, id, holder uuid.UUID) (Concession, error) {
	row, err := r.q.SetConcessionHolder(ctx, sqlcgen.SetConcessionHolderParams{
		HolderAccountID: holder,
		ID:              id,
	})
	if err != nil {
		return Concession{}, fmt.Errorf("world/land: cambiando el titular de la concesión %s: %w", id, err)
	}
	return concessionFrom(row.ID, row.RegionID, row.HolderAccountID, row.Parcel,
		row.CanonAmount, row.PeriodSimDays, row.ExpiresAtSim, row.Status, row.GrantedAtSim), nil
}

// insertTransferParams son los parámetros de InsertConcessionTransfer.
type insertTransferParams struct {
	ID            uuid.UUID
	ConcessionID  uuid.UUID
	From          uuid.UUID
	To            uuid.UUID
	Price         int64
	SystemFee     int64
	OccurredAtSim simtime.SimTime
}

// InsertConcessionTransfer registra el traspaso ejecutado.
func (r *Repo) InsertConcessionTransfer(ctx context.Context, p insertTransferParams) (ConcessionTransfer, error) {
	row, err := r.q.InsertConcessionTransfer(ctx, sqlcgen.InsertConcessionTransferParams{
		ID:            p.ID,
		ConcessionID:  p.ConcessionID,
		FromAccountID: p.From,
		ToAccountID:   p.To,
		Price:         p.Price,
		SystemFee:     p.SystemFee,
		OccurredAtSim: int64(p.OccurredAtSim),
	})
	if err != nil {
		return ConcessionTransfer{}, fmt.Errorf("world/land: registrando el traspaso de %s: %w", p.ConcessionID, err)
	}
	return ConcessionTransfer{
		ID:            row.ID,
		ConcessionID:  row.ConcessionID,
		FromAccountID: row.FromAccountID,
		ToAccountID:   row.ToAccountID,
		Price:         row.Price,
		SystemFee:     row.SystemFee,
		OccurredAtSim: row.OccurredAtSim,
	}, nil
}

// ─── Soporte de ledger (mismo patrón que internal/contracts) ─────────────────

// ledgerAccount es la vista mínima de una cuenta del ledger (id y saldo).
type ledgerAccount struct {
	ID      uuid.UUID
	Balance int64
}

// GetCashAccount devuelve la caja de una corporación; pgx.ErrNoRows si no existe.
func (r *Repo) GetCashAccount(ctx context.Context, owner uuid.UUID) (ledgerAccount, error) {
	row, err := r.q.GetCashAccount(ctx, &owner)
	if err != nil {
		return ledgerAccount{}, err
	}
	return ledgerAccount{ID: row.ID, Balance: row.Balance}, nil
}

// GetSinkAccount devuelve la cuenta sink del banco central; pgx.ErrNoRows si el
// seed no la creó.
func (r *Repo) GetSinkAccount(ctx context.Context) (ledgerAccount, error) {
	row, err := r.q.GetSinkAccount(ctx)
	if err != nil {
		return ledgerAccount{}, err
	}
	return ledgerAccount{ID: row.ID, Balance: row.Balance}, nil
}

// AccountExists comprueba la existencia de una cuenta de auth.
func (r *Repo) AccountExists(ctx context.Context, id uuid.UUID) (bool, error) {
	ok, err := r.q.AccountExists(ctx, id)
	if err != nil {
		return false, fmt.Errorf("world/land: comprobando la cuenta %s: %w", id, err)
	}
	return ok, nil
}

// entryAmount es una partida de un asiento del ledger (importe con signo, nunca
// cero).
type entryAmount struct {
	AccountID uuid.UUID
	Amount    int64
}

// PostLedgerTransaction asienta cabecera + partidas dentro de la transacción SQL
// del Repo. Los triggers de 0004 aplican los saldos y garantizan la
// no-negatividad y la doble entrada por activo. Los IDs (UUIDv7) los genera la
// aplicación (ADR-018).
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
		return fmt.Errorf("world/land: asentando la cabecera %s de %s: %w", kind, reference, err)
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
			return fmt.Errorf("world/land: asentando la partida de %s (cuenta %s): %w", reference, e.AccountID, err)
		}
	}
	return nil
}

// ─── Conversión ──────────────────────────────────────────────────────────────

// concessionFrom construye el tipo de dominio desde las columnas comunes de las
// filas generadas (todas las queries de concesión devuelven la misma forma).
func concessionFrom(id, region, holder uuid.UUID, parcel string, canon int64, period int32, expires int64, status sqlcgen.WorldConcessionStatus, granted int64) Concession {
	return Concession{
		ID:              id,
		RegionID:        region,
		HolderAccountID: holder,
		Parcel:          parcel,
		CanonAmount:     canon,
		PeriodSimDays:   period,
		ExpiresAtSim:    expires,
		Status:          string(status),
		GrantedAtSim:    granted,
	}
}

// newUUIDv7 genera un UUIDv7 (ADR-018: los IDs los produce la aplicación).
func newUUIDv7() (uuid.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("world/land: generando UUIDv7: %w", err)
	}
	return id, nil
}
