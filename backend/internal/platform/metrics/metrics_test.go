package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMiddlewareObservesRequests(t *testing.T) {
	m := New(ServiceGateway)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /falla", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	h := m.Middleware(mux)

	for _, path := range []string{"/healthz", "/falla", "/no-existe"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	}

	// El propio handler de /metrics sirve para verificar la exposición.
	rr := httptest.NewRecorder()
	m.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body, err := io.ReadAll(rr.Result().Body)
	if err != nil {
		t.Fatalf("leyendo /metrics: %v", err)
	}
	out := string(body)

	wantSeries := []string{
		`ii_http_requests_total{method="GET",route="/healthz",service="gateway",status="200"} 1`,
		`ii_http_requests_total{method="GET",route="/falla",service="gateway",status="500"} 1`,
		`ii_http_requests_total{method="GET",route="unmatched",service="gateway",status="404"} 1`,
		`ii_http_request_duration_seconds_count{method="GET",route="/healthz",service="gateway",status="200"} 1`,
	}
	for _, want := range wantSeries {
		if !strings.Contains(out, want) {
			t.Errorf("/metrics no contiene la serie esperada:\n  %s", want)
		}
	}

	// Collectors estándar de Go/proceso presentes.
	for _, want := range []string{"go_goroutines", "process_cpu_seconds_total"} {
		if !strings.Contains(out, want) {
			t.Errorf("/metrics no contiene el collector estándar %q", want)
		}
	}
}

func TestMiddlewareDefaultsTo200WithoutWriteHeader(t *testing.T) {
	m := New(ServiceEngine)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /implicit", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok")) // sin WriteHeader explícito
	})
	h := m.Middleware(mux)

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/implicit", nil))

	mr := httptest.NewRecorder()
	m.Handler().ServeHTTP(mr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	out := mr.Body.String()
	want := `ii_http_requests_total{method="GET",route="/implicit",service="engine",status="200"} 1`
	if !strings.Contains(out, want) {
		t.Fatalf("/metrics no contiene %q", want)
	}
}
