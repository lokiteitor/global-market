package botsdk

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// ── Red logística ──

// NetworkNodesQuery filtra GET /logistics/network/nodes.
type NetworkNodesQuery struct {
	RegionID string
	Kind     NodeKind
	PageQuery
}

// values serializa la query.
func (q NetworkNodesQuery) values() url.Values {
	v := url.Values{}
	if q.RegionID != "" {
		v.Set("region_id", q.RegionID)
	}
	if q.Kind != "" {
		v.Set("kind", string(q.Kind))
	}
	q.apply(v)
	return v
}

// NetworkNodes devuelve los nodos del grafo logístico
// (GET /logistics/network/nodes).
func (c *Client) NetworkNodes(ctx context.Context, q NetworkNodesQuery) (Page[NetworkNode], error) {
	return getList[NetworkNode](ctx, c, "/logistics/network/nodes", q.values())
}

// NetworkLinksQuery filtra GET /logistics/network/links.
type NetworkLinksQuery struct {
	RegionID   string
	Mode       LinkMode
	FromNodeID string
	PageQuery
}

// values serializa la query.
func (q NetworkLinksQuery) values() url.Values {
	v := url.Values{}
	if q.RegionID != "" {
		v.Set("region_id", q.RegionID)
	}
	if q.Mode != "" {
		v.Set("mode", string(q.Mode))
	}
	if q.FromNodeID != "" {
		v.Set("from_node_id", q.FromNodeID)
	}
	q.apply(v)
	return v
}

// NetworkLinks devuelve los enlaces del grafo con sus segmentos y congestión
// EMA (GET /logistics/network/links).
func (c *Client) NetworkLinks(ctx context.Context, q NetworkLinksQuery) (Page[NetworkLink], error) {
	return getList[NetworkLink](ctx, c, "/logistics/network/links", q.values())
}

// ── Planificación ──

// PlanRoute calcula una ruta óptima con pathfinding jerárquico sobre la
// congestión EMA (POST /logistics/route-plans). Las ETAs son estimaciones
// informativas, no garantías; no persiste nada (422 NO_ROUTE_FOUND si no hay
// ruta ejecutable).
func (c *Client) PlanRoute(ctx context.Context, in RoutePlanRequest) (RoutePlan, error) {
	return mutate[RoutePlan](ctx, c, http.MethodPost, "/logistics/route-plans", in)
}

// ── Rutas propias ──

// RoutesQuery filtra GET /logistics/routes.
type RoutesQuery struct {
	Kind   RouteKind
	Active *bool
	PageQuery
}

// values serializa la query.
func (q RoutesQuery) values() url.Values {
	v := url.Values{}
	if q.Kind != "" {
		v.Set("kind", string(q.Kind))
	}
	if q.Active != nil {
		v.Set("active", strconv.FormatBool(*q.Active))
	}
	q.apply(v)
	return v
}

// ListRoutes devuelve las rutas definidas por la corporación
// (GET /logistics/routes).
func (c *Client) ListRoutes(ctx context.Context, q RoutesQuery) (Page[Route], error) {
	return getList[Route](ctx, c, "/logistics/routes", q.values())
}

// CreateRoute define una ruta como secuencia contigua de enlaces
// (POST /logistics/routes).
func (c *Client) CreateRoute(ctx context.Context, in RouteCreate) (Route, error) {
	return mutate[Route](ctx, c, http.MethodPost, "/logistics/routes", in)
}

// GetRoute devuelve una ruta con sus tramos (GET /logistics/routes/{id}).
func (c *Client) GetRoute(ctx context.Context, routeID string) (Route, error) {
	return getOne[Route](ctx, c, "/logistics/routes/"+pathID(routeID), nil)
}

// UpdateRoute modifica una ruta (PATCH /logistics/routes/{id}).
func (c *Client) UpdateRoute(ctx context.Context, routeID string, in RouteUpdate) (Route, error) {
	return mutate[Route](ctx, c, http.MethodPatch, "/logistics/routes/"+pathID(routeID), in)
}

// DeleteRoute elimina una ruta; los vehículos asignados quedan idle al
// completar su tramo actual (DELETE /logistics/routes/{id}).
func (c *Client) DeleteRoute(ctx context.Context, routeID string) error {
	_, err := c.do(ctx, http.MethodDelete, "/logistics/routes/"+pathID(routeID), nil, nil, nil)
	return err
}
