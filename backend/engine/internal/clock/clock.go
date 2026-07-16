// Package clock avanza y cachea el reloj sim-time persistido
// (world.sim_clock, fila única). Ratio 24x por defecto: 1 s wall = 24 s sim
// (ADR-IMPL-06); configurable con TIME_RATIO (>24 SOLO para tests/desarrollo).
package clock

import (
	"context"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"imperio/engine/internal/db"
)

// defaultSimRatio es el ratio de producción (ADR-IMPL-06): 1 s wall = 24 s sim.
const defaultSimRatio = 24

// simRatioFromEnv lee TIME_RATIO del entorno (default 24). Valores > 24 son
// SOLO para tests/desarrollo (aceleran construcción, lotes y tránsitos); en
// producción el ratio debe ser siempre 24.
func simRatioFromEnv() int64 {
	if v := os.Getenv("TIME_RATIO"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			return n
		}
	}
	return defaultSimRatio
}

type Clock struct {
	pool  *pgxpool.Pool
	ratio int64 // ratio sim/wall (TIME_RATIO, default 24)

	mu       sync.Mutex
	simNow   int64 // cacheado por tick
	frozen   bool
	lastWall time.Time // instante wall del último tick (monotónico)
	carryMs  int64     // residuo de milisegundos sim aún no volcados (precisión)
}

func New(pool *pgxpool.Pool) *Clock {
	return &Clock{pool: pool, ratio: simRatioFromEnv(), lastWall: time.Now()}
}

// Tick avanza el reloj: sim_seconds += ms_transcurridos*24/1000, acumulando el
// residuo en carryMs para no perder precisión. Si frozen, no avanza (y el
// bucle principal se salta el resto de procesadores). Devuelve el sim-time
// cacheado del tick.
func (c *Clock) Tick(ctx context.Context) (simNow int64, frozen bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	elapsedMs := now.Sub(c.lastWall).Milliseconds()
	c.lastWall = now
	if elapsedMs < 0 {
		elapsedMs = 0
	}

	err = db.RunSerializable(ctx, c.pool, func(tx pgx.Tx) error {
		var dbSim int64
		var dbFrozen bool
		if err := tx.QueryRow(ctx,
			`SELECT sim_seconds, frozen FROM world.sim_clock WHERE id = 1 FOR UPDATE`).
			Scan(&dbSim, &dbFrozen); err != nil {
			return err
		}
		if dbFrozen {
			// Congelado (ventana de mantenimiento): no avanza ni acumula.
			c.simNow, c.frozen, c.carryMs = dbSim, true, 0
			return nil
		}
		total := c.carryMs + elapsedMs*c.ratio
		addSec := total / 1000
		c.carryMs = total % 1000
		c.simNow = dbSim + addSec
		c.frozen = false
		if addSec > 0 {
			_, err := tx.Exec(ctx,
				`UPDATE world.sim_clock SET sim_seconds = $1, updated_at = now() WHERE id = 1`, c.simNow)
			return err
		}
		return nil
	})
	if err != nil {
		return c.simNow, c.frozen, err
	}
	return c.simNow, c.frozen, nil
}

// Now devuelve el sim-time cacheado por el último Tick.
func (c *Clock) Now() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.simNow
}
