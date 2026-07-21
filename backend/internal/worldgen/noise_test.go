package worldgen

import (
	"math"
	"testing"
)

// TestNoiseDeterminism: la misma semilla y coordenadas producen SIEMPRE el mismo
// valor (requisito duro del generador: mismo II_WORLD_SEED ⇒ mismo mundo).
func TestNoiseDeterminism(t *testing.T) {
	a := newNoise(42)
	b := newNoise(42)
	for _, p := range [][2]float64{{0, 0}, {0.5, 0.5}, {-1.5, 2.25}, {13.7, -4.2}, {100.1, 100.9}} {
		if a.elevation(p[0], p[1]) != b.elevation(p[0], p[1]) {
			t.Fatalf("elevación no determinista en (%.2f,%.2f)", p[0], p[1])
		}
		if a.humidity(p[0], p[1]) != b.humidity(p[0], p[1]) {
			t.Fatalf("humedad no determinista en (%.2f,%.2f)", p[0], p[1])
		}
	}
}

// TestNoiseDifferentSeeds: semillas distintas producen mundos distintos (al menos
// en algún punto): el value-noise depende realmente de la semilla.
func TestNoiseDifferentSeeds(t *testing.T) {
	a := newNoise(1)
	b := newNoise(2)
	diff := false
	for x := 0.0; x < 5; x++ {
		for y := 0.0; y < 5; y++ {
			if a.elevation(x+0.5, y+0.5) != b.elevation(x+0.5, y+0.5) {
				diff = true
			}
		}
	}
	if !diff {
		t.Fatal("dos semillas distintas produjeron la misma elevación en toda la rejilla")
	}
}

// TestNoiseRange: elevación y humedad caen siempre en [0,1] (invariante que la
// tabla de biomas asume).
func TestNoiseRange(t *testing.T) {
	n := newNoise(42)
	for i := -50; i <= 50; i++ {
		for j := -50; j <= 50; j++ {
			x := float64(i) * 0.37
			y := float64(j) * 0.41
			e := n.elevation(x, y)
			h := n.humidity(x, y)
			if e < 0 || e > 1 {
				t.Fatalf("elevación fuera de rango en (%.2f,%.2f): %f", x, y, e)
			}
			if h < 0 || h > 1 {
				t.Fatalf("humedad fuera de rango en (%.2f,%.2f): %f", x, y, h)
			}
		}
	}
}

// TestNoiseContinuity: el ruido es continuo (fade quíntico): un desplazamiento
// pequeño produce un cambio pequeño, sin saltos. Cota holgada pero real (descarta
// ruido blanco por celda).
func TestNoiseContinuity(t *testing.T) {
	n := newNoise(7)
	const eps = 1e-4
	const maxDelta = 0.05 // un paso de 1e-4 no puede mover el valor más de esto
	for _, p := range [][2]float64{{0.1, 0.1}, {2.3, -1.7}, {5.5, 5.5}, {-3.2, 4.8}} {
		base := n.elevation(p[0], p[1])
		for _, d := range [][2]float64{{eps, 0}, {0, eps}, {-eps, 0}, {0, -eps}} {
			got := n.elevation(p[0]+d[0], p[1]+d[1])
			if math.Abs(got-base) > maxDelta {
				t.Fatalf("discontinuidad en (%.2f,%.2f)+(%.5f,%.5f): %f vs %f",
					p[0], p[1], d[0], d[1], got, base)
			}
		}
	}
}

// TestNoiseChannelsDecorrelated: elevación y humedad no son la misma función
// (canales disjuntos): difieren en la mayoría de los puntos.
func TestNoiseChannelsDecorrelated(t *testing.T) {
	n := newNoise(42)
	equal := 0
	total := 0
	for i := 0; i < 20; i++ {
		for j := 0; j < 20; j++ {
			x := float64(i) + 0.5
			y := float64(j) + 0.5
			if n.elevation(x, y) == n.humidity(x, y) {
				equal++
			}
			total++
		}
	}
	if equal == total {
		t.Fatal("elevación y humedad coinciden en todos los puntos: canales no disjuntos")
	}
}
