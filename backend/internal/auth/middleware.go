package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lokiteitor/global-market/backend/internal/platform/httpx"
)

// ctxKey es el tipo privado de las claves de contexto del paquete.
type ctxKey int

const ctxKeyPrincipal ctxKey = iota

// Principal es la identidad autenticada de una petición: la cuenta y la
// sesión que la respalda.
type Principal struct {
	Account   Account
	SessionID uuid.UUID
}

// ContextWithPrincipal inyecta la identidad autenticada en el contexto
// (lo hace RequireAuth; exportado para tests y composición).
func ContextWithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKeyPrincipal, p)
}

// PrincipalFromContext devuelve la identidad autenticada de la petición.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKeyPrincipal).(Principal)
	return p, ok
}

// FromContext devuelve la cuenta autenticada de la petición.
func FromContext(ctx context.Context) (Account, bool) {
	p, ok := PrincipalFromContext(ctx)
	return p.Account, ok
}

// touchTimeout acota la actualización asíncrona de last_seen_at.
const touchTimeout = 5 * time.Second

// Middleware agrupa los decoradores HTTP de autenticación y rate limiting
// de la API. El composition root lo monta sobre las rutas protegidas.
type Middleware struct {
	svc        *Service
	apiLimiter *Limiter
	metrics    *Metrics
	logger     *slog.Logger
	// now es inyectable en tests; time.Now en producción.
	now func() time.Time
}

// NewMiddleware construye los middlewares del módulo. El limiter de API es
// por cuenta autenticada con tasa opts.APIRPS y ráfaga opts.APIBurst,
// idéntico para humanos y bots (GDD §9). metrics puede ser nil (sin
// instrumentación, p. ej. en tests).
func NewMiddleware(svc *Service, opts Options, metrics *Metrics, logger *slog.Logger) *Middleware {
	return &Middleware{
		svc:        svc,
		apiLimiter: NewLimiter(opts.APIRPS, opts.APIBurst),
		metrics:    metrics,
		logger:     logger,
		now:        time.Now,
	}
}

// RequireAuth exige un token bearer válido: lo resuelve a su sesión vigente,
// inyecta el Principal en el contexto (FromContext) y dispara el touch
// throttled de last_seen_at en segundo plano. Sin token o con sesión
// inválida/expirada responde 401 UNAUTHORIZED con el envelope del contrato.
func (m *Middleware) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		if !ok {
			writeUnauthorized(w)
			return
		}
		sess, acc, err := m.svc.Authenticate(r.Context(), token)
		if err != nil {
			if errors.Is(err, ErrUnauthorized) {
				writeUnauthorized(w)
				return
			}
			// El cliente que aborta la petición mientras se resuelve su sesión
			// no es un fallo del servicio: RequireAuth está en el camino de
			// TODAS las peticiones autenticadas, así que contarlo como 5xx
			// convertiría cada desconexión masiva de bots en un pico de errores
			// del servidor (SAD §13: los disparadores se miden, no se adivinan).
			if httpx.WriteClientGone(w, r, m.logger, err, "autenticando sesión") {
				return
			}
			m.logger.LogAttrs(r.Context(), slog.LevelError, "error autenticando sesión",
				slog.Any("error", err))
			httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal,
				"error interno del servidor", nil)
			return
		}
		m.touchAsync(r.Context(), sess)
		ctx := ContextWithPrincipal(r.Context(), Principal{Account: acc, SessionID: sess.ID})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// touchAsync actualiza last_seen_at fuera del camino de la petición, solo si
// el valor cargado ya supera touchInterval (el UPDATE repite la guarda en
// SQL, así que las carreras entre peticiones concurrentes son inocuas).
func (m *Middleware) touchAsync(ctx context.Context, sess Session) {
	if m.now().Sub(sess.LastSeenAt) < touchInterval {
		return
	}
	// Contexto desacoplado de la cancelación de la petición pero conservando
	// sus valores (request_id para correlación en logs).
	bg := context.WithoutCancel(ctx)
	go func() {
		tctx, cancel := context.WithTimeout(bg, touchTimeout)
		defer cancel()
		if err := m.svc.repo.TouchSessionLastSeen(tctx, sess.ID); err != nil {
			m.logger.LogAttrs(tctx, slog.LevelWarn, "no se pudo actualizar last_seen_at",
				slog.String("session_id", sess.ID.String()),
				slog.Any("error", err))
		}
	}()
}

// RateLimitAPI aplica el token bucket por cuenta autenticada. Debe montarse
// por dentro de RequireAuth (necesita el Principal del contexto). Al superar
// el límite responde 429 RATE_LIMITED con Retry-After en segundos.
func (m *Middleware) RateLimitAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFromContext(r.Context())
		if !ok {
			// Sin RequireAuth por fuera no hay clave de cuenta: rechazo seguro.
			writeUnauthorized(w)
			return
		}
		if allowed, retryAfter := m.apiLimiter.Allow(p.Account.ID.String()); !allowed {
			m.metrics.RateLimited(ScopeAPI)
			writeRateLimited(w, retryAfter)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearerToken extrae el token del header Authorization (esquema Bearer,
// case-insensitive según RFC 7235).
func bearerToken(r *http.Request) (string, bool) {
	const prefix = "bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(h[len(prefix):])
	if token == "" {
		return "", false
	}
	return token, true
}

// writeUnauthorized responde 401 con el envelope UNAUTHORIZED del contrato.
// El mensaje es deliberadamente genérico: nunca filtra la causa.
func writeUnauthorized(w http.ResponseWriter) {
	httpx.WriteError(w, http.StatusUnauthorized, CodeUnauthorized,
		"credenciales o sesión inválidas", nil)
}
