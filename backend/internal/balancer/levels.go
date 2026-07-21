package balancer

import (
	"math"

	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// Agregado y tipos de evento del crecimiento de ciudad que el Balancer emite por
// el outbox (contrato entre agentes: nombre + payload JSON, no el código Go).
const (
	aggregateCity = "city"

	// EventCityLevelUp / EventCityLevelDown: cambios de nivel de una ciudad
	// (población, huella, categorías desbloqueadas), objetivo estratégico
	// observable por todos los jugadores (GDD 5.6).
	EventCityLevelUp   = "city.level_up"
	EventCityLevelDown = "city.level_down"
)

// maxCityLevel acota el nivel de ciudad (alineado con el max_level de edificios,
// GDD 6.3): una ciudad no crece indefinidamente en una sola ventana.
const maxCityLevel int32 = 8

// levelDirection es la dirección del cambio de nivel ("" = sin cambio).
type levelDirection string

const (
	levelNone levelDirection = ""
	levelUp   levelDirection = "up"
	levelDown levelDirection = "down"
)

// levelOutcome es la decisión de la máquina de niveles para una ciudad.
type levelOutcome struct {
	Level       int32
	Population  int64
	SupplyIndex float64
	Direction   levelDirection
	D0FactorBP  int64 // != 0 si hay que escalar D0 (crecimiento/reducción de demanda)
}

// decideLevel aplica la máquina de niveles de una ciudad (GDD 5.6) con
// histéresis y como MUCHO un cambio de nivel por ventana:
//
//   - decaimiento: si no hubo suministro en la ventana (totalRecentSupply == 0),
//     supply_index decae SupplyIndexDecayPerSimDay por día de juego transcurrido
//     (nunca por debajo de 0) — el abandono logístico penaliza a la ciudad.
//   - subir de nivel: supply_index >= LevelupIndexBase * nivel (umbral escalado)
//     → nivel+1, población +PopulationGrowthPct%, D0 +D0GrowthPct% (desbloquea de
//     hecho las categorías cuyo unlocked_at_level == nivel nuevo).
//   - bajar de nivel: supply_index < LevelupIndexBase * (nivel-1) (histéresis) y
//     nivel > 1 → nivel-1, población y D0 reducidos simétricamente.
func decideLevel(c city, totalRecentSupply int64, windowSim int64, o Options) levelOutcome {
	out := levelOutcome{Level: c.Level, Population: c.Population, SupplyIndex: c.SupplyIndex}

	// Decaimiento por abandono logístico (solo si no hubo oferta en la ventana).
	if totalRecentSupply == 0 && o.SupplyIndexDecayPerSimDay > 0 {
		window := windowSim
		if window <= 0 {
			window = simtime.SimDay
		}
		decay := o.SupplyIndexDecayPerSimDay * float64(window) / float64(simtime.SimDay)
		out.SupplyIndex = math.Max(0, out.SupplyIndex-decay)
	}

	// Subir de nivel: umbral escalado por el nivel actual.
	if c.Level < maxCityLevel && out.SupplyIndex >= o.LevelupIndexBase*float64(c.Level) {
		out.Level = c.Level + 1
		out.Population = grownPopulation(c.Population, o.PopulationGrowthPct, true)
		out.D0FactorBP = growthFactorBP(o.D0GrowthPct, true)
		out.Direction = levelUp
		return out
	}

	// Bajar de nivel: histéresis en el umbral del nivel inferior.
	if c.Level > 1 && out.SupplyIndex < o.LevelupIndexBase*float64(c.Level-1) {
		out.Level = c.Level - 1
		out.Population = grownPopulation(c.Population, o.PopulationGrowthPct, false)
		out.D0FactorBP = growthFactorBP(o.D0GrowthPct, false)
		out.Direction = levelDown
		return out
	}

	return out
}

// grownPopulation escala la población por ±pct (up=true suma, up=false resta),
// con suelo 0. Rango realista de población: sin riesgo de overflow int64.
func grownPopulation(population int64, pct int, up bool) int64 {
	if up {
		return population * int64(100+pct) / 100
	}
	p := population * int64(100-pct) / 100
	if p < 0 {
		return 0
	}
	return p
}

// growthFactorBP es el factor en puntos básicos (×/10000) para escalar D0 al
// cambiar de nivel: up=true → 10000 + pct*100 (p. ej. +20% = 12000); up=false →
// inverso 10000*10000/(10000+pct*100) (p. ej. 8333 ≈ deshace un +20%).
func growthFactorBP(pct int, up bool) int64 {
	up100 := int64(10000 + pct*100)
	if up {
		return up100
	}
	return 10000 * 10000 / up100
}
