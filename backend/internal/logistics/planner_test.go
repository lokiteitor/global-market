package logistics

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

// nodes fijos para los grafos de prueba (deterministas, comparables).
var (
	nodeA = uuid.MustParse("00000000-0000-0000-0000-0000000000a0")
	nodeB = uuid.MustParse("00000000-0000-0000-0000-0000000000b0")
	nodeC = uuid.MustParse("00000000-0000-0000-0000-0000000000c0")
	nodeD = uuid.MustParse("00000000-0000-0000-0000-0000000000d0")

	linkAB = uuid.MustParse("00000000-0000-0000-0000-00000000ab00")
	linkAC = uuid.MustParse("00000000-0000-0000-0000-00000000ac00")
	linkCB = uuid.MustParse("00000000-0000-0000-0000-00000000cb00")
	linkBD = uuid.MustParse("00000000-0000-0000-0000-00000000bd00")
)

// Grafos de prueba de un SOLO modo (road): no hay cambios de modo, así que las
// terminales y la penalización de transbordo nunca se consultan.
var noTerminals map[uuid.UUID]terminalInfo

func noTransship(uuid.UUID) int64 { return 0 }

// edge construye un rawEdge road de longitud/velocidad/congestión dados.
func edge(id, from, to uuid.UUID, lengthM, speed, capacity int64, congestion, customsBp float64) rawEdge {
	return rawEdge{
		LinkID: id, From: from, To: to, Mode: ModeRoad,
		LengthM: lengthM, BaseSpeedKmh: speed, CapacityPerHour: capacity,
		Congestion: congestion, CustomsBp: customsBp,
	}
}

// pathLinks extrae la secuencia de link ids de un camino.
func pathLinks(path []planEdge) []uuid.UUID {
	ids := make([]uuid.UUID, len(path))
	for i, e := range path {
		ids[i] = e.linkID
	}
	return ids
}

func sameLinks(got, want []uuid.UUID) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestDijkstraPicksFastestOfAlternatives: con dos caminos A→B (directo largo y
// A→C→B en dos tramos), fluido el directo es el más rápido; con congestión alta
// en el directo, Dijkstra desvía por C.
func TestDijkstraPicksFastestOfAlternatives(t *testing.T) {
	weight := weightSelector(OptimizeTime)

	t.Run("fluido: camino directo", func(t *testing.T) {
		edges := []rawEdge{
			edge(linkAB, nodeA, nodeB, 100000, 100, 1000, 1.0, 0), // 100 km ⇒ 1 h
			edge(linkAC, nodeA, nodeC, 60000, 100, 1000, 1.0, 0),  // 60 km ⇒ 1 h
			edge(linkCB, nodeC, nodeB, 60000, 100, 1000, 1.0, 0),  // 60 km ⇒ 1 h (desvío = 2 h)
		}
		adj := buildAdjacency(edges, 0, DefaultFuelCostPerKm)
		path, err := dijkstra(adj, nodeA, nodeB, weight, noTerminals, noTransship)
		if err != nil {
			t.Fatalf("dijkstra: %v", err)
		}
		if !sameLinks(pathLinks(path), []uuid.UUID{linkAB}) {
			t.Fatalf("fluido: esperado [AB], obtenido %v", pathLinks(path))
		}
	})

	t.Run("congestión alta en el directo: desvía por C", func(t *testing.T) {
		edges := []rawEdge{
			edge(linkAB, nodeA, nodeB, 100000, 100, 1000, 2.5, 0), // congestión 2.5 ⇒ ceil(2.5 h)=3 h
			edge(linkAC, nodeA, nodeC, 60000, 100, 1000, 1.0, 0),  // desvío = 2 h < 3 h
			edge(linkCB, nodeC, nodeB, 60000, 100, 1000, 1.0, 0),
		}
		adj := buildAdjacency(edges, 0, DefaultFuelCostPerKm)
		path, err := dijkstra(adj, nodeA, nodeB, weight, noTerminals, noTransship)
		if err != nil {
			t.Fatalf("dijkstra: %v", err)
		}
		if !sameLinks(pathLinks(path), []uuid.UUID{linkAC, linkCB}) {
			t.Fatalf("congestión: esperado [AC, CB], obtenido %v", pathLinks(path))
		}
	})
}

// TestDijkstraNoRoute: si el destino es inalcanzable (grafo desconectado),
// dijkstra devuelve ErrNoRoute.
func TestDijkstraNoRoute(t *testing.T) {
	edges := []rawEdge{
		edge(linkAC, nodeA, nodeC, 6000, 100, 1000, 1.0, 0), // A→C, pero nada llega a D
	}
	adj := buildAdjacency(edges, 0, DefaultFuelCostPerKm)
	if _, err := dijkstra(adj, nodeA, nodeD, weightSelector(OptimizeTime), noTerminals, noTransship); !errors.Is(err, ErrNoRoute) {
		t.Fatalf("esperado ErrNoRoute, obtenido %v", err)
	}
}

// TestDijkstraCostOptimize: con las MISMAS distancias, unas aduanas altas en el
// tramo directo lo encarecen y optimize=cost desvía por C, mientras que
// optimize=time (sin congestión) mantiene el directo.
func TestDijkstraCostOptimize(t *testing.T) {
	edges := []rawEdge{
		edge(linkAB, nodeA, nodeB, 10000, 100, 1000, 1.0, 6000), // aduanas 60%
		edge(linkAC, nodeA, nodeC, 6000, 100, 1000, 1.0, 0),
		edge(linkCB, nodeC, nodeB, 6000, 100, 1000, 1.0, 0),
	}
	adj := buildAdjacency(edges, 0, DefaultFuelCostPerKm)

	// Tiempo: el directo (más corto en km) gana pese a las aduanas.
	timePath, err := dijkstra(adj, nodeA, nodeB, weightSelector(OptimizeTime), noTerminals, noTransship)
	if err != nil {
		t.Fatalf("dijkstra time: %v", err)
	}
	if !sameLinks(pathLinks(timePath), []uuid.UUID{linkAB}) {
		t.Fatalf("time: esperado [AB], obtenido %v", pathLinks(timePath))
	}

	// Coste: las aduanas del directo (1000 fuel + 600 aduanas = 1600) superan al
	// desvío (600 + 600 = 1200) → desvía por C.
	costPath, err := dijkstra(adj, nodeA, nodeB, weightSelector(OptimizeCost), noTerminals, noTransship)
	if err != nil {
		t.Fatalf("dijkstra cost: %v", err)
	}
	if !sameLinks(pathLinks(costPath), []uuid.UUID{linkAC, linkCB}) {
		t.Fatalf("cost: esperado [AC, CB], obtenido %v", pathLinks(costPath))
	}
}

// TestCapacityFilterExcludesLink: un cargo_volume mayor que la capacidad de un
// enlace lo descarta (filtro informativo de viabilidad).
func TestCapacityFilterExcludesLink(t *testing.T) {
	edges := []rawEdge{
		edge(linkAB, nodeA, nodeB, 10000, 100, 50, 1.0, 0), // capacidad 50
		edge(linkAC, nodeA, nodeC, 6000, 100, 1000, 1.0, 0),
		edge(linkCB, nodeC, nodeB, 6000, 100, 1000, 1.0, 0),
	}
	// Volumen 100 > capacidad del directo (50) → el directo se excluye y solo
	// queda el desvío por C.
	adj := buildAdjacency(edges, 100, DefaultFuelCostPerKm)
	path, err := dijkstra(adj, nodeA, nodeB, weightSelector(OptimizeTime), noTerminals, noTransship)
	if err != nil {
		t.Fatalf("dijkstra: %v", err)
	}
	if !sameLinks(pathLinks(path), []uuid.UUID{linkAC, linkCB}) {
		t.Fatalf("capacidad: esperado desvío [AC, CB], obtenido %v", pathLinks(path))
	}

	// Con volumen 40 (<= 50) el directo vuelve a ser viable y gana.
	adj = buildAdjacency(edges, 40, DefaultFuelCostPerKm)
	path, err = dijkstra(adj, nodeA, nodeB, weightSelector(OptimizeTime), noTerminals, noTransship)
	if err != nil {
		t.Fatalf("dijkstra: %v", err)
	}
	if !sameLinks(pathLinks(path), []uuid.UUID{linkAB}) {
		t.Fatalf("capacidad: esperado directo [AB], obtenido %v", pathLinks(path))
	}
}

// TestEdgeEta comprueba la fórmula de ETA, que replica EXACTAMENTE la función
// VINCULANTE world.segment_travel_seconds (migración 0009) que el motor de
// tránsito usa para asentar la llegada física: t = ceil(length_km * congestion /
// speed) * 3600. El redondeo es a HORAS enteras (no a segundos), de modo que el
// route-plan no diverja del tiempo real del vehículo.
func TestEdgeEta(t *testing.T) {
	cases := []struct {
		lengthM, speed int64
		congestion     float64
		want           int64
	}{
		{14142, 80, 1.0, 3600},  // caso seeded real: 14.142 km a 80 km/h ⇒ ceil(0.18 h)=1 h=3600 s
		{90000, 80, 1.0, 7200},  // 90 km a 80 km/h ⇒ ceil(1.125 h)=2 h=7200 s
		{90000, 80, 2.0, 10800}, // congestión 2.0 ⇒ ceil(2.25 h)=3 h=10800 s (más lento)
		{160000, 80, 1.0, 7200}, // 160 km a 80 km/h ⇒ ceil(2.0 h)=2 h exacto=7200 s
		{1, 100, 1.0, 3600},     // suelo de una hora para cualquier tramo real (length_m > 0)
	}
	for _, c := range cases {
		if got := edgeEta(c.lengthM, c.speed, c.congestion); got != c.want {
			t.Errorf("edgeEta(%d,%d,%.1f)=%d, esperado %d", c.lengthM, c.speed, c.congestion, got, c.want)
		}
	}
}

// TestScaleByVolume: el coste base escala por el volumen con math/big; el
// desbordamiento se detecta (jamás float para dinero).
func TestScaleByVolume(t *testing.T) {
	if got, err := scaleByVolume(1200, 0); err != nil || got != 1200 {
		t.Fatalf("volumen 0 (factor 1): got=%d err=%v", got, err)
	}
	if got, err := scaleByVolume(1200, 5); err != nil || got != 6000 {
		t.Fatalf("volumen 5: got=%d err=%v", got, err)
	}
	if _, err := scaleByVolume(1<<62, 1<<10); !errors.Is(err, ErrOverflow) {
		t.Fatalf("esperado ErrOverflow, obtenido %v", err)
	}
}
