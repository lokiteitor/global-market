package seed

// Red vial de Askadia (Incremento 3, LOGÍSTICA FÍSICA, Fase 1: terrestre). Sobre
// el mundo mínimo ya sembrado (región, almacenes y sus nodos del grafo) levanta
// una RED VIAL CONEXA: un nodo junction central y enlaces road BIDIRECCIONALES
// (el grafo es dirigido: un enlace por sentido) que unen warehouse(Demo) —
// junction — warehouse(Norte), cada uno con su único link_segment (Fase 1: 1
// segmento por enlace, congestión EMA fluida 1.0). Además siembra el catálogo de
// vehículos terrestres (truck_small, truck_large). Así los contratos de compra
// cross-node (origen ≠ destino) pueden EJECUTARSE con transporte físico real
// desde el primer día (GDD 7 logística, 8 flotas; ADR-019 SRID 0 planar).
//
// Todo es idempotente por clave natural (junction: región+kind+ubicación; enlace:
// (from, to, mode); segmento: (link, seq); vehículo: code): re-ejecutar el seed
// nunca duplica y los IDs sembrados son estables. Geometrías: SRID 0 planar,
// metros de mundo (ADR-019).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Ubicaciones de la implantación de cada corporación (centros de sus parcelas) y
// del junction central. Son las MISMAS que usa el reparto de placements de
// seed.go: la geometría de nodos y enlaces debe ser coherente (la posición de un
// vehículo se interpola sobre la geometría del segmento, GDD 1.1).
const (
	demoCenterX  int64 = 10_000
	demoCenterY  int64 = 10_000
	norteCenterX int64 = 30_000
	norteCenterY int64 = 30_000
	// Junction central: punto medio entre Demo y Norte (nodo de tránsito puro,
	// sin almacén) que hace conexa la red terrestre.
	junctionX int64 = 20_000
	junctionY int64 = 20_000
)

// Parámetros de los enlaces road del seed (GDD 7): capacidad y velocidad base
// realistas de una vía terrestre de Fase 1.
const (
	roadCapacityPerHour int32 = 60
	roadBaseSpeedKmh    int32 = 80
)

// Catálogo de vehículos terrestres del Incremento 3 (Fase 1). El combustible es
// coal (único producto combustible del mundo mínimo): fuel_product = coal.
type vehicleTypeSpec struct {
	code                string
	name                string
	mode                string // world.link_mode
	cargoCapacity       int64  // en unidades de volumen logístico
	speedKmh            int32
	fuelPer100km        int64
	autonomyKm          int32
	purchasePrice       int64
	operatingCostPerDay int64
}

// seedVehicleTypes es el catálogo terrestre semilla: un camión ligero y uno
// pesado (GDD 8). Ambos consumen coal como combustible.
var seedVehicleTypes = []vehicleTypeSpec{
	{code: "truck_small", name: "Camión ligero", mode: "road", cargoCapacity: 2_000,
		speedKmh: 80, fuelPer100km: 20, autonomyKm: 800, purchasePrice: 40_000, operatingCostPerDay: 100},
	{code: "truck_large", name: "Camión pesado", mode: "road", cargoCapacity: 6_000,
		speedKmh: 70, fuelPer100km: 35, autonomyKm: 1_000, purchasePrice: 90_000, operatingCostPerDay: 200},
}

// ensureLogisticsNetwork garantiza la red vial conexa (junction + enlaces road
// bidireccionales con sus segmentos) y el catálogo de vehículos terrestres.
func ensureLogisticsNetwork(ctx context.Context, pool *pgxpool.Pool, cat worldCatalog, demoSite, norteSite corpSite, logger *slog.Logger) error {
	coalID, ok := cat.productID("coal")
	if !ok {
		return errors.New("seed: falta el producto coal del mundo mínimo (combustible de la flota)")
	}

	junctionID, err := ensureJunctionNode(ctx, pool, cat.RegionID, junctionX, junctionY, logger)
	if err != nil {
		return err
	}

	// Enlaces road BIDIRECCIONALES (un enlace dirigido por sentido) con su
	// segmento: warehouse(Demo) ⇄ junction ⇄ warehouse(Norte).
	segments := []struct {
		fromID       uuid.UUID
		fromX, fromY int64
		toID         uuid.UUID
		toX, toY     int64
	}{
		{demoSite.NodeID, demoCenterX, demoCenterY, junctionID, junctionX, junctionY},
		{junctionID, junctionX, junctionY, demoSite.NodeID, demoCenterX, demoCenterY},
		{junctionID, junctionX, junctionY, norteSite.NodeID, norteCenterX, norteCenterY},
		{norteSite.NodeID, norteCenterX, norteCenterY, junctionID, junctionX, junctionY},
	}
	for _, s := range segments {
		if err := ensureRoadLink(ctx, pool, cat.RegionID, s.fromID, s.fromX, s.fromY, s.toID, s.toX, s.toY, logger); err != nil {
			return err
		}
	}

	for _, spec := range seedVehicleTypes {
		if err := ensureVehicleType(ctx, pool, spec, coalID, logger); err != nil {
			return err
		}
	}

	logger.Info("red vial de Askadia garantizada (junction + enlaces road bidireccionales + flota)")
	return nil
}

// ensureJunctionNode garantiza el nodo junction central (clave natural: región +
// kind junction + ubicación exacta).
func ensureJunctionNode(ctx context.Context, pool *pgxpool.Pool, regionID uuid.UUID, x, y int64, logger *slog.Logger) (uuid.UUID, error) {
	var id uuid.UUID
	err := pool.QueryRow(ctx, `
		SELECT id FROM world.network_nodes
		 WHERE region_id = $1 AND kind = 'junction'
		   AND ST_Equals(location, ST_GeomFromText($2, 0))
		 LIMIT 1`, regionID, pointWKT(x, y)).Scan(&id)
	switch {
	case err == nil:
		logger.Info("nodo junction ya existía: omitido", slog.String("node_id", id.String()))
		return id, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return uuid.Nil, fmt.Errorf("seed: consultando el nodo junction: %w", err)
	}
	id, err = newID()
	if err != nil {
		return uuid.Nil, err
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO world.network_nodes (id, kind, region_id, location)
		VALUES ($1, 'junction', $2, ST_GeomFromText($3, 0))`,
		id, regionID, pointWKT(x, y)); err != nil {
		return uuid.Nil, fmt.Errorf("seed: creando el nodo junction: %w", err)
	}
	logger.Info("nodo junction creado",
		slog.String("node_id", id.String()), slog.Int64("x", x), slog.Int64("y", y))
	return id, nil
}

// ensureRoadLink garantiza UN enlace road dirigido from→to (clave natural:
// (from_node_id, to_node_id, mode)) con su único link_segment (seq 1, región,
// congestión EMA fluida 1.0). length_m = distancia euclídea (coherente con la
// geometría del trazado, ADR-019).
func ensureRoadLink(ctx context.Context, pool *pgxpool.Pool, regionID, fromID uuid.UUID, fromX, fromY int64, toID uuid.UUID, toX, toY int64, logger *slog.Logger) error {
	var linkID uuid.UUID
	err := pool.QueryRow(ctx, `
		SELECT id FROM world.network_links
		 WHERE from_node_id = $1 AND to_node_id = $2 AND mode = 'road'
		 LIMIT 1`, fromID, toID).Scan(&linkID)
	switch {
	case err == nil:
		logger.Info("enlace road ya existía: omitido", slog.String("link_id", linkID.String()))
		return nil
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("seed: consultando el enlace road %s→%s: %w", fromID, toID, err)
	}

	lengthM := euclideanM(fromX, fromY, toX, toY)
	path := lineWKT(fromX, fromY, toX, toY)

	linkID, err = newID()
	if err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO world.network_links
		       (id, mode, from_node_id, to_node_id, path, length_m, capacity_per_hour, base_speed_kmh)
		VALUES ($1, 'road', $2, $3, ST_GeomFromText($4, 0), $5, $6, $7)`,
		linkID, fromID, toID, path, lengthM, roadCapacityPerHour, roadBaseSpeedKmh); err != nil {
		return fmt.Errorf("seed: creando el enlace road %s→%s: %w", fromID, toID, err)
	}

	segID, err := newID()
	if err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO world.link_segments (id, link_id, region_id, seq, portion, length_m, congestion_ema)
		VALUES ($1, $2, $3, 1, ST_GeomFromText($4, 0), $5, 1.0)`,
		segID, linkID, regionID, path, lengthM); err != nil {
		return fmt.Errorf("seed: creando el segmento del enlace road %s: %w", linkID, err)
	}
	logger.Info("enlace road creado (con segmento)",
		slog.String("link_id", linkID.String()), slog.String("segment_id", segID.String()),
		slog.Int64("length_m", lengthM))
	return nil
}

// ensureVehicleType garantiza un tipo de vehículo del catálogo (clave natural:
// code, UNIQUE).
func ensureVehicleType(ctx context.Context, pool *pgxpool.Pool, spec vehicleTypeSpec, fuelProductID uuid.UUID, logger *slog.Logger) error {
	var id uuid.UUID
	err := pool.QueryRow(ctx, `SELECT id FROM world.vehicle_types WHERE code = $1`, spec.code).Scan(&id)
	switch {
	case err == nil:
		logger.Info("tipo de vehículo ya existía: omitido", slog.String("vehicle_type", spec.code))
		return nil
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("seed: consultando el tipo de vehículo %q: %w", spec.code, err)
	}
	id, err = newID()
	if err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO world.vehicle_types
		       (id, code, name, mode, cargo_capacity, speed_kmh, fuel_product_id,
		        fuel_per_100km, autonomy_km, purchase_price, operating_cost_per_day)
		VALUES ($1, $2, $3, $4::world.link_mode, $5, $6, $7, $8, $9, $10, $11)`,
		id, spec.code, spec.name, spec.mode, spec.cargoCapacity, spec.speedKmh, fuelProductID,
		spec.fuelPer100km, spec.autonomyKm, spec.purchasePrice, spec.operatingCostPerDay); err != nil {
		return fmt.Errorf("seed: creando el tipo de vehículo %q: %w", spec.code, err)
	}
	logger.Info("tipo de vehículo creado",
		slog.String("vehicle_type", spec.code), slog.String("vehicle_type_id", id.String()),
		slog.Int64("cargo_capacity", spec.cargoCapacity), slog.Int64("purchase_price", spec.purchasePrice))
	return nil
}

// euclideanM devuelve la distancia euclídea entre dos puntos en metros de mundo,
// redondeada al entero más cercano y con suelo 1 (length_m > 0 exigido por la BD).
func euclideanM(x1, y1, x2, y2 int64) int64 {
	dx := float64(x2 - x1)
	dy := float64(y2 - y1)
	d := int64(math.Round(math.Hypot(dx, dy)))
	if d < 1 {
		d = 1
	}
	return d
}

// lineWKT construye el WKT de un segmento recto entre dos puntos (metros de
// mundo, SRID 0 planar, ADR-019).
func lineWKT(x1, y1, x2, y2 int64) string {
	return fmt.Sprintf("LINESTRING(%d %d,%d %d)", x1, y1, x2, y2)
}
