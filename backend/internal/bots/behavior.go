package bots

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/lokiteitor/global-market/backend/pkg/botsdk"
)

// simDaySeconds es la duración de un día de juego en segundos de sim-time
// (GDD 1.1; mismo valor que internal/sim/simtime.SimDay, replicado aquí
// porque los arquetipos solo dependen del SDK público).
const simDaySeconds int64 = 86_400

// Behavior es un arquetipo de bot (ADR-024): reglas fijas auditables sobre la
// API pública. Decide es UNA pasada de decisión idempotente que el
// orquestador invoca periódicamente; debe tolerar re-ejecuciones y estados a
// medias (el estado observable de la API manda; State solo cachea IDs).
type Behavior interface {
	// Name devuelve el nombre estable del arquetipo (p. ej. "coal_producer").
	Name() string
	// Decide ejecuta una pasada de decisión con el cliente del SDK y el
	// estado propio del bot.
	Decide(ctx context.Context, c *botsdk.Client, st *State) error
}

// State es el estado local de UN bot: cachés de IDs ya descubiertos por la
// API y memoria de corto plazo de la sesión. Se puede perder en cualquier
// momento (reinicio del bot): Decide lo reconstruye desde la API.
type State struct {
	// AccountID es la cuenta del bot (descubierta con /auth/me).
	AccountID string
	// Watermark es el último watermark del join WS (0 sin join); el bot
	// re-sincroniza por REST cuando cambia tras una reconexión.
	Watermark int64
	// LastCash es la caja observada en la última pasada (para la métrica
	// ii_bot_cash); válida solo si LastCashValid.
	LastCash      int64
	LastCashValid bool

	// Cachés de catálogo (por code) y de mundo, inmutables en la práctica.
	products      map[string]botsdk.Product
	productsByID  map[string]botsdk.Product
	buildingTypes map[string]botsdk.BuildingType
	recipes       map[string]botsdk.Recipe
	vehicleTypes  map[string]botsdk.VehicleType
	// linkLengthM cachea linkID → longitud del enlace en metros (grafo común,
	// estable): base del coste de combustible estimado del transportista.
	linkLengthM map[string]int64
	// nodeByBuilding cachea buildingID → nodeID del grafo logístico.
	nodeByBuilding map[string]string
	// routeByKey cachea "origen→destino" → routeID de rutas propias creadas.
	routeByKey map[string]string

	// Productores: implantación descubierta/creada.
	depositID    string
	depositX     float64
	depositY     float64
	regionID     string
	concessionID string
	mineID       string
	mineNodeID   string

	// Transformador industrial: planta descubierta/creada y el índice del
	// emplazamiento candidato en curso (la búsqueda de suelo libre avanza al
	// siguiente cuando el sistema rechaza el anterior).
	plantID     string
	plantNodeID string
	siteIndex   int

	// pendingAcceptances registra las publicaciones ya aceptadas por este
	// proceso (publicationID → acceptanceID) para no re-aceptar en la ventana
	// de sorteo. Si se pierde (reinicio), el colateral congelado limita el
	// daño: una re-aceptación exige stock/caja libres reales.
	pendingAcceptances map[string]string

	// lastBuyPrice recuerda el último precio unitario pagado por producto
	// (productID → precio) para calcular el margen de re-listado del trader.
	lastBuyPrice map[string]int64
}

// PendingAcceptance devuelve la aceptación registrada por este proceso para
// la publicación dada (para tests y tooling de auditoría).
func (s *State) PendingAcceptance(publicationID string) (string, bool) {
	id, ok := s.pendingAcceptances[publicationID]
	return id, ok
}

// NewState construye un State vacío listo para usarse.
func NewState() *State {
	return &State{
		products:           map[string]botsdk.Product{},
		productsByID:       map[string]botsdk.Product{},
		buildingTypes:      map[string]botsdk.BuildingType{},
		recipes:            map[string]botsdk.Recipe{},
		vehicleTypes:       map[string]botsdk.VehicleType{},
		linkLengthM:        map[string]int64{},
		nodeByBuilding:     map[string]string{},
		routeByKey:         map[string]string{},
		pendingAcceptances: map[string]string{},
		lastBuyPrice:       map[string]int64{},
	}
}

// base agrupa lo común de los arquetipos: identidad, logging de decisiones y
// métricas.
type base struct {
	bot       string // nombre de la cuenta del bot (etiqueta de métricas)
	archetype string
	log       *slog.Logger
	metrics   *Metrics

	// Latido de la pasada en curso (ver pass/idle): passActed marca si ya se
	// registró alguna decisión, idleReason el motivo del PRIMER no-op observado
	// (la causa más aguas arriba de la inacción) e idleAttrs el detalle de
	// todos ellos. Solo los toca la goroutine de la sesión del bot, que es la
	// única que invoca Decide.
	passActed  bool
	idleReason string
	idleAttrs  []any
}

func newBase(bot, archetype string, logger *slog.Logger, metrics *Metrics) base {
	if logger == nil {
		logger = slog.Default()
	}
	if metrics == nil {
		metrics = NewMetrics(nil)
	}
	return base{
		bot:       bot,
		archetype: archetype,
		log:       logger.With(slog.String("bot", bot), slog.String("archetype", archetype)),
		metrics:   metrics,
	}
}

// decide registra una decisión auditables: log slog INFO estructurado (bot,
// arquetipo, decisión, motivo, ids) + métrica ii_bot_decisions_total.
func (b *base) decide(decision, reason string, attrs ...any) {
	args := append([]any{slog.String("decision", decision), slog.String("reason", reason)}, attrs...)
	b.log.Info("decisión de bot", args...)
	b.metrics.Decisions.WithLabelValues(b.bot, decision).Inc()
	b.passActed = true
}

// idleFallbackReason es el motivo de una pasada ociosa que ninguna etapa anotó
// con idle: no debería ocurrir, y verlo en el log señala un camino de no-op sin
// instrumentar (no un bot atascado).
const idleFallbackReason = "idle_pass"

// idle ANOTA por qué una etapa de la pasada no hizo nada. No emite decisión por
// sí misma: si la pasada acaba tomando alguna, la anotación se descarta. El
// motivo que sobrevive es el PRIMERO anotado (la causa más aguas arriba: un
// transformador sin insumos reporta awaiting_inputs, no el sell_already_active
// que también es cierto), y el detalle de cada anotación viaja agrupado bajo su
// propio motivo.
func (b *base) idle(reason string, attrs ...any) {
	if b.idleReason == "" {
		b.idleReason = reason
	}
	b.idleAttrs = append(b.idleAttrs, slog.Group(reason, attrs...))
}

// pass envuelve UNA pasada de decisión y garantiza su LATIDO: si run termina
// sin error y no registró ninguna decisión, emite la decisión terminal `wait`
// con el motivo anotado por las etapas. Sin esto una pasada legítimamente
// ociosa (insumos en camino, venta ya activa, cola llena) no deja rastro ni en
// el log ni en ii_bot_decisions_total, y un bot sano parado es indistinguible
// de uno colgado: el diagnóstico exige volcar goroutines. Con el latido, un bot
// atascado se detecta por TASA de decisiones.
//
// Una pasada que falla NO emite latido: el error ya deja rastro propio (log de
// error + ii_bot_errors_total en el orquestador).
func (b *base) pass(run func() error) error {
	b.passActed = false
	b.idleReason = ""
	b.idleAttrs = nil
	if err := run(); err != nil {
		return err
	}
	if b.passActed {
		return nil
	}
	reason := b.idleReason
	if reason == "" {
		reason = idleFallbackReason
	}
	b.decide("wait", reason, b.idleAttrs...)
	return nil
}

// ─── Descubrimiento compartido vía SDK ──────────────────────────────────────

// ensureIdentity resuelve y cachea la cuenta del bot.
func ensureIdentity(ctx context.Context, c *botsdk.Client, st *State) error {
	if st.AccountID != "" {
		return nil
	}
	me, err := c.Me(ctx)
	if err != nil {
		return fmt.Errorf("bots: resolviendo la identidad: %w", err)
	}
	st.AccountID = me.ID
	return nil
}

// productByCode cachea y devuelve un producto del catálogo por code.
func productByCode(ctx context.Context, c *botsdk.Client, st *State, code string) (botsdk.Product, error) {
	if p, ok := st.products[code]; ok {
		return p, nil
	}
	if err := loadProducts(ctx, c, st); err != nil {
		return botsdk.Product{}, err
	}
	p, ok := st.products[code]
	if !ok {
		return botsdk.Product{}, fmt.Errorf("bots: el producto %q no existe en el catálogo", code)
	}
	return p, nil
}

// productByID cachea y devuelve un producto del catálogo por id (los
// ingredientes de una receta y las publicaciones referencian el producto por
// id, no por code).
func productByID(ctx context.Context, c *botsdk.Client, st *State, id string) (botsdk.Product, error) {
	if p, ok := st.productsByID[id]; ok {
		return p, nil
	}
	if err := loadProducts(ctx, c, st); err != nil {
		return botsdk.Product{}, err
	}
	p, ok := st.productsByID[id]
	if !ok {
		return botsdk.Product{}, fmt.Errorf("bots: el producto %s no existe en el catálogo", id)
	}
	return p, nil
}

// loadProducts recarga el catálogo completo de productos en las dos cachés
// (por code y por id).
func loadProducts(ctx context.Context, c *botsdk.Client, st *State) error {
	items, err := botsdk.CollectAll(ctx, func(ctx context.Context, cursor string) (botsdk.Page[botsdk.Product], error) {
		return c.Products(ctx, botsdk.ProductsQuery{PageQuery: botsdk.PageQuery{Cursor: cursor, Limit: 200}})
	})
	if err != nil {
		return fmt.Errorf("bots: cargando el catálogo de productos: %w", err)
	}
	for _, p := range items {
		st.products[p.Code] = p
		st.productsByID[p.ID] = p
	}
	return nil
}

// buildingTypeByCode cachea y devuelve un tipo de edificio por code.
func buildingTypeByCode(ctx context.Context, c *botsdk.Client, st *State, code string) (botsdk.BuildingType, error) {
	if bt, ok := st.buildingTypes[code]; ok {
		return bt, nil
	}
	items, err := botsdk.CollectAll(ctx, func(ctx context.Context, cursor string) (botsdk.Page[botsdk.BuildingType], error) {
		return c.BuildingTypes(ctx, botsdk.BuildingTypesQuery{PageQuery: botsdk.PageQuery{Cursor: cursor, Limit: 200}})
	})
	if err != nil {
		return botsdk.BuildingType{}, fmt.Errorf("bots: cargando los tipos de edificio: %w", err)
	}
	for _, bt := range items {
		st.buildingTypes[bt.Code] = bt
	}
	bt, ok := st.buildingTypes[code]
	if !ok {
		return botsdk.BuildingType{}, fmt.Errorf("bots: el tipo de edificio %q no existe en el catálogo", code)
	}
	return bt, nil
}

// recipeByCode cachea y devuelve una receta por code.
func recipeByCode(ctx context.Context, c *botsdk.Client, st *State, code string) (botsdk.Recipe, error) {
	if r, ok := st.recipes[code]; ok {
		return r, nil
	}
	items, err := botsdk.CollectAll(ctx, func(ctx context.Context, cursor string) (botsdk.Page[botsdk.Recipe], error) {
		return c.Recipes(ctx, botsdk.RecipesQuery{PageQuery: botsdk.PageQuery{Cursor: cursor, Limit: 200}})
	})
	if err != nil {
		return botsdk.Recipe{}, fmt.Errorf("bots: cargando las recetas: %w", err)
	}
	for _, r := range items {
		st.recipes[r.Code] = r
	}
	r, ok := st.recipes[code]
	if !ok {
		return botsdk.Recipe{}, fmt.Errorf("bots: la receta %q no existe en el catálogo", code)
	}
	return r, nil
}

// vehicleTypeByCode cachea y devuelve un tipo de vehículo por code.
func vehicleTypeByCode(ctx context.Context, c *botsdk.Client, st *State, code string) (botsdk.VehicleType, error) {
	if vt, ok := st.vehicleTypes[code]; ok {
		return vt, nil
	}
	items, err := botsdk.CollectAll(ctx, func(ctx context.Context, cursor string) (botsdk.Page[botsdk.VehicleType], error) {
		return c.VehicleTypes(ctx, botsdk.VehicleTypesQuery{PageQuery: botsdk.PageQuery{Cursor: cursor, Limit: 200}})
	})
	if err != nil {
		return botsdk.VehicleType{}, fmt.Errorf("bots: cargando los tipos de vehículo: %w", err)
	}
	for _, vt := range items {
		st.vehicleTypes[vt.Code] = vt
	}
	vt, ok := st.vehicleTypes[code]
	if !ok {
		return botsdk.VehicleType{}, fmt.Errorf("bots: el tipo de vehículo %q no existe en el catálogo", code)
	}
	return vt, nil
}

// nodeOfBuilding resuelve (y cachea) el nodo del grafo logístico respaldado
// por un edificio, recorriendo los nodos de su región (o todos si regionID
// está vacío).
func nodeOfBuilding(ctx context.Context, c *botsdk.Client, st *State, regionID, buildingID string) (string, error) {
	if id, ok := st.nodeByBuilding[buildingID]; ok {
		return id, nil
	}
	nodes, err := botsdk.CollectAll(ctx, func(ctx context.Context, cursor string) (botsdk.Page[botsdk.NetworkNode], error) {
		return c.NetworkNodes(ctx, botsdk.NetworkNodesQuery{
			RegionID:  regionID,
			PageQuery: botsdk.PageQuery{Cursor: cursor, Limit: 200},
		})
	})
	if err != nil {
		return "", fmt.Errorf("bots: cargando los nodos del grafo: %w", err)
	}
	for _, n := range nodes {
		if n.BuildingID != "" {
			st.nodeByBuilding[n.BuildingID] = n.ID
		}
	}
	id, ok := st.nodeByBuilding[buildingID]
	if !ok {
		return "", fmt.Errorf("bots: el edificio %s no tiene nodo en el grafo logístico", buildingID)
	}
	return id, nil
}

// cashBalance lee la caja del bot y la cachea en el State para la métrica.
func cashBalance(ctx context.Context, c *botsdk.Client, st *State) (int64, error) {
	page, err := c.ListAccounts(ctx, botsdk.LedgerAccountsQuery{
		Kind:      botsdk.LedgerCash,
		PageQuery: botsdk.PageQuery{Limit: 1},
	})
	if err != nil {
		return 0, fmt.Errorf("bots: leyendo la caja: %w", err)
	}
	if len(page.Items) == 0 {
		return 0, nil
	}
	v, err := page.Items[0].Balance.Int64()
	if err != nil {
		return 0, fmt.Errorf("bots: caja con saldo inválido: %w", err)
	}
	st.LastCash = v
	st.LastCashValid = true
	return v, nil
}

// stockFreeAt devuelve el saldo stock_free de un producto en un almacén
// concreto (0 si no hay cuenta).
func stockFreeAt(ctx context.Context, c *botsdk.Client, productID, warehouseBuildingID string) (int64, error) {
	accs, err := botsdk.CollectAll(ctx, func(ctx context.Context, cursor string) (botsdk.Page[botsdk.LedgerAccount], error) {
		return c.ListAccounts(ctx, botsdk.LedgerAccountsQuery{
			Kind:      botsdk.LedgerStockFree,
			ProductID: productID,
			PageQuery: botsdk.PageQuery{Cursor: cursor, Limit: 200},
		})
	})
	if err != nil {
		return 0, fmt.Errorf("bots: leyendo el stock libre: %w", err)
	}
	for _, a := range accs {
		if a.WarehouseBuildingID == warehouseBuildingID {
			v, err := a.Balance.Int64()
			if err != nil {
				return 0, fmt.Errorf("bots: stock_free con saldo inválido: %w", err)
			}
			return v, nil
		}
	}
	return 0, nil
}

// myOpenPublication busca en el tablón una publicación propia visible del
// kind y producto dados ("" si no hay).
func myOpenPublication(ctx context.Context, c *botsdk.Client, st *State, kind botsdk.PublicationKind, productID string) (*botsdk.Publication, error) {
	var found *botsdk.Publication
	for pub, err := range botsdk.All(ctx, func(ctx context.Context, cursor string) (botsdk.Page[botsdk.Publication], error) {
		return c.Board(ctx, botsdk.BoardQuery{
			Kind:      kind,
			ProductID: productID,
			PageQuery: botsdk.PageQuery{Cursor: cursor, Limit: 200},
		})
	}) {
		if err != nil {
			return nil, fmt.Errorf("bots: consultando el tablón: %w", err)
		}
		if pub.PublisherAccountID == st.AccountID {
			p := pub
			found = &p
			break
		}
	}
	return found, nil
}

// acceptableAgain decide si el bot puede volver a aceptar una publicación que
// este proceso ya aceptó una vez. Mientras su aceptación siga pending_draw la
// respuesta es NO: re-aceptar dentro de la misma ventana de sorteo duplicaría
// el compromiso de stock o caja sobre la misma solicitud. Una vez RESUELTA
// (servida o liberada) la anotación se olvida y la publicación vuelve a ser
// candidata.
//
// Olvidar es tan necesario como recordar: una aceptación está acotada por el
// stock libre del momento, así que una solicitud grande se sirve por lotes
// (el transformador pide 200 de iron_ore y el minero entrega los 50 del lote
// que tenga). Sin olvido el productor serviría UNA vez cada solicitud y el
// resto se quedaría sin contraparte para siempre, con el comprador bloqueado
// además por su propia regla de UNA publicación activa por producto.
func acceptableAgain(ctx context.Context, c *botsdk.Client, st *State, publicationID string) (bool, error) {
	acceptanceID, seen := st.pendingAcceptances[publicationID]
	if !seen {
		return true, nil
	}
	acc, err := c.GetAcceptance(ctx, acceptanceID)
	if err != nil {
		return false, fmt.Errorf("bots: consultando la aceptación %s: %w", acceptanceID, err)
	}
	if acc.Status == botsdk.AcceptancePendingDraw {
		return false, nil
	}
	delete(st.pendingAcceptances, publicationID)
	return true, nil
}

// pendingBatches suma los lotes aún no producidos de las órdenes no terminales
// de un edificio (la cola pendiente real, no las órdenes).
func pendingBatches(ctx context.Context, c *botsdk.Client, buildingID string) (int, error) {
	batches, err := botsdk.CollectAll(ctx, func(ctx context.Context, cursor string) (botsdk.Page[botsdk.ProductionBatch], error) {
		return c.ListProductionBatches(ctx, buildingID, botsdk.ProductionBatchesQuery{
			PageQuery: botsdk.PageQuery{Cursor: cursor, Limit: 200},
		})
	})
	if err != nil {
		return 0, fmt.Errorf("bots: listando la cola de producción: %w", err)
	}
	pending := 0
	for _, b := range batches {
		switch b.Status {
		case botsdk.BatchCompleted, botsdk.BatchCancelled:
			continue
		default:
			pending += b.BatchesQueued - b.BatchesDone
		}
	}
	return pending, nil
}

// referencePrice devuelve el precio de referencia de un producto y la fuente
// usada (para la traza auditable): el CIERRE de la vela OHLC más reciente
// dentro de la ventana windowSim (velas de contratos realmente liquidados, GDD
// 5.2) o, si el mercado aún no tiene historia, el base_price del catálogo. El
// listado OHLC es cronológico ascendente: la última vela de la ventana es la
// más reciente.
func referencePrice(ctx context.Context, c *botsdk.Client, product botsdk.Product, windowSim int64) (int64, string, error) {
	base, err := product.BasePrice.Int64()
	if err != nil {
		return 0, "", fmt.Errorf("bots: base_price inválido de %s: %w", product.Code, err)
	}
	simNow := c.SimTimeSeconds()
	if simNow > 0 && windowSim > 0 {
		from := simNow - windowSim
		if from < 0 {
			from = 0
		}
		candles, err := c.OHLC(ctx, botsdk.OhlcQuery{ProductID: product.ID, FromSim: from, Limit: 200})
		if err != nil {
			return 0, "", fmt.Errorf("bots: consultando el OHLC de %s: %w", product.Code, err)
		}
		for i := len(candles) - 1; i >= 0; i-- {
			close, cerr := candles[i].ClosePrice.Int64()
			if cerr == nil && close > 0 {
				return close, "ohlc", nil
			}
		}
	}
	return base, "base_price", nil
}

// vehiclePolicy es la política de flota de un arquetipo que despacha
// cargamentos: qué compra y cuántos vehículos como máximo.
type vehiclePolicy struct {
	typeCode string
	max      int
}

// vehicleSlot es el resultado de asegurar capacidad de transporte en un nodo.
// Distingue los tres desenlaces que el llamante trata distinto: hay vehículo
// listo para cargar, se puso uno EN CAMINO (viaje en vacío: no hay nada que
// despachar en esta pasada, pero la espera es finita) o no hay nada que hacer
// aún.
type vehicleSlot struct {
	// id es el vehículo idle en el nodo pedido ("" si no hay).
	id string
	// ready indica que id es utilizable ya (idle en el nodo).
	ready bool
	// repositioning indica que se despachó un viaje EN VACÍO hacia el nodo: el
	// vehículo llegará y una pasada posterior podrá cargar.
	repositioning bool
}

// ensureVehicleAt asegura capacidad de transporte en un nodo, en este orden:
//
//  1. un vehículo propio idle YA situado en el nodo → listo;
//  2. flota incompleta → compra uno entregado allí (nace donde hace falta);
//  3. flota completa con algún vehículo idle en OTRO nodo → lo REPOSICIONA en
//     vacío hasta el nodo (decisión reposition_vehicle);
//  4. flota completa y todos ocupados → espera auditada.
//
// El paso (3) es lo que da liveness al ciclo: una entrega deja el vehículo idle
// en el nodo DESTINO, y ningún cargamento futuro nace ahí, así que sin viaje en
// vacío el vehículo quedaría varado para siempre y el bot no podría cumplir
// ningún contrato posterior (aceptándolos igualmente hasta quemar su garantía).
func ensureVehicleAt(ctx context.Context, c *botsdk.Client, st *State, b *base, pol vehiclePolicy, nodeID string) (vehicleSlot, error) {
	vehicles, err := botsdk.CollectAll(ctx, func(ctx context.Context, cursor string) (botsdk.Page[botsdk.Vehicle], error) {
		return c.ListVehicles(ctx, botsdk.VehiclesQuery{PageQuery: botsdk.PageQuery{Cursor: cursor, Limit: 200}})
	})
	if err != nil {
		return vehicleSlot{}, fmt.Errorf("bots: listando la flota: %w", err)
	}
	var idleElsewhere *botsdk.Vehicle
	for i, v := range vehicles {
		if v.Status != botsdk.VehicleIdle {
			continue
		}
		if v.Position.AtNodeID == nodeID {
			return vehicleSlot{id: v.ID, ready: true}, nil
		}
		if idleElsewhere == nil && v.Position.AtNodeID != "" {
			idleElsewhere = &vehicles[i]
		}
	}
	vt, err := vehicleTypeByCode(ctx, c, st, pol.typeCode)
	if err != nil {
		return vehicleSlot{}, err
	}
	if len(vehicles) >= pol.max {
		if idleElsewhere == nil {
			b.decide("wait", "vehicle_busy",
				slog.String("node_id", nodeID), slog.Int("fleet", len(vehicles)))
			return vehicleSlot{}, nil
		}
		return repositionVehicleTo(ctx, c, st, b, vt, *idleElsewhere, nodeID)
	}
	v, err := c.PurchaseVehicle(ctx, botsdk.VehiclePurchase{
		VehicleTypeID:  vt.ID,
		DeliveryNodeID: nodeID,
	})
	if err != nil {
		if code, ok := blockedCode(err); ok {
			b.decide("blocked", code, slog.String("step", "buy_vehicle"), slog.String("node_id", nodeID))
			return vehicleSlot{}, nil
		}
		if botsdk.IsCode(err, "VALIDATION_ERROR") {
			// Nodo sin enlace del modo del vehículo: sin red del modo no hay
			// entrega posible; se espera a que exista infraestructura.
			b.decide("wait", "node_not_accessible_for_mode",
				slog.String("node_id", nodeID), slog.String("mode", string(vt.Mode)))
			return vehicleSlot{}, nil
		}
		return vehicleSlot{}, fmt.Errorf("bots: comprando el vehículo %s: %w", pol.typeCode, err)
	}
	price, _ := vt.PurchasePrice.Int64()
	b.decide("buy_vehicle", "no_vehicle_available",
		slog.String("vehicle_id", v.ID),
		slog.String("vehicle_type", pol.typeCode),
		slog.Int64("purchase_price", price),
		slog.String("delivery_node_id", nodeID))
	return vehicleSlot{id: v.ID, ready: true}, nil
}

// repositionVehicleTo pone en camino EN VACÍO un vehículo idle varado en otro
// nodo hasta el nodo de recogida: planifica la ruta desde su posición REAL, la
// materializa y la despacha sin carga. Devuelve repositioning=true (el vehículo
// no está disponible todavía: llegará y una pasada posterior cargará). Toda
// rama sin salida deja decisión auditable.
func repositionVehicleTo(ctx context.Context, c *botsdk.Client, st *State, b *base, vt botsdk.VehicleType, v botsdk.Vehicle, nodeID string) (vehicleSlot, error) {
	from := v.Position.AtNodeID
	routeID, ok, err := ensureRoute(ctx, c, st, b, vt.Mode, from, nodeID)
	if err != nil || !ok {
		return vehicleSlot{}, err // sin ruta: espera ya registrada
	}
	if _, err := c.RepositionVehicle(ctx, v.ID, routeID); err != nil {
		if code, blocked := blockedCode(err); blocked {
			b.decide("blocked", code, slog.String("step", "reposition_vehicle"),
				slog.String("vehicle_id", v.ID), slog.String("node_id", nodeID))
			return vehicleSlot{}, nil
		}
		if botsdk.IsCode(err, "VALIDATION_ERROR") {
			// Combustible insuficiente para el viaje en vacío o ruta inservible
			// para el modo: el vehículo ya no sirve para esta recogida.
			b.decide("wait", "reposition_not_possible",
				slog.String("vehicle_id", v.ID),
				slog.String("origin_node_id", from),
				slog.String("destination_node_id", nodeID))
			return vehicleSlot{}, nil
		}
		return vehicleSlot{}, fmt.Errorf("bots: reposicionando el vehículo %s: %w", v.ID, err)
	}
	b.decide("reposition_vehicle", "vehicle_elsewhere",
		slog.String("vehicle_id", v.ID),
		slog.String("route_id", routeID),
		slog.String("origin_node_id", from),
		slog.String("destination_node_id", nodeID))
	return vehicleSlot{repositioning: true}, nil
}

// ensureRoute devuelve la ruta propia origen→destino para un modo, creándola
// desde un plan del Logistics Service si no existe (idempotente por nombre
// determinista).
func ensureRoute(ctx context.Context, c *botsdk.Client, st *State, b *base, mode botsdk.LinkMode, originNodeID, destNodeID string) (string, bool, error) {
	plan, ok, err := planRoute(ctx, c, st, b, mode, originNodeID, destNodeID)
	if err != nil || !ok {
		return "", false, err
	}
	return ensureRouteFromPlan(ctx, c, st, b, plan, originNodeID, destNodeID)
}

// planRoute calcula el plan de ruta origen→destino restringido a un modo,
// cacheando la longitud de sus enlaces. ok=false (con la espera registrada) si
// el grafo aún no ofrece camino.
func planRoute(ctx context.Context, c *botsdk.Client, st *State, b *base, mode botsdk.LinkMode, originNodeID, destNodeID string) (botsdk.RoutePlan, bool, error) {
	plan, err := c.PlanRoute(ctx, botsdk.RoutePlanRequest{
		OriginNodeID:      originNodeID,
		DestinationNodeID: destNodeID,
		Modes:             []botsdk.LinkMode{mode},
	})
	if err != nil {
		if botsdk.IsCode(err, "NO_ROUTE_FOUND") {
			b.decide("wait", "no_route",
				slog.String("origin_node_id", originNodeID),
				slog.String("destination_node_id", destNodeID),
				slog.String("mode", string(mode)))
			return botsdk.RoutePlan{}, false, nil
		}
		return botsdk.RoutePlan{}, false, fmt.Errorf("bots: calculando el plan de ruta: %w", err)
	}
	return plan, true, nil
}

// ensureRouteFromPlan materializa (o reutiliza) la ruta propia de un plan ya
// calculado. El nombre determinista es la clave de idempotencia entre reinicios
// del bot.
func ensureRouteFromPlan(ctx context.Context, c *botsdk.Client, st *State, b *base, plan botsdk.RoutePlan, originNodeID, destNodeID string) (string, bool, error) {
	key := originNodeID + "→" + destNodeID
	if id, ok := st.routeByKey[key]; ok {
		return id, true, nil
	}
	name := routeName(originNodeID, destNodeID)
	routes, err := botsdk.CollectAll(ctx, func(ctx context.Context, cursor string) (botsdk.Page[botsdk.Route], error) {
		return c.ListRoutes(ctx, botsdk.RoutesQuery{PageQuery: botsdk.PageQuery{Cursor: cursor, Limit: 200}})
	})
	if err != nil {
		return "", false, fmt.Errorf("bots: listando rutas: %w", err)
	}
	for _, r := range routes {
		if r.Name == name {
			st.routeByKey[key] = r.ID
			return r.ID, true, nil
		}
	}
	legs := make([]string, len(plan.Legs))
	for i, leg := range plan.Legs {
		legs[i] = leg.LinkID
	}
	route, err := c.CreateRoute(ctx, botsdk.RouteCreate{
		Name: name,
		Kind: botsdk.RouteOnDemand,
		Legs: legs,
	})
	if err != nil {
		return "", false, fmt.Errorf("bots: creando la ruta %s: %w", name, err)
	}
	st.routeByKey[key] = route.ID
	b.decide("create_route", "dispatch_needs_route",
		slog.String("route_id", route.ID),
		slog.String("origin_node_id", originNodeID),
		slog.String("destination_node_id", destNodeID),
		slog.Int("legs", len(legs)))
	return route.ID, true, nil
}

// routeName deriva el nombre determinista de la ruta de despacho
// origen→destino: es la clave de idempotencia entre reinicios del bot (los
// IDs van completos: los prefijos de un UUIDv7 son casi idénticos entre IDs
// contemporáneos).
func routeName(originNodeID, destNodeID string) string {
	return fmt.Sprintf("bot %s→%s", originNodeID, destNodeID)
}

// planDistanceM suma la longitud de los enlaces de un plan de ruta (metros),
// cacheando el grafo por enlace. Es la base física del coste de combustible
// estimado: el contrato no expone la distancia del plan, solo sus enlaces.
func planDistanceM(ctx context.Context, c *botsdk.Client, st *State, plan botsdk.RoutePlan) (int64, error) {
	missing := false
	for _, leg := range plan.Legs {
		if _, ok := st.linkLengthM[leg.LinkID]; !ok {
			missing = true
			break
		}
	}
	if missing {
		links, err := botsdk.CollectAll(ctx, func(ctx context.Context, cursor string) (botsdk.Page[botsdk.NetworkLink], error) {
			return c.NetworkLinks(ctx, botsdk.NetworkLinksQuery{PageQuery: botsdk.PageQuery{Cursor: cursor, Limit: 200}})
		})
		if err != nil {
			return 0, fmt.Errorf("bots: cargando los enlaces del grafo: %w", err)
		}
		for _, l := range links {
			st.linkLengthM[l.ID] = int64(l.LengthM)
		}
	}
	var total int64
	for _, leg := range plan.Legs {
		length, ok := st.linkLengthM[leg.LinkID]
		if !ok {
			return 0, fmt.Errorf("bots: el enlace %s del plan no está en el grafo", leg.LinkID)
		}
		total += length
	}
	return total, nil
}

// ─── Reglas puras compartidas (testeables sin API) ──────────────────────────

// applyBP aplica una fracción en basis points a un importe (redondeo a la
// baja): applyBP(100, 9500) = 95.
func applyBP(amount, bp int64) int64 {
	return amount * bp / 10_000
}

// applyBPCeil aplica una fracción en basis points redondeando al alza:
// applyBPCeil(54, 11500) = 63 (54×1,15 = 62,1 → 63).
func applyBPCeil(amount, bp int64) int64 {
	return (amount*bp + 9_999) / 10_000
}

// ceilDiv divide dos enteros no negativos redondeando al alza (coste unitario
// de una receta: nunca se subestima el coste por el redondeo).
func ceilDiv(a, b int64) int64 {
	if b <= 0 {
		return 0
	}
	return (a + b - 1) / b
}

// acceptQty decide la cantidad a aceptar de una publicación: el mínimo entre
// lo restante y los topes (0 si no alcanza el lote mínimo efectivo
// min(minLot, remaining)).
func acceptQty(remaining, minLot int64, caps ...int64) int64 {
	qty := remaining
	for _, c := range caps {
		qty = min(qty, c)
	}
	minAccept := min(minLot, remaining)
	if minAccept <= 0 {
		minAccept = 1
	}
	if qty < minAccept {
		return 0
	}
	return qty
}
