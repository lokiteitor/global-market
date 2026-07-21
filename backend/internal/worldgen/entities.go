package worldgen

// Altas idempotentes de las entidades del mundo generado (regiones, junctions,
// ciudades con su centro de distribución, yacimientos, enlaces road) por pgx
// directo. Cada pieza se localiza por su clave natural antes de crearse; los IDs
// son estables entre ejecuciones. Geometrías SRID 0 planar; dinero/stock int64.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lokiteitor/global-market/backend/internal/auth"
	"github.com/lokiteitor/global-market/backend/internal/ledger"
)

// ─── Regiones ─────────────────────────────────────────────────────────────────

// regionName construye el nombre único y estable de una región por su celda.
func regionName(gx, gy int) string {
	return fmt.Sprintf("Región %d,%d", gx, gy)
}

// ensureRegionRow garantiza la fila de región (clave natural: (grid_x, grid_y)
// UNIQUE). Bounds planos contiguos, bioma por noise, shard_key propio y palancas
// fiscales por bioma. Devuelve id, nombre y si se creó.
func ensureRegionRow(ctx context.Context, st *genState, gx, gy int, minX, minY int64, biome string, params biomeParams) (uuid.UUID, string, bool, error) {
	name := regionName(gx, gy)
	var id uuid.UUID
	err := st.pool.QueryRow(ctx, `SELECT id FROM world.regions WHERE grid_x = $1 AND grid_y = $2`, gx, gy).Scan(&id)
	switch {
	case err == nil:
		st.logger.Info("región ya existía: omitida", slog.String("region", name), slog.String("biome", biome))
		return id, name, false, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return uuid.Nil, "", false, fmt.Errorf("worldgen: consultando la región (%d,%d): %w", gx, gy, err)
	}
	id, err = newID()
	if err != nil {
		return uuid.Nil, "", false, err
	}
	size := st.opts.RegionSizeM
	shardKey := fmt.Sprintf("shard-%d-%d", gx, gy)
	if _, err := st.pool.Exec(ctx, `
		INSERT INTO world.regions
		       (id, name, grid_x, grid_y, bounds, biome, shard_key,
		        tax_rate_bp, customs_rate_bp, canon_base)
		VALUES ($1, $2, $3, $4, ST_GeomFromText($5, 0), $6::world.biome, $7, $8, $9, $10)`,
		id, name, gx, gy, rectWKT(minX, minY, minX+size, minY+size), biome, shardKey,
		params.taxBP, params.customsBP, params.canonBase); err != nil {
		return uuid.Nil, "", false, fmt.Errorf("worldgen: creando la región (%d,%d): %w", gx, gy, err)
	}
	st.logger.Info("región creada",
		slog.String("region", name), slog.String("region_id", id.String()),
		slog.String("biome", biome), slog.String("shard_key", shardKey))
	return id, name, true, nil
}

// ─── Nodos ────────────────────────────────────────────────────────────────────

// ensureCentralJunction garantiza el junction central de una región en (x,y)
// (clave natural: región + kind junction + ubicación exacta).
func ensureCentralJunction(ctx context.Context, st *genState, regionID uuid.UUID, x, y int64) (uuid.UUID, error) {
	return ensureJunctionAt(ctx, st, regionID, x, y)
}

// ensureJunctionAt garantiza un nodo junction en (x,y) de la región (clave
// natural: región + kind junction + ubicación exacta). Sirve tanto al hub central
// como a los nodos de acceso a yacimientos.
func ensureJunctionAt(ctx context.Context, st *genState, regionID uuid.UUID, x, y int64) (uuid.UUID, error) {
	var id uuid.UUID
	err := st.pool.QueryRow(ctx, `
		SELECT id FROM world.network_nodes
		 WHERE region_id = $1 AND kind = 'junction'
		   AND ST_Equals(location, ST_GeomFromText($2, 0))
		 LIMIT 1`, regionID, pointWKT(x, y)).Scan(&id)
	switch {
	case err == nil:
		return id, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return uuid.Nil, fmt.Errorf("worldgen: consultando junction (%d,%d): %w", x, y, err)
	}
	id, err = newID()
	if err != nil {
		return uuid.Nil, err
	}
	if _, err := st.pool.Exec(ctx, `
		INSERT INTO world.network_nodes (id, kind, region_id, location)
		VALUES ($1, 'junction', $2, ST_GeomFromText($3, 0))`,
		id, regionID, pointWKT(x, y)); err != nil {
		return uuid.Nil, fmt.Errorf("worldgen: creando junction (%d,%d): %w", x, y, err)
	}
	return id, nil
}

// ─── Enlaces ROAD intra-región ────────────────────────────────────────────────

// ensureRoadLinkPair garantiza el par de enlaces road bidireccionales entre dos
// nodos (un enlace dirigido por sentido), cada uno con su único link_segment en
// la región.
func ensureRoadLinkPair(ctx context.Context, st *genState, regionID, aID uuid.UUID, ax, ay int64, bID uuid.UUID, bx, by int64) error {
	if err := ensureRoadLink(ctx, st, regionID, aID, ax, ay, bID, bx, by); err != nil {
		return err
	}
	return ensureRoadLink(ctx, st, regionID, bID, bx, by, aID, ax, ay)
}

// ensureRoadLink garantiza UN enlace road dirigido from→to (clave natural:
// (from_node_id, to_node_id, mode)) con su único link_segment (seq 1, región,
// congestión fluida 1.0).
func ensureRoadLink(ctx context.Context, st *genState, regionID, fromID uuid.UUID, fx, fy int64, toID uuid.UUID, tx, ty int64) error {
	var linkID uuid.UUID
	err := st.pool.QueryRow(ctx, `
		SELECT id FROM world.network_links
		 WHERE from_node_id = $1 AND to_node_id = $2 AND mode = 'road'
		 LIMIT 1`, fromID, toID).Scan(&linkID)
	switch {
	case err == nil:
		return nil
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("worldgen: consultando enlace road %s→%s: %w", fromID, toID, err)
	}
	lengthM := euclideanM(fx, fy, tx, ty)
	path := lineWKT(fx, fy, tx, ty)
	linkID, err = newID()
	if err != nil {
		return err
	}
	if _, err := st.pool.Exec(ctx, `
		INSERT INTO world.network_links
		       (id, mode, from_node_id, to_node_id, path, length_m, capacity_per_hour, base_speed_kmh)
		VALUES ($1, 'road', $2, $3, ST_GeomFromText($4, 0), $5, $6, $7)`,
		linkID, fromID, toID, path, lengthM, genRoadCapacityPerHour, genRoadBaseSpeedKmh); err != nil {
		return fmt.Errorf("worldgen: creando enlace road %s→%s: %w", fromID, toID, err)
	}
	segID, err := newID()
	if err != nil {
		return err
	}
	if _, err := st.pool.Exec(ctx, `
		INSERT INTO world.link_segments (id, link_id, region_id, seq, portion, length_m, congestion_ema)
		VALUES ($1, $2, $3, 1, ST_GeomFromText($4, 0), $5, 1.0)`,
		segID, linkID, regionID, path, lengthM); err != nil {
		return fmt.Errorf("worldgen: creando segmento road %s: %w", linkID, err)
	}
	return nil
}

// ─── Ciudades ─────────────────────────────────────────────────────────────────

// cityName construye el nombre único y estable de una ciudad por bioma, celda e
// índice. Los prefijos temáticos por bioma dan sabor sin comprometer la unicidad
// (garantizada por (gx,gy,i)).
func cityName(biome string, gx, gy, i int) string {
	base := "Villa"
	switch biome {
	case BiomeMountain:
		base = "Monte"
	case BiomeDesert:
		base = "Oasis"
	case BiomeForest:
		base = "Robledal"
	case BiomeCoast:
		base = "Puerto"
	}
	name := fmt.Sprintf("%s %d,%d", base, gx, gy)
	if i > 0 {
		name = fmt.Sprintf("%s (anexo %d)", name, i)
	}
	return name
}

// ensureGeneratedCity garantiza una ciudad generada completa: cuenta kind=city,
// caja prefondeada (emisión del banco central), fila de ciudad, centro de
// distribución (concesión del sistema + edificio de la ciudad + nodo), enlace vial
// al junction y demanda base de iron_ore. Reutiliza el patrón del seed/balancer.
func ensureGeneratedCity(ctx context.Context, st *genState, reg *genRegion, params biomeParams, i int, x, y int64) error {
	name := cityName(reg.Biome, reg.GridX, reg.GridY, i)

	acct, err := ensureAuthAccount(ctx, st.authRepo, "city", name, st.logger)
	if err != nil {
		return err
	}
	if err := ensureCityCapital(ctx, st, acct, name); err != nil {
		return err
	}
	cityID, created, err := ensureCityRow(ctx, st, reg.RegionID, acct, name, params, x, y)
	if err != nil {
		return err
	}
	if created {
		st.summary.CitiesCreated++
	}

	concessionID, err := ensureCityConcession(ctx, st, reg.RegionID, name, x, y)
	if err != nil {
		return err
	}
	buildingID, err := ensureDistCenterBuilding(ctx, st, reg.RegionID, acct, concessionID, name, x, y)
	if err != nil {
		return err
	}
	distNodeID, err := ensureDistCenterNode(ctx, st, reg.RegionID, buildingID, cityID, name, x, y)
	if err != nil {
		return err
	}
	if err := ensureRoadLinkPair(ctx, st, reg.RegionID, distNodeID, x, y, reg.JunctionID, reg.JunctionX, reg.JunctionY); err != nil {
		return err
	}
	return ensureCityDemand(ctx, st, cityID, st.ironOreID, params.demandD0, params.demandPrice)
}

// ensureCityRow garantiza la fila world.cities (clave natural: name UNIQUE).
func ensureCityRow(ctx context.Context, st *genState, regionID uuid.UUID, acct auth.Account, name string, params biomeParams, x, y int64) (uuid.UUID, bool, error) {
	var id uuid.UUID
	err := st.pool.QueryRow(ctx, `SELECT id FROM world.cities WHERE name = $1`, name).Scan(&id)
	switch {
	case err == nil:
		return id, false, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return uuid.Nil, false, fmt.Errorf("worldgen: consultando la ciudad %q: %w", name, err)
	}
	id, err = newID()
	if err != nil {
		return uuid.Nil, false, err
	}
	if _, err := st.pool.Exec(ctx, `
		INSERT INTO world.cities
		       (id, region_id, account_id, name, location, level, population,
		        supply_index, influence_radius_m, base_salary)
		VALUES ($1, $2, $3, $4, ST_GeomFromText($5, 0), $6, $7, $8, $9, $10)`,
		id, regionID, acct.ID, name, pointWKT(x, y),
		params.cityLevel, params.population, params.supplyIndex, params.influenceM, params.baseSalary); err != nil {
		return uuid.Nil, false, fmt.Errorf("worldgen: creando la ciudad %q: %w", name, err)
	}
	st.logger.Info("ciudad creada", slog.String("city", name), slog.String("city_id", id.String()),
		slog.Int64("x", x), slog.Int64("y", y))
	return id, true, nil
}

// ensureCityCapital garantiza la caja de la ciudad con su capital inicial, emitido
// una sola vez (+caja / −emisión). La existencia de la caja es la clave de
// idempotencia.
func ensureCityCapital(ctx context.Context, st *genState, acct auth.Account, name string) error {
	existing, _, err := st.ledger.ListAccounts(ctx, acct.ID, ledger.AccountFilter{Kind: ledger.AccountKindCash, Limit: 1})
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	cash, err := st.ledger.EnsureCashAccount(ctx, acct.ID)
	if err != nil {
		return err
	}
	ref := acct.ID
	if _, err := st.ledger.PostTransaction(ctx, ledger.TransactionKindSeedCapital, st.simNow, &ref,
		fmt.Sprintf("Capital inicial de la ciudad %s (emisión del banco central, worldgen)", name),
		[]ledger.EntryInput{
			{AccountID: cash.ID, Amount: CityInitialCapital},
			{AccountID: st.emission.ID, Amount: -CityInitialCapital},
		}); err != nil {
		return fmt.Errorf("worldgen: prefondeando la ciudad %q: %w", name, err)
	}
	st.logger.Info("capital inicial de ciudad asentado", slog.String("city", name), slog.Int64("amount", CityInitialCapital))
	return nil
}

// ensureCityConcession garantiza la concesión del sistema (holder = banco central)
// para el centro de distribución (clave: holder banco + región + parcela).
func ensureCityConcession(ctx context.Context, st *genState, regionID uuid.UUID, name string, x, y int64) (uuid.UUID, error) {
	parcel := rectWKT(x-distCenterParcelHalfM, y-distCenterParcelHalfM, x+distCenterParcelHalfM, y+distCenterParcelHalfM)
	var id uuid.UUID
	err := st.pool.QueryRow(ctx, `
		SELECT id FROM world.land_concessions
		 WHERE holder_account_id = $1 AND region_id = $2
		   AND ST_Equals(parcel, ST_GeomFromText($3, 0)) AND status <> 'reverted'
		 LIMIT 1`, st.bank.ID, regionID, parcel).Scan(&id)
	switch {
	case err == nil:
		return id, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return uuid.Nil, fmt.Errorf("worldgen: consultando la concesión de %q: %w", name, err)
	}
	id, err = newID()
	if err != nil {
		return uuid.Nil, err
	}
	if _, err := st.pool.Exec(ctx, `
		INSERT INTO world.land_concessions
		       (id, region_id, holder_account_id, parcel, canon_amount,
		        period_sim_days, expires_at_sim, status, granted_at_sim)
		VALUES ($1, $2, $3, ST_GeomFromText($4, 0), $5, $6, $7, 'active', $8)`,
		id, regionID, st.bank.ID, parcel, distConcessionCanon,
		distConcessionPeriodDays, int64(distConcessionExpiresAtSim), distConcessionGrantedAtSim); err != nil {
		return uuid.Nil, fmt.Errorf("worldgen: creando la concesión de %q: %w", name, err)
	}
	return id, nil
}

// ensureDistCenterBuilding garantiza el centro de distribución de la ciudad
// (owner = cuenta de la ciudad) sobre su concesión (clave: owner + tipo).
func ensureDistCenterBuilding(ctx context.Context, st *genState, regionID uuid.UUID, acct auth.Account, concessionID uuid.UUID, name string, x, y int64) (uuid.UUID, error) {
	var id uuid.UUID
	err := st.pool.QueryRow(ctx, `
		SELECT id FROM world.buildings
		 WHERE owner_account_id = $1 AND building_type_id = $2
		 LIMIT 1`, acct.ID, st.distTypeID).Scan(&id)
	switch {
	case err == nil:
		return id, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return uuid.Nil, fmt.Errorf("worldgen: consultando el centro de %q: %w", name, err)
	}
	id, err = newID()
	if err != nil {
		return uuid.Nil, err
	}
	footprint := rectWKT(x-distCenterFootprintHalfM, y-distCenterFootprintHalfM, x+distCenterFootprintHalfM, y+distCenterFootprintHalfM)
	if _, err := st.pool.Exec(ctx, `
		INSERT INTO world.buildings
		       (id, owner_account_id, region_id, concession_id, building_type_id, footprint, level, status)
		VALUES ($1, $2, $3, $4, $5, ST_GeomFromText($6, 0), 1, 'operational')`,
		id, acct.ID, regionID, concessionID, st.distTypeID, footprint); err != nil {
		return uuid.Nil, fmt.Errorf("worldgen: creando el centro de %q: %w", name, err)
	}
	return id, nil
}

// ensureDistCenterNode garantiza el nodo distribution_center del grafo ligado al
// centro y a la ciudad (clave: building_id + kind). Es el destino de las buys.
func ensureDistCenterNode(ctx context.Context, st *genState, regionID, buildingID, cityID uuid.UUID, name string, x, y int64) (uuid.UUID, error) {
	var id uuid.UUID
	err := st.pool.QueryRow(ctx, `
		SELECT id FROM world.network_nodes
		 WHERE building_id = $1 AND kind = 'distribution_center'
		 LIMIT 1`, buildingID).Scan(&id)
	switch {
	case err == nil:
		return id, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return uuid.Nil, fmt.Errorf("worldgen: consultando el nodo del centro de %q: %w", name, err)
	}
	id, err = newID()
	if err != nil {
		return uuid.Nil, err
	}
	if _, err := st.pool.Exec(ctx, `
		INSERT INTO world.network_nodes (id, kind, region_id, building_id, city_id, location)
		VALUES ($1, 'distribution_center', $2, $3, $4, ST_GeomFromText($5, 0))`,
		id, regionID, buildingID, cityID, pointWKT(x, y)); err != nil {
		return uuid.Nil, fmt.Errorf("worldgen: creando el nodo del centro de %q: %w", name, err)
	}
	return id, nil
}

// ensureCityDemand garantiza una fila de demanda (PK: city_id, product_id).
func ensureCityDemand(ctx context.Context, st *genState, cityID, productID uuid.UUID, d0, currentPrice int64) error {
	if _, err := st.pool.Exec(ctx, `
		INSERT INTO world.city_demand
		       (city_id, product_id, d0_per_sim_day, supply_ema, saturation_factor, current_price, unlocked_at_level)
		VALUES ($1, $2, $3, 1.0, 1.0, $4, 1)
		ON CONFLICT (city_id, product_id) DO NOTHING`,
		cityID, productID, d0, currentPrice); err != nil {
		return fmt.Errorf("worldgen: creando la demanda de la ciudad %s: %w", cityID, err)
	}
	return nil
}

// ─── Yacimientos ──────────────────────────────────────────────────────────────

// ensureGeneratedDeposit garantiza un yacimiento finito y su nodo de acceso vial
// al junction central (para que un futuro jugador levante una mina con acceso).
func ensureGeneratedDeposit(ctx context.Context, st *genState, reg *genRegion, productID uuid.UUID, x, y int64) error {
	created, err := ensureResourceDeposit(ctx, st, reg.RegionID, productID, x, y)
	if err != nil {
		return err
	}
	if created {
		st.summary.DepositsCreated++
	}
	accessID, err := ensureJunctionAt(ctx, st, reg.RegionID, x, y)
	if err != nil {
		return err
	}
	// El nodo de acceso puede coincidir con el junction central si el RNG colocó el
	// yacimiento justo en el centro: en ese caso no hay enlace (mismo nodo).
	if accessID == reg.JunctionID {
		return nil
	}
	return ensureRoadLinkPair(ctx, st, reg.RegionID, accessID, x, y, reg.JunctionID, reg.JunctionX, reg.JunctionY)
}

// ensureResourceDeposit garantiza el yacimiento finito de un producto en un punto
// (clave natural: región + producto + ubicación exacta). No renovable (GDD 10).
func ensureResourceDeposit(ctx context.Context, st *genState, regionID, productID uuid.UUID, x, y int64) (bool, error) {
	var id uuid.UUID
	err := st.pool.QueryRow(ctx, `
		SELECT id FROM world.resource_deposits
		 WHERE region_id = $1 AND product_id = $2
		   AND ST_Equals(location, ST_GeomFromText($3, 0))
		 LIMIT 1`, regionID, productID, pointWKT(x, y)).Scan(&id)
	switch {
	case err == nil:
		return false, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return false, fmt.Errorf("worldgen: consultando el yacimiento de %s: %w", productID, err)
	}
	id, err = newID()
	if err != nil {
		return false, err
	}
	if _, err := st.pool.Exec(ctx, `
		INSERT INTO world.resource_deposits
		       (id, region_id, product_id, location, initial_amount, remaining_amount, renewable, regen_per_sim_day)
		VALUES ($1, $2, $3, ST_GeomFromText($4, 0), $5, $5, false, 0)`,
		id, regionID, productID, pointWKT(x, y), depositAmount); err != nil {
		return false, fmt.Errorf("worldgen: creando el yacimiento de %s: %w", productID, err)
	}
	return true, nil
}

// ─── Catálogo aditivo ─────────────────────────────────────────────────────────

// ensureRailSeaVehicleTypes garantiza el catálogo de vehículos ferroviarios y
// marítimos (clave natural: code). Combustible: coal.
func ensureRailSeaVehicleTypes(ctx context.Context, st *genState) error {
	for _, spec := range railSeaVehicleTypes {
		var id uuid.UUID
		err := st.pool.QueryRow(ctx, `SELECT id FROM world.vehicle_types WHERE code = $1`, spec.code).Scan(&id)
		switch {
		case err == nil:
			continue
		case !errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("worldgen: consultando el tipo de vehículo %q: %w", spec.code, err)
		}
		id, err = newID()
		if err != nil {
			return err
		}
		if _, err := st.pool.Exec(ctx, `
			INSERT INTO world.vehicle_types
			       (id, code, name, mode, cargo_capacity, speed_kmh, fuel_product_id,
			        fuel_per_100km, autonomy_km, purchase_price, operating_cost_per_day)
			VALUES ($1, $2, $3, $4::world.link_mode, $5, $6, $7, $8, $9, $10, $11)`,
			id, spec.code, spec.name, spec.mode, spec.cargoCapacity, spec.speedKmh, st.coalID,
			spec.fuelPer100km, spec.autonomyKm, spec.purchasePrice, spec.operatingCostPerDay); err != nil {
			return fmt.Errorf("worldgen: creando el tipo de vehículo %q: %w", spec.code, err)
		}
		st.logger.Info("tipo de vehículo creado", slog.String("vehicle_type", spec.code), slog.String("mode", spec.mode))
	}
	return nil
}

// ensureDistCenterType garantiza el tipo de edificación distribution_center (clave
// natural: code). Idempotente con el del seed.
func ensureDistCenterType(ctx context.Context, st *genState) (uuid.UUID, error) {
	var id uuid.UUID
	err := st.pool.QueryRow(ctx, `SELECT id FROM world.building_types WHERE code = $1`, distCenterTypeCode).Scan(&id)
	switch {
	case err == nil:
		return id, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return uuid.Nil, fmt.Errorf("worldgen: consultando el tipo %q: %w", distCenterTypeCode, err)
	}
	id, err = newID()
	if err != nil {
		return uuid.Nil, err
	}
	if _, err := st.pool.Exec(ctx, `
		INSERT INTO world.building_types
		       (id, code, name, footprint_cells, max_level, base_storage,
		        placement_rules, level_curve, build_cost, maintenance_cost)
		VALUES ($1, $2, $3, $4, $5, $6, '{}'::jsonb, '{}'::jsonb, 0, 0)`,
		id, distCenterTypeCode, distCenterTypeName, distCenterFootprintCells,
		distCenterMaxLevel, distCenterBaseStorage); err != nil {
		return uuid.Nil, fmt.Errorf("worldgen: creando el tipo %q: %w", distCenterTypeCode, err)
	}
	return id, nil
}

// ensureAuthAccount devuelve la cuenta de auth con ese nombre, creándola con el
// kind dado si no existe (clave: unicidad de lower(name)).
func ensureAuthAccount(ctx context.Context, repo *auth.PGRepository, kind, name string, logger *slog.Logger) (auth.Account, error) {
	acc, err := repo.GetAccountByName(ctx, name)
	switch {
	case err == nil:
		return acc, nil
	case !errors.Is(err, auth.ErrNotFound):
		return auth.Account{}, fmt.Errorf("worldgen: consultando la cuenta %q: %w", name, err)
	}
	acc, err = repo.CreateAccount(ctx, kind, name)
	if err != nil {
		return auth.Account{}, fmt.Errorf("worldgen: creando la cuenta %q: %w", name, err)
	}
	logger.Info("cuenta creada", slog.String("account", name), slog.String("kind", kind))
	return acc, nil
}
