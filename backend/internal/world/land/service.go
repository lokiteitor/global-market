package land

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/outbox"
	"github.com/lokiteitor/global-market/backend/internal/platform/db"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
	"github.com/lokiteitor/global-market/backend/internal/world/sqlcgen"
)

// SQLSTATE y constraints que este subpaquete traduce a errores tipados.
const (
	sqlstateCheckViolation = "23514" // check_violation
	sqlstateFKViolation    = "23503" // foreign_key_violation

	constraintNonNegative = "ck_accounts_non_negative"

	// bpDenominator es el 100% en puntos básicos (denominador de la tasa).
	bpDenominator int64 = 10000
)

// SimSource entrega el sim-time actual del mundo. Producción: *clock.Reader; los
// tests inyectan un reloj fijo.
type SimSource interface {
	Now(ctx context.Context) simtime.SimTime
}

// Service implementa el ciclo de vida del suelo (concesiones y traspasos). Toda
// operación que mueve valor corre en una única transacción SERIALIZABLE
// (platform/db.RunSerializable) que asienta a la vez el estado del mundo, las
// partidas del ledger (canon/transfer como sink) y el evento del outbox.
type Service struct {
	pool   *pgxpool.Pool
	repo   *Repo
	sim    SimSource
	opts   Options
	logger *slog.Logger

	granted     prometheus.Counter
	renewed     prometheus.Counter
	transferred prometheus.Counter
}

// NewService construye el servicio sobre el pool compartido de la plataforma.
// reg registra las métricas (nil las deja sin registrar: tests); logger nil usa
// slog.Default(). Options inválidas devuelven error: la configuración rota debe
// impedir el arranque.
func NewService(pool *pgxpool.Pool, sim SimSource, opts Options, logger *slog.Logger, reg prometheus.Registerer) (*Service, error) {
	if pool == nil {
		return nil, errors.New("world/land: el pool de BD es obligatorio")
	}
	if sim == nil {
		return nil, errors.New("world/land: el SimSource es obligatorio")
	}
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{
		pool:   pool,
		repo:   NewRepo(pool),
		sim:    sim,
		opts:   opts,
		logger: logger,
		granted: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_concessions_granted_total",
			Help: "Total de concesiones de suelo otorgadas.",
		}),
		renewed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_concessions_renewed_total",
			Help: "Total de renovaciones de concesión.",
		}),
		transferred: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_concession_transfers_total",
			Help: "Total de traspasos de concesión ejecutados.",
		}),
	}
	if reg != nil {
		reg.MustRegister(s.granted, s.renewed, s.transferred)
	}
	return s, nil
}

// ─── Lectura ─────────────────────────────────────────────────────────────────

// ListConcessions devuelve las concesiones del titular autenticado (SOLO
// propias) con los filtros del contrato y el cursor de la página siguiente.
func (s *Service) ListConcessions(ctx context.Context, holder uuid.UUID, f ConcessionFilter) ([]Concession, string, error) {
	if f.Status != "" && !validConcessionStatus(f.Status) {
		return nil, "", fmt.Errorf("%w: status inválido %q", ErrValidation, f.Status)
	}
	limit := normalizeLimit(f.Limit)
	var afterID *uuid.UUID
	if f.Cursor != "" {
		id, err := decodeCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		afterID = &id
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.QueryTimeout)
	defer cancel()

	rows, err := s.repo.ListConcessions(ctx, holder, f, afterID, limit+1)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > int(limit) {
		rows = rows[:limit]
		next = encodeCursor(rows[len(rows)-1].ID)
	}
	return rows, next, nil
}

// GetConcession devuelve el detalle de una concesión propia. ErrConcessionNotFound
// si no existe; ErrNotHolder si pertenece a otra corporación.
func (s *Service) GetConcession(ctx context.Context, holder, id uuid.UUID) (Concession, error) {
	ctx, cancel := context.WithTimeout(ctx, s.opts.QueryTimeout)
	defer cancel()

	c, err := s.repo.GetConcession(ctx, id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Concession{}, fmt.Errorf("%w (%s)", ErrConcessionNotFound, id)
	case err != nil:
		return Concession{}, fmt.Errorf("world/land: consultando la concesión %s: %w", id, err)
	}
	if c.HolderAccountID != holder {
		return Concession{}, fmt.Errorf("%w (%s)", ErrNotHolder, id)
	}
	return c, nil
}

// ─── Otorgamiento ────────────────────────────────────────────────────────────

// CreateConcession otorga una concesión sobre una parcela dentro de la región,
// que no se solape con otra vigente, cobrando el primer canon al sink. Todo
// —validación, cobro, alta y evento— se confirma en una única transacción
// SERIALIZABLE.
func (s *Service) CreateConcession(ctx context.Context, holder uuid.UUID, in ConcessionInput) (Concession, error) {
	if holder == uuid.Nil {
		return Concession{}, fmt.Errorf("%w: titular vacío", ErrValidation)
	}
	simNow := s.sim.Now(ctx)
	parcelGeoJSON := string(in.Parcel)

	var out Concession
	err := db.RunSerializable(ctx, s.pool, func(tx pgx.Tx) error {
		r := s.repo.WithTx(tx)

		within, canonBase, err := r.RegionParcelWithin(ctx, in.RegionID, parcelGeoJSON)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("%w: la región %s no existe", ErrValidation, in.RegionID)
		case err != nil:
			return fmt.Errorf("world/land: validando la región %s: %w", in.RegionID, err)
		}
		if !within {
			return fmt.Errorf("%w: la parcela no está dentro de los límites de la región", ErrValidation)
		}
		canon := canonBase * CanonBaseMultiplier
		if canon <= 0 {
			return fmt.Errorf("%w: la región no admite concesiones (canon base %d)", ErrValidation, canonBase)
		}

		overlaps, err := r.ConcessionParcelOverlaps(ctx, in.RegionID, parcelGeoJSON)
		if err != nil {
			return err
		}
		if overlaps {
			return ErrParcelOverlap
		}

		concessionID, err := newUUIDv7()
		if err != nil {
			return err
		}
		if err := s.chargeCanon(ctx, r, holder, concessionID, canon, simNow,
			fmt.Sprintf("Canon inicial de concesión (%d)", canon)); err != nil {
			return err
		}

		expires := simNow + simtime.SimTime(int64(ConcessionPeriodDays)*simtime.SimDay)
		out, err = r.InsertConcession(ctx, insertConcessionParams{
			ID:            concessionID,
			RegionID:      in.RegionID,
			Holder:        holder,
			ParcelGeoJSON: parcelGeoJSON,
			CanonAmount:   canon,
			PeriodSimDays: ConcessionPeriodDays,
			ExpiresAtSim:  expires,
			GrantedAtSim:  simNow,
		})
		if err != nil {
			return err
		}
		return outbox.Emit(ctx, tx, int64(simNow), AggregateConcession, out.ID, EventConcessionGranted, ConcessionGrantedPayload{
			ConcessionID:    out.ID.String(),
			RegionID:        out.RegionID.String(),
			HolderAccountID: holder.String(),
			CanonAmount:     fixed(out.CanonAmount),
			ExpiresAtSim:    out.ExpiresAtSim,
			GrantedAtSim:    out.GrantedAtSim,
		})
	})
	if err != nil {
		return Concession{}, mapLedgerError(err)
	}
	s.granted.Inc()
	s.logger.Info("concesión otorgada",
		slog.String("concession_id", out.ID.String()),
		slog.String("holder", holder.String()),
		slog.Int64("canon", out.CanonAmount))
	return out, nil
}

// RenewConcession extiende una concesión propia otro periodo pagando el canon
// vigente al sink.
func (s *Service) RenewConcession(ctx context.Context, holder, id uuid.UUID) (Concession, error) {
	simNow := s.sim.Now(ctx)

	var out Concession
	err := db.RunSerializable(ctx, s.pool, func(tx pgx.Tx) error {
		r := s.repo.WithTx(tx)

		c, err := r.GetConcessionForUpdate(ctx, id)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("%w (%s)", ErrConcessionNotFound, id)
		case err != nil:
			return fmt.Errorf("world/land: bloqueando la concesión %s: %w", id, err)
		}
		if c.HolderAccountID != holder {
			return fmt.Errorf("%w (%s)", ErrNotHolder, id)
		}
		if c.Status == string(sqlcgen.WorldConcessionStatusReverted) {
			return ErrConcessionReverted
		}
		if err := s.chargeCanon(ctx, r, holder, id, c.CanonAmount, simNow,
			fmt.Sprintf("Canon de renovación de concesión (%d)", c.CanonAmount)); err != nil {
			return err
		}
		extend := int64(c.PeriodSimDays) * simtime.SimDay
		out, err = r.RenewConcession(ctx, id, extend)
		if err != nil {
			return err
		}
		return outbox.Emit(ctx, tx, int64(simNow), AggregateConcession, out.ID, EventConcessionRenewed, ConcessionRenewedPayload{
			ConcessionID:    out.ID.String(),
			HolderAccountID: holder.String(),
			CanonAmount:     fixed(out.CanonAmount),
			ExpiresAtSim:    out.ExpiresAtSim,
			RenewedAtSim:    int64(simNow),
		})
	})
	if err != nil {
		return Concession{}, mapLedgerError(err)
	}
	s.renewed.Inc()
	s.logger.Info("concesión renovada",
		slog.String("concession_id", out.ID.String()),
		slog.String("holder", holder.String()),
		slog.Int64("expires_at_sim", out.ExpiresAtSim))
	return out, nil
}

// TransferConcession traspasa una concesión propia a otra corporación. En v1 lo
// invoca el TITULAR ACTUAL (vendedor) indicando el destinatario; el precio se
// debita de la caja del COMPRADOR (que debe tener fondos) hacia el vendedor, y
// la tasa del sistema (II_CONCESSION_TRANSFER_FEE_BP) del comprador al sink. Un
// flujo de oferta/aceptación con consentimiento explícito del comprador es
// mejora futura (documentado).
func (s *Service) TransferConcession(ctx context.Context, seller uuid.UUID, in TransferInput) (ConcessionTransfer, error) {
	if seller == uuid.Nil {
		return ConcessionTransfer{}, fmt.Errorf("%w: vendedor vacío", ErrValidation)
	}
	if in.Price < 0 {
		return ConcessionTransfer{}, fmt.Errorf("%w: price debe ser >= 0", ErrValidation)
	}
	fee, err := transferFee(in.Price, s.opts.TransferFeeBP)
	if err != nil {
		return ConcessionTransfer{}, err
	}
	total := in.Price + fee
	simNow := s.sim.Now(ctx)

	var out ConcessionTransfer
	err = db.RunSerializable(ctx, s.pool, func(tx pgx.Tx) error {
		r := s.repo.WithTx(tx)

		c, err := r.GetConcessionForUpdate(ctx, in.ConcessionID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("%w (%s)", ErrConcessionNotFound, in.ConcessionID)
		case err != nil:
			return fmt.Errorf("world/land: bloqueando la concesión %s: %w", in.ConcessionID, err)
		}
		if c.HolderAccountID != seller {
			return fmt.Errorf("%w (%s)", ErrNotHolder, in.ConcessionID)
		}
		if c.Status == string(sqlcgen.WorldConcessionStatusReverted) {
			return ErrConcessionReverted
		}
		if in.ToAccountID == seller {
			return fmt.Errorf("%w: no puede traspasarse a la propia corporación titular", ErrValidation)
		}
		exists, err := r.AccountExists(ctx, in.ToAccountID)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("%w: la cuenta destino %s no existe", ErrValidation, in.ToAccountID)
		}

		if total > 0 {
			if err := s.settleTransfer(ctx, r, seller, in.ToAccountID, in.ConcessionID, in.Price, fee, simNow); err != nil {
				return err
			}
		}

		if _, err := r.SetConcessionHolder(ctx, in.ConcessionID, in.ToAccountID); err != nil {
			return err
		}

		transferID, err := newUUIDv7()
		if err != nil {
			return err
		}
		out, err = r.InsertConcessionTransfer(ctx, insertTransferParams{
			ID:            transferID,
			ConcessionID:  in.ConcessionID,
			From:          seller,
			To:            in.ToAccountID,
			Price:         in.Price,
			SystemFee:     fee,
			OccurredAtSim: simNow,
		})
		if err != nil {
			return err
		}
		return outbox.Emit(ctx, tx, int64(simNow), AggregateConcession, in.ConcessionID, EventConcessionTransferred, ConcessionTransferredPayload{
			TransferID:    out.ID.String(),
			ConcessionID:  out.ConcessionID.String(),
			FromAccountID: out.FromAccountID.String(),
			ToAccountID:   out.ToAccountID.String(),
			Price:         fixed(out.Price),
			SystemFee:     fixed(out.SystemFee),
			OccurredAtSim: out.OccurredAtSim,
		})
	})
	if err != nil {
		return ConcessionTransfer{}, mapLedgerError(err)
	}
	s.transferred.Inc()
	s.logger.Info("concesión traspasada",
		slog.String("concession_id", out.ConcessionID.String()),
		slog.String("from", out.FromAccountID.String()),
		slog.String("to", out.ToAccountID.String()),
		slog.Int64("price", out.Price),
		slog.Int64("system_fee", out.SystemFee))
	return out, nil
}

// ─── Asientos del ledger ─────────────────────────────────────────────────────

// chargeCanon cobra el canon (cash del titular → sink) con un asiento canon.
// FundsError (422 INSUFFICIENT_FUNDS) si la caja no cubre el canon; la
// verificación definitiva sigue siendo el constraint de no-negatividad.
func (s *Service) chargeCanon(ctx context.Context, r *Repo, holder, reference uuid.UUID, canon int64, simNow simtime.SimTime, description string) error {
	cash, err := s.cashOrFunds(ctx, r, holder, canon)
	if err != nil {
		return err
	}
	sink, err := r.GetSinkAccount(ctx)
	if err != nil {
		return fmt.Errorf("world/land: localizando la cuenta sink del banco central: %w", err)
	}
	return r.PostLedgerTransaction(ctx, sqlcgen.LedgerTransactionKindCanon, simNow, reference, description, []entryAmount{
		{AccountID: cash.ID, Amount: -canon},
		{AccountID: sink.ID, Amount: canon},
	})
}

// settleTransfer asienta el pago del traspaso: comprador → vendedor (price) y
// comprador → sink (fee), con un asiento transfer. El comprador debe tener
// fondos para price+fee (FundsError si no).
func (s *Service) settleTransfer(ctx context.Context, r *Repo, seller, buyer, reference uuid.UUID, price, fee int64, simNow simtime.SimTime) error {
	total := price + fee
	buyerCash, err := s.cashOrFunds(ctx, r, buyer, total)
	if err != nil {
		return err
	}
	entries := []entryAmount{{AccountID: buyerCash.ID, Amount: -total}}
	if price > 0 {
		sellerCash, err := r.GetCashAccount(ctx, seller)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: el vendedor no tiene caja para recibir el pago", ErrValidation)
			}
			return fmt.Errorf("world/land: localizando la caja del vendedor %s: %w", seller, err)
		}
		entries = append(entries, entryAmount{AccountID: sellerCash.ID, Amount: price})
	}
	if fee > 0 {
		sink, err := r.GetSinkAccount(ctx)
		if err != nil {
			return fmt.Errorf("world/land: localizando la cuenta sink del banco central: %w", err)
		}
		entries = append(entries, entryAmount{AccountID: sink.ID, Amount: fee})
	}
	return r.PostLedgerTransaction(ctx, sqlcgen.LedgerTransactionKindTransfer, simNow, reference,
		fmt.Sprintf("Traspaso de concesión: precio %d + tasa %d", price, fee), entries)
}

// cashOrFunds localiza la caja del titular y comprueba que cubre required. La
// ausencia de cuenta o el saldo corto devuelven FundsError (422
// INSUFFICIENT_FUNDS) con {required, available}.
func (s *Service) cashOrFunds(ctx context.Context, r *Repo, owner uuid.UUID, required int64) (ledgerAccount, error) {
	acc, err := r.GetCashAccount(ctx, owner)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ledgerAccount{}, &FundsError{Required: required, Available: 0}
	case err != nil:
		return ledgerAccount{}, fmt.Errorf("world/land: consultando la caja de %s: %w", owner, err)
	case acc.Balance < required:
		return ledgerAccount{}, &FundsError{Required: required, Available: acc.Balance}
	}
	return acc, nil
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// transferFee calcula la tasa del sistema (price*feeBP/10000) validando con
// math/big que ni la tasa ni price+fee desbordan int64.
func transferFee(price, feeBP int64) (int64, error) {
	f := new(big.Int).Mul(big.NewInt(price), big.NewInt(feeBP))
	f.Quo(f, big.NewInt(bpDenominator))
	total := new(big.Int).Add(big.NewInt(price), f)
	if !total.IsInt64() {
		return 0, ErrOverflow
	}
	return f.Int64(), nil
}

// normalizeLimit aplica el default y el máximo del contrato (50/200).
func normalizeLimit(limit int) int32 {
	switch {
	case limit <= 0:
		return DefaultPageLimit
	case int32(limit) > MaxPageLimit:
		return MaxPageLimit
	default:
		return int32(limit)
	}
}

// validConcessionStatus indica si s es un estado de concesión válido.
func validConcessionStatus(s string) bool {
	switch sqlcgen.WorldConcessionStatus(s) {
	case sqlcgen.WorldConcessionStatusActive, sqlcgen.WorldConcessionStatusDelinquent,
		sqlcgen.WorldConcessionStatusGrace, sqlcgen.WorldConcessionStatusReverted:
		return true
	}
	return false
}

// mapLedgerError traduce las violaciones de invariantes de la BD (carreras
// resueltas por constraint) a errores tipados del subpaquete.
func mapLedgerError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == sqlstateCheckViolation && pgErr.ConstraintName == constraintNonNegative:
			// El asiento dejaría la caja negativa: fondos insuficientes
			// detectados por el constraint (todo-o-nada: nada quedó asentado).
			return fmt.Errorf("%w: %s", ErrInsufficientFunds, pgErr.Message)
		case pgErr.Code == sqlstateFKViolation:
			return fmt.Errorf("%w: referencia inexistente (%s)", ErrValidation, pgErr.ConstraintName)
		}
	}
	return err
}
