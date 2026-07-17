package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lokiteitor/global-market/backend/internal/platform/config"
	"github.com/lokiteitor/global-market/backend/internal/platform/httpx"
	"github.com/lokiteitor/global-market/backend/internal/platform/metrics"
)

// testApp construye una App contra una BD inaccesible (puerto 1): el pool es
// perezoso, así que el ensamblado funciona y readyz debe responder 503.
func testApp(t *testing.T) *App {
	t.Helper()
	cfg := config.Config{
		DatabaseURL: "postgres://nadie:nada@127.0.0.1:1/inexistente?sslmode=disable",
		LogLevel:    "error",
	}
	app, err := New(context.Background(), metrics.ServiceGateway, ":0", cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(app.Close)
	return app
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	return rr
}

func TestHealthz(t *testing.T) {
	app := testApp(t)
	rr := get(t, app.Handler(), "/healthz")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, quiero 200", rr.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("cuerpo no JSON: %v (%q)", err, rr.Body.String())
	}
	if body["status"] != "ok" || body["service"] != "gateway" {
		t.Fatalf("cuerpo = %v", body)
	}
	if rr.Header().Get(httpx.HeaderRequestID) == "" {
		t.Fatal("la cadena de middlewares no añadió X-Request-Id")
	}
}

func TestReadyzUnavailableWithoutDatabase(t *testing.T) {
	app := testApp(t)
	rr := get(t, app.Handler(), "/readyz")

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, quiero 503 sin BD accesible", rr.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("cuerpo no JSON: %v", err)
	}
	if body["status"] != "unavailable" || body["service"] != "gateway" {
		t.Fatalf("cuerpo = %v", body)
	}
}

func TestMetricsEndpointServed(t *testing.T) {
	app := testApp(t)
	h := app.Handler()

	// Genera tráfico previo para que existan series HTTP.
	_ = get(t, h, "/healthz")

	rr := get(t, h, "/metrics")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, quiero 200", rr.Code)
	}
	out := rr.Body.String()
	for _, want := range []string{
		`ii_http_requests_total{method="GET",route="/healthz",service="gateway",status="200"}`,
		"ii_pgxpool_max_conns",
		"go_goroutines",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("/metrics no contiene %q", want)
		}
	}
}
