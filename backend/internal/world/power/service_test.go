package power

import (
	"encoding/json"
	"testing"
)

func TestLineCost(t *testing.T) {
	cases := []struct {
		lengthM, costPerKm, want int64
	}{
		{5_000, 5_000, 25_000}, // 5 km exactos
		{1, 5_000, 5},          // fracción de km: redondeo hacia arriba
		{999, 1_000, 999},      // ceil(999*1000/1000)
		{1_500, 0, 0},          // coste 0 configurado
	}
	for _, c := range cases {
		got, err := lineCost(c.lengthM, c.costPerKm)
		if err != nil || got != c.want {
			t.Fatalf("lineCost(%d, %d) = %d (err %v), esperado %d", c.lengthM, c.costPerKm, got, err, c.want)
		}
	}
	if _, err := lineCost(1<<62, 1<<62); err == nil {
		t.Fatal("lineCost debe detectar el desbordamiento de int64")
	}
}

func TestValidateLineString(t *testing.T) {
	valid := json.RawMessage(`{"type":"LineString","coordinates":[[0,0],[100,100]]}`)
	if err := validateLineString(valid); err != nil {
		t.Fatalf("LineString válido rechazado: %v", err)
	}
	invalid := []json.RawMessage{
		nil,
		json.RawMessage(`{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,0]]]}`),
		json.RawMessage(`{"type":"LineString","coordinates":[[0,0]]}`),
		json.RawMessage(`{"type":"LineString","coordinates":[[0],[1,1]]}`),
		json.RawMessage(`no-json`),
	}
	for i, raw := range invalid {
		if err := validateLineString(raw); err == nil {
			t.Fatalf("caso inválido %d aceptado: %s", i, raw)
		}
	}
}
