// Package idempotency implementa la cabecera Idempotency-Key del contrato
// OpenAPI v1.2.0: reintentos seguros de comandos que mueven valor. El primer
// intento de un comando mutante se ejecuta y su respuesta queda almacenada
// (public.idempotency_keys, migración 0008) acotada por cuenta autenticada;
// todo reintento con la misma clave reproduce esa misma respuesta — nunca hay
// doble ejecución.
//
// El middleware envuelve endpoints mutantes YA autenticados (por dentro de
// RequireAuth): resuelve la cuenta del contexto vía AccountResolver, que
// cablea el composition root — este paquete no conoce al módulo auth.
//
// Semántica:
//   - Sin cabecera Idempotency-Key: passthrough (sin coste).
//   - Clave no UUID: 400 VALIDATION_ERROR sin ejecutar el handler.
//   - Hit: reproduce la respuesta almacenada (status, Content-Type, cuerpo)
//     con la cabecera Idempotency-Replayed: true.
//   - Miss: ejecuta capturando la respuesta y la persiste SOLO si el status
//     es < 500 (un error interno debe poder reintentarse de verdad).
//   - Carrera (misma clave concurrente): INSERT ... ON CONFLICT DO NOTHING
//     tras ejecutar; quien pierde la carrera devuelve la respuesta almacenada
//     por el ganador, de modo que todos los clientes ven UNA sola respuesta.
package idempotency

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/platform/httpx"
)

// Cabeceras del protocolo de idempotencia.
const (
	// Header es la cabecera de petición con la clave (uuid) del cliente.
	Header = "Idempotency-Key"
	// HeaderReplayed marca ("true") una respuesta reproducida del almacén.
	HeaderReplayed = "Idempotency-Replayed"
)

// codeUnauthorized es el código UNAUTHORIZED del contrato; se emite en el
// rechazo seguro cuando el middleware se monta sin RequireAuth por fuera.
const codeUnauthorized = "UNAUTHORIZED"

// AccountResolver resuelve la cuenta autenticada de la petición desde el
// contexto. La implementa el composition root sobre el Principal de auth
// (misma forma que ledger.Identity): los módulos no se importan entre sí.
type AccountResolver interface {
	// AccountID devuelve la cuenta autenticada, o false si no la hay.
	AccountID(ctx context.Context) (uuid.UUID, bool)
}

// Middleware aplica el protocolo Idempotency-Key sobre endpoints mutantes
// autenticados. Lo construye NewMiddleware y lo monta el composition root
// con Wrap.
type Middleware struct {
	store    store
	resolver AccountResolver
	logger   *slog.Logger
	hits     prometheus.Counter
}

// NewMiddleware construye el middleware sobre el pool de la plataforma.
// reg registra la métrica ii_idempotency_hits_total; nil la deja sin
// instrumentar (tests). logger es obligatorio.
func NewMiddleware(pool *pgxpool.Pool, resolver AccountResolver, reg prometheus.Registerer, logger *slog.Logger) *Middleware {
	return newMiddleware(&pgStore{db: pool}, resolver, reg, logger)
}

// newMiddleware permite inyectar el almacén en tests unitarios.
func newMiddleware(st store, resolver AccountResolver, reg prometheus.Registerer, logger *slog.Logger) *Middleware {
	hits := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "ii_idempotency_hits_total",
		Help: "Total de respuestas reproducidas desde el almacén de idempotencia (hits de Idempotency-Key).",
	})
	if reg != nil {
		reg.MustRegister(hits)
	}
	return &Middleware{store: st, resolver: resolver, logger: logger, hits: hits}
}

// Wrap decora next con el protocolo de idempotencia. Las peticiones sin
// cabecera Idempotency-Key pasan intactas.
func (m *Middleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.Header.Get(Header)
		if raw == "" {
			next.ServeHTTP(w, r)
			return
		}
		key, err := uuid.Parse(raw)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, httpx.CodeValidationError,
				"cabecera Idempotency-Key inválida: debe ser un UUID", nil)
			return
		}
		account, ok := m.resolver.AccountID(r.Context())
		if !ok {
			// Montado por dentro de RequireAuth esto no ocurre; sin cuenta no
			// hay ámbito de clave posible: rechazo seguro (como RateLimitAPI).
			httpx.WriteError(w, http.StatusUnauthorized, codeUnauthorized,
				"credenciales o sesión inválidas", nil)
			return
		}

		// Hit: la clave ya tiene respuesta almacenada para esta cuenta.
		resp, found, err := m.store.find(r.Context(), key, account)
		if err != nil {
			// Si el cliente ya se fue no hay nada que garantizar ni nadie a
			// quien responder: no es un fallo del almacén y no debe contar
			// como 5xx.
			if httpx.WriteClientGone(w, r, m.logger, err, "consultando el almacén de idempotencia") {
				return
			}
			// Sin lectura del almacén no se puede garantizar la no-ejecución
			// doble: no ejecutar es lo único seguro.
			m.logError(r, "error consultando el almacén de idempotencia", err)
			httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal,
				"error interno del servidor", nil)
			return
		}
		if found {
			m.replay(w, resp)
			return
		}

		// Miss: ejecuta capturando la respuesta completa en memoria.
		rec := newRecorder()
		next.ServeHTTP(rec, r)

		if rec.status() >= http.StatusInternalServerError {
			// Los 5xx no se persisten: el reintento debe ejecutar de verdad.
			rec.flush(w)
			return
		}
		inserted, err := m.store.save(r.Context(), key, account, r.Method, r.URL.Path, rec.stored())
		if err != nil {
			// La operación YA se ejecutó: perder el registro de idempotencia
			// no debe convertir un éxito en error. Se entrega la respuesta y
			// queda constancia en el log.
			m.logError(r, "no se pudo persistir la clave de idempotencia", err)
			rec.flush(w)
			return
		}
		if !inserted {
			// Perdió la carrera contra una petición concurrente con la misma
			// clave: todos los clientes reciben la respuesta del ganador.
			winner, found, err := m.store.find(r.Context(), key, account)
			if err != nil || !found {
				// Insólito (p. ej. purga entre el INSERT y esta lectura):
				// mejor la respuesta propia que un error tras ejecutar.
				m.logError(r, "no se pudo recuperar la respuesta almacenada tras perder la carrera", err)
				rec.flush(w)
				return
			}
			m.replay(w, winner)
			return
		}
		rec.flush(w)
	})
}

// replay reproduce una respuesta almacenada y contabiliza el hit.
func (m *Middleware) replay(w http.ResponseWriter, resp storedResponse) {
	m.hits.Inc()
	if resp.ContentType != "" {
		w.Header().Set("Content-Type", resp.ContentType)
	}
	w.Header().Set(HeaderReplayed, "true")
	w.WriteHeader(resp.Status)
	_, _ = w.Write(resp.Body)
}

// logError registra un fallo del almacén con el request id de la petición. Si
// el error solo refleja que el cliente ya no está (estos sitios se alcanzan con
// la operación YA ejecutada, cuando no queda respuesta que dar) baja a WARN:
// el log de errores debe quedarse con los fallos reales del servicio.
func (m *Middleware) logError(r *http.Request, msg string, err error) {
	m.logger.LogAttrs(r.Context(), httpx.LogLevelFor(r, err), msg,
		slog.String("request_id", httpx.RequestIDFromContext(r.Context())),
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.Any("error", err),
	)
}
