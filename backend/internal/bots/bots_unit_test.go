package bots

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

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
	t.Setenv(EnvTransformers, "4")
	t.Setenv(EnvFreighters, "5")
	t.Setenv(EnvTransformerMarginBP, "3000")
	t.Setenv(EnvFreighterMarginBP, "1500")
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
		Transformers: 4, Freighters: 5,
		TransformerMarginBP: 3_000, FreighterMarginBP: 1_500,
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

func TestCeilDiv(t *testing.T) {
	cases := []struct {
		a, b, want int64
	}{
		{2_860, 8, 358}, // coste por lingote: 2860 de insumos / 8 de salida
		{16, 8, 2},
		{1, 8, 1},
		{0, 8, 0},
		{10, 0, 0}, // divisor inválido: 0 (defensivo)
	}
	for _, tc := range cases {
		if got := ceilDiv(tc.a, tc.b); got != tc.want {
			t.Errorf("ceilDiv(%d, %d) = %d, esperado %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestSiteCandidateDeterministicRings(t *testing.T) {
	const ax, ay, base, step = 20_000.0, 20_000.0, 6_000.0, 4_000.0
	x0, y0 := siteCandidate(ax, ay, 0, base, step)
	if x0 != 26_000 || y0 != 20_000 {
		t.Fatalf("candidato 0 = (%v, %v), esperado (26000, 20000)", x0, y0)
	}
	// Mismo índice ⇒ mismo emplazamiento (reintento tras reinicio del bot).
	if x, y := siteCandidate(ax, ay, 0, base, step); x != x0 || y != y0 {
		t.Fatal("siteCandidate debe ser determinista para el mismo índice")
	}
	// Índices distintos del primer anillo ⇒ emplazamientos distintos.
	seen := map[[2]float64]bool{}
	for i := range 8 {
		x, y := siteCandidate(ax, ay, i, base, step)
		if seen[[2]float64{x, y}] {
			t.Fatalf("candidato %d repetido: (%v, %v)", i, x, y)
		}
		seen[[2]float64{x, y}] = true
	}
	// El anillo siguiente se aleja un paso.
	if x, _ := siteCandidate(ax, ay, 8, base, step); x != ax+base+step {
		t.Fatalf("candidato 8 = %v, esperado %v (segundo anillo)", x, ax+base+step)
	}
}

func TestPolygonCenterOfSquare(t *testing.T) {
	x, y, ok := polygonCenter(squareAround(26_000, 20_000, 1_500))
	if !ok || x != 26_000 || y != 20_000 {
		t.Fatalf("polygonCenter = (%v, %v, %v), esperado (26000, 20000, true)", x, y, ok)
	}
	if _, _, ok := polygonCenter(botsdk.GeoPolygon{}); ok {
		t.Fatal("un polígono vacío no tiene centro")
	}
}

func TestFreighterQuoteMarginRule(t *testing.T) {
	// La regla de aceptación es pura: ingreso >= coste × (1 + margen).
	cost := int64(440)
	required := applyBPCeil(cost, 10_000+DefaultFreighterMarginBP)
	if required != 528 {
		t.Fatalf("ingreso exigido = %d, esperado 528 (440 × 1,20)", required)
	}
	if !(int64(2_000) >= required) {
		t.Fatal("una tarifa de 2000 debe cubrir el coste con margen")
	}
	if int64(500) >= required {
		t.Fatal("una tarifa de 500 NO debe cubrir el coste con margen")
	}
}

// TestPassHeartbeat fija la regla operativa de auditabilidad: NINGUNA pasada
// sin error es muda. Una pasada que no decide nada emite UNA sola decisión
// wait con el motivo anotado más aguas arriba y el detalle de todas las
// etapas, de modo que un bot sano parado (esperando insumos, con la venta ya
// activa) se distingue en ii_bot_decisions_total de uno colgado.
func TestPassHeartbeat(t *testing.T) {
	const botName = "Bot Prueba 01"
	newTestBase := func(buf *bytes.Buffer) base {
		return newBase(botName, "test", slog.New(slog.NewJSONHandler(buf, nil)), NewMetrics(nil))
	}
	waits := func(b *base) float64 {
		return testutil.ToFloat64(b.metrics.Decisions.WithLabelValues(botName, "wait"))
	}

	// (1) Pasada ociosa: el motivo es el PRIMERO anotado y el log lleva el
	// detalle de las dos etapas.
	var buf bytes.Buffer
	b := newTestBase(&buf)
	if err := b.pass(func() error {
		b.idle("awaiting_inputs", slog.Int64("stock", 0), slog.Int64("low_water", 50))
		b.idle("sell_already_active", slog.String("publication_id", "pub-1"))
		return nil
	}); err != nil {
		t.Fatalf("pass ociosa: %v", err)
	}
	if got := waits(&b); got != 1 {
		t.Fatalf("una pasada ociosa debe emitir exactamente 1 wait, emitió %v", got)
	}
	line := buf.String()
	for _, want := range []string{
		`"decision":"wait"`, `"reason":"awaiting_inputs"`,
		`"awaiting_inputs":{"stock":0,"low_water":50}`,
		`"sell_already_active":{"publication_id":"pub-1"}`,
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("el log de la pasada ociosa no contiene %s: %s", want, line)
		}
	}

	// (2) Pasada que actúa: no se añade ningún wait redundante.
	buf.Reset()
	b = newTestBase(&buf)
	if err := b.pass(func() error {
		b.idle("sell_already_active", slog.String("publication_id", "pub-1"))
		b.decide("accept_buy", "price_at_or_above_threshold")
		return nil
	}); err != nil {
		t.Fatalf("pass con acción: %v", err)
	}
	if got := waits(&b); got != 0 {
		t.Fatalf("una pasada que decide no debe emitir wait, emitió %v", got)
	}

	// (3) Pasada fallida: no emite latido (el error ya deja rastro propio).
	buf.Reset()
	b = newTestBase(&buf)
	sentinel := errors.New("fallo de la pasada")
	if err := b.pass(func() error {
		b.idle("awaiting_inputs")
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("pass debe propagar el error, devolvió %v", err)
	}
	if got := waits(&b); got != 0 {
		t.Fatalf("una pasada fallida no debe emitir wait, emitió %v", got)
	}

	// (4) Invariante duro: sin ninguna anotación se emite igualmente el latido
	// con el motivo de reserva (un no-op sin instrumentar es visible, no mudo).
	buf.Reset()
	b = newTestBase(&buf)
	if err := b.pass(func() error { return nil }); err != nil {
		t.Fatalf("pass sin anotaciones: %v", err)
	}
	if got := waits(&b); got != 1 {
		t.Fatalf("una pasada sin anotaciones debe emitir 1 wait, emitió %v", got)
	}
	if !strings.Contains(buf.String(), `"reason":"`+idleFallbackReason+`"`) {
		t.Fatalf("el latido de reserva debe usar el motivo %q: %s", idleFallbackReason, buf.String())
	}

	// (5) Las pasadas no arrastran estado: la siguiente vuelve a anotar desde
	// cero (motivo propio, sin los grupos de la anterior).
	buf.Reset()
	if err := b.pass(func() error {
		b.idle("queue_full", slog.Int("pending", 3))
		return nil
	}); err != nil {
		t.Fatalf("segunda pass: %v", err)
	}
	if got := waits(&b); got != 2 {
		t.Fatalf("dos pasadas ociosas = 2 waits, contadas %v", got)
	}
	if line := buf.String(); !strings.Contains(line, `"reason":"queue_full"`) ||
		strings.Contains(line, idleFallbackReason) {
		t.Fatalf("la segunda pasada arrastró el estado de la primera: %s", line)
	}
}
