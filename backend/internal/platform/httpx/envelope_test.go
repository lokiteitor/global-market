package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// metaFija produce un Meta determinista para los golden tests.
func metaFija() Meta {
	return Meta{
		SimTime:        "360-045-12:30",
		SimTimeSeconds: 31_104_000,
		ServerTime:     time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC),
	}
}

func TestWriteDataGolden(t *testing.T) {
	rr := httptest.NewRecorder()
	data := struct {
		Answer int    `json:"answer"`
		Name   string `json:"name"`
	}{Answer: 42, Name: "acme"}

	WriteData(rr, http.StatusOK, data, metaFija())

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, quiero 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
	want := `{"data":{"answer":42,"name":"acme"},"meta":{"sim_time":"360-045-12:30","sim_time_seconds":31104000,"server_time":"2026-07-15T10:00:00Z"}}`
	if got := rr.Body.String(); got != want {
		t.Fatalf("cuerpo:\n  got:  %s\n  want: %s", got, want)
	}
}

func TestWriteDataGoldenWithCursor(t *testing.T) {
	rr := httptest.NewRecorder()
	meta := metaFija()
	meta.NextCursor = "abc123"

	WriteData(rr, http.StatusOK, []int{1, 2}, meta)

	want := `{"data":[1,2],"meta":{"sim_time":"360-045-12:30","sim_time_seconds":31104000,"server_time":"2026-07-15T10:00:00Z","next_cursor":"abc123"}}`
	if got := rr.Body.String(); got != want {
		t.Fatalf("cuerpo:\n  got:  %s\n  want: %s", got, want)
	}
}

func TestWriteDataNilData(t *testing.T) {
	rr := httptest.NewRecorder()
	WriteData(rr, http.StatusCreated, nil, metaFija())

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, quiero 201", rr.Code)
	}
	want := `{"data":null,"meta":{"sim_time":"360-045-12:30","sim_time_seconds":31104000,"server_time":"2026-07-15T10:00:00Z"}}`
	if got := rr.Body.String(); got != want {
		t.Fatalf("cuerpo:\n  got:  %s\n  want: %s", got, want)
	}
}

func TestWriteDataUnserializableFallsBackToInternal(t *testing.T) {
	rr := httptest.NewRecorder()
	WriteData(rr, http.StatusOK, make(chan int), metaFija()) // chan no es serializable

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, quiero 500", rr.Code)
	}
	want := `{"error":{"code":"INTERNAL","message":"error interno serializando la respuesta"}}`
	if got := rr.Body.String(); got != want {
		t.Fatalf("cuerpo:\n  got:  %s\n  want: %s", got, want)
	}
}

func TestWriteErrorGolden(t *testing.T) {
	rr := httptest.NewRecorder()
	WriteError(rr, http.StatusUnprocessableEntity, "INSUFFICIENT_COLLATERAL",
		"La garantía disponible no cubre la publicación solicitada",
		map[string]any{"required": "1000", "available": "740"})

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, quiero 422", rr.Code)
	}
	// json.Marshal ordena las claves de map alfabéticamente: golden estable.
	want := `{"error":{"code":"INSUFFICIENT_COLLATERAL","message":"La garantía disponible no cubre la publicación solicitada","details":{"available":"740","required":"1000"}}}`
	if got := rr.Body.String(); got != want {
		t.Fatalf("cuerpo:\n  got:  %s\n  want: %s", got, want)
	}
}

func TestWriteErrorGoldenSinDetails(t *testing.T) {
	rr := httptest.NewRecorder()
	WriteError(rr, http.StatusNotFound, CodeNotFound, "entidad inexistente", nil)

	want := `{"error":{"code":"NOT_FOUND","message":"entidad inexistente"}}`
	if got := rr.Body.String(); got != want {
		t.Fatalf("cuerpo:\n  got:  %s\n  want: %s", got, want)
	}
}
