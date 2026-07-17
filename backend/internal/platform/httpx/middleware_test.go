package httpx

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func testLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, nil))
}

func TestRequestIDGeneratesUUIDv7(t *testing.T) {
	var seen string
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RequestIDFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}), RequestID())

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/x", nil))

	header := rr.Header().Get(HeaderRequestID)
	if header == "" {
		t.Fatal("la respuesta no lleva X-Request-Id")
	}
	if seen != header {
		t.Fatalf("id del contexto %q != cabecera %q", seen, header)
	}
	id, err := uuid.Parse(header)
	if err != nil {
		t.Fatalf("X-Request-Id no es un UUID: %v", err)
	}
	if id.Version() != 7 {
		t.Fatalf("versión UUID = %d, quiero 7", id.Version())
	}
}

func TestRequestIDRespectsValidIncoming(t *testing.T) {
	incoming := uuid.Must(uuid.NewV7()).String()
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), RequestID())

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(HeaderRequestID, incoming)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if got := rr.Header().Get(HeaderRequestID); got != incoming {
		t.Fatalf("X-Request-Id = %q, quiero el entrante %q", got, incoming)
	}
}

func TestRequestIDReplacesInvalidIncoming(t *testing.T) {
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), RequestID())

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(HeaderRequestID, "no-es-un-uuid")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	got := rr.Header().Get(HeaderRequestID)
	if got == "no-es-un-uuid" || got == "" {
		t.Fatalf("X-Request-Id = %q, quiero un UUID generado", got)
	}
	if _, err := uuid.Parse(got); err != nil {
		t.Fatalf("X-Request-Id no es un UUID: %v", err)
	}
}

func TestRecoverWritesEnvelopeAndLogs(t *testing.T) {
	var buf bytes.Buffer
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("se rompió")
	}), RequestID(), Recover(testLogger(&buf)))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, quiero 500", rr.Code)
	}
	want := `{"error":{"code":"INTERNAL","message":"error interno del servidor"}}`
	if got := rr.Body.String(); got != want {
		t.Fatalf("cuerpo:\n  got:  %s\n  want: %s", got, want)
	}

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("log no es JSON: %v (%q)", err, buf.String())
	}
	if line["panic"] != "se rompió" {
		t.Fatalf("panic logueado = %v", line["panic"])
	}
	if stack, _ := line["stack"].(string); stack == "" {
		t.Fatal("el log del panic no incluye stack")
	}
	if line["request_id"] != rr.Header().Get(HeaderRequestID) {
		t.Fatalf("request_id del log = %v, quiero %q", line["request_id"], rr.Header().Get(HeaderRequestID))
	}
}

func TestRecoverDoesNotOverwriteStartedResponse(t *testing.T) {
	var buf bytes.Buffer
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, "parcial")
		panic("tras escribir")
	}), Recover(testLogger(&buf)))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, quiero el 202 original", rr.Code)
	}
	if rr.Body.String() != "parcial" {
		t.Fatalf("el cuerpo parcial no debe ampliarse: %q", rr.Body.String())
	}
	if buf.Len() == 0 {
		t.Fatal("el panic debe loguearse aunque la respuesta ya empezara")
	}
}

func TestRecoverRepanicsErrAbortHandler(t *testing.T) {
	var buf bytes.Buffer
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(http.ErrAbortHandler)
	}), Recover(testLogger(&buf)))

	defer func() {
		if p := recover(); p != http.ErrAbortHandler {
			t.Fatalf("panic propagado = %v, quiero http.ErrAbortHandler", p)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
}

func TestAccessLogLine(t *testing.T) {
	var buf bytes.Buffer
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}), RequestID(), AccessLog(testLogger(&buf)))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/tetera", nil))

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("log no es JSON: %v (%q)", err, buf.String())
	}
	if line["msg"] != "http_request" || line["method"] != "GET" || line["path"] != "/tetera" {
		t.Fatalf("línea inesperada: %v", line)
	}
	if line["status"] != float64(http.StatusTeapot) {
		t.Fatalf("status logueado = %v, quiero 418", line["status"])
	}
	if line["request_id"] != rr.Header().Get(HeaderRequestID) {
		t.Fatalf("request_id = %v, quiero %q", line["request_id"], rr.Header().Get(HeaderRequestID))
	}
	if _, ok := line["duration"]; !ok {
		t.Fatal("la línea no incluye duration")
	}
}

// TestChainOrder verifica que el primer middleware de la lista es el más
// externo (envuelve a los demás).
func TestChainOrder(t *testing.T) {
	var order []string
	mark := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		order = append(order, "handler")
	}), mark("a"), mark("b"))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	want := []string{"a", "b", "handler"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("orden = %v, quiero %v", order, want)
		}
	}
}
