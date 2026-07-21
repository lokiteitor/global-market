// Package seed siembra el mundo mínimo de desarrollo (target `make seed`,
// ADR-016): la fila única de world.sim_clock, la cuenta de sistema del banco
// central con sus cuentas contables de emisión y sink, dos corporaciones
// humanas (Demo y Norte Trading) con credencial argon2id, caja del ledger y
// capital semilla, y el mundo mínimo del Incremento 1 (GDD 5.3): la región
// Askadia, el catálogo de productos (iron_ore, coal) y de edificación
// (warehouse), una implantación completa por corporación (concesión de suelo
// → almacén operativo → nodo del grafo logístico) y el stock inicial asentado
// a la vez en el plano físico (world.building_inventories) y en el contable
// (+N stock_free / -N world_source, ADR-022).
//
// Es una biblioteca de composición (como internal/gateway): la única capa que
// conoce a la vez auth, ledger, world y el reloj — los módulos no se importan
// entre sí (SAD v1.1 §7). La consumen cmd/seed y los tests E2E. Cada paso es
// idempotente por clave natural (name/code): re-ejecutar el seed nunca
// duplica datos, re-emite capital ni re-asienta stock, y los IDs sembrados
// son estables entre ejecuciones.
package seed

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lokiteitor/global-market/backend/internal/auth"
	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/sim/clock"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// Variables de entorno propias del seed (prefijo II_, 12-factor).
const (
	// EnvDemoName es el nombre de la cuenta humana demo. Default "Demo".
	EnvDemoName = "II_SEED_DEMO_NAME"
	// EnvDemoSecret es el secreto de la cuenta demo. Default "demo-secret-dev"
	// (solo entornos de desarrollo: el seed rehúsa ejecutarse en prod).
	EnvDemoSecret = "II_SEED_DEMO_SECRET"
	// EnvTraderName es el nombre de la segunda corporación humana.
	// Default "Norte Trading".
	EnvTraderName = "II_SEED_TRADER_NAME"
	// EnvTraderSecret es el secreto de la segunda corporación.
	// Default "norte-secret-dev" (solo desarrollo).
	EnvTraderSecret = "II_SEED_TRADER_SECRET"
)

// Valores por defecto documentados.
const (
	DefaultDemoName     = "Demo"
	DefaultDemoSecret   = "demo-secret-dev"
	DefaultTraderName   = "Norte Trading"
	DefaultTraderSecret = "norte-secret-dev"
)

// CentralBankName es el nombre reservado de la cuenta de sistema del banco
// central (único por lower(name): es la clave de idempotencia).
const CentralBankName = "Banco Central"

// CorpSeedCapital es el capital semilla de cada corporación humana, en
// unidades menores de dinero (int64, nunca float). Se asienta UNA sola vez
// por corporación como emisión balanceada: +capital caja / -capital emisión.
const CorpSeedCapital int64 = 1_000_000

// CentralBankTreasury es la tesorería de sistema del banco central, en unidades
// menores de dinero. Es la garantía con la que el sistema publica sus ofertas
// sell en la subasta del stock embargado (Incremento 6a, GDD 11.2): cada oferta
// bloquea el 10% del valor como garantía, que el liquidador toma de esta caja.
// Se emite UNA sola vez —el banco central puede emitir para sí (Arquitectura
// §9)— como asiento balanceado +tesorería caja / -tesorería emisión. Si llegara
// a agotarse, el liquidador emite el faltante como colateral (system_liquidator,
// ensureGuaranteeCollateral); esta tesorería evita esa emisión en el caso común.
const CentralBankTreasury int64 = 1_000_000

// Options es la configuración del seed.
type Options struct {
	// DemoName es el nombre de la cuenta humana demo (II_SEED_DEMO_NAME).
	DemoName string
	// DemoSecret es el secreto de la cuenta demo (II_SEED_DEMO_SECRET).
	DemoSecret string
	// TraderName es el nombre de la segunda corporación (II_SEED_TRADER_NAME).
	TraderName string
	// TraderSecret es el secreto de la segunda corporación
	// (II_SEED_TRADER_SECRET).
	TraderSecret string
	// Ledger es la configuración del módulo ledger que usa el seed.
	Ledger ledger.Options
	// SkipIndustrialWorld omite el mundo industrial del Incremento 2 (catálogo de
	// producción, ciudad y yacimiento). Por defecto false: el seed lo incluye. Lo
	// activan los tests de integración de los subpaquetes world, que aportan sus
	// PROPIOS fixtures industriales (tipos/recetas/yacimientos con las mismas
	// claves naturales) y no quieren la versión canónica del seed.
	SkipIndustrialWorld bool
}

// OptionsFromEnv construye las Options desde el entorno con los defaults
// documentados, incluida la configuración del módulo ledger (II_LEDGER_*).
func OptionsFromEnv() (Options, error) {
	ledgerOpts, err := ledger.OptionsFromEnv()
	if err != nil {
		return Options{}, err
	}
	return Options{
		DemoName:     nameOrDefault(EnvDemoName, DefaultDemoName),
		DemoSecret:   secretOrDefault(EnvDemoSecret, DefaultDemoSecret),
		TraderName:   nameOrDefault(EnvTraderName, DefaultTraderName),
		TraderSecret: secretOrDefault(EnvTraderSecret, DefaultTraderSecret),
		Ledger:       ledgerOpts,
	}, nil
}

// nameOrDefault lee un nombre del entorno (recortado); vacío = default.
func nameOrDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// secretOrDefault lee un secreto del entorno tal cual (sin recortar: un
// secreto puede contener espacios); vacío = default.
func secretOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Run ejecuta el seed completo sobre el pool. Cada elemento se crea solo si
// no existe (clave natural: name/code); el capital semilla y el stock inicial
// se asientan una única vez. La guarda de entorno (rehusar en prod) es del
// composition root (cmd/seed), que es quien conoce II_ENV.
func Run(ctx context.Context, pool *pgxpool.Pool, opts Options, logger *slog.Logger) error {
	if pool == nil {
		return errors.New("seed: el pool de BD es obligatorio")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if opts.DemoName == "" || opts.DemoSecret == "" {
		return errors.New("seed: DemoName y DemoSecret no pueden estar vacíos")
	}
	if opts.TraderName == "" || opts.TraderSecret == "" {
		return errors.New("seed: TraderName y TraderSecret no pueden estar vacíos")
	}
	if err := checkSchema(ctx, pool); err != nil {
		return err
	}

	// (a) Reloj de simulación: fila única en génesis si no existía. El resto
	// del seed asienta con el sim-time derivado de este ancla.
	store := clock.NewStore(pool)
	if err := store.EnsureExists(ctx); err != nil {
		return err
	}
	logger.Info("world.sim_clock garantizado (génesis si no existía, ratio 24x)")
	simNow, err := currentSimTime(ctx, store)
	if err != nil {
		return err
	}

	repo := auth.NewPGRepository(pool)
	ledgerSvc := ledger.NewService(pool, opts.Ledger, nil)

	// (b) Banco central: cuenta de sistema sin canal privilegiado
	// (Arquitectura §9) con sus cuentas de emisión (única cuenta monetaria que
	// puede quedar negativa: es la masa emitida) y sink (destrucción de valor,
	// GDD 5.5). Las world_source por producto (ADR-022) se crean con el stock.
	bank, _, err := ensureAuthAccount(ctx, repo, "system", CentralBankName, logger)
	if err != nil {
		return err
	}
	emission, err := ensureLedgerAccount(ctx, ledgerSvc, ledger.AccountKindEmission, bank, logger)
	if err != nil {
		return err
	}
	if _, err := ensureLedgerAccount(ctx, ledgerSvc, ledger.AccountKindSink, bank, logger); err != nil {
		return err
	}

	// (b') Tesorería del banco central: caja de sistema con capital de garantía,
	// emitido una única vez (el banco central emite para sí). Habilita al sistema
	// a publicar ofertas sell en la subasta del stock embargado sin depender de
	// emitir colateral por operación (Incremento 6a, GDD 11.2).
	if err := ensureCentralBankTreasury(ctx, ledgerSvc, bank, emission, simNow, logger); err != nil {
		return err
	}

	// (c) Corporaciones humanas jugables: credencial argon2id, caja del
	// ledger y capital semilla (una emisión por corporación, jamás re-emitida).
	demo, err := ensureCorporation(ctx, repo, ledgerSvc, emission, opts.DemoName, opts.DemoSecret, simNow, logger)
	if err != nil {
		return err
	}
	trader, err := ensureCorporation(ctx, repo, ledgerSvc, emission, opts.TraderName, opts.TraderSecret, simNow, logger)
	if err != nil {
		return err
	}

	// (d) Mundo estático del Incremento 1: región, productos y tipo de almacén.
	cat, err := ensureWorldCatalog(ctx, pool, logger)
	if err != nil {
		return err
	}

	// (e) Implantación física + stock inicial por corporación, en ubicaciones
	// separadas dentro de Askadia para que los contratos BUY con tránsito
	// (origen ≠ destino) sean posibles desde el primer día.
	placements := []struct {
		corp             auth.Account
		centerX, centerY int64
	}{
		{demo, demoCenterX, demoCenterY},
		{trader, norteCenterX, norteCenterY},
	}
	sites := make(map[uuid.UUID]corpSite, len(placements))
	for _, p := range placements {
		site, err := ensureCorpSite(ctx, pool, cat, p.corp, p.centerX, p.centerY, logger)
		if err != nil {
			return err
		}
		if err := ensureInitialStock(ctx, pool, ledgerSvc, bank, p.corp, site, cat, simNow, logger); err != nil {
			return err
		}
		sites[p.corp.ID] = site
	}

	// (f) Mundo industrial del Incremento 2: catálogo de producción (steel_ingot,
	// iron_mine, blast_furnace), recetas (mine_iron, smelt_steel), la ciudad
	// consumidora Nueva Askadia con su demanda y el yacimiento finito de iron_ore
	// en una parcela libre reservada para levantar una mina.
	if !opts.SkipIndustrialWorld {
		if err := ensureIndustrialWorld(ctx, pool, repo, cat, logger); err != nil {
			return err
		}

		// (g) Red vial de Askadia (Incremento 3, Fase 1: logística terrestre): un
		// junction central y enlaces road bidireccionales warehouse(Demo) —
		// junction — warehouse(Norte), cada uno con su segmento, más el catálogo de
		// vehículos terrestres. Se omite junto al mundo industrial: los tests de
		// integración de los subpaquetes world aportan sus propios fixtures de red
		// y flota.
		if err := ensureLogisticsNetwork(ctx, pool, cat, sites[demo.ID], sites[trader.ID], logger); err != nil {
			return err
		}

		// (h) Infraestructura urbana del Incremento 6b (ECONOMY BALANCER): por
		// cada ciudad, su centro de distribución (concesión del sistema + edificio
		// de la ciudad + nodo del grafo) —destino de las buys que publica el
		// Balancer— y su caja con capital inicial (faucet). Depende de la ciudad
		// del mundo industrial (por eso comparte su gate).
		if err := ensureCityInfrastructure(ctx, pool, ledgerSvc, bank, emission, cat, simNow, logger); err != nil {
			return err
		}
	}

	logger.Info("seed completado")
	return nil
}

// checkSchema comprueba que las migraciones ya crearon las tablas y tipos que
// el seed escribe, para guiar al target correcto (`make migrate-up`) en lugar
// de fallar con un error SQL críptico a mitad de siembra.
func checkSchema(ctx context.Context, pool *pgxpool.Pool) error {
	var ok bool
	err := pool.QueryRow(ctx, `
		SELECT to_regclass('auth.accounts')              IS NOT NULL
		   AND to_regclass('ledger.accounts')            IS NOT NULL
		   AND to_regclass('world.sim_clock')            IS NOT NULL
		   AND to_regclass('world.regions')              IS NOT NULL
		   AND to_regclass('world.products')             IS NOT NULL
		   AND to_regclass('world.building_types')       IS NOT NULL
		   AND to_regclass('world.land_concessions')     IS NOT NULL
		   AND to_regclass('world.buildings')            IS NOT NULL
		   AND to_regclass('world.building_inventories') IS NOT NULL
		   AND to_regclass('world.network_nodes')        IS NOT NULL
		   AND to_regclass('world.recipes')              IS NOT NULL
		   AND to_regclass('world.recipe_ingredients')   IS NOT NULL
		   AND to_regclass('world.resource_deposits')    IS NOT NULL
		   AND to_regclass('world.cities')               IS NOT NULL
		   AND to_regclass('world.city_demand')          IS NOT NULL`).Scan(&ok)
	if err != nil {
		return fmt.Errorf("seed: verificando el esquema: %w", err)
	}
	if !ok {
		return errors.New("seed: esquema incompleto: ejecuta antes `make migrate-up`")
	}
	// El stock inicial exige la contrapartida física del ledger: el kind
	// world_source del enum ledger.account_kind (ADR-022, migración 0008).
	err = pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			  FROM pg_enum e
			  JOIN pg_type t ON t.oid = e.enumtypid
			  JOIN pg_namespace n ON n.oid = t.typnamespace
			 WHERE n.nspname = 'ledger' AND t.typname = 'account_kind'
			   AND e.enumlabel = 'world_source')`).Scan(&ok)
	if err != nil {
		return fmt.Errorf("seed: verificando ledger.account_kind: %w", err)
	}
	if !ok {
		return errors.New("seed: falta el kind world_source en ledger.account_kind (ADR-022, migración 0008): ejecuta antes `make migrate-up`")
	}
	return nil
}

// ensureCorporation garantiza una corporación humana jugable completa: cuenta
// de auth, credencial argon2id (crypto de internal/auth, nunca sobrescrita),
// caja del ledger y capital semilla emitido por el banco central. La caja es
// la clave de idempotencia del capital: si ya existía, el capital ya se
// emitió en un seed anterior y no se re-emite.
func ensureCorporation(ctx context.Context, repo *auth.PGRepository, ledgerSvc *ledger.Service, emission ledger.Account, name, secret string, simNow simtime.SimTime, logger *slog.Logger) (auth.Account, error) {
	acc, _, err := ensureAuthAccount(ctx, repo, "human", name, logger)
	if err != nil {
		return auth.Account{}, err
	}
	secretHash, err := auth.HashSecret(secret)
	if err != nil {
		return auth.Account{}, err
	}
	credCreated, err := repo.EnsureCredential(ctx, acc.ID, secretHash)
	if err != nil {
		return auth.Account{}, err
	}
	if credCreated {
		logger.Info("credencial creada (argon2id)", slog.String("account", acc.Name))
	} else {
		logger.Info("credencial ya existía: omitida (no se sobrescribe)",
			slog.String("account", acc.Name))
	}

	existing, _, err := ledgerSvc.ListAccounts(ctx, acc.ID, ledger.AccountFilter{
		Kind: ledger.AccountKindCash, Limit: 1,
	})
	if err != nil {
		return auth.Account{}, err
	}
	if len(existing) > 0 {
		logger.Info("caja ya existía: capital semilla omitido",
			slog.String("account", acc.Name),
			slog.String("ledger_account_id", existing[0].ID.String()),
			slog.Int64("balance", existing[0].Balance))
		return acc, nil
	}

	cash, err := ledgerSvc.EnsureCashAccount(ctx, acc.ID)
	if err != nil {
		return auth.Account{}, err
	}
	logger.Info("caja creada",
		slog.String("account", acc.Name),
		slog.String("ledger_account_id", cash.ID.String()))

	ref := acc.ID
	txID, err := ledgerSvc.PostTransaction(ctx, ledger.TransactionKindSeedCapital, simNow, &ref,
		fmt.Sprintf("Capital semilla de %s (emisión del banco central)", acc.Name),
		[]ledger.EntryInput{
			{AccountID: cash.ID, Amount: CorpSeedCapital},
			{AccountID: emission.ID, Amount: -CorpSeedCapital},
		})
	if err != nil {
		return auth.Account{}, err
	}
	logger.Info("capital semilla asentado",
		slog.String("account", acc.Name),
		slog.Int64("amount", CorpSeedCapital),
		slog.String("transaction_id", txID.String()),
		slog.Int64("sim_time_at", int64(simNow)))
	return acc, nil
}

// ensureCentralBankTreasury garantiza la caja de sistema del banco central con
// su tesorería de garantía (CentralBankTreasury), emitida una única vez. La
// existencia de la caja es la clave de idempotencia del capital: si ya existía,
// la tesorería se emitió en un seed anterior y no se re-emite (ni en re-arranques
// ni tras cobrar proceeds de subastas). El asiento es balanceado
// (+tesorería caja / −tesorería emisión), así el ledger sigue cerrando a cero
// por dinero (la emisión es la única cuenta monetaria que puede quedar negativa).
func ensureCentralBankTreasury(ctx context.Context, ledgerSvc *ledger.Service, bank auth.Account, emission ledger.Account, simNow simtime.SimTime, logger *slog.Logger) error {
	existing, _, err := ledgerSvc.ListAccounts(ctx, bank.ID, ledger.AccountFilter{
		Kind: ledger.AccountKindCash, Limit: 1,
	})
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		logger.Info("tesorería del banco central ya existía: emisión omitida",
			slog.String("account", bank.Name),
			slog.String("ledger_account_id", existing[0].ID.String()),
			slog.Int64("balance", existing[0].Balance))
		return nil
	}

	cash, err := ledgerSvc.EnsureCashAccount(ctx, bank.ID)
	if err != nil {
		return err
	}
	ref := bank.ID
	txID, err := ledgerSvc.PostTransaction(ctx, ledger.TransactionKindSeedCapital, simNow, &ref,
		"Tesorería de garantía del banco central (emisión para sí)",
		[]ledger.EntryInput{
			{AccountID: cash.ID, Amount: CentralBankTreasury},
			{AccountID: emission.ID, Amount: -CentralBankTreasury},
		})
	if err != nil {
		return err
	}
	logger.Info("tesorería del banco central asentada",
		slog.String("account", bank.Name),
		slog.Int64("amount", CentralBankTreasury),
		slog.String("ledger_account_id", cash.ID.String()),
		slog.String("transaction_id", txID.String()),
		slog.Int64("sim_time_at", int64(simNow)))
	return nil
}

// ensureAuthAccount devuelve la cuenta de auth con ese nombre, creándola con
// el kind dado si no existe (clave de idempotencia: unicidad de lower(name)).
func ensureAuthAccount(ctx context.Context, repo *auth.PGRepository, kind, name string, logger *slog.Logger) (auth.Account, bool, error) {
	acc, err := repo.GetAccountByName(ctx, name)
	switch {
	case err == nil:
		logger.Info("cuenta ya existía: omitida",
			slog.String("account", name), slog.String("kind", acc.Kind))
		return acc, false, nil
	case !errors.Is(err, auth.ErrNotFound):
		return auth.Account{}, false, err
	}
	acc, err = repo.CreateAccount(ctx, kind, name)
	if err != nil {
		return auth.Account{}, false, err
	}
	logger.Info("cuenta creada",
		slog.String("account", name), slog.String("kind", kind),
		slog.String("account_id", acc.ID.String()))
	return acc, true, nil
}

// ensureLedgerAccount devuelve la cuenta del ledger (kind, titular),
// creándola si no existe. Cubre las cuentas monetarias del banco central
// (emission, sink: sin producto ni almacén, ck_accounts_asset).
func ensureLedgerAccount(ctx context.Context, svc *ledger.Service, kind ledger.AccountKind, owner auth.Account, logger *slog.Logger) (ledger.Account, error) {
	existing, _, err := svc.ListAccounts(ctx, owner.ID, ledger.AccountFilter{Kind: kind, Limit: 1})
	if err != nil {
		return ledger.Account{}, err
	}
	if len(existing) > 0 {
		logger.Info("cuenta ledger ya existía: omitida",
			slog.String("owner", owner.Name), slog.String("kind", string(kind)))
		return existing[0], nil
	}
	ownerID := owner.ID
	acc, err := svc.CreateAccount(ctx, kind, &ownerID, nil, nil, nil)
	if err != nil {
		return ledger.Account{}, err
	}
	logger.Info("cuenta ledger creada",
		slog.String("owner", owner.Name), slog.String("kind", string(kind)),
		slog.String("ledger_account_id", acc.ID.String()))
	return acc, nil
}

// currentSimTime deriva el sim-time actual desde el ancla persistida en
// world.sim_clock (la fila que este mismo seed garantiza).
func currentSimTime(ctx context.Context, store *clock.Store) (simtime.SimTime, error) {
	a, err := store.Load(ctx)
	if err != nil {
		return 0, err
	}
	return simtime.Derive(a.SimTimeAt, a.WallAnchor, time.Now(), a.Ratio, a.Frozen), nil
}
