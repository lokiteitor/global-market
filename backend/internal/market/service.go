package market

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lokiteitor/global-market/backend/internal/market/sqlcgen"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// Service es el lado de LECTURA del módulo market: sirve el historial OHLC
// (contrato GET /market/ohlc) desde analytics.market_ohlc, tal cual está
// almacenado, sin re-agregar. La construcción de velas la hace el Aggregator
// por el outbox; este servicio no escribe.
type Service struct {
	q    *sqlcgen.Queries
	opts Options
}

// NewService construye el servicio de lectura sobre el pool compartido de la
// plataforma. Aplica los defaults del módulo si opts trae valores no válidos
// (mismo criterio que ledger.NewService).
func NewService(pool *pgxpool.Pool, opts Options) *Service {
	if opts.OhlcBucketSimSeconds <= 0 {
		opts.OhlcBucketSimSeconds = DefaultOhlcBucketSimSeconds
	}
	if opts.QueryTimeout <= 0 {
		opts.QueryTimeout = DefaultQueryTimeout
	}
	return &Service{q: sqlcgen.New(pool), opts: opts}
}

// ListCandles devuelve la serie OHLC que satisface el filtro (producto
// obligatorio; región y rango de sim-time opcionales), en orden cronológico y
// acotada por el límite del contrato. No re-agrega: cada vela conserva su
// bucket_sim_secs almacenado.
func (s *Service) ListCandles(ctx context.Context, f CandleFilter) ([]Candle, error) {
	ctx, cancel := context.WithTimeout(ctx, s.opts.QueryTimeout)
	defer cancel()

	rows, err := s.q.ListOhlcCandles(ctx, sqlcgen.ListOhlcCandlesParams{
		ProductID: f.ProductID,
		RegionID:  f.RegionID,
		FromSim:   (*int64)(f.FromSim),
		ToSim:     (*int64)(f.ToSim),
		PageLimit: normalizeLimit(f.Limit),
	})
	if err != nil {
		return nil, fmt.Errorf("market: listando velas OHLC del producto %s: %w", f.ProductID, err)
	}

	candles := make([]Candle, len(rows))
	for i, r := range rows {
		candles[i] = Candle{
			ProductID:      r.ProductID,
			RegionID:       r.RegionID,
			BucketStartSim: simtime.SimTime(r.BucketStartSim),
			BucketSimSecs:  r.BucketSimSecs,
			OpenPrice:      r.OpenPrice,
			HighPrice:      r.HighPrice,
			LowPrice:       r.LowPrice,
			ClosePrice:     r.ClosePrice,
			Volume:         r.Volume,
			ContractCount:  r.ContractCount,
		}
	}
	return candles, nil
}

// normalizeLimit aplica el default y el máximo del contrato (50/200).
func normalizeLimit(limit int) int32 {
	switch {
	case limit <= 0:
		return DefaultPageLimit
	case limit > MaxPageLimit:
		return MaxPageLimit
	default:
		return int32(limit)
	}
}
