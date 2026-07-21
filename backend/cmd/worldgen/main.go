// El binario worldgen genera proceduralmente el mundo multi-región del
// Incremento 7 (FASE 2 MUNDO, GDD 9) contra II_DATABASE_URL, delegando en
// internal/worldgen. Es ADITIVO y determinista: conserva Askadia (0,0) y su seed
// intactos y añade las regiones que la rodean (biomas por value-noise, ciudades,
// yacimientos, red vial intra-región y enlaces rail/sea inter-región con
// terminales intermodales). Exige que el seed haya corrido antes (banco central,
// reloj, catálogo mínimo). Seguro de re-ejecutar: misma II_WORLD_SEED ⇒ mismo
// mundo, sin duplicados.
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
	"github.com/lokiteitor/global-market/backend/internal/worldgen"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "worldgen:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opts, err := worldgen.OptionsFromEnv()
	if err != nil {
		return err
	}
	pool, err := db.NewPool(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()

	logger := logging.New(cfg, "worldgen")
	summary, err := worldgen.Generate(ctx, pool, opts, logger)
	if err != nil {
		return err
	}

	printSummary(summary)
	return nil
}

// printSummary vuelca a stdout el resumen del mundo generado (grilla, biomas,
// conteos) para inspección directa desde la línea de comandos.
func printSummary(s worldgen.Summary) {
	fmt.Printf("Mundo generado — semilla=%d grilla=%dx%d región=%dm\n",
		s.Seed, s.Grid, s.Grid, s.RegionSizeM)
	fmt.Printf("  regiones creadas: %d | ciudades: %d | yacimientos: %d\n",
		s.RegionsCreated, s.CitiesCreated, s.DepositsCreated)
	fmt.Printf("  enlaces inter-región: rail=%d sea=%d | terminales: %d\n",
		s.RailLinks, s.SeaLinks, s.TerminalsCreated)
	fmt.Println("  biomas por celda:")
	for _, r := range s.Regions {
		tag := "tierra"
		if !r.Terrestrial {
			tag = "agua"
		}
		fmt.Printf("    (%+d,%+d) %-10s %-8s %s\n", r.GridX, r.GridY, r.Biome, tag, r.Name)
	}
}
