package migrate

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Guardas que no requieren BD: se comprueban antes de tocar la conexión.

func TestDownRejectsNonPositiveN(t *testing.T) {
	r := New(nil, t.TempDir(), "dev", nil)
	for _, n := range []int{0, -1} {
		if _, err := r.Down(context.Background(), n); err == nil {
			t.Errorf("Down(%d) sin error", n)
		}
	}
}

func TestResetRefusesProd(t *testing.T) {
	r := New(nil, t.TempDir(), "prod", nil)
	err := r.Reset(context.Background())
	if err == nil || !strings.Contains(err.Error(), "prod") {
		t.Fatalf("Reset en prod debería rehusar, obtenido: %v", err)
	}
}

// TestRunnerIntegration ejercita el ciclo completo contra una BD real:
// up -> status limpio -> drift -> down parcial y total -> re-up -> reset.
// Se omite si II_TEST_DATABASE_URL no está definida. La URL solo identifica
// el servidor: el test crea una base de datos EFÍMERA propia (el rol debe
// tener CREATEDB), trabaja exclusivamente sobre ella y la destruye al
// terminar, sin tocar nunca los datos ni schema_migrations de la BD apuntada.
func TestRunnerIntegration(t *testing.T) {
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
	dbName := fmt.Sprintf("migratetest_%d_%d", os.Getpid(), time.Now().UnixNano())
	quoted := pgx.Identifier{dbName}.Sanitize()
	mustExec(t, ctx, admin, "CREATE DATABASE "+quoted)
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		defer admin.Close(dropCtx)
		if _, err := admin.Exec(dropCtx, "DROP DATABASE "+quoted+" WITH (FORCE)"); err != nil {
			t.Errorf("eliminando la BD efímera %s: %v", dbName, err)
		}
	})

	cfg, err := pgx.ParseConfig(adminURL)
	if err != nil {
		t.Fatalf("interpretando II_TEST_DATABASE_URL: %v", err)
	}
	cfg.Database = dbName
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("conectando a la BD efímera %s: %v", dbName, err)
	}
	defer conn.Close(context.Background())

	dir := t.TempDir()
	writeFixture(t, dir, "0001_esquema.up.sql", `
CREATE SCHEMA migratetest;
CREATE TABLE migratetest.items (
    id   BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name TEXT NOT NULL
);
`)
	writeFixture(t, dir, "0001_esquema.down.sql", `
DROP TABLE migratetest.items;
DROP SCHEMA migratetest;
`)
	writeFixture(t, dir, "0002_datos.up.sql", `
INSERT INTO migratetest.items (name) VALUES ('alfa'), ('beta');
`)
	writeFixture(t, dir, "0002_datos.down.sql", `
DELETE FROM migratetest.items;
`)
	// Migración sin transacción: dos sentencias CONCURRENTLY, que PostgreSQL
	// rechaza dentro de un bloque de transacción (incluida la implícita de un
	// mensaje multisentencia). Valida directiva + troceado de sentencias.
	writeFixture(t, dir, "0003_indices.up.sql", NoTxDirective+`
CREATE INDEX CONCURRENTLY items_name_idx ON migratetest.items (name);
CREATE INDEX CONCURRENTLY items_name_upper_idx ON migratetest.items (upper(name));
`)
	writeFixture(t, dir, "0003_indices.down.sql", NoTxDirective+`
DROP INDEX CONCURRENTLY migratetest.items_name_idx;
DROP INDEX CONCURRENTLY migratetest.items_name_upper_idx;
`)

	r := New(conn, dir, "dev", io.Discard)

	// Up completo sobre BD vacía.
	applied, err := r.Up(ctx)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if len(applied) != 3 {
		t.Fatalf("Up aplicó %d, esperado 3", len(applied))
	}
	if got := countRows(t, ctx, conn, "SELECT count(*) FROM public.schema_migrations"); got != 3 {
		t.Fatalf("schema_migrations tiene %d filas, esperado 3", got)
	}
	if got := countRows(t, ctx, conn, "SELECT count(*) FROM migratetest.items"); got != 2 {
		t.Fatalf("items tiene %d filas, esperado 2", got)
	}
	if got := countRows(t, ctx, conn, "SELECT count(*) FROM pg_indexes WHERE schemaname = 'migratetest' AND indexname LIKE 'items_name%'"); got != 2 {
		t.Fatalf("índices CONCURRENTLY creados: %d, esperado 2", got)
	}

	// Up idempotente.
	applied, err = r.Up(ctx)
	if err != nil {
		t.Fatalf("Up (2ª vez): %v", err)
	}
	if len(applied) != 0 {
		t.Fatalf("Up re-aplicó %d migraciones", len(applied))
	}

	// Status limpio.
	items, err := r.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	for _, it := range items {
		if !it.Applied {
			t.Errorf("%s pendiente tras Up", it.Migration.ID())
		}
	}

	// Drift: modificar un up aplicado hace fallar Status y Up con mensaje claro.
	upPath := filepath.Join(dir, "0002_datos.up.sql")
	original, err := os.ReadFile(upPath)
	if err != nil {
		t.Fatalf("leyendo fixture: %v", err)
	}
	writeFixture(t, dir, "0002_datos.up.sql", string(original)+"-- drift\n")
	if _, err := r.Status(ctx); err == nil || !strings.Contains(err.Error(), "drift") {
		t.Fatalf("Status no detectó el drift: %v", err)
	}
	if _, err := r.Up(ctx); err == nil || !strings.Contains(err.Error(), "drift") {
		t.Fatalf("Up no detectó el drift: %v", err)
	}
	if err := os.WriteFile(upPath, original, 0o644); err != nil {
		t.Fatalf("restaurando fixture: %v", err)
	}
	if _, err := r.Status(ctx); err != nil {
		t.Fatalf("Status tras restaurar: %v", err)
	}

	// Down parcial: revierte la última (índices no-tx).
	reverted, err := r.Down(ctx, 1)
	if err != nil {
		t.Fatalf("Down(1): %v", err)
	}
	if len(reverted) != 1 || reverted[0].Version != 3 {
		t.Fatalf("Down(1) revirtió %v", reverted)
	}
	if got := countRows(t, ctx, conn, "SELECT count(*) FROM pg_indexes WHERE schemaname = 'migratetest' AND indexname LIKE 'items_name%'"); got != 0 {
		t.Fatalf("índices restantes tras Down: %d", got)
	}

	// Down de más migraciones de las aplicadas es un error.
	if _, err := r.Down(ctx, 5); err == nil {
		t.Fatal("Down(5) con solo 2 aplicadas no devolvió error")
	}

	// Down total y re-up.
	if _, err := r.Up(ctx); err != nil {
		t.Fatalf("re-Up: %v", err)
	}
	reverted, err = r.Down(ctx, 3)
	if err != nil {
		t.Fatalf("Down(3): %v", err)
	}
	if len(reverted) != 3 {
		t.Fatalf("Down(3) revirtió %d", len(reverted))
	}
	if got := countRows(t, ctx, conn, "SELECT count(*) FROM information_schema.schemata WHERE schema_name = 'migratetest'"); got != 0 {
		t.Fatal("el esquema migratetest sobrevivió al down total")
	}
	if got := countRows(t, ctx, conn, "SELECT count(*) FROM public.schema_migrations"); got != 0 {
		t.Fatalf("schema_migrations tiene %d filas tras el down total", got)
	}
	if _, err := r.Up(ctx); err != nil {
		t.Fatalf("re-Up tras down total: %v", err)
	}

	// Reset en dev: clean-slate (barrido de esquemas/objetos) + up de todo.
	if err := r.Reset(ctx); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if got := countRows(t, ctx, conn, "SELECT count(*) FROM public.schema_migrations"); got != 3 {
		t.Fatalf("schema_migrations tras Reset: %d filas", got)
	}
	if got := countRows(t, ctx, conn, "SELECT count(*) FROM migratetest.items"); got != 2 {
		t.Fatalf("items tras Reset: %d filas", got)
	}

	// Reset rehusado en prod aunque haya conexión válida.
	if err := New(conn, dir, "prod", io.Discard).Reset(ctx); err == nil {
		t.Fatal("Reset en prod no devolvió error")
	}

	// Transaccionalidad: una migración que falla a mitad no deja rastro.
	writeFixture(t, dir, "0004_rota.up.sql", `
CREATE TABLE migratetest.tx_probe (id INT);
SELECT funcion_que_no_existe();
`)
	writeFixture(t, dir, "0004_rota.down.sql", `
DROP TABLE migratetest.tx_probe;
`)
	if _, err := r.Up(ctx); err == nil {
		t.Fatal("Up con migración rota no devolvió error")
	}
	if got := countRows(t, ctx, conn, "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'migratetest' AND table_name = 'tx_probe'"); got != 0 {
		t.Fatal("tx_probe existe: la migración rota no se revirtió")
	}
	if got := countRows(t, ctx, conn, "SELECT count(*) FROM public.schema_migrations"); got != 3 {
		t.Fatalf("schema_migrations tras migración rota: %d filas, esperado 3", got)
	}
	for _, fn := range []string{"0004_rota.up.sql", "0004_rota.down.sql"} {
		if err := os.Remove(filepath.Join(dir, fn)); err != nil {
			t.Fatalf("borrando %s: %v", fn, err)
		}
	}
	if _, err := r.Status(ctx); err != nil {
		t.Fatalf("Status tras retirar la migración rota: %v", err)
	}

	// Exclusión mutua: con el advisory lock tomado por otra sesión, Up falla.
	conn2, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("segunda conexión: %v", err)
	}
	defer conn2.Close(context.Background())
	mustExecOn(t, ctx, conn2, "SELECT pg_advisory_lock($1)", lockKey)
	if _, err := r.Up(ctx); err == nil || !strings.Contains(err.Error(), "advisory lock") {
		t.Fatalf("Up con el lock ocupado: %v", err)
	}
	mustExecOn(t, ctx, conn2, "SELECT pg_advisory_unlock($1)", lockKey)
	if _, err := r.Up(ctx); err != nil {
		t.Fatalf("Up tras liberar el lock: %v", err)
	}
}

// writeFixture escribe un fichero de migración de prueba.
func writeFixture(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("escribiendo fixture %s: %v", name, err)
	}
}

func mustExec(t *testing.T, ctx context.Context, conn *pgx.Conn, sql string) {
	t.Helper()
	if _, err := conn.Exec(ctx, sql); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func mustExecOn(t *testing.T, ctx context.Context, conn *pgx.Conn, sql string, args ...any) {
	t.Helper()
	if _, err := conn.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func countRows(t *testing.T, ctx context.Context, conn *pgx.Conn, sql string) int {
	t.Helper()
	var n int
	if err := conn.QueryRow(ctx, sql).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return n
}
