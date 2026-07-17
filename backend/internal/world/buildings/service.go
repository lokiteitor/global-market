package buildings

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

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
)

// SimSource entrega el sim-time actual del mundo. Producción: *clock.Reader; los
// tests inyectan un reloj fijo.
type SimSource interface {
	Now(ctx context.Context) simtime.SimTime
}

// Service implementa el ciclo de vida de las instalaciones. Toda operación que
// mueve valor (build_cost, upgrade_cost) corre en una única transacción
// SERIALIZABLE (platform/db.RunSerializable) que asienta a la vez el estado del
// mundo (world.buildings, world.network_nodes), las partidas del ledger (sink) y
// el evento del outbox.
type Service struct {
	pool   *pgxpool.Pool
	repo   *Repo
	sim    SimSource
	opts   Options
	logger *slog.Logger

	created  prometheus.Counter
	upgraded prometheus.Counter
}

// NewService construye el servicio sobre el pool compartido de la plataforma.
func NewService(pool *pgxpool.Pool, sim SimSource, opts Options, logger *slog.Logger, reg prometheus.Registerer) (*Service, error) {
	if pool == nil {
		return nil, errors.New("world/buildings: el pool de BD es obligatorio")
	}
	if sim == nil {
		return nil, errors.New("world/buildings: el SimSource es obligatorio")
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
		created: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_buildings_created_total",
			Help: "Total de edificios iniciados (under_construction).",
		}),
		upgraded: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_buildings_upgraded_total",
			Help: "Total de mejoras de nivel de edificio.",
		}),
	}
	if reg != nil {
		reg.MustRegister(s.created, s.upgraded)
	}
	return s, nil
}

// ─── Lectura ─────────────────────────────────────────────────────────────────

// ListBuildings devuelve los edificios del dueño autenticado (SOLO propios) con
// los filtros del contrato y el cursor de la página siguiente.
func (s *Service) ListBuildings(ctx context.Context, owner uuid.UUID, f BuildingFilter) ([]Building, string, error) {
	if f.Status != "" && !validBuildingStatus(f.Status) {
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

	rows, err := s.repo.ListBuildings(ctx, owner, f, afterID, limit+1)
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

// GetBuilding devuelve el detalle de un edificio propio. ErrBuildingNotFound si
// no existe; ErrForbidden si pertenece a otra corporación.
func (s *Service) GetBuilding(ctx context.Context, owner, id uuid.UUID) (Building, error) {
	ctx, cancel := context.WithTimeout(ctx, s.opts.QueryTimeout)
	defer cancel()
	return s.getOwned(ctx, s.repo, owner, id)
}

// ListInventory devuelve el inventario físico de un edificio propio.
func (s *Service) ListInventory(ctx context.Context, owner, id uuid.UUID) ([]InventoryItem, error) {
	ctx, cancel := context.WithTimeout(ctx, s.opts.QueryTimeout)
	defer cancel()
	if _, err := s.getOwned(ctx, s.repo, owner, id); err != nil {
		return nil, err
	}
	return s.repo.ListBuildingInventory(ctx, id)
}

// getOwned localiza un edificio y verifica la propiedad (404/403).
func (s *Service) getOwned(ctx context.Context, r *Repo, owner, id uuid.UUID) (Building, error) {
	b, err := r.GetBuilding(ctx, id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Building{}, fmt.Errorf("%w (%s)", ErrBuildingNotFound, id)
	case err != nil:
		return Building{}, fmt.Errorf("world/buildings: consultando el edificio %s: %w", id, err)
	}
	if b.OwnerAccountID != owner {
		return Building{}, fmt.Errorf("%w (%s)", ErrForbidden, id)
	}
	return b, nil
}

// ─── Construcción ────────────────────────────────────────────────────────────

// CreateBuilding inicia la construcción de un edificio sobre una concesión
// propia, validando el emplazamiento (4 reglas), cobrando el coste al sink y
// creando el nodo del grafo logístico. Todo se confirma en una única transacción
// SERIALIZABLE.
func (s *Service) CreateBuilding(ctx context.Context, owner uuid.UUID, in BuildingInput) (Building, error) {
	if owner == uuid.Nil {
		return Building{}, fmt.Errorf("%w: dueño vacío", ErrValidation)
	}
	simNow := s.sim.Now(ctx)
	footprintGeoJSON := string(in.Footprint)

	var out Building
	var nodeKind sqlcgen.WorldNodeKind
	var nodeID uuid.UUID
	var buildCost int64
	err := db.RunSerializable(ctx, s.pool, func(tx pgx.Tx) error {
		r := s.repo.WithTx(tx)

		bt, err := r.GetBuildingType(ctx, in.BuildingTypeID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("%w: el tipo de edificio %s no existe", ErrValidation, in.BuildingTypeID)
		case err != nil:
			return fmt.Errorf("world/buildings: consultando el tipo %s: %w", in.BuildingTypeID, err)
		}
		buildCost = bt.BuildCost

		conc, err := r.LockConcessionForBuilding(ctx, in.ConcessionID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("%w: la concesión %s no existe", ErrValidation, in.ConcessionID)
		case err != nil:
			return fmt.Errorf("world/buildings: bloqueando la concesión %s: %w", in.ConcessionID, err)
		}
		// Regla (a): la concesión es del solicitante (propiedad) y está activa.
		if conc.HolderAccountID != owner {
			return fmt.Errorf("%w: la concesión %s pertenece a otra corporación", ErrForbidden, in.ConcessionID)
		}
		if conc.Status != string(sqlcgen.WorldConcessionStatusActive) {
			return &PlacementError{Rule: "concession_active",
				Message: fmt.Sprintf("la concesión no está activa (estado %s)", conc.Status),
				Details: map[string]any{"concession_status": conc.Status}}
		}

		rules, err := parsePlacementRules(bt.PlacementRules)
		if err != nil {
			return err
		}
		for _, u := range rules.Unknown {
			s.logger.Warn("regla de emplazamiento desconocida ignorada",
				slog.String("building_type", bt.Code), slog.String("rule", u))
		}

		// Regla (b): el footprint cae dentro de la parcela.
		within, err := r.FootprintWithinParcel(ctx, in.ConcessionID, footprintGeoJSON)
		if err != nil {
			return err
		}
		if !within {
			return &PlacementError{Rule: "footprint_within_parcel",
				Message: "el footprint no cae dentro de la parcela de la concesión"}
		}

		// Regla (c): el footprint no se solapa con edificios existentes.
		overlaps, err := r.BuildingFootprintOverlaps(ctx, footprintGeoJSON)
		if err != nil {
			return err
		}
		if overlaps {
			return &PlacementError{Rule: "footprint_overlap",
				Message: "el footprint se solapa con un edificio existente"}
		}

		// Regla (d): placement_rules del tipo.
		if err := s.checkPlacementRules(ctx, r, rules, conc.RegionID, footprintGeoJSON, bt.Code); err != nil {
			return err
		}

		buildingID, err := newUUIDv7()
		if err != nil {
			return err
		}
		if err := s.chargeToSink(ctx, r, owner, buildingID, bt.BuildCost, simNow,
			fmt.Sprintf("Coste de construcción de %s (%d)", bt.Code, bt.BuildCost)); err != nil {
			return err
		}

		out, err = r.InsertBuilding(ctx, insertBuildingParams{
			ID:               buildingID,
			Owner:            owner,
			RegionID:         conc.RegionID,
			ConcessionID:     in.ConcessionID,
			BuildingTypeID:   in.BuildingTypeID,
			FootprintGeoJSON: footprintGeoJSON,
			UpdatedAtSim:     simNow,
		})
		if err != nil {
			return err
		}

		nodeKind = deriveNodeKind(bt.Code, rules)
		nid, err := newUUIDv7()
		if err != nil {
			return err
		}
		nodeID, err = r.InsertNetworkNode(ctx, nid, nodeKind, conc.RegionID, buildingID, footprintGeoJSON)
		if err != nil {
			return err
		}
		return outbox.Emit(ctx, tx, int64(simNow), AggregateBuilding, out.ID, EventBuildingCreated, BuildingCreatedPayload{
			BuildingID:     out.ID.String(),
			OwnerAccountID: owner.String(),
			RegionID:       out.RegionID.String(),
			ConcessionID:   out.ConcessionID.String(),
			BuildingTypeID: out.BuildingTypeID.String(),
			NodeID:         nodeID.String(),
			NodeKind:       string(nodeKind),
			BuildCost:      fixed(bt.BuildCost),
			CreatedAtSim:   int64(simNow),
		})
	})
	if err != nil {
		return Building{}, mapLedgerError(err)
	}
	s.created.Inc()
	s.logger.Info("edificio en construcción",
		slog.String("building_id", out.ID.String()),
		slog.String("owner", owner.String()),
		slog.String("node_id", nodeID.String()),
		slog.String("node_kind", string(nodeKind)),
		slog.Int64("build_cost", buildCost))
	return out, nil
}

// checkPlacementRules evalúa las reglas near_resource y requires_node_kind; una
// regla presente pero mal formada o desconocida se ignora con warn (extensible).
func (s *Service) checkPlacementRules(ctx context.Context, r *Repo, rules parsedRules, regionID uuid.UUID, footprintGeoJSON, buildingTypeCode string) error {
	if rules.HasNearResource {
		if !rules.HasMaxDistance {
			s.logger.Warn("regla near_resource sin max_distance_m ignorada",
				slog.String("building_type", buildingTypeCode), slog.String("product", rules.NearResource))
		} else {
			present, err := r.ResourceNearby(ctx, rules.NearResource, footprintGeoJSON, rules.MaxDistanceM)
			if err != nil {
				return err
			}
			if !present {
				return &PlacementError{Rule: "near_resource",
					Message: fmt.Sprintf("no hay yacimiento de %q con existencias dentro de %.0f m", rules.NearResource, rules.MaxDistanceM),
					Details: map[string]any{"product": rules.NearResource, "max_distance_m": rules.MaxDistanceM}}
			}
		}
	}
	if rules.HasRequiresNode {
		if !validNodeKind(rules.RequiresNodeKind) {
			s.logger.Warn("regla requires_node_kind con kind desconocido ignorada",
				slog.String("building_type", buildingTypeCode), slog.String("node_kind", rules.RequiresNodeKind))
		} else {
			present, err := r.NodeKindPresentInRegion(ctx, regionID, rules.RequiresNodeKind)
			if err != nil {
				return err
			}
			if !present {
				return &PlacementError{Rule: "requires_node_kind",
					Message: fmt.Sprintf("no hay un nodo %q en la región", rules.RequiresNodeKind),
					Details: map[string]any{"node_kind": rules.RequiresNodeKind}}
			}
		}
	}
	return nil
}

// ─── Configuración ───────────────────────────────────────────────────────────

// UpdateBuilding cambia la receta activa (o la detiene con null) e/o inicia
// mantenimiento. Cambiar receta valida que pertenece al tipo del edificio y que
// su min_city_level lo alcanza la ciudad más cercana de la región.
func (s *Service) UpdateBuilding(ctx context.Context, owner, id uuid.UUID, in BuildingUpdateInput) (Building, error) {
	if !in.SetRecipe && !in.StartMaintenance {
		return Building{}, fmt.Errorf("%w: la actualización no cambia nada (active_recipe_id o start_maintenance)", ErrValidation)
	}
	simNow := s.sim.Now(ctx)

	var out Building
	err := db.RunSerializable(ctx, s.pool, func(tx pgx.Tx) error {
		r := s.repo.WithTx(tx)

		b, err := r.GetBuildingForUpdate(ctx, id)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("%w (%s)", ErrBuildingNotFound, id)
		case err != nil:
			return fmt.Errorf("world/buildings: bloqueando el edificio %s: %w", id, err)
		}
		if b.OwnerAccountID != owner {
			return fmt.Errorf("%w (%s)", ErrForbidden, id)
		}
		out = b

		if in.SetRecipe {
			if in.RecipeID != nil {
				if err := s.validateRecipe(ctx, r, b, *in.RecipeID); err != nil {
					return err
				}
			}
			out, err = r.SetBuildingRecipe(ctx, id, in.RecipeID, simNow)
			if err != nil {
				return err
			}
		}
		if in.StartMaintenance {
			out, err = r.SetBuildingStatus(ctx, id, sqlcgen.WorldBuildingStatusInMaintenance, simNow)
			if err != nil {
				return err
			}
		}
		return outbox.Emit(ctx, tx, int64(simNow), AggregateBuilding, out.ID, EventBuildingUpdated, BuildingUpdatedPayload{
			BuildingID:     out.ID.String(),
			OwnerAccountID: owner.String(),
			Status:         out.Status,
			ActiveRecipeID: uuidOrEmpty(out.ActiveRecipeID),
			UpdatedAtSim:   int64(simNow),
		})
	})
	if err != nil {
		return Building{}, mapLedgerError(err)
	}
	s.logger.Info("edificio actualizado",
		slog.String("building_id", out.ID.String()),
		slog.String("owner", owner.String()),
		slog.String("status", out.Status),
		slog.String("active_recipe_id", uuidOrEmpty(out.ActiveRecipeID)))
	return out, nil
}

// validateRecipe comprueba que la receta pertenece al tipo del edificio y que su
// min_city_level lo alcanza la ciudad más cercana de la región.
func (s *Service) validateRecipe(ctx context.Context, r *Repo, b Building, recipeID uuid.UUID) error {
	rec, err := r.GetRecipe(ctx, recipeID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("%w: la receta %s no existe", ErrValidation, recipeID)
	case err != nil:
		return fmt.Errorf("world/buildings: consultando la receta %s: %w", recipeID, err)
	}
	if rec.BuildingTypeID != b.BuildingTypeID {
		return fmt.Errorf("%w: la receta %s no pertenece al tipo del edificio", ErrValidation, recipeID)
	}
	level, err := r.NearestCityLevelInRegion(ctx, b.RegionID, b.ID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("%w: no hay ciudad cercana en la región para cualificar la receta", ErrValidation)
	case err != nil:
		return fmt.Errorf("world/buildings: buscando la ciudad más cercana: %w", err)
	}
	if rec.MinCityLevel > level {
		return fmt.Errorf("%w: la receta exige nivel de ciudad %d y la más cercana es de nivel %d", ErrValidation, rec.MinCityLevel, level)
	}
	return nil
}

// ─── Mejora ──────────────────────────────────────────────────────────────────

// UpgradeBuilding sube el nivel del edificio con coste no lineal según la
// level_curve del tipo, cobrado al sink.
func (s *Service) UpgradeBuilding(ctx context.Context, owner, id uuid.UUID) (Building, error) {
	simNow := s.sim.Now(ctx)

	var out Building
	var cost int64
	err := db.RunSerializable(ctx, s.pool, func(tx pgx.Tx) error {
		r := s.repo.WithTx(tx)

		b, err := r.GetBuildingForUpdate(ctx, id)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("%w (%s)", ErrBuildingNotFound, id)
		case err != nil:
			return fmt.Errorf("world/buildings: bloqueando el edificio %s: %w", id, err)
		}
		if b.OwnerAccountID != owner {
			return fmt.Errorf("%w (%s)", ErrForbidden, id)
		}
		bt, err := r.GetBuildingType(ctx, b.BuildingTypeID)
		if err != nil {
			return fmt.Errorf("world/buildings: consultando el tipo del edificio %s: %w", id, err)
		}
		if b.Level >= bt.MaxLevel {
			return fmt.Errorf("%w (nivel %d de %d)", ErrMaxLevelReached, b.Level, bt.MaxLevel)
		}
		destLevel := b.Level + 1
		cost, err = upgradeCost(bt.BuildCost, bt.LevelCurve, destLevel)
		if err != nil {
			return err
		}
		if err := s.chargeToSink(ctx, r, owner, id, cost, simNow,
			fmt.Sprintf("Coste de mejora al nivel %d (%d)", destLevel, cost)); err != nil {
			return err
		}
		out, err = r.SetBuildingLevel(ctx, id, destLevel, simNow)
		if err != nil {
			return err
		}
		return outbox.Emit(ctx, tx, int64(simNow), AggregateBuilding, out.ID, EventBuildingUpgraded, BuildingUpgradedPayload{
			BuildingID:     out.ID.String(),
			OwnerAccountID: owner.String(),
			Level:          out.Level,
			UpgradeCost:    fixed(cost),
			UpgradedAtSim:  int64(simNow),
		})
	})
	if err != nil {
		return Building{}, mapLedgerError(err)
	}
	s.upgraded.Inc()
	s.logger.Info("edificio mejorado",
		slog.String("building_id", out.ID.String()),
		slog.String("owner", owner.String()),
		slog.Int("level", int(out.Level)),
		slog.Int64("upgrade_cost", cost))
	return out, nil
}

// ─── Asientos del ledger ─────────────────────────────────────────────────────

// chargeToSink cobra amount (cash del dueño → sink) con un asiento maintenance
// (build_cost/upgrade_cost como sink). amount <= 0 no genera asiento. FundsError
// (422 INSUFFICIENT_FUNDS) si la caja no cubre el coste.
func (s *Service) chargeToSink(ctx context.Context, r *Repo, owner, reference uuid.UUID, amount int64, simNow simtime.SimTime, description string) error {
	if amount <= 0 {
		return nil
	}
	cash, err := s.cashOrFunds(ctx, r, owner, amount)
	if err != nil {
		return err
	}
	sink, err := r.GetSinkAccount(ctx)
	if err != nil {
		return fmt.Errorf("world/buildings: localizando la cuenta sink del banco central: %w", err)
	}
	return r.PostLedgerTransaction(ctx, sqlcgen.LedgerTransactionKindMaintenance, simNow, reference, description, []entryAmount{
		{AccountID: cash.ID, Amount: -amount},
		{AccountID: sink.ID, Amount: amount},
	})
}

// cashOrFunds localiza la caja del dueño y comprueba que cubre required.
func (s *Service) cashOrFunds(ctx context.Context, r *Repo, owner uuid.UUID, required int64) (ledgerAccount, error) {
	acc, err := r.GetCashAccount(ctx, owner)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ledgerAccount{}, &FundsError{Required: required, Available: 0}
	case err != nil:
		return ledgerAccount{}, fmt.Errorf("world/buildings: consultando la caja de %s: %w", owner, err)
	case acc.Balance < required:
		return ledgerAccount{}, &FundsError{Required: required, Available: acc.Balance}
	}
	return acc, nil
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

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

// validBuildingStatus indica si s es un estado de edificio válido.
func validBuildingStatus(s string) bool {
	switch sqlcgen.WorldBuildingStatus(s) {
	case sqlcgen.WorldBuildingStatusUnderConstruction, sqlcgen.WorldBuildingStatusOperational,
		sqlcgen.WorldBuildingStatusDamaged, sqlcgen.WorldBuildingStatusInMaintenance,
		sqlcgen.WorldBuildingStatusAbandoned, sqlcgen.WorldBuildingStatusSeized:
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
			return fmt.Errorf("%w: %s", ErrInsufficientFunds, pgErr.Message)
		case pgErr.Code == sqlstateFKViolation:
			return fmt.Errorf("%w: referencia inexistente (%s)", ErrValidation, pgErr.ConstraintName)
		}
	}
	return err
}
