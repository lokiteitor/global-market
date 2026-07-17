// Package gateway compone la API pública del contrato OpenAPI v1.2.0 sobre
// los bounded contexts del backend. Es la biblioteca del composition root de
// cmd/gateway (SAD v1.1 §7): la ÚNICA pieza que conoce a la vez auth, ledger,
// contracts, market y el reloj de simulación — los módulos nunca se importan
// entre sí. También la reutilizan los tests E2E para levantar exactamente el
// mismo árbol de rutas que producción sin duplicarlo.
package gateway

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/auth"
	"github.com/lokiteitor/global-market/backend/internal/contracts"
	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/market"
	"github.com/lokiteitor/global-market/backend/internal/platform/httpx"
	"github.com/lokiteitor/global-market/backend/internal/platform/idempotency"
	"github.com/lokiteitor/global-market/backend/internal/sim/clock"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// APIPrefix es el prefijo de todas las rutas del contrato (servers.url del
// OpenAPI). healthz/readyz/metrics quedan fuera: son sondas de plataforma.
const APIPrefix = "/api/v1"

// Options agrupa la configuración de los módulos que compone el gateway.
// Cada módulo define y valida la suya (12-factor, variables II_*).
type Options struct {
	// Auth son los límites de rate limiting del módulo auth.
	Auth auth.Options
	// Ledger es la configuración del módulo ledger.
	Ledger ledger.Options
	// Contracts es la configuración del módulo contracts (ventanas de sorteo,
	// cooldown, TTL y reparto de garantía del CCRI).
	Contracts contracts.Options
	// Market es la configuración del lado de lectura del módulo market (OHLC).
	Market market.Options
	// ClockReader es la caché del lector del reloj de simulación.
	ClockReader clock.ReaderOptions
}

// OptionsFromEnv carga la configuración de todos los módulos compuestos desde
// el entorno. Cualquier valor inválido devuelve error: la configuración rota
// debe impedir el arranque.
func OptionsFromEnv() (Options, error) {
	authOpts, err := auth.OptionsFromEnv()
	if err != nil {
		return Options{}, err
	}
	ledgerOpts, err := ledger.OptionsFromEnv()
	if err != nil {
		return Options{}, err
	}
	contractsOpts, err := contracts.OptionsFromEnv()
	if err != nil {
		return Options{}, err
	}
	marketOpts, err := market.OptionsFromEnv()
	if err != nil {
		return Options{}, err
	}
	readerOpts, err := clock.ReaderOptionsFromEnv()
	if err != nil {
		return Options{}, err
	}
	return Options{
		Auth:        authOpts,
		Ledger:      ledgerOpts,
		Contracts:   contractsOpts,
		Market:      marketOpts,
		ClockReader: readerOpts,
	}, nil
}

// Deps son las dependencias de plataforma que BuildHandler compone.
type Deps struct {
	// Pool es el pool de conexiones compartido de la plataforma.
	Pool *pgxpool.Pool
	// Logger es el logger raíz del servicio.
	Logger *slog.Logger
	// Registry registra las métricas de los módulos; nil las deja sin
	// instrumentar (tests).
	Registry prometheus.Registerer
	// Options es la configuración de los módulos.
	Options Options
}

// BuildHandler construye el árbol de rutas del contrato bajo APIPrefix y lo
// devuelve listo para montarse en el mux del servicio (patrón
// `mux.Handle(APIPrefix+"/", handler)`). Toda respuesta — incluidos los 404 y
// 405 que el ServeMux resolvería en texto plano — usa los envelopes del
// contrato. cmd/gateway y los tests E2E comparten esta única definición.
func BuildHandler(deps Deps) (http.Handler, error) {
	if deps.Pool == nil {
		return nil, errors.New("gateway: Deps.Pool es obligatorio")
	}
	if deps.Logger == nil {
		return nil, errors.New("gateway: Deps.Logger es obligatorio")
	}

	// Reloj de simulación: lector cacheado que estampa el sim-time en el meta
	// de toda respuesta exitosa (schema Meta del contrato).
	meta := &simMetaSource{
		reader: clock.NewReader(clock.NewStore(deps.Pool), deps.Options.ClockReader, deps.Logger),
	}

	// Módulo auth: sesiones, identidad y rate limiting.
	var authMetrics *auth.Metrics
	if deps.Registry != nil {
		authMetrics = auth.NewMetrics(deps.Registry)
	}
	authSvc, err := auth.NewService(auth.NewPGRepository(deps.Pool), deps.Logger)
	if err != nil {
		return nil, err
	}
	authHandlers := auth.NewHandlers(authSvc, meta, deps.Options.Auth, authMetrics, deps.Logger)
	authMW := auth.NewMiddleware(authSvc, deps.Options.Auth, authMetrics, deps.Logger)

	// Módulo ledger: lecturas contables con autorización por propiedad. Su
	// Identity se implementa aquí sobre el Principal de auth (sessionIdentity):
	// los módulos no se conocen entre sí.
	ledgerSvc := ledger.NewService(deps.Pool, deps.Options.Ledger, deps.Registry)
	ledgerHandlers := ledger.NewHandlers(ledgerSvc, sessionIdentity{}, meta, deps.Logger)

	// Módulo contracts: el ciclo CCRI del tablón (publicar, consultar, cancelar,
	// aceptar) y la lectura de contratos/entregas. Su SimSource es el mismo
	// lector del reloj que estampa el meta; su Identity, el Principal de auth.
	contractsSvc, err := contracts.NewService(deps.Pool, meta.reader, deps.Options.Contracts, deps.Logger, deps.Registry)
	if err != nil {
		return nil, err
	}
	contractsHandlers := contracts.NewHandlers(contractsSvc, sessionIdentity{}, meta, deps.Logger)

	// Módulo market: lado de lectura del historial OHLC (el agregador vive en el
	// engine). Sin Identity propia: la autorización es la sesión del gateway.
	marketSvc := market.NewService(deps.Pool, deps.Options.Market)
	marketHandlers := market.NewHandlers(marketSvc, meta, deps.Logger)

	// Idempotencia (Idempotency-Key del contrato v1.2.0): reintentos seguros de
	// los comandos que mueven valor. Resuelve la cuenta con el mismo
	// sessionIdentity; se monta por dentro de RequireAuth sobre los POST/DELETE
	// de contracts.
	idemMW := idempotency.NewMiddleware(deps.Pool, sessionIdentity{}, deps.Registry, deps.Logger)

	api := http.NewServeMux()

	// Auth (contrato: POST /auth/sessions es público con rate limit de login
	// por IP+nombre dentro del propio handler; el resto exige sesión).
	api.Handle("POST "+APIPrefix+"/auth/sessions", authHandlers.CreateSession())
	api.Handle("DELETE "+APIPrefix+"/auth/sessions/current", authMW.RequireAuth(authHandlers.DeleteCurrentSession()))
	api.Handle("GET "+APIPrefix+"/auth/me", authMW.RequireAuth(authHandlers.Me()))

	// Ledger: el módulo registra sus rutas sin prefijo; se montan bajo
	// APIPrefix protegidas por sesión y por el rate limit de API por cuenta
	// (RateLimitAPI por dentro de RequireAuth: necesita el Principal).
	ledgerMux := http.NewServeMux()
	ledgerHandlers.Register(ledgerMux)
	api.Handle(APIPrefix+"/ledger/",
		http.StripPrefix(APIPrefix, authMW.RequireAuth(authMW.RateLimitAPI(ledgerMux))))

	// Contracts: mismo patrón que ledger (sesión + rate limit), y además el
	// protocolo de idempotencia SOLO sobre los comandos mutantes (POST/DELETE);
	// las lecturas pasan intactas. La cadena queda RequireAuth → RateLimitAPI →
	// idempotencia → mux, de modo que el resolver de cuenta ve el Principal.
	contractsMux := http.NewServeMux()
	contractsHandlers.Register(contractsMux)
	api.Handle(APIPrefix+"/contracts/",
		http.StripPrefix(APIPrefix, authMW.RequireAuth(authMW.RateLimitAPI(idempotentWrites(idemMW, contractsMux)))))

	// Market: lectura protegida por sesión y rate limit (sin idempotencia: no
	// muta estado).
	marketMux := http.NewServeMux()
	marketHandlers.Register(marketMux)
	api.Handle(APIPrefix+"/market/",
		http.StripPrefix(APIPrefix, authMW.RequireAuth(authMW.RateLimitAPI(marketMux))))

	return contractErrors(api), nil
}

// idempotentWrites aplica el protocolo Idempotency-Key únicamente a los
// comandos que mueven valor (POST/DELETE); las lecturas (GET) pasan sin coste.
// Así una GET nunca queda cacheada por una clave de idempotencia, que solo
// tiene sentido en mutaciones.
func idempotentWrites(mw *idempotency.Middleware, next http.Handler) http.Handler {
	guarded := mw.Wrap(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost, http.MethodDelete:
			guarded.ServeHTTP(w, r)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// ─── Meta del contrato (reloj de simulación) ────────────────────────────────

// simMetaSource implementa auth.MetaSource y ledger.MetaSource con el lector
// del reloj de simulación: sim_time legible, sim_time_seconds canónico y el
// wall-clock del servidor (RFC 3339, solo informativo).
type simMetaSource struct {
	reader *clock.Reader
}

func (s *simMetaSource) Meta(ctx context.Context) httpx.Meta {
	now := s.reader.Now(ctx)
	return httpx.Meta{
		SimTime:        simtime.Format(now),
		SimTimeSeconds: int64(now),
		ServerTime:     time.Now().UTC(),
	}
}

// ─── Identidad del ledger sobre la sesión de auth ───────────────────────────

// sessionIdentity implementa ledger.Identity con el Principal que RequireAuth
// inyecta en el contexto. Es el único puente entre ambos bounded contexts.
type sessionIdentity struct{}

func (sessionIdentity) AccountID(ctx context.Context) (uuid.UUID, bool) {
	acc, ok := auth.FromContext(ctx)
	if !ok {
		return uuid.Nil, false
	}
	return acc.ID, true
}

// ─── 404/405 con envelope del contrato ──────────────────────────────────────

// contractErrors garantiza que los 404/405 en texto plano del ServeMux salgan
// como envelopes de error del contrato. Los 404 propios de los handlers (ya
// JSON) pasan intactos: se distingue por el Content-Type fijado antes de
// WriteHeader.
func contractErrors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&envelopeErrorWriter{ResponseWriter: w}, r)
	})
}

// envelopeErrorWriter intercepta WriteHeader(404|405) sin Content-Type JSON,
// sustituye el cuerpo por el envelope del contrato y descarta el texto plano
// que el handler original escriba después.
type envelopeErrorWriter struct {
	http.ResponseWriter
	wroteHeader  bool
	suppressBody bool
}

func (w *envelopeErrorWriter) WriteHeader(status int) {
	if w.wroteHeader {
		w.ResponseWriter.WriteHeader(status)
		return
	}
	w.wroteHeader = true

	plain := !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json")
	switch {
	case status == http.StatusNotFound && plain:
		w.suppressBody = true
		w.stripPlainTextHeaders()
		httpx.WriteError(w.ResponseWriter, http.StatusNotFound, httpx.CodeNotFound,
			"recurso no encontrado", nil)
	case status == http.StatusMethodNotAllowed && plain:
		w.suppressBody = true
		w.stripPlainTextHeaders()
		var details map[string]any
		if allow := w.Header().Get("Allow"); allow != "" {
			details = map[string]any{"allow": allow}
		}
		httpx.WriteError(w.ResponseWriter, http.StatusMethodNotAllowed, httpx.CodeValidationError,
			"método no permitido para esta ruta", details)
	default:
		w.ResponseWriter.WriteHeader(status)
	}
}

func (w *envelopeErrorWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if w.suppressBody {
		return len(b), nil // el cuerpo de texto plano original se descarta
	}
	return w.ResponseWriter.Write(b)
}

// stripPlainTextHeaders retira las cabeceras del error en texto plano de
// net/http antes de emitir el envelope JSON.
func (w *envelopeErrorWriter) stripPlainTextHeaders() {
	h := w.Header()
	h.Del("Content-Type")
	h.Del("Content-Length")
	h.Del("X-Content-Type-Options")
}

// Unwrap permite a http.ResponseController alcanzar el writer original.
func (w *envelopeErrorWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
