package stress

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lokiteitor/global-market/backend/internal/auth"
	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/seed"
	"github.com/lokiteitor/global-market/backend/internal/sim/clock"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// El PROVISIONER es ADMIN DEL ENTORNO DE PRUEBAS, NO GAMEPLAY.
//
// El contrato público (docs/api/openapi.yaml) NO expone endpoint de registro de
// cuentas: no hay forma de crear una corporación «jugando». Por eso el harness
// crea sus cuentas por BD, con el mismo patrón que el Bot Orchestration Service
// (ADR-024: provisioning por paquetes internos, gameplay por el SDK) pero SIN
// importar internal/bots. Todo lo que ocurre DESPUÉS del provisioning —cada
// consulta y cada mutación de la carga— pasa por pkg/botsdk contra la API
// pública, igual que un humano (ADR-010, GDD §15.4).
//
// Cada cuenta creada lleva el prefijo reconocible "stress-<run_id>-" y un
// bot_profile cuyo behavior JSON registra la corrida, de modo que las cuentas de
// cualquier run puedan identificarse y limpiarse sin ambigüedad.
//
// # Por qué el provisioning también DOTA STOCK
//
// El capital solo habilita el lado COMPRADOR del CCRI. Con caja y sin mercancía
// una cuenta no puede publicar sell ni freight (ambas exigen el stock YA en el
// almacén de origen) ni aceptar un buy (exige además ser dueño de ese almacén):
// lo único que puede emitir es buy… y lo único que puede aceptar es una oferta
// sell AJENA. Un harness solo capitalizado queda por tanto colgado de una oferta
// exógena y finita —las de los bots del mundo vivo—, y su operación de
// aceptación (el camino de escritura caro: escrow, ventana de sorteo y
// contención SERIALIZABLE real) se agota en los primeros segundos y se degrada a
// CERO justo cuando más bots compiten. Por eso el provisioner emite, junto al
// capital, una dotación de stock por el MISMO camino que el seed
// (production_output: +N stock_free del bot / −N world_source del producto,
// ADR-022) moviendo a la vez el plano físico (world.building_inventories) para
// que la reconciliación del mundo siga cuadrando. Es admin del entorno de
// pruebas, contabilizada y auditable, nunca un grifo oculto: II_STRESS_STOCK_ENDOWMENT=0
// la desactiva.

// maxEndowmentSites acota los almacenes candidatos de la dotación: el reparto
// solo necesita variedad suficiente para repartir las ofertas por regiones, no
// el grafo entero de un mundo generado.
const maxEndowmentSites = 256

// StressBot es una cuenta del harness lista para jugar.
type StressBot struct {
	// Name es el nombre de la cuenta (clave de idempotencia del provisioning).
	Name string
	// AccountID es la cuenta kind=bot creada o reutilizada.
	AccountID uuid.UUID
	// Archetype es el arquetipo de carga que ejecutará.
	Archetype Archetype
	// Secret es el secreto derivado (solo en memoria: la BD guarda su hash).
	Secret string
	// Capitalized indica si esta corrida emitió su capital (false = la cuenta
	// ya existía capitalizada de una corrida anterior con el mismo run_id).
	Capitalized bool
	// StockProductID y StockNodeID son el producto y el nodo-almacén de la
	// dotación de stock del bot: lo que necesita para publicar sell por la API
	// (product_id + origin_node_id). Nil/vacío = sin dotación.
	StockProductID uuid.UUID
	StockNodeID    uuid.UUID
	// StockQuantity es el saldo stock_free disponible del bot en esa dotación
	// (0 = no puede publicar sell).
	StockQuantity int64
	// Endowed indica si esta corrida asentó la dotación (false = ya existía de
	// una corrida anterior con el mismo run_id, o el harness va sin dotación).
	Endowed bool
}

// Provisioner crea y retira las cuentas de una corrida de stress sobre la BD
// del ENTORNO DE PRUEBAS.
type Provisioner struct {
	pool      *pgxpool.Pool
	opts      Options
	logger    *slog.Logger
	repo      *auth.PGRepository
	ledgerSvc *ledger.Service
}

// NewProvisioner construye el provisioner del harness.
func NewProvisioner(pool *pgxpool.Pool, opts Options, ledgerOpts ledger.Options, logger *slog.Logger) (*Provisioner, error) {
	if pool == nil {
		return nil, errors.New("stress: el provisioner requiere un pool de BD del entorno de pruebas")
	}
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Provisioner{
		pool:      pool,
		opts:      opts,
		logger:    logger,
		repo:      auth.NewPGRepository(pool),
		ledgerSvc: ledger.NewService(pool, ledgerOpts, nil),
	}, nil
}

// DeriveSecret deriva el secreto de una cuenta del harness a partir de la
// semilla y el nombre (HMAC-SHA256, hex): reproducible entre corridas del mismo
// run_id sin almacenar nunca el secreto en claro.
func DeriveSecret(seedValue, name string) string {
	mac := hmac.New(sha256.New, []byte(seedValue))
	mac.Write([]byte("imperio-stress-secret:" + name))
	return hex.EncodeToString(mac.Sum(nil))
}

// Population construye la población de la corrida: un nombre estable y
// reconocible por bot ("stress-<run_id>-<arquetipo>-NNNN") con la mezcla
// repartida por resto mayor e INTERCALADA (la rampa mezcla arquetipos desde el
// primer segundo).
func (p *Provisioner) Population() []StressBot {
	assignment := p.opts.Mix.Allocate(p.opts.Bots)
	index := map[Archetype]int{}
	bots := make([]StressBot, 0, len(assignment))
	for _, a := range assignment {
		index[a]++
		name := p.opts.AccountName(a, index[a])
		bots = append(bots, StressBot{
			Name:      name,
			Archetype: a,
			Secret:    DeriveSecret(p.opts.SecretSeed, name),
		})
	}
	return bots
}

// Provision asegura la población completa de forma idempotente por nombre:
// cuenta kind=bot, credencial argon2id (nunca sobrescrita), bot_profile con el
// marcador de la corrida y la CAPITALIZACIÓN única del banco central si la
// cuenta aún no tenía caja (+capital cash / −capital emission, GDD §15.4: la
// emisión de los bots es contabilidad visible, nunca un grifo oculto). Requiere
// el mundo sembrado en el entorno de pruebas.
func (p *Provisioner) Provision(ctx context.Context) ([]StressBot, error) {
	emission, err := p.emissionAccount(ctx)
	if err != nil {
		return nil, err
	}
	simNow, err := p.currentSimTime(ctx)
	if err != nil {
		return nil, err
	}
	products, sites, err := p.endowmentPlan(ctx)
	if err != nil {
		return nil, err
	}
	bots := p.Population()
	endowed := 0
	for i := range bots {
		if err := p.provisionOne(ctx, &bots[i], emission, simNow); err != nil {
			return nil, err
		}
		if len(products) > 0 && len(sites) > 0 {
			// Reparto DECORRELACIONADO producto×almacén: el producto rota en cada
			// bot y el almacén cada vuelta completa de productos, de modo que las
			// ofertas sell del harness se repartan por productos Y por regiones (los
			// filtros del tablón dejan de ver un único par).
			prod := products[i%len(products)]
			site := sites[(i/len(products))%len(sites)]
			if err := p.endowOne(ctx, &bots[i], prod, site, simNow); err != nil {
				return nil, err
			}
			if bots[i].Endowed {
				endowed++
			}
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	p.logger.Info("población de stress aprovisionada",
		slog.String("run_id", p.opts.RunID),
		slog.String("account_prefix", p.opts.RunAccountPrefix()),
		slog.Int("bots", len(bots)),
		slog.String("mix", p.opts.Mix.String()),
		slog.Int64("stock_endowment", p.opts.StockEndowment),
		slog.Int("stock_endowed_now", endowed),
		slog.Int("endowment_products", len(products)),
		slog.Int("endowment_warehouses", len(sites)))
	return bots, nil
}

// endowmentProduct es un producto del mundo con su contrapartida world_source ya
// sembrada (ADR-022): el harness NO crea base física nueva, se apoya en la que
// el seed dejó.
type endowmentProduct struct {
	ProductID     uuid.UUID
	WorldSourceID uuid.UUID
	Code          string
}

// endowmentSite es un almacén del entorno de pruebas: el nodo del grafo (lo que
// viaja por la API como origin_node_id) y el edificio que lo respalda (lo que
// ancla la cuenta stock_free del ledger).
type endowmentSite struct {
	NodeID     uuid.UUID
	BuildingID uuid.UUID
}

// endowmentPlan carga los productos y almacenes con los que dotar a la
// población. Devuelve listas vacías si la dotación está desactivada
// (II_STRESS_STOCK_ENDOWMENT=0). Un entorno sembrado que no ofrezca ninguno es
// un error de arranque: sin lado vendedor la corrida NO mediría el camino de
// aceptación, y arrancar en silencio produciría justamente el informe engañoso
// que este harness debe evitar.
func (p *Provisioner) endowmentPlan(ctx context.Context) ([]endowmentProduct, []endowmentSite, error) {
	if p.opts.StockEndowment <= 0 {
		p.logger.Warn("dotación de stock desactivada: el harness solo podrá publicar buy y su tasa de aceptación dependerá de ofertas sell ajenas",
			slog.String("env", EnvStockEndowment))
		return nil, nil, nil
	}
	products, err := p.endowmentProducts(ctx)
	if err != nil {
		return nil, nil, err
	}
	sites, err := p.endowmentSites(ctx)
	if err != nil {
		return nil, nil, err
	}
	if len(products) == 0 || len(sites) == 0 {
		return nil, nil, fmt.Errorf(
			"stress: el entorno de pruebas no permite dotar el lado vendedor (%d productos con cuenta world_source, %d almacenes con edificio): siembra el mundo antes de la corrida, o desactiva la dotación con %s=0 asumiendo que la aceptación no se medirá",
			len(products), len(sites), EnvStockEndowment)
	}
	return products, sites, nil
}

// endowmentProducts lista los productos que tienen cuenta world_source (la
// contrapartida del alta de stock, ADR-022), en orden estable por código.
func (p *Provisioner) endowmentProducts(ctx context.Context) ([]endowmentProduct, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT p.id, a.id, p.code
		  FROM world.products p
		  JOIN ledger.accounts a
		    ON a.kind = 'world_source' AND a.product_id = p.id
		 ORDER BY p.code`)
	if err != nil {
		return nil, fmt.Errorf("stress: listando los productos con contrapartida world_source: %w", err)
	}
	defer rows.Close()
	var out []endowmentProduct
	for rows.Next() {
		var e endowmentProduct
		if err := rows.Scan(&e.ProductID, &e.WorldSourceID, &e.Code); err != nil {
			return nil, fmt.Errorf("stress: leyendo un producto de la dotación: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// endowmentSites lista los nodos del grafo respaldados por un edificio: son los
// almacenes que el CCRI admite como origen de una publicación sell. El orden por
// región reparte la dotación entre regiones antes que dentro de una sola.
func (p *Provisioner) endowmentSites(ctx context.Context) ([]endowmentSite, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT n.id, n.building_id
		  FROM world.network_nodes n
		 WHERE n.building_id IS NOT NULL
		 ORDER BY n.region_id, n.id
		 LIMIT $1`, maxEndowmentSites)
	if err != nil {
		return nil, fmt.Errorf("stress: listando los almacenes del entorno de pruebas: %w", err)
	}
	defer rows.Close()
	var out []endowmentSite
	for rows.Next() {
		var e endowmentSite
		if err := rows.Scan(&e.NodeID, &e.BuildingID); err != nil {
			return nil, fmt.Errorf("stress: leyendo un almacén de la dotación: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// endowOne asienta la dotación de stock de UN bot (idempotente: la existencia de
// su cuenta stock_free en ese almacén es la clave, igual que la caja lo es del
// capital). El asiento mueve los DOS planos —contable y físico— para que la
// reconciliación del mundo siga cuadrando.
func (p *Provisioner) endowOne(ctx context.Context, bot *StressBot, prod endowmentProduct, site endowmentSite, simNow simtime.SimTime) error {
	bot.StockProductID = prod.ProductID
	bot.StockNodeID = site.NodeID

	acc, created, err := p.ensureStockFreeAccount(ctx, bot.AccountID, prod.ProductID, site.BuildingID)
	if err != nil {
		return err
	}
	if !created {
		// Dotación de una corrida anterior con el mismo run_id: NO se re-emite;
		// el bot juega con el saldo que le quede.
		bot.StockQuantity = acc.Balance
		return nil
	}

	ref := site.BuildingID
	if _, err := p.ledgerSvc.PostTransaction(ctx, ledger.TransactionKindProductionOutput, simNow, &ref,
		fmt.Sprintf("Dotación de stock de %s (harness de stress): %d %s en el almacén %s",
			bot.Name, p.opts.StockEndowment, prod.Code, site.BuildingID),
		[]ledger.EntryInput{
			{AccountID: acc.ID, Amount: p.opts.StockEndowment},
			{AccountID: prod.WorldSourceID, Amount: -p.opts.StockEndowment},
		}); err != nil {
		return err
	}
	if err := p.addPhysicalInventory(ctx, site.BuildingID, prod.ProductID, p.opts.StockEndowment, simNow); err != nil {
		return err
	}
	bot.StockQuantity = p.opts.StockEndowment
	bot.Endowed = true
	return nil
}

// ensureStockFreeAccount localiza (o crea) la cuenta stock_free del bot para
// (producto, almacén). El segundo valor indica si la creó ESTA llamada: es la
// clave de idempotencia de la dotación.
func (p *Provisioner) ensureStockFreeAccount(ctx context.Context, owner, product, warehouse uuid.UUID) (ledger.Account, bool, error) {
	productID := product
	filter := ledger.AccountFilter{Kind: ledger.AccountKindStockFree, ProductID: &productID}
	for {
		page, next, err := p.ledgerSvc.ListAccounts(ctx, owner, filter)
		if err != nil {
			return ledger.Account{}, false, err
		}
		for _, acc := range page {
			if acc.WarehouseBuildingID != nil && *acc.WarehouseBuildingID == warehouse {
				return acc, false, nil
			}
		}
		if next == "" {
			break
		}
		filter.Cursor = next
	}
	ownerID, wh := owner, warehouse
	acc, err := p.ledgerSvc.CreateAccount(ctx, ledger.AccountKindStockFree, &ownerID, &productID, &wh, nil)
	if err != nil {
		return ledger.Account{}, false, err
	}
	return acc, true, nil
}

// addPhysicalInventory suma la dotación al inventario FÍSICO del almacén. El
// alta de stock es un hecho con dos planos que deben moverse juntos (ADR-022):
// asentar solo el contable dejaría al reconciliador del mundo publicando
// discrepancias que no son del sistema, sino del harness.
func (p *Provisioner) addPhysicalInventory(ctx context.Context, building, product uuid.UUID, quantity int64, simNow simtime.SimTime) error {
	_, err := p.pool.Exec(ctx, `
		INSERT INTO world.building_inventories (building_id, product_id, quantity, updated_at_sim)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (building_id, product_id) DO UPDATE
		   SET quantity = world.building_inventories.quantity + EXCLUDED.quantity,
		       updated_at_sim = EXCLUDED.updated_at_sim`,
		building, product, quantity, int64(simNow))
	if err != nil {
		return fmt.Errorf("stress: sumando la dotación al inventario físico del almacén %s: %w", building, err)
	}
	return nil
}

// botProfile es el behavior JSON persistido de una cuenta del harness: deja
// registrado, de forma auditable, QUÉ corrida la creó y con qué perfil de carga.
type botProfile struct {
	Harness    string `json:"harness"`
	RunID      string `json:"stress_run_id"`
	Archetype  string `json:"stress_archetype"`
	CreatedAt  string `json:"stress_created_at"`
	WriteRatio string `json:"stress_write_ratio"`
}

// provisionOne asegura una cuenta del harness (idempotente por nombre).
func (p *Provisioner) provisionOne(ctx context.Context, bot *StressBot, emission ledger.Account, simNow simtime.SimTime) error {
	acc, err := p.repo.GetAccountByName(ctx, bot.Name)
	switch {
	case errors.Is(err, auth.ErrNotFound):
		acc, err = p.repo.CreateAccount(ctx, "bot", bot.Name)
		if err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		if acc.Kind != "bot" {
			return fmt.Errorf("stress: la cuenta %q existe con kind %q (esperado bot)", bot.Name, acc.Kind)
		}
	}
	bot.AccountID = acc.ID

	secretHash, err := auth.HashSecret(bot.Secret)
	if err != nil {
		return err
	}
	if _, err := p.repo.EnsureCredential(ctx, acc.ID, secretHash); err != nil {
		return err
	}

	behavior, err := json.Marshal(botProfile{
		Harness:    "stress",
		RunID:      p.opts.RunID,
		Archetype:  string(bot.Archetype),
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		WriteRatio: fmt.Sprintf("%.3f", p.opts.WriteRatio),
	})
	if err != nil {
		return fmt.Errorf("stress: serializando el perfil de %s: %w", bot.Name, err)
	}
	if _, err := p.repo.EnsureBotProfile(ctx, acc.ID, bot.Archetype.BotArchetype(), behavior); err != nil {
		return err
	}

	// Capitalización ÚNICA: la existencia de caja es la clave de idempotencia
	// (mismo patrón que el capital semilla). Si ya existe, el capital ya se
	// emitió en una corrida anterior con este mismo run_id.
	existing, _, err := p.ledgerSvc.ListAccounts(ctx, acc.ID, ledger.AccountFilter{
		Kind: ledger.AccountKindCash, Limit: 1,
	})
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	cash, err := p.ledgerSvc.EnsureCashAccount(ctx, acc.ID)
	if err != nil {
		return err
	}
	ref := acc.ID
	if _, err := p.ledgerSvc.PostTransaction(ctx, ledger.TransactionKindBotCapitalization, simNow, &ref,
		fmt.Sprintf("Capitalización de %s (harness de stress, emisión del banco central)", bot.Name),
		[]ledger.EntryInput{
			{AccountID: cash.ID, Amount: p.opts.Capital},
			{AccountID: emission.ID, Amount: -p.opts.Capital},
		}); err != nil {
		return err
	}
	bot.Capitalized = true
	return nil
}

// CleanupResult resume la limpieza final de una corrida.
type CleanupResult struct {
	// Requested son las cuentas del run consideradas.
	Requested int `json:"requested"`
	// Retired son las cuentas efectivamente marcadas como retiradas.
	Retired int `json:"retired"`
	// AlreadyInactive son las cuentas que ya no estaban activas.
	AlreadyInactive int `json:"already_inactive"`
	// Failed son las cuentas cuyo retiro falló.
	Failed int `json:"failed"`
	// Skipped indica que la limpieza estaba desactivada (II_STRESS_CLEANUP=false).
	Skipped bool `json:"skipped"`
}

// Cleanup marca como RETIRADAS las cuentas de la corrida y desactiva sus
// perfiles: dejan de poder entrar y de ser candidatas de ningún orquestador.
//
// NO borra datos económicos: el ledger es append-only por diseño (GDD §17), así
// que los asientos, publicaciones y contratos que la corrida generó permanecen
// como historia auditable del entorno de pruebas. El retiro se registra en el
// log estructurado y en el informe.
func (p *Provisioner) Cleanup(ctx context.Context, bots []StressBot) CleanupResult {
	res := CleanupResult{Requested: len(bots)}
	if !p.opts.Cleanup {
		res.Skipped = true
		p.logger.Info("limpieza desactivada: las cuentas del run siguen activas",
			slog.String("run_id", p.opts.RunID),
			slog.String("account_prefix", p.opts.RunAccountPrefix()),
			slog.Int("accounts", len(bots)))
		return res
	}
	for _, bot := range bots {
		if bot.AccountID == uuid.Nil {
			// Limpieza de una población NO aprovisionada en este proceso (p. ej.
			// el barrido de un run anterior con el mismo run_id): la cuenta se
			// resuelve por su nombre, que es la clave de idempotencia.
			acc, err := p.repo.GetAccountByName(ctx, bot.Name)
			if errors.Is(err, auth.ErrNotFound) {
				res.AlreadyInactive++
				continue
			}
			if err != nil {
				res.Failed++
				p.logger.Warn("no se pudo resolver la cuenta del run",
					slog.String("bot", bot.Name), slog.Any("error", err))
				continue
			}
			bot.AccountID = acc.ID
		}
		retired, err := p.repo.RetireBotAccount(ctx, bot.AccountID)
		switch {
		case err != nil:
			res.Failed++
			p.logger.Warn("no se pudo retirar la cuenta del run",
				slog.String("bot", bot.Name), slog.Any("error", err))
		case retired:
			res.Retired++
		default:
			res.AlreadyInactive++
		}
	}
	p.logger.Info("limpieza de la corrida de stress completada (ledger intacto: append-only)",
		slog.String("run_id", p.opts.RunID),
		slog.String("account_prefix", p.opts.RunAccountPrefix()),
		slog.Int("retired", res.Retired),
		slog.Int("already_inactive", res.AlreadyInactive),
		slog.Int("failed", res.Failed))
	return res
}

// emissionAccount localiza la cuenta de emisión del banco central sembrado. El
// harness exige el entorno de pruebas sembrado: no re-crea la base monetaria.
func (p *Provisioner) emissionAccount(ctx context.Context) (ledger.Account, error) {
	bank, err := p.repo.GetAccountByName(ctx, seed.CentralBankName)
	if errors.Is(err, auth.ErrNotFound) {
		return ledger.Account{}, fmt.Errorf("stress: no existe la cuenta del banco central %q: siembra antes el entorno de pruebas", seed.CentralBankName)
	}
	if err != nil {
		return ledger.Account{}, err
	}
	accounts, _, err := p.ledgerSvc.ListAccounts(ctx, bank.ID, ledger.AccountFilter{
		Kind: ledger.AccountKindEmission, Limit: 1,
	})
	if err != nil {
		return ledger.Account{}, err
	}
	if len(accounts) == 0 {
		return ledger.Account{}, errors.New("stress: el banco central no tiene cuenta de emisión: siembra antes el entorno de pruebas")
	}
	return accounts[0], nil
}

// currentSimTime deriva el sim-time actual del ancla persistida en
// world.sim_clock.
func (p *Provisioner) currentSimTime(ctx context.Context) (simtime.SimTime, error) {
	a, err := clock.NewStore(p.pool).Load(ctx)
	if err != nil {
		return 0, fmt.Errorf("stress: leyendo el reloj de simulación: %w", err)
	}
	return simtime.Derive(a.SimTimeAt, a.WallAnchor, time.Now(), a.Ratio, a.Frozen), nil
}
