package db

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// SQLSTATE que PostgreSQL emite cuando una transacción concurrente obliga a
// repetir la nuestra: fallo de serialización (SSI) y deadlock. Ambos son
// transitorios por definición y la respuesta correcta es reejecutar la
// transacción completa desde el principio.
const (
	sqlstateSerializationFailure = "40001"
	sqlstateDeadlockDetected     = "40P01"
)

// Parámetros del reintento: hasta maxTxRetries reejecuciones con backoff
// exponencial y jitter, para que las transacciones en conflicto no vuelvan a
// chocar en fase.
const (
	maxTxRetries  = 5
	txBackoffBase = 10 * time.Millisecond
	txBackoffCap  = 250 * time.Millisecond
)

// serializationRetries cuenta los reintentos ejecutados por RunSerializable.
// Es del paquete (no hay registro global, ADR de observabilidad): cada
// binario lo registra en su registry con RegisterTxMetrics.
var serializationRetries = prometheus.NewCounter(prometheus.CounterOpts{
	Name: "ii_tx_serialization_retries_total",
	Help: "Total de reintentos de transacciones SERIALIZABLE por conflicto de serialización o deadlock (SQLSTATE 40001/40P01).",
})

// RegisterTxMetrics registra las métricas de transacciones del paquete en el
// registry del binario. Idempotente: registrarlas dos veces es inocuo.
func RegisterTxMetrics(reg prometheus.Registerer) {
	if err := reg.Register(serializationRetries); err != nil {
		var already prometheus.AlreadyRegisteredError
		if !errors.As(err, &already) {
			panic(err)
		}
	}
}

// RunSerializable ejecuta fn dentro de una transacción SERIALIZABLE (regla de
// oro GDD 18.3: toda operación que mueve valor). Confirma si fn devuelve nil
// y revierte en caso contrario. Ante un fallo de serialización o deadlock
// (SQLSTATE 40001/40P01, también si emerge en el COMMIT) reejecuta la
// transacción completa hasta maxTxRetries veces con backoff exponencial y
// jitter, contando cada reintento en ii_tx_serialization_retries_total.
//
// fn debe ser reejecutable: sin efectos observables fuera de la transacción
// (los efectos externos van al outbox, dentro de la misma transacción).
func RunSerializable(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	for attempt := 0; ; attempt++ {
		err := runSerializableOnce(ctx, pool, fn)
		switch {
		case err == nil:
			return nil
		case !retryableTxError(err):
			return err
		case attempt >= maxTxRetries:
			return fmt.Errorf("db: transacción serializable agotó los %d reintentos: %w", maxTxRetries, err)
		}
		serializationRetries.Inc()
		if werr := waitBackoff(ctx, attempt); werr != nil {
			return fmt.Errorf("db: reintento de transacción serializable interrumpido: %w (causa: %v)", werr, err)
		}
	}
}

// runSerializableOnce abre la transacción, ejecuta fn y confirma. El rollback
// diferido tras un Commit exitoso devuelve ErrTxClosed: inocuo.
func runSerializableOnce(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return fmt.Errorf("db: abriendo la transacción serializable: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck

	if err := fn(tx); err != nil {
		return err
	}
	// Los triggers diferidos (p. ej. doble entrada del ledger) y buena parte
	// de los fallos de serialización se evalúan aquí.
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("db: confirmando la transacción serializable: %w", err)
	}
	return nil
}

// retryableTxError reconoce los SQLSTATE transitorios en cualquier punto de
// la cadena de errores.
func retryableTxError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == sqlstateSerializationFailure || pgErr.Code == sqlstateDeadlockDetected
}

// waitBackoff duerme el backoff del reintento attempt (desde 0) respetando la
// cancelación del contexto.
func waitBackoff(ctx context.Context, attempt int) error {
	timer := time.NewTimer(backoffDelay(attempt))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// backoffDelay calcula la espera del reintento attempt: ventana exponencial
// acotada por txBackoffCap, con la mitad fija y la mitad aleatoria ("equal
// jitter") — desincroniza transacciones en conflicto sin degenerar en esperas
// casi nulas.
func backoffDelay(attempt int) time.Duration {
	window := min(txBackoffBase<<attempt, txBackoffCap)
	return window/2 + rand.N(window/2+1)
}
