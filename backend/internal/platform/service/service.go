// Package service ensambla la plataforma común de los binarios HTTP del
// backend (gateway y engine): config → logger → métricas → pool de BD →
// mux con healthz/readyz/metrics → cadena de middlewares → servidor HTTP
// con apagado graceful. Los main de cmd/ solo eligen nombre y dirección.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lokiteitor/global-market/backend/internal/platform/config"
	"github.com/lokiteitor/global-market/backend/internal/platform/db"
	"github.com/lokiteitor/global-market/backend/internal/platform/httpx"
	"github.com/lokiteitor/global-market/backend/internal/platform/logging"
	"github.com/lokiteitor/global-market/backend/internal/platform/metrics"
)

const (
	// readyzTimeout acota el ping a la BD del handler de readyz.
	readyzTimeout = 2 * time.Second
	// shutdownTimeout es el plazo máximo del apagado graceful.
	shutdownTimeout = 15 * time.Second
	// readHeaderTimeout mitiga slowloris en la lectura de cabeceras.
	readHeaderTimeout = 10 * time.Second
)

// App es un binario HTTP de la plataforma ya ensamblado.
type App struct {
	name    string
	addr    string
	logger  *slog.Logger
	metrics *metrics.Metrics
	pool    *pgxpool.Pool
	mux     *http.ServeMux
}

// New ensambla la plataforma de un servicio. El pool de BD es perezoso: el
// arranque no depende de que la BD responda; readyz refleja su estado real.
func New(ctx context.Context, name, addr string, cfg config.Config) (*App, error) {
	logger := logging.New(cfg, name)
	m := metrics.New(name)

	pool, err := db.NewPool(ctx, cfg)
	if err != nil {
		return nil, err
	}
	m.Registry().MustRegister(db.NewPoolCollector(pool))

	a := &App{
		name:    name,
		addr:    addr,
		logger:  logger,
		metrics: m,
		pool:    pool,
		mux:     http.NewServeMux(),
	}
	a.registerPlatformRoutes()
	return a, nil
}

// Logger devuelve el logger raíz del servicio.
func (a *App) Logger() *slog.Logger { return a.logger }

// Metrics devuelve la instrumentación del servicio.
func (a *App) Metrics() *metrics.Metrics { return a.metrics }

// Pool devuelve el pool de conexiones a la BD.
func (a *App) Pool() *pgxpool.Pool { return a.pool }

// Mux devuelve el mux del servicio para registrar rutas de dominio.
func (a *App) Mux() *http.ServeMux { return a.mux }

// Handler devuelve el mux envuelto en la cadena de middlewares de la
// plataforma: RequestID → AccessLog → Metrics → Recover (el más interno,
// para que su 500 quede logueado y medido).
func (a *App) Handler() http.Handler {
	return httpx.Chain(a.mux,
		httpx.RequestID(),
		httpx.AccessLog(a.logger),
		a.metrics.Middleware,
		httpx.Recover(a.logger),
	)
}

// Run sirve HTTP hasta que el contexto se cancele (señal) y entonces apaga
// el servidor de forma graceful con plazo máximo shutdownTimeout.
func (a *App) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:              a.addr,
		Handler:           a.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		ErrorLog:          slog.NewLogLogger(a.logger.Handler(), slog.LevelError),
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	a.logger.Info("servidor HTTP escuchando", slog.String("addr", a.addr))

	select {
	case err := <-errCh:
		return fmt.Errorf("service: servidor HTTP: %w", err)
	case <-ctx.Done():
	}

	a.logger.Info("apagado solicitado; cerrando servidor HTTP",
		slog.Duration("timeout", shutdownTimeout))
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("service: apagado graceful: %w", err)
	}
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("service: servidor HTTP: %w", err)
	}
	a.logger.Info("servidor HTTP detenido limpiamente")
	return nil
}

// Close libera los recursos del servicio (pool de BD).
func (a *App) Close() {
	a.pool.Close()
}

// registerPlatformRoutes publica los endpoints operacionales. healthz y
// readyz no usan el envelope del contrato: son sondas de infraestructura.
func (a *App) registerPlatformRoutes() {
	a.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		a.writeProbe(w, http.StatusOK, "ok")
	})
	a.mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), readyzTimeout)
		defer cancel()
		if err := db.Ping(ctx, a.pool); err != nil {
			a.logger.Warn("readyz: la base de datos no responde", slog.Any("error", err))
			a.writeProbe(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		a.writeProbe(w, http.StatusOK, "ok")
	})
	a.mux.Handle("GET /metrics", a.metrics.Handler())
}

// probeResponse es el cuerpo plano de healthz/readyz.
type probeResponse struct {
	Status  string `json:"status"`
	Service string `json:"service"`
}

func (a *App) writeProbe(w http.ResponseWriter, status int, state string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(probeResponse{Status: state, Service: a.name}); err != nil {
		a.logger.Error("escribiendo respuesta de sonda", slog.Any("error", err))
	}
}
