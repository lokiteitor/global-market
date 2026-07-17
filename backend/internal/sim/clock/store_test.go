package clock

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// simClockDDL replica exactamente la definición de world.sim_clock de la
// migración 0003 (y el dominio sim_time de la 0001), sin el resto del esquema
// world (que requiere PostGIS y no interviene en el reloj).
const simClockDDL = `
CREATE DOMAIN sim_time AS BIGINT CHECK (VALUE >= 0);
CREATE SCHEMA world;
CREATE TABLE world.sim_clock (
    id           SMALLINT PRIMARY KEY CHECK (id = 1),
    sim_time_at  sim_time NOT NULL DEFAULT 0,
    wall_anchor  TIMESTAMPTZ NOT NULL DEFAULT now(),
    ratio        INT NOT NULL DEFAULT 24 CHECK (ratio > 0),
    frozen       BOOLEAN NOT NULL DEFAULT false,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
`

// TestStoreIntegration ejercita el Store y el Reader contra una BD real.
// Se omite si II_TEST_DATABASE_URL no está definida. La URL solo identifica
// el servidor: el test crea una base de datos EFÍMERA propia (el rol debe
// tener CREATEDB), trabaja exclusivamente sobre ella y la destruye al
// terminar (mismo patrón que internal/platform/migrate).
func TestStoreIntegration(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test de integración omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("conectando a %s: %v", adminURL, err)
	}
	dbName := fmt.Sprintf("clocktest_%d_%d", os.Getpid(), time.Now().UnixNano())
	quoted := pgx.Identifier{dbName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
		t.Fatalf("creando la BD efímera %s: %v", dbName, err)
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
		t.Fatalf("creando el pool sobre %s: %v", dbName, err)
	}
	t.Cleanup(pool.Close)

	if _, err := pool.Exec(ctx, simClockDDL); err != nil {
		t.Fatalf("creando world.sim_clock: %v", err)
	}

	st := NewStore(pool)

	// Sin fila, Load falla con un error descriptivo.
	if _, err := st.Load(ctx); err == nil {
		t.Fatal("Load sin fila no devolvió error")
	}

	// EnsureExists crea el génesis.
	if err := st.EnsureExists(ctx); err != nil {
		t.Fatalf("EnsureExists: %v", err)
	}
	a1, err := st.Load(ctx)
	if err != nil {
		t.Fatalf("Load tras EnsureExists: %v", err)
	}
	if a1.SimTimeAt != 0 || a1.Ratio != simtime.Ratio || a1.Frozen {
		t.Fatalf("génesis inesperado: %+v", a1)
	}

	// EnsureExists es idempotente: la fila queda intacta.
	if err := st.EnsureExists(ctx); err != nil {
		t.Fatalf("EnsureExists (2ª vez): %v", err)
	}
	a2, err := st.Load(ctx)
	if err != nil {
		t.Fatalf("Load tras el segundo EnsureExists: %v", err)
	}
	if !a2.WallAnchor.Equal(a1.WallAnchor) || !a2.UpdatedAt.Equal(a1.UpdatedAt) || a2.SimTimeAt != a1.SimTimeAt {
		t.Fatalf("EnsureExists no fue idempotente: antes %+v, después %+v", a1, a2)
	}

	// PersistAnchor avanza sim_time_at y re-ancla el wall-clock.
	if err := st.PersistAnchor(ctx, 12345); err != nil {
		t.Fatalf("PersistAnchor: %v", err)
	}
	a3, err := st.Load(ctx)
	if err != nil {
		t.Fatalf("Load tras PersistAnchor: %v", err)
	}
	if a3.SimTimeAt != 12345 {
		t.Fatalf("sim_time_at = %d, esperado 12345", a3.SimTimeAt)
	}
	if a3.WallAnchor.Before(a1.WallAnchor) || a3.UpdatedAt.Before(a1.UpdatedAt) {
		t.Fatalf("PersistAnchor no re-ancló: antes %+v, después %+v", a1, a3)
	}

	// Con frozen=true PersistAnchor no toca la fila (y no es un error).
	if _, err := pool.Exec(ctx, `UPDATE world.sim_clock SET frozen = true WHERE id = 1`); err != nil {
		t.Fatalf("congelando el reloj: %v", err)
	}
	if err := st.PersistAnchor(ctx, 99999); err != nil {
		t.Fatalf("PersistAnchor congelado: %v", err)
	}
	a4, err := st.Load(ctx)
	if err != nil {
		t.Fatalf("Load congelado: %v", err)
	}
	if a4.SimTimeAt != 12345 || !a4.Frozen {
		t.Fatalf("PersistAnchor no respetó frozen: %+v", a4)
	}

	// Descongelado, el Reader deriva un sim-time creciente sin tocar la BD
	// entre recargas (caché TTL).
	if _, err := pool.Exec(ctx, `UPDATE world.sim_clock SET frozen = false WHERE id = 1`); err != nil {
		t.Fatalf("descongelando el reloj: %v", err)
	}
	r := NewReader(st, ReaderOptions{CacheTTL: time.Second}, nil)
	n1 := r.Now(ctx)
	if n1 < 12345 {
		t.Fatalf("Reader.Now() = %d, esperado >= 12345", n1)
	}
	time.Sleep(100 * time.Millisecond) // 100ms reales = 2.4s de sim-time (ratio 24)
	n2 := r.Now(ctx)
	if n2 <= n1 {
		t.Fatalf("Reader.Now() no crece: %d -> %d", n1, n2)
	}

	// El Clock del motor arranca sobre la misma fila y avanza.
	clk := New(st, Options{PersistInterval: 50 * time.Millisecond, RefreshInterval: 25 * time.Millisecond}, nil, nil)
	clkCtx, clkCancel := context.WithCancel(ctx)
	if err := clk.Start(clkCtx); err != nil {
		t.Fatalf("Clock.Start: %v", err)
	}
	c1 := clk.Now()
	time.Sleep(120 * time.Millisecond) // cubre al menos un ciclo de persistencia
	c2 := clk.Now()
	if c2 <= c1 {
		t.Fatalf("Clock.Now() no crece: %d -> %d", c1, c2)
	}
	clkCancel()
	clk.Stop()
	final, err := st.Load(ctx)
	if err != nil {
		t.Fatalf("Load tras parar el Clock: %v", err)
	}
	if final.SimTimeAt <= 12345 {
		t.Fatalf("el Clock no persistió el avance: sim_time_at = %d", final.SimTimeAt)
	}
}
