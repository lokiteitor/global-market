package migrate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
)

// controlTableSQL crea la tabla de control on-demand (idempotente).
const controlTableSQL = `
CREATE TABLE IF NOT EXISTS public.schema_migrations (
    version    INT         PRIMARY KEY,
    name       TEXT        NOT NULL,
    checksum   TEXT        NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`

const (
	insertRecordSQL = `INSERT INTO public.schema_migrations (version, name, checksum) VALUES ($1, $2, $3)`
	deleteRecordSQL = `DELETE FROM public.schema_migrations WHERE version = $1`
	selectRecordSQL = `SELECT version, name, checksum, applied_at FROM public.schema_migrations ORDER BY version`
)

// cleanSlateSQL descarta el estado que dejan las migraciones sin ejercitar sus
// .down.sql (Reset lo usa; ver su doc). Es genérico: descubre los objetos por
// catálogo en vez de nombrarlos, de modo que sigue a las migraciones sin
// acoplarse a ellas y sirve igual a los fixtures de test.
//
//  1. Esquemas de usuario (dominios del proyecto + fixtures): fuera con
//     CASCADE. Se preservan los del sistema (pg_catalog, information_schema,
//     public, pg_toast/pg_temp) y los creados por una extensión.
//  2. Objetos que las migraciones dejan en public (tablas como
//     schema_migrations e idempotency_keys; dominios de tipo como sim_time),
//     preservando SIEMPRE lo que pertenece a una extensión (postgis:
//     spatial_ref_sys, etc.). Las tablas se borran antes que los dominios para
//     no dejar columnas dependientes. schema_migrations se recrea en el Up.
const cleanSlateSQL = `
DO $clean_slate$
DECLARE
    obj text;
BEGIN
    FOR obj IN
        SELECT n.nspname
        FROM pg_namespace n
        WHERE n.nspname NOT IN ('pg_catalog', 'information_schema', 'public')
          AND n.nspname NOT LIKE 'pg\_toast%'
          AND n.nspname NOT LIKE 'pg\_temp%'
          AND NOT EXISTS (
              SELECT 1 FROM pg_depend d
              WHERE d.classid = 'pg_namespace'::regclass
                AND d.objid = n.oid AND d.deptype = 'e')
    LOOP
        EXECUTE format('DROP SCHEMA %I CASCADE', obj);
    END LOOP;

    FOR obj IN
        SELECT c.relname
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p')
          AND NOT EXISTS (
              SELECT 1 FROM pg_depend d
              WHERE d.classid = 'pg_class'::regclass
                AND d.objid = c.oid AND d.deptype = 'e')
    LOOP
        EXECUTE format('DROP TABLE IF EXISTS public.%I CASCADE', obj);
    END LOOP;

    FOR obj IN
        SELECT t.typname
        FROM pg_type t
        JOIN pg_namespace n ON n.oid = t.typnamespace
        WHERE n.nspname = 'public' AND t.typtype = 'd'
          AND NOT EXISTS (
              SELECT 1 FROM pg_depend d
              WHERE d.classid = 'pg_type'::regclass
                AND d.objid = t.oid AND d.deptype = 'e')
    LOOP
        EXECUTE format('DROP DOMAIN IF EXISTS public.%I CASCADE', obj);
    END LOOP;
END
$clean_slate$;`

// lockKey identifica el advisory lock de sesión que serializa los runners
// concurrentes sobre la misma BD (ASCII "IMPERMIG").
const lockKey int64 = 0x494D5045524D4947

// Record es una fila de public.schema_migrations.
type Record struct {
	Version   int
	Name      string
	Checksum  string
	AppliedAt time.Time
}

// Status describe el estado de una migración descubierta respecto a la BD.
type Status struct {
	Migration Migration
	Applied   bool
	// AppliedAt solo es significativo cuando Applied es true.
	AppliedAt time.Time
}

// Runner ejecuta las operaciones de migración sobre una conexión dedicada.
// No es seguro para uso concurrente: es una herramienta de línea de comandos.
type Runner struct {
	conn *pgx.Conn
	dir  string
	env  string
	out  io.Writer
}

// New construye un Runner. env es el entorno de ejecución (II_ENV); Reset
// rehúsa ejecutarse con env == "prod". out recibe el progreso legible por
// consola (nil lo silencia).
func New(conn *pgx.Conn, dir, env string, out io.Writer) *Runner {
	if out == nil {
		out = io.Discard
	}
	return &Runner{conn: conn, dir: dir, env: env, out: out}
}

// Up aplica en orden todas las migraciones pendientes, cada una en su propia
// transacción (salvo directiva no-transaction), registrándolas en la tabla de
// control. Antes de aplicar nada verifica que el historial aplicado coincide
// con los ficheros (checksums incluidos). Devuelve las migraciones aplicadas.
func (r *Runner) Up(ctx context.Context) ([]Migration, error) {
	migs, err := Discover(r.dir)
	if err != nil {
		return nil, err
	}
	var applied []Migration
	err = r.locked(ctx, func(ctx context.Context) error {
		recs, err := r.records(ctx)
		if err != nil {
			return err
		}
		if err := verifyAppliedState(migs, recs, true); err != nil {
			return err
		}
		for _, m := range migs[len(recs):] {
			fmt.Fprintf(r.out, "aplicando %s\n", m.ID())
			if err := r.applyUp(ctx, m); err != nil {
				return fmt.Errorf("aplicando %s: %w", m.ID(), err)
			}
			applied = append(applied, m)
		}
		return nil
	})
	return applied, err
}

// Down revierte las n últimas migraciones aplicadas ejecutando su .down.sql
// y borrando su registro. No verifica checksums: es la vía de recuperación
// cuando un fichero up cambió en desarrollo (el drift bloquea Up y Status,
// nunca la reversión). Devuelve las migraciones revertidas.
func (r *Runner) Down(ctx context.Context, n int) ([]Migration, error) {
	if n < 1 {
		return nil, fmt.Errorf("el número de migraciones a revertir debe ser >= 1 (recibido %d)", n)
	}
	migs, err := Discover(r.dir)
	if err != nil {
		return nil, err
	}
	var reverted []Migration
	err = r.locked(ctx, func(ctx context.Context) error {
		recs, err := r.records(ctx)
		if err != nil {
			return err
		}
		if err := verifyAppliedState(migs, recs, false); err != nil {
			return err
		}
		if n > len(recs) {
			return fmt.Errorf("no se pueden revertir %d migraciones: solo hay %d aplicadas", n, len(recs))
		}
		for i := len(recs) - 1; i >= len(recs)-n; i-- {
			m := migs[recs[i].Version-1]
			fmt.Fprintf(r.out, "revirtiendo %s\n", m.ID())
			if err := r.applyDown(ctx, m); err != nil {
				return fmt.Errorf("revirtiendo %s: %w", m.ID(), err)
			}
			reverted = append(reverted, m)
		}
		return nil
	})
	return reverted, err
}

// Status devuelve el estado (aplicada/pendiente) de cada migración
// descubierta y verifica la integridad del historial: checksum de cada
// migración aplicada, nombres y consecutividad. Si detecta drift devuelve
// la lista construida junto con un error descriptivo.
func (r *Runner) Status(ctx context.Context) ([]Status, error) {
	migs, err := Discover(r.dir)
	if err != nil {
		return nil, err
	}
	if err := r.ensureControlTable(ctx); err != nil {
		return nil, err
	}
	recs, err := r.records(ctx)
	if err != nil {
		return nil, err
	}
	byVersion := make(map[int]Record, len(recs))
	for _, rec := range recs {
		byVersion[rec.Version] = rec
	}
	items := make([]Status, len(migs))
	for i, m := range migs {
		st := Status{Migration: m}
		if rec, ok := byVersion[m.Version]; ok {
			st.Applied = true
			st.AppliedAt = rec.AppliedAt
		}
		items[i] = st
	}
	return items, verifyAppliedState(migs, recs, true)
}

// Reset lleva la BD a un estado limpio y reaplica todas las migraciones.
// Rehúsa ejecutarse en producción.
//
// El clean-slate NO ejercita los .down.sql: los descarta con un barrido
// directo (DROP SCHEMA ... CASCADE de los esquemas de usuario + borrado de
// los objetos que las migraciones dejan en public). Ejercitar los downs sobre
// una BD sembrada es imposible por diseño: llevan guardas de datos legítimas
// para reversiones reales (p. ej. 0008 rehúsa revertir mientras existan
// cuentas world_source, que el seed crea desde el Incremento 2). reset-db es
// una operación de "nuke and repave" de desarrollo, no una reversión, así que
// no debe depender de esas guardas. Coincide con el contrato documentado del
// Makefile: "Destruye los esquemas de dominio y reaplica todo (solo dev)".
func (r *Runner) Reset(ctx context.Context) error {
	if r.env == "prod" {
		return errors.New("reset rehusado: II_ENV=prod (operación destructiva, solo entornos no productivos)")
	}
	if _, err := r.conn.Exec(ctx, cleanSlateSQL); err != nil {
		return fmt.Errorf("reset (clean slate): %w", err)
	}
	if _, err := r.Up(ctx); err != nil {
		return fmt.Errorf("reset (up): %w", err)
	}
	return nil
}

// applyUp ejecuta el up de una migración y la registra. Con directiva
// no-transaction las sentencias van una a una en autocommit; en otro caso
// SQL y registro comparten transacción (todo o nada).
func (r *Runner) applyUp(ctx context.Context, m Migration) error {
	if m.NoTxUp {
		for _, stmt := range splitStatements(m.UpSQL) {
			if _, err := r.conn.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("fuera de transacción, estado posiblemente parcial (revisa la BD): %w", err)
			}
		}
		if _, err := r.conn.Exec(ctx, insertRecordSQL, m.Version, m.Name, m.Checksum); err != nil {
			return fmt.Errorf("la migración se ejecutó pero no pudo registrarse en schema_migrations: %w", err)
		}
		return nil
	}
	tx, err := r.conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("abriendo la transacción: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // rollback tras commit devuelve ErrTxClosed
	if _, err := tx.Exec(ctx, m.UpSQL); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, insertRecordSQL, m.Version, m.Name, m.Checksum); err != nil {
		return fmt.Errorf("registrando en schema_migrations: %w", err)
	}
	return tx.Commit(ctx)
}

// applyDown ejecuta el down de una migración y borra su registro, con la
// misma semántica transaccional que applyUp.
func (r *Runner) applyDown(ctx context.Context, m Migration) error {
	if m.NoTxDown {
		for _, stmt := range splitStatements(m.DownSQL) {
			if _, err := r.conn.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("fuera de transacción, estado posiblemente parcial (revisa la BD): %w", err)
			}
		}
		if _, err := r.conn.Exec(ctx, deleteRecordSQL, m.Version); err != nil {
			return fmt.Errorf("la reversión se ejecutó pero no pudo borrarse su registro: %w", err)
		}
		return nil
	}
	tx, err := r.conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("abriendo la transacción: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // rollback tras commit devuelve ErrTxClosed
	if _, err := tx.Exec(ctx, m.DownSQL); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, deleteRecordSQL, m.Version); err != nil {
		return fmt.Errorf("borrando el registro de schema_migrations: %w", err)
	}
	return tx.Commit(ctx)
}

// locked asegura la tabla de control, toma el advisory lock de sesión que
// serializa runners concurrentes y ejecuta fn.
func (r *Runner) locked(ctx context.Context, fn func(context.Context) error) error {
	if err := r.ensureControlTable(ctx); err != nil {
		return err
	}
	var ok bool
	if err := r.conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", lockKey).Scan(&ok); err != nil {
		return fmt.Errorf("adquiriendo el advisory lock: %w", err)
	}
	if !ok {
		return errors.New("otro runner de migraciones está en ejecución sobre esta BD (advisory lock ocupado)")
	}
	defer func() {
		_, _ = r.conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", lockKey)
	}()
	return fn(ctx)
}

// ensureControlTable crea public.schema_migrations si no existe.
func (r *Runner) ensureControlTable(ctx context.Context) error {
	if _, err := r.conn.Exec(ctx, controlTableSQL); err != nil {
		return fmt.Errorf("creando public.schema_migrations: %w", err)
	}
	return nil
}

// records carga las filas de la tabla de control ordenadas por versión.
func (r *Runner) records(ctx context.Context) ([]Record, error) {
	rows, err := r.conn.Query(ctx, selectRecordSQL)
	if err != nil {
		return nil, fmt.Errorf("leyendo schema_migrations: %w", err)
	}
	defer rows.Close()
	var recs []Record
	for rows.Next() {
		var rec Record
		if err := rows.Scan(&rec.Version, &rec.Name, &rec.Checksum, &rec.AppliedAt); err != nil {
			return nil, fmt.Errorf("leyendo schema_migrations: %w", err)
		}
		recs = append(recs, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("leyendo schema_migrations: %w", err)
	}
	return recs, nil
}

// verifyAppliedState comprueba que el historial aplicado es un prefijo
// consecutivo de las migraciones descubiertas y que nombre (y, si se pide,
// checksum) coinciden fichero a fichero. El drift es un error con mensaje
// claro: la reproducibilidad exige que un fichero aplicado no cambie.
func verifyAppliedState(migs []Migration, recs []Record, checkChecksums bool) error {
	if len(recs) > len(migs) {
		return fmt.Errorf("la BD registra %d migraciones aplicadas y el directorio solo contiene %d: faltan ficheros de migración", len(recs), len(migs))
	}
	for i, rec := range recs {
		if rec.Version != i+1 {
			return fmt.Errorf("estado incoherente en schema_migrations: versiones aplicadas no consecutivas (esperada %04d, registrada %04d)", i+1, rec.Version)
		}
		m := migs[i]
		if rec.Name != m.Name {
			return fmt.Errorf("drift en la versión %04d: la BD registra el nombre %q y el fichero se llama %q", rec.Version, rec.Name, m.Name)
		}
		if checkChecksums && rec.Checksum != m.Checksum {
			return fmt.Errorf("drift en %s: el fichero up cambió después de aplicarse (checksum registrado %s, actual %s); restaura el fichero o rehaz la migración con down/up", m.ID(), rec.Checksum, m.Checksum)
		}
	}
	return nil
}
