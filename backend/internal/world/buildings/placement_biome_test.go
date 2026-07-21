package buildings

import "testing"

// Tests de la regla requires_biome (ADR-025 §5: emplazamiento "ríos/agua" de
// las hidroeléctricas materializado como lista de biomas admitidos).

func TestParsePlacementRulesRequiresBiome(t *testing.T) {
	pr, err := parsePlacementRules([]byte(`{"requires_biome":["coast","ocean"]}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !pr.HasRequiresBiome || len(pr.RequiresBiome) != 2 || pr.RequiresBiome[0] != "coast" {
		t.Fatalf("regla requires_biome mal interpretada: %+v", pr)
	}

	// Lista vacía o tipo incorrecto ⇒ regla ausente (no bloquea).
	for _, raw := range []string{`{"requires_biome":[]}`, `{"requires_biome":"coast"}`} {
		pr, err := parsePlacementRules([]byte(raw))
		if err != nil {
			t.Fatalf("parse %s: %v", raw, err)
		}
		if pr.HasRequiresBiome {
			t.Fatalf("regla malformada %s no debe activarse: %+v", raw, pr)
		}
	}

	// Clave desconocida sigue cayendo en Unknown (extensibilidad hacia delante).
	pr, err = parsePlacementRules([]byte(`{"near_river_m":500}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(pr.Unknown) != 1 || pr.Unknown[0] != "near_river_m" {
		t.Fatalf("clave desconocida no recogida: %+v", pr)
	}
}

func TestValidBiome(t *testing.T) {
	for _, b := range []string{"plains", "forest", "desert", "mountain", "ocean", "coast"} {
		if !validBiome(b) {
			t.Fatalf("bioma %q debe ser válido", b)
		}
	}
	if validBiome("river") || validBiome("") {
		t.Fatal("biomas desconocidos deben rechazarse (se ignoran con warn)")
	}
}
