package core

import "testing"

func TestFormatSimTime(t *testing.T) {
	cases := []struct {
		sim  int64
		want string
	}{
		{0, "1-001-00:00"},
		{3661, "1-001-01:01"},
		{86400, "1-002-00:00"},
		{86400*359 + 23*3600 + 59*60, "1-360-23:59"},
		{86400 * 360, "2-001-00:00"},
		{86400*720 + 12*3600 + 30*60, "3-001-12:30"},
	}
	for _, c := range cases {
		if got := FormatSimTime(c.sim); got != c.want {
			t.Errorf("FormatSimTime(%d) = %q, want %q", c.sim, got, c.want)
		}
	}
}

func TestParseSimTimeRoundTrip(t *testing.T) {
	for _, sim := range []int64{0, 60, 3600, 86400, 86400*360 + 12*3600, 123456 * 60} {
		s := FormatSimTime(sim)
		back, err := ParseSimTime(s)
		if err != nil {
			t.Fatalf("ParseSimTime(%q): %v", s, err)
		}
		// La precisión del formato es de minuto.
		if back != sim-(sim%60) {
			t.Errorf("round trip %d → %q → %d", sim, s, back)
		}
	}
	if _, err := ParseSimTime("1-999-00:00"); err == nil {
		t.Error("ParseSimTime aceptó un día fuera de rango")
	}
}

func TestCityPriceClamps(t *testing.T) {
	// Escasez extrema (supply_ema en el suelo): el precio debe topar en el ceiling.
	price, sat := CityPrice(100, 50, 300, 100000, 0.5, "basic")
	if price != 300 {
		t.Errorf("escasez: price = %d, want ceiling 300", price)
	}
	if sat < 0 || sat > 10 {
		t.Errorf("saturación fuera de [0,10]: %f", sat)
	}
	// Sobreoferta extrema: el precio debe topar en el floor.
	price, sat = CityPrice(100, 50, 300, 10, 100000, "basic")
	if price != 50 {
		t.Errorf("sobreoferta: price = %d, want floor 50", price)
	}
	if sat != 10 {
		t.Errorf("sobreoferta: saturación = %f, want 10 (clamp)", sat)
	}
	// Equilibrio (ratio 1): precio = base con cualquier k.
	price, _ = CityPrice(100, 50, 300, 500, 500, "luxury")
	if price != 100 {
		t.Errorf("equilibrio: price = %d, want base 100", price)
	}
	// luxury es más sensible que basic ante la misma escasez (ratio 2).
	pb, _ := CityPrice(100, 1, 100000, 200, 100, "basic")
	pl, _ := CityPrice(100, 1, 100000, 200, 100, "luxury")
	if pl <= pb {
		t.Errorf("luxury (%d) debería reaccionar más que basic (%d)", pl, pb)
	}
}

func TestSegmentDuration(t *testing.T) {
	// 45 km a 80 km/h sin congestión: 45000×3.6/80 = 2025 s sim.
	d, speed := SegmentDuration(45000, 80, 100, 1.0)
	if d != 2025 {
		t.Errorf("duración = %d, want 2025", d)
	}
	if speed != 80 {
		t.Errorf("velocidad efectiva = %f, want 80 (min(base, vehículo))", speed)
	}
	// El vehículo lento limita: min(80, 40) = 40 → 45000×3.6/40 = 4050.
	d, speed = SegmentDuration(45000, 80, 40, 1.0)
	if d != 4050 || speed != 40 {
		t.Errorf("vehículo lento: d=%d speed=%f, want 4050/40", d, speed)
	}
	// Congestión 2 divide la velocidad: duración se duplica.
	d2, speed2 := SegmentDuration(45000, 80, 100, 2.0)
	if d2 != 4050 || speed2 != 40 {
		t.Errorf("congestión: d=%d speed=%f, want 4050/40", d2, speed2)
	}
	// Nunca devuelve duración < 1.
	if d, _ := SegmentDuration(1, 1000, 1000, 1.0); d < 1 {
		t.Errorf("duración mínima violada: %d", d)
	}
}
