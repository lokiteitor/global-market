package auth

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/lokiteitor/global-market/backend/internal/platform/httpx"
)

// Códigos de error del contrato emitidos por este módulo (schema Error).
const (
	CodeUnauthorized = "UNAUTHORIZED"
	CodeRateLimited  = "RATE_LIMITED"
)

// Límites de validación del body de login.
const (
	// maxLoginBodyBytes acota el cuerpo de POST /auth/sessions (incluye
	// client_info arbitrario).
	maxLoginBodyBytes int64 = 16 << 10 // 16 KiB
	// maxAccountNameLen es la longitud máxima aceptada de account_name.
	maxAccountNameLen = 128
	// maxSecretLen es la longitud máxima aceptada de secret.
	maxSecretLen = 1024
)

// MetaSource construye la Meta del envelope de éxito ({data,meta}). La
// implementa el composition root, que posee el reloj de simulación; este
// módulo no conoce el sim-time (sin imports cruzados entre contexts).
type MetaSource interface {
	Meta(ctx context.Context) httpx.Meta
}

// Handlers expone un http.Handler por endpoint auth del contrato. El
// composition root los monta en el mux (este módulo no registra rutas).
type Handlers struct {
	svc          *Service
	meta         MetaSource
	loginLimiter *Limiter
	metrics      *Metrics
	logger       *slog.Logger
}

// NewHandlers construye los handlers. El limiter de login es por IP+nombre
// con opts.LoginPerMin intentos por minuto (ráfaga igual al propio límite).
// metrics puede ser nil (sin instrumentación, p. ej. en tests).
func NewHandlers(svc *Service, meta MetaSource, opts Options, metrics *Metrics, logger *slog.Logger) *Handlers {
	return &Handlers{
		svc:          svc,
		meta:         meta,
		loginLimiter: NewLimiter(float64(opts.LoginPerMin)/60.0, opts.LoginPerMin),
		metrics:      metrics,
		logger:       logger,
	}
}

// sessionCreateRequest es el schema SessionCreateRequest del contrato.
type sessionCreateRequest struct {
	AccountName string         `json:"account_name"`
	Secret      string         `json:"secret"`
	ClientInfo  map[string]any `json:"client_info"`
}

// accountPayload es el schema Account del contrato (snake_case;
// bot_archetype solo presente cuando kind = bot).
type accountPayload struct {
	ID           string    `json:"id"`
	Kind         string    `json:"kind"`
	Name         string    `json:"name"`
	Status       string    `json:"status"`
	BotArchetype string    `json:"bot_archetype,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// sessionCreatedPayload es el schema SessionCreated del contrato.
type sessionCreatedPayload struct {
	SessionID string         `json:"session_id"`
	Token     string         `json:"token"`
	ExpiresAt time.Time      `json:"expires_at"`
	Account   accountPayload `json:"account"`
}

// toAccountPayload proyecta la cuenta de dominio al schema del contrato.
func toAccountPayload(a Account) accountPayload {
	return accountPayload{
		ID:           a.ID.String(),
		Kind:         a.Kind,
		Name:         a.Name,
		Status:       a.Status,
		BotArchetype: a.BotArchetype,
		CreatedAt:    a.CreatedAt,
	}
}

// CreateSession es POST /auth/sessions: valida el body, aplica el rate limit
// de login por IP+nombre y responde 201 con SessionCreated (el token viaja
// una única vez) o 401 UNAUTHORIZED genérico.
func (h *Handlers) CreateSession() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req sessionCreateRequest
		if err := httpx.ReadJSON(w, r, &req, maxLoginBodyBytes); err != nil {
			return // ReadJSON ya respondió 400 VALIDATION_ERROR
		}
		name := strings.TrimSpace(req.AccountName)
		if name == "" || len(name) > maxAccountNameLen {
			httpx.WriteError(w, http.StatusBadRequest, httpx.CodeValidationError,
				"account_name es obligatorio y de longitud válida",
				map[string]any{"field": "account_name", "max_length": maxAccountNameLen})
			return
		}
		if req.Secret == "" || len(req.Secret) > maxSecretLen {
			httpx.WriteError(w, http.StatusBadRequest, httpx.CodeValidationError,
				"secret es obligatorio y de longitud válida",
				map[string]any{"field": "secret", "max_length": maxSecretLen})
			return
		}

		// Rate limit de login por IP+nombre, antes de tocar BD o argon2.
		key := clientIP(r) + "|" + strings.ToLower(name)
		if allowed, retryAfter := h.loginLimiter.Allow(key); !allowed {
			h.metrics.RateLimited(ScopeLogin)
			writeRateLimited(w, retryAfter)
			return
		}

		created, err := h.svc.Login(r.Context(), name, req.Secret, req.ClientInfo)
		if err != nil {
			if errors.Is(err, ErrUnauthorized) {
				writeUnauthorized(w)
				return
			}
			h.internalError(w, r, "error creando sesión", err)
			return
		}
		httpx.WriteData(w, http.StatusCreated, sessionCreatedPayload{
			SessionID: created.Session.ID.String(),
			Token:     created.Token,
			ExpiresAt: created.Session.ExpiresAt,
			Account:   toAccountPayload(created.Account),
		}, h.meta.Meta(r.Context()))
	})
}

// DeleteCurrentSession es DELETE /auth/sessions/current: invalida la sesión
// del contexto y responde 204 sin cuerpo (sin envelope, por contrato).
// Requiere RequireAuth por fuera.
func (h *Handlers) DeleteCurrentSession() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFromContext(r.Context())
		if !ok {
			writeUnauthorized(w)
			return
		}
		if err := h.svc.Logout(r.Context(), p.SessionID); err != nil {
			h.internalError(w, r, "error cerrando sesión", err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// Me es GET /auth/me: responde 200 con la cuenta autenticada del contexto.
// Requiere RequireAuth por fuera.
func (h *Handlers) Me() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acc, err := h.svc.Me(r.Context())
		if err != nil {
			writeUnauthorized(w)
			return
		}
		httpx.WriteData(w, http.StatusOK, toAccountPayload(acc), h.meta.Meta(r.Context()))
	})
}

// internalError loguea el error real y responde 500 INTERNAL genérico.
func (h *Handlers) internalError(w http.ResponseWriter, r *http.Request, msg string, err error) {
	h.logger.LogAttrs(r.Context(), slog.LevelError, msg, slog.Any("error", err))
	httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal,
		"error interno del servidor", nil)
}

// clientIP deriva la IP del cliente de RemoteAddr. La resolución de
// X-Forwarded-For pertenece al edge/proxy de confianza, no a este módulo:
// confiar aquí en cabeceras del cliente permitiría evadir el rate limit.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
