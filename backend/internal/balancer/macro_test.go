package balancer

import (
	"testing"

	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// TestEffectiveSalary comprueba la fórmula laboral (GDD 5.7): el salario crece
// con el nivel de la ciudad y con la ocupación industrial, acotado por el
// multiplicador máximo de saturación.
func TestEffectiveSalary(t *testing.T) {
	o := DefaultOptions()

	base := effectiveSalary(1, 0, o)
	if base != o.SalaryBase {
		t.Fatalf("salario nivel 1 sin saturación = %d, esperado %d (base)", base, o.SalaryBase)
	}
	// Sube con la ocupación.
	sat := effectiveSalary(1, 2, o) // mult = clamp(1+0.5·2,1,3) = 2
	if sat <= base {
		t.Fatalf("saturación no subió el salario: %d <= %d", sat, base)
	}
	if want := int64(200); sat != want {
		t.Fatalf("salario con ocupación 2 = %d, esperado %d", sat, want)
	}
	// Acotado por el multiplicador máximo.
	capped := effectiveSalary(1, 100, o) // mult clamp a 3.0
	if want := int64(300); capped != want {
		t.Fatalf("salario con ocupación enorme = %d, esperado %d (cap 3×)", capped, want)
	}
	// Sube con el nivel.
	lvl2 := effectiveSalary(2, 0, o) // base·(1+0.25)
	if lvl2 <= base {
		t.Fatalf("el nivel superior no subió el salario base: %d <= %d", lvl2, base)
	}
	if want := int64(125); lvl2 != want {
		t.Fatalf("salario base nivel 2 = %d, esperado %d", lvl2, want)
	}
	// Nunca por debajo de 1.
	if s := effectiveSalary(0, 0, o); s < 1 {
		t.Fatalf("salario %d < 1 (base_salary debe ser positivo)", s)
	}
}

// TestFiscalDirection comprueba la señal del lazo fiscal: inflación (masa
// monetaria crece más que el PIB) → subir; deflación → bajar; banda muerta y
// series demasiado cortas → mantener.
func TestFiscalDirection(t *testing.T) {
	o := DefaultOptions()

	cases := []struct {
		name   string
		recent []macroPoint
		want   int
	}{
		{"inflacion", []macroPoint{{MoneySupply: 2000, SimulatedGDP: 500}, {MoneySupply: 1000, SimulatedGDP: 500}}, +1},
		{"deflacion", []macroPoint{{MoneySupply: 1000, SimulatedGDP: 500}, {MoneySupply: 1000, SimulatedGDP: 100}}, -1},
		{"estable", []macroPoint{{MoneySupply: 1000, SimulatedGDP: 500}, {MoneySupply: 1000, SimulatedGDP: 500}}, 0},
		{"un_punto", []macroPoint{{MoneySupply: 1000, SimulatedGDP: 500}}, 0},
		{"vacio", nil, 0},
	}
	for _, c := range cases {
		if got := fiscalDirection(c.recent, o); got != c.want {
			t.Errorf("%s: fiscalDirection = %d, esperado %d", c.name, got, c.want)
		}
	}
}

// TestNextTaxBP comprueba que el ajuste de tax_rate_bp da pasos del tamaño
// configurado y NUNCA sale del rango [TaxMinBP, TaxMaxBP].
func TestNextTaxBP(t *testing.T) {
	o := DefaultOptions() // min 0, max 2000, step 50

	if got := nextTaxBP(500, +1, o); got != 550 {
		t.Fatalf("subida normal: %d, esperado 550", got)
	}
	if got := nextTaxBP(500, -1, o); got != 450 {
		t.Fatalf("bajada normal: %d, esperado 450", got)
	}
	if got := nextTaxBP(1990, +1, o); got != int32(o.TaxMaxBP) {
		t.Fatalf("techo: %d, esperado %d (clamp a max)", got, o.TaxMaxBP)
	}
	if got := nextTaxBP(10, -1, o); got != int32(o.TaxMinBP) {
		t.Fatalf("suelo: %d, esperado %d (clamp a min)", got, o.TaxMinBP)
	}
	// Iterar muchos pasos al alza jamás excede el techo.
	tax := int32(0)
	for i := 0; i < 1000; i++ {
		tax = nextTaxBP(tax, +1, o)
		if tax > int32(o.TaxMaxBP) || tax < int32(o.TaxMinBP) {
			t.Fatalf("tax %d fuera de rango [%d,%d]", tax, o.TaxMinBP, o.TaxMaxBP)
		}
	}
	if tax != int32(o.TaxMaxBP) {
		t.Fatalf("tras 1000 subidas tax=%d, esperado el techo %d", tax, o.TaxMaxBP)
	}
}

// TestNextCanon comprueba el paso proporcional del canon y su clamp al rango.
func TestNextCanon(t *testing.T) {
	o := DefaultOptions() // min 100, max 100000, step 200bp (2%)

	if got := nextCanon(1000, +1, o); got != 1020 {
		t.Fatalf("subida de canon: %d, esperado 1020 (+2%%)", got)
	}
	if got := nextCanon(100, -1, o); got != o.CanonMin {
		t.Fatalf("suelo de canon: %d, esperado %d", got, o.CanonMin)
	}
	if got := nextCanon(o.CanonMax, +1, o); got != o.CanonMax {
		t.Fatalf("techo de canon: %d, esperado %d", got, o.CanonMax)
	}
}

// TestComputeDepletion comprueba el ritmo global y la proyección por recurso.
func TestComputeDepletion(t *testing.T) {
	o := DefaultOptions() // horizonte 360 días de juego
	simNow := simtime.SimTime(10 * simtime.SimDay)
	byProduct := []depletionProduct{{Code: "iron_ore", Remaining: 1000, Extracted: 500}}

	rate, report := computeDepletion(simNow, byProduct, o)
	if rate != 50 { // 500 extraído / 10 días
		t.Fatalf("ritmo global = %g, esperado 50/día", rate)
	}
	r, ok := report.Resources["iron_ore"]
	if !ok {
		t.Fatal("falta la proyección de iron_ore")
	}
	if r.RatePerSimDay != 50 || r.SimDaysToDepletion != 20 || !r.DepletedWithinHorizon {
		t.Fatalf("proyección iron_ore = %+v, esperado rate 50, days 20, dentro de horizonte", r)
	}

	// Sin extracción: ritmo 0 y sin agotamiento proyectado.
	rate0, report0 := computeDepletion(simNow, []depletionProduct{{Code: "coal", Remaining: 100, Extracted: 0}}, o)
	if rate0 != 0 {
		t.Fatalf("ritmo con extracción nula = %g, esperado 0", rate0)
	}
	if c := report0.Resources["coal"]; c.SimDaysToDepletion != -1 || c.DepletedWithinHorizon {
		t.Fatalf("coal sin extracción: %+v, esperado sin agotamiento", c)
	}
}

// TestBucketStartSim comprueba el cálculo de inicio de bucket (floor).
func TestBucketStartSim(t *testing.T) {
	const bucket = int64(86_400)
	cases := []struct{ now, want int64 }{
		{0, 0},
		{100, 0},
		{86_400, 86_400},
		{2_592_000 + 100, 2_592_000},
		{-5, 0},
	}
	for _, c := range cases {
		if got := bucketStartSim(c.now, bucket); got != c.want {
			t.Errorf("bucketStartSim(%d) = %d, esperado %d", c.now, got, c.want)
		}
	}
}
