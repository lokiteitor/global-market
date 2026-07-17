package simtime

import (
	"testing"
	"time"
)

func TestFormat(t *testing.T) {
	cases := []struct {
		name string
		in   SimTime
		want string
	}{
		{"génesis", 0, "001-001-00:00"},
		{"último segundo del primer minuto", 59, "001-001-00:00"},
		{"primer minuto cumplido", 60, "001-001-00:01"},
		{"media hora", 1_800, "001-001-00:30"},
		{"una hora", 3_600, "001-001-01:00"},
		{"último minuto del primer día", SimTime(SimDay - 60), "001-001-23:59"},
		{"último segundo del primer día", SimTime(SimDay - 1), "001-001-23:59"},
		{"cambio de día", SimTime(SimDay), "001-002-00:00"},
		{"día 45, 12:30 (ejemplo del contrato)", SimTime(44*SimDay + 12*3600 + 30*60), "001-045-12:30"},
		{"último día del primer año", SimTime(SimYear - SimDay), "001-360-00:00"},
		{"último segundo del primer año", SimTime(SimYear - 1), "001-360-23:59"},
		{"cambio de año", SimTime(SimYear), "002-001-00:00"},
		{"ejemplo canónico del contrato (31104000)", 31_104_000, "002-001-00:00"},
		{"año 360, día 45 (ejemplo legible del contrato)", SimTime(359*SimYear + 44*SimDay + 12*3600 + 30*60), "360-045-12:30"},
		{"año de cuatro dígitos", SimTime(999*SimYear + SimDay), "1000-002-00:00"},
		{"negativo se trata como génesis", -1, "001-001-00:00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Format(tc.in); got != tc.want {
				t.Fatalf("Format(%d) = %q, quiero %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestDerive(t *testing.T) {
	anchor := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)

	cases := []struct {
		name   string
		base   SimTime
		now    time.Time
		ratio  int64
		frozen bool
		want   SimTime
	}{
		{"sin tiempo transcurrido", 1_000, anchor, Ratio, false, 1_000},
		{"un segundo real = ratio segundos sim", 0, anchor.Add(time.Second), Ratio, false, 24},
		{"una hora real = un día sim", 0, anchor.Add(time.Hour), Ratio, false, SimTime(SimDay)},
		{"base no nula acumula", 500, anchor.Add(time.Minute), Ratio, false, 500 + 60*24},
		{"precisión sub-segundo (500ms×24 = 12s sim)", 0, anchor.Add(500 * time.Millisecond), Ratio, false, 12},
		{"trunca hacia cero por debajo del segundo sim", 0, anchor.Add(40 * time.Millisecond), Ratio, false, 0},
		{"ratio 1 (tiempo real)", 0, anchor.Add(90 * time.Second), 1, false, 90},
		{"congelado ignora el tiempo transcurrido", 7_777, anchor.Add(48 * time.Hour), Ratio, true, 7_777},
		{"congelado en el génesis", 0, anchor.Add(time.Hour), Ratio, true, 0},
		{"elapsed negativo trunca hacia cero de forma simétrica", 1_000, anchor.Add(-time.Second), Ratio, false, 976},
		{"15 días reales cruzan el año sim", 0, anchor.Add(15 * 24 * time.Hour), Ratio, false, SimTime(SimYear)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Derive(tc.base, anchor, tc.now, tc.ratio, tc.frozen)
			if got != tc.want {
				t.Fatalf("Derive(%d, anchor, anchor+%v, %d, %v) = %d, quiero %d",
					tc.base, tc.now.Sub(anchor), tc.ratio, tc.frozen, got, tc.want)
			}
		})
	}
}

// TestDeriveLongSpan comprueba que intervalos muy largos no desbordan la
// aritmética interna (elapsed*ratio en nanosegundos desbordaría int64 a
// partir de ~12 años reales con ratio 24).
func TestDeriveLongSpan(t *testing.T) {
	anchor := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := anchor.Add(20 * 365 * 24 * time.Hour) // 20 años reales
	want := SimTime(20 * 365 * 24 * 3600 * Ratio)
	if got := Derive(0, anchor, now, Ratio, false); got != want {
		t.Fatalf("Derive con 20 años reales = %d, quiero %d", got, want)
	}
}

// TestDeriveFormatIntegración cruza ambas funciones: una hora real desde el
// génesis debe formatearse como el inicio del segundo día de juego.
func TestDeriveFormatIntegración(t *testing.T) {
	anchor := time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC)
	st := Derive(0, anchor, anchor.Add(time.Hour), Ratio, false)
	if got := Format(st); got != "001-002-00:00" {
		t.Fatalf("Format(Derive(1h)) = %q, quiero %q", got, "001-002-00:00")
	}
}
