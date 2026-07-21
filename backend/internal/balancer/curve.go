package balancer

import (
	"math"

	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// productClassLuxury es el literal del enum world.product_class de la clase
// elástica (GDD 5.6: dos clases, basic inelástica / luxury elástica).
const productClassLuxury = "luxury"

// curveInput son las magnitudes de entrada del recálculo de una (ciudad,
// producto) para un producto.
type curveInput struct {
	D0PerSimDay  int64   // demanda base D0(producto, nivel), > 0 en filas con demanda
	SupplyEMAOld float64 // supply_ema previo (media móvil de oferta, > 0)
	RecentSupply int64   // oferta entregada desde el último recálculo (>= 0)
	WindowSim    int64   // sim-segundos transcurridos desde el último recálculo
	BasePrice    int64   // ancla de precio del producto (> 0, GDD 5.1)
	PriceFloor   int64   // clamp inferior obligatorio (> 0)
	PriceCeiling int64   // clamp superior obligatorio (>= floor)
	LuxuryClass  bool    // true: clase elástica; false: basic inelástica
}

// curveOutput es el resultado acotado del recálculo: supply_ema (con suelo > 0),
// saturation_factor (acotado) y current_price (clampado en [floor, ceiling]).
type curveOutput struct {
	SupplyEMA        float64
	SaturationFactor float64
	CurrentPrice     int64
}

// recomputeCurve aplica el modelo de demanda dinámica de la ciudad (GDD 5.6) con
// TODOS los clamps obligatorios, de modo que ni una ciudad inundada ni una sin
// suministro produzcan precios/factores fuera de rango:
//
//  1. oferta reciente normalizada a tasa por día de juego:
//     observed_rate = recent_supply * SimDay / window_sim
//     (window_sim <= 0 → una jornada de juego: génesis / sin tiempo transcurrido)
//  2. supply_ema = alpha*observed_rate + (1-alpha)*supply_ema_old, con SUELO > 0
//     (nunca 0: sin suelo, una ciudad sin oferta daría precio → ∞)
//  3. raw_ratio = D0 / supply_ema (>1 escasez, <1 saturación/exceso)
//  4. saturation_factor = clamp(raw_ratio, [satMin, satMax]) — multiplicador de
//     demanda efectiva: decae (<1) cuando la oferta supera a la demanda
//  5. current_price = clamp(round(base_price * raw_ratio^elasticidad),
//     [price_floor, price_ceiling]) — elasticidad por clase (basic < 1 inelástica,
//     luxury > 1 elástica)
func recomputeCurve(in curveInput, o Options) curveOutput {
	window := in.WindowSim
	if window <= 0 {
		window = simtime.SimDay // génesis / sin tiempo transcurrido: una jornada
	}
	observedRate := float64(in.RecentSupply) * float64(simtime.SimDay) / float64(window)

	supplyEMA := o.SupplyEMAAlpha*observedRate + (1-o.SupplyEMAAlpha)*in.SupplyEMAOld
	if supplyEMA < o.SupplyEMAFloor {
		supplyEMA = o.SupplyEMAFloor // SUELO obligatorio > 0 (GDD 5.6)
	}

	rawRatio := float64(in.D0PerSimDay) / supplyEMA

	saturation := clampFloat(rawRatio, o.SaturationMin, o.SaturationMax)

	elasticity := o.ElasticityBasic
	if in.LuxuryClass {
		elasticity = o.ElasticityLuxury
	}
	priceF := float64(in.BasePrice) * math.Pow(rawRatio, elasticity)
	price := clampInt64(int64(math.Round(priceF)), in.PriceFloor, in.PriceCeiling)

	return curveOutput{SupplyEMA: supplyEMA, SaturationFactor: saturation, CurrentPrice: price}
}

// buyTargetQty calcula la cantidad objetivo de la solicitud de compra de una
// ciudad: D0 por el horizonte de compra (BuyTargetDays días de juego) escalado
// por el factor de déficit (saturation_factor: >1 cuando escasea → compra más;
// <1 cuando sobra → compra menos, frenando la inundación). Suelo 0 (no negativa).
func buyTargetQty(d0PerSimDay int64, saturationFactor float64, o Options) int64 {
	target := float64(d0PerSimDay) * o.BuyTargetDays * saturationFactor
	if target <= 0 {
		return 0
	}
	return int64(math.Round(target))
}

// clampFloat acota v a [lo, hi].
func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// clampInt64 acota v a [lo, hi].
func clampInt64(v, lo, hi int64) int64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
