package logistics

import (
	"testing"

	"github.com/google/uuid"
)

func nid(n byte) uuid.UUID {
	var u uuid.UUID
	u[15] = n
	u[0] = 0xAA
	return u
}
func lid(n byte) uuid.UUID {
	var u uuid.UUID
	u[15] = n
	u[0] = 0xBB
	return u
}
func sid(n byte) uuid.UUID {
	var u uuid.UUID
	u[15] = n
	u[0] = 0xCC
	return u
}

// Grafo:  A --1(10km)-- B --2(10km)-- C
//
//	A --------3(15km)---------- C
//	D aislado
func testEdges(congestion3 float64) []Edge {
	return []Edge{
		{LinkID: lid(1), From: nid(1), To: nid(2), BaseSpeedKmh: 80,
			Segments: []SegmentRef{{ID: sid(1), LengthM: 10000, Congestion: 1}}},
		{LinkID: lid(2), From: nid(2), To: nid(3), BaseSpeedKmh: 80,
			Segments: []SegmentRef{{ID: sid(2), LengthM: 10000, Congestion: 1}}},
		{LinkID: lid(3), From: nid(1), To: nid(3), BaseSpeedKmh: 80,
			Segments: []SegmentRef{{ID: sid(3), LengthM: 15000, Congestion: congestion3}}},
	}
}

func TestShortestPathDirect(t *testing.T) {
	// Sin congestión, el enlace directo (15 km) gana a A-B-C (20 km).
	steps, ok := ShortestPath(testEdges(1), nid(1), nid(3))
	if !ok {
		t.Fatal("sin camino")
	}
	if len(steps) != 1 || steps[0].LinkID != lid(3) {
		t.Fatalf("esperaba el enlace directo, got %+v", steps)
	}
}

func TestShortestPathAvoidsCongestion(t *testing.T) {
	// Con congestión 2 en el directo (peso 30000), A-B-C (20000) gana.
	steps, ok := ShortestPath(testEdges(2), nid(1), nid(3))
	if !ok {
		t.Fatal("sin camino")
	}
	if len(steps) != 2 || steps[0].LinkID != lid(1) || steps[1].LinkID != lid(2) {
		t.Fatalf("esperaba A-B-C, got %+v", steps)
	}
	if steps[0].FromNode != nid(1) || steps[1].ToNode != nid(3) {
		t.Fatalf("orden de tramos incorrecto: %+v", steps)
	}
}

func TestShortestPathReversedSegments(t *testing.T) {
	// Viaje C→A por el enlace 3 (definido From=A): los tramos deben venir en
	// sentido de viaje (FromNode=C).
	steps, ok := ShortestPath(testEdges(1), nid(3), nid(1))
	if !ok {
		t.Fatal("sin camino")
	}
	if len(steps) != 1 || steps[0].FromNode != nid(3) || steps[0].ToNode != nid(1) {
		t.Fatalf("esperaba el directo invertido, got %+v", steps)
	}
}

func TestShortestPathUnreachable(t *testing.T) {
	if _, ok := ShortestPath(testEdges(1), nid(1), nid(4)); ok {
		t.Fatal("nodo aislado no debería ser alcanzable")
	}
}

func TestShortestPathSameNode(t *testing.T) {
	steps, ok := ShortestPath(testEdges(1), nid(1), nid(1))
	if !ok || len(steps) != 0 {
		t.Fatalf("origen==destino debe dar camino vacío, got %v/%v", steps, ok)
	}
}
