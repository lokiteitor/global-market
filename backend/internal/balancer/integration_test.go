package balancer_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lokiteitor/global-market/backend/internal/balancer"
	"github.com/lokiteitor/global-market/backend/internal/contracts"
	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/outbox"
	"github.com/lokiteitor/global-market/backend/internal/platform/migrate"
	"github.com/lokiteitor/global-market/backend/internal/seed"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// TestBalancerIntegration ejercita el núcleo de ciudades del Economy Balancer
// (Incremento 6b) contra una BD real migrada (incl. 0013) y el seed completo
// (que ahora siembra el centro de distribución de la ciudad y su capital). Cada
// sub-test usa su PROPIA BD efímera (aislamiento total) y fija su estado por SQL.
//
// Cubre el mandato de tests a–f: (a) el recálculo respeta los clamps; (b) inundar
// baja el precio y la escasez lo sube; (c) se publica UNA sola buy por (ciudad,
// producto) sin duplicar; (d) el consumer consume lo entregado (city stock_free →
// world_source) y sube supply_index; (e) el índice cruza el umbral → level_up
// (sube D0, desbloquea categoría); (f) contabilidad: la emisión del faucet cuadra
// y el consumo cuadra por producto.
//
// Se omite si II_TEST_DATABASE_URL no está definida (la URL identifica el
// servidor; cada sub-test crea su BD efímera con CREATEDB).
func TestBalancerIntegration(t *testing.T) {
	if os.Getenv("II_TEST_DATABASE_URL") == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test de integración omitido")
	}

	t.Run("clamps_del_recalculo", testDemandClamps)             // (a)
	t.Run("inundacion_y_escasez", testFloodingAndScarcity)      // (b)
	t.Run("una_buy_por_ciudad_producto", testOneBuyPerProduct)  // (c)
	t.Run("consumo_de_entrega_urbana", testConsumeCityDelivery) // (d) + (f consumo)
	t.Run("subida_de_nivel", testLevelUp)                       // (e)
	t.Run("faucet_de_fondeo_cuadra", testFaucetAccounting)      // (f faucet)

	// Macro (Incremento 6b, entregable macro): (g) la analítica escribe las tres
	// tablas con valores coherentes y emisión−absorción cuadra con el delta de
	// masa monetaria del bucket; (h) el ajuste fiscal sube impuestos ante
	// inflación sintética y respeta el rango; (i) la fórmula laboral sube el
	// salario en una región saturada de industria.
	t.Run("analitica_macro_coherente", testAnalyticsMacro)        // (g)
	t.Run("ajuste_fiscal_en_rango", testFiscalWithinRange)        // (h)
	t.Run("salario_sube_en_region_saturada", testLaborSaturation) // (i)
}

// ─── (a) Clamps del recálculo ─────────────────────────────────────────────────

func testDemandClamps(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	fx := setup(t, ctx)

	// Nivel estable (banda del nivel 2 bajo el base por defecto): sin cambios de
	// nivel que interfieran con el recálculo de la curva.
	fx.setCity(t, ctx, 2, 150_000, 0)
	// Escenario extremo de escasez para iron_ore: sin oferta reciente, EMA mínima.
	fx.setDemand(t, ctx, fx.iron, 1000, 0.01, 100, 0, 0, 1)
	// Escenario extremo de inundación para steel: oferta enorme en una jornada.
	fx.setDemand(t, ctx, fx.steel, 500, 1, 400, 5_000_000, int64(fx.sim.now)-simtime.SimDay, 2)

	w := fx.worker(t, stableOpts())
	w.RunOnce(ctx)

	// iron_ore: clamps [20, 400]; steel: clamps [80, 1600].
	ep, esat, epr, _ := fx.getDemand(t, ctx, fx.iron)
	if ep <= 0 {
		t.Fatalf("iron supply_ema %g no es > 0 (suelo obligatorio)", ep)
	}
	if epr < 20 || epr > 400 {
		t.Fatalf("iron precio %d fuera de [20, 400]", epr)
	}
	if esat < 0 || esat > 10 {
		t.Fatalf("iron saturation_factor %g fuera de [0, 10]", esat)
	}
	sp, _, spr, _ := fx.getDemand(t, ctx, fx.steel)
	if sp <= 0 {
		t.Fatalf("steel supply_ema %g no es > 0", sp)
	}
	if spr < 80 || spr > 1600 {
		t.Fatalf("steel precio %d fuera de [80, 1600]", spr)
	}
}

// ─── (b) Inundación baja el precio; escasez lo sube ───────────────────────────

func testFloodingAndScarcity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	fx := setup(t, ctx)
	fx.setCity(t, ctx, 2, 150_000, 0)

	// Inundación de iron_ore: oferta reciente enorme en una jornada.
	fx.setDemand(t, ctx, fx.iron, 1000, 1, 100, 5_000_000, int64(fx.sim.now)-simtime.SimDay, 1)
	fx.worker(t, stableOpts()).RunOnce(ctx)
	_, _, flood, _ := fx.getDemand(t, ctx, fx.iron)

	// Escasez de iron_ore: sin oferta, EMA cae al suelo.
	fx.setDemand(t, ctx, fx.iron, 1000, 1, 100, 0, int64(fx.sim.now)-simtime.SimDay, 1)
	fx.worker(t, stableOpts()).RunOnce(ctx)
	_, _, scarce, _ := fx.getDemand(t, ctx, fx.iron)

	if flood >= 100 {
		t.Fatalf("inundación: precio %d debería estar por debajo del base (100)", flood)
	}
	if scarce <= 100 {
		t.Fatalf("escasez: precio %d debería estar por encima del base (100)", scarce)
	}
	if flood >= scarce {
		t.Fatalf("inundación (%d) debería ser < escasez (%d)", flood, scarce)
	}
}

// ─── (c) Una sola buy por (ciudad, producto), sin duplicar ────────────────────

func testOneBuyPerProduct(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	fx := setup(t, ctx)
	fx.setCity(t, ctx, 2, 150_000, 0)
	fx.setDemand(t, ctx, fx.iron, 1000, 1, 100, 0, 0, 1)
	fx.setDemand(t, ctx, fx.steel, 500, 1, 400, 0, 0, 2)

	w := fx.worker(t, stableOpts())
	// Tres pasadas: la buy viva debe deduplicarse (no se re-publica mientras vive).
	w.RunOnce(ctx)
	w.RunOnce(ctx)
	w.RunOnce(ctx)

	if n := fx.countLiveBuys(t, ctx, fx.iron); n != 1 {
		t.Fatalf("iron: %d buys vivas, esperado 1 (dedup)", n)
	}
	if n := fx.countLiveBuys(t, ctx, fx.steel); n != 1 {
		t.Fatalf("steel: %d buys vivas, esperado 1 (dedup)", n)
	}
	// La buy debe tener destino = el nodo del centro de distribución de la ciudad.
	var dest uuid.UUID
	fx.pool.QueryRow(ctx, `SELECT destination_node_id FROM ledger.publications
		WHERE publisher_account_id=$1 AND product_id=$2 AND kind='buy' LIMIT 1`,
		fx.cityAccount, fx.iron).Scan(&dest)
	if dest != fx.distNode {
		t.Fatalf("destino de la buy %s, esperado el centro de distribución %s", dest, fx.distNode)
	}
}

// ─── (d) El consumer consume la entrega urbana y sube supply_index ────────────

func testConsumeCityDelivery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	fx := setup(t, ctx)
	fx.setCity(t, ctx, 2, 150_000, 0)
	fx.setDemand(t, ctx, fx.iron, 1000, 1, 100, 0, 0, 1)

	const qty int64 = 400
	contractID := fx.fabricateCityDelivery(t, ctx, fx.iron, qty)

	stockBefore := fx.stockFree(t, ctx, fx.cityAccount, fx.iron, fx.distBuilding)
	physBefore := fx.physical(t, ctx, fx.distBuilding, fx.iron)
	wsBefore := fx.worldSource(t, ctx, fx.iron)
	_, _, siBefore := fx.getCity(t, ctx)
	if stockBefore != qty || physBefore != qty {
		t.Fatalf("precondición: stock_free=%d físico=%d, esperado %d", stockBefore, physBefore, qty)
	}

	c := fx.consumer(t, balancer.DefaultOptions())
	fx.deliverSettled(t, ctx, c, contractID)

	stockAfter := fx.stockFree(t, ctx, fx.cityAccount, fx.iron, fx.distBuilding)
	physAfter := fx.physical(t, ctx, fx.distBuilding, fx.iron)
	wsAfter := fx.worldSource(t, ctx, fx.iron)
	_, _, siAfter := fx.getCity(t, ctx)

	if stockAfter != 0 {
		t.Fatalf("stock_free de la ciudad tras consumir: %d, esperado 0", stockAfter)
	}
	if physAfter != 0 {
		t.Fatalf("inventario físico tras consumir: %d, esperado 0", physAfter)
	}
	// (f consumo) el consumo cuadra por producto: +qty world_source / −qty stock_free.
	if wsAfter-wsBefore != qty {
		t.Fatalf("world_source subió %d, esperado +%d (consumo cuadra)", wsAfter-wsBefore, qty)
	}
	if siAfter <= siBefore {
		t.Fatalf("supply_index no subió (%g -> %g)", siBefore, siAfter)
	}
	// recent_supply del producto debe reflejar la entrega (alimenta el EMA).
	_, _, _, recent := fx.getDemand(t, ctx, fx.iron)
	if recent != qty {
		t.Fatalf("recent_supply=%d, esperado %d", recent, qty)
	}
}

// ─── (e) El índice cruza el umbral → level_up (D0 sube, desbloquea categoría) ──

func testLevelUp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	fx := setup(t, ctx)

	// Ciudad nivel 1 con índice por encima del umbral (base pequeño para el test):
	// base=1000 → umbral de subida a 2 = base*1 = 1000. Índice 1500 >= 1000.
	opts := balancer.DefaultOptions()
	opts.LevelupIndexBase = 1000
	fx.setCity(t, ctx, 1, 1500, 0)
	// iron activo (nivel 1) CON oferta reciente (>0: hubo suministro → sin
	// decaimiento del índice); steel bloqueado (unlocked_at_level 2): se desbloquea
	// al subir de nivel.
	fx.setDemand(t, ctx, fx.iron, 1000, 1, 100, 200, 0, 1)
	fx.setDemand(t, ctx, fx.steel, 500, 1, 400, 0, 0, 2)
	ironD0Before := fx.d0(t, ctx, fx.iron)
	steelUpdatedBefore := fx.demandUpdatedAtSim(t, ctx, fx.steel)

	fx.sim.set(int64(10 * simtime.SimDay)) // avanzar el reloj para una ventana clara
	fx.worker(t, opts).RunOnce(ctx)

	level, _, _ := fx.getCity(t, ctx)
	if level != 2 {
		t.Fatalf("nivel tras el recálculo: %d, esperado 2 (level up)", level)
	}
	// D0 de un producto existente subió (~+20%).
	if d0 := fx.d0(t, ctx, fx.iron); d0 <= ironD0Before {
		t.Fatalf("D0 de iron no subió al subir de nivel (%d -> %d)", ironD0Before, d0)
	}
	// La categoría desbloqueada (steel, unlocked_at_level 2) se recalculó esta pasada.
	if u := fx.demandUpdatedAtSim(t, ctx, fx.steel); u == steelUpdatedBefore {
		t.Fatalf("steel (categoría desbloqueada) no se recalculó al subir de nivel")
	}
	// Se emitió city.level_up.
	if !fx.outboxHas(t, ctx, "city.level_up", fx.cityID) {
		t.Fatal("no se emitió city.level_up")
	}
}

// ─── (f) La emisión del faucet cuadra (masa monetaria sube) ───────────────────

func testFaucetAccounting(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	fx := setup(t, ctx)
	fx.setCity(t, ctx, 2, 150_000, 0)
	fx.setDemand(t, ctx, fx.iron, 1000, 1, 100, 0, 0, 1)
	fx.setDemand(t, ctx, fx.steel, 500, 1, 400, 0, 0, 2)

	// Vaciar la caja de la ciudad para forzar el faucet en la próxima compra.
	fx.setCityCash(t, ctx, 0)

	moneyBefore := fx.moneySupply(t, ctx)
	emissionBefore := fx.emissionBalance(t, ctx)

	fx.worker(t, stableOpts()).RunOnce(ctx)

	moneyAfter := fx.moneySupply(t, ctx)
	emissionAfter := fx.emissionBalance(t, ctx)

	moneyDelta := moneyAfter - moneyBefore
	emissionDelta := emissionBefore - emissionAfter // emisión más negativa = faucet
	if moneyDelta <= 0 {
		t.Fatalf("la masa monetaria no subió (delta %d): el faucet no operó", moneyDelta)
	}
	if moneyDelta != emissionDelta {
		t.Fatalf("la emisión no cuadra con la masa monetaria: money +%d vs emisión -%d", moneyDelta, emissionDelta)
	}
	// Se publicó al menos una buy de ciudad (faucet ligado a la compra).
	if n := fx.countLiveBuys(t, ctx, fx.iron) + fx.countLiveBuys(t, ctx, fx.steel); n == 0 {
		t.Fatal("no se publicó ninguna buy de ciudad")
	}
}

// ─── (g) Analítica macro: tres tablas coherentes + coherencia emisión/absorción ─

func testAnalyticsMacro(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	fx := setup(t, ctx)
	opts := balancer.DefaultOptions() // bucket = 1 día de juego

	region := fx.regionOfCity(t, ctx)
	emission := fx.ledgerAccountByKind(t, ctx, "emission")
	sink := fx.ledgerAccountByKind(t, ctx, "sink")
	demoCash := fx.cashAccountOf(t, ctx, fx.seller)

	bucketStart := (fx.sim.now / opts.AnalyticsBucketSim) * opts.AnalyticsBucketSim
	bucketEnd := bucketStart + opts.AnalyticsBucketSim

	// Un faucet (emisión) y un sink DENTRO del bucket: la contabilidad del bucle
	// faucet/sink (GDD 5.5) que la analítica debe desglosar y cuadrar.
	const faucet, drain int64 = 5000, 2000
	fx.postMoneyTx(t, ctx, "seed_capital", bucketStart+10, []moneyEntry{{demoCash, faucet}, {emission, -faucet}})
	fx.postMoneyTx(t, ctx, "tax", bucketStart+20, []moneyEntry{{demoCash, -drain}, {sink, drain}})

	fx.analyticsWorker(t, opts).RunOnce(ctx)

	// economy_indicators: masa monetaria > 0, faucet/sink desglosados.
	ind := fx.getEconomyIndicators(t, ctx, bucketStart)
	if ind.moneySupply <= 0 {
		t.Fatalf("money_supply = %d, esperado > 0", ind.moneySupply)
	}
	if ind.emissionTotal != faucet {
		t.Fatalf("emission_total = %d, esperado %d", ind.emissionTotal, faucet)
	}
	if ind.absorptionTotal != drain {
		t.Fatalf("absorption_total = %d, esperado %d", ind.absorptionTotal, drain)
	}
	// Coherencia macro: emisión − absorción explica el delta de masa monetaria del
	// bucket (medido de forma independiente sobre las partidas del ledger).
	delta := fx.bucketMoneyDelta(t, ctx, bucketStart, bucketEnd)
	if ind.emissionTotal-ind.absorptionTotal != delta {
		t.Fatalf("incoherencia macro: emisión−absorción = %d, delta masa monetaria del bucket = %d",
			ind.emissionTotal-ind.absorptionTotal, delta)
	}
	if delta != faucet-drain {
		t.Fatalf("delta masa monetaria = %d, esperado %d", delta, faucet-drain)
	}
	if ind.depletionProjection == "" || ind.depletionProjection == "{}" {
		t.Fatalf("depletion_projection vacío: %q", ind.depletionProjection)
	}

	// region_stats: fila del bucket con magnitudes no negativas y coherentes.
	rs := fx.getRegionStats(t, ctx, region, bucketStart)
	if rs.activeBuildings < 0 || rs.occupation < 0 {
		t.Fatalf("region_stats incoherente: buildings=%d occupation=%g", rs.activeBuildings, rs.occupation)
	}

	// city_snapshots: foto del bucket para la ciudad seedada.
	level, population, _ := fx.getCity(t, ctx)
	snap := fx.getCitySnapshot(t, ctx, fx.cityID, bucketStart)
	if snap.level != int32(level) || snap.population != population {
		t.Fatalf("city_snapshot (level=%d pop=%d) no coincide con la ciudad (level=%d pop=%d)",
			snap.level, snap.population, level, population)
	}
}

// ─── (h) Ajuste fiscal: sube impuestos ante inflación y respeta el rango ──────

func testFiscalWithinRange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	fx := setup(t, ctx)
	opts := balancer.DefaultOptions()

	region := fx.regionOfCity(t, ctx)
	bucketStart := (fx.sim.now / opts.AnalyticsBucketSim) * opts.AnalyticsBucketSim
	prevBucket := bucketStart - opts.AnalyticsBucketSim

	// Bucket previo sintético con masa monetaria baja y PIB alto: junto al bucket
	// actual (masa monetaria real de millones del seed, PIB 0) la tendencia es
	// fuertemente inflacionaria → el lazo fiscal debe subir impuestos.
	fx.insertIndicator(t, ctx, prevBucket, 1000, 1000)

	taxBefore := fx.getRegionTax(t, ctx, region)

	w := fx.analyticsWorker(t, opts)
	w.RunOnce(ctx) // crea el bucket actual y aplica UN paso fiscal

	taxAfter := fx.getRegionTax(t, ctx, region)
	if taxAfter != taxBefore+int32(opts.TaxStepBP) {
		t.Fatalf("ante inflación el impuesto debía subir un paso: %d -> %d (paso %d)", taxBefore, taxAfter, opts.TaxStepBP)
	}

	// Muchos barridos: el impuesto trepa hasta el techo y NUNCA sale del rango.
	for i := 0; i < 60; i++ {
		w.RunOnce(ctx)
		tax := fx.getRegionTax(t, ctx, region)
		if tax < int32(opts.TaxMinBP) || tax > int32(opts.TaxMaxBP) {
			t.Fatalf("iteración %d: tax %d fuera de rango [%d, %d]", i, tax, opts.TaxMinBP, opts.TaxMaxBP)
		}
	}
	if tax := fx.getRegionTax(t, ctx, region); tax != int32(opts.TaxMaxBP) {
		t.Fatalf("tras muchos barridos inflacionarios tax=%d, esperado el techo %d", tax, opts.TaxMaxBP)
	}
}

// ─── (i) Fórmula laboral: sube el salario en una región saturada de industria ─

func testLaborSaturation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	fx := setup(t, ctx)
	opts := balancer.DefaultOptions()

	w := fx.analyticsWorker(t, opts)
	w.RunOnce(ctx) // salario base con la ocupación del seed
	salaryBefore := fx.getCityBaseSalary(t, ctx, fx.cityID)

	// Saturar la región de la ciudad: muchos edificios operativos → ocupación alta.
	fx.addOperationalBuildings(t, ctx, 200)

	w.RunOnce(ctx)
	salaryAfter := fx.getCityBaseSalary(t, ctx, fx.cityID)

	if salaryAfter <= salaryBefore {
		t.Fatalf("la fórmula laboral no subió el salario en región saturada: %d -> %d", salaryBefore, salaryAfter)
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Fixture y helpers
// ═══════════════════════════════════════════════════════════════════════════

type mutSim struct{ now int64 }

func (m *mutSim) Now(context.Context) simtime.SimTime { return simtime.SimTime(m.now) }
func (m *mutSim) set(n int64)                         { m.now = n }

// stableOpts son Options que NO cambian el nivel de una ciudad seedada estable
// (supply_index 150000 en la banda del nivel 2 bajo el base por defecto), al
// desactivar el decaimiento: los tests que no ejercitan la máquina de niveles
// (curva, dedup, faucet) no quieren que la ventana grande del reloj fijo del
// test degrade a la ciudad. El nivel se ejercita aparte (subida_de_nivel).
func stableOpts() balancer.Options {
	o := balancer.DefaultOptions()
	o.SupplyIndexDecayPerSimDay = 0
	return o
}

type fixture struct {
	pool         *pgxpool.Pool
	sim          *mutSim
	port         balancer.PublicationCreator
	contractsSvc *contracts.Service
	cityID       uuid.UUID
	cityAccount  uuid.UUID
	distNode     uuid.UUID
	distBuilding uuid.UUID
	iron         uuid.UUID
	steel        uuid.UUID
	bank         uuid.UUID
	seller       uuid.UUID
}

// testPort adapta el Contract Service al PORT del Balancer (como el composition
// root): publica la buy por la API estándar (CreatePublication).
type testPort struct{ svc *contracts.Service }

func (p testPort) CreateCityBuy(ctx context.Context, by balancer.CityBuy) error {
	product := by.ProductID
	dest := by.DestinationNodeID
	_, err := p.svc.CreatePublication(ctx, by.CityAccountID, contracts.PublicationInput{
		Kind:               contracts.KindBuy,
		Channel:            contracts.ChannelBoard,
		ProductID:          &product,
		QuantityTotal:      by.Quantity,
		UnitPrice:          by.UnitPrice,
		DestinationNodeID:  &dest,
		DeliverySimSeconds: int64(by.DeliverySimSeconds),
	})
	return err
}

func setup(t *testing.T, ctx context.Context) *fixture {
	t.Helper()
	pool := newEphemeralDB(t, ctx)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := seed.Run(ctx, pool, seed.Options{
		DemoName: seed.DefaultDemoName, DemoSecret: "demo-secret-test",
		TraderName: seed.DefaultTraderName, TraderSecret: "norte-secret-test",
		Ledger: ledger.DefaultOptions(),
	}, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}

	sim := &mutSim{now: int64(30 * simtime.SimDay)} // un sim-time realista de arranque
	svc, err := contracts.NewService(pool, sim, contracts.DefaultOptions(), logger, nil)
	if err != nil {
		t.Fatalf("contracts.NewService: %v", err)
	}
	fx := &fixture{pool: pool, sim: sim, port: testPort{svc: svc}, contractsSvc: svc}
	fx.iron = fx.productID(t, ctx, "iron_ore")
	fx.steel = fx.productID(t, ctx, "steel_ingot")
	fx.bank = fx.accountID(t, ctx, seed.CentralBankName)
	fx.seller = fx.accountID(t, ctx, seed.DefaultDemoName)

	fx.pool.QueryRow(ctx, `SELECT id, account_id FROM world.cities LIMIT 1`).Scan(&fx.cityID, &fx.cityAccount)
	if err := fx.pool.QueryRow(ctx, `SELECT id, building_id FROM world.network_nodes
		WHERE city_id=$1 AND kind='distribution_center' LIMIT 1`, fx.cityID).Scan(&fx.distNode, &fx.distBuilding); err != nil {
		t.Fatalf("nodo del centro de distribución: %v", err)
	}
	return fx
}

func (fx *fixture) worker(t *testing.T, opts balancer.Options) *balancer.DemandWorker {
	t.Helper()
	w, err := balancer.NewDemandWorker(fx.pool, fx.port, fx.sim, opts, balancer.NewMetrics(nil), discardLogger())
	if err != nil {
		t.Fatalf("NewDemandWorker: %v", err)
	}
	return w
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func (fx *fixture) consumer(t *testing.T, opts balancer.Options) *balancer.Consumer {
	t.Helper()
	c, err := balancer.NewConsumer(opts, balancer.NewMetrics(nil), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	return c
}

func (fx *fixture) analyticsWorker(t *testing.T, opts balancer.Options) *balancer.AnalyticsWorker {
	t.Helper()
	w, err := balancer.NewAnalyticsWorker(fx.pool, fx.sim, opts, balancer.NewMetrics(nil), discardLogger())
	if err != nil {
		t.Fatalf("NewAnalyticsWorker: %v", err)
	}
	return w
}

// ── Helpers macro (analítica / fiscal / laboral) ──

// moneyEntry es una partida (cuenta, importe con signo) de un asiento de dinero.
type moneyEntry struct {
	acc uuid.UUID
	amt int64
}

// postMoneyTx asienta una transacción de dinero balanceada (cabecera + partidas)
// en su propia transacción serializable, con el sim_time_at dado (para caer en el
// bucket de analítica bajo prueba).
func (fx *fixture) postMoneyTx(t *testing.T, ctx context.Context, kind string, simAt int64, entries []moneyEntry) {
	t.Helper()
	txID := uuid.Must(uuid.NewV7())
	tx, err := fx.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		t.Fatalf("begin tx (%s): %v", kind, err)
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck
	mustExecTx(t, ctx, tx, `INSERT INTO ledger.transactions (id, kind, sim_time_at) VALUES ($1,$2,$3)`, txID, kind, simAt)
	for _, e := range entries {
		mustExecTx(t, ctx, tx, `INSERT INTO ledger.entries (id, transaction_id, account_id, amount) VALUES ($1,$2,$3,$4)`,
			uuid.Must(uuid.NewV7()), txID, e.acc, e.amt)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit (%s): %v", kind, err)
	}
}

func (fx *fixture) regionOfCity(t *testing.T, ctx context.Context) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := fx.pool.QueryRow(ctx, `SELECT region_id FROM world.cities WHERE id=$1`, fx.cityID).Scan(&id); err != nil {
		t.Fatalf("regionOfCity: %v", err)
	}
	return id
}

func (fx *fixture) ledgerAccountByKind(t *testing.T, ctx context.Context, kind string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := fx.pool.QueryRow(ctx, `SELECT id FROM ledger.accounts WHERE kind=$1 ORDER BY id LIMIT 1`, kind).Scan(&id); err != nil {
		t.Fatalf("ledgerAccountByKind %s: %v", kind, err)
	}
	return id
}

func (fx *fixture) cashAccountOf(t *testing.T, ctx context.Context, owner uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := fx.pool.QueryRow(ctx, `SELECT id FROM ledger.accounts WHERE kind='cash' AND owner_account_id=$1`, owner).Scan(&id); err != nil {
		t.Fatalf("cashAccountOf %s: %v", owner, err)
	}
	return id
}

// economyRow es la fila de analytics.economy_indicators bajo prueba.
type economyRow struct {
	moneySupply         int64
	simulatedGDP        int64
	emissionTotal       int64
	absorptionTotal     int64
	depletionProjection string
}

func (fx *fixture) getEconomyIndicators(t *testing.T, ctx context.Context, bucketStart int64) economyRow {
	t.Helper()
	var r economyRow
	err := fx.pool.QueryRow(ctx, `SELECT money_supply, simulated_gdp, emission_total, absorption_total, depletion_projection::text
		FROM analytics.economy_indicators WHERE bucket_start_sim=$1`, bucketStart).
		Scan(&r.moneySupply, &r.simulatedGDP, &r.emissionTotal, &r.absorptionTotal, &r.depletionProjection)
	if err != nil {
		t.Fatalf("getEconomyIndicators (bucket %d): %v", bucketStart, err)
	}
	return r
}

// insertIndicator inserta una fila sintética de economy_indicators (para fijar la
// tendencia del lazo fiscal). depletion_projection queda como '{}'.
func (fx *fixture) insertIndicator(t *testing.T, ctx context.Context, bucketStart, moneySupply, gdp int64) {
	t.Helper()
	mustExec(t, ctx, fx.pool, `INSERT INTO analytics.economy_indicators
		(bucket_start_sim, money_supply, simulated_gdp, emission_total, absorption_total,
		 active_bot_count, active_human_count, global_depletion_rate, depletion_projection)
		VALUES ($1,$2,$3,0,0,0,0,0,'{}')`, bucketStart, moneySupply, gdp)
}

// bucketMoneyDelta suma las partidas monetarias (cash+escrow+guarantee) de los
// asientos con sim_time_at en [start, end): el delta de masa monetaria del bucket
// medido de forma independiente sobre el ledger (contraparte de la coherencia).
func (fx *fixture) bucketMoneyDelta(t *testing.T, ctx context.Context, start, end int64) int64 {
	t.Helper()
	var delta int64
	err := fx.pool.QueryRow(ctx, `SELECT COALESCE(SUM(e.amount),0)
		FROM ledger.entries e
		JOIN ledger.accounts a     ON a.id = e.account_id
		JOIN ledger.transactions t ON t.id = e.transaction_id
		WHERE a.kind IN ('cash','escrow','guarantee')
		  AND t.sim_time_at >= $1 AND t.sim_time_at < $2`, start, end).Scan(&delta)
	if err != nil {
		t.Fatalf("bucketMoneyDelta: %v", err)
	}
	return delta
}

// regionStatsRow es la fila de analytics.region_stats bajo prueba.
type regionStatsRow struct {
	occupation      float64
	activeBuildings int32
}

func (fx *fixture) getRegionStats(t *testing.T, ctx context.Context, region uuid.UUID, bucketStart int64) regionStatsRow {
	t.Helper()
	var r regionStatsRow
	err := fx.pool.QueryRow(ctx, `SELECT industrial_occupation::float8, active_buildings
		FROM analytics.region_stats WHERE region_id=$1 AND bucket_start_sim=$2`, region, bucketStart).
		Scan(&r.occupation, &r.activeBuildings)
	if err != nil {
		t.Fatalf("getRegionStats: %v", err)
	}
	return r
}

// citySnapshotRow es la fila de analytics.city_snapshots bajo prueba.
type citySnapshotRow struct {
	level      int32
	population int64
}

func (fx *fixture) getCitySnapshot(t *testing.T, ctx context.Context, city uuid.UUID, bucketStart int64) citySnapshotRow {
	t.Helper()
	var r citySnapshotRow
	err := fx.pool.QueryRow(ctx, `SELECT level, population FROM analytics.city_snapshots
		WHERE city_id=$1 AND bucket_start_sim=$2`, city, bucketStart).Scan(&r.level, &r.population)
	if err != nil {
		t.Fatalf("getCitySnapshot: %v", err)
	}
	return r
}

func (fx *fixture) getRegionTax(t *testing.T, ctx context.Context, region uuid.UUID) int32 {
	t.Helper()
	var tax int32
	if err := fx.pool.QueryRow(ctx, `SELECT tax_rate_bp FROM world.regions WHERE id=$1`, region).Scan(&tax); err != nil {
		t.Fatalf("getRegionTax: %v", err)
	}
	return tax
}

func (fx *fixture) getCityBaseSalary(t *testing.T, ctx context.Context, city uuid.UUID) int64 {
	t.Helper()
	var s int64
	if err := fx.pool.QueryRow(ctx, `SELECT base_salary FROM world.cities WHERE id=$1`, city).Scan(&s); err != nil {
		t.Fatalf("getCityBaseSalary: %v", err)
	}
	return s
}

// addOperationalBuildings clona n edificios OPERATIVOS a partir de uno existente
// (misma región/concesión/tipo/huella), para saturar la ocupación industrial de
// la región de la ciudad.
func (fx *fixture) addOperationalBuildings(t *testing.T, ctx context.Context, n int) {
	t.Helper()
	mustExec(t, ctx, fx.pool, `INSERT INTO world.buildings
		(owner_account_id, region_id, concession_id, building_type_id, footprint, level, status)
		SELECT b.owner_account_id, b.region_id, b.concession_id, b.building_type_id, b.footprint, 1, 'operational'
		FROM (SELECT owner_account_id, region_id, concession_id, building_type_id, footprint
		      FROM world.buildings LIMIT 1) b, generate_series(1, $1)`, n)
}

// deliverSettled invoca el handler del consumer con un evento contract.settled
// fabricado, dentro de una transacción serializable (como el lote del outbox).
func (fx *fixture) deliverSettled(t *testing.T, ctx context.Context, c *balancer.Consumer, contractID uuid.UUID) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"contract_id": contractID.String(), "status": "settled"})
	ev := outbox.Event{Seq: 1, EventType: "contract.settled", Payload: payload, SimTimeAt: fx.sim.now}
	tx, err := fx.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck
	if err := c.Handle(ctx, tx, ev); err != nil {
		t.Fatalf("consumer.Handle: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// fabricateCityDelivery deja el mundo como tras una entrega urbana liquidada:
// stock_free de la ciudad + inventario físico en el centro de distribución, y un
// contrato settled con comprador = ciudad y destino = el nodo del centro. Devuelve
// el contract_id para el evento contract.settled.
func (fx *fixture) fabricateCityDelivery(t *testing.T, ctx context.Context, product uuid.UUID, qty int64) uuid.UUID {
	t.Helper()
	// world_source del producto (contrapartida, ADR-022).
	var wsID uuid.UUID
	err := fx.pool.QueryRow(ctx, `SELECT id FROM ledger.accounts WHERE kind='world_source' AND product_id=$1`, product).Scan(&wsID)
	if err != nil {
		wsID = uuid.Must(uuid.NewV7())
		mustExec(t, ctx, fx.pool, `INSERT INTO ledger.accounts (id, kind, owner_account_id, product_id) VALUES ($1,'world_source',$2,$3)`, wsID, fx.bank, product)
	}
	// stock_free de la ciudad en el centro de distribución.
	sfID := uuid.Must(uuid.NewV7())
	mustExec(t, ctx, fx.pool, `INSERT INTO ledger.accounts (id, kind, owner_account_id, product_id, warehouse_building_id)
		VALUES ($1,'stock_free',$2,$3,$4)`, sfID, fx.cityAccount, product, fx.distBuilding)
	// Alta de stock (production_output): +qty stock_free / −qty world_source, en UNA
	// transacción (el trigger diferido de doble entrada se evalúa en el COMMIT).
	txID := uuid.Must(uuid.NewV7())
	tx, err := fx.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		t.Fatalf("begin tx (alta de stock): %v", err)
	}
	mustExecTx(t, ctx, tx, `INSERT INTO ledger.transactions (id, kind, sim_time_at) VALUES ($1,'production_output',$2)`, txID, fx.sim.now)
	mustExecTx(t, ctx, tx, `INSERT INTO ledger.entries (id, transaction_id, account_id, amount) VALUES ($1,$2,$3,$4)`, uuid.Must(uuid.NewV7()), txID, sfID, qty)
	mustExecTx(t, ctx, tx, `INSERT INTO ledger.entries (id, transaction_id, account_id, amount) VALUES ($1,$2,$3,$4)`, uuid.Must(uuid.NewV7()), txID, wsID, -qty)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit (alta de stock): %v", err)
	}
	// Inventario físico en el centro (como tras la llegada del cargamento).
	mustExec(t, ctx, fx.pool, `INSERT INTO world.building_inventories (building_id, product_id, quantity, updated_at_sim)
		VALUES ($1,$2,$3,$4) ON CONFLICT (building_id, product_id) DO UPDATE SET quantity = world.building_inventories.quantity + $3`,
		fx.distBuilding, product, qty, fx.sim.now)

	// Cuenta espejo ficticia para los FKs NOT NULL del contrato (el consumer no las lee).
	mirror := uuid.Must(uuid.NewV7())
	mustExec(t, ctx, fx.pool, `INSERT INTO ledger.accounts (id, kind, owner_account_id) VALUES ($1,'escrow',$2)`, mirror, fx.cityAccount)

	// Contrato liquidado: comprador = ciudad, destino = nodo del centro de distribución.
	contractID := uuid.Must(uuid.NewV7())
	mustExec(t, ctx, fx.pool, `INSERT INTO ledger.contracts
		(id, channel, buyer_account_id, seller_account_id, product_id, quantity_agreed, quantity_delivered,
		 unit_price, origin_node_id, destination_node_id, deadline_sim, status, fill_bp,
		 stock_reserve_account_id, seller_guarantee_account_id, escrow_account_id, confirmed_at_sim, settled_at_sim)
		VALUES ($1,'board',$2,$3,$4,$5,$5,1,$6,$6,$7,'settled',10000,$8,$8,$8,$7,$7)`,
		contractID, fx.cityAccount, fx.seller, product, qty, fx.distNode, fx.sim.now, mirror)
	return contractID
}

// ── setters/getters de estado (SQL directo, fixtures de test) ──

func (fx *fixture) setCity(t *testing.T, ctx context.Context, level int, supplyIndex float64, updatedAtSim int64) {
	t.Helper()
	mustExec(t, ctx, fx.pool, `UPDATE world.cities SET level=$1, supply_index=$2, updated_at_sim=$3 WHERE id=$4`,
		level, supplyIndex, updatedAtSim, fx.cityID)
}

func (fx *fixture) setCityCash(t *testing.T, ctx context.Context, balance int64) {
	t.Helper()
	mustExec(t, ctx, fx.pool, `UPDATE ledger.accounts SET balance=$1 WHERE kind='cash' AND owner_account_id=$2`, balance, fx.cityAccount)
}

func (fx *fixture) setDemand(t *testing.T, ctx context.Context, product uuid.UUID, d0 int64, supplyEMA float64, price int64, recent int64, updatedAtSim int64, unlockedAt int) {
	t.Helper()
	mustExec(t, ctx, fx.pool, `UPDATE world.city_demand
		SET d0_per_sim_day=$1, supply_ema=$2, current_price=$3, recent_supply=$4, updated_at_sim=$5, unlocked_at_level=$6
		WHERE city_id=$7 AND product_id=$8`,
		d0, supplyEMA, price, recent, updatedAtSim, unlockedAt, fx.cityID, product)
}

func (fx *fixture) getDemand(t *testing.T, ctx context.Context, product uuid.UUID) (supplyEMA, saturation float64, price, recent int64) {
	t.Helper()
	err := fx.pool.QueryRow(ctx, `SELECT supply_ema::float8, saturation_factor::float8, current_price, recent_supply
		FROM world.city_demand WHERE city_id=$1 AND product_id=$2`, fx.cityID, product).Scan(&supplyEMA, &saturation, &price, &recent)
	if err != nil {
		t.Fatalf("getDemand: %v", err)
	}
	return
}

func (fx *fixture) d0(t *testing.T, ctx context.Context, product uuid.UUID) int64 {
	t.Helper()
	var d0 int64
	if err := fx.pool.QueryRow(ctx, `SELECT d0_per_sim_day FROM world.city_demand WHERE city_id=$1 AND product_id=$2`, fx.cityID, product).Scan(&d0); err != nil {
		t.Fatalf("d0: %v", err)
	}
	return d0
}

func (fx *fixture) demandUpdatedAtSim(t *testing.T, ctx context.Context, product uuid.UUID) int64 {
	t.Helper()
	var u int64
	if err := fx.pool.QueryRow(ctx, `SELECT updated_at_sim FROM world.city_demand WHERE city_id=$1 AND product_id=$2`, fx.cityID, product).Scan(&u); err != nil {
		t.Fatalf("demandUpdatedAtSim: %v", err)
	}
	return u
}

func (fx *fixture) getCity(t *testing.T, ctx context.Context) (level int, population int64, supplyIndex float64) {
	t.Helper()
	if err := fx.pool.QueryRow(ctx, `SELECT level, population, supply_index::float8 FROM world.cities WHERE id=$1`, fx.cityID).Scan(&level, &population, &supplyIndex); err != nil {
		t.Fatalf("getCity: %v", err)
	}
	return
}

func (fx *fixture) countLiveBuys(t *testing.T, ctx context.Context, product uuid.UUID) int {
	t.Helper()
	var n int
	if err := fx.pool.QueryRow(ctx, `SELECT count(*) FROM ledger.publications
		WHERE publisher_account_id=$1 AND product_id=$2 AND kind='buy' AND status IN ('draw_window','open','micro_window')`,
		fx.cityAccount, product).Scan(&n); err != nil {
		t.Fatalf("countLiveBuys: %v", err)
	}
	return n
}

func (fx *fixture) stockFree(t *testing.T, ctx context.Context, owner, product, warehouse uuid.UUID) int64 {
	t.Helper()
	var b int64
	err := fx.pool.QueryRow(ctx, `SELECT COALESCE((SELECT balance FROM ledger.accounts
		WHERE kind='stock_free' AND owner_account_id=$1 AND product_id=$2 AND warehouse_building_id=$3),0)`,
		owner, product, warehouse).Scan(&b)
	if err != nil {
		t.Fatalf("stockFree: %v", err)
	}
	return b
}

func (fx *fixture) physical(t *testing.T, ctx context.Context, building, product uuid.UUID) int64 {
	t.Helper()
	var q int64
	err := fx.pool.QueryRow(ctx, `SELECT COALESCE((SELECT quantity FROM world.building_inventories WHERE building_id=$1 AND product_id=$2),0)`,
		building, product).Scan(&q)
	if err != nil {
		t.Fatalf("physical: %v", err)
	}
	return q
}

func (fx *fixture) worldSource(t *testing.T, ctx context.Context, product uuid.UUID) int64 {
	t.Helper()
	var b int64
	err := fx.pool.QueryRow(ctx, `SELECT COALESCE((SELECT balance FROM ledger.accounts WHERE kind='world_source' AND product_id=$1),0)`, product).Scan(&b)
	if err != nil {
		t.Fatalf("worldSource: %v", err)
	}
	return b
}

func (fx *fixture) moneySupply(t *testing.T, ctx context.Context) int64 {
	t.Helper()
	var total int64
	if err := fx.pool.QueryRow(ctx, `SELECT COALESCE(SUM(balance),0) FROM ledger.accounts WHERE kind IN ('cash','escrow','guarantee')`).Scan(&total); err != nil {
		t.Fatalf("moneySupply: %v", err)
	}
	return total
}

func (fx *fixture) emissionBalance(t *testing.T, ctx context.Context) int64 {
	t.Helper()
	var b int64
	if err := fx.pool.QueryRow(ctx, `SELECT balance FROM ledger.accounts WHERE kind='emission' ORDER BY id LIMIT 1`).Scan(&b); err != nil {
		t.Fatalf("emissionBalance: %v", err)
	}
	return b
}

func (fx *fixture) outboxHas(t *testing.T, ctx context.Context, eventType string, aggregateID uuid.UUID) bool {
	t.Helper()
	var exists bool
	if err := fx.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM outbox.events WHERE event_type=$1 AND aggregate_id=$2)`, eventType, aggregateID).Scan(&exists); err != nil {
		t.Fatalf("outboxHas: %v", err)
	}
	return exists
}

func (fx *fixture) productID(t *testing.T, ctx context.Context, code string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := fx.pool.QueryRow(ctx, `SELECT id FROM world.products WHERE code=$1`, code).Scan(&id); err != nil {
		t.Fatalf("productID %s: %v", code, err)
	}
	return id
}

func (fx *fixture) accountID(t *testing.T, ctx context.Context, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := fx.pool.QueryRow(ctx, `SELECT id FROM auth.accounts WHERE lower(name)=lower($1)`, name).Scan(&id); err != nil {
		t.Fatalf("accountID %s: %v", name, err)
	}
	return id
}

func mustExec(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func mustExecTx(t *testing.T, ctx context.Context, tx pgx.Tx, sql string, args ...any) {
	t.Helper()
	if _, err := tx.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec (tx) %q: %v", sql, err)
	}
}

func newEphemeralDB(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("conectando a %s: %v", adminURL, err)
	}
	dbName := fmt.Sprintf("balancertest_%d_%d", os.Getpid(), time.Now().UnixNano())
	quoted := pgx.Identifier{dbName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
		t.Fatalf("creando la BD efímera: %v", err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		defer admin.Close(dropCtx)
		if _, err := admin.Exec(dropCtx, "DROP DATABASE "+quoted+" WITH (FORCE)"); err != nil {
			t.Errorf("eliminando la BD efímera %s: %v", dbName, err)
		}
	})

	connCfg, err := pgx.ParseConfig(adminURL)
	if err != nil {
		t.Fatalf("interpretando II_TEST_DATABASE_URL: %v", err)
	}
	connCfg.Database = dbName
	conn, err := pgx.ConnectConfig(ctx, connCfg)
	if err != nil {
		t.Fatalf("conectando a la BD efímera: %v", err)
	}
	if _, err := migrate.New(conn, "../../db/migrations", "dev", io.Discard).Up(ctx); err != nil {
		t.Fatalf("aplicando las migraciones: %v", err)
	}
	if err := conn.Close(ctx); err != nil {
		t.Fatalf("cerrando la conexión de migraciones: %v", err)
	}

	poolCfg, err := pgxpool.ParseConfig(adminURL)
	if err != nil {
		t.Fatalf("interpretando la URL del pool: %v", err)
	}
	poolCfg.ConnConfig.Database = dbName
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		t.Fatalf("creando el pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}
