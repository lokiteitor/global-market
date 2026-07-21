package worldgen

import "testing"

// TestBiomeForDecisionTable cubre la tabla de decisión de biomas en cada rama,
// incluidas las fronteras de los umbrales.
func TestBiomeForDecisionTable(t *testing.T) {
	cases := []struct {
		name      string
		elev, hum float64
		wantBiome string
	}{
		{"agua profunda", 0.10, 0.50, BiomeOcean},
		{"borde océano (<=umbral)", oceanElevMax, 0.50, BiomeOcean},
		{"costa litoral", 0.32, 0.50, BiomeCoast},
		{"borde costa (<=umbral)", coastElevMax, 0.90, BiomeCoast},
		{"montaña", 0.85, 0.50, BiomeMountain},
		{"borde montaña (>=umbral)", mountainElevMin, 0.10, BiomeMountain},
		{"desierto: medio y seco", 0.50, 0.20, BiomeDesert},
		{"desierto borde humedad", 0.50, desertHumidMax - 0.001, BiomeDesert},
		{"bosque: medio y húmedo", 0.50, 0.80, BiomeForest},
		{"bosque borde humedad", 0.50, forestHumidMin, BiomeForest},
		{"llanura: medio y templado", 0.50, 0.50, BiomePlains},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := biomeFor(c.elev, c.hum); got != c.wantBiome {
				t.Fatalf("biomeFor(%.3f,%.3f) = %s, esperado %s", c.elev, c.hum, got, c.wantBiome)
			}
		})
	}
}

// TestBiomeForOrdering verifica que el agua y la montaña dominan sobre el eje de
// humedad (el orden de las reglas importa): un punto muy alto y seco es montaña,
// no desierto; un punto muy bajo y húmedo es océano, no bosque.
func TestBiomeForOrdering(t *testing.T) {
	if got := biomeFor(0.95, 0.10); got != BiomeMountain {
		t.Fatalf("alto y seco debe ser mountain, fue %s", got)
	}
	if got := biomeFor(0.05, 0.95); got != BiomeOcean {
		t.Fatalf("bajo y húmedo debe ser ocean, fue %s", got)
	}
}

// TestBiomeClassifiers cubre isTerrestrial / isCoastalOrOcean.
func TestBiomeClassifiers(t *testing.T) {
	if isTerrestrial(BiomeOcean) {
		t.Fatal("el océano no es terrestre")
	}
	for _, b := range []string{BiomePlains, BiomeForest, BiomeDesert, BiomeMountain, BiomeCoast} {
		if !isTerrestrial(b) {
			t.Fatalf("%s debe ser terrestre", b)
		}
	}
	if !isCoastalOrOcean(BiomeCoast) || !isCoastalOrOcean(BiomeOcean) {
		t.Fatal("costa y océano dan acceso al mar")
	}
	for _, b := range []string{BiomePlains, BiomeForest, BiomeDesert, BiomeMountain} {
		if isCoastalOrOcean(b) {
			t.Fatalf("%s no da acceso al mar", b)
		}
	}
}
