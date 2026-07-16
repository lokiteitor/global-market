// Package db ofrece el pool de conexiones y el helper de transacciones
// SERIALIZABLE con reintento ante fallos de serialización (SQLSTATE 40001).
package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const maxRetries = 5

func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("db: parse config: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return pool, nil
}

// RunSerializable ejecuta fn dentro de una transacción SERIALIZABLE,
// reintentando hasta maxRetries veces ante 40001 (serialization_failure) o
// 40P01 (deadlock_detected). Cada unidad de trabajo del motor usa este helper:
// un fallo definitivo se registra y NO bloquea al resto de unidades.
func RunSerializable(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			// Backoff corto y creciente para desescalar la contención.
			select {
			case <-time.After(time.Duration(attempt*25) * time.Millisecond):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
		if err != nil {
			return fmt.Errorf("db: begin: %w", err)
		}
		err = fn(tx)
		if err == nil {
			err = tx.Commit(ctx)
			if err == nil {
				return nil
			}
		} else {
			_ = tx.Rollback(ctx)
		}
		if !isRetryable(err) {
			return err
		}
		lastErr = err
	}
	return fmt.Errorf("db: agotados %d reintentos de serialización: %w", maxRetries, lastErr)
}

func isRetryable(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "40001" || pgErr.Code == "40P01"
	}
	return false
}
