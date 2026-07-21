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
	buildingTypes map[string]botsdk.BuildingType
	recipes       map[string]botsdk.Recipe
	vehicleTypes  map[string]botsdk.VehicleType
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
		buildingTypes:      map[string]botsdk.BuildingType{},
		recipes:            map[string]botsdk.Recipe{},
		vehicleTypes:       map[string]botsdk.VehicleType{},
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
	items, err := botsdk.CollectAll(ctx, func(ctx context.Context, cursor string) (botsdk.Page[botsdk.Product], error) {
		return c.Products(ctx, botsdk.ProductsQuery{PageQuery: botsdk.PageQuery{Cursor: cursor, Limit: 200}})
	})
	if err != nil {
		return botsdk.Product{}, fmt.Errorf("bots: cargando el catálogo de productos: %w", err)
	}
	for _, p := range items {
		st.products[p.Code] = p
	}
	p, ok := st.products[code]
	if !ok {
		return botsdk.Product{}, fmt.Errorf("bots: el producto %q no existe en el catálogo", code)
	}
	return p, nil
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
