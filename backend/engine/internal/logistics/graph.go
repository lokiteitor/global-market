// Package logistics ejecuta el ciclo físico de transporte: auto-despacho de
// contratos confirmados (ADR-IMPL-13), avance analítico de vehículos, averías,
// entregas y congestión EMA.
package logistics

import (
	"container/heap"
	"context"
	"math"

	"github.com/google/uuid"

	"imperio/engine/internal/core"
)

// SegmentRef es un segmento de enlace con los datos que consume el avance.
type SegmentRef struct {
	ID         uuid.UUID
	LengthM    int64
	Congestion float64
}

// Edge es un enlace del grafo logístico (no dirigido para la aplicación).
type Edge struct {
	LinkID       uuid.UUID
	From, To     uuid.UUID
	BaseSpeedKmh int64
	Segments     []SegmentRef // en orden seq ascendente (sentido From→To)
}

// Weight: peso Dijkstra del enlace = Σ length_m × congestion_ema por segmento.
func (e Edge) Weight() float64 {
	w := 0.0
	for _, s := range e.Segments {
		w += float64(s.LengthM) * s.Congestion
	}
	return w
}

// Step es un tramo del camino resuelto, con los segmentos ya ordenados en el
// sentido del viaje (invertidos si el enlace se recorre To→From).
type Step struct {
	LinkID       uuid.UUID
	FromNode     uuid.UUID
	ToNode       uuid.UUID
	BaseSpeedKmh int64
	Segments     []SegmentRef
}

// ShortestPath resuelve Dijkstra sobre el grafo no dirigido con peso
// length_m × congestion_ema. Devuelve los tramos en orden origen→destino.
func ShortestPath(edges []Edge, from, to uuid.UUID) ([]Step, bool) {
	if from == to {
		return nil, true
	}
	type half struct {
		edge Edge
		rev  bool // el enlace se recorre To→From
		next uuid.UUID
	}
	adj := map[uuid.UUID][]half{}
	for _, e := range edges {
		adj[e.From] = append(adj[e.From], half{edge: e, rev: false, next: e.To})
		adj[e.To] = append(adj[e.To], half{edge: e, rev: true, next: e.From})
	}

	dist := map[uuid.UUID]float64{from: 0}
	prev := map[uuid.UUID]half{}
	visited := map[uuid.UUID]bool{}
	pq := &nodeHeap{{id: from, dist: 0}}
	for pq.Len() > 0 {
		cur := heap.Pop(pq).(nodeItem)
		if visited[cur.id] {
			continue
		}
		visited[cur.id] = true
		if cur.id == to {
			break
		}
		for _, h := range adj[cur.id] {
			if visited[h.next] {
				continue
			}
			nd := cur.dist + h.edge.Weight()
			if d, ok := dist[h.next]; !ok || nd < d {
				dist[h.next] = nd
				prev[h.next] = h
				heap.Push(pq, nodeItem{id: h.next, dist: nd})
			}
		}
	}
	if !visited[to] {
		if _, ok := dist[to]; !ok || math.IsInf(dist[to], 1) {
			return nil, false
		}
	}
	if _, ok := prev[to]; !ok {
		return nil, false
	}
	// Reconstrucción hacia atrás.
	var steps []Step
	node := to
	for node != from {
		h, ok := prev[node]
		if !ok {
			return nil, false
		}
		segs := h.edge.Segments
		fromNode, toNode := h.edge.From, h.edge.To
		if h.rev {
			segs = reverseSegments(segs)
			fromNode, toNode = h.edge.To, h.edge.From
		}
		steps = append(steps, Step{
			LinkID: h.edge.LinkID, FromNode: fromNode, ToNode: toNode,
			BaseSpeedKmh: h.edge.BaseSpeedKmh, Segments: segs,
		})
		node = fromNode
	}
	// steps está de destino a origen: invertir.
	for i, j := 0, len(steps)-1; i < j; i, j = i+1, j-1 {
		steps[i], steps[j] = steps[j], steps[i]
	}
	return steps, true
}

func reverseSegments(in []SegmentRef) []SegmentRef {
	out := make([]SegmentRef, len(in))
	for i, s := range in {
		out[len(in)-1-i] = s
	}
	return out
}

type nodeItem struct {
	id   uuid.UUID
	dist float64
}
type nodeHeap []nodeItem

func (h nodeHeap) Len() int           { return len(h) }
func (h nodeHeap) Less(i, j int) bool { return h[i].dist < h[j].dist }
func (h nodeHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *nodeHeap) Push(x any)        { *h = append(*h, x.(nodeItem)) }
func (h *nodeHeap) Pop() any {
	old := *h
	n := len(old)
	it := old[n-1]
	*h = old[:n-1]
	return it
}

// LoadGraph carga los enlaces (y sus segmentos ordenados) de un modo dado.
func LoadGraph(ctx context.Context, q core.Querier, mode string) ([]Edge, error) {
	rows, err := q.Query(ctx, `
		SELECT nl.id, nl.from_node_id, nl.to_node_id, nl.base_speed_kmh,
		       ls.id, ls.length_m, ls.congestion_ema
		  FROM world.network_links nl
		  JOIN world.link_segments ls ON ls.link_id = nl.id
		 WHERE nl.mode = $1
		 ORDER BY nl.id, ls.seq`, mode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var edges []Edge
	byLink := map[uuid.UUID]int{}
	for rows.Next() {
		var (
			linkID, from, to, segID uuid.UUID
			baseSpeed               int64
			lengthM                 int64
			congestion              float64
		)
		if err := rows.Scan(&linkID, &from, &to, &baseSpeed, &segID, &lengthM, &congestion); err != nil {
			return nil, err
		}
		idx, ok := byLink[linkID]
		if !ok {
			edges = append(edges, Edge{LinkID: linkID, From: from, To: to, BaseSpeedKmh: baseSpeed})
			idx = len(edges) - 1
			byLink[linkID] = idx
		}
		edges[idx].Segments = append(edges[idx].Segments, SegmentRef{ID: segID, LengthM: lengthM, Congestion: congestion})
	}
	return edges, rows.Err()
}
