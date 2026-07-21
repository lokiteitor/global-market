package botsdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Valores por defecto de Options.
const (
	DefaultMaxRetries       = 3
	DefaultRetryBackoffBase = 500 * time.Millisecond
	DefaultRetryBackoffMax  = 15 * time.Second
	defaultUserAgent        = "imperio-botsdk/1"
)

// Options configura un Client. Solo BaseURL es obligatorio.
type Options struct {
	// BaseURL es la raíz de la API pública, incluido el prefijo de versión
	// (p. ej. "http://localhost:8080/api/v1").
	BaseURL string
	// HTTPClient permite inyectar un *http.Client propio (timeouts, proxy,
	// transporte). Por defecto, http.Client con timeout de 30 s.
	HTTPClient *http.Client
	// MaxRetries es el número máximo de reintentos adicionales tras el primer
	// intento, ante 429 y errores de red. Por defecto DefaultMaxRetries;
	// negativo = sin reintentos.
	MaxRetries int
	// RetryBackoffBase es la espera del primer reintento; cada reintento
	// posterior la duplica (backoff exponencial determinista). Por defecto
	// DefaultRetryBackoffBase.
	RetryBackoffBase time.Duration
	// RetryBackoffMax acota la espera entre reintentos. Por defecto
	// DefaultRetryBackoffMax.
	RetryBackoffMax time.Duration
	// Logger recibe los logs de decisión del cliente (reintentos, esperas)
	// en nivel Debug. Por defecto se descartan.
	Logger *slog.Logger
	// UserAgent identifica al cliente; por defecto "imperio-botsdk/1".
	UserAgent string
}

// Client es el cliente REST del SDK. Es seguro para uso concurrente; el token
// de sesión vive solo en memoria.
type Client struct {
	baseURL     string
	httpc       *http.Client
	maxRetries  int
	backoffBase time.Duration
	backoffMax  time.Duration
	log         *slog.Logger
	userAgent   string

	// sleep es inyectable en tests para no dormir de verdad.
	sleep func(ctx context.Context, d time.Duration) error

	mu       sync.RWMutex
	token    string
	lastMeta Meta
}

// New construye un Client validando las opciones.
func New(opts Options) (*Client, error) {
	base := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if base == "" {
		return nil, errors.New("botsdk: Options.BaseURL es obligatorio")
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("botsdk: Options.BaseURL inválida: %q", opts.BaseURL)
	}
	httpc := opts.HTTPClient
	if httpc == nil {
		httpc = &http.Client{Timeout: 30 * time.Second}
	}
	maxRetries := opts.MaxRetries
	if maxRetries == 0 {
		maxRetries = DefaultMaxRetries
	}
	if maxRetries < 0 {
		maxRetries = 0
	}
	backoffBase := opts.RetryBackoffBase
	if backoffBase <= 0 {
		backoffBase = DefaultRetryBackoffBase
	}
	backoffMax := opts.RetryBackoffMax
	if backoffMax <= 0 {
		backoffMax = DefaultRetryBackoffMax
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	userAgent := opts.UserAgent
	if userAgent == "" {
		userAgent = defaultUserAgent
	}
	return &Client{
		baseURL:     base,
		httpc:       httpc,
		maxRetries:  maxRetries,
		backoffBase: backoffBase,
		backoffMax:  backoffMax,
		log:         logger,
		userAgent:   userAgent,
		sleep:       sleepCtx,
	}, nil
}

// APIError es un error tipado del envelope {error:{code,message,details}} del
// contrato, más el status HTTP y la cabecera Retry-After si el servidor la envió.
type APIError struct {
	// Status es el código HTTP de la respuesta.
	Status int
	// Code es el código estable de error de dominio (p. ej.
	// INSUFFICIENT_COLLATERAL, RATE_LIMITED, MAINTENANCE_WINDOW).
	Code string
	// Message es la descripción legible del error.
	Message string
	// Details es el contexto estructurado del error (importes como strings).
	Details map[string]any
	// RetryAfter es la espera sugerida por la cabecera Retry-After (0 si ausente).
	RetryAfter time.Duration
}

// Error implementa error.
func (e *APIError) Error() string {
	code := e.Code
	if code == "" {
		code = http.StatusText(e.Status)
	}
	return fmt.Sprintf("botsdk: api %d %s: %s", e.Status, code, e.Message)
}

// IsCode informa de si err es un *APIError con el código de dominio dado.
func IsCode(err error, code string) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Code == code
}

// AsAPIError extrae el *APIError de la cadena de errores, si lo hay.
func AsAPIError(err error) (*APIError, bool) {
	var apiErr *APIError
	ok := errors.As(err, &apiErr)
	return apiErr, ok
}

// ── Sesión ──

// sessionCreateRequest es el cuerpo de POST /auth/sessions (schema
// SessionCreateRequest del contrato).
type sessionCreateRequest struct {
	AccountName string `json:"account_name"`
	Secret      string `json:"secret"`
}

// Login crea una sesión (POST /auth/sessions) y guarda el token en memoria
// para las llamadas siguientes.
func (c *Client) Login(ctx context.Context, accountName, secret string) (SessionCreated, error) {
	var out SessionCreated
	_, err := c.do(ctx, http.MethodPost, "/auth/sessions", nil, sessionCreateRequest{AccountName: accountName, Secret: secret}, &out)
	if err != nil {
		return SessionCreated{}, err
	}
	c.SetToken(out.Token)
	return out, nil
}

// Logout invalida la sesión actual (DELETE /auth/sessions/current) y olvida
// el token en memoria.
func (c *Client) Logout(ctx context.Context) error {
	if _, err := c.do(ctx, http.MethodDelete, "/auth/sessions/current", nil, nil, nil); err != nil {
		return err
	}
	c.SetToken("")
	return nil
}

// Me devuelve la cuenta autenticada (GET /auth/me).
func (c *Client) Me(ctx context.Context) (Account, error) {
	var out Account
	_, err := c.do(ctx, http.MethodGet, "/auth/me", nil, nil, &out)
	return out, err
}

// SetToken fija el token de sesión (p. ej. para reanudar una sesión emitida
// fuera del cliente). Login lo hace automáticamente.
func (c *Client) SetToken(token string) {
	c.mu.Lock()
	c.token = token
	c.mu.Unlock()
}

// Token devuelve el token de sesión en memoria ("" si no hay sesión).
func (c *Client) Token() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token
}

// LastMeta devuelve los metadatos (meta) de la última respuesta exitosa con
// envelope, incluido sim_time_seconds — el reloj de simulación que ven los bots.
func (c *Client) LastMeta() Meta {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastMeta
}

// SimTimeSeconds es un atajo de LastMeta().SimTimeSeconds.
func (c *Client) SimTimeSeconds() SimTime {
	return c.LastMeta().SimTimeSeconds
}

// ── Núcleo de peticiones ──

// dataEnvelope es el sobre {data,meta} de las respuestas exitosas.
type dataEnvelope struct {
	Data json.RawMessage `json:"data"`
	Meta Meta            `json:"meta"`
}

// errorEnvelope es el sobre {error:{code,message,details}}.
type errorEnvelope struct {
	Error struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	} `json:"error"`
}

// do ejecuta una petición contra la API: construye la URL, serializa el
// cuerpo, aplica autenticación e Idempotency-Key (UUIDv7 por mutación,
// reutilizada en los reintentos de esa misma mutación), decodifica el
// envelope y reintenta con backoff ante 429 (respetando Retry-After) y ante
// errores de red. out puede ser nil (respuestas 204).
func (c *Client) do(ctx context.Context, method, path string, query url.Values, in, out any) (Meta, error) {
	var payload []byte
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return Meta{}, fmt.Errorf("botsdk: serializando el cuerpo de %s %s: %w", method, path, err)
		}
		payload = b
	}
	fullURL := c.baseURL + path
	if len(query) > 0 {
		fullURL += "?" + query.Encode()
	}
	// Una clave de idempotencia por mutación lógica, estable entre reintentos.
	idemKey := ""
	if method != http.MethodGet {
		k, err := uuid.NewV7()
		if err != nil {
			return Meta{}, fmt.Errorf("botsdk: generando Idempotency-Key: %w", err)
		}
		idemKey = k.String()
	}

	for attempt := 0; ; attempt++ {
		var body io.Reader = http.NoBody
		if payload != nil {
			body = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, fullURL, body)
		if err != nil {
			return Meta{}, fmt.Errorf("botsdk: construyendo %s %s: %w", method, path, err)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", c.userAgent)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if idemKey != "" {
			req.Header.Set("Idempotency-Key", idemKey)
		}
		if tok := c.Token(); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}

		resp, err := c.httpc.Do(req)
		if err != nil {
			// Error de red: reintentable porque los GET son idempotentes y
			// toda mutación viaja con Idempotency-Key (misma clave ⇒ misma
			// respuesta reproducida, nunca doble ejecución).
			if attempt < c.maxRetries && ctx.Err() == nil {
				wait := c.backoff(attempt)
				c.log.DebugContext(ctx, "botsdk: reintento por error de red",
					"method", method, "path", path, "attempt", attempt+1, "wait", wait, "error", err)
				if serr := c.sleep(ctx, wait); serr != nil {
					return Meta{}, serr
				}
				continue
			}
			return Meta{}, fmt.Errorf("botsdk: %s %s: %w", method, path, err)
		}
		respBody, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			if attempt < c.maxRetries && ctx.Err() == nil {
				wait := c.backoff(attempt)
				c.log.DebugContext(ctx, "botsdk: reintento por cuerpo truncado",
					"method", method, "path", path, "attempt", attempt+1, "wait", wait, "error", readErr)
				if serr := c.sleep(ctx, wait); serr != nil {
					return Meta{}, serr
				}
				continue
			}
			return Meta{}, fmt.Errorf("botsdk: leyendo respuesta de %s %s: %w", method, path, readErr)
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			if resp.StatusCode == http.StatusNoContent || len(respBody) == 0 {
				return c.LastMeta(), nil
			}
			var env dataEnvelope
			if err := json.Unmarshal(respBody, &env); err != nil {
				return Meta{}, fmt.Errorf("botsdk: decodificando el envelope de %s %s: %w", method, path, err)
			}
			if out != nil && len(env.Data) > 0 && string(env.Data) != "null" {
				if err := json.Unmarshal(env.Data, out); err != nil {
					return Meta{}, fmt.Errorf("botsdk: decodificando data de %s %s: %w", method, path, err)
				}
			}
			c.mu.Lock()
			c.lastMeta = env.Meta
			c.mu.Unlock()
			return env.Meta, nil
		}

		apiErr := parseAPIError(resp.StatusCode, resp.Header, respBody)
		if resp.StatusCode == http.StatusTooManyRequests && attempt < c.maxRetries {
			wait := apiErr.RetryAfter
			if wait <= 0 {
				wait = c.backoff(attempt)
			}
			c.log.DebugContext(ctx, "botsdk: 429, esperando para reintentar",
				"method", method, "path", path, "attempt", attempt+1, "wait", wait)
			if serr := c.sleep(ctx, wait); serr != nil {
				return Meta{}, serr
			}
			continue
		}
		return Meta{}, apiErr
	}
}

// backoff devuelve la espera determinista del reintento attempt (0-based):
// base × 2^attempt, acotada por backoffMax.
func (c *Client) backoff(attempt int) time.Duration {
	d := c.backoffBase
	for i := 0; i < attempt && d < c.backoffMax; i++ {
		d *= 2
	}
	return min(d, c.backoffMax)
}

// parseAPIError construye el APIError desde el status, la cabecera
// Retry-After y el envelope de error (tolerante a cuerpos no-JSON, p. ej. de
// un proxy intermedio).
func parseAPIError(status int, header http.Header, body []byte) *APIError {
	apiErr := &APIError{Status: status}
	if ra := strings.TrimSpace(header.Get("Retry-After")); ra != "" {
		if secs, err := strconv.ParseInt(ra, 10, 64); err == nil && secs >= 0 {
			apiErr.RetryAfter = time.Duration(secs) * time.Second
		}
	}
	var env errorEnvelope
	if err := json.Unmarshal(body, &env); err == nil && env.Error.Code != "" {
		apiErr.Code = env.Error.Code
		apiErr.Message = env.Error.Message
		apiErr.Details = env.Error.Details
		return apiErr
	}
	msg := strings.TrimSpace(string(body))
	const maxMsg = 256
	if len(msg) > maxMsg {
		msg = msg[:maxMsg] + "…"
	}
	if msg == "" {
		msg = http.StatusText(status)
	}
	apiErr.Message = msg
	return apiErr
}

// sleepCtx duerme d respetando la cancelación del contexto.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ── Ayudas genéricas internas (los métodos Go no admiten type parameters) ──

// getOne ejecuta un GET y decodifica data en T.
func getOne[T any](ctx context.Context, c *Client, path string, query url.Values) (T, error) {
	var out T
	_, err := c.do(ctx, http.MethodGet, path, query, nil, &out)
	return out, err
}

// getList ejecuta un GET de listado y devuelve la página con su Meta.
func getList[T any](ctx context.Context, c *Client, path string, query url.Values) (Page[T], error) {
	var items []T
	meta, err := c.do(ctx, http.MethodGet, path, query, nil, &items)
	if err != nil {
		return Page[T]{}, err
	}
	return Page[T]{Items: items, Meta: meta}, nil
}

// mutate ejecuta una mutación (POST/PATCH/DELETE) con cuerpo opcional y
// decodifica data en T.
func mutate[T any](ctx context.Context, c *Client, method, path string, in any) (T, error) {
	var out T
	_, err := c.do(ctx, method, path, nil, in, &out)
	return out, err
}

// pathID escapa un identificador para incrustarlo en un path.
func pathID(id string) string { return url.PathEscape(id) }
