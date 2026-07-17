package httpx

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/google/uuid"

	"github.com/lokiteitor/global-market/backend/internal/platform/logging"
)

// HeaderRequestID es la cabecera de correlación de peticiones.
const HeaderRequestID = "X-Request-Id"

// Middleware es un decorador estándar de http.Handler.
type Middleware func(http.Handler) http.Handler

// Chain aplica los middlewares sobre h en orden de lectura: el primero de la
// lista queda como el más externo.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// ctxKey es el tipo privado de las claves de contexto del paquete.
type ctxKey int

const ctxKeyRequestID ctxKey = iota

// RequestID asigna a cada petición un identificador de correlación UUIDv7:
// respeta un X-Request-Id entrante válido (UUID) y genera uno nuevo en caso
// contrario. El id queda en el contexto y en la cabecera de la respuesta.
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(HeaderRequestID)
			if _, err := uuid.Parse(id); err != nil {
				id = newRequestID()
			}
			w.Header().Set(HeaderRequestID, id)
			ctx := context.WithValue(r.Context(), ctxKeyRequestID, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequestIDFromContext devuelve el request id de la petición, o "" si el
// middleware RequestID no está en la cadena.
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(ctxKeyRequestID).(string)
	return id
}

// newRequestID genera un UUIDv7; si la fuente de entropía/reloj fallara
// (nunca esperado) degrada a UUIDv4 antes que dejar la petición sin id.
func newRequestID() string {
	if id, err := uuid.NewV7(); err == nil {
		return id.String()
	}
	return uuid.New().String()
}

// Recover captura panics del handler: loguea el stack con el request id y
// responde 500 INTERNAL con el envelope del contrato si la respuesta aún no
// había empezado a escribirse. http.ErrAbortHandler se propaga tal cual
// (protocolo de net/http para abortar la conexión).
func Recover(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := &observedWriter{ResponseWriter: w}
			defer func() {
				p := recover()
				if p == nil {
					return
				}
				if p == http.ErrAbortHandler {
					panic(p)
				}
				logging.WithRequestID(logger, RequestIDFromContext(r.Context())).LogAttrs(
					r.Context(), slog.LevelError, "panic recuperado en handler HTTP",
					slog.Any("panic", p),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.String("stack", string(debug.Stack())),
				)
				if !rec.wroteHeader {
					WriteError(rec, http.StatusInternalServerError, CodeInternal,
						"error interno del servidor", nil)
				}
			}()
			next.ServeHTTP(rec, r)
		})
	}
}

// AccessLog emite una línea estructurada por petición servida: método, ruta,
// status, duración y request id.
func AccessLog(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &observedWriter{ResponseWriter: w}
			next.ServeHTTP(rec, r)

			status := rec.status
			if status == 0 {
				status = http.StatusOK // Write sin WriteHeader explícito
			}
			logging.WithRequestID(logger, RequestIDFromContext(r.Context())).LogAttrs(
				r.Context(), slog.LevelInfo, "http_request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", status),
				slog.Duration("duration", time.Since(start)),
			)
		})
	}
}

// observedWriter registra si la respuesta empezó a escribirse y su status.
type observedWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *observedWriter) WriteHeader(status int) {
	if !w.wroteHeader {
		w.status = status
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *observedWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.status = http.StatusOK
		w.wroteHeader = true
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap permite a http.ResponseController alcanzar el writer original.
func (w *observedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
