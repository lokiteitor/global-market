package buildings

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
	"github.com/lokiteitor/global-market/backend/internal/world/sqlcgen"
)

// Repo es la capa de acceso a datos del subpaquete sobre el código generado por
// sqlc (paquete compartido del contexto world). No abre transacciones — el
// servicio decide el ámbito transaccional y deriva un Repo con WithTx.
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

// ─── Edificios ───────────────────────────────────────────────────────────────

// ListBuildings lista los edificios de un dueño con los filtros del contrato y
// la paginación keyset (id ASC).
func (r *Repo) ListBuildings(ctx context.Context, owner uuid.UUID, f BuildingFilter, afterID *uuid.UUID, limit int32) ([]Building, error) {
	rows, err := r.q.ListBuildings(ctx, sqlcgen.ListBuildingsParams{
		OwnerAccountID: owner,
		RegionID:       f.RegionID,
		Status: sqlcgen.NullWorldBuildingStatus{
			WorldBuildingStatus: sqlcgen.WorldBuildingStatus(f.Status),
			Valid:               f.Status != "",
		},
		BuildingTypeID: f.BuildingTypeID,
		AfterID:        afterID,
		PageLimit:      limit,
	})
	if err != nil {
		return nil, fmt.Errorf("world/buildings: listando edificios de %s: %w", owner, err)
	}
	out := make([]Building, len(rows))
	for i, row := range rows {
		out[i] = buildingFrom(row.ID, row.OwnerAccountID, row.RegionID, row.ConcessionID,
			row.BuildingTypeID, row.Footprint, row.Level, row.Status, row.ActiveRecipeID,
			row.ConditionPct, row.FuelStock, row.CreatedAt, row.UpdatedAtSim)
	}
	return out, nil
}

// GetBuilding devuelve un edificio por id; pgx.ErrNoRows si no existe.
func (r *Repo) GetBuilding(ctx context.Context, id uuid.UUID) (Building, error) {
	row, err := r.q.GetBuilding(ctx, id)
	if err != nil {
		return Building{}, err
	}
	return buildingFrom(row.ID, row.OwnerAccountID, row.RegionID, row.ConcessionID,
		row.BuildingTypeID, row.Footprint, row.Level, row.Status, row.ActiveRecipeID,
		row.ConditionPct, row.FuelStock, row.CreatedAt, row.UpdatedAtSim), nil
}

// GetBuildingForUpdate bloquea la fila (FOR UPDATE); pgx.ErrNoRows si no existe.
func (r *Repo) GetBuildingForUpdate(ctx context.Context, id uuid.UUID) (Building, error) {
	row, err := r.q.GetBuildingForUpdate(ctx, id)
	if err != nil {
		return Building{}, err
	}
	return buildingFrom(row.ID, row.OwnerAccountID, row.RegionID, row.ConcessionID,
		row.BuildingTypeID, row.Footprint, row.Level, row.Status, row.ActiveRecipeID,
		row.ConditionPct, row.FuelStock, row.CreatedAt, row.UpdatedAtSim), nil
}

// buildingType es la vista del tipo de edificio que el subpaquete necesita para
// construir/mejorar.
type buildingType struct {
	ID             uuid.UUID
	Code           string
	MaxLevel       int32
	PlacementRules []byte
	LevelCurve     []byte
	BuildCost      int64
}

// GetBuildingType devuelve el tipo de edificio; pgx.ErrNoRows si no existe.
func (r *Repo) GetBuildingType(ctx context.Context, id uuid.UUID) (buildingType, error) {
	row, err := r.q.GetBuildingType(ctx, id)
	if err != nil {
		return buildingType{}, err
	}
	return buildingType{
		ID:             row.ID,
		Code:           row.Code,
		MaxLevel:       row.MaxLevel,
		PlacementRules: row.PlacementRules,
		LevelCurve:     row.LevelCurve,
		BuildCost:      row.BuildCost,
	}, nil
}

// recipe es la vista de una receta para validar tipo y cualificación.
type recipe struct {
	ID             uuid.UUID
	BuildingTypeID uuid.UUID
	MinCityLevel   int32
}

// GetRecipe devuelve una receta; pgx.ErrNoRows si no existe.
func (r *Repo) GetRecipe(ctx context.Context, id uuid.UUID) (recipe, error) {
	row, err := r.q.GetRecipe(ctx, id)
	if err != nil {
		return recipe{}, err
	}
	return recipe{ID: row.ID, BuildingTypeID: row.BuildingTypeID, MinCityLevel: row.MinCityLevel}, nil
}

// ─── Emplazamiento ───────────────────────────────────────────────────────────

// concessionInfo es la vista de la concesión de destino para el emplazamiento.
type concessionInfo struct {
	ID              uuid.UUID
	RegionID        uuid.UUID
	HolderAccountID uuid.UUID
	Status          string
}

// LockConcessionForBuilding bloquea la concesión de destino; pgx.ErrNoRows si no
// existe.
func (r *Repo) LockConcessionForBuilding(ctx context.Context, id uuid.UUID) (concessionInfo, error) {
	row, err := r.q.LockConcessionForBuilding(ctx, id)
	if err != nil {
		return concessionInfo{}, err
	}
	return concessionInfo{
		ID:              row.ID,
		RegionID:        row.RegionID,
		HolderAccountID: row.HolderAccountID,
		Status:          string(row.Status),
	}, nil
}

// FootprintWithinParcel comprueba que el footprint cae dentro de la parcela.
func (r *Repo) FootprintWithinParcel(ctx context.Context, concessionID uuid.UUID, footprintGeoJSON string) (bool, error) {
	within, err := r.q.FootprintWithinParcel(ctx, sqlcgen.FootprintWithinParcelParams{
		FootprintGeojson: footprintGeoJSON,
		ID:               concessionID,
	})
	if err != nil {
		return false, fmt.Errorf("world/buildings: comprobando contención del footprint: %w", err)
	}
	return within, nil
}

// BuildingFootprintOverlaps comprueba solape con edificios existentes.
func (r *Repo) BuildingFootprintOverlaps(ctx context.Context, footprintGeoJSON string) (bool, error) {
	overlaps, err := r.q.BuildingFootprintOverlaps(ctx, footprintGeoJSON)
	if err != nil {
		return false, fmt.Errorf("world/buildings: comprobando solape de footprint: %w", err)
	}
	return overlaps, nil
}

// ResourceNearby comprueba la regla near_resource.
func (r *Repo) ResourceNearby(ctx context.Context, productCode, footprintGeoJSON string, maxDistanceM float64) (bool, error) {
	present, err := r.q.ResourceNearby(ctx, sqlcgen.ResourceNearbyParams{
		ProductCode:      productCode,
		FootprintGeojson: footprintGeoJSON,
		MaxDistanceM:     maxDistanceM,
	})
	if err != nil {
		return false, fmt.Errorf("world/buildings: comprobando yacimiento cercano de %q: %w", productCode, err)
	}
	return present, nil
}

// NodeKindPresentInRegion comprueba la regla requires_node_kind.
func (r *Repo) NodeKindPresentInRegion(ctx context.Context, regionID uuid.UUID, nodeKind string) (bool, error) {
	present, err := r.q.NodeKindPresentInRegion(ctx, sqlcgen.NodeKindPresentInRegionParams{
		RegionID: regionID,
		NodeKind: sqlcgen.WorldNodeKind(nodeKind),
	})
	if err != nil {
		return false, fmt.Errorf("world/buildings: comprobando nodo %q en la región %s: %w", nodeKind, regionID, err)
	}
	return present, nil
}

// RegionBiome devuelve el bioma de la región (regla requires_biome, ADR-025).
func (r *Repo) RegionBiome(ctx context.Context, regionID uuid.UUID) (string, error) {
	biome, err := r.q.GetRegionBiome(ctx, regionID)
	if err != nil {
		return "", fmt.Errorf("world/buildings: leyendo el bioma de la región %s: %w", regionID, err)
	}
	return string(biome), nil
}

// ─── Construcción ────────────────────────────────────────────────────────────

// insertBuildingParams son los parámetros de InsertBuilding.
type insertBuildingParams struct {
	ID               uuid.UUID
	Owner            uuid.UUID
	RegionID         uuid.UUID
	ConcessionID     uuid.UUID
	BuildingTypeID   uuid.UUID
	FootprintGeoJSON string
	UpdatedAtSim     simtime.SimTime
}

// InsertBuilding crea el edificio under_construction.
func (r *Repo) InsertBuilding(ctx context.Context, p insertBuildingParams) (Building, error) {
	row, err := r.q.InsertBuilding(ctx, sqlcgen.InsertBuildingParams{
		ID:               p.ID,
		OwnerAccountID:   p.Owner,
		RegionID:         p.RegionID,
		ConcessionID:     p.ConcessionID,
		BuildingTypeID:   p.BuildingTypeID,
		FootprintGeojson: p.FootprintGeoJSON,
		UpdatedAtSim:     int64(p.UpdatedAtSim),
	})
	if err != nil {
		return Building{}, fmt.Errorf("world/buildings: creando el edificio %s: %w", p.ID, err)
	}
	return buildingFrom(row.ID, row.OwnerAccountID, row.RegionID, row.ConcessionID,
		row.BuildingTypeID, row.Footprint, row.Level, row.Status, row.ActiveRecipeID,
		row.ConditionPct, row.FuelStock, row.CreatedAt, row.UpdatedAtSim), nil
}

// InsertNetworkNode crea el nodo del grafo ligado al edificio, en el centroide
// del footprint.
func (r *Repo) InsertNetworkNode(ctx context.Context, id uuid.UUID, kind sqlcgen.WorldNodeKind, regionID, buildingID uuid.UUID, footprintGeoJSON string) (uuid.UUID, error) {
	bid := buildingID
	nodeID, err := r.q.InsertNetworkNode(ctx, sqlcgen.InsertNetworkNodeParams{
		ID:               id,
		Kind:             kind,
		RegionID:         regionID,
		BuildingID:       &bid,
		FootprintGeojson: footprintGeoJSON,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("world/buildings: creando el nodo del edificio %s: %w", buildingID, err)
	}
	return nodeID, nil
}

// NearestRoadNodeInRegion devuelve el nodo road-conectado de la región más
// cercano al nodo dado (enganche del ramal de última milla); pgx.ErrNoRows si la
// región aún no tiene red vial.
func (r *Repo) NearestRoadNodeInRegion(ctx context.Context, regionID, nodeID uuid.UUID) (uuid.UUID, error) {
	return r.q.NearestRoadNodeInRegion(ctx, sqlcgen.NearestRoadNodeInRegionParams{
		RegionID: regionID,
		NodeID:   nodeID,
	})
}

// InsertRoadSpurLink crea el enlace road dirigido from→to con su único segmento
// en la región y devuelve el id del enlace y su longitud en metros.
func (r *Repo) InsertRoadSpurLink(ctx context.Context, linkID, segmentID, regionID, fromNodeID, toNodeID uuid.UUID) (uuid.UUID, int32, error) {
	link, err := r.q.InsertRoadSpurLink(ctx, sqlcgen.InsertRoadSpurLinkParams{
		ID:              linkID,
		CapacityPerHour: spurRoadCapacityPerHour,
		BaseSpeedKmh:    spurRoadBaseSpeedKmh,
		FromNodeID:      fromNodeID,
		ToNodeID:        toNodeID,
	})
	if err != nil {
		return uuid.Nil, 0, fmt.Errorf("world/buildings: creando el ramal road %s→%s: %w", fromNodeID, toNodeID, err)
	}
	if err := r.q.InsertRoadSpurSegment(ctx, sqlcgen.InsertRoadSpurSegmentParams{
		ID:       segmentID,
		RegionID: regionID,
		LinkID:   link.ID,
	}); err != nil {
		return uuid.Nil, 0, fmt.Errorf("world/buildings: creando el segmento del ramal road %s: %w", link.ID, err)
	}
	return link.ID, link.LengthM, nil
}

// ─── Configuración y mejora ──────────────────────────────────────────────────

// NearestCityLevelInRegion devuelve el nivel de la ciudad más cercana al
// edificio en su región; pgx.ErrNoRows si la región no tiene ciudades.
func (r *Repo) NearestCityLevelInRegion(ctx context.Context, regionID, buildingID uuid.UUID) (int32, error) {
	return r.q.NearestCityLevelInRegion(ctx, sqlcgen.NearestCityLevelInRegionParams{
		RegionID:   regionID,
		BuildingID: buildingID,
	})
}

// SetBuildingRecipe fija (o detiene, con nil) la receta activa.
func (r *Repo) SetBuildingRecipe(ctx context.Context, id uuid.UUID, recipeID *uuid.UUID, updatedAtSim simtime.SimTime) (Building, error) {
	row, err := r.q.SetBuildingRecipe(ctx, sqlcgen.SetBuildingRecipeParams{
		ActiveRecipeID: recipeID,
		UpdatedAtSim:   int64(updatedAtSim),
		ID:             id,
	})
	if err != nil {
		return Building{}, fmt.Errorf("world/buildings: cambiando la receta del edificio %s: %w", id, err)
	}
	return buildingFrom(row.ID, row.OwnerAccountID, row.RegionID, row.ConcessionID,
		row.BuildingTypeID, row.Footprint, row.Level, row.Status, row.ActiveRecipeID,
		row.ConditionPct, row.FuelStock, row.CreatedAt, row.UpdatedAtSim), nil
}

// SetBuildingStatus cambia el estado del edificio.
func (r *Repo) SetBuildingStatus(ctx context.Context, id uuid.UUID, status sqlcgen.WorldBuildingStatus, updatedAtSim simtime.SimTime) (Building, error) {
	row, err := r.q.SetBuildingStatus(ctx, sqlcgen.SetBuildingStatusParams{
		Status:       status,
		UpdatedAtSim: int64(updatedAtSim),
		ID:           id,
	})
	if err != nil {
		return Building{}, fmt.Errorf("world/buildings: cambiando el estado del edificio %s: %w", id, err)
	}
	return buildingFrom(row.ID, row.OwnerAccountID, row.RegionID, row.ConcessionID,
		row.BuildingTypeID, row.Footprint, row.Level, row.Status, row.ActiveRecipeID,
		row.ConditionPct, row.FuelStock, row.CreatedAt, row.UpdatedAtSim), nil
}

// SetBuildingLevel sube el nivel del edificio (mejora).
func (r *Repo) SetBuildingLevel(ctx context.Context, id uuid.UUID, level int32, updatedAtSim simtime.SimTime) (Building, error) {
	row, err := r.q.SetBuildingLevel(ctx, sqlcgen.SetBuildingLevelParams{
		Level:        level,
		UpdatedAtSim: int64(updatedAtSim),
		ID:           id,
	})
	if err != nil {
		return Building{}, fmt.Errorf("world/buildings: subiendo el nivel del edificio %s: %w", id, err)
	}
	return buildingFrom(row.ID, row.OwnerAccountID, row.RegionID, row.ConcessionID,
		row.BuildingTypeID, row.Footprint, row.Level, row.Status, row.ActiveRecipeID,
		row.ConditionPct, row.FuelStock, row.CreatedAt, row.UpdatedAtSim), nil
}

// ─── Inventario ──────────────────────────────────────────────────────────────

// ListBuildingInventory devuelve el inventario físico del edificio.
func (r *Repo) ListBuildingInventory(ctx context.Context, buildingID uuid.UUID) ([]InventoryItem, error) {
	rows, err := r.q.ListBuildingInventory(ctx, buildingID)
	if err != nil {
		return nil, fmt.Errorf("world/buildings: listando el inventario del edificio %s: %w", buildingID, err)
	}
	out := make([]InventoryItem, len(rows))
	for i, row := range rows {
		out[i] = InventoryItem{
			BuildingID:   row.BuildingID,
			ProductID:    row.ProductID,
			Quantity:     row.Quantity,
			UpdatedAtSim: row.UpdatedAtSim,
		}
	}
	return out, nil
}

// ─── Soporte de ledger (mismo patrón que world/land e internal/contracts) ────

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

// entryAmount es una partida de un asiento del ledger (importe con signo).
type entryAmount struct {
	AccountID uuid.UUID
	Amount    int64
}

// PostLedgerTransaction asienta cabecera + partidas dentro de la transacción SQL
// del Repo (los triggers de 0004 garantizan saldo, no-negatividad y doble
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
		return fmt.Errorf("world/buildings: asentando la cabecera %s de %s: %w", kind, reference, err)
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
			return fmt.Errorf("world/buildings: asentando la partida de %s (cuenta %s): %w", reference, e.AccountID, err)
		}
	}
	return nil
}

// ─── Conversión ──────────────────────────────────────────────────────────────

func buildingFrom(id, owner, region, concession, buildingType uuid.UUID, footprint string, level int32, status sqlcgen.WorldBuildingStatus, activeRecipe *uuid.UUID, condition int32, fuel int64, createdAt time.Time, updatedAtSim int64) Building {
	return Building{
		ID:             id,
		OwnerAccountID: owner,
		RegionID:       region,
		ConcessionID:   concession,
		BuildingTypeID: buildingType,
		Footprint:      footprint,
		Level:          level,
		Status:         string(status),
		ActiveRecipeID: activeRecipe,
		ConditionPct:   condition,
		FuelStock:      fuel,
		CreatedAt:      createdAt,
		UpdatedAtSim:   updatedAtSim,
	}
}

// newUUIDv7 genera un UUIDv7 (ADR-018: los IDs los produce la aplicación).
func newUUIDv7() (uuid.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("world/buildings: generando UUIDv7: %w", err)
	}
	return id, nil
}
