// Package seed siembra el mundo mínimo de desarrollo (target `make seed`,
// ADR-016): la fila única de world.sim_clock, la cuenta de sistema del banco
// central con sus cuentas contables de emisión y sink, y una cuenta demo con
// credencial argon2id, caja del ledger y capital semilla asentado como
// emisión explícita del banco central (GDD 5.5, ADR-010).
//
// Es una biblioteca de composición (como internal/gateway): la única capa que
// conoce a la vez auth, ledger y el reloj — los módulos no se importan entre
// sí (SAD v1.1 §7). La consumen cmd/seed y los tests E2E. Cada paso es
// idempotente: re-ejecutar el seed nunca duplica datos ni re-emite capital.
package seed

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

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
)

// Valores por defecto documentados.
const (
	DefaultDemoName   = "Demo"
	DefaultDemoSecret = "demo-secret-dev"
)

// CentralBankName es el nombre reservado de la cuenta de sistema del banco
// central (único por lower(name): es la clave de idempotencia).
const CentralBankName = "Banco Central"

// DemoSeedCapital es el capital semilla de la cuenta demo, en unidades
// menores de dinero (int64, nunca float). Se asienta UNA sola vez como
// emisión balanceada: +capital caja demo / -capital emisión del banco.
const DemoSeedCapital int64 = 1_000_000

// Options es la configuración del seed.
type Options struct {
	// DemoName es el nombre de la cuenta humana demo (II_SEED_DEMO_NAME).
	DemoName string
	// DemoSecret es el secreto de la cuenta demo (II_SEED_DEMO_SECRET).
	DemoSecret string
	// Ledger es la configuración del módulo ledger que usa el seed.
	Ledger ledger.Options
}

// OptionsFromEnv construye las Options desde el entorno con los defaults
// documentados, incluida la configuración del módulo ledger (II_LEDGER_*).
func OptionsFromEnv() (Options, error) {
	ledgerOpts, err := ledger.OptionsFromEnv()
	if err != nil {
		return Options{}, err
	}
	name := strings.TrimSpace(os.Getenv(EnvDemoName))
	if name == "" {
		name = DefaultDemoName
	}
	secret := os.Getenv(EnvDemoSecret)
	if secret == "" {
		secret = DefaultDemoSecret
	}
	return Options{DemoName: name, DemoSecret: secret, Ledger: ledgerOpts}, nil
}

// Run ejecuta el seed completo sobre el pool. Cada elemento se crea solo si
// no existe; el capital semilla se emite únicamente cuando la caja demo acaba
// de crearse. La guarda de entorno (rehusar en prod) es del composition root
// (cmd/seed), que es quien conoce II_ENV.
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
	if err := checkSchema(ctx, pool); err != nil {
		return err
	}

	// (a) Reloj de simulación: fila única en génesis si no existía.
	store := clock.NewStore(pool)
	if err := store.EnsureExists(ctx); err != nil {
		return err
	}
	logger.Info("world.sim_clock garantizado (génesis si no existía, ratio 24x)")

	repo := auth.NewPGRepository(pool)
	ledgerSvc := ledger.NewService(pool, opts.Ledger, nil)

	// (b) Banco central: cuenta de sistema sin canal privilegiado
	// (Arquitectura §9) con sus cuentas de emisión (única que puede quedar
	// negativa: es la masa emitida) y sink (destrucción de valor, GDD 5.5).
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

	// (c) Cuenta humana demo con credencial argon2id (crypto de internal/auth,
	// nunca duplicada aquí), caja del ledger y capital semilla.
	demo, _, err := ensureAuthAccount(ctx, repo, "human", opts.DemoName, logger)
	if err != nil {
		return err
	}
	secretHash, err := auth.HashSecret(opts.DemoSecret)
	if err != nil {
		return err
	}
	credCreated, err := repo.EnsureCredential(ctx, demo.ID, secretHash)
	if err != nil {
		return err
	}
	if credCreated {
		logger.Info("credencial demo creada (argon2id)", slog.String("account", demo.Name))
	} else {
		logger.Info("credencial demo ya existía: omitida (no se sobrescribe)",
			slog.String("account", demo.Name))
	}

	// La caja demo es la clave de idempotencia del capital semilla: si ya
	// existía, el capital ya se emitió en un seed anterior y no se re-emite.
	existing, _, err := ledgerSvc.ListAccounts(ctx, demo.ID, ledger.AccountFilter{
		Kind: ledger.AccountKindCash, Limit: 1,
	})
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		logger.Info("caja demo ya existía: capital semilla omitido",
			slog.String("account", demo.Name),
			slog.String("ledger_account_id", existing[0].ID.String()),
			slog.Int64("balance", existing[0].Balance))
		logger.Info("seed completado")
		return nil
	}

	cash, err := ledgerSvc.EnsureCashAccount(ctx, demo.ID)
	if err != nil {
		return err
	}
	logger.Info("caja demo creada", slog.String("ledger_account_id", cash.ID.String()))

	simNow, err := currentSimTime(ctx, store)
	if err != nil {
		return err
	}
	ref := demo.ID
	txID, err := ledgerSvc.PostTransaction(ctx, ledger.TransactionKindSeedCapital, simNow, &ref,
		"Capital semilla de la cuenta demo (emisión del banco central)",
		[]ledger.EntryInput{
			{AccountID: cash.ID, Amount: DemoSeedCapital},
			{AccountID: emission.ID, Amount: -DemoSeedCapital},
		})
	if err != nil {
		return err
	}
	logger.Info("capital semilla asentado",
		slog.String("account", demo.Name),
		slog.Int64("amount", DemoSeedCapital),
		slog.String("transaction_id", txID.String()),
		slog.Int64("sim_time_at", int64(simNow)))
	logger.Info("seed completado")
	return nil
}

// checkSchema comprueba que las migraciones ya crearon las tablas que el seed
// escribe, para guiar al target correcto (`make migrate-up`) en lugar de
// fallar con un error SQL críptico a mitad de siembra.
func checkSchema(ctx context.Context, pool *pgxpool.Pool) error {
	var ok bool
	err := pool.QueryRow(ctx, `
		SELECT to_regclass('auth.accounts')   IS NOT NULL
		   AND to_regclass('ledger.accounts') IS NOT NULL
		   AND to_regclass('world.sim_clock') IS NOT NULL`).Scan(&ok)
	if err != nil {
		return fmt.Errorf("seed: verificando el esquema: %w", err)
	}
	if !ok {
		return errors.New("seed: esquema incompleto: ejecuta antes `make migrate-up`")
	}
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
