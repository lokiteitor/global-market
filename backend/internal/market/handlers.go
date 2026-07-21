package market

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"github.com/google/uuid"

	"github.com/lokiteitor/global-market/backend/internal/platform/httpx"
	"github.com/lokiteitor/global-market/backend/internal/platform/logging"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// MetaSource construye los metadatos comunes (schema Meta del contrato:
// sim_time actual del mundo) de toda respuesta exitosa. Lo implementa el
// composition root con el reloj de simulación (mismo patrón que ledger).
type MetaSource interface {
	Meta(ctx context.Context) httpx.Meta
}

// Reader es la superficie de lectura que consumen los handlers; la implementa
// *Service.
type Reader interface {
	ListCandles(ctx context.Context, f CandleFilter) ([]Candle, error)
}

var _ Reader = (*Service)(nil)

// Handlers sirve el endpoint /market/ohlc del contrato OpenAPI.
type Handlers struct {
	reader Reader
	meta   MetaSource
	logger *slog.Logger
}

// NewHandlers construye los handlers del módulo market.
func NewHandlers(reader Reader, meta MetaSource, logger *slog.Logger) *Handlers {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handlers{reader: reader, meta: meta, logger: logger}
}

// Register monta las rutas del contrato en el mux del gateway (sin prefijo: lo
// añade el composition root, como el resto de módulos).
func (h *Handlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /market/ohlc", h.getOhlc)
}

// ─── GET /market/ohlc ────────────────────────────────────────────────────────

func (h *Handlers) getOhlc(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	// product_id es obligatorio (contrato): ausente o no-UUID → 400.
	rawProduct := q.Get("product_id")
	if rawProduct == "" {
		writeValidationError(w, "product_id", "es obligatorio")
		return
	}
	productID, err := uuid.Parse(rawProduct)
	if err != nil {
		writeValidationError(w, "product_id", "no es un UUID válido")
		return
	}

	filter := CandleFilter{ProductID: productID}

	if raw := q.Get("region_id"); raw != "" {
		regionID, err := uuid.Parse(raw)
		if err != nil {
			writeValidationError(w, "region_id", "no es un UUID válido")
			return
		}
		filter.RegionID = &regionID
	}

	// bucket_sim_secs es informativo (default 3600 en el contrato): se valida
	// como entero > 0 pero NO re-agrega. Cada vela devuelve su propio
	// bucket_sim_secs (la granularidad realmente almacenada).
	if raw := q.Get("bucket_sim_secs"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeValidationError(w, "bucket_sim_secs", "debe ser un entero")
			return
		}
		if n < 1 {
			writeValidationError(w, "bucket_sim_secs", "debe ser >= 1")
			return
		}
	}

	if filter.FromSim, err = parseSimTime(q, "from_sim"); err != nil {
		writeValidationError(w, "from_sim", err.Error())
		return
	}
	if filter.ToSim, err = parseSimTime(q, "to_sim"); err != nil {
		writeValidationError(w, "to_sim", err.Error())
		return
	}
	limit, err := parseLimit(q)
	if err != nil {
		writeValidationError(w, "limit", err.Error())
		return
	}
	filter.Limit = limit

	candles, err := h.reader.ListCandles(r.Context(), filter)
	if err != nil {
		// Petición abortada por el cliente o plazo agotado: no es un fallo
		// del servicio y no debe contarse como 5xx ni loguearse como ERROR.
		if httpx.WriteClientGone(w, r, h.logger, err, "listando velas OHLC") {
			return
		}
		logging.WithRequestID(h.logger, httpx.RequestIDFromContext(r.Context())).LogAttrs(
			r.Context(), slog.LevelError, "error listando velas OHLC",
			slog.String("error", err.Error()),
		)
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, "error interno del servidor", nil)
		return
	}

	data := make([]candleJSON, len(candles))
	for i, c := range candles {
		data[i] = toCandleJSON(c)
	}
	httpx.WriteData(w, http.StatusOK, data, h.meta.Meta(r.Context()))
}

// ─── Parsing y errores ───────────────────────────────────────────────────────

// writeValidationError responde 400 VALIDATION_ERROR con el campo culpable
// (mismo formato que el resto de módulos).
func writeValidationError(w http.ResponseWriter, field, reason string) {
	httpx.WriteError(w, http.StatusBadRequest, httpx.CodeValidationError,
		fmt.Sprintf("parámetro %s inválido: %s", field, reason),
		map[string]any{"field": field})
}

// parseLimit interpreta el query param limit del contrato (entero 1..200;
// ausente = 0, el servicio aplica el default 50).
func parseLimit(q url.Values) (int, error) {
	raw := q.Get("limit")
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, errors.New("debe ser un entero")
	}
	if n < 1 || n > MaxPageLimit {
		return 0, fmt.Errorf("debe estar entre 1 y %d", MaxPageLimit)
	}
	return n, nil
}

// parseSimTime interpreta un query param de sim-time (int64 >= 0; ausente = nil).
func parseSimTime(q url.Values, name string) (*simtime.SimTime, error) {
	raw := q.Get(name)
	if raw == "" {
		return nil, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return nil, errors.New("debe ser un entero de sim-time")
	}
	if n < 0 {
		return nil, errors.New("debe ser >= 0")
	}
	t := simtime.SimTime(n)
	return &t, nil
}

// ─── DTO del contrato (schema OhlcCandle; dinero/stock como string) ─────────

// candleJSON es el schema OhlcCandle del contrato OpenAPI: dinero y volumen
// como string de punto fijo (jamás float), sim-time y contadores como enteros.
type candleJSON struct {
	ProductID      string `json:"product_id"`
	RegionID       string `json:"region_id"`
	BucketStartSim int64  `json:"bucket_start_sim"`
	BucketSimSecs  int64  `json:"bucket_sim_secs"`
	OpenPrice      string `json:"open_price"`
	HighPrice      string `json:"high_price"`
	LowPrice       string `json:"low_price"`
	ClosePrice     string `json:"close_price"`
	Volume         string `json:"volume"`
	ContractCount  int32  `json:"contract_count"`
}

func toCandleJSON(c Candle) candleJSON {
	return candleJSON{
		ProductID:      c.ProductID.String(),
		RegionID:       c.RegionID.String(),
		BucketStartSim: int64(c.BucketStartSim),
		BucketSimSecs:  c.BucketSimSecs,
		OpenPrice:      strconv.FormatInt(c.OpenPrice, 10),
		HighPrice:      strconv.FormatInt(c.HighPrice, 10),
		LowPrice:       strconv.FormatInt(c.LowPrice, 10),
		ClosePrice:     strconv.FormatInt(c.ClosePrice, 10),
		Volume:         strconv.FormatInt(c.Volume, 10),
		ContractCount:  c.ContractCount,
	}
}
