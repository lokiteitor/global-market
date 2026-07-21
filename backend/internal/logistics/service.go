package logistics

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// Service implementa el Logistics Service: lectura del grafo (nodos, enlaces y
// segmentos), pathfinding de solo cálculo (route-plans) y CRUD de rutas
// propietarias. Es PLANIFICACIÓN sin estado de tránsito (ADR-006): el único
// estado que escribe son las rutas (world.routes/route_legs), en una
// transacción atómica; el movimiento físico lo simula internal/world.
type Service struct {
	pool    *pgxpool.Pool
	repo    *Repo
	planner Planner
	opts    Options
	logger  *slog.Logger

	routePlans           *prometheus.CounterVec
	routePlansMultimodal prometheus.Counter
	routesCreated        prometheus.Counter
	planDuration         prometheus.Histogram
}

// NewService construye el servicio sobre el pool compartido de la plataforma.
// reg registra las métricas del módulo (nil las deja sin registrar: tests,
// herramientas); logger nil usa slog.Default(). Options inválidas devuelven
// error: la configuración rota debe impedir el arranque.
func NewService(pool *pgxpool.Pool, opts Options, logger *slog.Logger, reg prometheus.Registerer) (*Service, error) {
	if pool == nil {
		return nil, errors.New("logistics: el pool de BD es obligatorio")
	}
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	repo := NewRepo(pool)
	s := &Service{
		pool:    pool,
		repo:    repo,
		planner: newDijkstraPlanner(repo, opts.FuelCostPerKm),
		opts:    opts,
		logger:  logger,
		routePlans: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ii_route_plans_total",
			Help: "Total de route-plans calculados, por resultado (found, no_route, not_found, invalid, error).",
		}, []string{"result"}),
		routePlansMultimodal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_route_plans_multimodal_total",
			Help: "Total de route-plans hallados que son multimodales (≥2 modos con transbordo en terminal).",
		}),
		routesCreated: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_routes_created_total",
			Help: "Total de rutas creadas por las corporaciones.",
		}),
		planDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "ii_route_plan_duration_seconds",
			Help:    "Duración del pathfinding (Dijkstra ponderado por congestión) de cada route-plan.",
			Buckets: prometheus.DefBuckets,
		}),
	}
	if reg != nil {
		reg.MustRegister(s.routePlans, s.routePlansMultimodal, s.routesCreated, s.planDuration)
	}
	return s, nil
}

func (s *Service) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.opts.QueryTimeout)
}

// ─── (3) Route-plan: pathfinding de solo cálculo ─────────────────────────────

// PlanRoute calcula la ruta óptima entre dos nodos del grafo ponderada por la
// congestión suavizada (EMA). Operación de SOLO cálculo: no persiste nada. Mide
// la duración del pathfinding y contabiliza el resultado. Los errores de negocio
// (nodo inexistente, sin ruta, validación) se devuelven tipados; el handler los
// mapea a 404/422/400.
func (s *Service) PlanRoute(ctx context.Context, req PlanRequest) (RoutePlan, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	start := time.Now()
	plan, err := s.planner.Plan(ctx, req)
	s.planDuration.Observe(time.Since(start).Seconds())

	if err != nil {
		switch {
		case errors.Is(err, ErrNodeNotFound):
			s.routePlans.WithLabelValues("not_found").Inc()
		case errors.Is(err, ErrNoRoute):
			s.routePlans.WithLabelValues("no_route").Inc()
		case errors.Is(err, ErrValidation):
			s.routePlans.WithLabelValues("invalid").Inc()
		default:
			s.routePlans.WithLabelValues("error").Inc()
		}
		return RoutePlan{}, err
	}
	s.routePlans.WithLabelValues("found").Inc()
	multimodal := isMultimodal(plan)
	if multimodal {
		s.routePlansMultimodal.Inc()
	}
	s.logger.Info("route-plan calculado",
		slog.String("origin", req.Origin.String()),
		slog.String("destination", req.Destination.String()),
		slog.String("optimize", req.Optimize),
		slog.Int("legs", len(plan.Legs)),
		slog.Bool("multimodal", multimodal),
		slog.Int64("total_eta_sim_seconds", plan.TotalEtaSimSeconds))
	return plan, nil
}

// isMultimodal indica si un plan combina ≥2 modos de transporte (implica al menos
// un transbordo en terminal).
func isMultimodal(plan RoutePlan) bool {
	if len(plan.Legs) < 2 {
		return false
	}
	first := plan.Legs[0].Mode
	for _, leg := range plan.Legs[1:] {
		if leg.Mode != first {
			return true
		}
	}
	return false
}

// Planner devuelve el planner del servicio (la interface lista para la jerarquía
// HPA* del GDD 7.4 cuando la escala lo exija).
func (s *Service) Planner() Planner { return s.planner }
