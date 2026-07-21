package power

import (
	"context"
	"encoding/json"
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

const (
	sqlstateCheckViolation = "23514" // check_violation
	constraintNonNegative  = "ck_accounts_non_negative"
)

// SimSource entrega el sim-time actual (lo implementa el composition root).
type SimSource interface {
	Now(ctx context.Context) simtime.SimTime
}

// Service materializa los comandos y lecturas de la red eléctrica (activos
// físicos: líneas, ofertas, pujas; el tick del spot es del Balancer).
type Service struct {
	pool   *pgxpool.Pool
	repo   *Repo
	sim    SimSource
	opts   Options
	logger *slog.Logger

	linesCreated prometheus.Counter
}

// NewService construye el servicio del subpaquete power.
func NewService(pool *pgxpool.Pool, sim SimSource, opts Options, logger *slog.Logger, reg prometheus.Registerer) (*Service, error) {
	if pool == nil {
		return nil, errors.New("world/power: el pool de BD es obligatorio")
	}
	if sim == nil {
		return nil, errors.New("world/power: el SimSource es obligatorio")
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
		logger: logger.With(slog.String("module", "world/power")),
		linesCreated: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_power_lines_built_total",
			Help: "Total de líneas de transmisión construidas.",
		}),
	}
	if reg != nil {
		reg.MustRegister(s.linesCreated)
	}
	return s, nil
}

// ─── Líneas de transmisión ───────────────────────────────────────────────────

// PowerLineInput es el comando de POST /world/power-lines.
type PowerLineInput struct {
	Path json.RawMessage
}

// CreatePowerLine da de alta una línea de transmisión: valida el trazado
// (LineString íntegro dentro de UNA región — sin interconexiones
// interregionales, GDD 22), cobra el coste por longitud al sink (como el
// build_cost de un edificio) y emite power_line.created, todo en una única tx
// SERIALIZABLE. Las líneas no requieren concesión de suelo (ADR-025 §4).
func (s *Service) CreatePowerLine(ctx context.Context, owner uuid.UUID, in PowerLineInput) (PowerLine, error) {
	if err := validateLineString(in.Path); err != nil {
		return PowerLine{}, fmt.Errorf("%w: path: %s", ErrValidation, err)
	}
	pathJSON := string(in.Path)
	simNow := s.sim.Now(ctx)

	var out PowerLine
	err := db.RunSerializable(ctx, s.pool, func(tx pgx.Tx) error {
		r := s.repo.WithTx(tx)

		regionID, _, err := r.RegionContainingLine(ctx, pathJSON)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return &PlacementError{Rule: "line_within_region",
					Message: "el trazado debe caer íntegro dentro de una región (las interconexiones interregionales son expansión futura)",
					Details: map[string]any{}}
			}
			return fmt.Errorf("world/power: resolviendo la región del trazado: %w", err)
		}
		lengthM, err := r.LineLengthM(ctx, pathJSON)
		if err != nil {
			return err
		}
		cost, err := lineCost(int64(lengthM), s.opts.LineCostPerKm)
		if err != nil {
			return err
		}

		id, err := newUUIDv7()
		if err != nil {
			return err
		}
		if err := s.chargeToSink(ctx, r, owner, id, cost, simNow,
			fmt.Sprintf("Construcción de línea eléctrica (%d m)", lengthM)); err != nil {
			return err
		}
		if err := r.InsertLine(ctx, id, owner, regionID, pathJSON, lengthM, simNow); err != nil {
			return err
		}
		if err := outbox.Emit(ctx, tx, int64(simNow), AggregatePowerLine, id, EventPowerLineCreated,
			PowerLineCreatedPayload{
				PowerLineID:    id.String(),
				OwnerAccountID: owner.String(),
				RegionID:       regionID.String(),
				LengthM:        lengthM,
				BuildCost:      fmt.Sprintf("%d", cost),
				CreatedAtSim:   int64(simNow),
			}); err != nil {
			return err
		}
		out, err = r.GetLine(ctx, id)
		return err
	})
	if err != nil {
		return PowerLine{}, err
	}
	s.linesCreated.Inc()
	return out, nil
}

// GetPowerLine devuelve una línea (catálogo público).
func (s *Service) GetPowerLine(ctx context.Context, id uuid.UUID) (PowerLine, error) {
	line, err := s.repo.GetLine(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return PowerLine{}, fmt.Errorf("%w: línea %s", ErrNotFound, id)
	}
	return line, err
}

// ListPowerLines pagina el catálogo de líneas (público, filtro por región).
func (s *Service) ListPowerLines(ctx context.Context, f LineFilter) ([]PowerLine, string, error) {
	limit := normalizeLimit(f.Limit)
	var afterID *uuid.UUID
	if f.Cursor != "" {
		id, err := decodeCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		afterID = &id
	}
	lines, err := s.repo.ListLines(ctx, f.RegionID, afterID, limit+1)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(lines) > int(limit) {
		lines = lines[:limit]
		next = encodeCursor(lines[len(lines)-1].ID)
	}
	return lines, next, nil
}

// ─── Ofertas y pujas ─────────────────────────────────────────────────────────

// SetOffer fija el precio de oferta de una central propia (unidades de dinero
// por unidad de energía). Solo tipos con parámetros de generación.
func (s *Service) SetOffer(ctx context.Context, owner, buildingID uuid.UUID, unitPrice int64) error {
	if unitPrice <= 0 {
		return fmt.Errorf("%w: unit_price debe ser > 0", ErrValidation)
	}
	b, err := s.buildingOwned(ctx, owner, buildingID)
	if err != nil {
		return err
	}
	if !b.IsPowerPlant {
		return fmt.Errorf("%w: el edificio %s no es una central eléctrica", ErrValidation, buildingID)
	}
	return s.repo.UpsertOffer(ctx, buildingID, unitPrice, s.sim.Now(ctx))
}

// SetBid fija la puja máxima de un edificio consumidor propio (prioridad
// inversa del recorte y techo personal; sin puja rige el default del tick).
func (s *Service) SetBid(ctx context.Context, owner, buildingID uuid.UUID, unitPrice int64) error {
	if unitPrice <= 0 {
		return fmt.Errorf("%w: unit_price debe ser > 0", ErrValidation)
	}
	b, err := s.buildingOwned(ctx, owner, buildingID)
	if err != nil {
		return err
	}
	if b.IsPowerPlant {
		return fmt.Errorf("%w: una central no consume electricidad (la puja es de consumidores)", ErrValidation)
	}
	return s.repo.UpsertBid(ctx, buildingID, unitPrice, s.sim.Now(ctx))
}

// ─── Lecturas del contrato ───────────────────────────────────────────────────

// ListSpotTicks devuelve el histórico del spot de una región (público).
func (s *Service) ListSpotTicks(ctx context.Context, region uuid.UUID, beforeSim *int64, limit int) ([]SpotTick, error) {
	return s.repo.ListSpotTicks(ctx, region, beforeSim, normalizeLimit(limit))
}

// ListDispatches devuelve el despacho/consumo de un edificio PROPIO.
func (s *Service) ListDispatches(ctx context.Context, owner, buildingID uuid.UUID, beforeSim *int64, limit int) ([]Dispatch, error) {
	if _, err := s.buildingOwned(ctx, owner, buildingID); err != nil {
		return nil, err
	}
	return s.repo.ListDispatches(ctx, buildingID, beforeSim, normalizeLimit(limit))
}

// ─── Soporte ─────────────────────────────────────────────────────────────────

// buildingOwned resuelve el edificio y autoriza por propiedad.
func (s *Service) buildingOwned(ctx context.Context, owner, buildingID uuid.UUID) (sqlcgen.GetBuildingForPowerRow, error) {
	b, err := s.repo.BuildingForPower(ctx, buildingID)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, fmt.Errorf("%w: edificio %s", ErrNotFound, buildingID)
	}
	if err != nil {
		return b, fmt.Errorf("world/power: leyendo el edificio %s: %w", buildingID, err)
	}
	if b.OwnerAccountID != owner {
		return b, ErrForbidden
	}
	return b, nil
}

// chargeToSink cobra un coste estructural a la caja del dueño hacia el sink del
// banco central (kind maintenance, como build_cost — v1.3).
func (s *Service) chargeToSink(ctx context.Context, r *Repo, owner, reference uuid.UUID, amount int64, simNow simtime.SimTime, description string) error {
	if amount <= 0 {
		return nil
	}
	cash, err := r.GetCashAccount(ctx, owner)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return &FundsError{Required: amount, Available: 0}
	case err != nil:
		return fmt.Errorf("world/power: localizando la caja de %s: %w", owner, err)
	}
	if cash.Balance < amount {
		return &FundsError{Required: amount, Available: cash.Balance}
	}
	sink, err := r.GetSinkAccount(ctx)
	if err != nil {
		return fmt.Errorf("world/power: localizando la cuenta sink del banco central: %w", err)
	}
	err = r.PostLedgerTransaction(ctx, sqlcgen.LedgerTransactionKindMaintenance, simNow, reference, description, []entryAmount{
		{AccountID: cash.ID, Amount: -amount},
		{AccountID: sink.ID, Amount: amount},
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == sqlstateCheckViolation && pgErr.ConstraintName == constraintNonNegative {
		return &FundsError{Required: amount, Available: cash.Balance}
	}
	return err
}

// lineCost calcula el coste de construcción por longitud con guarda de
// overflow: length_m × costPerKm / 1000 (redondeo hacia arriba).
func lineCost(lengthM, costPerKm int64) (int64, error) {
	product := new(big.Int).Mul(big.NewInt(lengthM), big.NewInt(costPerKm))
	product.Add(product, big.NewInt(999))
	product.Div(product, big.NewInt(1000))
	if !product.IsInt64() {
		return 0, fmt.Errorf("%w: el coste de la línea desborda int64", ErrValidation)
	}
	return product.Int64(), nil
}

// validateLineString valida la forma GeoJSON del trazado (LineString con >= 2
// vértices) antes de tocar PostGIS (400 en vez de un error interno).
func validateLineString(raw json.RawMessage) error {
	if len(raw) == 0 {
		return errors.New("requerido")
	}
	var g struct {
		Type        string      `json:"type"`
		Coordinates [][]float64 `json:"coordinates"`
	}
	if err := json.Unmarshal(raw, &g); err != nil {
		return errors.New("no es un objeto GeoJSON válido")
	}
	if g.Type != "LineString" {
		return errors.New("type debe ser LineString")
	}
	if len(g.Coordinates) < 2 {
		return errors.New("coordinates requiere al menos 2 vértices")
	}
	for _, p := range g.Coordinates {
		if len(p) < 2 {
			return errors.New("cada vértice requiere [x_m, y_m]")
		}
	}
	return nil
}
