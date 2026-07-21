package bots

import (
	"testing"
	"time"

	"github.com/lokiteitor/global-market/backend/pkg/botsdk"
)

// mustProduct construye un producto mínimo para las reglas puras.
func mustProduct(code string, basePrice botsdk.Money) botsdk.Product {
	return botsdk.Product{ID: "prod-" + code, Code: code, BasePrice: basePrice}
}

func TestDeriveSecret(t *testing.T) {
	a := DeriveSecret("seed-1", "Bot Carbonera 01")
	b := DeriveSecret("seed-1", "Bot Carbonera 01")
	if a != b {
		t.Fatal("DeriveSecret debe ser determinista para (seed, nombre)")
	}
	if len(a) != 64 {
		t.Fatalf("DeriveSecret debe producir 64 hex chars, produjo %d", len(a))
	}
	if DeriveSecret("seed-2", "Bot Carbonera 01") == a {
		t.Fatal("una semilla distinta debe derivar un secreto distinto")
	}
	if DeriveSecret("seed-1", "Bot Carbonera 02") == a {
		t.Fatal("un nombre distinto debe derivar un secreto distinto")
	}
}

func TestApplyBP(t *testing.T) {
	cases := []struct {
		amount, bp, want int64
	}{
		{100, 9_500, 95},          // umbral de compra del trader: 95% del base
		{60, 9_000, 54},           // umbral de aceptación del carbonero: 90% de 60
		{60, 11_000, 66},          // precio de la solicitud de combustible: 110% de 60
		{500_000, 2_000, 100_000}, // colchón del trader: 20% del capital
	}
	for _, tc := range cases {
		if got := applyBP(tc.amount, tc.bp); got != tc.want {
			t.Errorf("applyBP(%d, %d) = %d, esperado %d", tc.amount, tc.bp, got, tc.want)
		}
	}
}

func TestApplyBPCeil(t *testing.T) {
	// Margen de re-listado del trader: pagado × 1,15 redondeado al alza.
	cases := []struct {
		amount, bp, want int64
	}{
		{54, 11_500, 63}, // 54 × 1,15 = 62,1 → 63
		{100, 11_500, 115},
		{1, 11_500, 2}, // 1,15 → 2
	}
	for _, tc := range cases {
		if got := applyBPCeil(tc.amount, tc.bp); got != tc.want {
			t.Errorf("applyBPCeil(%d, %d) = %d, esperado %d", tc.amount, tc.bp, got, tc.want)
		}
	}
}

func TestAcceptQty(t *testing.T) {
	cases := []struct {
		name              string
		remaining, minLot int64
		caps              []int64
		want              int64
	}{
		{"toma todo si los topes lo permiten", 100, 50, []int64{500, 1000}, 100},
		{"acota por stock libre", 100, 50, []int64{80}, 80},
		{"acota por presupuesto", 100, 50, []int64{500, 60}, 60},
		{"bajo el lote mínimo: no acepta", 100, 50, []int64{40}, 0},
		{"min_lot mayor que lo restante: el mínimo efectivo es lo restante", 30, 50, []int64{100}, 30},
		{"sin margen: cero", 100, 50, []int64{0}, 0},
	}
	for _, tc := range cases {
		if got := acceptQty(tc.remaining, tc.minLot, tc.caps...); got != tc.want {
			t.Errorf("%s: acceptQty(%d, %d, %v) = %d, esperado %d",
				tc.name, tc.remaining, tc.minLot, tc.caps, got, tc.want)
		}
	}
}

func TestOptionsFromEnvDefaultsAndOverrides(t *testing.T) {
	opts, err := OptionsFromEnv()
	if err != nil {
		t.Fatalf("OptionsFromEnv con entorno limpio: %v", err)
	}
	if opts != DefaultOptions() {
		t.Fatalf("defaults inesperados: %+v", opts)
	}

	t.Setenv(EnvCoalProducers, "3")
	t.Setenv(EnvIronProducers, "0")
	t.Setenv(EnvTraders, "2")
	t.Setenv(EnvSecretSeed, "otra-semilla")
	t.Setenv(EnvCapital, "750000")
	t.Setenv(EnvTick, "250ms")
	t.Setenv(EnvAddr, ":9999")
	t.Setenv(EnvAPIURL, "http://gateway:8080/api/v1")
	opts, err = OptionsFromEnv()
	if err != nil {
		t.Fatalf("OptionsFromEnv con overrides: %v", err)
	}
	want := Options{
		CoalProducers: 3, IronProducers: 0, Traders: 2,
		SecretSeed: "otra-semilla", Capital: 750_000,
		Tick: 250 * time.Millisecond, Addr: ":9999",
		APIURL: "http://gateway:8080/api/v1",
	}
	if opts != want {
		t.Fatalf("overrides: %+v, esperado %+v", opts, want)
	}
}

func TestOptionsFromEnvInvalid(t *testing.T) {
	t.Setenv(EnvCapital, "-5")
	if _, err := OptionsFromEnv(); err == nil {
		t.Fatal("capital negativo debía fallar")
	}
	t.Setenv(EnvCapital, "")
	t.Setenv(EnvTick, "nada")
	if _, err := OptionsFromEnv(); err == nil {
		t.Fatal("tick inválido debía fallar")
	}
	t.Setenv(EnvTick, "")
	t.Setenv(EnvCoalProducers, "-1")
	if _, err := OptionsFromEnv(); err == nil {
		t.Fatal("población negativa debía fallar")
	}
}

func TestRouteNameDeterministic(t *testing.T) {
	origin := "0197b2f0-1111-7000-8000-000000000001"
	dest := "0197b2f0-2222-7000-8000-000000000002"
	a := routeName(origin, dest)
	if a != routeName(origin, dest) {
		t.Fatal("routeName debe ser determinista")
	}
	if a == routeName(dest, origin) {
		t.Fatal("routeName debe distinguir el sentido")
	}
}

func TestTraderRelistPriceFallback(t *testing.T) {
	tr := NewTrader(DefaultTraderConfig(500_000), "Bot Mercader 01", nil, nil)
	st := NewState()
	// Sin memoria de compra: fallback conservador base × 95% × 115%.
	price, err := tr.relistPrice(st, mustProduct("coal", "60"))
	if err != nil {
		t.Fatalf("relistPrice: %v", err)
	}
	if want := applyBPCeil(applyBP(60, 9_500), 11_500); price != want {
		t.Fatalf("fallback = %d, esperado %d", price, want)
	}
	// Con memoria: pagado × 1,15 (techo).
	st.lastBuyPrice["prod-coal"] = 54
	price, err = tr.relistPrice(st, mustProduct("coal", "60"))
	if err != nil {
		t.Fatalf("relistPrice: %v", err)
	}
	if price != 63 {
		t.Fatalf("con memoria = %d, esperado 63 (54 × 1,15 techo)", price)
	}
}
