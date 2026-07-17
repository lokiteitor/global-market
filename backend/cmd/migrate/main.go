// El binario migrate es el runner propio de migraciones SQL (ADR-020):
// up | down [n] | status | create <nombre> | reset. Se invoca desde el
// Makefile raíz (migrate-up, migrate-down, migrate-status, migrate-create,
// reset-db) y sale con código distinto de cero ante cualquier error.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/lokiteitor/global-market/backend/internal/platform/config"
	"github.com/lokiteitor/global-market/backend/internal/platform/migrate"
)

const usage = `uso: migrate [-dir DIR] <orden>

órdenes:
  up               aplica las migraciones pendientes en orden
  down [n]         revierte las n últimas migraciones (por defecto 1)
  status           estado por migración y verificación de checksums
  create <nombre>  crea la pareja NNNN_<nombre>.{up,down}.sql
  reset            revierte todo y lo reaplica (rehusado si II_ENV=prod)

flags:
  -dir DIR         directorio de migraciones
                   (por defecto II_MIGRATIONS_DIR o db/migrations)
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run(argv []string) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	dir := fs.String("dir", "", "directorio de migraciones")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	args := fs.Args()
	if len(args) == 0 {
		fs.Usage()
		return fmt.Errorf("falta la orden")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if *dir == "" {
		*dir = cfg.MigrationsDir
	}

	switch cmd := args[0]; cmd {
	case "create":
		if len(args) != 2 {
			return fmt.Errorf("uso: migrate create <nombre>")
		}
		upPath, downPath, err := migrate.Create(*dir, args[1])
		if err != nil {
			return err
		}
		fmt.Println("creado", upPath)
		fmt.Println("creado", downPath)
		return nil
	case "up", "down", "status", "reset":
		n := 1
		switch {
		case cmd == "down" && len(args) == 2:
			v, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("n inválido %q: debe ser un entero", args[1])
			}
			n = v
		case cmd == "down" && len(args) > 2:
			return fmt.Errorf("uso: migrate down [n]")
		case cmd != "down" && len(args) != 1:
			return fmt.Errorf("la orden %s no admite argumentos", cmd)
		}
		return runDB(cmd, n, *dir, cfg)
	default:
		fs.Usage()
		return fmt.Errorf("orden desconocida %q", args[0])
	}
}

// runDB ejecuta las órdenes que requieren conexión a la base de datos.
func runDB(cmd string, n int, dir string, cfg config.Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	conn, err := pgx.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("conectando a la base de datos: %w", err)
	}
	defer conn.Close(context.WithoutCancel(ctx))

	r := migrate.New(conn, dir, cfg.Env, os.Stdout)
	switch cmd {
	case "up":
		applied, err := r.Up(ctx)
		if err != nil {
			return err
		}
		if len(applied) == 0 {
			fmt.Println("sin migraciones pendientes")
		} else {
			fmt.Printf("%d migración(es) aplicada(s)\n", len(applied))
		}
	case "down":
		reverted, err := r.Down(ctx, n)
		if err != nil {
			return err
		}
		fmt.Printf("%d migración(es) revertida(s)\n", len(reverted))
	case "status":
		items, verifyErr := r.Status(ctx)
		printStatus(items)
		if verifyErr != nil {
			return verifyErr
		}
	case "reset":
		if err := r.Reset(ctx); err != nil {
			return err
		}
		fmt.Println("reset completado")
	}
	return nil
}

// printStatus imprime la tabla de estado y un resumen de totales.
func printStatus(items []migrate.Status) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "VERSIÓN\tNOMBRE\tESTADO\tAPLICADA EN")
	applied := 0
	for _, it := range items {
		if it.Applied {
			applied++
			fmt.Fprintf(w, "%04d\t%s\taplicada\t%s\n",
				it.Migration.Version, it.Migration.Name, it.AppliedAt.Local().Format(time.RFC3339))
		} else {
			fmt.Fprintf(w, "%04d\t%s\tpendiente\t\n", it.Migration.Version, it.Migration.Name)
		}
	}
	w.Flush()
	fmt.Printf("%d aplicada(s), %d pendiente(s)\n", applied, len(items)-applied)
}
