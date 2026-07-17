package idempotency

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// resolverStub implementa AccountResolver con una cuenta fija.
type resolverStub struct {
	id uuid.UUID
	ok bool
}

func (s resolverStub) AccountID(context.Context) (uuid.UUID, bool) { return s.id, s.ok }

// fakeStore implementa store en memoria para los tests unitarios del
// middleware; los fallos y la carrera perdida se inyectan por campo.
type fakeStore struct {
	rows      map[string]storedResponse
	findErr   error
	saveErr   error
	saveLoses bool // save devuelve false (ON CONFLICT) sin escribir
	findCalls int
	saveCalls int
	lastSave  struct {
		key, account uuid.UUID
		method, path string
		resp         storedResponse
	}
}

func newFakeStore() *fakeStore {
	return &fakeStore{rows: make(map[string]storedResponse)}
}

func rowKey(key, account uuid.UUID) string { return key.String() + "/" + account.String() }

func (f *fakeStore) find(_ context.Context, key, account uuid.UUID) (storedResponse, bool, error) {
	f.findCalls++
	if f.findErr != nil {
		return storedResponse{}, false, f.findErr
	}
	resp, ok := f.rows[rowKey(key, account)]
	return resp, ok, nil
}

func (f *fakeStore) save(_ context.Context, key, account uuid.UUID, method, path string, resp storedResponse) (bool, error) {
	f.saveCalls++
	f.lastSave.key, f.lastSave.account = key, account
	f.lastSave.method, f.lastSave.path = method, path
	f.lastSave.resp = resp
	if f.saveErr != nil {
		return false, f.saveErr
	}
	if f.saveLoses {
		return false, nil
	}
	f.rows[rowKey(key, account)] = resp
	return true, nil
}

// newTestMiddleware construye el middleware sobre el fake y un handler que
// cuenta ejecuciones y responde 201 con JSON.
func newTestMiddleware(t *testing.T, st store, resolver AccountResolver) (*Middleware, http.Handler, *int) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := newMiddleware(st, resolver, prometheus.NewRegistry(), logger)
	executed := 0
	h := m.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		executed++
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"n":1},"meta":{}}`))
	}))
	return m, h, &executed
}

func doRequest(h http.Handler, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/ledger/publications", nil)
	if key != "" {
		req.Header.Set(Header, key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decodificando el envelope de error: %v (cuerpo %q)", err, rec.Body.String())
	}
	return body.Error.Code
}

func TestPassthroughSinCabecera(t *testing.T) {
	st := newFakeStore()
	_, h, executed := newTestMiddleware(t, st, resolverStub{id: uuid.New(), ok: true})

	rec := doRequest(h, "")
	if rec.Code != http.StatusCreated || *executed != 1 {
		t.Fatalf("status %d, ejecuciones %d; esperado 201 y 1", rec.Code, *executed)
	}
	if st.findCalls != 0 || st.saveCalls != 0 {
		t.Fatalf("sin cabecera el almacén no debe tocarse: find %d, save %d", st.findCalls, st.saveCalls)
	}
	if rec.Header().Get(HeaderReplayed) != "" {
		t.Fatal("passthrough no debe marcar Idempotency-Replayed")
	}
}

func TestClaveInvalida(t *testing.T) {
	st := newFakeStore()
	_, h, executed := newTestMiddleware(t, st, resolverStub{id: uuid.New(), ok: true})

	rec := doRequest(h, "no-es-un-uuid")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, esperado 400", rec.Code)
	}
	if code := errorCode(t, rec); code != "VALIDATION_ERROR" {
		t.Fatalf("código %q, esperado VALIDATION_ERROR", code)
	}
	if *executed != 0 || st.findCalls != 0 || st.saveCalls != 0 {
		t.Fatal("con clave inválida el handler y el almacén no deben tocarse")
	}
}

func TestSinCuentaAutenticada(t *testing.T) {
	st := newFakeStore()
	_, h, executed := newTestMiddleware(t, st, resolverStub{ok: false})

	rec := doRequest(h, uuid.NewString())
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, esperado 401", rec.Code)
	}
	if code := errorCode(t, rec); code != "UNAUTHORIZED" {
		t.Fatalf("código %q, esperado UNAUTHORIZED", code)
	}
	if *executed != 0 {
		t.Fatal("sin cuenta el handler no debe ejecutarse")
	}
}

func TestMissEjecutaYPersiste(t *testing.T) {
	st := newFakeStore()
	account := uuid.New()
	m, h, executed := newTestMiddleware(t, st, resolverStub{id: account, ok: true})

	key := uuid.New()
	rec := doRequest(h, key.String())
	if rec.Code != http.StatusCreated || *executed != 1 {
		t.Fatalf("status %d, ejecuciones %d; esperado 201 y 1", rec.Code, *executed)
	}
	if rec.Header().Get(HeaderReplayed) != "" {
		t.Fatal("el primer intento no es un replay")
	}
	if st.saveCalls != 1 {
		t.Fatalf("save llamado %d veces, esperado 1", st.saveCalls)
	}
	s := st.lastSave
	if s.key != key || s.account != account || s.method != http.MethodPost ||
		s.path != "/ledger/publications" || s.resp.Status != http.StatusCreated ||
		s.resp.ContentType != "application/json; charset=utf-8" ||
		string(s.resp.Body) != `{"data":{"n":1},"meta":{}}` {
		t.Fatalf("persistencia inesperada: %+v", s)
	}
	if got := testutil.ToFloat64(m.hits); got != 0 {
		t.Fatalf("hits tras un miss: %v, esperado 0", got)
	}
}

func TestHitReproduceRespuesta(t *testing.T) {
	st := newFakeStore()
	account := uuid.New()
	key := uuid.New()
	st.rows[rowKey(key, account)] = storedResponse{
		Status:      http.StatusCreated,
		ContentType: "application/json; charset=utf-8",
		Body:        []byte(`{"data":{"almacenada":true},"meta":{}}`),
	}
	m, h, executed := newTestMiddleware(t, st, resolverStub{id: account, ok: true})

	rec := doRequest(h, key.String())
	if *executed != 0 {
		t.Fatal("un hit no debe ejecutar el handler")
	}
	if rec.Code != http.StatusCreated ||
		rec.Body.String() != `{"data":{"almacenada":true},"meta":{}}` ||
		rec.Header().Get("Content-Type") != "application/json; charset=utf-8" ||
		rec.Header().Get(HeaderReplayed) != "true" {
		t.Fatalf("replay inesperado: status %d, cuerpo %q, cabeceras %v",
			rec.Code, rec.Body.String(), rec.Header())
	}
	if got := testutil.ToFloat64(m.hits); got != 1 {
		t.Fatalf("ii_idempotency_hits_total = %v, esperado 1", got)
	}
}

func TestCarreraPerdidaDevuelveLaAlmacenada(t *testing.T) {
	st := newFakeStore()
	account := uuid.New()
	key := uuid.New()
	m, _, _ := newTestMiddleware(t, st, resolverStub{id: account, ok: true})

	// El handler simula al perdedor: mientras se ejecuta, el "ganador"
	// concurrente ya persistió su respuesta; el save de esta petición
	// devuelve false (ON CONFLICT DO NOTHING).
	winner := storedResponse{
		Status:      http.StatusCreated,
		ContentType: "application/json; charset=utf-8",
		Body:        []byte(`{"data":{"ganador":true},"meta":{}}`),
	}
	executed := 0
	h := m.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		executed++
		st.rows[rowKey(key, account)] = winner
		st.saveLoses = true
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"perdedor":true},"meta":{}}`))
	}))

	rec := doRequest(h, key.String())
	if executed != 1 || st.saveCalls != 1 {
		t.Fatalf("ejecuciones %d, saves %d; esperado 1 y 1", executed, st.saveCalls)
	}
	if rec.Body.String() != string(winner.Body) || rec.Header().Get(HeaderReplayed) != "true" {
		t.Fatalf("el perdedor debe devolver la respuesta del ganador: %q (replayed %q)",
			rec.Body.String(), rec.Header().Get(HeaderReplayed))
	}
	if got := testutil.ToFloat64(m.hits); got != 1 {
		t.Fatalf("hits tras la carrera: %v, esperado 1", got)
	}
}

func TestErrorInternoNoSePersiste(t *testing.T) {
	st := newFakeStore()
	m, _, _ := newTestMiddleware(t, st, resolverStub{id: uuid.New(), ok: true})
	h := m.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))

	rec := doRequest(h, uuid.NewString())
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, esperado 500", rec.Code)
	}
	if st.saveCalls != 0 {
		t.Fatal("un 5xx no debe persistirse: el reintento debe ejecutar de verdad")
	}
}

func TestErrorDeBusquedaRechazaSinEjecutar(t *testing.T) {
	st := newFakeStore()
	st.findErr = errors.New("bd caída")
	_, h, executed := newTestMiddleware(t, st, resolverStub{id: uuid.New(), ok: true})

	rec := doRequest(h, uuid.NewString())
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, esperado 500", rec.Code)
	}
	if code := errorCode(t, rec); code != "INTERNAL" {
		t.Fatalf("código %q, esperado INTERNAL", code)
	}
	if *executed != 0 {
		t.Fatal("sin lectura del almacén no debe ejecutarse el handler (riesgo de doble ejecución)")
	}
}

func TestErrorAlPersistirEntregaLaRespuesta(t *testing.T) {
	st := newFakeStore()
	st.saveErr = errors.New("bd caída")
	_, h, executed := newTestMiddleware(t, st, resolverStub{id: uuid.New(), ok: true})

	rec := doRequest(h, uuid.NewString())
	if rec.Code != http.StatusCreated || *executed != 1 {
		t.Fatalf("status %d, ejecuciones %d; esperado 201 y 1 (la operación ya se ejecutó)", rec.Code, *executed)
	}
	if rec.Body.String() != `{"data":{"n":1},"meta":{}}` {
		t.Fatalf("cuerpo inesperado: %q", rec.Body.String())
	}
}
