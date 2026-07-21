package balancer

import (
	"testing"

	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// anchor de prueba: iron_ore del seed (base 100, clamps [20, 400]).
func testInput(recent int64, supplyEMAOld float64, window int64) curveInput {
	return curveInput{
		D0PerSimDay:  1000,
		SupplyEMAOld: supplyEMAOld,
		RecentSupply: recent,
		WindowSim:    window,
		BasePrice:    100,
		PriceFloor:   20,
		PriceCeiling: 400,
		LuxuryClass:  false,
	}
}

// TestRecomputeCurveClamps: el precio nunca sale de [floor, ceiling] y supply_ema
// nunca cae a 0 (suelo obligatorio), ni con escasez extrema ni con inundación
// extrema (GDD 5.6, test a).
func TestRecomputeCurveClamps(t *testing.T) {
	o := DefaultOptions()
	cases := []struct {
		name   string
		recent int64
		emaOld float64
		window int64
	}{
		{"escasez_extrema", 0, 0.001, simtime.SimDay},
		{"escasez_sin_ventana", 0, 1, 0},
		{"inundacion_extrema", 1_000_000_000, 1, simtime.SimDay},
		{"inundacion_ventana_diminuta", 1_000_000, 1, 1},
		{"equilibrio", 1000, 1000, simtime.SimDay},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := recomputeCurve(testInput(c.recent, c.emaOld, c.window), o)
			if out.SupplyEMA < o.SupplyEMAFloor {
				t.Fatalf("supply_ema %g por debajo del suelo %g", out.SupplyEMA, o.SupplyEMAFloor)
			}
			if out.SupplyEMA <= 0 {
				t.Fatalf("supply_ema %g no es > 0", out.SupplyEMA)
			}
			if out.CurrentPrice < 20 || out.CurrentPrice > 400 {
				t.Fatalf("precio %d fuera de [20, 400]", out.CurrentPrice)
			}
			if out.SaturationFactor < o.SaturationMin || out.SaturationFactor > o.SaturationMax {
				t.Fatalf("saturation_factor %g fuera de [%g, %g]", out.SaturationFactor, o.SaturationMin, o.SaturationMax)
			}
		})
	}
}

// TestRecomputeCurvePriceDirection: inundar la ciudad baja el precio hacia el
// floor; la escasez lo sube hacia el ceiling (GDD 5.6, test b).
func TestRecomputeCurvePriceDirection(t *testing.T) {
	o := DefaultOptions()

	// Inundación: oferta reciente muy por encima de D0 → precio al floor.
	flood := recomputeCurve(testInput(100_000, 1, simtime.SimDay), o)
	if flood.CurrentPrice != 20 {
		t.Fatalf("inundación: precio %d, esperado el floor 20", flood.CurrentPrice)
	}

	// Escasez sostenida: sin oferta, el EMA cae al suelo → precio al ceiling.
	scarce := recomputeCurve(testInput(0, 1, simtime.SimDay), o)
	if scarce.CurrentPrice != 400 {
		t.Fatalf("escasez: precio %d, esperado el ceiling 400", scarce.CurrentPrice)
	}

	if flood.CurrentPrice >= scarce.CurrentPrice {
		t.Fatalf("inundación (%d) debería ser < escasez (%d)", flood.CurrentPrice, scarce.CurrentPrice)
	}
}

// TestRecomputeCurveLuxuryMoreElastic: ante la misma escasez moderada, la clase
// luxury (elástica) sube el precio MÁS que la basic (inelástica) — dos clases de
// elasticidad (GDD 5.6).
func TestRecomputeCurveLuxuryMoreElastic(t *testing.T) {
	o := DefaultOptions()
	// Escasez moderada: EMA old alto para que el ratio no clampe al ceiling.
	// D0=1000, supplyEMA≈2000 tras la muestra 0 → ratio 0.5 (exceso leve).
	base := curveInput{D0PerSimDay: 1000, SupplyEMAOld: 3000, RecentSupply: 0, WindowSim: simtime.SimDay,
		BasePrice: 100, PriceFloor: 1, PriceCeiling: 100000, LuxuryClass: false}
	basic := recomputeCurve(base, o)
	base.LuxuryClass = true
	luxury := recomputeCurve(base, o)
	// ratio < 1 (exceso) → luxury (exponente mayor) cae MÁS que basic.
	if luxury.CurrentPrice >= basic.CurrentPrice {
		t.Fatalf("con exceso, luxury (%d) debería caer más que basic (%d)", luxury.CurrentPrice, basic.CurrentPrice)
	}
}

// TestDecideLevelUp: supply_index >= base*nivel → sube de nivel con +pob y factor
// de D0 (>10000), como mucho un nivel por ventana (GDD 5.6, test e).
func TestDecideLevelUp(t *testing.T) {
	o := DefaultOptions()
	o.LevelupIndexBase = 100
	c := city{Level: 1, Population: 1000, SupplyIndex: 150} // 150 >= 100*1
	out := decideLevel(c, 500, simtime.SimDay, o)
	if out.Direction != levelUp || out.Level != 2 {
		t.Fatalf("esperado level up a 2, got dir=%q level=%d", out.Direction, out.Level)
	}
	if out.Population <= c.Population {
		t.Fatalf("la población debería crecer (%d -> %d)", c.Population, out.Population)
	}
	if out.D0FactorBP <= 10000 {
		t.Fatalf("el factor de D0 debería ser > 10000 al subir (got %d)", out.D0FactorBP)
	}
}

// TestDecideLevelDecayAndDown: sin suministro en la ventana, supply_index decae;
// si cruza a la baja el umbral de histéresis, baja de nivel.
func TestDecideLevelDecayAndDown(t *testing.T) {
	o := DefaultOptions()
	o.LevelupIndexBase = 100
	o.SupplyIndexDecayPerSimDay = 60
	// Nivel 2 con índice apenas por encima del umbral de bajada (100*(2-1)=100).
	c := city{Level: 2, Population: 1000, SupplyIndex: 130}
	// Ventana de un día → decae 60 → 70 < 100 → baja a nivel 1.
	out := decideLevel(c, 0, simtime.SimDay, o)
	if out.SupplyIndex >= c.SupplyIndex {
		t.Fatalf("el índice debería decaer (%g -> %g)", c.SupplyIndex, out.SupplyIndex)
	}
	if out.Direction != levelDown || out.Level != 1 {
		t.Fatalf("esperado level down a 1, got dir=%q level=%d", out.Direction, out.Level)
	}
}

// TestDecideLevelNoDecayWithSupply: con suministro en la ventana no hay
// decaimiento (el abandono es la condición del decaimiento).
func TestDecideLevelNoDecayWithSupply(t *testing.T) {
	o := DefaultOptions()
	o.LevelupIndexBase = 100000
	o.SupplyIndexDecayPerSimDay = 1000
	// Índice en la banda estable del nivel 2: [base*(2-1), base*2) = [100000, 200000).
	c := city{Level: 2, Population: 1000, SupplyIndex: 150000}
	out := decideLevel(c, 42, simtime.SimDay, o) // hubo oferta
	if out.SupplyIndex != c.SupplyIndex {
		t.Fatalf("no debería decaer con oferta (%g -> %g)", c.SupplyIndex, out.SupplyIndex)
	}
	if out.Direction != levelNone {
		t.Fatalf("no debería cambiar de nivel (got %q)", out.Direction)
	}
}

// TestBuyTargetQty: la cantidad objetivo escala con el factor de déficit
// (saturation_factor) y es 0 si no hay demanda.
func TestBuyTargetQty(t *testing.T) {
	o := DefaultOptions()                // BuyTargetDays = 2
	scarce := buyTargetQty(1000, 2.0, o) // 1000*2*2 = 4000
	if scarce != 4000 {
		t.Fatalf("escasez: qty %d, esperado 4000", scarce)
	}
	oversupplied := buyTargetQty(1000, 0.1, o) // 1000*2*0.1 = 200
	if oversupplied != 200 {
		t.Fatalf("exceso: qty %d, esperado 200", oversupplied)
	}
	if oversupplied >= scarce {
		t.Fatalf("el exceso (%d) debería comprar menos que la escasez (%d)", oversupplied, scarce)
	}
	if got := buyTargetQty(0, 1.0, o); got != 0 {
		t.Fatalf("sin demanda base la qty debe ser 0 (got %d)", got)
	}
}
