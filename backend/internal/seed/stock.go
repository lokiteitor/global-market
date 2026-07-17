package seed

// Stock inicial del Incremento 1 (ADR-022): el alta de stock es UN hecho
// económico con dos planos que deben moverse juntos — el físico (fila de
// world.building_inventories en el almacén) y el contable (asiento
// production_output: +N stock_free de la corporación / -N world_source del
// producto). world_source es la contrapartida fiat del banco central, una por
// producto y única cuenta de stock que puede quedar negativa: su saldo
// negativo es exactamente el stock neto emitido al mundo.
//
// Idempotencia: la clave del asiento contable es la existencia de CUALQUIER
// partida production_output sobre la cuenta stock_free del (corporación,
// producto, almacén); la del plano físico, la PK de building_inventories
// (ON CONFLICT DO NOTHING). Ambas se reevalúan por separado en cada
// ejecución, de modo que un seed interrumpido a medias converge a un estado
// coherente físico↔contable al re-ejecutarse.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lokiteitor/global-market/backend/internal/auth"
	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// sqlstateUniqueViolation es el SQLSTATE de unique_violation, usado para
// resolver carreras de creación entre procesos de seed concurrentes.
const sqlstateUniqueViolation = "23505"

// ensureInitialStock garantiza el stock inicial de la corporación en su
// almacén, producto a producto: cuenta world_source del banco central (una
// por producto), cuenta stock_free (corp, producto, almacén), asiento
// production_output y fila física de inventario.
func ensureInitialStock(ctx context.Context, pool *pgxpool.Pool, ledgerSvc *ledger.Service, bank, corp auth.Account, site corpSite, cat worldCatalog, simNow simtime.SimTime, logger *slog.Logger) error {
	for _, p := range cat.Products {
		worldSourceID, err := ensureWorldSourceAccount(ctx, pool, bank, p, logger)
		if err != nil {
			return err
		}
		stockFree, err := ensureStockFreeAccount(ctx, ledgerSvc, corp, p, site.BuildingID, logger)
		if err != nil {
			return err
		}

		funded, err := hasProductionOutput(ctx, pool, stockFree.ID)
		if err != nil {
			return err
		}
		if funded {
			logger.Info("stock inicial ya asentado: omitido",
				slog.String("account", corp.Name),
				slog.String("product", p.code),
				slog.Int64("balance", stockFree.Balance))
		} else {
			ref := site.BuildingID
			txID, err := ledgerSvc.PostTransaction(ctx, ledger.TransactionKindProductionOutput, simNow, &ref,
				fmt.Sprintf("Stock inicial de desarrollo: %d %s en el almacén de %s", p.initialStock, p.code, corp.Name),
				[]ledger.EntryInput{
					{AccountID: stockFree.ID, Amount: p.initialStock},
					{AccountID: worldSourceID, Amount: -p.initialStock},
				})
			if err != nil {
				return err
			}
			logger.Info("stock inicial asentado (+stock_free / -world_source)",
				slog.String("account", corp.Name),
				slog.String("product", p.code),
				slog.Int64("quantity", p.initialStock),
				slog.String("transaction_id", txID.String()))
		}

		if err := ensureInventoryRow(ctx, pool, site.BuildingID, corp, p, simNow, logger); err != nil {
			return err
		}
	}
	return nil
}

// ensureWorldSourceAccount garantiza la cuenta world_source del producto
// (ADR-022): titular el banco central, una por producto. El alta es SQL
// directo (y no ledger.Service.CreateAccount) porque el kind world_source
// nace con la migración 0008 y aún no forma parte del API del módulo ledger;
// se sigue la misma convención de IDs (UUIDv7 de aplicación, ADR-018).
func ensureWorldSourceAccount(ctx context.Context, pool *pgxpool.Pool, bank auth.Account, p seededProduct, logger *slog.Logger) (uuid.UUID, error) {
	const selectSQL = `
		SELECT id FROM ledger.accounts
		 WHERE kind = 'world_source' AND product_id = $1
		 LIMIT 1`
	var id uuid.UUID
	err := pool.QueryRow(ctx, selectSQL, p.ID).Scan(&id)
	switch {
	case err == nil:
		logger.Info("cuenta world_source ya existía: omitida", slog.String("product", p.code))
		return id, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return uuid.Nil, fmt.Errorf("seed: consultando la cuenta world_source de %s: %w", p.code, err)
	}

	id, err = newID()
	if err != nil {
		return uuid.Nil, err
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO ledger.accounts (id, kind, owner_account_id, product_id)
		VALUES ($1, 'world_source', $2, $3)`, id, bank.ID, p.ID)
	if err == nil {
		logger.Info("cuenta world_source creada",
			slog.String("product", p.code),
			slog.String("ledger_account_id", id.String()))
		return id, nil
	}
	// Carrera con otro proceso de seed: si una unicidad de la BD la creó
	// primero, se relee la ganadora.
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == sqlstateUniqueViolation {
		if selErr := pool.QueryRow(ctx, selectSQL, p.ID).Scan(&id); selErr == nil {
			logger.Info("cuenta world_source creada por otro proceso: releída",
				slog.String("product", p.code))
			return id, nil
		}
	}
	return uuid.Nil, fmt.Errorf("seed: creando la cuenta world_source de %s: %w", p.code, err)
}

// ensureStockFreeAccount garantiza la cuenta stock_free de la corporación
// para (producto, almacén), vía el API del módulo ledger (la unicidad parcial
// ux_accounts_stock_free respalda la clave en la BD).
func ensureStockFreeAccount(ctx context.Context, ledgerSvc *ledger.Service, corp auth.Account, p seededProduct, warehouseID uuid.UUID, logger *slog.Logger) (ledger.Account, error) {
	productID := p.ID
	filter := ledger.AccountFilter{Kind: ledger.AccountKindStockFree, ProductID: &productID}
	for {
		page, next, err := ledgerSvc.ListAccounts(ctx, corp.ID, filter)
		if err != nil {
			return ledger.Account{}, err
		}
		for _, acc := range page {
			if acc.WarehouseBuildingID != nil && *acc.WarehouseBuildingID == warehouseID {
				logger.Info("cuenta stock_free ya existía: omitida",
					slog.String("account", corp.Name),
					slog.String("product", p.code))
				return acc, nil
			}
		}
		if next == "" {
			break
		}
		filter.Cursor = next
	}

	ownerID := corp.ID
	wh := warehouseID
	acc, err := ledgerSvc.CreateAccount(ctx, ledger.AccountKindStockFree, &ownerID, &productID, &wh, nil)
	if err != nil {
		return ledger.Account{}, err
	}
	logger.Info("cuenta stock_free creada",
		slog.String("account", corp.Name),
		slog.String("product", p.code),
		slog.String("ledger_account_id", acc.ID.String()))
	return acc, nil
}

// hasProductionOutput indica si la cuenta stock_free ya recibió algún asiento
// production_output. Es la clave de idempotencia del stock inicial: precisa
// aunque un seed anterior quedara a medias (lectura de auditoría; el saldo no
// sirve de clave porque el juego lo mueve).
func hasProductionOutput(ctx context.Context, pool *pgxpool.Pool, accountID uuid.UUID) (bool, error) {
	var exists bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			  FROM ledger.entries e
			  JOIN ledger.transactions t ON t.id = e.transaction_id
			 WHERE e.account_id = $1 AND t.kind = 'production_output')`, accountID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("seed: comprobando el stock inicial de la cuenta %s: %w", accountID, err)
	}
	return exists, nil
}

// ensureInventoryRow garantiza la fila FÍSICA de inventario del almacén
// (world.building_inventories). ON CONFLICT DO NOTHING sobre la PK: nunca
// pisa una cantidad ya movida por el juego.
func ensureInventoryRow(ctx context.Context, pool *pgxpool.Pool, buildingID uuid.UUID, corp auth.Account, p seededProduct, simNow simtime.SimTime, logger *slog.Logger) error {
	tag, err := pool.Exec(ctx, `
		INSERT INTO world.building_inventories (building_id, product_id, quantity, updated_at_sim)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (building_id, product_id) DO NOTHING`,
		buildingID, p.ID, p.initialStock, int64(simNow))
	if err != nil {
		return fmt.Errorf("seed: creando el inventario físico de %s (%s): %w", corp.Name, p.code, err)
	}
	if tag.RowsAffected() > 0 {
		logger.Info("inventario físico creado",
			slog.String("account", corp.Name),
			slog.String("product", p.code),
			slog.Int64("quantity", p.initialStock))
	} else {
		logger.Info("inventario físico ya existía: omitido",
			slog.String("account", corp.Name),
			slog.String("product", p.code))
	}
	return nil
}
