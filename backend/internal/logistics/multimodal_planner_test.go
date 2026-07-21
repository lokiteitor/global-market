package logistics_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/logistics"
	"github.com/lokiteitor/global-market/backend/internal/seed"
)

// TestLogisticsMultimodalPlanner ejercita el pathfinding MULTIMODAL (Incremento 7,
// FASE 2 MUNDO): route-plans que combinan road/rail/sea con transbordo SOLO en nodos
// con terminal intermodal (GDD 7.3/7.4). Contra una BD real con el seed mínimo y un
// grafo propio de varias "regiones" conectadas por rail/sea.
//
// Cubre: (a) un route-plan inter-región devuelve una ruta multimodal road→rail→road
// con transshipment_terminal_id en cada cambio de modo y una ETA coherente (tramos +
// transbordos); (b) rail es más rápido que road en tierra a igual distancia; (c) el
// mar es el ÚNICO camino a una región insular; (d) un cambio de modo en un nodo SIN
// terminal no es transitable (NO_ROUTE), y pasa a serlo al crear la terminal.
func TestLogisticsMultimodalPlanner(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test de integración omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	pool := newEphemeralDB(t, ctx, adminURL)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := seed.Run(ctx, pool, seed.Options{
		DemoName: seed.DefaultDemoName, DemoSecret: "demo-secret-test",
		TraderName: seed.DefaultTraderName, TraderSecret: "norte-secret-test",
		Ledger:              ledger.DefaultOptions(),
		SkipIndustrialWorld: true,
	}, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}

	region := regionID(t, ctx, pool, seed.RegionName)
	demo := accountID(t, ctx, pool, seed.DefaultDemoName)

	// ── Grafo multimodal ──────────────────────────────────────────────────────
	// Cadena inter-región: cA —road→ jA —rail→ jB —road→ cB, con jB —sea→ isla.
	// Terminales intermodales en jA y jB (cambios de modo). road 80 km/h, rail 120,
	// sea 40 (los pesos de la ETA reflejan la velocidad de cada modo).
	cA := insertNode(t, ctx, pool, region, "warehouse", 0, 0)
	jA := insertNode(t, ctx, pool, region, "junction", 10000, 0)
	jB := insertNode(t, ctx, pool, region, "junction", 210000, 0)
	cB := insertNode(t, ctx, pool, region, "warehouse", 220000, 0)
	island := insertNode(t, ctx, pool, region, "warehouse", 210000, 50000)

	roadCAtoJA := insertLink(t, ctx, pool, region, "road", cA, jA, 10000, 100, 80, "0 0", "10000 0", 1.0)
	railJAtoJB := insertLink(t, ctx, pool, region, "rail", jA, jB, 200000, 200, 120, "10000 0", "210000 0", 1.0)
	roadJBtoCB := insertLink(t, ctx, pool, region, "road", jB, cB, 10000, 100, 80, "210000 0", "220000 0", 1.0)
	insertLink(t, ctx, pool, region, "road", cB, jB, 10000, 100, 80, "220000 0", "210000 0", 1.0)
	seaJBtoIsland := insertLink(t, ctx, pool, region, "sea", jB, island, 50000, 400, 40, "210000 0", "210000 50000", 1.0)

	termJA := insertTerminal(t, ctx, pool, jA, demo)
	termJB := insertTerminal(t, ctx, pool, jB, demo)

	// Comparación rail vs road a IGUAL distancia (200 km): dos enlaces paralelos.
	p := insertNode(t, ctx, pool, region, "junction", 0, 100000)
	q := insertNode(t, ctx, pool, region, "junction", 200000, 100000)
	insertLink(t, ctx, pool, region, "rail", p, q, 200000, 200, 120, "0 100000", "200000 100000", 1.0)
	insertLink(t, ctx, pool, region, "road", p, q, 200000, 100, 80, "0 100000", "200000 100000", 1.0)

	// Cambio de modo en un nodo SIN terminal: r0 —road→ m —rail→ r1 (m sin terminal).
	r0 := insertNode(t, ctx, pool, region, "warehouse", 0, 200000)
	m := insertNode(t, ctx, pool, region, "junction", 20000, 200000)
	r1 := insertNode(t, ctx, pool, region, "warehouse", 40000, 200000)
	insertLink(t, ctx, pool, region, "road", r0, m, 20000, 100, 80, "0 200000", "20000 200000", 1.0)
	insertLink(t, ctx, pool, region, "rail", m, r1, 20000, 200, 120, "20000 200000", "40000 200000", 1.0)

	svc, err := logistics.NewService(pool, logistics.DefaultOptions(), logger, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	demoMux := newMux(svc, demo, logger)

	// ── (a) route-plan inter-región multimodal road→rail→road con transbordos ──
	t.Run("multimodal road-rail-road con transbordo y ETA coherente", func(t *testing.T) {
		plan := planOf(t, demoMux, fmt.Sprintf(`{"origin_node_id":%q,"destination_node_id":%q,"optimize":"time"}`, cA, cB))
		if len(plan.Legs) != 3 {
			t.Fatalf("legs = %d, esperado 3 (road, rail, road): %+v", len(plan.Legs), plan.Legs)
		}
		if plan.Legs[0].Mode != "road" || plan.Legs[1].Mode != "rail" || plan.Legs[2].Mode != "road" {
			t.Fatalf("modos = [%s,%s,%s], esperado [road,rail,road]", plan.Legs[0].Mode, plan.Legs[1].Mode, plan.Legs[2].Mode)
		}
		if plan.Legs[0].LinkID != roadCAtoJA.String() || plan.Legs[1].LinkID != railJAtoJB.String() || plan.Legs[2].LinkID != roadJBtoCB.String() {
			t.Fatalf("enlaces del plan inesperados: %+v", plan.Legs)
		}
		// Terminal de transbordo en cada cambio de modo (road→rail en jA, rail→road en jB).
		if plan.Legs[0].TransshipmentTerminalID != termJA.String() {
			t.Fatalf("transbordo del leg 0 = %q, esperado terminal de jA %q", plan.Legs[0].TransshipmentTerminalID, termJA)
		}
		if plan.Legs[1].TransshipmentTerminalID != termJB.String() {
			t.Fatalf("transbordo del leg 1 = %q, esperado terminal de jB %q", plan.Legs[1].TransshipmentTerminalID, termJB)
		}
		if plan.Legs[2].TransshipmentTerminalID != "" {
			t.Fatalf("el último leg no debe tener terminal: %q", plan.Legs[2].TransshipmentTerminalID)
		}
		// ETA = road(10km@80=1h) + rail(200km@120=ceil(1.67)=2h) + road(1h) +
		// 2 transbordos (volumen 0 ⇒ suelo 1h cada uno) = 1h+2h+1h+1h+1h = 6h = 21600 s.
		if plan.TotalEtaSimSeconds != 21600 {
			t.Fatalf("ETA total = %d, esperada 21600 (3 tramos + 2 transbordos)", plan.TotalEtaSimSeconds)
		}
	})

	// ── (b) rail más rápido que road en tierra a igual distancia ──────────────
	t.Run("rail mas rapido que road en tierra", func(t *testing.T) {
		railPlan := planOf(t, demoMux, fmt.Sprintf(`{"origin_node_id":%q,"destination_node_id":%q,"optimize":"time","modes":["rail"]}`, p, q))
		roadPlan := planOf(t, demoMux, fmt.Sprintf(`{"origin_node_id":%q,"destination_node_id":%q,"optimize":"time","modes":["road"]}`, p, q))
		if railPlan.Legs[0].Mode != "rail" || roadPlan.Legs[0].Mode != "road" {
			t.Fatalf("modos inesperados: rail=%s road=%s", railPlan.Legs[0].Mode, roadPlan.Legs[0].Mode)
		}
		// 200 km: rail@120 ⇒ ceil(1.67)=2h=7200; road@80 ⇒ ceil(2.5)=3h=10800.
		if railPlan.TotalEtaSimSeconds != 7200 || roadPlan.TotalEtaSimSeconds != 10800 {
			t.Fatalf("ETA rail=%d road=%d, esperado 7200 y 10800", railPlan.TotalEtaSimSeconds, roadPlan.TotalEtaSimSeconds)
		}
		if railPlan.TotalEtaSimSeconds >= roadPlan.TotalEtaSimSeconds {
			t.Fatalf("rail (%d) no es más rápido que road (%d)", railPlan.TotalEtaSimSeconds, roadPlan.TotalEtaSimSeconds)
		}
	})

	// ── (c) el mar es el ÚNICO camino a una región insular ────────────────────
	t.Run("sea es el unico camino a la isla", func(t *testing.T) {
		// Solo road: no hay forma de llegar a la isla → NO_ROUTE_FOUND.
		code, status := errorOf(t, demoMux, http.MethodPost, "/logistics/route-plans",
			fmt.Sprintf(`{"origin_node_id":%q,"destination_node_id":%q,"modes":["road"]}`, cB, island))
		if status != http.StatusUnprocessableEntity || code != "NO_ROUTE_FOUND" {
			t.Fatalf("isla solo por road: code=%s status=%d (esperado NO_ROUTE_FOUND)", code, status)
		}
		// Todos los modos: road hasta jB y sea a la isla (multimodal, transbordo en jB).
		plan := planOf(t, demoMux, fmt.Sprintf(`{"origin_node_id":%q,"destination_node_id":%q}`, cB, island))
		if len(plan.Legs) != 2 || plan.Legs[0].Mode != "road" || plan.Legs[1].Mode != "sea" {
			t.Fatalf("ruta a la isla inesperada: %+v", plan.Legs)
		}
		if plan.Legs[1].LinkID != seaJBtoIsland.String() {
			t.Fatalf("el tramo marítimo no es el esperado: %+v", plan.Legs)
		}
		if plan.Legs[0].TransshipmentTerminalID != termJB.String() {
			t.Fatalf("transbordo a la isla = %q, esperado terminal de jB %q", plan.Legs[0].TransshipmentTerminalID, termJB)
		}
	})

	// ── (d) cambio de modo SOLO en un nodo con terminal ───────────────────────
	t.Run("cambio de modo requiere terminal en el nodo", func(t *testing.T) {
		// m no tiene terminal: el único camino r0→r1 cambia de modo en m ⇒ NO_ROUTE.
		code, status := errorOf(t, demoMux, http.MethodPost, "/logistics/route-plans",
			fmt.Sprintf(`{"origin_node_id":%q,"destination_node_id":%q}`, r0, r1))
		if status != http.StatusUnprocessableEntity || code != "NO_ROUTE_FOUND" {
			t.Fatalf("cambio de modo sin terminal: code=%s status=%d (esperado NO_ROUTE_FOUND)", code, status)
		}
		// Con terminal en m, el mismo salto road→rail pasa a ser transitable.
		termM := insertTerminal(t, ctx, pool, m, demo)
		plan := planOf(t, demoMux, fmt.Sprintf(`{"origin_node_id":%q,"destination_node_id":%q}`, r0, r1))
		if len(plan.Legs) != 2 || plan.Legs[0].Mode != "road" || plan.Legs[1].Mode != "rail" {
			t.Fatalf("con terminal en m, ruta inesperada: %+v", plan.Legs)
		}
		if plan.Legs[0].TransshipmentTerminalID != termM.String() {
			t.Fatalf("transbordo en m = %q, esperado terminal de m %q", plan.Legs[0].TransshipmentTerminalID, termM)
		}
	})
}
