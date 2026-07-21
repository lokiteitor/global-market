package balancer

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/lokiteitor/global-market/backend/internal/platform/db"
)

// Ajuste fiscal algorítmico (banco central algorítmico, GDD 5.5).
//
// El Balancer regula la fiscalidad de las regiones con un LAZO SUAVE y ACOTADO,
// nunca un salto brusco: compara la tendencia de la masa monetaria frente a la
// del PIB simulado (economy_indicators recientes). Si la masa monetaria crece más
// rápido que el PIB (presión inflacionaria) SUBE los impuestos un paso pequeño
// (más absorción por sinks fiscales); si crece más despacio (deflación / holgura)
// los BAJA. El tax_rate_bp y el canon_base se mueven SIEMPRE dentro de su rango
// [min, max] configurado — el lazo jamás los saca de rango.
//
// La señal es inflación = crecimiento(masa monetaria) − crecimiento(PIB). Con al
// menos dos buckets consecutivos:
//
//	crecimiento(x) = (x_nuevo − x_viejo) / max(x_viejo, 1)
//
// Si |inflación| <= umbral el lazo NO actúa (banda muerta anti-parpadeo).

// fiscalDirection decide el sentido del ajuste fiscal a partir de la serie macro
// reciente (ordenada del más reciente al más antiguo): +1 sube impuestos
// (inflación), −1 los baja (deflación), 0 mantiene. Función pura (testeable).
func fiscalDirection(recent []macroPoint, o Options) int {
	if len(recent) < 2 {
		return 0 // sin tendencia medible: no actuar
	}
	newer, older := recent[0], recent[1]
	moneyGrowth := growth(newer.MoneySupply, older.MoneySupply)
	gdpGrowth := growth(newer.SimulatedGDP, older.SimulatedGDP)
	inflation := moneyGrowth - gdpGrowth
	switch {
	case inflation > o.FiscalInflationThreshold:
		return +1
	case inflation < -o.FiscalInflationThreshold:
		return -1
	default:
		return 0
	}
}

// growth calcula el crecimiento relativo (nuevo − viejo) / max(viejo, 1),
// evitando la división por cero cuando el punto anterior es 0.
func growth(newer, older int64) float64 {
	den := older
	if den < 1 {
		den = 1
	}
	return float64(newer-older) / float64(den)
}

// nextTaxBP aplica un paso de ajuste al tax_rate_bp en el sentido dir, clampado
// al rango [TaxMinBP, TaxMaxBP] (nunca fuera de rango, GDD 5.5).
func nextTaxBP(current int32, dir int, o Options) int32 {
	next := int(current) + dir*o.TaxStepBP
	return int32(clampIntRange(next, o.TaxMinBP, o.TaxMaxBP))
}

// nextCanon aplica un paso PROPORCIONAL (CanonStepBP del canon vigente) al
// canon_base en el sentido dir, clampado al rango [CanonMin, CanonMax].
func nextCanon(current int64, dir int, o Options) int64 {
	step := current * o.CanonStepBP / 10000
	if step < 1 && o.CanonStepBP > 0 {
		step = 1 // paso mínimo perceptible en canones pequeños
	}
	next := current + int64(dir)*step
	return clampInt64(next, o.CanonMin, o.CanonMax)
}

// clampIntRange acota v a [lo, hi] (enteros de máquina).
func clampIntRange(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// recalcFiscal ejecuta un barrido del lazo fiscal: lee la tendencia macro, decide
// el sentido y aplica un paso acotado a la fiscalidad de cada región, todo en UNA
// transacción SERIALIZABLE. Con menos de dos buckets no hace nada (sin tendencia).
// Devuelve el sentido aplicado (para la traza) y error.
func (w *AnalyticsWorker) recalcFiscal(ctx context.Context) (int, error) {
	recent, err := w.repo.RecentEconomyIndicators(ctx, 2)
	if err != nil {
		return 0, err
	}
	dir := fiscalDirection(recent, w.opts)

	regions, err := w.repo.ListRegions(ctx)
	if err != nil {
		return 0, err
	}
	if len(regions) == 0 {
		return dir, nil
	}

	// Estado resultante para fijar los gauges tras el COMMIT (fuera de la
	// transacción, que RunSerializable puede reejecutar).
	applied := make(map[string]int32, len(regions))
	err = db.RunSerializable(ctx, w.pool, func(tx pgx.Tx) error {
		clear(applied)
		r := w.repo.WithTx(tx)
		for _, reg := range regions {
			tax := nextTaxBP(reg.TaxRateBP, dir, w.opts)
			canon := nextCanon(reg.CanonBase, dir, w.opts)
			if err := r.UpdateRegionFiscal(ctx, reg.ID, tax, canon); err != nil {
				return err
			}
			applied[reg.ID.String()] = tax
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	for region, tax := range applied {
		w.metrics.setTaxRateBP(region, tax)
	}
	if dir != 0 {
		w.logger.LogAttrs(ctx, slog.LevelDebug, "ajuste fiscal aplicado",
			slog.Int("direction", dir),
			slog.Int("regions", len(regions)))
	}
	return dir, nil
}
