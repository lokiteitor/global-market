// Integración del RETIRO DE BOTS INSOLVENTES (ADR-024, GDD 5.9) contra una BD
// real con el esquema migrado y el seed de los Incrementos 1-4. Ejercita el
// RetirementJob directamente (con un reloj de simulación inyectado, avanzado por
// el test) sobre una población aprovisionada por el orquestador. Sin mocks.
//
// Cubre el mandato del incremento:
//
//  1. INSOLVENCIA SOSTENIDA: un bot sin edificios/contratos/publicaciones y con
//     caja bajo el piso NO se retira de inmediato (se marca insolvent_since_sim);
//     al cumplirse la ventana II_BOT_RETIRE_IDLE_SIM_SECONDS, el barrido lo
//     retira: absorbe su caja al banco central (la emisión sube hacia 0), marca
//     la cuenta 'retired', desactiva el perfil y emite bot.retired.
//  2. SOLVENTE / CON ACTIVIDAD no se retira: un bot con caja suficiente y otro
//     con caja 0 pero con un edificio no embargado quedan intactos.
//  3. El retirado NO vuelve a ser candidato (barrido idempotente) y el ledger
//     cuadra (la masa monetaria baja exactamente lo absorbido).
//  4. RETIRO INSTANTÁNEO (II_BOT_RETIRE_IDLE_SIM_SECONDS = 0) de un bot con caja
//     0: se retira en un solo barrido, con absorbed_cash = "0".
//
// Se omite si II_TEST_DATABASE_URL no está definida (BD efímera propia).
package bots_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/bots"
	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/seed"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

const (
	retireFloor  int64 = 1000
	retireWindow int64 = 1000 // ventana de gracia (sim-time) del sub-test sostenido
	coalBot2Name       = "Bot Carbonera 02"
)

func TestBotsRetirementIntegration(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test de integración omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	pool := newEphemeralDB(t, ctx, adminURL)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := seed.Run(ctx, pool, seed.Options{
		DemoName: itDemoName, DemoSecret: itDemoSecret,
		TraderName: itNorteName, TraderSecret: itNorteSecret,
		Ledger: ledger.DefaultOptions(),
	}, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}
	freezeSim(t, ctx, pool, itSimBase)

	// ── Población: 2 carboneras + 1 minera + 1 mercader ──────────────────────
	botsOpts := bots.Options{
		CoalProducers: 2, IronProducers: 1, Traders: 1,
		SecretSeed: itSecretSeed, Capital: itCapital,
		Tick: time.Second, Addr: ":0", APIURL: "http://localhost:0/api/v1",
	}
	orch, err := bots.NewOrchestrator(pool, botsOpts, ledger.DefaultOptions(), logger, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewOrchestrator: %v", err)
	}
	provisioned, err := orch.Provision(ctx)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	byName := map[string]uuid.UUID{}
	for _, b := range provisioned {
		byName[b.Name] = b.AccountID
	}
	insolvent := byName[coalBotName]      // Bot Carbonera 01: se volverá insolvente
	solvent := byName[ironBotName]        // Bot Minera 01: conserva su capital
	withBuilding := byName[traderBotName] // Bot Mercader 01: caja 0 pero con edificio
	instant := byName[coalBot2Name]       // Bot Carbonera 02: retiro instantáneo (caja 0)
	for name, id := range map[string]uuid.UUID{
		coalBotName: insolvent, ironBotName: solvent, traderBotName: withBuilding, coalBot2Name: instant,
	} {
		if id == uuid.Nil {
			t.Fatalf("falta el bot %q en la población aprovisionada", name)
		}
	}

	// Estado inicial de los actores:
	//   * Carbonera 01: caja por debajo del piso (300) y sin actividad → insolvente.
	//   * Mercader 01: caja 0 pero dueño de un edificio no embargado → NO insolvente.
	//   * Minera 01 y Carbonera 02: conservan su capital (solventes) por ahora.
	drainCashTo(t, ctx, pool, insolvent, 300)
	drainCashTo(t, ctx, pool, withBuilding, 0)
	reassignBuilding(t, ctx, pool, withBuilding)

	// ── Barrido con ventana de gracia (insolvencia sostenida) ────────────────
	sim := &retireSim{now: itSimBase}
	reg := prometheus.NewRegistry()
	job, err := bots.NewRetirementJob(pool, ledger.DefaultOptions(), bots.RetirementOptions{
		Interval: time.Minute, CashFloor: retireFloor, IdleSimSeconds: retireWindow,
	}, sim, logger, reg)
	if err != nil {
		t.Fatalf("NewRetirementJob: %v", err)
	}

	t.Run("insolvencia sostenida: primero se marca, no se retira", func(t *testing.T) {
		job.RunOnce(ctx)

		if st := botStatus(t, ctx, pool, insolvent); st != "active" {
			t.Fatalf("Carbonera 01 status=%s tras el primer barrido, esperado active (dentro de la gracia)", st)
		}
		mark := insolventMark(t, ctx, pool, insolvent)
		if mark == nil || *mark != itSimBase {
			t.Fatalf("marca insolvent_since_sim=%v, esperada %d (arranque del reloj de gracia)", mark, itSimBase)
		}
		if got := counterValue(t, reg, "ii_bots_retired_total"); got != 0 {
			t.Fatalf("ii_bots_retired_total=%v tras el primer barrido, esperado 0", got)
		}
		// El solvente y el que tiene edificio no se marcan.
		if m := insolventMark(t, ctx, pool, solvent); m != nil {
			t.Fatalf("Minera 01 (solvente) quedó marcada: %v", *m)
		}
		if m := insolventMark(t, ctx, pool, withBuilding); m != nil {
			t.Fatalf("Mercader 01 (con edificio) quedó marcada: %v", *m)
		}
	})

	t.Run("cumplida la ventana: retira, absorbe y emite", func(t *testing.T) {
		emissionBefore := emissionBalance(t, ctx, pool)
		sim.set(itSimBase + retireWindow + 1) // ventana de gracia agotada

		job.RunOnce(ctx)

		if st := botStatus(t, ctx, pool, insolvent); st != "retired" {
			t.Fatalf("Carbonera 01 status=%s, esperado retired", st)
		}
		if profileActive(t, ctx, pool, insolvent) {
			t.Fatal("el perfil de Carbonera 01 quedó activo tras el retiro")
		}
		if m := insolventMark(t, ctx, pool, insolvent); m != nil {
			t.Fatalf("la marca de insolvencia no se limpió tras el retiro: %v", *m)
		}
		if got := cashOf(t, ctx, pool, insolvent); got != 0 {
			t.Fatalf("caja de Carbonera 01 tras absorber: %d, esperado 0", got)
		}
		if d := emissionBalance(t, ctx, pool) - emissionBefore; d != 300 {
			t.Fatalf("la emisión subió %d, esperado 300 (absorción hacia 0)", d)
		}
		p := lastRetiredPayload(t, ctx, pool, insolvent)
		if p.AccountID != insolvent.String() || p.AbsorbedCash != "300" || p.RetiredAtSim != itSimBase+retireWindow+1 {
			t.Fatalf("payload bot.retired inesperado: %+v", p)
		}
		if got := counterValue(t, reg, "ii_bots_retired_total"); got != 1 {
			t.Fatalf("ii_bots_retired_total=%v, esperado 1", got)
		}
		if got := counterValue(t, reg, "ii_bots_absorbed_cash_total"); got != 300 {
			t.Fatalf("ii_bots_absorbed_cash_total=%v, esperado 300", got)
		}
		// Los demás no se retiran.
		if st := botStatus(t, ctx, pool, solvent); st != "active" {
			t.Fatalf("Minera 01 (solvente) status=%s, esperado active", st)
		}
		if st := botStatus(t, ctx, pool, withBuilding); st != "active" {
			t.Fatalf("Mercader 01 (con edificio) status=%s, esperado active", st)
		}
		assertNoNegativeCash(t, ctx, pool)
		assertBalancedLedger(t, ctx, pool)
	})

	t.Run("el retirado no vuelve a ser candidato", func(t *testing.T) {
		job.RunOnce(ctx)
		if st := botStatus(t, ctx, pool, insolvent); st != "retired" {
			t.Fatalf("Carbonera 01 status=%s tras re-barrer, esperado retired (estable)", st)
		}
		if got := counterValue(t, reg, "ii_bots_retired_total"); got != 1 {
			t.Fatalf("ii_bots_retired_total=%v tras re-barrer, esperado 1 (idempotente)", got)
		}
	})

	// ── Retiro instantáneo (ventana 0) de un bot con caja 0 ──────────────────
	t.Run("retiro instantaneo con caja 0", func(t *testing.T) {
		drainCashTo(t, ctx, pool, instant, 0)
		instReg := prometheus.NewRegistry()
		instJob, err := bots.NewRetirementJob(pool, ledger.DefaultOptions(), bots.RetirementOptions{
			Interval: time.Minute, CashFloor: retireFloor, IdleSimSeconds: 0,
		}, &retireSim{now: itSimBase}, logger, instReg)
		if err != nil {
			t.Fatalf("NewRetirementJob (instantáneo): %v", err)
		}
		emissionBefore := emissionBalance(t, ctx, pool)

		instJob.RunOnce(ctx)

		if st := botStatus(t, ctx, pool, instant); st != "retired" {
			t.Fatalf("Carbonera 02 status=%s, esperado retired en un solo barrido", st)
		}
		p := lastRetiredPayload(t, ctx, pool, instant)
		if p.AbsorbedCash != "0" {
			t.Fatalf("absorbed_cash=%q, esperado \"0\" (caja vacía)", p.AbsorbedCash)
		}
		if d := emissionBalance(t, ctx, pool) - emissionBefore; d != 0 {
			t.Fatalf("la emisión cambió %d con caja 0, esperado 0", d)
		}
		if got := counterValue(t, instReg, "ii_bots_retired_total"); got != 1 {
			t.Fatalf("ii_bots_retired_total (instantáneo)=%v, esperado 1", got)
		}
		// El solvente sigue en pie; el retiro instantáneo no lo toca.
		if st := botStatus(t, ctx, pool, solvent); st != "active" {
			t.Fatalf("Minera 01 status=%s, esperado active", st)
		}
		assertNoNegativeCash(t, ctx, pool)
		assertBalancedLedger(t, ctx, pool)
	})
}

// ─── Reloj mutable inyectado ──────────────────────────────────────────────────

type retireSim struct{ now int64 }

func (s *retireSim) Now(context.Context) simtime.SimTime { return simtime.SimTime(s.now) }
func (s *retireSim) set(n int64)                         { s.now = n }

// ─── Fixtures / mutaciones por SQL ───────────────────────────────────────────

// drainCashTo mueve el excedente de caja (por encima de target) a la cuenta sink
// mediante un asiento cash → sink, para forzar la insolvencia sin dejar la caja
// negativa. target debe ser <= saldo actual.
func drainCashTo(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner uuid.UUID, target int64) {
	t.Helper()
	bal := cashOf(t, ctx, pool, owner)
	delta := bal - target
	if delta <= 0 {
		return
	}
	var cashID, sinkID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM ledger.accounts WHERE kind='cash' AND owner_account_id=$1`, owner).Scan(&cashID); err != nil {
		t.Fatalf("cash id de %s: %v", owner, err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM ledger.accounts WHERE kind='sink' ORDER BY id LIMIT 1`).Scan(&sinkID); err != nil {
		t.Fatalf("sink id: %v", err)
	}
	postLedger(t, ctx, pool, "transfer", []retireEntry{{cashID, -delta}, {sinkID, delta}})
}

// reassignBuilding asigna un edificio sembrado (no embargado) a la cuenta dada,
// para que cuente como actividad viva pese a tener caja 0.
func reassignBuilding(t *testing.T, ctx context.Context, pool *pgxpool.Pool, owner uuid.UUID) {
	t.Helper()
	var building uuid.UUID
	if err := pool.QueryRow(ctx,
		`SELECT id FROM world.buildings WHERE status <> 'seized' ORDER BY id LIMIT 1`).Scan(&building); err != nil {
		t.Fatalf("edificio sembrado no embargado: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE world.buildings SET owner_account_id=$1 WHERE id=$2`, owner, building); err != nil {
		t.Fatalf("reasignando el edificio %s: %v", building, err)
	}
}

type retireEntry struct {
	account uuid.UUID
	amount  int64
}

// postLedger asienta cabecera + partidas en UNA sola transacción (el balance por
// activo es un constraint trigger DIFERIDO: se evalúa en el COMMIT).
func postLedger(t *testing.T, ctx context.Context, pool *pgxpool.Pool, kind string, entries []retireEntry) {
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

// ─── Lecturas de estado ──────────────────────────────────────────────────────

func botStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) string {
	t.Helper()
	var st string
	if err := pool.QueryRow(ctx, `SELECT status::text FROM auth.accounts WHERE id=$1`, id).Scan(&st); err != nil {
		t.Fatalf("status de %s: %v", id, err)
	}
	return st
}

func profileActive(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) bool {
	t.Helper()
	var active bool
	if err := pool.QueryRow(ctx, `SELECT active FROM auth.bot_profiles WHERE account_id=$1`, id).Scan(&active); err != nil {
		t.Fatalf("perfil de %s: %v", id, err)
	}
	return active
}

func insolventMark(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) *int64 {
	t.Helper()
	var mark *int64
	if err := pool.QueryRow(ctx,
		`SELECT (behavior->>'insolvent_since_sim')::bigint FROM auth.bot_profiles WHERE account_id=$1`, id).Scan(&mark); err != nil {
		t.Fatalf("marca de insolvencia de %s: %v", id, err)
	}
	return mark
}

type retiredPayload struct {
	AccountID    string `json:"account_id"`
	AbsorbedCash string `json:"absorbed_cash"`
	RetiredAtSim int64  `json:"retired_at_sim"`
}

func lastRetiredPayload(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) retiredPayload {
	t.Helper()
	var raw []byte
	if err := pool.QueryRow(ctx, `
		SELECT payload FROM outbox.events
		 WHERE event_type = 'bot.retired' AND aggregate_id = $1
		 ORDER BY seq DESC LIMIT 1`, id).Scan(&raw); err != nil {
		t.Fatalf("leyendo bot.retired de %s: %v", id, err)
	}
	var p retiredPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("payload bot.retired inválido: %v (%s)", err, raw)
	}
	return p
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
