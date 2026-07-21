package enforcement_test

import (
	"context"
	"encoding/json"
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
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/platform/migrate"
	"github.com/lokiteitor/global-market/backend/internal/seed"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
	"github.com/lokiteitor/global-market/backend/internal/world/enforcement"
	"github.com/lokiteitor/global-market/backend/internal/world/land"
)

// notDue es un maintenance_paid_until_sim / expires_at_sim lejano: deja una
// entidad FUERA de todos los barridos (los sub-tests hacen "due" solo su
// objetivo para aislarse entre sí).
const notDue int64 = 1_000_000_000_000

const day = simtime.SimDay

// TestEnforcementIntegration ejercita la cascada de insolvencia (Incremento 6a)
// contra una BD real con el esquema migrado (incl. 0011) y el seed del
// Incremento 1-4 (Demo y Norte, cada una con caja 1.000.000, una concesión y un
// almacén operativo con stock). Cada sub-test fija su estado por SQL, avanza el
// reloj (mutSim) para simular días y ejecuta los barridos del motor directamente.
//
// Cubre: (a) mantenimiento cobra cash→sink; (b) sin fondos degrada →
// operational→damaged→abandoned y para la producción; opex de flota; canon
// vigente cobrado (renovación); (c) canon impagado active→delinquent→grace→
// reverted; (d) embargo emite building.seized con el stock correcto, congela el
// edificio, revierte la concesión y LIBERA la parcela (nuevo POST ya no da 409);
// embargo por abandono (rama mantenimiento); y (e) la caja jamás queda negativa.
//
// Se omite si II_TEST_DATABASE_URL no está definida (la URL solo identifica el
// servidor; el test crea una BD EFÍMERA propia con CREATEDB).
func TestEnforcementIntegration(t *testing.T) {
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

	demo := accountID(t, ctx, pool, seed.DefaultDemoName)
	norte := accountID(t, ctx, pool, seed.DefaultTraderName)
	demoBuilding := buildingOf(t, ctx, pool, demo)
	norteBuilding := buildingOf(t, ctx, pool, norte)
	demoConcession := concessionOf(t, ctx, pool, demo)
	norteConcession := concessionOf(t, ctx, pool, norte)

	sim := &mutSim{}
	reg := prometheus.NewRegistry()
	w, err := enforcement.NewWorker(pool, sim, enforcement.DefaultWorkerOptions(), logger, reg)
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}

	// ── (a) Mantenimiento cobra y baja cash → sink ────────────────────────────
	t.Run("mantenimiento cobra cash->sink", func(t *testing.T) {
		setAllNotDue(t, ctx, pool)
		setBuildingMaint(t, ctx, pool, demoBuilding, 0, "operational", 100)
		sim.set(5 * day) // 5 días vencidos

		cashBefore := cashBalance(t, ctx, pool, demo)
		sinkBefore := sinkBalance(t, ctx, pool)

		if _, err := w.SweepBuildingMaintenance(ctx); err != nil {
			t.Fatalf("SweepBuildingMaintenance: %v", err)
		}
		assertNoNegativeCash(t, ctx, pool)

		const cost = 50 * 5 // maintenance_cost(50) × 5 días
		if d := cashBefore - cashBalance(t, ctx, pool, demo); d != cost {
			t.Fatalf("la caja de Demo cayó %d, esperado %d (mantenimiento)", d, cost)
		}
		if d := sinkBalance(t, ctx, pool) - sinkBefore; d != cost {
			t.Fatalf("el sink subió %d, esperado %d (mantenimiento)", d, cost)
		}
		st, cond, paid := buildingState(t, ctx, pool, demoBuilding)
		if st != "operational" || cond != 100 || paid != 5*day {
			t.Fatalf("estado del edificio tras cobro: status=%s cond=%d paid=%d (esperado operational/100/%d)", st, cond, paid, 5*day)
		}
	})

	// ── Opex de flota: cobra cash → sink (solo drena; sin condición) ──────────
	vehicle, opex := insertVehicle(t, ctx, pool, demo, demoBuilding)
	t.Run("opex de vehiculo cobra cash->sink", func(t *testing.T) {
		setAllNotDue(t, ctx, pool)
		mustExec(t, ctx, pool, `UPDATE world.vehicles SET maintenance_paid_until_sim = 0 WHERE id = $1`, vehicle)
		sim.set(3 * day)

		cashBefore := cashBalance(t, ctx, pool, demo)
		sinkBefore := sinkBalance(t, ctx, pool)
		if _, err := w.SweepVehicleMaintenance(ctx); err != nil {
			t.Fatalf("SweepVehicleMaintenance: %v", err)
		}
		assertNoNegativeCash(t, ctx, pool)

		want := opex * 3
		if d := cashBefore - cashBalance(t, ctx, pool, demo); d != want {
			t.Fatalf("la caja de Demo cayó %d, esperado %d (opex)", d, want)
		}
		if d := sinkBalance(t, ctx, pool) - sinkBefore; d != want {
			t.Fatalf("el sink subió %d, esperado %d (opex)", d, want)
		}
	})

	// ── Canon vigente cobrado: renovación happy-path ──────────────────────────
	t.Run("canon vigente renueva y cobra", func(t *testing.T) {
		setAllNotDue(t, ctx, pool)
		mustExec(t, ctx, pool, `UPDATE world.land_concessions SET expires_at_sim = 0, status = 'active', grace_until_sim = NULL WHERE id = $1`, norteConcession)
		sim.set(100 * day)

		cashBefore := cashBalance(t, ctx, pool, norte)
		sinkBefore := sinkBalance(t, ctx, pool)
		if _, err := w.SweepCanon(ctx); err != nil {
			t.Fatalf("SweepCanon: %v", err)
		}
		assertNoNegativeCash(t, ctx, pool)

		const canon = 1000
		if d := cashBefore - cashBalance(t, ctx, pool, norte); d != canon {
			t.Fatalf("la caja de Norte cayó %d, esperado %d (canon)", d, canon)
		}
		if d := sinkBalance(t, ctx, pool) - sinkBefore; d != canon {
			t.Fatalf("el sink subió %d, esperado %d (canon)", d, canon)
		}
		st, expires, _ := concessionState(t, ctx, pool, norteConcession)
		if st != "active" || expires != 90*day { // 0 + period(90) × SimDay
			t.Fatalf("concesión tras renovar: status=%s expires=%d (esperado active/%d)", st, expires, 90*day)
		}
	})

	// ── (b) Sin fondos: degrada operational→damaged→abandoned; para producción ─
	batchID := insertRunningBatch(t, ctx, pool, norteBuilding)
	t.Run("sin fondos degrada y abandona; para produccion", func(t *testing.T) {
		drainCash(t, ctx, pool, norte)
		setAllNotDue(t, ctx, pool)
		setBuildingMaint(t, ctx, pool, norteBuilding, 0, "operational", 100)

		// Fase 1: 5 días impagados → damaged, condición 75 (100 − 5×5).
		sim.set(5 * day)
		sinkBefore := sinkBalance(t, ctx, pool)
		if _, err := w.SweepBuildingMaintenance(ctx); err != nil {
			t.Fatalf("SweepBuildingMaintenance fase 1: %v", err)
		}
		assertNoNegativeCash(t, ctx, pool)
		if st, cond, _ := buildingState(t, ctx, pool, norteBuilding); st != "damaged" || cond != 75 {
			t.Fatalf("fase 1: status=%s cond=%d (esperado damaged/75)", st, cond)
		}
		if d := sinkBalance(t, ctx, pool) - sinkBefore; d != 0 {
			t.Fatalf("el sink cambió %d con caja a 0 (esperado 0: no se cobra sin fondos)", d)
		}

		// Fase 2: hasta 20 días → condición 0 (≤ 20) → abandoned.
		sim.set(20 * day)
		if _, err := w.SweepBuildingMaintenance(ctx); err != nil {
			t.Fatalf("SweepBuildingMaintenance fase 2: %v", err)
		}
		assertNoNegativeCash(t, ctx, pool)
		st, cond, paid := buildingState(t, ctx, pool, norteBuilding)
		if st != "abandoned" || cond != 0 {
			t.Fatalf("fase 2: status=%s cond=%d (esperado abandoned/0)", st, cond)
		}
		if paid != 20*day { // el marcador se reancla al instante del abandono
			t.Fatalf("fase 2: maintenance_paid_until_sim=%d (esperado %d, instante del abandono)", paid, 20*day)
		}
		if bs := batchStatus(t, ctx, pool, batchID); bs != "paused_no_workers" {
			t.Fatalf("el lote quedó en %q, esperado paused_no_workers (producción parada)", bs)
		}
		if got := cashBalance(t, ctx, pool, norte); got != 0 {
			t.Fatalf("la caja de Norte quedó en %d, esperado 0 (nunca negativa)", got)
		}
	})

	// ── Embargo por ABANDONO (rama mantenimiento): revierte el suelo aunque el
	//    canon estuviese al día; building.seized reason=abandoned ──────────────
	t.Run("embargo por abandono congela y revierte", func(t *testing.T) {
		setAllNotDue(t, ctx, pool)
		// Norte building quedó abandoned con paid=20*day; su gracia vence a
		// 20*day + II_SEIZE_GRACE_SIM_SECONDS.
		sim.set(20*day + enforcement.DefaultSeizeGraceSimSeconds + day)

		if _, err := w.SweepEmbargo(ctx); err != nil {
			t.Fatalf("SweepEmbargo: %v", err)
		}
		assertNoNegativeCash(t, ctx, pool)

		if st, _, _ := buildingState(t, ctx, pool, norteBuilding); st != "seized" {
			t.Fatalf("Norte building status=%s, esperado seized", st)
		}
		if st, _, _ := concessionState(t, ctx, pool, norteConcession); st != "reverted" {
			t.Fatalf("Norte concesión status=%s, esperado reverted (el suelo revierte con el inmueble)", st)
		}
		payload := lastSeizedPayload(t, ctx, pool, norteBuilding)
		if payload.Reason != "abandoned" {
			t.Fatalf("building.seized reason=%q, esperado abandoned", payload.Reason)
		}
		assertSeizedStock(t, payload, map[string]string{"iron_ore": "5000", "coal": "3000"}, ctx, pool, norteBuilding)
	})

	// ── (c)+(d) Canon impagado: active→delinquent→grace→reverted; embargo emite
	//    building.seized (reason canon_reverted) y LIBERA la parcela ────────────
	landSvc, err := land.NewService(pool, sim, land.DefaultOptions(), logger, nil)
	if err != nil {
		t.Fatalf("land.NewService: %v", err)
	}
	t.Run("canon impagado revierte y embarga; parcela liberada", func(t *testing.T) {
		setAllNotDue(t, ctx, pool)
		demoParcel := concessionParcel(t, ctx, pool, demoConcession)

		// La parcela de Demo se solapa consigo misma mientras la concesión está
		// ACTIVA → 409 CONCESSION_OVERLAP.
		if _, err := landSvc.CreateConcession(ctx, demo, land.ConcessionInput{RegionID: regionID(t, ctx, pool), Parcel: []byte(demoParcel)}); !errors.Is(err, land.ErrParcelOverlap) {
			t.Fatalf("POST concesión solapada con la activa: err=%v, esperado ErrParcelOverlap", err)
		}

		drainCash(t, ctx, pool, demo)
		mustExec(t, ctx, pool, `UPDATE world.land_concessions SET expires_at_sim = 0, status = 'active', grace_until_sim = NULL WHERE id = $1`, demoConcession)

		// Paso 1: periodo vencido + canon impagable → delinquent + gracia.
		sim.set(100 * day)
		if _, err := w.SweepCanon(ctx); err != nil {
			t.Fatalf("SweepCanon (delinquent): %v", err)
		}
		assertNoNegativeCash(t, ctx, pool)
		st, _, grace := concessionState(t, ctx, pool, demoConcession)
		if st != "delinquent" || grace == nil {
			t.Fatalf("tras impago: status=%s grace_until=%v (esperado delinquent con gracia)", st, grace)
		}
		if *grace != 100*day+enforcement.DefaultSeizeGraceSimSeconds {
			t.Fatalf("grace_until_sim=%d, esperado %d", *grace, 100*day+enforcement.DefaultSeizeGraceSimSeconds)
		}

		// Paso 2: gracia agotada → grace.
		sim.set(*grace + 1)
		if _, err := w.SweepCanon(ctx); err != nil {
			t.Fatalf("SweepCanon (grace): %v", err)
		}
		if st, _, _ := concessionState(t, ctx, pool, demoConcession); st != "grace" {
			t.Fatalf("tras gracia: status=%s, esperado grace", st)
		}

		// Paso 3: embargo → building.seized (reason canon_reverted) + reverted.
		if _, err := w.SweepEmbargo(ctx); err != nil {
			t.Fatalf("SweepEmbargo: %v", err)
		}
		assertNoNegativeCash(t, ctx, pool)
		if st, _, _ := concessionState(t, ctx, pool, demoConcession); st != "reverted" {
			t.Fatalf("tras embargo: status=%s, esperado reverted", st)
		}
		if st, _, _ := buildingState(t, ctx, pool, demoBuilding); st != "seized" {
			t.Fatalf("Demo building status=%s, esperado seized", st)
		}
		payload := lastSeizedPayload(t, ctx, pool, demoBuilding)
		if payload.Reason != "canon_reverted" {
			t.Fatalf("building.seized reason=%q, esperado canon_reverted", payload.Reason)
		}
		assertSeizedStock(t, payload, map[string]string{"iron_ore": "5000", "coal": "3000"}, ctx, pool, demoBuilding)
		if !revertedEventEmitted(t, ctx, pool, demoConcession, demo) {
			t.Fatalf("no se emitió concession.reverted con former_holder=Demo")
		}

		// Parcela LIBERADA: un nuevo POST sobre la misma parcela ya no da 409.
		mintCash(t, ctx, pool, demo, 100_000)
		if _, err := landSvc.CreateConcession(ctx, demo, land.ConcessionInput{RegionID: regionID(t, ctx, pool), Parcel: []byte(demoParcel)}); err != nil {
			t.Fatalf("POST concesión sobre parcela revertida: err=%v, esperado éxito (parcela liberada)", err)
		}
	})

	// ── Métricas: coherentes con lo ejercitado ────────────────────────────────
	t.Run("metricas de la cascada", func(t *testing.T) {
		checks := map[string]float64{
			"ii_buildings_abandoned_total":    1, // Norte
			"ii_buildings_seized_total":       2, // Norte + Demo
			"ii_concessions_reverted_total":   2, // Norte + Demo
			"ii_concessions_delinquent_total": 1, // Demo
		}
		for name, want := range checks {
			if got := counterValue(t, reg, name); got != want {
				t.Fatalf("métrica %s = %v, esperado %v", name, got, want)
			}
		}
		if got := counterValue(t, reg, "ii_maintenance_charged_total"); got <= 0 {
			t.Fatalf("ii_maintenance_charged_total = %v, esperado > 0", got)
		}
		if got := counterValue(t, reg, "ii_canon_charged_total"); got != 1000 {
			t.Fatalf("ii_canon_charged_total = %v, esperado 1000", got)
		}
	})
}

// ─── Reloj mutable (avanzado por el test para simular días) ───────────────────

type mutSim struct{ now int64 }

func (m *mutSim) Now(context.Context) simtime.SimTime { return simtime.SimTime(m.now) }
func (m *mutSim) set(n int64)                         { m.now = n }

// ─── Payload de building.seized para las aserciones ──────────────────────────

type seizedPayload struct {
	BuildingID   string `json:"building_id"`
	OriginNodeID string `json:"origin_node_id"`
	Reason       string `json:"reason"`
	Stock        []struct {
		ProductID           string `json:"product_id"`
		Quantity            string `json:"quantity"`
		WarehouseBuildingID string `json:"warehouse_building_id"`
	} `json:"stock"`
	SeizedAtSim int64 `json:"seized_at_sim"`
}

func lastSeizedPayload(t *testing.T, ctx context.Context, pool *pgxpool.Pool, building uuid.UUID) seizedPayload {
	t.Helper()
	var raw []byte
	if err := pool.QueryRow(ctx, `
		SELECT payload FROM outbox.events
		 WHERE event_type = 'building.seized' AND aggregate_id = $1
		 ORDER BY seq DESC LIMIT 1`, building).Scan(&raw); err != nil {
		t.Fatalf("leyendo building.seized de %s: %v", building, err)
	}
	var p seizedPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("payload building.seized inválido: %v (%s)", err, raw)
	}
	return p
}

// assertSeizedStock comprueba que el stock del evento coincide con el stock_free
// del edificio (por código de producto) y que warehouse_building_id/origin_node
// son coherentes.
func assertSeizedStock(t *testing.T, p seizedPayload, wantByCode map[string]string, ctx context.Context, pool *pgxpool.Pool, building uuid.UUID) {
	t.Helper()
	node := buildingNode(t, ctx, pool, building)
	if p.OriginNodeID != node.String() {
		t.Fatalf("origin_node_id=%q, esperado %q (nodo del edificio)", p.OriginNodeID, node)
	}
	got := map[string]string{}
	for _, s := range p.Stock {
		if s.WarehouseBuildingID != building.String() {
			t.Fatalf("línea de stock con warehouse_building_id=%q, esperado %q", s.WarehouseBuildingID, building)
		}
		got[productCode(t, ctx, pool, s.ProductID)] = s.Quantity
	}
	if len(got) != len(wantByCode) {
		t.Fatalf("stock embargado con %d líneas, esperado %d (%v)", len(got), len(wantByCode), got)
	}
	for code, qty := range wantByCode {
		if got[code] != qty {
			t.Fatalf("stock embargado de %s = %q, esperado %q", code, got[code], qty)
		}
	}
}

func revertedEventEmitted(t *testing.T, ctx context.Context, pool *pgxpool.Pool, concession, former uuid.UUID) bool {
	t.Helper()
	var former2 string
	err := pool.QueryRow(ctx, `
		SELECT payload->>'former_holder' FROM outbox.events
		 WHERE event_type = 'concession.reverted' AND aggregate_id = $1
		 ORDER BY seq DESC LIMIT 1`, concession).Scan(&former2)
	if errors.Is(err, pgx.ErrNoRows) {
		return false
	}
	if err != nil {
		t.Fatalf("leyendo concession.reverted de %s: %v", concession, err)
	}
	return former2 == former.String()
}

// ─── Fixtures / mutaciones por SQL ───────────────────────────────────────────

func setAllNotDue(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	mustExec(t, ctx, pool, `UPDATE world.buildings SET maintenance_paid_until_sim = $1 WHERE status IN ('operational','damaged')`, notDue)
	mustExec(t, ctx, pool, `UPDATE world.vehicles SET maintenance_paid_until_sim = $1`, notDue)
	mustExec(t, ctx, pool, `UPDATE world.land_concessions SET expires_at_sim = $1, grace_until_sim = NULL WHERE status = 'active'`, notDue)
}

func setBuildingMaint(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, paid int64, status string, cond int32) {
	t.Helper()
	mustExec(t, ctx, pool, `UPDATE world.buildings SET maintenance_paid_until_sim = $1, status = $2::world.building_status, condition_pct = $3 WHERE id = $4`, paid, status, cond, id)
}

func insertVehicle(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner, building uuid.UUID) (uuid.UUID, int64) {
	t.Helper()
	var vtID uuid.UUID
	var opex int64
	if err := pool.QueryRow(ctx, `SELECT id, operating_cost_per_day FROM world.vehicle_types ORDER BY operating_cost_per_day DESC LIMIT 1`).Scan(&vtID, &opex); err != nil {
		t.Fatalf("vehicle_type: %v", err)
	}
	node := buildingNode(t, ctx, pool, building)
	id := uuid.Must(uuid.NewV7())
	mustExec(t, ctx, pool, `
		INSERT INTO world.vehicles (id, vehicle_type_id, owner_account_id, status, wear_pct, fuel, at_node_id, updated_at_sim, maintenance_paid_until_sim)
		VALUES ($1, $2, $3, 'idle', 0, 0, $4, 0, 0)`, id, vtID, owner, node)
	return id, opex
}

func insertRunningBatch(t *testing.T, ctx context.Context, pool *pgxpool.Pool, building uuid.UUID) uuid.UUID {
	t.Helper()
	var recipe uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM world.recipes ORDER BY id LIMIT 1`).Scan(&recipe); err != nil {
		t.Fatalf("recipe: %v", err)
	}
	id := uuid.Must(uuid.NewV7())
	mustExec(t, ctx, pool, `
		INSERT INTO world.production_batches (id, building_id, recipe_id, batches_queued, batches_done, status, queue_position, started_at_sim, updated_at_sim)
		VALUES ($1, $2, $3, 3, 0, 'running', 0, 0, 0)`, id, building, recipe)
	return id
}

// drainCash lleva la caja de una corporación a 0 (asiento cash → sink), para
// forzar la insolvencia sin dejar la caja negativa.
func drainCash(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner uuid.UUID) {
	t.Helper()
	bal := cashBalance(t, ctx, pool, owner)
	if bal <= 0 {
		return
	}
	var cashID, sinkID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM ledger.accounts WHERE kind='cash' AND owner_account_id=$1`, owner).Scan(&cashID); err != nil {
		t.Fatalf("cash id de %s: %v", owner, err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM ledger.accounts WHERE kind='sink' ORDER BY id LIMIT 1`).Scan(&sinkID); err != nil {
		t.Fatalf("sink id: %v", err)
	}
	postLedger(t, ctx, pool, "transfer", []entry{{cashID, -bal}, {sinkID, bal}})
}

// mintCash acredita caja a una corporación (asiento emission → cash) para
// habilitar un cobro de canon posterior (el POST de concesión cobra canon).
func mintCash(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner uuid.UUID, amount int64) {
	t.Helper()
	var cashID, emissionID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM ledger.accounts WHERE kind='cash' AND owner_account_id=$1`, owner).Scan(&cashID); err != nil {
		t.Fatalf("cash id de %s: %v", owner, err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM ledger.accounts WHERE kind='emission' ORDER BY id LIMIT 1`).Scan(&emissionID); err != nil {
		t.Fatalf("emission id: %v", err)
	}
	postLedger(t, ctx, pool, "seed_capital", []entry{{emissionID, -amount}, {cashID, amount}})
}

type entry struct {
	account uuid.UUID
	amount  int64
}

// postLedger asienta cabecera + partidas en UNA sola transacción (el balance por
// activo es un constraint trigger DIFERIDO: se evalúa en el COMMIT, así que todas
// las partidas deben confirmar juntas).
func postLedger(t *testing.T, ctx context.Context, pool *pgxpool.Pool, kind string, entries []entry) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback tras commit es no-op
	txID := uuid.Must(uuid.NewV7())
	if _, err := tx.Exec(ctx, `INSERT INTO ledger.transactions (id, kind, sim_time_at) VALUES ($1, $2::ledger.transaction_kind, 0)`, txID, kind); err != nil {
		t.Fatalf("insert transaction: %v", err)
	}
	for _, e := range entries {
		if _, err := tx.Exec(ctx, `INSERT INTO ledger.entries (id, transaction_id, account_id, amount) VALUES ($1, $2, $3, $4)`,
			uuid.Must(uuid.NewV7()), txID, e.account, e.amount); err != nil {
			t.Fatalf("insert entry: %v", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func mustExec(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

// ─── Lecturas de estado ──────────────────────────────────────────────────────

func buildingState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (status string, cond int32, paid int64) {
	t.Helper()
	if err := pool.QueryRow(ctx, `SELECT status, condition_pct, maintenance_paid_until_sim FROM world.buildings WHERE id=$1`, id).Scan(&status, &cond, &paid); err != nil {
		t.Fatalf("estado del edificio %s: %v", id, err)
	}
	return status, cond, paid
}

func concessionState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (status string, expires int64, grace *int64) {
	t.Helper()
	if err := pool.QueryRow(ctx, `SELECT status, expires_at_sim, grace_until_sim FROM world.land_concessions WHERE id=$1`, id).Scan(&status, &expires, &grace); err != nil {
		t.Fatalf("estado de la concesión %s: %v", id, err)
	}
	return status, expires, grace
}

func batchStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) string {
	t.Helper()
	var st string
	if err := pool.QueryRow(ctx, `SELECT status FROM world.production_batches WHERE id=$1`, id).Scan(&st); err != nil {
		t.Fatalf("estado del lote %s: %v", id, err)
	}
	return st
}

func assertNoNegativeCash(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ledger.accounts WHERE kind='cash' AND balance < 0`).Scan(&n); err != nil {
		t.Fatalf("comprobando cajas negativas: %v", err)
	}
	if n != 0 {
		t.Fatalf("%d caja(s) con saldo negativo (invariante GDD 5.9 violado)", n)
	}
}

func cashBalance(t *testing.T, ctx context.Context, pool *pgxpool.Pool, acc uuid.UUID) int64 {
	t.Helper()
	var bal int64
	if err := pool.QueryRow(ctx, `SELECT balance FROM ledger.accounts WHERE kind='cash' AND owner_account_id=$1`, acc).Scan(&bal); err != nil {
		t.Fatalf("saldo de caja de %s: %v", acc, err)
	}
	return bal
}

func sinkBalance(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int64 {
	t.Helper()
	var bal int64
	if err := pool.QueryRow(ctx, `SELECT balance FROM ledger.accounts WHERE kind='sink' ORDER BY id LIMIT 1`).Scan(&bal); err != nil {
		t.Fatalf("saldo del sink: %v", err)
	}
	return bal
}

func accountID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM auth.accounts WHERE name=$1`, name).Scan(&id); err != nil {
		t.Fatalf("cuenta %q: %v", name, err)
	}
	return id
}

func regionID(t *testing.T, ctx context.Context, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM world.regions WHERE name=$1`, seed.RegionName).Scan(&id); err != nil {
		t.Fatalf("región %q: %v", seed.RegionName, err)
	}
	return id
}

func buildingOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM world.buildings WHERE owner_account_id=$1 ORDER BY id LIMIT 1`, owner).Scan(&id); err != nil {
		t.Fatalf("edificio de %s: %v", owner, err)
	}
	return id
}

func concessionOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, holder uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM world.land_concessions WHERE holder_account_id=$1 AND status='active' ORDER BY id LIMIT 1`, holder).Scan(&id); err != nil {
		t.Fatalf("concesión de %s: %v", holder, err)
	}
	return id
}

func buildingNode(t *testing.T, ctx context.Context, pool *pgxpool.Pool, building uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM world.network_nodes WHERE building_id=$1 ORDER BY id LIMIT 1`, building).Scan(&id); err != nil {
		t.Fatalf("nodo del edificio %s: %v", building, err)
	}
	return id
}

func concessionParcel(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) string {
	t.Helper()
	var geo string
	if err := pool.QueryRow(ctx, `SELECT ST_AsGeoJSON(parcel)::text FROM world.land_concessions WHERE id=$1`, id).Scan(&geo); err != nil {
		t.Fatalf("parcela de %s: %v", id, err)
	}
	return geo
}

func productCode(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id string) string {
	t.Helper()
	var code string
	if err := pool.QueryRow(ctx, `SELECT code FROM world.products WHERE id=$1`, id).Scan(&code); err != nil {
		t.Fatalf("producto %s: %v", id, err)
	}
	return code
}

func counterValue(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	fams, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather métricas: %v", err)
	}
	for _, mf := range fams {
		if mf.GetName() != name {
			continue
		}
		var sum float64
		for _, m := range mf.GetMetric() {
			sum += m.GetCounter().GetValue()
		}
		return sum
	}
	return 0
}

// ─── BD efímera (mismo patrón que world/land) ────────────────────────────────

func newEphemeralDB(t *testing.T, ctx context.Context, adminURL string) *pgxpool.Pool {
	t.Helper()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("conectando a %s: %v", adminURL, err)
	}
	dbName := fmt.Sprintf("worldenforcementtest_%d_%d", os.Getpid(), time.Now().UnixNano())
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
	if _, err := migrate.New(conn, "../../../db/migrations", "dev", io.Discard).Up(ctx); err != nil {
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
