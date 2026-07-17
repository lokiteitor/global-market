// El binario seed carga los datos mínimos de desarrollo (target `make seed`,
// ADR-016) delegando en internal/seed: reloj de simulación en génesis, banco
// central con sus cuentas de emisión y sink, dos corporaciones humanas (Demo
// y Norte Trading) con credencial, caja y capital semilla, y el mundo mínimo
// del Incremento 1: la región Askadia, los catálogos (iron_ore, coal,
// warehouse), una concesión + almacén + nodo logístico por corporación y el
// stock inicial físico y contable (ADR-022). Idempotente: re-ejecutarlo
// nunca duplica datos ni re-emite capital o stock.
//
// Rehúsa ejecutarse fuera de dev (II_ENV=prod): son datos de desarrollo, no
// de despliegue. La generación procedural del mundo (regiones, ciudades,
// yacimientos) y los catálogos versionados llegan en su fase (GDD §9).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/lokiteitor/global-market/backend/internal/platform/config"
	"github.com/lokiteitor/global-market/backend/internal/platform/db"
	"github.com/lokiteitor/global-market/backend/internal/platform/logging"
	"github.com/lokiteitor/global-market/backend/internal/seed"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !cfg.IsDev() {
		return fmt.Errorf("rehusado: II_ENV=%s (el seed carga datos de desarrollo, solo entornos no productivos)", cfg.Env)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opts, err := seed.OptionsFromEnv()
	if err != nil {
		return err
	}
	pool, err := db.NewPool(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()

	return seed.Run(ctx, pool, opts, logging.New(cfg, "seed"))
}
