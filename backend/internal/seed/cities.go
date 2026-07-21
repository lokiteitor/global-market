package seed

// Infraestructura urbana del Incremento 6b (ECONOMY BALANCER, GDD 5.5/5.6): para
// que una ciudad reciba bienes por el mecanismo estándar del CCRI (la entrega
// deja el stock como stock_free de la ciudad en un almacén, que requiere un
// warehouse_building_id), CADA ciudad tiene su propio edificio CENTRO DE
// DISTRIBUCIÓN (owner = cuenta de la ciudad) sobre una concesión del sistema, en
// su ubicación, dentro de su radio de influencia. El Balancer publica las buys de
// la ciudad con destino = el nodo de ese centro; al liquidarse, la entrega deja
// el stock como stock_free de la ciudad ahí, y el consumer del Balancer lo
// consume (city stock_free → world_source, ADR-022). Así la ciudad es sumidero
// final real sin acumular inventario.
//
// El centro de distribución es INFRAESTRUCTURA del sistema: build_cost y
// maintenance 0 (no es una inversión del jugador ni un sink), y su concesión es
// del banco central con vencimiento muy lejano (permanente a efectos de juego).
// La ciudad recibe además una emisión inicial holgada de caja para pre-fondear
// sus compras desde el primer día (el Balancer la re-fondea en marcha por el
// faucet). Todo idempotente por clave natural: re-ejecutar el seed no duplica ni
// re-emite. Geometrías: SRID 0 planar, metros de mundo (ADR-019).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lokiteitor/global-market/backend/internal/auth"
	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// Tipo de edificación distribution_center: infraestructura urbana del sistema
// (GDD 5.6). Footprint 6, almacén grande, build_cost/maintenance 0 (no es una
// inversión del jugador ni un sink).
const (
	distCenterTypeCode             = "distribution_center"
	distCenterTypeName             = "Centro de distribución"
	distCenterFootprintCells       = 6
	distCenterMaxLevel             = 1
	distCenterBaseStorage    int64 = 1_000_000
	distCenterBuildCost      int64 = 0
	distCenterMaintenance    int64 = 0
	distCenterParcelHalfM    int64 = 400
	distCenterFootprintHalfM int64 = 150
)

// Concesión del sistema para el centro de distribución: canon mínimo (CHECK > 0)
// y vencimiento muy lejano (infraestructura permanente: no renueva ni entra en
// la cascada de canon del Incremento 6a en el horizonte de juego).
const (
	distCenterConcessionCanon        int64 = 1
	distCenterConcessionPeriodDays   int64 = 36_000
	distCenterConcessionGrantedAtSim int64 = 0
	distCenterConcessionExpiresAtSim       = 100 * simtime.SimYear
)

// CityInitialCapital es la emisión inicial holgada de caja de cada ciudad
// (int64), asentada UNA sola vez como emisión del banco central (+caja ciudad /
// −emisión). El Balancer re-fondea en marcha por el faucet; este capital evita
// que la primera compra dependa de una emisión y da margen a las curvas.
const CityInitialCapital int64 = 10_000_000

// seedCity es la vista mínima de una ciudad para sembrar su infraestructura.
type seedCity struct {
	ID        uuid.UUID
	AccountID uuid.UUID
	Name      string
	RegionID  uuid.UUID
	X, Y      int64
}

// ensureCityInfrastructure garantiza, por cada ciudad de la región sembrada, su
// centro de distribución (concesión del sistema + edificio de la ciudad + nodo
// del grafo) y su caja con capital inicial. Se ejecuta tras el mundo industrial
// (la ciudad ya existe). Idempotente.
func ensureCityInfrastructure(ctx context.Context, pool *pgxpool.Pool, ledgerSvc *ledger.Service, bank auth.Account, emission ledger.Account, cat worldCatalog, simNow simtime.SimTime, logger *slog.Logger) error {
	distTypeID, err := ensureDistCenterType(ctx, pool, logger)
	if err != nil {
		return err
	}
	cities, err := listSeedCities(ctx, pool, cat.RegionID)
	if err != nil {
		return err
	}
	// El junction central de la red vial (sembrado por ensureLogisticsNetwork, que
	// corre antes) es el punto de anclaje de la ciudad al grafo terrestre: sin él la
	// entrega física a la ciudad sería irrealizable (nada se teletransporta, GDD
	// 7.1). Su ausencia (p. ej. un seed sin mundo industrial) no aborta: la
	// infraestructura urbana se siembra igual, solo sin el enlace vial.
	junction, hasJunction, err := lookupJunctionNode(ctx, pool, cat.RegionID)
	if err != nil {
		return err
	}
	for _, c := range cities {
		if err := ensureCityCapital(ctx, ledgerSvc, emission, c, simNow, logger); err != nil {
			return err
		}
		concessionID, err := ensureCityConcession(ctx, pool, bank, c, simNow, logger)
		if err != nil {
			return err
		}
		buildingID, err := ensureDistCenterBuilding(ctx, pool, distTypeID, concessionID, c, logger)
		if err != nil {
			return err
		}
		if err := ensureDistCenterNode(ctx, pool, buildingID, c, logger); err != nil {
			return err
		}
		if hasJunction {
			if err := ensureCityRoadLinks(ctx, pool, cat.RegionID, junction, c, logger); err != nil {
				return err
			}
		} else {
			logger.Warn("no hay junction en la región: el centro de distribución queda sin enlace vial",
				slog.String("city", c.Name))
		}
	}
	logger.Info("infraestructura urbana garantizada (centros de distribución + enlace vial + capital de ciudad)")
	return nil
}

// junctionNode es el nodo junction de anclaje vial (id + ubicación en metros de
// mundo) al que se conecta el centro de distribución de cada ciudad.
type junctionNode struct {
	ID   uuid.UUID
	X, Y int64
}

// lookupJunctionNode localiza el junction central de la región (kind=junction).
// found=false si la red vial aún no se sembró (seed sin mundo industrial).
func lookupJunctionNode(ctx context.Context, pool *pgxpool.Pool, regionID uuid.UUID) (junctionNode, bool, error) {
	var j junctionNode
	err := pool.QueryRow(ctx, `
		SELECT id, ST_X(location)::bigint, ST_Y(location)::bigint
		  FROM world.network_nodes
		 WHERE region_id = $1 AND kind = 'junction'
		 ORDER BY id
		 LIMIT 1`, regionID).Scan(&j.ID, &j.X, &j.Y)
	switch {
	case err == nil:
		return j, true, nil
	case errors.Is(err, pgx.ErrNoRows):
		return junctionNode{}, false, nil
	default:
		return junctionNode{}, false, fmt.Errorf("seed: consultando el junction de la región %s: %w", regionID, err)
	}
}

// ensureCityRoadLinks garantiza el par de enlaces road BIDIRECCIONALES entre el
// nodo del centro de distribución de la ciudad y el junction central, para que la
// entrega estándar del CCRI (transporte físico real, GDD 7) pueda alcanzar la
// ciudad. Reutiliza ensureRoadLink (idempotente por (from, to, mode)).
func ensureCityRoadLinks(ctx context.Context, pool *pgxpool.Pool, regionID uuid.UUID, junction junctionNode, c seedCity, logger *slog.Logger) error {
	distNodeID, err := distCenterNodeID(ctx, pool, c)
	if err != nil {
		return err
	}
	if err := ensureRoadLink(ctx, pool, regionID, distNodeID, c.X, c.Y, junction.ID, junction.X, junction.Y, logger); err != nil {
		return err
	}
	if err := ensureRoadLink(ctx, pool, regionID, junction.ID, junction.X, junction.Y, distNodeID, c.X, c.Y, logger); err != nil {
		return err
	}
	return nil
}

// distCenterNodeID devuelve el id del nodo distribution_center de la ciudad
// (sembrado por ensureDistCenterNode inmediatamente antes).
func distCenterNodeID(ctx context.Context, pool *pgxpool.Pool, c seedCity) (uuid.UUID, error) {
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT id FROM world.network_nodes
		 WHERE kind = 'distribution_center' AND city_id = $1
		 ORDER BY id LIMIT 1`, c.ID).Scan(&id); err != nil {
		return uuid.Nil, fmt.Errorf("seed: localizando el nodo del centro de %s: %w", c.Name, err)
	}
	return id, nil
}

// listSeedCities lista las ciudades de la región con su ubicación (centroide del
// punto, metros de mundo).
func listSeedCities(ctx context.Context, pool *pgxpool.Pool, regionID uuid.UUID) ([]seedCity, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, account_id, name, region_id,
		       ST_X(location)::bigint AS x, ST_Y(location)::bigint AS y
		FROM world.cities
		WHERE region_id = $1
		ORDER BY id`, regionID)
	if err != nil {
		return nil, fmt.Errorf("seed: listando ciudades de la región %s: %w", regionID, err)
	}
	defer rows.Close()
	var out []seedCity
	for rows.Next() {
		var c seedCity
		if err := rows.Scan(&c.ID, &c.AccountID, &c.Name, &c.RegionID, &c.X, &c.Y); err != nil {
			return nil, fmt.Errorf("seed: leyendo una ciudad: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ensureDistCenterType garantiza el tipo de edificación distribution_center
// (clave natural: code).
func ensureDistCenterType(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) (uuid.UUID, error) {
	var id uuid.UUID
	err := pool.QueryRow(ctx, `SELECT id FROM world.building_types WHERE code = $1`, distCenterTypeCode).Scan(&id)
	switch {
	case err == nil:
		logger.Info("tipo de edificio ya existía: omitido", slog.String("building_type", distCenterTypeCode))
		return id, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return uuid.Nil, fmt.Errorf("seed: consultando el tipo de edificio %q: %w", distCenterTypeCode, err)
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
		id, distCenterTypeCode, distCenterTypeName, distCenterFootprintCells,
		distCenterMaxLevel, distCenterBaseStorage, distCenterBuildCost, distCenterMaintenance); err != nil {
		return uuid.Nil, fmt.Errorf("seed: creando el tipo de edificio %q: %w", distCenterTypeCode, err)
	}
	logger.Info("tipo de edificio creado (infraestructura urbana)",
		slog.String("building_type", distCenterTypeCode),
		slog.String("building_type_id", id.String()))
	return id, nil
}

// ensureCityCapital garantiza la caja de la ciudad con su capital inicial,
// emitido una sola vez (+caja / −emisión). La existencia de la caja es la clave
// de idempotencia: si ya existía, el capital ya se emitió y no se re-emite.
func ensureCityCapital(ctx context.Context, ledgerSvc *ledger.Service, emission ledger.Account, c seedCity, simNow simtime.SimTime, logger *slog.Logger) error {
	existing, _, err := ledgerSvc.ListAccounts(ctx, c.AccountID, ledger.AccountFilter{Kind: ledger.AccountKindCash, Limit: 1})
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		logger.Info("caja de ciudad ya existía: capital inicial omitido",
			slog.String("city", c.Name), slog.Int64("balance", existing[0].Balance))
		return nil
	}
	cash, err := ledgerSvc.EnsureCashAccount(ctx, c.AccountID)
	if err != nil {
		return err
	}
	ref := c.AccountID
	txID, err := ledgerSvc.PostTransaction(ctx, ledger.TransactionKindSeedCapital, simNow, &ref,
		fmt.Sprintf("Capital inicial de la ciudad %s (emisión del banco central, faucet)", c.Name),
		[]ledger.EntryInput{
			{AccountID: cash.ID, Amount: CityInitialCapital},
			{AccountID: emission.ID, Amount: -CityInitialCapital},
		})
	if err != nil {
		return err
	}
	logger.Info("capital inicial de ciudad asentado",
		slog.String("city", c.Name), slog.Int64("amount", CityInitialCapital),
		slog.String("transaction_id", txID.String()))
	return nil
}

// ensureCityConcession garantiza la concesión del sistema (holder = banco
// central) para el centro de distribución de la ciudad (clave de idempotencia:
// una concesión del banco central cuya parcela coincide con la de la ciudad).
func ensureCityConcession(ctx context.Context, pool *pgxpool.Pool, bank auth.Account, c seedCity, simNow simtime.SimTime, logger *slog.Logger) (uuid.UUID, error) {
	parcel := rectWKT(c.X-distCenterParcelHalfM, c.Y-distCenterParcelHalfM, c.X+distCenterParcelHalfM, c.Y+distCenterParcelHalfM)
	var id uuid.UUID
	err := pool.QueryRow(ctx, `
		SELECT id FROM world.land_concessions
		 WHERE holder_account_id = $1 AND region_id = $2
		   AND ST_Equals(parcel, ST_GeomFromText($3, 0)) AND status <> 'reverted'
		 LIMIT 1`, bank.ID, c.RegionID, parcel).Scan(&id)
	switch {
	case err == nil:
		logger.Info("concesión del centro de distribución ya existía: omitida", slog.String("city", c.Name))
		return id, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return uuid.Nil, fmt.Errorf("seed: consultando la concesión del centro de %s: %w", c.Name, err)
	}
	id, err = newID()
	if err != nil {
		return uuid.Nil, err
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO world.land_concessions
		       (id, region_id, holder_account_id, parcel, canon_amount,
		        period_sim_days, expires_at_sim, status, granted_at_sim)
		VALUES ($1, $2, $3, ST_GeomFromText($4, 0), $5, $6, $7, 'active', $8)`,
		id, c.RegionID, bank.ID, parcel, distCenterConcessionCanon,
		distCenterConcessionPeriodDays, distCenterConcessionExpiresAtSim, distCenterConcessionGrantedAtSim); err != nil {
		return uuid.Nil, fmt.Errorf("seed: creando la concesión del centro de %s: %w", c.Name, err)
	}
	logger.Info("concesión del centro de distribución creada (sistema)",
		slog.String("city", c.Name), slog.String("concession_id", id.String()))
	return id, nil
}

// ensureDistCenterBuilding garantiza el centro de distribución de la ciudad
// (owner = cuenta de la ciudad) sobre su concesión, operativo (clave de
// idempotencia: un edificio de ese tipo por dueño-ciudad).
func ensureDistCenterBuilding(ctx context.Context, pool *pgxpool.Pool, distTypeID, concessionID uuid.UUID, c seedCity, logger *slog.Logger) (uuid.UUID, error) {
	var id uuid.UUID
	err := pool.QueryRow(ctx, `
		SELECT id FROM world.buildings
		 WHERE owner_account_id = $1 AND building_type_id = $2
		 LIMIT 1`, c.AccountID, distTypeID).Scan(&id)
	switch {
	case err == nil:
		logger.Info("centro de distribución ya existía: omitido", slog.String("city", c.Name))
		return id, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return uuid.Nil, fmt.Errorf("seed: consultando el centro de distribución de %s: %w", c.Name, err)
	}
	id, err = newID()
	if err != nil {
		return uuid.Nil, err
	}
	footprint := rectWKT(c.X-distCenterFootprintHalfM, c.Y-distCenterFootprintHalfM, c.X+distCenterFootprintHalfM, c.Y+distCenterFootprintHalfM)
	if _, err := pool.Exec(ctx, `
		INSERT INTO world.buildings
		       (id, owner_account_id, region_id, concession_id,
		        building_type_id, footprint, level, status)
		VALUES ($1, $2, $3, $4, $5, ST_GeomFromText($6, 0), 1, 'operational')`,
		id, c.AccountID, c.RegionID, concessionID, distTypeID, footprint); err != nil {
		return uuid.Nil, fmt.Errorf("seed: creando el centro de distribución de %s: %w", c.Name, err)
	}
	logger.Info("centro de distribución creado (operational, owner = ciudad)",
		slog.String("city", c.Name), slog.String("building_id", id.String()))
	return id, nil
}

// ensureDistCenterNode garantiza el nodo distribution_center del grafo logístico
// ligado al centro y a la ciudad (clave de idempotencia: un nodo de ese tipo por
// edificio). Es el destino de las buys de la ciudad.
func ensureDistCenterNode(ctx context.Context, pool *pgxpool.Pool, buildingID uuid.UUID, c seedCity, logger *slog.Logger) error {
	var id uuid.UUID
	err := pool.QueryRow(ctx, `
		SELECT id FROM world.network_nodes
		 WHERE building_id = $1 AND kind = 'distribution_center'
		 LIMIT 1`, buildingID).Scan(&id)
	switch {
	case err == nil:
		logger.Info("nodo del centro de distribución ya existía: omitido", slog.String("city", c.Name))
		return nil
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("seed: consultando el nodo del centro de %s: %w", c.Name, err)
	}
	id, err = newID()
	if err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO world.network_nodes (id, kind, region_id, building_id, city_id, location)
		VALUES ($1, 'distribution_center', $2, $3, $4, ST_GeomFromText($5, 0))`,
		id, c.RegionID, buildingID, c.ID, pointWKT(c.X, c.Y)); err != nil {
		return fmt.Errorf("seed: creando el nodo del centro de %s: %w", c.Name, err)
	}
	logger.Info("nodo del centro de distribución creado",
		slog.String("city", c.Name), slog.String("node_id", id.String()))
	return nil
}
