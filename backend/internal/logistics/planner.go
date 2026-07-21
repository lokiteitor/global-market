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
	// LoadTerminalNodes devuelve TODAS las terminales del mundo (node → terminal):
	// el pathfinding permite un cambio de modo SOLO en un nodo con terminal (GDD 7.3)
	// y suma su tiempo de transbordo a la ETA.
	LoadTerminalNodes(ctx context.Context) (map[uuid.UUID]terminalInfo, error)
}

// terminalInfo es la terminal intermodal de un nodo: su id y la capacidad de
// transbordo por hora (para el tiempo de transbordo del cambio de modo).
type terminalInfo struct {
	id      uuid.UUID
	perHour int64
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
	terminals, err := p.loader.LoadTerminalNodes(ctx)
	if err != nil {
		return RoutePlan{}, err
	}
	adj := buildAdjacency(edges, req.CargoVolume, p.fuelCostPerKm)

	weight := weightSelector(optimize)
	// Penalización de transbordo (tiempo perdido en la terminal al cambiar de modo).
	// Solo afecta al peso cuando se optimiza por TIEMPO; para coste no hay coste
	// monetario de transbordo en la Fase 2 (diferido). En ambos criterios la ETA
	// reportada incluye el transbordo (assemblePlan).
	transship := func(node uuid.UUID) int64 {
		if optimize != OptimizeTime {
			return 0
		}
		return transshipEta(terminals, node, req.CargoVolume)
	}
	path, err := dijkstra(adj, req.Origin, req.Destination, weight, terminals, transship)
	if err != nil {
		return RoutePlan{}, err
	}

	return p.assemblePlan(req, path, terminals)
}

// transshipEta devuelve el tiempo de transbordo (sim-segundos) en la terminal de un
// nodo para un volumen dado (0 si el nodo no tiene terminal).
func transshipEta(terminals map[uuid.UUID]terminalInfo, node uuid.UUID, volume int64) int64 {
	t, ok := terminals[node]
	if !ok {
		return 0
	}
	return transshipmentSeconds(volume, t.perHour)
}

// transshipmentSeconds calcula el tiempo de transbordo (sim-segundos) de un volumen
// en una terminal de tasa perHour. Redondeo a HORAS (granularidad de la ETA de los
// tramos) con suelo de una hora; réplica de la fórmula del motor de tránsito de
// world (world/fleet) para que la ETA planificada no diverja de la ejecución.
func transshipmentSeconds(volume, perHour int64) int64 {
	if volume <= 0 || perHour <= 0 {
		return 3600
	}
	hours := (volume + perHour - 1) / perHour
	if hours < 1 {
		hours = 1
	}
	return hours * 3600
}

// assemblePlan construye el RoutePlan a partir del camino: ETAs por tramo, ETA
// total (tramos + transbordos), coste estimado (escalado por el volumen) y las
// terminales de transbordo en los cambios de modo. El grafo garantiza (dijkstra)
// que todo cambio de modo ocurre en un nodo con terminal, así que
// transshipment_terminal_id siempre está presente donde cambia el modo.
func (p *dijkstraPlanner) assemblePlan(req PlanRequest, path []planEdge, terminals map[uuid.UUID]terminalInfo) (RoutePlan, error) {
	var totalEta, baseCost int64
	var ok bool
	for i := range path {
		if totalEta, ok = addNonNeg(totalEta, path[i].eta); !ok {
			return RoutePlan{}, ErrOverflow
		}
		if baseCost, ok = addNonNeg(baseCost, path[i].cost); !ok {
			return RoutePlan{}, ErrOverflow
		}
		// Cambio de modo: suma el tiempo de transbordo de la terminal a la ETA total
		// (el transbordo consume tiempo en la terminal, GDD 7.3).
		if i+1 < len(path) && path[i].mode != path[i+1].mode {
			if totalEta, ok = addNonNeg(totalEta, transshipEta(terminals, path[i].to, req.CargoVolume)); !ok {
				return RoutePlan{}, ErrOverflow
			}
		}
	}

	estimated, err := scaleByVolume(baseCost, req.CargoVolume)
	if err != nil {
		return RoutePlan{}, err
	}

	legs := make([]RoutePlanLeg, len(path))
	for i, e := range path {
		leg := RoutePlanLeg{Seq: i, LinkID: e.linkID, Mode: e.mode, EtaSimSeconds: e.eta}
		if i+1 < len(path) && e.mode != path[i+1].mode {
			if t, present := terminals[e.to]; present {
				id := t.id
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

// ─── Dijkstra con min-heap sobre estados (nodo, modo de llegada) ─────────────

// state es un estado del grafo expandido: un nodo alcanzado por un enlace de un
// modo concreto. El modo de llegada condiciona los enlaces de salida admisibles:
// un cambio de modo (multimodal) solo es transitable en un nodo con terminal
// intermodal (GDD 7.3). El origen usa el modo sentinela "" (sin llegada previa:
// cualquier primer modo vale).
type state struct {
	node uuid.UUID
	mode string
}

// dijkstra calcula el camino de coste mínimo (según weight) entre origin y dest
// sobre el grafo EXPANDIDO por modo de llegada. Un cambio de modo solo se permite
// en un nodo con terminal (terminals[node] presente) y suma transshipPenalty(node)
// al peso; sin terminal, ese cambio no es transitable. Devuelve las aristas del
// camino en orden (origen→destino) o ErrNoRoute si dest es inalcanzable con esas
// restricciones. Pesos no negativos ⇒ al extraer un estado del heap su distancia es
// definitiva.
func dijkstra(adj map[uuid.UUID][]planEdge, origin, dest uuid.UUID, weight func(planEdge) int64,
	terminals map[uuid.UUID]terminalInfo, transshipPenalty func(node uuid.UUID) int64) ([]planEdge, error) {
	start := state{node: origin, mode: ""}
	dist := map[state]int64{start: 0}
	prevEdge := map[state]planEdge{}
	prevState := map[state]state{}
	settled := map[state]bool{}

	pq := &stateHeap{{st: start, dist: 0}}
	var end state
	found := false
	for pq.Len() > 0 {
		cur := heap.Pop(pq).(stateDist)
		if settled[cur.st] {
			continue // entrada obsoleta (relajación posterior mejor)
		}
		settled[cur.st] = true
		if cur.st.node == dest {
			end = cur.st
			found = true
			break
		}
		for _, e := range adj[cur.st.node] {
			// Cambio de modo: solo transitable en un nodo con terminal intermodal.
			modeChange := cur.st.mode != "" && e.mode != cur.st.mode
			if modeChange {
				if _, ok := terminals[cur.st.node]; !ok {
					continue
				}
			}
			next := state{node: e.to, mode: e.mode}
			if settled[next] {
				continue
			}
			step := weight(e)
			if modeChange {
				step += transshipPenalty(cur.st.node)
			}
			nd, ok := addNonNeg(cur.dist, step)
			if !ok {
				continue // desbordamiento: tratar como inalcanzable por esta arista
			}
			if best, seen := dist[next]; !seen || nd < best {
				dist[next] = nd
				prevEdge[next] = e
				prevState[next] = cur.st
				heap.Push(pq, stateDist{st: next, dist: nd})
			}
		}
	}

	if !found {
		return nil, fmt.Errorf("%w (%s → %s)", ErrNoRoute, origin, dest)
	}

	// Reconstrucción origen→destino recorriendo los estados previos.
	var rev []planEdge
	for s := end; s != start; s = prevState[s] {
		rev = append(rev, prevEdge[s])
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev, nil
}

// stateDist es una entrada del min-heap de Dijkstra (estado con su distancia
// tentativa).
type stateDist struct {
	st   state
	dist int64
}

// stateHeap es un min-heap por distancia (container/heap).
type stateHeap []stateDist

func (h stateHeap) Len() int           { return len(h) }
func (h stateHeap) Less(i, j int) bool { return h[i].dist < h[j].dist }
func (h stateHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *stateHeap) Push(x any)        { *h = append(*h, x.(stateDist)) }
func (h *stateHeap) Pop() any {
	old := *h
	n := len(old)
	it := old[n-1]
	*h = old[:n-1]
	return it
}
