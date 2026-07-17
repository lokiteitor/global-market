package logistics

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lokiteitor/global-market/backend/internal/platform/db"
)

// ─── (4) Rutas: lectura ──────────────────────────────────────────────────────

// ListRoutes devuelve una página de las rutas del titular (SOLO propias) con los
// filtros del contrato y el cursor de la página siguiente ("" si no hay más).
func (s *Service) ListRoutes(ctx context.Context, owner uuid.UUID, f RouteFilter) ([]Route, string, error) {
	if f.Kind != "" && !validRouteKind(f.Kind) {
		return nil, "", fmt.Errorf("%w: kind de ruta inválido %q", ErrValidation, f.Kind)
	}
	after, err := decodeAfter(f.Cursor, cursorRoute)
	if err != nil {
		return nil, "", err
	}
	limit := normalizeLimit(f.Limit)

	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	routes, err := s.repo.ListRoutes(ctx, owner, f, after, limit+1)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(routes) > int(limit) {
		routes = routes[:limit]
		next = encodeCursor(cursorRoute, routes[len(routes)-1].ID)
	}
	if err := s.attachLegs(ctx, routes); err != nil {
		return nil, "", err
	}
	return routes, next, nil
}

// GetRoute devuelve una ruta propia con sus tramos. ErrRouteNotFound si no
// existe; ErrNotRouteOwner si pertenece a otra corporación (403).
func (s *Service) GetRoute(ctx context.Context, owner, id uuid.UUID) (Route, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	route, err := s.repo.GetRoute(ctx, id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Route{}, fmt.Errorf("%w (%s)", ErrRouteNotFound, id)
	case err != nil:
		return Route{}, fmt.Errorf("logistics: consultando la ruta %s: %w", id, err)
	}
	if route.OwnerAccountID != owner {
		return Route{}, fmt.Errorf("%w (%s)", ErrNotRouteOwner, id)
	}
	routes := []Route{route}
	if err := s.attachLegs(ctx, routes); err != nil {
		return Route{}, err
	}
	return routes[0], nil
}

// attachLegs carga y adjunta los tramos a un conjunto de rutas (una sola query
// por los ids de la página).
func (s *Service) attachLegs(ctx context.Context, routes []Route) error {
	if len(routes) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(routes))
	for i, r := range routes {
		ids[i] = r.ID
	}
	byRoute, err := s.repo.ListRouteLegsByRoutes(ctx, ids)
	if err != nil {
		return err
	}
	for i := range routes {
		if legs, ok := byRoute[routes[i].ID]; ok {
			routes[i].Legs = legs
		}
	}
	return nil
}

// ─── (5) Rutas: escritura ────────────────────────────────────────────────────

// CreateRoute crea una ruta propietaria como secuencia CONTIGUA de enlaces. La
// creación (validación de contigüidad/multimodalismo + inserción de la ruta y
// sus tramos) es una única transacción atómica. No mueve valor: no emite al
// outbox — el motor de tránsito de world consume las rutas al despachar.
func (s *Service) CreateRoute(ctx context.Context, owner uuid.UUID, in RouteInput) (Route, error) {
	if in.Name == "" {
		return Route{}, fmt.Errorf("%w: name es obligatorio", ErrValidation)
	}
	if !validRouteKind(in.Kind) {
		return Route{}, fmt.Errorf("%w: kind de ruta inválido %q", ErrValidation, in.Kind)
	}
	if len(in.Legs) == 0 {
		return Route{}, fmt.Errorf("%w: legs no puede estar vacío", ErrValidation)
	}

	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	var out Route
	err := db.RunSerializable(ctx, s.pool, func(tx pgx.Tx) error {
		r := s.repo.WithTx(tx)
		if err := validateLegs(ctx, r, in.Legs); err != nil {
			return err
		}
		id, err := newUUIDv7()
		if err != nil {
			return err
		}
		route, err := r.InsertRoute(ctx, id, owner, in.Name, in.Kind)
		if err != nil {
			return err
		}
		if err := insertLegs(ctx, r, id, in.Legs); err != nil {
			return err
		}
		route.Legs = legsOf(in.Legs)
		out = route
		return nil
	})
	if err != nil {
		return Route{}, err
	}
	s.routesCreated.Inc()
	s.logger.Info("ruta creada",
		slog.String("route_id", out.ID.String()),
		slog.String("owner", owner.String()),
		slog.String("kind", out.Kind),
		slog.Int("legs", len(out.Legs)))
	return out, nil
}

// UpdateRoute aplica un patch parcial a una ruta propia (name/active y,
// opcionalmente, la secuencia de tramos con la misma validación de contigüidad).
// ErrRouteNotFound/ErrNotRouteOwner si no existe o es ajena.
func (s *Service) UpdateRoute(ctx context.Context, owner, id uuid.UUID, upd RouteUpdate) (Route, error) {
	if upd.Legs != nil && len(*upd.Legs) == 0 {
		return Route{}, fmt.Errorf("%w: legs no puede estar vacío", ErrValidation)
	}

	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	var out Route
	err := db.RunSerializable(ctx, s.pool, func(tx pgx.Tx) error {
		r := s.repo.WithTx(tx)
		route, err := r.GetRouteForUpdate(ctx, id)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("%w (%s)", ErrRouteNotFound, id)
		case err != nil:
			return fmt.Errorf("logistics: bloqueando la ruta %s: %w", id, err)
		}
		if route.OwnerAccountID != owner {
			return fmt.Errorf("%w (%s)", ErrNotRouteOwner, id)
		}

		if upd.Legs != nil {
			if err := validateLegs(ctx, r, *upd.Legs); err != nil {
				return err
			}
			if err := r.DeleteRouteLegs(ctx, id); err != nil {
				return err
			}
			if err := insertLegs(ctx, r, id, *upd.Legs); err != nil {
				return err
			}
		}

		updated, err := r.UpdateRoute(ctx, id, upd.Name, upd.Active)
		if err != nil {
			return err
		}
		out = updated
		return nil
	})
	if err != nil {
		return Route{}, err
	}
	// Recarga los tramos vigentes fuera de la tx (lectura consistente por id).
	routes := []Route{out}
	if err := s.attachLegs(ctx, routes); err != nil {
		return Route{}, err
	}
	s.logger.Info("ruta actualizada",
		slog.String("route_id", id.String()),
		slog.String("owner", owner.String()),
		slog.Int("legs", len(routes[0].Legs)))
	return routes[0], nil
}

// DeleteRoute elimina una ruta propia (sus tramos caen en cascada). Los
// vehículos asignados los deja sin ruta el motor de tránsito de world al no
// encontrarla. ErrRouteNotFound/ErrNotRouteOwner si no existe o es ajena.
func (s *Service) DeleteRoute(ctx context.Context, owner, id uuid.UUID) error {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	return db.RunSerializable(ctx, s.pool, func(tx pgx.Tx) error {
		r := s.repo.WithTx(tx)
		route, err := r.GetRouteForUpdate(ctx, id)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("%w (%s)", ErrRouteNotFound, id)
		case err != nil:
			return fmt.Errorf("logistics: bloqueando la ruta %s: %w", id, err)
		}
		if route.OwnerAccountID != owner {
			return fmt.Errorf("%w (%s)", ErrNotRouteOwner, id)
		}
		return r.DeleteRoute(ctx, id)
	})
}

// ─── Validación de la secuencia de tramos ────────────────────────────────────

// validateLegs comprueba que los enlaces de una ruta forman una secuencia
// CONTIGUA (to_node[i] == from_node[i+1]) y que todo cambio de modo ocurre en un
// nodo con terminal intermodal (GDD 7.3). Un enlace inexistente es
// ErrLinkNotFound; la discontinuidad, ErrDiscontiguousLegs; el salto multimodal
// sin terminal, ErrMultimodalNoTerminal.
func validateLegs(ctx context.Context, r *Repo, legs []uuid.UUID) error {
	topos, err := r.LinksByIDs(ctx, legs)
	if err != nil {
		return err
	}
	ordered := make([]linkTopo, len(legs))
	for i, id := range legs {
		t, ok := topos[id]
		if !ok {
			return fmt.Errorf("%w (%s)", ErrLinkNotFound, id)
		}
		ordered[i] = t
	}

	var junctions []uuid.UUID
	for i := 0; i+1 < len(ordered); i++ {
		if ordered[i].To != ordered[i+1].From {
			return fmt.Errorf("%w: el tramo %d termina en %s pero el %d empieza en %s",
				ErrDiscontiguousLegs, i, ordered[i].To, i+1, ordered[i+1].From)
		}
		if ordered[i].Mode != ordered[i+1].Mode {
			junctions = append(junctions, ordered[i].To)
		}
	}

	if len(junctions) > 0 {
		terminals, err := r.TerminalsAtNodes(ctx, junctions)
		if err != nil {
			return err
		}
		for _, node := range junctions {
			if _, ok := terminals[node]; !ok {
				return fmt.Errorf("%w (nodo %s)", ErrMultimodalNoTerminal, node)
			}
		}
	}
	return nil
}

// insertLegs inserta los tramos de una ruta en orden (leg_index = posición).
func insertLegs(ctx context.Context, r *Repo, routeID uuid.UUID, legs []uuid.UUID) error {
	for i, linkID := range legs {
		if err := r.InsertRouteLeg(ctx, routeID, int32(i), linkID); err != nil { //nolint:gosec // i acotado por len(legs)
			return err
		}
	}
	return nil
}

// legsOf construye los RouteLeg de dominio desde la secuencia de enlaces (mismo
// orden = leg_index), para devolver la ruta recién creada sin releer la BD.
func legsOf(legs []uuid.UUID) []RouteLeg {
	out := make([]RouteLeg, len(legs))
	for i, id := range legs {
		out[i] = RouteLeg{LegIndex: int32(i), LinkID: id} //nolint:gosec // i acotado por len(legs)
	}
	return out
}
