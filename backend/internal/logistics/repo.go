package logistics

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/lokiteitor/global-market/backend/internal/logistics/sqlcgen"
)

// Repo es la capa de acceso a datos del bounded context sobre el código
// generado por sqlc: traduce entre las filas generadas y los tipos de dominio.
// No abre transacciones — el servicio decide el ámbito transaccional y deriva un
// Repo ligado a su transacción con WithTx.
type Repo struct {
	q *sqlcgen.Queries
}

// NewRepo construye el repositorio sobre un pool o una transacción pgx.
func NewRepo(db sqlcgen.DBTX) *Repo {
	return &Repo{q: sqlcgen.New(db)}
}

// WithTx devuelve un Repo que ejecuta sus queries dentro de tx.
func (r *Repo) WithTx(tx pgx.Tx) *Repo {
	return &Repo{q: r.q.WithTx(tx)}
}

// ─── Grafo: lectura ──────────────────────────────────────────────────────────

// ListNetworkNodes devuelve una página de nodos del grafo con el filtro dado.
func (r *Repo) ListNetworkNodes(ctx context.Context, f NodeFilter, afterID *uuid.UUID, limit int32) ([]NetworkNode, error) {
	rows, err := r.q.ListNetworkNodes(ctx, sqlcgen.ListNetworkNodesParams{
		RegionID: f.RegionID,
		Kind: sqlcgen.NullWorldNodeKind{
			WorldNodeKind: sqlcgen.WorldNodeKind(f.Kind),
			Valid:         f.Kind != "",
		},
		AfterID:   afterID,
		PageLimit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("logistics: listando nodos del grafo: %w", err)
	}
	nodes := make([]NetworkNode, len(rows))
	for i, row := range rows {
		nodes[i] = NetworkNode{
			ID: row.ID, Kind: string(row.Kind), RegionID: row.RegionID,
			BuildingID: row.BuildingID, CityID: row.CityID, Location: row.Location,
		}
	}
	return nodes, nil
}

// NetworkNodeExists comprueba la existencia de un nodo del grafo.
func (r *Repo) NetworkNodeExists(ctx context.Context, id uuid.UUID) (bool, error) {
	ok, err := r.q.NetworkNodeExists(ctx, id)
	if err != nil {
		return false, fmt.Errorf("logistics: comprobando el nodo %s: %w", id, err)
	}
	return ok, nil
}

// ListNetworkLinks devuelve una página de enlaces (sin segmentos: se resuelven
// aparte con ListLinkSegmentsByLinks).
func (r *Repo) ListNetworkLinks(ctx context.Context, f LinkFilter, afterID *uuid.UUID, limit int32) ([]NetworkLink, error) {
	rows, err := r.q.ListNetworkLinks(ctx, sqlcgen.ListNetworkLinksParams{
		RegionID: f.RegionID,
		Mode: sqlcgen.NullWorldLinkMode{
			WorldLinkMode: sqlcgen.WorldLinkMode(f.Mode),
			Valid:         f.Mode != "",
		},
		FromNodeID: f.FromNodeID,
		AfterID:    afterID,
		PageLimit:  limit,
	})
	if err != nil {
		return nil, fmt.Errorf("logistics: listando enlaces del grafo: %w", err)
	}
	links := make([]NetworkLink, len(rows))
	for i, row := range rows {
		links[i] = NetworkLink{
			ID: row.ID, Mode: string(row.Mode), FromNodeID: row.FromNodeID,
			ToNodeID: row.ToNodeID, Path: row.Path, LengthM: row.LengthM,
			CapacityPerHour: row.CapacityPerHour, BaseSpeedKmh: row.BaseSpeedKmh,
			Segments: []LinkSegment{},
		}
	}
	return links, nil
}

// ListLinkSegmentsByLinks devuelve los segmentos de un conjunto de enlaces,
// agrupados por enlace (orden estable por secuencia).
func (r *Repo) ListLinkSegmentsByLinks(ctx context.Context, linkIDs []uuid.UUID) (map[uuid.UUID][]LinkSegment, error) {
	if len(linkIDs) == 0 {
		return map[uuid.UUID][]LinkSegment{}, nil
	}
	rows, err := r.q.ListLinkSegmentsByLinks(ctx, linkIDs)
	if err != nil {
		return nil, fmt.Errorf("logistics: listando segmentos de enlaces: %w", err)
	}
	byLink := make(map[uuid.UUID][]LinkSegment, len(linkIDs))
	for _, row := range rows {
		byLink[row.LinkID] = append(byLink[row.LinkID], LinkSegment{
			ID: row.ID, LinkID: row.LinkID, RegionID: row.RegionID, Seq: row.Seq,
			LengthM: row.LengthM, CongestionEma: row.CongestionEma, UpdatedAtSim: row.UpdatedAtSim,
		})
	}
	return byLink, nil
}

// ─── Grafo: pathfinding ──────────────────────────────────────────────────────

// LoadGraphEdges carga los enlaces (filtrados por modos; nil = todos) con la
// congestión EMA media y la tasa de aduanas media de sus segmentos.
func (r *Repo) LoadGraphEdges(ctx context.Context, modes []string) ([]rawEdge, error) {
	// modes vacío = todos (nil → NULL → sin filtro por modo).
	if len(modes) == 0 {
		modes = nil
	}
	rows, err := r.q.LoadGraphEdges(ctx, modes)
	if err != nil {
		return nil, fmt.Errorf("logistics: cargando el grafo de enlaces: %w", err)
	}
	edges := make([]rawEdge, len(rows))
	for i, row := range rows {
		edges[i] = rawEdge{
			LinkID: row.ID, From: row.FromNodeID, To: row.ToNodeID, Mode: string(row.Mode),
			LengthM: int64(row.LengthM), BaseSpeedKmh: int64(row.BaseSpeedKmh),
			CapacityPerHour: int64(row.CapacityPerHour),
			Congestion:      row.CongestionEma, CustomsBp: row.CustomsRateBp,
		}
	}
	return edges, nil
}

// TerminalsAtNodes devuelve, para un conjunto de nodos, el id de la terminal
// intermodal presente en cada uno (nodo → terminal).
func (r *Repo) TerminalsAtNodes(ctx context.Context, nodeIDs []uuid.UUID) (map[uuid.UUID]uuid.UUID, error) {
	if len(nodeIDs) == 0 {
		return map[uuid.UUID]uuid.UUID{}, nil
	}
	rows, err := r.q.TerminalsAtNodes(ctx, nodeIDs)
	if err != nil {
		return nil, fmt.Errorf("logistics: consultando terminales: %w", err)
	}
	byNode := make(map[uuid.UUID]uuid.UUID, len(rows))
	for _, row := range rows {
		byNode[row.NodeID] = row.ID
	}
	return byNode, nil
}

// LoadTerminalNodes devuelve todas las terminales del mundo indexadas por su nodo
// (node → {id, transbordo/hora}). El pathfinding las usa para permitir un cambio de
// modo solo en un nodo con terminal y para el tiempo de transbordo.
func (r *Repo) LoadTerminalNodes(ctx context.Context) (map[uuid.UUID]terminalInfo, error) {
	rows, err := r.q.LoadTerminalNodes(ctx)
	if err != nil {
		return nil, fmt.Errorf("logistics: cargando terminales: %w", err)
	}
	byNode := make(map[uuid.UUID]terminalInfo, len(rows))
	for _, row := range rows {
		byNode[row.NodeID] = terminalInfo{id: row.ID, perHour: int64(row.TransshipmentPerHour)}
	}
	return byNode, nil
}

// LinksByIDs devuelve la topología (modo, extremos) de un conjunto de enlaces,
// indexada por id (no preserva orden: lo reordena la capa de servicio).
func (r *Repo) LinksByIDs(ctx context.Context, linkIDs []uuid.UUID) (map[uuid.UUID]linkTopo, error) {
	if len(linkIDs) == 0 {
		return map[uuid.UUID]linkTopo{}, nil
	}
	rows, err := r.q.LinksByIDs(ctx, linkIDs)
	if err != nil {
		return nil, fmt.Errorf("logistics: consultando enlaces de la ruta: %w", err)
	}
	byID := make(map[uuid.UUID]linkTopo, len(rows))
	for _, row := range rows {
		byID[row.ID] = linkTopo{
			ID: row.ID, Mode: string(row.Mode), From: row.FromNodeID, To: row.ToNodeID,
		}
	}
	return byID, nil
}

// ─── Rutas: lectura ──────────────────────────────────────────────────────────

// ListRoutes devuelve una página de rutas del titular (sin legs: se resuelven
// aparte con ListRouteLegsByRoutes).
func (r *Repo) ListRoutes(ctx context.Context, owner uuid.UUID, f RouteFilter, afterID *uuid.UUID, limit int32) ([]Route, error) {
	active := pgtype.Bool{}
	if f.Active != nil {
		active = pgtype.Bool{Bool: *f.Active, Valid: true}
	}
	rows, err := r.q.ListRoutes(ctx, sqlcgen.ListRoutesParams{
		OwnerAccountID: owner,
		Kind: sqlcgen.NullWorldRouteKind{
			WorldRouteKind: sqlcgen.WorldRouteKind(f.Kind),
			Valid:          f.Kind != "",
		},
		Active:    active,
		AfterID:   afterID,
		PageLimit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("logistics: listando rutas de %s: %w", owner, err)
	}
	routes := make([]Route, len(rows))
	for i, row := range rows {
		routes[i] = toRoute(row)
	}
	return routes, nil
}

// ListRouteLegsByRoutes devuelve los legs de un conjunto de rutas, agrupados por
// ruta (orden estable por leg_index).
func (r *Repo) ListRouteLegsByRoutes(ctx context.Context, routeIDs []uuid.UUID) (map[uuid.UUID][]RouteLeg, error) {
	if len(routeIDs) == 0 {
		return map[uuid.UUID][]RouteLeg{}, nil
	}
	rows, err := r.q.ListRouteLegsByRoutes(ctx, routeIDs)
	if err != nil {
		return nil, fmt.Errorf("logistics: listando tramos de rutas: %w", err)
	}
	byRoute := make(map[uuid.UUID][]RouteLeg, len(routeIDs))
	for _, row := range rows {
		byRoute[row.RouteID] = append(byRoute[row.RouteID], RouteLeg{
			LegIndex: row.LegIndex, LinkID: row.LinkID,
		})
	}
	return byRoute, nil
}

// GetRoute devuelve una ruta por id (sin legs); pgx.ErrNoRows si no existe.
func (r *Repo) GetRoute(ctx context.Context, id uuid.UUID) (Route, error) {
	row, err := r.q.GetRoute(ctx, id)
	if err != nil {
		return Route{}, err
	}
	return toRoute(row), nil
}

// GetRouteForUpdate bloquea la fila de la ruta (SELECT FOR UPDATE) y la devuelve
// sin legs; pgx.ErrNoRows si no existe.
func (r *Repo) GetRouteForUpdate(ctx context.Context, id uuid.UUID) (Route, error) {
	row, err := r.q.GetRouteForUpdate(ctx, id)
	if err != nil {
		return Route{}, err
	}
	return toRoute(row), nil
}

// ─── Rutas: escritura ────────────────────────────────────────────────────────

// InsertRoute crea la definición de ruta (activa por defecto).
func (r *Repo) InsertRoute(ctx context.Context, id, owner uuid.UUID, name, kind string) (Route, error) {
	row, err := r.q.InsertRoute(ctx, sqlcgen.InsertRouteParams{
		ID: id, OwnerAccountID: owner, Name: name, Kind: sqlcgen.WorldRouteKind(kind),
	})
	if err != nil {
		return Route{}, fmt.Errorf("logistics: creando la ruta %s: %w", id, err)
	}
	return toRoute(row), nil
}

// InsertRouteLeg añade un tramo a una ruta en su posición ordenada.
func (r *Repo) InsertRouteLeg(ctx context.Context, routeID uuid.UUID, legIndex int32, linkID uuid.UUID) error {
	if err := r.q.InsertRouteLeg(ctx, sqlcgen.InsertRouteLegParams{
		RouteID: routeID, LegIndex: legIndex, LinkID: linkID,
	}); err != nil {
		return fmt.Errorf("logistics: añadiendo el tramo %d de la ruta %s: %w", legIndex, routeID, err)
	}
	return nil
}

// UpdateRoute aplica el patch parcial de name/active y devuelve la ruta
// actualizada (sin legs).
func (r *Repo) UpdateRoute(ctx context.Context, id uuid.UUID, name *string, active *bool) (Route, error) {
	activeArg := pgtype.Bool{}
	if active != nil {
		activeArg = pgtype.Bool{Bool: *active, Valid: true}
	}
	row, err := r.q.UpdateRoute(ctx, sqlcgen.UpdateRouteParams{
		Name: name, Active: activeArg, ID: id,
	})
	if err != nil {
		return Route{}, fmt.Errorf("logistics: actualizando la ruta %s: %w", id, err)
	}
	return toRoute(row), nil
}

// DeleteRouteLegs borra todos los legs de una ruta (paso previo al reemplazo).
func (r *Repo) DeleteRouteLegs(ctx context.Context, routeID uuid.UUID) error {
	if err := r.q.DeleteRouteLegs(ctx, routeID); err != nil {
		return fmt.Errorf("logistics: borrando los tramos de la ruta %s: %w", routeID, err)
	}
	return nil
}

// DeleteRoute elimina la ruta (sus legs caen en cascada).
func (r *Repo) DeleteRoute(ctx context.Context, id uuid.UUID) error {
	if err := r.q.DeleteRoute(ctx, id); err != nil {
		return fmt.Errorf("logistics: eliminando la ruta %s: %w", id, err)
	}
	return nil
}

// ─── Conversión de filas generadas a tipos de dominio ────────────────────────

func toRoute(row sqlcgen.WorldRoute) Route {
	return Route{
		ID: row.ID, OwnerAccountID: row.OwnerAccountID, Name: row.Name,
		Kind: string(row.Kind), Active: row.Active,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, Legs: []RouteLeg{},
	}
}

// newUUIDv7 genera un UUIDv7 (ADR-018: los IDs los produce la aplicación).
func newUUIDv7() (uuid.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("logistics: generando UUIDv7: %w", err)
	}
	return id, nil
}
