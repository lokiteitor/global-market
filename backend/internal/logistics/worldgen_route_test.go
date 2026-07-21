package logistics_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/logistics"
	"github.com/lokiteitor/global-market/backend/internal/seed"
	"github.com/lokiteitor/global-market/backend/internal/worldgen"
)

// TestLogisticsRouteOverGeneratedWorld ejercita el pathfinding multimodal sobre el
// MUNDO PROCEDURAL REAL (seed Askadia + worldgen: regiones conectadas por rail/sea,
// terminales intermodales en los junctions). Prueba end-to-end que un route-plan
// desde un nodo terrestre de una región hasta un junction de una región vecina es
// una ruta MULTIMODAL con transbordo en la terminal del junction de salida, y que el
// catálogo aditivo de vehículos rail/sea existe.
func TestLogisticsRouteOverGeneratedWorld(t *testing.T) {
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
		Ledger: ledger.DefaultOptions(),
	}, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := worldgen.Generate(ctx, pool, worldgen.DefaultOptions(), logger); err != nil {
		t.Fatalf("worldgen: %v", err)
	}

	demo := accountID(t, ctx, pool, seed.DefaultDemoName)
	svc, err := logistics.NewService(pool, logistics.DefaultOptions(), logger, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	demoMux := newMux(svc, demo, logger)

	// Catálogo aditivo rail/sea presente en el mundo generado.
	if v := countScalar(t, ctx, pool, `SELECT count(*) FROM world.vehicle_types WHERE mode IN ('rail','sea')`); v < 2 {
		t.Fatalf("faltan tipos de vehículo rail/sea en el mundo generado: %d", v)
	}
	if term := countScalar(t, ctx, pool, `SELECT count(*) FROM world.terminals`); term == 0 {
		t.Fatal("el mundo generado no tiene terminales intermodales")
	}

	// Encuentra un caso multimodal FORZADO en el grafo real: una terminal J con un
	// nodo road-only R que solo la alcanza por road, y un vecino K al que J llega por
	// rail/sea. La única ruta R→K es road(R→J) + inter-región(J→K): multimodal.
	var termID, jNode, rNode, kNode uuid.UUID
	var interMode string
	err = pool.QueryRow(ctx, `
		SELECT t.id, t.node_id, rl.from_node_id, il.to_node_id, il.mode::text
		FROM world.terminals t
		JOIN world.network_links rl ON rl.to_node_id = t.node_id AND rl.mode = 'road'
		JOIN world.network_links il ON il.from_node_id = t.node_id AND il.mode IN ('rail','sea')
		WHERE NOT EXISTS (
			SELECT 1 FROM world.network_links x
			WHERE x.from_node_id = rl.from_node_id AND x.mode IN ('rail','sea'))
		ORDER BY t.node_id, rl.from_node_id, il.to_node_id
		LIMIT 1`).Scan(&termID, &jNode, &rNode, &kNode, &interMode)
	if errors.Is(err, pgx.ErrNoRows) {
		t.Skip("el mundo generado no ofrece un caso multimodal forzado (topología de la semilla)")
	}
	if err != nil {
		t.Fatalf("buscando un caso multimodal en el grafo generado: %v", err)
	}

	plan := planOf(t, demoMux, fmt.Sprintf(`{"origin_node_id":%q,"destination_node_id":%q,"optimize":"time"}`, rNode, kNode))
	if len(plan.Legs) < 2 {
		t.Fatalf("ruta R→K sobre el mundo generado con %d tramos, esperado ≥2 (multimodal): %+v", len(plan.Legs), plan.Legs)
	}
	if plan.Legs[0].Mode != "road" {
		t.Fatalf("primer tramo = %s, esperado road (R es road-only): %+v", plan.Legs[0].Mode, plan.Legs)
	}
	// El último tramo cruza al vecino por el modo inter-región (rail o sea).
	last := plan.Legs[len(plan.Legs)-1]
	if last.Mode != interMode {
		t.Fatalf("último tramo = %s, esperado %s (enlace inter-región): %+v", last.Mode, interMode, plan.Legs)
	}
	// El transbordo ocurre en la terminal del junction de salida J.
	foundTransship := false
	for _, leg := range plan.Legs {
		if leg.TransshipmentTerminalID == termID.String() {
			foundTransship = true
		}
	}
	if !foundTransship {
		t.Fatalf("ningún tramo transborda en la terminal de J (%s): %+v", termID, plan.Legs)
	}
	if plan.TotalEtaSimSeconds <= 0 {
		t.Fatalf("ETA total no positiva: %d", plan.TotalEtaSimSeconds)
	}
}

// countScalar ejecuta una consulta escalar que devuelve un entero.
func countScalar(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
		t.Fatalf("consulta %q: %v", query, err)
	}
	return n
}
