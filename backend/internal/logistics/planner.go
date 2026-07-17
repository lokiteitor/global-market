package logistics

import (
	"container/heap"
	"context"
	"fmt"
	"math"
	"math/big"

	"github.com/google/uuid"
)

// Planner calcula un plan de ruta óptimo sobre el grafo logístico, ponderado por
// la congestión suavizada (EMA) que publican los shards. Es la superficie que el
// route-plan expone al servicio.
//
// La implementación de la Fase 1 (dijkstraPlanner) es un Dijkstra plano con
// min-heap sobre nodos: a la escala de una región con pocos nodos es correcto y
// suficiente. La jerarquía HPA* del GDD 7.4 es una optimización MEDIDA para el
// grafo mundial a gran escala; esta interface la deja lista para insertarse sin
// tocar a los llamadores — no es un cambio de arquitectura, sino de escala.
type Planner interface {
	// Plan calcula la ruta óptima entre req.Origin y req.Destination. Devuelve
	// ErrNodeNotFound si un extremo no existe y ErrNoRoute si no hay camino con
	// los modos indicados.
	Plan(ctx context.Context, req PlanRequest) (RoutePlan, error)
}

// graphLoader es la superficie de datos que el planner consume; la implementa
// *Repo (frontera de bounded context: lee world.* por sqlc, sin importar world).
type graphLoader interface {
	NetworkNodeExists(ctx context.Context, id uuid.UUID) (bool, error)
	LoadGraphEdges(ctx context.Context, modes []string) ([]rawEdge, error)
	TerminalsAtNodes(ctx context.Context, nodeIDs []uuid.UUID) (map[uuid.UUID]uuid.UUID, error)
}

// rawEdge es un enlace cargado del grafo con los agregados de sus segmentos
// (congestión EMA media, tasa de aduanas media) antes de derivar los pesos.
type rawEdge struct {
	LinkID          uuid.UUID
	From            uuid.UUID
	To              uuid.UUID
	Mode            string
	LengthM         int64
	BaseSpeedKmh    int64
	CapacityPerHour int64
	Congestion      float64 // >= 1 (1 = fluido)
	CustomsBp       float64 // tasa de aduanas media de la región de los segmentos
}

// linkTopo es la topología mínima de un enlace (modo y extremos) para validar
// la contigüidad y el multimodalismo de una ruta.
type linkTopo struct {
	ID   uuid.UUID
	Mode string
	From uuid.UUID
	To   uuid.UUID
}

// planEdge es una arista del grafo con sus pesos ya derivados: ETA en
// sim-segundos (peso de tiempo) y coste base en dinero de punto fijo
// (independiente del volumen; el volumen escala el coste TOTAL al final).
type planEdge struct {
	linkID uuid.UUID
	from   uuid.UUID
	to     uuid.UUID
	mode   string
	eta    int64
	cost   int64
}

// dijkstraPlanner implementa Planner con un Dijkstra plano ponderado por
// congestión.
type dijkstraPlanner struct {
	loader        graphLoader
	fuelCostPerKm int64
}

var _ Planner = (*dijkstraPlanner)(nil)

// newDijkstraPlanner construye el planner de la Fase 1.
func newDijkstraPlanner(loader graphLoader, fuelCostPerKm int64) *dijkstraPlanner {
	return &dijkstraPlanner{loader: loader, fuelCostPerKm: fuelCostPerKm}
}

// Plan carga el grafo, corre Dijkstra sobre nodos y construye el plan con las
// ETAs por tramo, el coste estimado para el volumen indicado y las terminales de
// transbordo en los cambios de modo.
func (p *dijkstraPlanner) Plan(ctx context.Context, req PlanRequest) (RoutePlan, error) {
	optimize := req.Optimize
	if optimize == "" {
		optimize = OptimizeTime
	}
	if optimize != OptimizeTime && optimize != OptimizeCost {
		return RoutePlan{}, fmt.Errorf("%w: optimize inválido %q", ErrValidation, req.Optimize)
	}
	if req.Origin == req.Destination {
		return RoutePlan{}, fmt.Errorf("%w: el origen y el destino no pueden ser el mismo nodo", ErrValidation)
	}

	okOrigin, err := p.loader.NetworkNodeExists(ctx, req.Origin)
	if err != nil {
		return RoutePlan{}, err
	}
	if !okOrigin {
		return RoutePlan{}, fmt.Errorf("%w: el nodo de origen %s", ErrNodeNotFound, req.Origin)
	}
	okDest, err := p.loader.NetworkNodeExists(ctx, req.Destination)
	if err != nil {
		return RoutePlan{}, err
	}
	if !okDest {
		return RoutePlan{}, fmt.Errorf("%w: el nodo de destino %s", ErrNodeNotFound, req.Destination)
	}

	edges, err := p.loader.LoadGraphEdges(ctx, req.Modes)
	if err != nil {
		return RoutePlan{}, err
	}
	adj := buildAdjacency(edges, req.CargoVolume, p.fuelCostPerKm)

	weight := weightSelector(optimize)
	path, err := dijkstra(adj, req.Origin, req.Destination, weight)
	if err != nil {
		return RoutePlan{}, err
	}

	return p.assemblePlan(ctx, req, path)
}

// assemblePlan construye el RoutePlan a partir del camino: ETAs por tramo, ETA
// total, coste estimado (escalado por el volumen) y las terminales de transbordo
// en los cambios de modo.
func (p *dijkstraPlanner) assemblePlan(ctx context.Context, req PlanRequest, path []planEdge) (RoutePlan, error) {
	var totalEta, baseCost int64
	var ok bool
	junctionNodes := make([]uuid.UUID, 0)
	for i := range path {
		if totalEta, ok = addNonNeg(totalEta, path[i].eta); !ok {
			return RoutePlan{}, ErrOverflow
		}
		if baseCost, ok = addNonNeg(baseCost, path[i].cost); !ok {
			return RoutePlan{}, ErrOverflow
		}
		if i+1 < len(path) && path[i].mode != path[i+1].mode {
			junctionNodes = append(junctionNodes, path[i].to)
		}
	}

	// Terminales de transbordo: solo se consultan las de los nodos donde cambia
	// el modo (coste ∝ cambios, no ∝ tramos).
	terminals := map[uuid.UUID]uuid.UUID{}
	if len(junctionNodes) > 0 {
		terminals, _ = p.loader.TerminalsAtNodes(ctx, junctionNodes)
	}

	estimated, err := scaleByVolume(baseCost, req.CargoVolume)
	if err != nil {
		return RoutePlan{}, err
	}

	legs := make([]RoutePlanLeg, len(path))
	for i, e := range path {
		leg := RoutePlanLeg{Seq: i, LinkID: e.linkID, Mode: e.mode, EtaSimSeconds: e.eta}
		if i+1 < len(path) && e.mode != path[i+1].mode {
			if tid, present := terminals[e.to]; present {
				id := tid
				leg.TransshipmentTerminalID = &id
			}
		}
		legs[i] = leg
	}

	return RoutePlan{
		OriginNodeID:       req.Origin,
		DestinationNodeID:  req.Destination,
		Legs:               legs,
		TotalEtaSimSeconds: totalEta,
		EstimatedCost:      estimated,
		HasCost:            true,
	}, nil
}

// ─── Construcción del grafo y pesos ──────────────────────────────────────────

// buildAdjacency deriva la lista de adyacencia dirigida (from → aristas) desde
// los enlaces cargados, calculando ETA y coste base por arista. Si se indica
// cargo_volume, descarta los enlaces cuya capacidad no lo admita (filtro
// INFORMATIVO de viabilidad, GDD 7.2): capacity_per_hour < cargo_volume.
func buildAdjacency(edges []rawEdge, cargoVolume, fuelCostPerKm int64) map[uuid.UUID][]planEdge {
	adj := make(map[uuid.UUID][]planEdge)
	for _, e := range edges {
		if cargoVolume > 0 && e.CapacityPerHour < cargoVolume {
			continue
		}
		adj[e.From] = append(adj[e.From], planEdge{
			linkID: e.LinkID,
			from:   e.From,
			to:     e.To,
			mode:   e.Mode,
			eta:    edgeEta(e.LengthM, e.BaseSpeedKmh, e.Congestion),
			cost:   edgeBaseCost(e.LengthM, e.CustomsBp, fuelCostPerKm),
		})
	}
	return adj
}

// weightSelector devuelve la función de peso de Dijkstra para el criterio dado.
func weightSelector(optimize string) func(planEdge) int64 {
	if optimize == OptimizeCost {
		return func(e planEdge) int64 { return e.cost }
	}
	return func(e planEdge) int64 { return e.eta }
}

// edgeEta calcula la ETA de un enlace en sim-segundos con la congestión vigente,
// replicando EXACTAMENTE la fórmula VINCULANTE del motor de tránsito de world
// (world.segment_travel_seconds, migración 0009), que es la que el barrido de
// segmentos vencidos usa para asentar la llegada física:
//
//	factor        = 1 / congestion_ema   (>1 = más lento)
//	t_viaje (h)   = ceil( (length_m/1000) * congestion_ema / base_speed_kmh )
//	t_viaje (seg) = t_viaje(h) * 3600
//
// El redondeo es a HORAS enteras (no a segundos): un tramo se planifica con el
// mismo tiempo que tardará realmente el vehículo, para que la ETA del route-plan
// no diverja de la física. El suelo de congestión es 1.0 (fluido); para cualquier
// tramo real (length_m > 0) la ETA es como mínimo una hora = 3600 sim-segundos.
func edgeEta(lengthM, baseSpeedKmh int64, congestion float64) int64 {
	if baseSpeedKmh <= 0 {
		return math.MaxInt64 // defensivo: el esquema garantiza base_speed_kmh > 0
	}
	if congestion < 1 {
		congestion = 1
	}
	hours := math.Ceil(float64(lengthM) / 1000.0 * congestion / float64(baseSpeedKmh))
	if hours < 1 {
		hours = 1
	}
	return int64(hours) * 3600
}

// edgeBaseCost calcula el coste base (dinero de punto fijo) de un enlace: el
// combustible por km más las aduanas/peajes de la región de sus segmentos. Es
// una estimación APROXIMADA e informativa (no un movimiento de valor). El
// volumen escala el coste TOTAL al final, no cada arista (no cambia el argmin).
func edgeBaseCost(lengthM int64, customsBp float64, fuelCostPerKm int64) int64 {
	fuel := lengthM * fuelCostPerKm / 1000
	customs := int64(float64(fuel) * customsBp / 10000.0)
	return fuel + customs
}

// scaleByVolume escala el coste base del camino por el volumen (factor = max(1,
// volume)) con math/big para detectar el desbordamiento de int64 (jamás float
// para dinero). volume <= 0 = sin volumen (factor 1).
func scaleByVolume(base, volume int64) (int64, error) {
	factor := volume
	if factor < 1 {
		factor = 1
	}
	prod := new(big.Int).Mul(big.NewInt(base), big.NewInt(factor))
	if !prod.IsInt64() {
		return 0, ErrOverflow
	}
	return prod.Int64(), nil
}

// addNonNeg suma dos enteros no negativos detectando el desbordamiento (los
// pesos de Dijkstra y las ETAs acumuladas nunca son negativos).
func addNonNeg(a, b int64) (int64, bool) {
	s := a + b
	if s < a { // b >= 0 ⇒ desbordamiento si el resultado decrece
		return 0, false
	}
	return s, true
}

// ─── Dijkstra con min-heap ───────────────────────────────────────────────────

// dijkstra calcula el camino de coste mínimo (según weight) entre origin y dest
// sobre la lista de adyacencia dirigida. Devuelve las aristas del camino en
// orden (origen→destino) o ErrNoRoute si dest es inalcanzable. Pesos no
// negativos ⇒ al extraer un nodo del heap su distancia es definitiva.
func dijkstra(adj map[uuid.UUID][]planEdge, origin, dest uuid.UUID, weight func(planEdge) int64) ([]planEdge, error) {
	dist := map[uuid.UUID]int64{origin: 0}
	prevEdge := map[uuid.UUID]planEdge{}
	prevNode := map[uuid.UUID]uuid.UUID{}
	settled := map[uuid.UUID]bool{}

	pq := &nodeHeap{{node: origin, dist: 0}}
	for pq.Len() > 0 {
		cur := heap.Pop(pq).(nodeDist)
		if settled[cur.node] {
			continue // entrada obsoleta (relajación posterior mejor)
		}
		settled[cur.node] = true
		if cur.node == dest {
			break
		}
		for _, e := range adj[cur.node] {
			if settled[e.to] {
				continue
			}
			nd, ok := addNonNeg(cur.dist, weight(e))
			if !ok {
				continue // desbordamiento: tratar como inalcanzable por esta arista
			}
			if best, seen := dist[e.to]; !seen || nd < best {
				dist[e.to] = nd
				prevEdge[e.to] = e
				prevNode[e.to] = cur.node
				heap.Push(pq, nodeDist{node: e.to, dist: nd})
			}
		}
	}

	if !settled[dest] {
		return nil, fmt.Errorf("%w (%s → %s)", ErrNoRoute, origin, dest)
	}

	// Reconstrucción origen→destino.
	var rev []planEdge
	for n := dest; n != origin; n = prevNode[n] {
		rev = append(rev, prevEdge[n])
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev, nil
}

// nodeDist es una entrada del min-heap de Dijkstra (nodo con su distancia
// tentativa).
type nodeDist struct {
	node uuid.UUID
	dist int64
}

// nodeHeap es un min-heap por distancia (container/heap).
type nodeHeap []nodeDist

func (h nodeHeap) Len() int           { return len(h) }
func (h nodeHeap) Less(i, j int) bool { return h[i].dist < h[j].dist }
func (h nodeHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *nodeHeap) Push(x any)        { *h = append(*h, x.(nodeDist)) }
func (h *nodeHeap) Pop() any {
	old := *h
	n := len(old)
	it := old[n-1]
	*h = old[:n-1]
	return it
}
