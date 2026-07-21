package seed

// Mundo industrial del Incremento 2 (GDD 6 industrial, 10 recursos): extiende el
// mundo mínimo del Incremento 1 con el catálogo de producción que cierra el lazo
// construir→producir→vender —un producto manufacturado (steel_ingot), los tipos
// de instalación extractiva (iron_mine) y de manufactura (blast_furnace), sus
// recetas (mine_iron, smelt_steel), una ciudad consumidora con su curva de
// demanda (Nueva Askadia) y un yacimiento finito de iron_ore— junto a una parcela
// libre reservada para levantar una mina cerca de él. El Incremento 4 añade la
// cadena del carbón que necesitan los bots (coal_mine, mine_coal y un yacimiento
// de coal en suelo libre): sin ella nadie puede producir el combustible del que
// dependen mine_iron y smelt_steel. Todo se localiza por su clave natural
// (code/name o relación con su dueño) antes de crearse: los datos sembrados son
// estables entre ejecuciones y re-ejecutar el seed nunca los duplica.
// Geometrías: SRID 0 planar, metros de mundo (ADR-019).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lokiteitor/global-market/backend/internal/auth"
)

// Producto manufacturado del Incremento 2 (catálogo, sin stock inicial por
// corporación: nace en el mundo cuando un alto horno lo funde).
const (
	steelIngotCode             = "steel_ingot"
	steelIngotName             = "Lingote de acero"
	steelIngotClass            = "basic"
	steelIngotUnitVolume int64 = 3
	steelIngotBasePrice  int64 = 400
	steelIngotPriceFloor int64 = 80
	steelIngotCeiling    int64 = 1_600
)

// Tipo de instalación extractiva: la mina de hierro extrae de un yacimiento
// cercano (regla near_resource). build_cost/maintenance van al sink (GDD 5.7/11).
const (
	ironMineCode                 = "iron_mine"
	ironMineName                 = "Mina de hierro"
	ironMineFootprintCells       = 4
	ironMineMaxLevel             = 4
	ironMineBaseStorage    int64 = 50_000
	ironMineBuildCost      int64 = 80_000
	ironMineMaintenance    int64 = 100
)

// Tipo de instalación de manufactura: el alto horno funde iron_ore en
// steel_ingot consumiendo combustible (coal) in situ (GDD 5.8).
const (
	blastFurnaceCode                 = "blast_furnace"
	blastFurnaceName                 = "Alto horno"
	blastFurnaceFootprintCells       = 4
	blastFurnaceMaxLevel             = 4
	blastFurnaceBaseStorage    int64 = 40_000
	blastFurnaceBuildCost      int64 = 150_000
	blastFurnaceMaintenance    int64 = 200
)

// Tipo de instalación extractiva de carbón (Incremento 4, cadena del carbón):
// más barata de levantar y mantener que la mina de hierro para que un bot
// productor con capital semilla pueda arrancarla pronto (GDD 13/15.4).
const (
	coalMineCode                 = "coal_mine"
	coalMineName                 = "Mina de carbón"
	coalMineFootprintCells       = 4
	coalMineMaxLevel             = 4
	coalMineBaseStorage    int64 = 50_000
	coalMineBuildCost      int64 = 60_000
	coalMineMaintenance    int64 = 80
)

// curva de nivel común a los dos tipos (índice nivel-1): líneas paralelas,
// factores de velocidad y almacén crecientes, y el factor de coste de mejora no
// lineal por nivel destino (GDD 6.3; convención de world/production y
// world/buildings).
const industrialLevelCurve = `{"lines":[1,2,4,8],"speed_mult":[1,2,3,4],"storage_mult":[1,2,3,4],"upgrade_cost_factor":[1,2,4,8]}`

// placement_rules de la mina: exige un yacimiento de iron_ore con existencias
// dentro de ironMineMaxDistanceM del centroide de su footprint.
const ironMinePlacementRules = `{"near_resource":"iron_ore","max_distance_m":8000}`

// placement_rules de la mina de carbón: misma regla near_resource que la de
// hierro, sobre un yacimiento de coal.
const coalMinePlacementRules = `{"near_resource":"coal","max_distance_m":8000}`

// Receta extractiva mine_iron (mina de hierro): un lote de 3600 s de sim consume
// 5 de coal y 3 trabajadores para extraer 50 de iron_ore del yacimiento.
const (
	mineIronCode            = "mine_iron"
	mineIronName            = "Extracción de mineral de hierro"
	mineIronBatchSimSeconds = 3_600
	mineIronFuelPerBatch    = 5
	mineIronWorkers         = 3
	mineIronOutputQty       = 50
)

// Receta extractiva mine_coal (mina de carbón, Incremento 4): un lote de 3600 s
// de sim con 3 trabajadores extrae 60 de coal del yacimiento. SIN combustible
// (fuel_product NULL, fuel_per_batch 0): decisión de arranque v1 — extraer
// carbón no consume carbón, para romper la circularidad de bootstrap (nadie
// podría producir el primer coal si producirlo ya exigiera coal).
const (
	mineCoalCode            = "mine_coal"
	mineCoalName            = "Extracción de carbón"
	mineCoalBatchSimSeconds = 3_600
	mineCoalWorkers         = 3
	mineCoalOutputQty       = 60
)

// Receta de manufactura smelt_steel (alto horno): un lote de 7200 s de sim
// consume 10 de coal, 5 trabajadores y 20 de iron_ore para producir 8 de
// steel_ingot.
const (
	smeltSteelCode            = "smelt_steel"
	smeltSteelName            = "Fundición de acero"
	smeltSteelBatchSimSeconds = 7_200
	smeltSteelFuelPerBatch    = 10
	smeltSteelWorkers         = 5
	smeltSteelInputQty        = 20
	smeltSteelOutputQty       = 8
)

// Ciudad consumidora Nueva Askadia (GDD 5.6): agente de mercado (cuenta 'city')
// con su curva de demanda por producto. Su base_salary alimenta el coste laboral
// de la producción cercana (GDD 5.7).
const (
	cityName                   = "Nueva Askadia"
	cityLevel                  = 2
	cityPopulation             = 50_000
	cityInfluenceRadiusM       = 20_000
	cityBaseSalary       int64 = 30
	cityLocX             int64 = 20_000
	cityLocY             int64 = 22_000
	// citySupplyIndex es el índice de suministro histórico inicial: un mundo
	// sembrado representa una ciudad YA desarrollada, así que su índice debe ser
	// COHERENTE con su nivel para que el Balancer no la degrade de inmediato. Se
	// sitúa en la banda estable del nivel 2 bajo el II_CITY_LEVELUP_INDEX_BASE por
	// defecto (100000): umbral de bajada base*(nivel-1)=100000, umbral de subida
	// base*nivel=200000 → 150000 la mantiene estable (GDD 5.6, Incremento 6b).
	citySupplyIndex int64 = 150_000
)

// Zona industrial libre: el yacimiento de iron_ore se sitúa aquí, en suelo sin
// concesión ni edificio del seed, reservado para que un jugador levante una mina
// cerca (E2E del Incremento 2).
const (
	ironDepositX       int64 = 20_000
	ironDepositY       int64 = 20_000
	ironDepositInitial int64 = 1_000_000
)

// Yacimiento de coal (Incremento 4): en una zona LIBRE de Askadia, lejos de las
// parcelas sembradas (Demo (10k,10k), Norte (30k,30k), ciudad (20k,22k),
// yacimiento de hierro (20k,20k), junction (20k,20k)) y con >8 km de suelo libre
// alrededor dentro de la región (50 km × 50 km), de modo que un bot pueda tomar
// una parcela libre a <8000 m (coalMinePlacementRules) y levantar su coal_mine.
const (
	coalDepositX       int64 = 40_000
	coalDepositY       int64 = 15_000
	coalDepositInitial int64 = 2_000_000
)

// Demanda urbana por producto (city_demand): d0 razonable, supply_ema > 0 (suelo
// exigido por la BD) y current_price dentro de los clamps del producto.
const (
	demandIronD0            int64 = 1_000
	demandIronCurrentPrice  int64 = 100 // dentro de [20, 400]
	demandSteelD0           int64 = 500
	demandSteelCurrentPrice int64 = 400 // dentro de [80, 1600]
	demandSupplyEMA               = 1.0 // > 0 (arranque; el Balancer lo actualiza)
	demandSaturation              = 1.0
)

// ensureIndustrialWorld garantiza el mundo industrial del Incremento 2 sobre el
// mundo mínimo ya sembrado (cat). Cada elemento es idempotente por clave natural.
func ensureIndustrialWorld(ctx context.Context, pool *pgxpool.Pool, repo *auth.PGRepository, cat worldCatalog, logger *slog.Logger) error {
	ironOreID, ok := cat.productID("iron_ore")
	if !ok {
		return errors.New("seed: falta el producto iron_ore del mundo mínimo")
	}
	coalID, ok := cat.productID("coal")
	if !ok {
		return errors.New("seed: falta el producto coal del mundo mínimo")
	}

	// (a) Producto manufacturado steel_ingot (catálogo; sin stock inicial).
	steelID, err := ensureProduct(ctx, pool, productSpec{
		code: steelIngotCode, name: steelIngotName, class: steelIngotClass,
		unitVolume: steelIngotUnitVolume, basePrice: steelIngotBasePrice,
		priceFloor: steelIngotPriceFloor, priceCeiling: steelIngotCeiling,
	}, logger)
	if err != nil {
		return err
	}

	// (b) Tipos de instalación: mina extractiva y alto horno.
	ironMineTypeID, err := ensureBuildingType(ctx, pool, buildingTypeSpec{
		code: ironMineCode, name: ironMineName, footprintCells: ironMineFootprintCells,
		maxLevel: ironMineMaxLevel, baseStorage: ironMineBaseStorage,
		placementRules: ironMinePlacementRules, levelCurve: industrialLevelCurve,
		buildCost: ironMineBuildCost, maintenanceCost: ironMineMaintenance,
	}, logger)
	if err != nil {
		return err
	}
	blastFurnaceTypeID, err := ensureBuildingType(ctx, pool, buildingTypeSpec{
		code: blastFurnaceCode, name: blastFurnaceName, footprintCells: blastFurnaceFootprintCells,
		maxLevel: blastFurnaceMaxLevel, baseStorage: blastFurnaceBaseStorage,
		placementRules: "{}", levelCurve: industrialLevelCurve,
		buildCost: blastFurnaceBuildCost, maintenanceCost: blastFurnaceMaintenance,
	}, logger)
	if err != nil {
		return err
	}
	coalMineTypeID, err := ensureBuildingType(ctx, pool, buildingTypeSpec{
		code: coalMineCode, name: coalMineName, footprintCells: coalMineFootprintCells,
		maxLevel: coalMineMaxLevel, baseStorage: coalMineBaseStorage,
		placementRules: coalMinePlacementRules, levelCurve: industrialLevelCurve,
		buildCost: coalMineBuildCost, maintenanceCost: coalMineMaintenance,
	}, logger)
	if err != nil {
		return err
	}

	// (c) Recetas: extracción (mine_iron, mine_coal) y manufactura (smelt_steel).
	fuelCoal := coalID
	if err := ensureRecipe(ctx, pool, recipeSpec{
		code: mineIronCode, name: mineIronName, buildingTypeID: ironMineTypeID,
		batchSimSeconds: mineIronBatchSimSeconds, fuelProductID: &fuelCoal,
		fuelPerBatch: mineIronFuelPerBatch, workersRequired: mineIronWorkers,
		ingredients: []ingredientSpec{{productID: ironOreID, role: "output", quantity: mineIronOutputQty}},
	}, logger); err != nil {
		return err
	}
	// mine_coal arranca la cadena del combustible: fuelProductID nil y
	// fuelPerBatch 0 a propósito (extraer carbón no consume carbón en v1; ver
	// el comentario de las constantes mineCoal*).
	if err := ensureRecipe(ctx, pool, recipeSpec{
		code: mineCoalCode, name: mineCoalName, buildingTypeID: coalMineTypeID,
		batchSimSeconds: mineCoalBatchSimSeconds, fuelProductID: nil,
		fuelPerBatch: 0, workersRequired: mineCoalWorkers,
		ingredients: []ingredientSpec{{productID: coalID, role: "output", quantity: mineCoalOutputQty}},
	}, logger); err != nil {
		return err
	}
	if err := ensureRecipe(ctx, pool, recipeSpec{
		code: smeltSteelCode, name: smeltSteelName, buildingTypeID: blastFurnaceTypeID,
		batchSimSeconds: smeltSteelBatchSimSeconds, fuelProductID: &fuelCoal,
		fuelPerBatch: smeltSteelFuelPerBatch, workersRequired: smeltSteelWorkers,
		ingredients: []ingredientSpec{
			{productID: ironOreID, role: "input", quantity: smeltSteelInputQty},
			{productID: steelID, role: "output", quantity: smeltSteelOutputQty},
		},
	}, logger); err != nil {
		return err
	}

	// (d) Ciudad consumidora y su curva de demanda.
	cityAcct, _, err := ensureAuthAccount(ctx, repo, "city", cityName, logger)
	if err != nil {
		return err
	}
	cityID, err := ensureCity(ctx, pool, cat.RegionID, cityAcct, logger)
	if err != nil {
		return err
	}
	if err := ensureCityDemand(ctx, pool, cityID, ironOreID, demandIronD0, demandIronCurrentPrice, 1, logger); err != nil {
		return err
	}
	if err := ensureCityDemand(ctx, pool, cityID, steelID, demandSteelD0, demandSteelCurrentPrice, cityLevel, logger); err != nil {
		return err
	}

	// (e) Yacimientos finitos en suelo libre: iron_ore (Incremento 2) y coal
	// (Incremento 4, cadena del carbón para los bots).
	if err := ensureResourceDeposit(ctx, pool, cat.RegionID, ironOreID, ironDepositX, ironDepositY, ironDepositInitial, logger); err != nil {
		return err
	}
	if err := ensureResourceDeposit(ctx, pool, cat.RegionID, coalID, coalDepositX, coalDepositY, coalDepositInitial, logger); err != nil {
		return err
	}

	logger.Info("mundo industrial del Incremento 2 garantizado")
	return nil
}

// ─── Tipos de edificio ─────────────────────────────────────────────────────────

type buildingTypeSpec struct {
	code            string
	name            string
	footprintCells  int
	maxLevel        int
	baseStorage     int64
	placementRules  string // JSON
	levelCurve      string // JSON
	buildCost       int64
	maintenanceCost int64
}

// ensureBuildingType garantiza un tipo de edificación del catálogo (clave
// natural: code).
func ensureBuildingType(ctx context.Context, pool *pgxpool.Pool, spec buildingTypeSpec, logger *slog.Logger) (uuid.UUID, error) {
	var id uuid.UUID
	err := pool.QueryRow(ctx, `SELECT id FROM world.building_types WHERE code = $1`, spec.code).Scan(&id)
	switch {
	case err == nil:
		logger.Info("tipo de edificio ya existía: omitido", slog.String("building_type", spec.code))
		return id, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return uuid.Nil, fmt.Errorf("seed: consultando el tipo de edificio %q: %w", spec.code, err)
	}
	id, err = newID()
	if err != nil {
		return uuid.Nil, err
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO world.building_types
		       (id, code, name, footprint_cells, max_level, base_storage,
		        placement_rules, level_curve, build_cost, maintenance_cost)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8::jsonb, $9, $10)`,
		id, spec.code, spec.name, spec.footprintCells, spec.maxLevel, spec.baseStorage,
		spec.placementRules, spec.levelCurve, spec.buildCost, spec.maintenanceCost); err != nil {
		return uuid.Nil, fmt.Errorf("seed: creando el tipo de edificio %q: %w", spec.code, err)
	}
	logger.Info("tipo de edificio creado",
		slog.String("building_type", spec.code),
		slog.String("building_type_id", id.String()),
		slog.Int64("build_cost", spec.buildCost))
	return id, nil
}

// ─── Recetas ───────────────────────────────────────────────────────────────────

type ingredientSpec struct {
	productID uuid.UUID
	role      string // 'input' | 'output'
	quantity  int64
}

type recipeSpec struct {
	code            string
	name            string
	buildingTypeID  uuid.UUID
	batchSimSeconds int64
	fuelProductID   *uuid.UUID
	fuelPerBatch    int64
	workersRequired int
	ingredients     []ingredientSpec
}

// ensureRecipe garantiza una receta y sus ingredientes (clave natural de la
// receta: code; de cada ingrediente: la PK (recipe_id, product_id, role)).
func ensureRecipe(ctx context.Context, pool *pgxpool.Pool, spec recipeSpec, logger *slog.Logger) error {
	var id uuid.UUID
	err := pool.QueryRow(ctx, `SELECT id FROM world.recipes WHERE code = $1`, spec.code).Scan(&id)
	switch {
	case err == nil:
		logger.Info("receta ya existía: omitida", slog.String("recipe", spec.code))
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("seed: consultando la receta %q: %w", spec.code, err)
	default:
		id, err = newID()
		if err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO world.recipes
			       (id, building_type_id, code, name, batch_sim_seconds,
			        fuel_product_id, fuel_per_batch, workers_required)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			id, spec.buildingTypeID, spec.code, spec.name, spec.batchSimSeconds,
			spec.fuelProductID, spec.fuelPerBatch, spec.workersRequired); err != nil {
			return fmt.Errorf("seed: creando la receta %q: %w", spec.code, err)
		}
		logger.Info("receta creada",
			slog.String("recipe", spec.code),
			slog.String("recipe_id", id.String()),
			slog.Int64("batch_sim_seconds", spec.batchSimSeconds))
	}

	for _, ing := range spec.ingredients {
		if _, err := pool.Exec(ctx, `
			INSERT INTO world.recipe_ingredients (recipe_id, product_id, role, quantity)
			VALUES ($1, $2, $3::world.ingredient_role, $4)
			ON CONFLICT (recipe_id, product_id, role) DO NOTHING`,
			id, ing.productID, ing.role, ing.quantity); err != nil {
			return fmt.Errorf("seed: creando el ingrediente %s de la receta %q: %w", ing.role, spec.code, err)
		}
	}
	return nil
}

// ─── Ciudades y demanda ────────────────────────────────────────────────────────

// ensureCity garantiza la ciudad consumidora (clave natural: name).
func ensureCity(ctx context.Context, pool *pgxpool.Pool, regionID uuid.UUID, cityAcct auth.Account, logger *slog.Logger) (uuid.UUID, error) {
	var id uuid.UUID
	err := pool.QueryRow(ctx, `SELECT id FROM world.cities WHERE name = $1`, cityName).Scan(&id)
	switch {
	case err == nil:
		logger.Info("ciudad ya existía: omitida", slog.String("city", cityName))
		return id, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return uuid.Nil, fmt.Errorf("seed: consultando la ciudad %q: %w", cityName, err)
	}
	id, err = newID()
	if err != nil {
		return uuid.Nil, err
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO world.cities
		       (id, region_id, account_id, name, location, level, population,
		        supply_index, influence_radius_m, base_salary)
		VALUES ($1, $2, $3, $4, ST_GeomFromText($5, 0), $6, $7, $8, $9, $10)`,
		id, regionID, cityAcct.ID, cityName, pointWKT(cityLocX, cityLocY),
		cityLevel, cityPopulation, citySupplyIndex, cityInfluenceRadiusM, cityBaseSalary); err != nil {
		return uuid.Nil, fmt.Errorf("seed: creando la ciudad %q: %w", cityName, err)
	}
	logger.Info("ciudad creada",
		slog.String("city", cityName),
		slog.String("city_id", id.String()),
		slog.Int("level", cityLevel),
		slog.Int64("base_salary", cityBaseSalary))
	return id, nil
}

// ensureCityDemand garantiza una fila de la curva de demanda (PK: city_id,
// product_id). ON CONFLICT DO NOTHING: nunca pisa una demanda ya movida por el
// Balancer.
func ensureCityDemand(ctx context.Context, pool *pgxpool.Pool, cityID, productID uuid.UUID, d0, currentPrice int64, unlockedAtLevel int, logger *slog.Logger) error {
	tag, err := pool.Exec(ctx, `
		INSERT INTO world.city_demand
		       (city_id, product_id, d0_per_sim_day, supply_ema, saturation_factor,
		        current_price, unlocked_at_level)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (city_id, product_id) DO NOTHING`,
		cityID, productID, d0, demandSupplyEMA, demandSaturation, currentPrice, unlockedAtLevel)
	if err != nil {
		return fmt.Errorf("seed: creando la demanda de la ciudad %s: %w", cityID, err)
	}
	if tag.RowsAffected() > 0 {
		logger.Info("demanda urbana creada",
			slog.String("city_id", cityID.String()),
			slog.String("product_id", productID.String()),
			slog.Int64("d0_per_sim_day", d0))
	} else {
		logger.Info("demanda urbana ya existía: omitida",
			slog.String("city_id", cityID.String()),
			slog.String("product_id", productID.String()))
	}
	return nil
}

// ─── Yacimientos ───────────────────────────────────────────────────────────────

// ensureResourceDeposit garantiza el yacimiento finito de un producto en un punto
// (clave natural: región + producto + ubicación exacta). No renovable (GDD 10).
func ensureResourceDeposit(ctx context.Context, pool *pgxpool.Pool, regionID, productID uuid.UUID, x, y, initial int64, logger *slog.Logger) error {
	var id uuid.UUID
	err := pool.QueryRow(ctx, `
		SELECT id FROM world.resource_deposits
		 WHERE region_id = $1 AND product_id = $2
		   AND ST_Equals(location, ST_GeomFromText($3, 0))
		 LIMIT 1`, regionID, productID, pointWKT(x, y)).Scan(&id)
	switch {
	case err == nil:
		logger.Info("yacimiento ya existía: omitido",
			slog.String("deposit_id", id.String()),
			slog.String("product_id", productID.String()))
		return nil
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("seed: consultando el yacimiento de %s: %w", productID, err)
	}
	id, err = newID()
	if err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO world.resource_deposits
		       (id, region_id, product_id, location, initial_amount,
		        remaining_amount, renewable, regen_per_sim_day)
		VALUES ($1, $2, $3, ST_GeomFromText($4, 0), $5, $5, false, 0)`,
		id, regionID, productID, pointWKT(x, y), initial); err != nil {
		return fmt.Errorf("seed: creando el yacimiento de %s: %w", productID, err)
	}
	logger.Info("yacimiento creado",
		slog.String("deposit_id", id.String()),
		slog.String("product_id", productID.String()),
		slog.Int64("initial_amount", initial),
		slog.Int64("x", x), slog.Int64("y", y))
	return nil
}
