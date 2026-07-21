package seed

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Catálogo de la red eléctrica regional (GDD 5.8 Fase 3, ADR-025): centrales
// construibles y la PRIMERA receta eléctrica del juego. La convivencia con el
// combustible in situ es por RECETA (ADR-025 §1): ninguna receta existente
// cambia; smelt_steel_electric es la alternativa opt-in del alto horno (el
// jugador elige la receta activa — combustible vs. red — y ambas conviven).

// Central térmica de carbón: quema combustible físico por unidad de energía
// despachada (sin combustible no despacha, GDD 5.8). A nivel 1 genera
// coalPlantCapacity unidades por hora-sim (una cubre exactamente un alto horno
// eléctrico); el nivel multiplica por level_curve.capacity_mult.
const (
	coalPlantCode        = "coal_power_plant"
	coalPlantName        = "Central térmica de carbón"
	coalPlantFootprint   = 4
	coalPlantMaxLevel    = 4
	coalPlantBaseStorage = 20_000 // almacén local de carbón (llega por logística)
	coalPlantBuildCost   = 120_000
	coalPlantMaintenance = 150
	coalPlantCapacity    = 10 // unidades de energía por hora-sim (nivel 1)
	coalPlantFuelPerUnit = 1  // carbón por unidad de energía despachada
)

// Central hidroeléctrica: sin combustible (coste marginal ~0, abre el orden de
// mérito); emplazamiento restringido a regiones con agua (GDD 5.8 "ríos/agua"
// se materializa hoy como bioma coast; los ríos del worldgen siguen
// pendientes — ADR-025 §5).
const (
	hydroPlantCode        = "hydro_power_plant"
	hydroPlantName        = "Central hidroeléctrica"
	hydroPlantFootprint   = 6
	hydroPlantMaxLevel    = 4
	hydroPlantBaseStorage = 0
	hydroPlantBuildCost   = 200_000
	hydroPlantMaintenance = 100
	hydroPlantCapacity    = 6
	hydroPlantRules       = `{"requires_biome":["coast"]}`
)

// powerLevelCurve añade capacity_mult a la curva industrial estándar.
const powerLevelCurve = `{"capacity_mult":[1,2,3,4],"storage_mult":[1,2,3,4],"upgrade_cost_factor":[1,2,4,8]}`

// Receta eléctrica smelt_steel_electric (alto horno, ADR-025): mismos insumos
// y salidas que smelt_steel pero SIN combustible — consume
// smeltElectricPowerPerHour unidades de energía por hora-sim del pool regional
// mientras el lote está activo.
const (
	smeltElectricCode         = "smelt_steel_electric"
	smeltElectricName         = "Fundición de acero (horno eléctrico)"
	smeltElectricBatchSeconds = 7_200
	smeltElectricPowerPerHour = 10
	smeltElectricWorkers      = 5
	smeltElectricInputQty     = 20
	smeltElectricOutputQty    = 8
)

// ensurePowerCatalog garantiza el catálogo eléctrico (idempotente por claves
// naturales, como el resto del seed).
func ensurePowerCatalog(ctx context.Context, pool *pgxpool.Pool, cat worldCatalog, logger *slog.Logger) error {
	coalID, ok := cat.productID("coal")
	if !ok {
		return errors.New("seed: falta el producto coal del mundo mínimo")
	}
	ironOreID, ok := cat.productID("iron_ore")
	if !ok {
		return errors.New("seed: falta el producto iron_ore del mundo mínimo")
	}
	var steelID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM world.products WHERE code = $1`, steelIngotCode).Scan(&steelID); err != nil {
		return fmt.Errorf("seed: falta el producto %s del mundo industrial: %w", steelIngotCode, err)
	}

	// (a) Tipos de central con sus parámetros de generación.
	coalPlantTypeID, err := ensureBuildingType(ctx, pool, buildingTypeSpec{
		code: coalPlantCode, name: coalPlantName, footprintCells: coalPlantFootprint,
		maxLevel: coalPlantMaxLevel, baseStorage: coalPlantBaseStorage,
		placementRules: "{}", levelCurve: powerLevelCurve,
		buildCost: coalPlantBuildCost, maintenanceCost: coalPlantMaintenance,
	}, logger)
	if err != nil {
		return err
	}
	if err := ensurePowerPlantType(ctx, pool, coalPlantTypeID, coalPlantCapacity, &coalID, coalPlantFuelPerUnit, logger, coalPlantCode); err != nil {
		return err
	}

	hydroPlantTypeID, err := ensureBuildingType(ctx, pool, buildingTypeSpec{
		code: hydroPlantCode, name: hydroPlantName, footprintCells: hydroPlantFootprint,
		maxLevel: hydroPlantMaxLevel, baseStorage: hydroPlantBaseStorage,
		placementRules: hydroPlantRules, levelCurve: powerLevelCurve,
		buildCost: hydroPlantBuildCost, maintenanceCost: hydroPlantMaintenance,
	}, logger)
	if err != nil {
		return err
	}
	if err := ensurePowerPlantType(ctx, pool, hydroPlantTypeID, hydroPlantCapacity, nil, 0, logger, hydroPlantCode); err != nil {
		return err
	}

	// (b) Receta eléctrica del alto horno (opt-in del jugador; la de combustible
	// no cambia).
	var blastFurnaceTypeID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM world.building_types WHERE code = $1`, blastFurnaceCode).Scan(&blastFurnaceTypeID); err != nil {
		return fmt.Errorf("seed: falta el tipo %s del mundo industrial: %w", blastFurnaceCode, err)
	}
	return ensureRecipe(ctx, pool, recipeSpec{
		code: smeltElectricCode, name: smeltElectricName, buildingTypeID: blastFurnaceTypeID,
		batchSimSeconds: smeltElectricBatchSeconds, powerPerHour: smeltElectricPowerPerHour,
		workersRequired: smeltElectricWorkers,
		ingredients: []ingredientSpec{
			{productID: ironOreID, role: "input", quantity: smeltElectricInputQty},
			{productID: steelID, role: "output", quantity: smeltElectricOutputQty},
		},
	}, logger)
}

// ensurePowerPlantType garantiza los parámetros de generación de un tipo
// (clave natural: building_type_id).
func ensurePowerPlantType(ctx context.Context, pool *pgxpool.Pool, buildingTypeID uuid.UUID, capacity int64, fuelProductID *uuid.UUID, fuelPerUnit int64, logger *slog.Logger, code string) error {
	var existing uuid.UUID
	err := pool.QueryRow(ctx, `SELECT building_type_id FROM world.power_plant_types WHERE building_type_id = $1`, buildingTypeID).Scan(&existing)
	switch {
	case err == nil:
		logger.Info("parámetros de central ya existían: omitidos", slog.String("building_type", code))
		return nil
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("seed: consultando los parámetros de central de %q: %w", code, err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO world.power_plant_types (building_type_id, capacity, fuel_product_id, fuel_per_unit)
		VALUES ($1, $2, $3, $4)`,
		buildingTypeID, capacity, fuelProductID, fuelPerUnit); err != nil {
		return fmt.Errorf("seed: creando los parámetros de central de %q: %w", code, err)
	}
	logger.Info("parámetros de central creados",
		slog.String("building_type", code),
		slog.Int64("capacity_per_hour", capacity),
		slog.Int64("fuel_per_unit", fuelPerUnit))
	return nil
}
