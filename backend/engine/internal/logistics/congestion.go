package logistics

// Congestión EMA de segmentos: cada 60 ticks se recalcula
// ema = clamp(0.3 × (vehículos_en_segmento / (capacidad/60)) + 0.7 × ema, 1, 10).
// Es el peso que consume el pathfinding y la velocidad efectiva.

import (
	"context"

	"github.com/jackc/pgx/v5"

	"imperio/engine/internal/db"
)

func (p *Processor) RunCongestion(ctx context.Context, simNow int64) {
	err := db.RunSerializable(ctx, p.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE world.link_segments ls
			   SET congestion_ema = LEAST(10, GREATEST(1,
			           0.3 * (sub.cnt / (sub.cap / 60.0)) + 0.7 * ls.congestion_ema)),
			       updated_at_sim = $1
			  FROM (
			       SELECT s.id, nl.capacity_per_hour::numeric AS cap, COALESCE(v.cnt, 0)::numeric AS cnt
			         FROM world.link_segments s
			         JOIN world.network_links nl ON nl.id = s.link_id
			         LEFT JOIN (SELECT on_segment_id, count(*) AS cnt
			                      FROM world.vehicles
			                     WHERE on_segment_id IS NOT NULL
			                     GROUP BY on_segment_id) v ON v.on_segment_id = s.id
			       ) sub
			 WHERE sub.id = ls.id`, simNow)
		return err
	})
	if err != nil {
		p.Log.Error("logistics: congestión", "err", err)
	}
}
