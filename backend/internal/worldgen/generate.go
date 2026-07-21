// Package worldgen genera proceduralmente el mundo multi-región del Incremento 7
// (FASE 2 MUNDO, GDD 9) de forma DETERMINISTA, IDEMPOTENTE y ADITIVA: a partir de
// una semilla (II_WORLD_SEED) construye una grilla de macro-regiones centrada en
// Askadia (0,0) y, para cada celda distinta del origen, una región con su bioma
// (value-noise en el centro de la celda), sus ciudades (consumidores finales con
// centro de distribución y caja prefondeada), sus yacimientos finitos (recurso
// correlado al bioma), su red vial intra-región y —en la fase de red— sus enlaces
// inter-región ferroviarios/marítimos con segmentos partidos por la frontera y
// terminales intermodales de transbordo (GDD 7.2/7.3, 15.1).
//
// NO toca Askadia ni su seed (región 0,0, sus edificios, nodos y red road): solo
// añade regiones alrededor y conecta el junction existente de Askadia a sus
// vecinas por rail/sea. Reutiliza el catálogo del seed (productos iron_ore/coal,
// building_types) sin duplicarlo. Es una biblioteca de composición (como
// internal/seed): la única capa que conoce a la vez auth, ledger, world y el
// reloj. Cada pieza se localiza por su clave natural antes de crearse: re-ejecutar
// el generador nunca duplica y produce el MISMO mundo. Geometrías: SRID 0 planar,
// metros de mundo (ADR-019); dinero/stock int64.
package worldgen

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lokiteitor/global-market/backend/internal/auth"
	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/sim/clock"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// CentralBankName es el nombre reservado de la cuenta de sistema del banco
// central (dueño de las terminales y emisor de la caja de las ciudades). Debe
// coincidir con el del seed: el generador exige que el seed ya haya corrido.
const CentralBankName = "Banco Central"

// AskadiaGridX/Y es la celda raíz fija que el generador conserva intacta.
const (
	AskadiaGridX = 0
	AskadiaGridY = 0
)

// Constantes del centro de distribución de las ciudades generadas (réplica de las
// del seed: el ensure por code reutiliza el tipo ya sembrado). El centro es
// infraestructura del sistema (build/maintenance 0) sobre una concesión del banco
// central de vencimiento lejano; la ciudad es su dueña y sumidero final.
const (
	distCenterTypeCode             = "distribution_center"
	distCenterTypeName             = "Centro de distribución"
	distCenterFootprintCells       = 6
	distCenterMaxLevel             = 1
	distCenterBaseStorage    int64 = 1_000_000
	distCenterParcelHalfM    int64 = 400
	distCenterFootprintHalfM int64 = 150

	distConcessionCanon        int64 = 1
	distConcessionPeriodDays   int64 = 36_000
	distConcessionGrantedAtSim int64 = 0
)

// distConcessionExpiresAtSim: vencimiento lejano (infraestructura permanente).
var distConcessionExpiresAtSim = 100 * simtime.SimYear

// CityInitialCapital es la emisión inicial de caja de cada ciudad generada
// (int64), asentada una vez como emisión del banco central (faucet). Igual que el
// seed: pre-fondea las compras de la ciudad desde el primer día.
const CityInitialCapital int64 = 10_000_000

// Summary resume el mundo generado para el log y el retorno de cmd/worldgen.
type Summary struct {
	Seed             int64
	Grid             int
	RegionSizeM      int64
	RegionsCreated   int
	CitiesCreated    int
	DepositsCreated  int
	RailLinks        int
	SeaLinks         int
	TerminalsCreated int
	SlotsCreated     int
	Regions          []RegionSummary
}

// RegionSummary describe una región generada (o Askadia) en el resumen.
type RegionSummary struct {
	GridX, GridY int
	Name         string
	Biome        string
	Terrestrial  bool
}

// genRegion es el estado de una región ya presente en el mundo (generada o
// Askadia) que la fase de red necesita para tender los enlaces inter-región.
type genRegion struct {
	GridX, GridY         int
	Biome                string
	RegionID             uuid.UUID
	JunctionID           uuid.UUID
	JunctionX, JunctionY int64
	Terrestrial          bool
}

// genState reúne los handles compartidos de una ejecución del generador.
type genState struct {
	pool     *pgxpool.Pool
	ledger   *ledger.Service
	authRepo *auth.PGRepository
	bank     auth.Account
	emission ledger.Account
	simNow   simtime.SimTime
	noise    *noise
	opts     Options
	logger   *slog.Logger

	ironOreID  uuid.UUID
	coalID     uuid.UUID
	distTypeID uuid.UUID

	regions map[[2]int]*genRegion
	summary *Summary
}

// Generate construye (o completa idempotentemente) el mundo multi-región sobre el
// pool. Exige que el seed haya corrido (banco central, reloj y catálogo mínimo).
// Devuelve el resumen del mundo. Seguro de re-ejecutar: misma semilla ⇒ mismo
// mundo, sin duplicados.
func Generate(ctx context.Context, pool *pgxpool.Pool, opts Options, logger *slog.Logger) (Summary, error) {
	if pool == nil {
		return Summary{}, errors.New("worldgen: el pool de BD es obligatorio")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if err := opts.Validate(); err != nil {
		return Summary{}, err
	}

	st, err := resolveState(ctx, pool, opts, logger)
	if err != nil {
		return Summary{}, err
	}

	// (a) Catálogo aditivo: tipos de vehículo rail/sea y el tipo de edificación
	//     del centro de distribución (idempotentes por code).
	if err := ensureRailSeaVehicleTypes(ctx, st); err != nil {
		return Summary{}, err
	}
	distTypeID, err := ensureDistCenterType(ctx, st)
	if err != nil {
		return Summary{}, err
	}
	st.distTypeID = distTypeID

	// (b) Askadia (0,0): se carga su región y su junction existentes para la fase
	//     de red, SIN modificar nada.
	if err := loadAskadia(ctx, st); err != nil {
		return Summary{}, err
	}

	// (c) Grilla centrada en (0,0): genera cada celda != (0,0).
	half := opts.half()
	for gy := -half; gy <= half; gy++ {
		for gx := -half; gx <= half; gx++ {
			if gx == AskadiaGridX && gy == AskadiaGridY {
				continue
			}
			if err := generateRegion(ctx, st, gx, gy); err != nil {
				return Summary{}, fmt.Errorf("worldgen: generando la región (%d,%d): %w", gx, gy, err)
			}
		}
	}

	// (d) Fase de red: enlaces inter-región rail/sea con segmentos partidos por la
	//     frontera y terminales intermodales (network.go).
	if err := connectRegions(ctx, st); err != nil {
		return Summary{}, err
	}

	logger.Info("worldgen: mundo generado",
		slog.Int64("seed", opts.Seed), slog.Int("grid", opts.Grid),
		slog.Int("regiones_creadas", st.summary.RegionsCreated),
		slog.Int("ciudades", st.summary.CitiesCreated),
		slog.Int("yacimientos", st.summary.DepositsCreated),
		slog.Int("enlaces_rail", st.summary.RailLinks),
		slog.Int("enlaces_sea", st.summary.SeaLinks),
		slog.Int("terminales", st.summary.TerminalsCreated),
		slog.Int("slots", st.summary.SlotsCreated))
	return *st.summary, nil
}

// resolveState resuelve los handles compartidos: banco central y su cuenta de
// emisión, catálogo mínimo (iron_ore, coal) y sim-time. Falla con un mensaje
// claro si el seed no ha corrido.
func resolveState(ctx context.Context, pool *pgxpool.Pool, opts Options, logger *slog.Logger) (*genState, error) {
	authRepo := auth.NewPGRepository(pool)
	bank, err := authRepo.GetAccountByName(ctx, CentralBankName)
	if err != nil {
		if errors.Is(err, auth.ErrNotFound) {
			return nil, fmt.Errorf("worldgen: no existe el banco central %q: ejecuta antes el seed", CentralBankName)
		}
		return nil, fmt.Errorf("worldgen: resolviendo el banco central: %w", err)
	}

	ledgerSvc := ledger.NewService(pool, opts.Ledger, nil)
	emissions, _, err := ledgerSvc.ListAccounts(ctx, bank.ID, ledger.AccountFilter{Kind: ledger.AccountKindEmission, Limit: 1})
	if err != nil {
		return nil, fmt.Errorf("worldgen: resolviendo la cuenta de emisión: %w", err)
	}
	if len(emissions) == 0 {
		return nil, errors.New("worldgen: el banco central no tiene cuenta de emisión: ejecuta antes el seed")
	}

	ironOreID, err := productID(ctx, pool, "iron_ore")
	if err != nil {
		return nil, err
	}
	coalID, err := productID(ctx, pool, "coal")
	if err != nil {
		return nil, err
	}

	simNow, err := currentSimTime(ctx, pool)
	if err != nil {
		return nil, err
	}

	return &genState{
		pool:      pool,
		ledger:    ledgerSvc,
		authRepo:  authRepo,
		bank:      bank,
		emission:  emissions[0],
		simNow:    simNow,
		noise:     newNoise(opts.Seed),
		opts:      opts,
		logger:    logger,
		ironOreID: ironOreID,
		coalID:    coalID,
		regions:   make(map[[2]int]*genRegion),
		summary:   &Summary{Seed: opts.Seed, Grid: opts.Grid, RegionSizeM: opts.RegionSizeM},
	}, nil
}

// productID resuelve el id de un producto del catálogo por su code (debe existir:
// lo siembra el seed).
func productID(ctx context.Context, pool *pgxpool.Pool, code string) (uuid.UUID, error) {
	var id uuid.UUID
	err := pool.QueryRow(ctx, `SELECT id FROM world.products WHERE code = $1`, code).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, fmt.Errorf("worldgen: falta el producto %q del catálogo: ejecuta antes el seed", code)
		}
		return uuid.Nil, fmt.Errorf("worldgen: resolviendo el producto %q: %w", code, err)
	}
	return id, nil
}

// currentSimTime deriva el sim-time actual desde el ancla persistida en
// world.sim_clock (que el seed garantiza).
func currentSimTime(ctx context.Context, pool *pgxpool.Pool) (simtime.SimTime, error) {
	a, err := clock.NewStore(pool).Load(ctx)
	if err != nil {
		return 0, fmt.Errorf("worldgen: cargando el reloj de simulación (¿ejecutaste el seed?): %w", err)
	}
	return simtime.Derive(a.SimTimeAt, a.WallAnchor, time.Now(), a.Ratio, a.Frozen), nil
}

// loadAskadia carga la región raíz (0,0) y su junction existentes en el estado,
// para conectarlos por rail/sea en la fase de red. No modifica nada de Askadia.
func loadAskadia(ctx context.Context, st *genState) error {
	var (
		regionID uuid.UUID
		biome    string
		name     string
	)
	err := st.pool.QueryRow(ctx, `
		SELECT id, name, biome::text FROM world.regions WHERE grid_x = $1 AND grid_y = $2`,
		AskadiaGridX, AskadiaGridY).Scan(&regionID, &name, &biome)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("worldgen: no existe la región raíz Askadia (0,0): ejecuta antes el seed")
		}
		return fmt.Errorf("worldgen: cargando Askadia: %w", err)
	}
	var jID uuid.UUID
	var jx, jy int64
	err = st.pool.QueryRow(ctx, `
		SELECT id, ST_X(location)::bigint, ST_Y(location)::bigint
		  FROM world.network_nodes
		 WHERE region_id = $1 AND kind = 'junction'
		 ORDER BY id LIMIT 1`, regionID).Scan(&jID, &jx, &jy)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("worldgen: Askadia no tiene junction: ejecuta antes el seed (red vial)")
		}
		return fmt.Errorf("worldgen: cargando el junction de Askadia: %w", err)
	}
	st.regions[[2]int{AskadiaGridX, AskadiaGridY}] = &genRegion{
		GridX: AskadiaGridX, GridY: AskadiaGridY, Biome: biome,
		RegionID: regionID, JunctionID: jID, JunctionX: jx, JunctionY: jy,
		Terrestrial: isTerrestrial(biome),
	}
	st.summary.Regions = append(st.summary.Regions, RegionSummary{
		GridX: AskadiaGridX, GridY: AskadiaGridY, Name: name, Biome: biome, Terrestrial: isTerrestrial(biome),
	})
	return nil
}

// generateRegion genera (idempotentemente) una celda != (0,0): región, junction
// central, ciudades, yacimientos y red vial intra-región. Registra la región en
// el estado para la fase de red.
func generateRegion(ctx context.Context, st *genState, gx, gy int) error {
	size := st.opts.RegionSizeM
	minX := int64(gx) * size
	minY := int64(gy) * size
	centerX := minX + size/2
	centerY := minY + size/2

	// Bioma por value-noise en el centro de la celda (espacio de ruido = coords de
	// mundo / lado de región, para que celdas contiguas varíen suavemente).
	nx := float64(centerX) / float64(size)
	ny := float64(centerY) / float64(size)
	biome := biomeFor(st.noise.elevation(nx, ny), st.noise.humidity(nx, ny))
	params := paramsForBiome(biome)

	regionID, name, created, err := ensureRegionRow(ctx, st, gx, gy, minX, minY, biome, params)
	if err != nil {
		return err
	}
	if created {
		st.summary.RegionsCreated++
	}
	st.summary.Regions = append(st.summary.Regions, RegionSummary{
		GridX: gx, GridY: gy, Name: name, Biome: biome, Terrestrial: isTerrestrial(biome),
	})

	// Junction central de la región (hub de la red): presente en toda región,
	// incluidas las oceánicas (waypoint marítimo).
	junctionID, err := ensureCentralJunction(ctx, st, regionID, centerX, centerY)
	if err != nil {
		return err
	}

	reg := &genRegion{
		GridX: gx, GridY: gy, Biome: biome, RegionID: regionID,
		JunctionID: junctionID, JunctionX: centerX, JunctionY: centerY,
		Terrestrial: isTerrestrial(biome),
	}
	st.regions[[2]int{gx, gy}] = reg

	// Las regiones oceánicas son agua: sin ciudades ni yacimientos ni red vial.
	if !reg.Terrestrial {
		return nil
	}

	rng := cellRNG(st.opts.Seed, gx, gy)
	margin := size / 6

	// Ciudades (1-2 según bioma), cada una con centro de distribución, caja
	// prefondeada, demanda base y enlace vial al junction.
	nCities := intInRange(rng, params.cityMin, params.cityMax)
	for i := 0; i < nCities; i++ {
		cx := minX + margin + int64(rng.Intn(int(size-2*margin)))
		cy := minY + margin + int64(rng.Intn(int(size-2*margin)))
		if err := ensureGeneratedCity(ctx, st, reg, params, i, cx, cy); err != nil {
			return err
		}
	}

	// Yacimientos (2-4 según bioma), cada uno con un nodo de acceso vial al junction.
	nDeposits := intInRange(rng, params.depositMin, params.depositMax)
	products := depositProducts(biome, rng, st.ironOreID, st.coalID, nDeposits)
	for i := 0; i < nDeposits; i++ {
		dx := minX + margin + int64(rng.Intn(int(size-2*margin)))
		dy := minY + margin + int64(rng.Intn(int(size-2*margin)))
		if err := ensureGeneratedDeposit(ctx, st, reg, products[i], dx, dy); err != nil {
			return err
		}
	}
	return nil
}
