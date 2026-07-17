package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// ─── Unitarios (sin BD) ─────────────────────────────────────────────────────

func TestRetryableTxError(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"serialization_failure", &pgconn.PgError{Code: sqlstateSerializationFailure}, true},
		{"deadlock_detected", &pgconn.PgError{Code: sqlstateDeadlockDetected}, true},
		{"envuelto", fmt.Errorf("capa: %w", &pgconn.PgError{Code: sqlstateSerializationFailure}), true},
		{"otro SQLSTATE", &pgconn.PgError{Code: "23505"}, false},
		{"error plano", errors.New("cualquiera"), false},
		{"nil", nil, false},
	} {
		if got := retryableTxError(tc.err); got != tc.want {
			t.Errorf("retryableTxError(%s) = %v, esperado %v", tc.name, got, tc.want)
		}
	}
}

func TestBackoffDelayBounds(t *testing.T) {
	for attempt := range 10 {
		for range 50 {
			d := backoffDelay(attempt)
			window := min(txBackoffBase<<attempt, txBackoffCap)
			if d < window/2 || d > window {
				t.Fatalf("backoffDelay(%d) = %v fuera de [%v, %v]", attempt, d, window/2, window)
			}
		}
	}
}

func TestWaitBackoffHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitBackoff(ctx, 5); !errors.Is(err, context.Canceled) {
		t.Fatalf("waitBackoff con contexto cancelado: %v, esperado context.Canceled", err)
	}
}

// ─── Integración ────────────────────────────────────────────────────────────

// TestRunSerializableIntegration ejercita RunSerializable contra una BD real:
// commit, rollback todo-o-nada, error no reintentable y — lo esencial — un
// conflicto de serialización REAL (write skew clásico bajo SSI) que obliga a
// reintentar y se refleja en ii_tx_serialization_retries_total.
//
// Se omite si II_TEST_DATABASE_URL no está definida. La URL solo identifica
// el servidor: el test crea una base de datos EFÍMERA propia (el rol debe
// tener CREATEDB) y la destruye al terminar (mismo patrón que los demás
// tests de integración del repo).
func TestRunSerializableIntegration(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test de integración omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool := newEphemeralPool(t, ctx, adminURL)
	mustExec(t, ctx, pool, `CREATE TABLE saldos (id int PRIMARY KEY, saldo bigint NOT NULL)`)
	mustExec(t, ctx, pool, `INSERT INTO saldos VALUES (1, 100), (2, 100)`)

	reg := prometheus.NewRegistry()
	RegisterTxMetrics(reg)
	RegisterTxMetrics(reg) // idempotente: no entra en pánico
	retriesBefore := counterValue(t, reg, "ii_tx_serialization_retries_total")

	// ── Commit: los efectos de fn quedan asentados ──────────────────────────
	if err := RunSerializable(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO saldos VALUES (3, 7)`)
		return err
	}); err != nil {
		t.Fatalf("RunSerializable (commit): %v", err)
	}
	if got := queryInt(t, ctx, pool, `SELECT saldo FROM saldos WHERE id = 3`); got != 7 {
		t.Fatalf("fila confirmada con saldo %d, esperado 7", got)
	}

	// ── Rollback todo-o-nada: el error de fn se devuelve tal cual y no se
	//    reintenta ni queda rastro ───────────────────────────────────────────
	errNegocio := errors.New("regla de negocio violada")
	calls := 0
	err := RunSerializable(ctx, pool, func(tx pgx.Tx) error {
		calls++
		if _, err := tx.Exec(ctx, `INSERT INTO saldos VALUES (4, 1)`); err != nil {
			return err
		}
		return errNegocio
	})
	if !errors.Is(err, errNegocio) {
		t.Fatalf("RunSerializable (rollback): %v, esperado errNegocio", err)
	}
	if calls != 1 {
		t.Fatalf("un error no reintentable ejecutó fn %d veces, esperado 1", calls)
	}
	if got := queryInt(t, ctx, pool, `SELECT count(*) FROM saldos WHERE id = 4`); got != 0 {
		t.Fatal("la transacción revertida dejó rastro")
	}

	// ── Error de PostgreSQL no reintentable: se devuelve sin reintentos ─────
	calls = 0
	err = RunSerializable(ctx, pool, func(tx pgx.Tx) error {
		calls++
		_, err := tx.Exec(ctx, `INSERT INTO saldos VALUES (1, 0)`) // PK duplicada
		return err
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		t.Fatalf("PK duplicada: %v, esperado SQLSTATE 23505", err)
	}
	if calls != 1 {
		t.Fatalf("unique_violation ejecutó fn %d veces, esperado 1", calls)
	}

	// ── Conflicto de serialización real: write skew clásico ────────────────
	// Dos transacciones SERIALIZABLE concurrentes leen la tabla completa y
	// cada una actualiza una fila distinta con el total leído. El resultado
	// concurrente (200,200) no es serializable (en serie sería {200,300}),
	// así que SSI DEBE abortar una con 40001 y RunSerializable reintentarla.
	mustExec(t, ctx, pool, `DELETE FROM saldos WHERE id = 3`)

	var (
		barrier  sync.WaitGroup
		attempts [2]atomic.Int32
	)
	barrier.Add(2)
	run := func(i, writeID int) error {
		synced := false // la barrera solo aplica al primer intento
		return RunSerializable(ctx, pool, func(tx pgx.Tx) error {
			attempts[i].Add(1)
			var total int64
			if err := tx.QueryRow(ctx, `SELECT sum(saldo) FROM saldos`).Scan(&total); err != nil {
				return err
			}
			if !synced {
				synced = true
				barrier.Done()
				barrier.Wait() // ambas han leído antes de que ninguna escriba
			}
			_, err := tx.Exec(ctx, `UPDATE saldos SET saldo = $1 WHERE id = $2`, total, writeID)
			return err
		})
	}
	errCh := make(chan error, 2)
	go func() { errCh <- run(0, 1) }()
	go func() { errCh <- run(1, 2) }()
	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatalf("RunSerializable (conflicto): %v", err)
		}
	}

	// Resultado serializable en cualquier orden de commit: {200, 300}.
	rows, err := pool.Query(ctx, `SELECT saldo FROM saldos ORDER BY saldo`)
	if err != nil {
		t.Fatalf("leyendo saldos finales: %v", err)
	}
	finales, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		t.Fatalf("recogiendo saldos finales: %v", err)
	}
	if !slices.Equal(finales, []int64{200, 300}) {
		t.Fatalf("saldos finales %v, esperado [200 300] (resultado serializable)", finales)
	}

	totalAttempts := attempts[0].Load() + attempts[1].Load()
	if totalAttempts < 3 {
		t.Fatalf("intentos totales %d, esperado >= 3 (al menos una reejecución)", totalAttempts)
	}
	delta := counterValue(t, reg, "ii_tx_serialization_retries_total") - retriesBefore
	if delta < 1 {
		t.Fatalf("ii_tx_serialization_retries_total creció %v, esperado >= 1", delta)
	}
	if float64(totalAttempts-2) != delta {
		t.Fatalf("reintentos en la métrica (%v) != reejecuciones observadas (%d)", delta, totalAttempts-2)
	}
}

// ─── Infraestructura del test ───────────────────────────────────────────────

// newEphemeralPool crea una BD efímera vacía (este paquete no necesita el
// esquema del juego) y devuelve un pool sobre ella; todo se destruye al
// terminar el test.
func newEphemeralPool(t *testing.T, ctx context.Context, adminURL string) *pgxpool.Pool {
	t.Helper()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("conectando a %s: %v", adminURL, err)
	}
	dbName := fmt.Sprintf("dbtest_%d_%d", os.Getpid(), time.Now().UnixNano())
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

	poolCfg, err := pgxpool.ParseConfig(adminURL)
	if err != nil {
		t.Fatalf("interpretando II_TEST_DATABASE_URL: %v", err)
	}
	poolCfg.ConnConfig.Database = dbName
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		t.Fatalf("creando el pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func mustExec(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string) {
	t.Helper()
	if _, err := pool.Exec(ctx, sql); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func queryInt(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(ctx, sql).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return n
}

// counterValue lee el valor actual de un counter sin etiquetas del registry.
func counterValue(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("recogiendo métricas: %v", err)
	}
	for _, mf := range families {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			return m.GetCounter().GetValue()
		}
	}
	t.Fatalf("métrica %q no registrada", name)
	return 0
}
