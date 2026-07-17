package logistics

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// ─── (1) Nodos del grafo ─────────────────────────────────────────────────────

// ListNodes devuelve una página de nodos del grafo con los filtros del contrato
// y el cursor de la página siguiente ("" si no hay más). Lectura pública: sin
// filtro de propiedad (el grafo es información del mundo).
func (s *Service) ListNodes(ctx context.Context, f NodeFilter) ([]NetworkNode, string, error) {
	if f.Kind != "" && !validNodeKind(f.Kind) {
		return nil, "", fmt.Errorf("%w: kind de nodo inválido %q", ErrValidation, f.Kind)
	}
	after, err := decodeAfter(f.Cursor, cursorNode)
	if err != nil {
		return nil, "", err
	}
	limit := normalizeLimit(f.Limit)

	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	nodes, err := s.repo.ListNetworkNodes(ctx, f, after, limit+1)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(nodes) > int(limit) {
		nodes = nodes[:limit]
		next = encodeCursor(cursorNode, nodes[len(nodes)-1].ID)
	}
	return nodes, next, nil
}

// ─── (2) Enlaces del grafo ───────────────────────────────────────────────────

// ListLinks devuelve una página de enlaces del grafo con sus segmentos (y la
// congestión EMA vigente) y el cursor de la página siguiente. Los segmentos se
// resuelven en una segunda query por los ids de la página, para no multiplicar
// las filas de enlace en el JOIN.
func (s *Service) ListLinks(ctx context.Context, f LinkFilter) ([]NetworkLink, string, error) {
	if f.Mode != "" && !validMode(f.Mode) {
		return nil, "", fmt.Errorf("%w: modo inválido %q", ErrValidation, f.Mode)
	}
	after, err := decodeAfter(f.Cursor, cursorLink)
	if err != nil {
		return nil, "", err
	}
	limit := normalizeLimit(f.Limit)

	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	links, err := s.repo.ListNetworkLinks(ctx, f, after, limit+1)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(links) > int(limit) {
		links = links[:limit]
		next = encodeCursor(cursorLink, links[len(links)-1].ID)
	}

	if len(links) > 0 {
		ids := make([]uuid.UUID, len(links))
		for i, l := range links {
			ids[i] = l.ID
		}
		segments, err := s.repo.ListLinkSegmentsByLinks(ctx, ids)
		if err != nil {
			return nil, "", err
		}
		for i := range links {
			if segs, ok := segments[links[i].ID]; ok {
				links[i].Segments = segs
			}
		}
	}
	return links, next, nil
}
