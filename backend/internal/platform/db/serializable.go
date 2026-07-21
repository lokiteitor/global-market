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
//
// El presupuesto está dimensionado para la CONTENCIÓN REAL medida en el
// Incremento 9, no para el caso ideal: bajo 150 bots concurrentes PostgreSQL
// emite miles de 40001 por corrida sobre caminos que escriben en tablas
// append-only con clave UUIDv7 (ledger.entries, outbox.events). Casi todos son
// conflictos FALSOS —los predicate locks de SSI son de PÁGINA, y las claves
// monótonas concentran cada inserción concurrente en la misma página derecha
// del índice—, así que reejecutar SIEMPRE acaba funcionando. Con 5 reintentos
// y un techo de 250 ms el presupuesto total era de ~310 ms y la cola se
// agotaba; con 10 y 500 ms la ventana peor caso ronda los 2,6 s, que sigue
// siendo mucho menos que fallar una operación perfectamente reintentable.
const (
	maxTxRetries  = 10
	txBackoffBase = 10 * time.Millisecond
	txBackoffCap  = 500 * time.Millisecond
)

// Métricas de transacciones del paquete (no hay registro global, ADR de
// observabilidad): cada binario las registra en su registry con
// RegisterTxMetrics.
var (
	// serializationRetries cuenta los reintentos ejecutados por RunSerializable.
	serializationRetries = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "ii_tx_serialization_retries_total",
		Help: "Total de reintentos de transacciones SERIALIZABLE por conflicto de serialización o deadlock (SQLSTATE 40001/40P01).",
	})
	// serializationExhausted cuenta las transacciones que agotaron el
	// presupuesto de reintentos y se devolvieron al llamante como
	// SerializationError. Es el disparador MEDIDO de saturación por contención
	// (SAD §13): a diferencia de los reintentos —que son ruido normal bajo
	// carga— cada incremento aquí es una operación que el usuario no pudo
	// completar.
	serializationExhausted = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "ii_tx_serialization_exhausted_total",
		Help: "Total de transacciones SERIALIZABLE que agotaron su presupuesto de reintentos y fallaron con un error reintentable hacia el cliente.",
	})
)

// RegisterTxMetrics registra las métricas de transacciones del paquete en el
// registry del binario. Idempotente: registrarlas dos veces es inocuo.
func RegisterTxMetrics(reg prometheus.Registerer) {
	for _, c := range []prometheus.Collector{serializationRetries, serializationExhausted} {
		if err := reg.Register(c); err != nil {
			var already prometheus.AlreadyRegisteredError
			if !errors.As(err, &already) {
				panic(err)
			}
		}
	}
}

// ErrSerializationExhausted marca el agotamiento del presupuesto de reintentos
// de una transacción SERIALIZABLE. La transacción se revirtió ENTERA (no dejó
// ningún efecto), así que la operación es reintentable tal cual por el cliente:
// las capas HTTP deben traducirlo a una respuesta reintentable con Retry-After
// y NUNCA a un 500 INTERNAL opaco.
var ErrSerializationExhausted = errors.New("db: presupuesto de reintentos de la transacción serializable agotado")

// SerializationError es el error tipado que devuelve RunSerializable al agotar
// el presupuesto. Envuelve a la vez ErrSerializationExhausted (para el mapeo de
// las capas superiores) y el último error de PostgreSQL (para el diagnóstico).
type SerializationError struct {
	// Attempts son las ejecuciones de fn realizadas (1 + reintentos).
	Attempts int
	// Elapsed es el tiempo total consumido por el presupuesto.
	Elapsed time.Duration
	// Err es el último 40001/40P01 observado.
	Err error
}

func (e *SerializationError) Error() string {
	return fmt.Sprintf("db: transacción serializable agotada tras %d intentos en %s: %v",
		e.Attempts, e.Elapsed.Round(time.Millisecond), e.Err)
}

// Unwrap expone las dos causas: el centinela del agotamiento y el error de
// PostgreSQL subyacente.
func (e *SerializationError) Unwrap() []error { return []error{ErrSerializationExhausted, e.Err} }

// RetryAfter es la espera recomendada antes de reintentar la operación desde
// el cliente: un múltiplo del techo del backoff, suficiente para que la ráfaga
// de escrituras que provocó el conflicto haya drenado.
func (e *SerializationError) RetryAfter() time.Duration { return 2 * txBackoffCap }

// RunSerializable ejecuta fn dentro de una transacción SERIALIZABLE (regla de
// oro GDD 18.3: toda operación que mueve valor). Confirma si fn devuelve nil
// y revierte en caso contrario. Ante un fallo de serialización o deadlock
// (SQLSTATE 40001/40P01, también si emerge en el COMMIT) reejecuta la
// transacción completa hasta maxTxRetries veces con backoff exponencial y
// jitter, contando cada reintento en ii_tx_serialization_retries_total.
//
// Si el presupuesto se agota —o si el plazo restante del contexto ya no da
// para otro intento— devuelve un *SerializationError (errors.Is con
// ErrSerializationExhausted) y suma a ii_tx_serialization_exhausted_total: la
// operación NO se aplicó y es reintentable tal cual, así que la capa HTTP debe
// responder algo reintentable con Retry-After y no un 500 INTERNAL.
//
// fn debe ser reejecutable: sin efectos observables fuera de la transacción
// (los efectos externos van al outbox, dentro de la misma transacción).
func RunSerializable(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	started := time.Now()
	for attempt := 0; ; attempt++ {
		err := runSerializableOnce(ctx, pool, fn)
		switch {
		case err == nil:
			return nil
		case !retryableTxError(err):
			return err
		case attempt >= maxTxRetries:
			return exhausted(attempt+1, started, err)
		}
		serializationRetries.Inc()
		delay := backoffDelay(attempt)
		// Dormir más allá del plazo de la petición solo cambia un error
		// reintentable y explicable por un context.DeadlineExceeded opaco:
		// mejor rendirse mientras aún queda tiempo de responderlo.
		if !budgetAllows(ctx, delay) {
			return exhausted(attempt+1, started, err)
		}
		if werr := waitBackoff(ctx, delay); werr != nil {
			return fmt.Errorf("db: reintento de transacción serializable interrumpido: %w (causa: %v)", werr, err)
		}
	}
}

// exhausted construye el error tipado del presupuesto agotado y lo contabiliza.
func exhausted(attempts int, started time.Time, err error) error {
	serializationExhausted.Inc()
	return &SerializationError{Attempts: attempts, Elapsed: time.Since(started), Err: err}
}

// budgetAllows informa de si el contexto tiene plazo suficiente para esperar
// delay y volver a intentarlo. Sin deadline (trabajos de fondo) siempre cede.
func budgetAllows(ctx context.Context, delay time.Duration) bool {
	deadline, ok := ctx.Deadline()
	if !ok {
		return true
	}
	return time.Until(deadline) > delay
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

// waitBackoff duerme delay respetando la cancelación del contexto.
func waitBackoff(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
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
