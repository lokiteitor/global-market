package balancer

import (
	"context"
	"log/slog"
	"math"

	"github.com/jackc/pgx/v5"

	"github.com/lokiteitor/global-market/backend/internal/platform/db"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// Fórmula laboral (GDD 5.7).
//
// DECISIÓN VINCULANTE: world.cities.base_salary almacena el salario EFECTIVO ya
// recalculado por el Balancer, NO un salario nominal por nivel. El módulo de
// producción del Incremento 6a/2 lee cities.base_salary tal cual para el sink de
// salario, de modo que el Balancer es la única autoridad que lo fija:
//
//	salario_efectivo(ciudad) = salario_base(nivel_ciudad)
//	                           · factor_saturación(ocupación_industrial_regional)
//
// donde
//   - salario_base(nivel) = SalaryBase · (1 + SalaryPerLevelBP·(nivel−1)/10000):
//     el salario crece con el nivel de la ciudad (mano de obra más cualificada,
//     GDD 5.7).
//   - factor_saturación(ocupación) = clamp(1 + k·ocupación, [minMult, maxMult]):
//     una región con mucha industria activa (ocupación alta) puja al alza los
//     salarios; el clamp lo mantiene ACOTADO (nunca dispara el sink de salario a
//     valores irreales). La ocupación proviene de analytics.region_stats
//     (edificios operativos / capacidad de referencia), que el job de analítica
//     escribe antes en el mismo barrido.
//
// El resultado se clampa además a >= 1 (base_salary es money_amount positivo).

// salaryByLevel devuelve el salario base por nivel de ciudad (sin saturación).
func salaryByLevel(level int32, o Options) int64 {
	if level < 1 {
		level = 1
	}
	base := o.SalaryBase * (10000 + o.SalaryPerLevelBP*int64(level-1)) / 10000
	if base < 1 {
		base = 1
	}
	return base
}

// effectiveSalary aplica la fórmula laboral (GDD 5.7): salario_base(nivel) por el
// factor de saturación acotado de la ocupación industrial regional. Función pura
// (testeable sin BD).
func effectiveSalary(level int32, occupation float64, o Options) int64 {
	base := salaryByLevel(level, o)
	if occupation < 0 {
		occupation = 0
	}
	mult := clampFloat(1+o.LaborSaturationK*occupation, o.LaborSalaryMinMult, o.LaborSalaryMaxMult)
	salary := int64(math.Round(float64(base) * mult))
	if salary < 1 {
		salary = 1
	}
	return salary
}

// recalcLabor recalcula el salario efectivo de cada ciudad a partir de la
// ocupación industrial reciente de su región (analytics.region_stats) y lo
// persiste en world.cities.base_salary. Todas las escrituras en UNA transacción
// SERIALIZABLE. Best-effort a nivel de barrido: un fallo se propaga al llamante,
// que lo registra sin detener el bucle macro.
func (w *AnalyticsWorker) recalcLabor(ctx context.Context, simNow simtime.SimTime) error {
	occupation, err := w.repo.LatestRegionOccupation(ctx)
	if err != nil {
		return err
	}
	cities, err := w.repo.ListCities(ctx)
	if err != nil {
		return err
	}
	if len(cities) == 0 {
		return nil
	}

	return db.RunSerializable(ctx, w.pool, func(tx pgx.Tx) error {
		r := w.repo.WithTx(tx)
		for _, c := range cities {
			salary := effectiveSalary(c.Level, occupation[c.RegionID], w.opts)
			if err := r.UpdateCityBaseSalary(ctx, c.ID, salary); err != nil {
				return err
			}
			w.logger.LogAttrs(ctx, slog.LevelDebug, "salario efectivo recalculado",
				slog.String("city_id", c.ID.String()),
				slog.String("city", c.Name),
				slog.Int("level", int(c.Level)),
				slog.Float64("region_occupation", occupation[c.RegionID]),
				slog.Int64("base_salary", salary),
				slog.Int64("sim_time_at", int64(simNow)))
		}
		return nil
	})
}
