// Package db construye el pool de conexiones a PostgreSQL y su
// instrumentación Prometheus.
package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lokiteitor/global-market/backend/internal/platform/config"
)

// NewPool construye un *pgxpool.Pool a partir de la configuración. El pool
// es perezoso: no establece conexiones en el arranque, de modo que el binario
// levanta aunque la BD aún no responda (readyz refleja el estado real).
func NewPool(ctx context.Context, cfg config.Config) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("db: parseando %s: %w", config.EnvDatabaseURL, err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("db: creando el pool: %w", err)
	}
	return pool, nil
}

// Ping verifica la conectividad con la base de datos. El deadline lo aporta
// el contexto del llamador (p. ej. el timeout del handler de readyz).
func Ping(ctx context.Context, pool *pgxpool.Pool) error {
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("db: ping: %w", err)
	}
	return nil
}
