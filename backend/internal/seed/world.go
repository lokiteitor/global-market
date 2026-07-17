package seed

// Mundo mínimo del Incremento 1 (GDD 5.3, Fase 0): una región, dos productos,
// un tipo de almacén y una implantación completa por corporación (concesión de
// suelo → almacén operativo → nodo del grafo logístico). El esquema world lo
// escribe el seed con pgx directo (es la capa de composición; no existe aún un
// módulo Go propietario de world). Toda pieza se localiza por su clave natural
// (name/code o relación con su dueño) antes de crearse: los IDs sembrados son
// estables entre ejecuciones. Geometrías: SRID 0 planar, metros de mundo
// (ADR-019).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lokiteitor/global-market/backend/internal/auth"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// RegionName es la región semilla del Incremento 1.
const RegionName = "Askadia"

// Región Askadia: jurisdicción única de la Fase 0 (GDD 10, ADR-007).
const (
	regionShardKey        = "shard-0"
	regionGridX           = 0
	regionGridY           = 0
	regionSizeM     int64 = 50_000 // lado del cuadrado de la región, en metros
	regionTaxBP           = 500
	regionCustomsBP       = 200
	regionCanonBase int64 = 1_000
	regionBiome           = "plains"
)

// Tipo de edificación warehouse: catálogo mínimo de construcción (GDD 11).
const (
	warehouseTypeCode             = "warehouse"
	warehouseTypeName             = "Almacén"
	warehouseFootprintCells       = 4
	warehouseMaxLevel             = 4
	warehouseBaseStorage    int64 = 100_000
	warehouseBuildCost      int64 = 50_000
	warehouseMaintenance    int64 = 50
)

// Concesiones de suelo del sistema (GDD 11.1): plazo de referencia 90 días de
// juego desde el génesis.
const (
	concessionCanon        int64 = 1_000
	concessionPeriodDays   int64 = 90
	concessionGrantedAtSim int64 = 0
	concessionExpiresAtSim       = concessionPeriodDays * simtime.SimDay
)

// Geometría de la implantación de cada corporación: parcela cuadrada de 1 km
// de lado centrada en su ubicación y almacén de 200 m de lado en el centro
// (el footprint cae dentro de la parcela, y la parcela dentro de la región).
const (
	parcelHalfM    int64 = 500
	footprintHalfM int64 = 100
)

// productSpec describe un producto del catálogo semilla. Dinero y stock en
// int64 punto fijo (unidades menores / unidad mínima), nunca floats.
type productSpec struct {
	code         string
	name         string
	class        string // world.product_class
	unitVolume   int64
	basePrice    int64
	priceFloor   int64
	priceCeiling int64
	isFuel       bool
	initialStock int64 // stock inicial por corporación (físico y contable)
}

// seedProducts es el catálogo del Incremento 1: mineral de hierro y carbón
// (combustible), ambos de demanda básica (GDD 5.6).
var seedProducts = []productSpec{
	{code: "iron_ore", name: "Mineral de hierro", class: "basic", unitVolume: 2,
		basePrice: 100, priceFloor: 20, priceCeiling: 400, initialStock: 5_000},
	{code: "coal", name: "Carbón", class: "basic", unitVolume: 1,
		basePrice: 60, priceFloor: 12, priceCeiling: 240, isFuel: true, initialStock: 3_000},
}

// seededProduct es un producto del catálogo ya persistido.
type seededProduct struct {
	productSpec
	ID uuid.UUID
}

// worldCatalog reúne los IDs del mundo estático sembrado.
type worldCatalog struct {
	RegionID        uuid.UUID
	WarehouseTypeID uuid.UUID
	Products        []seededProduct
}

// productID devuelve el ID del producto sembrado con ese code (false si no está
// en el catálogo mínimo).
func (c worldCatalog) productID(code string) (uuid.UUID, bool) {
	for _, p := range c.Products {
		if p.code == code {
			return p.ID, true
		}
	}
	return uuid.Nil, false
}

// corpSite es la implantación física de una corporación: concesión, almacén
// operativo y nodo del grafo logístico.
type corpSite struct {
	ConcessionID uuid.UUID
	BuildingID   uuid.UUID
	NodeID       uuid.UUID
}

// ensureWorldCatalog garantiza el mundo estático: región, productos y tipo de
// almacén.
func ensureWorldCatalog(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) (worldCatalog, error) {
	regionID, err := ensureRegion(ctx, pool, logger)
	if err != nil {
		return worldCatalog{}, err
	}
	warehouseTypeID, err := ensureWarehouseType(ctx, pool, logger)
	if err != nil {
		return worldCatalog{}, err
	}
	cat := worldCatalog{RegionID: regionID, WarehouseTypeID: warehouseTypeID}
	for _, spec := range seedProducts {
		id, err := ensureProduct(ctx, pool, spec, logger)
		if err != nil {
			return worldCatalog{}, err
		}
		cat.Products = append(cat.Products, seededProduct{productSpec: spec, ID: id})
	}
	return cat, nil
}

// ensureRegion garantiza la región Askadia (clave natural: name).
func ensureRegion(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) (uuid.UUID, error) {
	var id uuid.UUID
	err := pool.QueryRow(ctx, `SELECT id FROM world.regions WHERE name = $1`, RegionName).Scan(&id)
	switch {
	case err == nil:
		logger.Info("región ya existía: omitida", slog.String("region", RegionName))
		return id, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return uuid.Nil, fmt.Errorf("seed: consultando la región %q: %w", RegionName, err)
	}
	id, err = newID()
	if err != nil {
		return uuid.Nil, err
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO world.regions
		       (id, name, grid_x, grid_y, bounds, biome, shard_key,
		        tax_rate_bp, customs_rate_bp, canon_base)
		VALUES ($1, $2, $3, $4, ST_GeomFromText($5, 0), $6::world.biome, $7, $8, $9, $10)`,
		id, RegionName, regionGridX, regionGridY,
		rectWKT(0, 0, regionSizeM, regionSizeM), regionBiome, regionShardKey,
		regionTaxBP, regionCustomsBP, regionCanonBase); err != nil {
		return uuid.Nil, fmt.Errorf("seed: creando la región %q: %w", RegionName, err)
	}
	logger.Info("región creada",
		slog.String("region", RegionName),
		slog.String("region_id", id.String()),
		slog.String("shard_key", regionShardKey))
	return id, nil
}

// ensureProduct garantiza un producto del catálogo (clave natural: code).
func ensureProduct(ctx context.Context, pool *pgxpool.Pool, spec productSpec, logger *slog.Logger) (uuid.UUID, error) {
	var id uuid.UUID
	err := pool.QueryRow(ctx, `SELECT id FROM world.products WHERE code = $1`, spec.code).Scan(&id)
	switch {
	case err == nil:
		logger.Info("producto ya existía: omitido", slog.String("product", spec.code))
		return id, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return uuid.Nil, fmt.Errorf("seed: consultando el producto %q: %w", spec.code, err)
	}
	id, err = newID()
	if err != nil {
		return uuid.Nil, err
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO world.products
		       (id, code, name, class, unit_volume, base_price,
		        price_floor, price_ceiling, is_fuel)
		VALUES ($1, $2, $3, $4::world.product_class, $5, $6, $7, $8, $9)`,
		id, spec.code, spec.name, spec.class, spec.unitVolume, spec.basePrice,
		spec.priceFloor, spec.priceCeiling, spec.isFuel); err != nil {
		return uuid.Nil, fmt.Errorf("seed: creando el producto %q: %w", spec.code, err)
	}
	logger.Info("producto creado",
		slog.String("product", spec.code),
		slog.String("product_id", id.String()),
		slog.Int64("base_price", spec.basePrice),
		slog.Bool("is_fuel", spec.isFuel))
	return id, nil
}

// ensureWarehouseType garantiza el tipo de edificación warehouse (clave
// natural: code).
func ensureWarehouseType(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) (uuid.UUID, error) {
	var id uuid.UUID
	err := pool.QueryRow(ctx, `SELECT id FROM world.building_types WHERE code = $1`, warehouseTypeCode).Scan(&id)
	switch {
	case err == nil:
		logger.Info("tipo de edificio ya existía: omitido", slog.String("building_type", warehouseTypeCode))
		return id, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return uuid.Nil, fmt.Errorf("seed: consultando el tipo de edificio %q: %w", warehouseTypeCode, err)
	}
	id, err = newID()
	if err != nil {
		return uuid.Nil, err
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO world.building_types
		       (id, code, name, footprint_cells, max_level, base_storage,
		        placement_rules, level_curve, build_cost, maintenance_cost)
		VALUES ($1, $2, $3, $4, $5, $6, '{}'::jsonb, '{}'::jsonb, $7, $8)`,
		id, warehouseTypeCode, warehouseTypeName, warehouseFootprintCells,
		warehouseMaxLevel, warehouseBaseStorage, warehouseBuildCost,
		warehouseMaintenance); err != nil {
		return uuid.Nil, fmt.Errorf("seed: creando el tipo de edificio %q: %w", warehouseTypeCode, err)
	}
	logger.Info("tipo de edificio creado",
		slog.String("building_type", warehouseTypeCode),
		slog.String("building_type_id", id.String()))
	return id, nil
}

// ensureCorpSite garantiza la implantación física de una corporación centrada
// en (centerX, centerY): concesión de suelo activa, almacén operativo sobre
// ella y nodo warehouse del grafo logístico ligado al edificio.
func ensureCorpSite(ctx context.Context, pool *pgxpool.Pool, cat worldCatalog, corp auth.Account, centerX, centerY int64, logger *slog.Logger) (corpSite, error) {
	concessionID, err := ensureConcession(ctx, pool, cat.RegionID, corp, centerX, centerY, logger)
	if err != nil {
		return corpSite{}, err
	}
	buildingID, err := ensureWarehouseBuilding(ctx, pool, cat, corp, concessionID, centerX, centerY, logger)
	if err != nil {
		return corpSite{}, err
	}
	nodeID, err := ensureWarehouseNode(ctx, pool, cat.RegionID, corp, buildingID, centerX, centerY, logger)
	if err != nil {
		return corpSite{}, err
	}
	return corpSite{ConcessionID: concessionID, BuildingID: buildingID, NodeID: nodeID}, nil
}

// ensureConcession garantiza la concesión de suelo de la corporación en la
// región (clave de idempotencia: una concesión no revertida por titular y
// región en el mundo semilla).
func ensureConcession(ctx context.Context, pool *pgxpool.Pool, regionID uuid.UUID, corp auth.Account, centerX, centerY int64, logger *slog.Logger) (uuid.UUID, error) {
	var id uuid.UUID
	err := pool.QueryRow(ctx, `
		SELECT id FROM world.land_concessions
		 WHERE holder_account_id = $1 AND region_id = $2 AND status <> 'reverted'
		 LIMIT 1`, corp.ID, regionID).Scan(&id)
	switch {
	case err == nil:
		logger.Info("concesión ya existía: omitida", slog.String("account", corp.Name))
		return id, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return uuid.Nil, fmt.Errorf("seed: consultando la concesión de %s: %w", corp.Name, err)
	}
	id, err = newID()
	if err != nil {
		return uuid.Nil, err
	}
	parcel := rectWKT(centerX-parcelHalfM, centerY-parcelHalfM, centerX+parcelHalfM, centerY+parcelHalfM)
	if _, err := pool.Exec(ctx, `
		INSERT INTO world.land_concessions
		       (id, region_id, holder_account_id, parcel, canon_amount,
		        period_sim_days, expires_at_sim, status, granted_at_sim)
		VALUES ($1, $2, $3, ST_GeomFromText($4, 0), $5, $6, $7, 'active', $8)`,
		id, regionID, corp.ID, parcel, concessionCanon, concessionPeriodDays,
		concessionExpiresAtSim, concessionGrantedAtSim); err != nil {
		return uuid.Nil, fmt.Errorf("seed: creando la concesión de %s: %w", corp.Name, err)
	}
	logger.Info("concesión creada",
		slog.String("account", corp.Name),
		slog.String("concession_id", id.String()),
		slog.Int64("canon", concessionCanon),
		slog.Int64("expires_at_sim", concessionExpiresAtSim))
	return id, nil
}

// ensureWarehouseBuilding garantiza el almacén operativo de la corporación
// sobre su concesión (clave de idempotencia: un edificio por dueño y tipo en
// el mundo semilla).
func ensureWarehouseBuilding(ctx context.Context, pool *pgxpool.Pool, cat worldCatalog, corp auth.Account, concessionID uuid.UUID, centerX, centerY int64, logger *slog.Logger) (uuid.UUID, error) {
	var id uuid.UUID
	err := pool.QueryRow(ctx, `
		SELECT id FROM world.buildings
		 WHERE owner_account_id = $1 AND building_type_id = $2
		 LIMIT 1`, corp.ID, cat.WarehouseTypeID).Scan(&id)
	switch {
	case err == nil:
		logger.Info("almacén ya existía: omitido", slog.String("account", corp.Name))
		return id, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return uuid.Nil, fmt.Errorf("seed: consultando el almacén de %s: %w", corp.Name, err)
	}
	id, err = newID()
	if err != nil {
		return uuid.Nil, err
	}
	footprint := rectWKT(centerX-footprintHalfM, centerY-footprintHalfM, centerX+footprintHalfM, centerY+footprintHalfM)
	if _, err := pool.Exec(ctx, `
		INSERT INTO world.buildings
		       (id, owner_account_id, region_id, concession_id,
		        building_type_id, footprint, level, status)
		VALUES ($1, $2, $3, $4, $5, ST_GeomFromText($6, 0), 1, 'operational')`,
		id, corp.ID, cat.RegionID, concessionID, cat.WarehouseTypeID, footprint); err != nil {
		return uuid.Nil, fmt.Errorf("seed: creando el almacén de %s: %w", corp.Name, err)
	}
	logger.Info("almacén creado (operational)",
		slog.String("account", corp.Name),
		slog.String("building_id", id.String()))
	return id, nil
}

// ensureWarehouseNode garantiza el nodo warehouse del grafo logístico ligado
// al almacén (clave de idempotencia: un nodo warehouse por edificio).
func ensureWarehouseNode(ctx context.Context, pool *pgxpool.Pool, regionID uuid.UUID, corp auth.Account, buildingID uuid.UUID, centerX, centerY int64, logger *slog.Logger) (uuid.UUID, error) {
	var id uuid.UUID
	err := pool.QueryRow(ctx, `
		SELECT id FROM world.network_nodes
		 WHERE building_id = $1 AND kind = 'warehouse'
		 LIMIT 1`, buildingID).Scan(&id)
	switch {
	case err == nil:
		logger.Info("nodo logístico ya existía: omitido", slog.String("account", corp.Name))
		return id, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return uuid.Nil, fmt.Errorf("seed: consultando el nodo del almacén de %s: %w", corp.Name, err)
	}
	id, err = newID()
	if err != nil {
		return uuid.Nil, err
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO world.network_nodes (id, kind, region_id, building_id, location)
		VALUES ($1, 'warehouse', $2, $3, ST_GeomFromText($4, 0))`,
		id, regionID, buildingID, pointWKT(centerX, centerY)); err != nil {
		return uuid.Nil, fmt.Errorf("seed: creando el nodo del almacén de %s: %w", corp.Name, err)
	}
	logger.Info("nodo logístico creado",
		slog.String("account", corp.Name),
		slog.String("node_id", id.String()),
		slog.Int64("x", centerX), slog.Int64("y", centerY))
	return id, nil
}

// rectWKT construye el WKT de un rectángulo cerrado (anillo CCW) en metros de
// mundo (SRID 0 planar, ADR-019).
func rectWKT(minX, minY, maxX, maxY int64) string {
	return fmt.Sprintf("POLYGON((%d %d,%d %d,%d %d,%d %d,%d %d))",
		minX, minY, maxX, minY, maxX, maxY, minX, maxY, minX, minY)
}

// pointWKT construye el WKT de un punto en metros de mundo.
func pointWKT(x, y int64) string {
	return fmt.Sprintf("POINT(%d %d)", x, y)
}

// newID genera un UUIDv7: los IDs los produce la aplicación (ADR-018).
func newID() (uuid.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("seed: generando UUIDv7: %w", err)
	}
	return id, nil
}
