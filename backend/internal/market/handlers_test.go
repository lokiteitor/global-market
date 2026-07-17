package market

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/lokiteitor/global-market/backend/internal/platform/httpx"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// fakeReader implementa Reader capturando el filtro recibido.
type fakeReader struct {
	candles []Candle
	err     error
	got     CandleFilter
	calls   int
}

func (f *fakeReader) ListCandles(_ context.Context, filter CandleFilter) ([]Candle, error) {
	f.calls++
	f.got = filter
	return f.candles, f.err
}

// fixedMeta implementa MetaSource con un sim-time fijo.
type fixedMeta struct{}

func (fixedMeta) Meta(context.Context) httpx.Meta {
	return httpx.Meta{
		SimTime:        simtime.Format(3600),
		SimTimeSeconds: 3600,
		ServerTime:     time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

// serve monta los handlers y ejecuta la petición dada.
func serve(t *testing.T, reader Reader, target string) *httptest.ResponseRecorder {
	t.Helper()
	h := NewHandlers(reader, fixedMeta{}, nil)
	mux := http.NewServeMux()
	h.Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestGetOhlcHappyPath(t *testing.T) {
	product := uuid.Must(uuid.NewV7())
	region := uuid.Must(uuid.NewV7())
	reader := &fakeReader{candles: []Candle{{
		ProductID:      product,
		RegionID:       region,
		BucketStartSim: 3600,
		BucketSimSecs:  3600,
		OpenPrice:      1000,
		HighPrice:      1500,
		LowPrice:       800,
		ClosePrice:     1200,
		Volume:         180,
		ContractCount:  3,
	}}}

	rec := serve(t, reader, "/market/ohlc?product_id="+product.String()+
		"&region_id="+region.String()+"&from_sim=0&to_sim=7200&bucket_sim_secs=3600&limit=10")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, esperado 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}

	var resp struct {
		Data []candleJSON `json:"data"`
		Meta httpx.Meta   `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decodificando la respuesta: %v (body: %s)", err, rec.Body.String())
	}
	if len(resp.Data) != 1 {
		t.Fatalf("velas devueltas: %d, esperado 1", len(resp.Data))
	}
	c := resp.Data[0]
	// Dinero y volumen SIEMPRE como string de punto fijo (jamás float).
	if c.ProductID != product.String() || c.RegionID != region.String() ||
		c.BucketStartSim != 3600 || c.BucketSimSecs != 3600 ||
		c.OpenPrice != "1000" || c.HighPrice != "1500" || c.LowPrice != "800" ||
		c.ClosePrice != "1200" || c.Volume != "180" || c.ContractCount != 3 {
		t.Fatalf("vela serializada inesperada: %+v", c)
	}
	if resp.Meta.SimTimeSeconds != 3600 {
		t.Fatalf("meta.sim_time_seconds = %d, esperado 3600", resp.Meta.SimTimeSeconds)
	}

	// El filtro llegó al Reader tal cual (producto, región y rango).
	if reader.got.ProductID != product || reader.got.RegionID == nil || *reader.got.RegionID != region ||
		reader.got.FromSim == nil || *reader.got.FromSim != 0 ||
		reader.got.ToSim == nil || *reader.got.ToSim != 7200 || reader.got.Limit != 10 {
		t.Fatalf("filtro recibido inesperado: %+v", reader.got)
	}
}

func TestGetOhlcMoneyIsJSONString(t *testing.T) {
	product := uuid.Must(uuid.NewV7())
	reader := &fakeReader{candles: []Candle{{
		ProductID: product, RegionID: uuid.Must(uuid.NewV7()),
		BucketStartSim: 0, BucketSimSecs: 3600,
		OpenPrice: 42, HighPrice: 42, LowPrice: 42, ClosePrice: 42, Volume: 7, ContractCount: 1,
	}}}
	rec := serve(t, reader, "/market/ohlc?product_id="+product.String())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body: %s)", rec.Code, rec.Body.String())
	}
	// Comprobación textual: los importes viajan entrecomillados, nunca como
	// número JSON.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("json: %v", err)
	}
	var arr []map[string]json.RawMessage
	if err := json.Unmarshal(raw["data"], &arr); err != nil {
		t.Fatalf("json data: %v", err)
	}
	for _, field := range []string{"open_price", "high_price", "low_price", "close_price", "volume"} {
		if got := string(arr[0][field]); got != `"42"` && got != `"7"` {
			t.Fatalf("campo %s = %s, esperado string entrecomillado", field, got)
		}
	}
}

func TestGetOhlcValidation(t *testing.T) {
	product := uuid.Must(uuid.NewV7()).String()
	cases := []struct {
		name, target, field string
	}{
		{"product_id ausente", "/market/ohlc", "product_id"},
		{"product_id no uuid", "/market/ohlc?product_id=nope", "product_id"},
		{"region_id no uuid", "/market/ohlc?product_id=" + product + "&region_id=nope", "region_id"},
		{"bucket_sim_secs no entero", "/market/ohlc?product_id=" + product + "&bucket_sim_secs=x", "bucket_sim_secs"},
		{"bucket_sim_secs cero", "/market/ohlc?product_id=" + product + "&bucket_sim_secs=0", "bucket_sim_secs"},
		{"from_sim negativo", "/market/ohlc?product_id=" + product + "&from_sim=-1", "from_sim"},
		{"to_sim no entero", "/market/ohlc?product_id=" + product + "&to_sim=abc", "to_sim"},
		{"limit fuera de rango", "/market/ohlc?product_id=" + product + "&limit=999", "limit"},
		{"limit no entero", "/market/ohlc?product_id=" + product + "&limit=x", "limit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader := &fakeReader{}
			rec := serve(t, reader, tc.target)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, esperado 400 (body: %s)", rec.Code, rec.Body.String())
			}
			if reader.calls != 0 {
				t.Fatalf("el Reader no debe invocarse ante un parámetro inválido (calls=%d)", reader.calls)
			}
			var resp struct {
				Error struct {
					Code    string         `json:"code"`
					Details map[string]any `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("json: %v (body: %s)", err, rec.Body.String())
			}
			if resp.Error.Code != httpx.CodeValidationError {
				t.Fatalf("code = %q, esperado %q", resp.Error.Code, httpx.CodeValidationError)
			}
			if resp.Error.Details["field"] != tc.field {
				t.Fatalf("details.field = %v, esperado %q", resp.Error.Details["field"], tc.field)
			}
		})
	}
}

func TestGetOhlcEmptySeries(t *testing.T) {
	product := uuid.Must(uuid.NewV7()).String()
	rec := serve(t, &fakeReader{candles: nil}, "/market/ohlc?product_id="+product)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, esperado 200", rec.Code)
	}
	// data debe ser un array vacío, nunca null (contrato: array requerido).
	var resp struct {
		Data []candleJSON `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if resp.Data == nil || len(resp.Data) != 0 {
		t.Fatalf("data = %v, esperado []", resp.Data)
	}
}
