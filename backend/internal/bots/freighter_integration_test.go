// Integración del arquetipo freighter (GDD 13.2 + CCRI-Flete del GDD 5.3.2)
// contra una BD real y el gateway REAL servido con httptest, con los motores
// del engine disparados desde el test: worker CCRI (sorteo/confirmación),
// freight_shipment_creator (cargamento del flete), motor de tránsito y
// freight_settler (liquidación por llegada física). Ningún mock.
//
// Cubre el mandato del arquetipo:
//
//  1. RECHAZO AUDITADO: con una tarifa que no cubre el coste del trayecto más
//     el margen exigido, el transportista NO acepta y registra
//     skip_freight/below_margin — sin comprar vehículo ni inmovilizar garantía.
//  2. ACEPTACIÓN Y EJECUCIÓN: con tarifa suficiente compra su camión en el
//     origen, acepta el flete (garantía bloqueada), localiza el cargamento del
//     CARGADOR que le toca transportar, crea la ruta y despacha.
//  3. ENTREGA Y COBRO: avanzando el reloj el cargamento llega físicamente, el
//     settler liquida el flete: la carga pasa al cargador EN EL DESTINO y el
//     transportista cobra la tarifa y recupera la garantía.
//
// Se omite si II_TEST_DATABASE_URL no está definida.
package bots_test

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/lokiteitor/global-market/backend/internal/bots"
	"github.com/lokiteitor/global-market/backend/internal/contracts"
	"github.com/lokiteitor/global-market/backend/internal/seed"
	"github.com/lokiteitor/global-market/backend/internal/world/fleet"

	"github.com/lokiteitor/global-market/backend/pkg/botsdk"
)

const (
	freighterBotName = "Bot Transportista 01"
	// truckPrice es el precio de catálogo del truck_small sembrado.
	truckPrice int64 = 40_000
	// freightCargoQty es la carga del flete del test y freightDeclared su valor
	// declarado (base de la garantía del transportista: el 10% por defecto).
	freightCargoQty  int64 = 200
	freightDeclared  int64 = 20_000
	freightGuarantee       = freightDeclared / 10
	// freightGoodTariff paga de sobra el trayecto; freightPoorTariff no llega
	// al margen exigido (20% sobre el coste estimado).
	freightGoodTariff int64 = 20
	freightPoorTariff int64 = 1
	// freightDeadlineSim es el plazo del flete: 3 días de sim, holgado para el
	// tránsito real del test.
	freightDeadlineSim int64 = 3 * 86_400
)

func TestFreighterIntegration(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test de integración omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	env := newBotsEnv(t, ctx, adminURL, bots.Options{
		Freighters: 1,
		SecretSeed: itSecretSeed, Capital: itCapital,
		TransformerMarginBP: bots.DefaultTransformerMarginBP,
		FreighterMarginBP:   bots.DefaultFreighterMarginBP,
		Tick:                time.Second, Addr: ":0",
	})
	pool := env.pool

	provisioned, err := env.orch.Provision(ctx)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if len(provisioned) != 1 || provisioned[0].Name != freighterBotName {
		t.Fatalf("población aprovisionada inesperada: %+v", provisioned)
	}
	var archetype string
	if err := pool.QueryRow(ctx, `
		SELECT bp.archetype::text FROM auth.bot_profiles bp WHERE bp.account_id = $1`,
		provisioned[0].AccountID).Scan(&archetype); err != nil {
		t.Fatalf("perfil del transportista: %v", err)
	}
	if archetype != "freighter" {
		t.Fatalf("arquetipo persistido %q, esperado freighter", archetype)
	}

	botID := provisioned[0].AccountID
	client := loginBot(t, ctx, env.apiURL, provisioned[0])
	state := bots.NewState()
	metrics := bots.NewMetrics(prometheus.NewRegistry())
	// El transportista escribe sus decisiones en un buffer: la auditoría exige
	// log Y métrica, y el test comprueba las dos.
	var botLogs bytes.Buffer
	bot := bots.NewFreighter(
		bots.DefaultFreighterConfig(bots.DefaultFreighterMarginBP, itCapital),
		freighterBotName, slog.New(slog.NewJSONHandler(&botLogs, nil)), metrics)

	ironID := queryUUID(t, ctx, pool, `SELECT id FROM world.products WHERE code = 'iron_ore'`)
	regionID := queryUUID(t, ctx, pool, `SELECT id FROM world.regions WHERE name = $1`, seed.RegionName)
	_ = regionID
	norteID := queryUUID(t, ctx, pool, `SELECT id FROM auth.accounts WHERE name = $1`, itNorteName)
	demoID := queryUUID(t, ctx, pool, `SELECT id FROM auth.accounts WHERE name = $1`, itDemoName)
	norteNode := warehouseNodeOf(t, ctx, pool, norteID)
	demoNode := warehouseNodeOf(t, ctx, pool, demoID)
	demoWarehouse := queryUUID(t, ctx, pool, `SELECT building_id FROM world.network_nodes WHERE id = $1`, demoNode)

	// El CARGADOR es Norte (tiene iron_ore físico en su almacén sembrado): pide
	// llevar 200 unidades de su almacén al almacén de Demo.
	shipper := newSDKClient(t, env.apiURL)
	if _, err := shipper.Login(ctx, itNorteName, itNorteSecret); err != nil {
		t.Fatalf("login Norte: %v", err)
	}

	// ── (0) Tablón sin fletes: la pasada ociosa DEBE dejar rastro ────────────
	if err := bot.Decide(ctx, client, state); err != nil {
		t.Fatalf("freighter Decide (tablón vacío): %v", err)
	}
	if got := testutil.ToFloat64(metrics.Decisions.WithLabelValues(freighterBotName, "wait")); got != 1 {
		t.Fatalf("esperas auditadas del transportista: %v, esperada 1 (la pasada ociosa debe contar)", got)
	}
	if !strings.Contains(botLogs.String(), `"reason":"no_freight_on_board"`) {
		t.Fatalf("el transportista no registró la espera de la pasada ociosa: %s", botLogs.String())
	}
	if got := testutil.ToFloat64(metrics.Decisions.WithLabelValues(freighterBotName, "evaluate_freight")); got != 0 {
		t.Fatalf("evaluaciones sin solicitudes en el tablón: %v, esperada 0", got)
	}

	// ── (1) Tarifa insuficiente: rechazo auditado, sin comprar ni bloquear ───
	poorPub := publishFreight(t, ctx, shipper, ironID, norteNode, demoNode, freightPoorTariff)
	if err := bot.Decide(ctx, client, state); err != nil {
		t.Fatalf("freighter Decide (rechazo): %v", err)
	}
	if got := testutil.ToFloat64(metrics.Decisions.WithLabelValues(freighterBotName, "evaluate_freight")); got != 1 {
		t.Fatalf("evaluaciones de flete: %v, esperada 1 (auditoría de la valoración)", got)
	}
	if got := testutil.ToFloat64(metrics.Decisions.WithLabelValues(freighterBotName, "skip_freight")); got != 1 {
		t.Fatalf("rechazos por margen: %v, esperado 1", got)
	}
	if n := countRows(t, ctx, pool, `SELECT count(*) FROM ledger.publication_acceptances WHERE acceptor_account_id = $1`, botID); n != 0 {
		t.Fatalf("aceptaciones con tarifa insuficiente: %d, esperada 0", n)
	}
	if n := countRows(t, ctx, pool, `SELECT count(*) FROM world.vehicles WHERE owner_account_id = $1`, botID); n != 0 {
		t.Fatalf("el transportista no debe comprar flota para un flete que rechaza (tiene %d)", n)
	}
	if got := cashOf(t, ctx, pool, botID); got != itCapital {
		t.Fatalf("caja tras rechazar: %d, esperada intacta %d", got, itCapital)
	}
	if _, err := shipper.CancelPublication(ctx, poorPub); err != nil {
		t.Fatalf("cancelando la solicitud barata: %v", err)
	}

	// ── (2) Tarifa suficiente: compra camión, acepta y bloquea garantía ──────
	goodPub := publishFreight(t, ctx, shipper, ironID, norteNode, demoNode, freightGoodTariff)
	if err := bot.Decide(ctx, client, state); err != nil {
		t.Fatalf("freighter Decide (aceptación): %v", err)
	}
	accID, ok := state.PendingAcceptance(goodPub)
	if !ok {
		t.Fatal("el transportista no aceptó el flete bien pagado")
	}
	if got := testutil.ToFloat64(metrics.Decisions.WithLabelValues(freighterBotName, "accept_freight")); got != 1 {
		t.Fatalf("aceptaciones de flete: %v, esperada 1", got)
	}
	if n := countRows(t, ctx, pool, `SELECT count(*) FROM world.vehicles WHERE owner_account_id = $1`, botID); n != 1 {
		t.Fatalf("flota del transportista: %d, esperado 1 camión comprado en el origen", n)
	}

	// El sorteo confirma el flete y el creador materializa el cargamento del
	// CARGADOR en su almacén de origen.
	freightID := driveFreightDrawUntilServed(t, ctx, env.ccriWorker, client, accID)
	drainConsumer(t, ctx, pool, env.fcConsumer, env.freightCreator.Handle,
		fleet.ConsumerFreightShipmentCreator, "freight.confirmed")

	var fcStatus string
	var fcPrice, fcDeclared int64
	if err := pool.QueryRow(ctx, `
		SELECT status::text, freight_price, declared_value FROM ledger.freight_contracts WHERE id = $1`,
		freightID).Scan(&fcStatus, &fcPrice, &fcDeclared); err != nil {
		t.Fatalf("contrato de flete: %v", err)
	}
	if fcStatus != "active" || fcPrice != freightCargoQty*freightGoodTariff || fcDeclared != freightDeclared {
		t.Fatalf("flete confirmado: status=%s precio=%d declarado=%d, esperado active/%d/%d",
			fcStatus, fcPrice, fcDeclared, freightCargoQty*freightGoodTariff, freightDeclared)
	}
	if got := guaranteeOf(t, ctx, pool, botID); got != freightGuarantee {
		t.Fatalf("garantía inmovilizada del transportista: %d, esperada %d", got, freightGuarantee)
	}

	// ── (3) Ejecución: localiza el cargamento ajeno, crea ruta y despacha ────
	shipmentID := queryUUID(t, ctx, pool, `SELECT id FROM world.shipments WHERE freight_contract_id = $1`, freightID)
	deadline := time.Now().Add(30 * time.Second)
	for {
		if err := bot.Decide(ctx, client, state); err != nil {
			t.Fatalf("freighter Decide (despacho): %v", err)
		}
		var status string
		if err := pool.QueryRow(ctx, `SELECT status::text FROM world.shipments WHERE id = $1`, shipmentID).Scan(&status); err != nil {
			t.Fatalf("estado del cargamento: %v", err)
		}
		if status == "in_transit" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout: el transportista no despachó el cargamento del flete (estado %s)", status)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got := testutil.ToFloat64(metrics.Decisions.WithLabelValues(freighterBotName, "dispatch")); got < 1 {
		t.Fatalf("despachos auditados: %v, esperado >= 1", got)
	}
	// El cargamento es del CARGADOR y viaja en el vehículo del TRANSPORTISTA.
	var shipmentOwner, vehicleOwner uuid.UUID
	if err := pool.QueryRow(ctx, `
		SELECT sh.owner_account_id, v.owner_account_id
		  FROM world.shipments sh JOIN world.vehicles v ON v.id = sh.vehicle_id
		 WHERE sh.id = $1`, shipmentID).Scan(&shipmentOwner, &vehicleOwner); err != nil {
		t.Fatalf("dueños del cargamento y el vehículo: %v", err)
	}
	if shipmentOwner != norteID || vehicleOwner != botID {
		t.Fatalf("cargamento del cargador %s en vehículo del transportista %s, obtenido %s/%s",
			norteID, botID, shipmentOwner, vehicleOwner)
	}

	// ── (4) Entrega física y liquidación del flete ───────────────────────────
	driveTransitUntilDelivered(t, ctx, pool, env.transitWorker, shipmentID)
	drainConsumer(t, ctx, pool, env.fsConsumer, env.settler.Handle,
		contracts.ConsumerFreightSettler, "shipment.arrived")

	var settledStatus string
	var fillBP int
	if err := pool.QueryRow(ctx, `
		SELECT status::text, COALESCE(fill_bp, 0) FROM ledger.freight_contracts WHERE id = $1`,
		freightID).Scan(&settledStatus, &fillBP); err != nil {
		t.Fatalf("flete liquidado: %v", err)
	}
	if settledStatus != "settled" || fillBP != 10_000 {
		t.Fatalf("flete tras la entrega: status=%s fill=%d, esperado settled/10000", settledStatus, fillBP)
	}
	// La carga aparece en el DESTINO como stock libre del CARGADOR.
	if got := stockFreeOf(t, ctx, pool, norteID, ironID, demoWarehouse); got != freightCargoQty {
		t.Fatalf("carga entregada al cargador en destino: %d, esperada %d", got, freightCargoQty)
	}
	// El transportista cobró el flete y recuperó la garantía; solo pagó su camión.
	wantCash := itCapital - truckPrice + freightCargoQty*freightGoodTariff
	if got := cashOf(t, ctx, pool, botID); got != wantCash {
		t.Fatalf("caja del transportista tras cobrar: %d, esperada %d", got, wantCash)
	}
	if got := guaranteeOf(t, ctx, pool, botID); got != 0 {
		t.Fatalf("garantía sin liberar tras la entrega: %d", got)
	}

	assertBalancedLedger(t, ctx, pool)
}

// ─── Auxiliares del test de flete ────────────────────────────────────────────

// publishFreight publica una solicitud de flete (kind=freight) del cargador:
// producto de la carga, origen, destino, tarifa por unidad y valor declarado.
func publishFreight(t *testing.T, ctx context.Context, shipper *botsdk.Client, productID, origin, dest uuid.UUID, tariff int64) string {
	t.Helper()
	qty, err := botsdk.QtyFromInt64(freightCargoQty)
	if err != nil {
		t.Fatalf("cantidad inválida: %v", err)
	}
	pub, err := shipper.CreatePublication(ctx, botsdk.PublicationCreate{
		Kind:               botsdk.PublicationFreight,
		ProductID:          productID.String(),
		QuantityTotal:      qty,
		UnitPrice:          botsdk.MoneyFromInt64(tariff),
		OriginNodeID:       origin.String(),
		DestinationNodeID:  dest.String(),
		DeclaredValue:      botsdk.MoneyFromInt64(freightDeclared),
		DeliverySimSeconds: freightDeadlineSim,
	})
	if err != nil {
		t.Fatalf("publicando la solicitud de flete (tarifa %d): %v", tariff, err)
	}
	return pub.ID
}

// driveFreightDrawUntilServed dispara el barrido del CCRI hasta que la
// aceptación de flete queda servida y devuelve el freight_contract_id.
func driveFreightDrawUntilServed(t *testing.T, ctx context.Context, w *contracts.Worker, c *botsdk.Client, accID string) uuid.UUID {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		w.RunOnce(ctx)
		acc, err := c.GetAcceptance(ctx, accID)
		if err == nil && acc.Status == botsdk.AcceptanceServed {
			if acc.FreightContractID == "" {
				t.Fatalf("aceptación de flete servida sin freight_contract_id: %+v", acc)
			}
			id, perr := uuid.Parse(acc.FreightContractID)
			if perr != nil {
				t.Fatalf("freight_contract_id inválido %q: %v", acc.FreightContractID, perr)
			}
			return id
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout esperando el sorteo de la aceptación de flete %s (err %v)", accID, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// warehouseNodeOf devuelve el nodo del almacén sembrado de una corporación.
func warehouseNodeOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner uuid.UUID) uuid.UUID {
	t.Helper()
	return queryUUID(t, ctx, pool, `
		SELECT n.id FROM world.network_nodes n
		  JOIN world.buildings b ON b.id = n.building_id
		 WHERE b.owner_account_id = $1 AND n.kind = 'warehouse'`, owner)
}

// guaranteeOf suma las garantías vivas (cuentas espejo) de una corporación.
func guaranteeOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner uuid.UUID) int64 {
	t.Helper()
	var total int64
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(balance), 0) FROM ledger.accounts
		 WHERE kind = 'guarantee' AND owner_account_id = $1`, owner).Scan(&total); err != nil {
		t.Fatalf("garantías de %s: %v", owner, err)
	}
	return total
}
