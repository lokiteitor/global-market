package ledger

import "testing"

// La liquidación pro-rata debe repartir escrow y garantía en proporción a lo
// entregado, sin crear ni destruir valor (identidades contables exactas).
func TestProrataProportionality(t *testing.T) {
	cases := []struct{ agreed, delivered, price int64 }{
		{100, 100, 80}, // fill completo
		{100, 50, 80},  // mitad
		{100, 0, 80},   // fallo total
		{7, 3, 13},     // números que no dividen exacto (redondeo entero)
		{1000000, 333333, 997},
	}
	for _, c := range cases {
		r := Prorata(c.agreed, c.delivered, c.price, 5000)
		// Conservación: filled + missing = total, en valor y en garantía.
		if r.ValueFilled+r.ValueMissing != r.ValueTotal {
			t.Errorf("%+v: valor no conservado", c)
		}
		if r.GuarFilled+r.GuarMissing != r.GuarTotal {
			t.Errorf("%+v: garantía no conservada", c)
		}
		// La garantía incumplida se reparte íntegra entre compensación y sink.
		if r.Compensation+r.SinkPart != r.GuarMissing {
			t.Errorf("%+v: reparto compensación/sink no cierra", c)
		}
		// Proporcionalidad exacta del valor: delivered × price.
		if r.ValueFilled != c.delivered*c.price {
			t.Errorf("%+v: value_filled = %d", c, r.ValueFilled)
		}
		// Proporcionalidad de la garantía (con redondeo entero hacia abajo):
		// guar_filled = floor(guar_total × delivered / agreed).
		want := (r.GuarTotal * c.delivered) / c.agreed
		if r.GuarFilled != want {
			t.Errorf("%+v: guar_filled = %d, want %d", c, r.GuarFilled, want)
		}
		if r.QtyMissing != c.agreed-c.delivered {
			t.Errorf("%+v: qty_missing = %d", c, r.QtyMissing)
		}
		// Con compensación al 50%, comp ≤ guar_missing y sink ≥ 0.
		if r.Compensation > r.GuarMissing || r.SinkPart < 0 {
			t.Errorf("%+v: compensación fuera de rango", c)
		}
	}
	// Fill 100%: nada al sink, nada de compensación.
	r := Prorata(100, 100, 80, 5000)
	if r.GuarMissing != 0 || r.Compensation != 0 || r.SinkPart != 0 || r.ValueMissing != 0 {
		t.Errorf("fill completo: no debe haber penalización: %+v", r)
	}
}
