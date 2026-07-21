package stress

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/lokiteitor/global-market/backend/pkg/botsdk"
)

// El PERFIL DE CARGA del harness NO intenta jugar bien: los arquetipos «que
// juegan» (heurísticas auditables del GDD §13.3) viven en internal/bots. Aquí
// los comportamientos son LIGEROS y de ALTA FRECUENCIA, elegidos para ejercitar
// los caminos calientes del sistema —tablón con filtros, publicación y
// cancelación, aceptación, ledger, catálogo del mundo y planificación de
// rutas— con una mezcla parametrizable de LECTURA y ESCRITURA
// (II_STRESS_WRITE_RATIO). Todo pasa por pkg/botsdk contra la API pública.

// Parámetros de las escrituras del harness. Deliberadamente diminutos: la
// publicación de carga debe costar céntimos para que la caja del bot no se
// agote y la corrida mida el sistema, no la contabilidad del bot.
const (
	pubMaxQty            = 5
	pubMaxUnitPrice      = 50
	pubDeliverySimSecs   = 86_400 // un día-sim de plazo declarado
	boardPageLimit       = 50
	catalogPageLimit     = 50
	networkPageLimit     = 100
	maxTrackedPubs       = 64 // publicaciones vivas que un bot recuerda para cancelar
	maxCachedSells       = 32 // ofertas sell en caché para la operación de aceptación
	acceptSafetyDivider  = 2  // el bot solo acepta si el coste cabe holgado en su caja
	warehouseNodeMaxPage = 200
	// sellMinUnitPrice mantiene el valor de toda oferta sell por encima de 10
	// unidades menores: la garantía del publicador es el 10% ENTERO del valor y
	// una oferta más barata la dejaría en 0, que no es un bloqueo real de
	// colateral y no ejercitaría el camino que se quiere medir.
	sellMinUnitPrice = 10
	// sellGuaranteePercent es la garantía del publicador de una sell (GDD 5.3):
	// el harness la descuenta de su estimación de caja para no autoinfligirse
	// INSUFFICIENT_COLLATERAL.
	sellGuaranteePercent = 10
)

// weighted es una opción ponderada del selector de operaciones.
type weighted struct {
	op     Op
	weight int
}

// readMix es la mezcla de LECTURAS por arquetipo: cada arquetipo calienta la
// familia de endpoints que le corresponde en el GDD §13.2.
var readMix = map[Archetype][]weighted{
	ArchetypeProducer: {
		{OpWorldRead, 4}, {OpBoardRead, 3}, {OpLedgerRead, 2}, {OpContractsRead, 1},
	},
	ArchetypeTrader: {
		{OpBoardRead, 6}, {OpMarketRead, 2}, {OpLedgerRead, 1}, {OpContractsRead, 1},
	},
	ArchetypeFreighter: {
		{OpNetworkRead, 4}, {OpBoardRead, 3}, {OpFleetRead, 2}, {OpContractsRead, 1},
	},
	ArchetypeTransformer: {
		{OpWorldRead, 3}, {OpBoardRead, 3}, {OpLedgerRead, 2}, {OpContractsRead, 2},
	},
}

// writeMix es la mezcla de ESCRITURAS por arquetipo.
var writeMix = map[Archetype][]weighted{
	ArchetypeProducer: {
		{OpPublish, 5}, {OpCancel, 3}, {OpRoutePlan, 2},
	},
	ArchetypeTrader: {
		{OpPublish, 4}, {OpAccept, 4}, {OpCancel, 2},
	},
	ArchetypeFreighter: {
		{OpRoutePlan, 6}, {OpPublish, 2}, {OpCancel, 2},
	},
	ArchetypeTransformer: {
		{OpPublish, 4}, {OpCancel, 3}, {OpRoutePlan, 3},
	},
}

// nodeRef es un nodo del grafo logístico cacheado por el bot.
type nodeRef struct {
	id       string
	regionID string
}

// livePub es una publicación propia viva, con el instante en que su cooldown
// anti-parpadeo permite cancelarla (el harness NO fuerza 409: respeta la regla).
//
// La cancelación NO devuelve el colateral a la estimación del bot: una
// publicación puede haberse servido en parte antes de cancelarse, así que
// reponer el importe íntegro haría que el harness se creyera con más caja o más
// stock del que tiene y acabaría provocando 422 INSUFFICIENT_COLLATERAL —un
// artefacto del harness que el informe contaría como error INESPERADO—. La
// estimación se mantiene conservadora (la caja, además, se resincroniza con
// cada lectura del ledger) y la dotación de stock se dimensiona con holgura.
type livePub struct {
	id            string
	cooldownUntil time.Time
}

// session es un bot del harness: su cliente del SDK, su caché del mundo y su
// estado de juego mínimo.
type session struct {
	bot    StressBot
	client *botsdk.Client
	log    *slog.Logger
	rng    *rand.Rand
	record func(Result)

	accountID string
	// cash es la estimación de caja del bot en unidades menores: arranca en el
	// capital, se refresca con cada lectura del ledger y se descuenta con cada
	// bloqueo. Evita que la carga se autoinflija INSUFFICIENT_COLLATERAL.
	cash int64
	// sellShare es la fracción de publicaciones que el bot emite como sell
	// mientras le quede dotación (el resto son buy).
	sellShare float64
	// stockProduct, stockNode y stockLeft son la dotación de stock del bot: el
	// producto, el almacén de origen y las unidades que aún puede ofrecer. Es lo
	// que le permite ser CONTRAPARTE del tablón y no solo comprador.
	stockProduct string
	stockNode    string
	stockLeft    int64

	products  []string
	nodes     []nodeRef
	regions   []string
	livePubs  []livePub
	sellCache []botsdk.Publication
}

// newSession construye el bot del harness contra la API pública. capital y
// sellShare vienen de las Options; la dotación de stock, del provisioner.
func newSession(bot StressBot, apiURL string, httpc *http.Client, capital int64, sellShare float64, logger *slog.Logger, seed uint64, record func(Result)) (*session, error) {
	log := logger.With(slog.String("bot", bot.Name), slog.String("archetype", string(bot.Archetype)))
	client, err := botsdk.New(botsdk.Options{
		BaseURL:    apiURL,
		HTTPClient: httpc,
		// SIN reintentos: el harness mide la respuesta CRUDA del sistema bajo
		// prueba. Si el SDK reintentase los 429, el informe escondería
		// justamente el backpressure que la corrida quiere medir.
		MaxRetries: -1,
		Logger:     log,
		UserAgent:  "imperio-stress/1 (" + string(bot.Archetype) + ")",
	})
	if err != nil {
		return nil, err
	}
	s := &session{
		bot:       bot,
		client:    client,
		log:       log,
		rng:       rand.New(rand.NewPCG(seed, seed^0xa24baed4963ee407)),
		record:    record,
		cash:      capital,
		sellShare: sellShare,
		stockLeft: bot.StockQuantity,
	}
	if bot.StockQuantity > 0 && bot.StockProductID != uuid.Nil && bot.StockNodeID != uuid.Nil {
		s.stockProduct = bot.StockProductID.String()
		s.stockNode = bot.StockNodeID.String()
	} else {
		s.stockLeft = 0
	}
	return s, nil
}

// measure ejecuta una llamada al SDK midiéndola y la registra en el colector.
func (s *session) measure(op Op, call func() error) error {
	start := time.Now()
	err := call()
	d := time.Since(start)
	if err != nil {
		r := classify(op, d, err)
		s.record(r)
		s.log.Debug("operación fallida",
			slog.String("op", string(op)),
			slog.String("class", r.Class),
			slog.String("domain_code", r.DomainCode),
			slog.Duration("latency", d))
		return err
	}
	s.record(okResult(op, d))
	return nil
}

// skip registra una operación NO emitida con su motivo auditable.
func (s *session) skip(op Op, reason string) {
	s.record(skippedResult(op, reason))
	s.log.Debug("operación omitida", slog.String("op", string(op)), slog.String("reason", reason))
}

// Login abre la sesión del bot por la API pública (POST /auth/sessions).
func (s *session) Login(ctx context.Context) error {
	return s.measure(OpLogin, func() error {
		out, err := s.client.Login(ctx, s.bot.Name, s.bot.Secret)
		if err != nil {
			return err
		}
		s.accountID = out.Account.ID
		return nil
	})
}

// Logout cierra la sesión (DELETE /auth/sessions/current).
func (s *session) Logout(ctx context.Context) {
	_ = s.measure(OpLogout, func() error { return s.client.Logout(ctx) })
}

// Bootstrap cachea las referencias del mundo que necesitan las escrituras
// (productos y nodos con almacén). Son lecturas reales de la API: cuentan como
// carga y se miden como tales.
func (s *session) Bootstrap(ctx context.Context) {
	_ = s.measure(OpWorldRead, func() error {
		page, err := s.client.Products(ctx, botsdk.ProductsQuery{PageQuery: botsdk.PageQuery{Limit: catalogPageLimit}})
		if err != nil {
			return err
		}
		for _, p := range page.Items {
			s.products = append(s.products, p.ID)
		}
		return nil
	})
	_ = s.measure(OpNetworkRead, func() error {
		page, err := s.client.NetworkNodes(ctx, botsdk.NetworkNodesQuery{
			Kind:      botsdk.NodeWarehouse,
			PageQuery: botsdk.PageQuery{Limit: warehouseNodeMaxPage},
		})
		if err != nil {
			return err
		}
		seenRegion := map[string]bool{}
		for _, n := range page.Items {
			s.nodes = append(s.nodes, nodeRef{id: n.ID, regionID: n.RegionID})
			if n.RegionID != "" && !seenRegion[n.RegionID] {
				seenRegion[n.RegionID] = true
				s.regions = append(s.regions, n.RegionID)
			}
		}
		return nil
	})
	if len(s.products) == 0 || len(s.nodes) == 0 {
		s.log.Warn("mundo sin productos o sin nodos con almacén: el bot solo generará lecturas",
			slog.Int("products", len(s.products)), slog.Int("warehouse_nodes", len(s.nodes)))
	}
}

// Act ejecuta UNA acción del bot: escritura con probabilidad writeRatio,
// lectura en caso contrario.
func (s *session) Act(ctx context.Context, writeRatio float64) {
	if s.rng.Float64() < writeRatio {
		s.execute(ctx, pick(s.rng, writeMix[s.bot.Archetype], OpPublish))
		return
	}
	s.execute(ctx, pick(s.rng, readMix[s.bot.Archetype], OpBoardRead))
}

// execute despacha una operación del catálogo.
func (s *session) execute(ctx context.Context, op Op) {
	switch op {
	case OpBoardRead:
		s.boardRead(ctx)
	case OpWorldRead:
		s.worldRead(ctx)
	case OpNetworkRead:
		s.networkRead(ctx)
	case OpLedgerRead:
		s.ledgerRead(ctx)
	case OpContractsRead:
		s.contractsRead(ctx)
	case OpFleetRead:
		s.fleetRead(ctx)
	case OpMarketRead:
		s.marketRead(ctx)
	case OpPublish:
		s.publish(ctx)
	case OpCancel:
		s.cancel(ctx)
	case OpAccept:
		s.accept(ctx)
	case OpRoutePlan:
		s.routePlan(ctx)
	default:
		s.skip(op, "operación no soportada")
	}
}

// ─── Lecturas ────────────────────────────────────────────────────────────────

// boardRead consulta el tablón con filtros propios del arquetipo (GDD §14.2: el
// tablón SIEMPRE se consulta bajo demanda con filtros, nunca por push).
func (s *session) boardRead(ctx context.Context) {
	q := botsdk.BoardQuery{PageQuery: botsdk.PageQuery{Limit: boardPageLimit}}
	switch s.bot.Archetype {
	case ArchetypeTrader:
		q.Kind = botsdk.PublicationSell
		q.Sort = pickString(s.rng, botsdk.SortUnitPriceAsc, botsdk.SortPublishedAtDesc)
	case ArchetypeFreighter:
		q.Kind = botsdk.PublicationFreight
		q.Sort = botsdk.SortDeadlineAsc
	case ArchetypeProducer:
		q.Kind = botsdk.PublicationBuy
		q.Sort = botsdk.SortUnitPriceDesc
	default:
		q.Kind = pickKind(s.rng)
		q.Sort = botsdk.SortUnitPriceAsc
	}
	if p := s.randomProduct(); p != "" && s.rng.IntN(2) == 0 {
		q.ProductID = p
	}
	if r := s.randomRegion(); r != "" && s.rng.IntN(3) == 0 {
		if q.Kind == botsdk.PublicationBuy {
			q.DestinationRegionID = r
		} else {
			q.OriginRegionID = r
		}
	}
	_ = s.measure(OpBoardRead, func() error {
		page, err := s.client.Board(ctx, q)
		if err != nil {
			return err
		}
		if q.Kind == botsdk.PublicationSell {
			s.cacheSells(page.Items)
		}
		return nil
	})
}

// worldRead rota entre los catálogos y el estado del mundo.
func (s *session) worldRead(ctx context.Context) {
	page := botsdk.PageQuery{Limit: catalogPageLimit}
	switch s.rng.IntN(6) {
	case 0:
		_ = s.measure(OpWorldRead, func() error {
			_, err := s.client.Regions(ctx, botsdk.RegionsQuery{PageQuery: page})
			return err
		})
	case 1:
		_ = s.measure(OpWorldRead, func() error {
			_, err := s.client.Products(ctx, botsdk.ProductsQuery{PageQuery: page})
			return err
		})
	case 2:
		_ = s.measure(OpWorldRead, func() error {
			_, err := s.client.Recipes(ctx, botsdk.RecipesQuery{ProductID: s.randomProduct(), PageQuery: page})
			return err
		})
	case 3:
		_ = s.measure(OpWorldRead, func() error {
			_, err := s.client.Cities(ctx, botsdk.CitiesQuery{RegionID: s.randomRegion(), PageQuery: page})
			return err
		})
	case 4:
		_ = s.measure(OpWorldRead, func() error {
			_, err := s.client.ResourceDeposits(ctx, botsdk.ResourceDepositsQuery{
				RegionID: s.randomRegion(), PageQuery: page,
			})
			return err
		})
	default:
		_ = s.measure(OpWorldRead, func() error {
			_, err := s.client.ListBuildings(ctx, botsdk.BuildingsQuery{PageQuery: page})
			return err
		})
	}
}

// networkRead consulta el grafo logístico (nodos y enlaces con su congestión).
func (s *session) networkRead(ctx context.Context) {
	page := botsdk.PageQuery{Limit: networkPageLimit}
	if s.rng.IntN(2) == 0 {
		_ = s.measure(OpNetworkRead, func() error {
			_, err := s.client.NetworkNodes(ctx, botsdk.NetworkNodesQuery{
				RegionID: s.randomRegion(), PageQuery: page,
			})
			return err
		})
		return
	}
	_ = s.measure(OpNetworkRead, func() error {
		_, err := s.client.NetworkLinks(ctx, botsdk.NetworkLinksQuery{
			RegionID: s.randomRegion(), PageQuery: page,
		})
		return err
	})
}

// ledgerRead consulta las cuentas propias y refresca la estimación de caja.
func (s *session) ledgerRead(ctx context.Context) {
	_ = s.measure(OpLedgerRead, func() error {
		page, err := s.client.ListAccounts(ctx, botsdk.LedgerAccountsQuery{
			Kind: botsdk.LedgerCash, PageQuery: botsdk.PageQuery{Limit: 10},
		})
		if err != nil {
			return err
		}
		for _, acc := range page.Items {
			if v, perr := acc.Balance.Int64(); perr == nil {
				s.cash = v
				break
			}
		}
		return nil
	})
}

// contractsRead consulta los contratos CCRI y CCRI-Flete propios.
func (s *session) contractsRead(ctx context.Context) {
	page := botsdk.PageQuery{Limit: catalogPageLimit}
	if s.bot.Archetype == ArchetypeFreighter {
		_ = s.measure(OpContractsRead, func() error {
			_, err := s.client.ListFreightContracts(ctx, botsdk.FreightContractsQuery{
				Role: botsdk.RoleCarrier, PageQuery: page,
			})
			return err
		})
		return
	}
	_ = s.measure(OpContractsRead, func() error {
		_, err := s.client.ListContracts(ctx, botsdk.ContractsQuery{PageQuery: page})
		return err
	})
}

// fleetRead consulta la flota propia, los cargamentos y el catálogo de vehículos.
func (s *session) fleetRead(ctx context.Context) {
	page := botsdk.PageQuery{Limit: catalogPageLimit}
	switch s.rng.IntN(3) {
	case 0:
		_ = s.measure(OpFleetRead, func() error {
			_, err := s.client.ListVehicles(ctx, botsdk.VehiclesQuery{PageQuery: page})
			return err
		})
	case 1:
		_ = s.measure(OpFleetRead, func() error {
			_, err := s.client.ListShipments(ctx, botsdk.ShipmentsQuery{PageQuery: page})
			return err
		})
	default:
		_ = s.measure(OpFleetRead, func() error {
			_, err := s.client.VehicleTypes(ctx, botsdk.VehicleTypesQuery{PageQuery: page})
			return err
		})
	}
}

// marketRead consulta el histórico OHLC de un producto (velas de contratos
// efectivamente liquidados).
func (s *session) marketRead(ctx context.Context) {
	product := s.randomProduct()
	if product == "" {
		s.skip(OpMarketRead, "sin productos cacheados")
		return
	}
	_ = s.measure(OpMarketRead, func() error {
		_, err := s.client.OHLC(ctx, botsdk.OhlcQuery{ProductID: product, Limit: 50})
		return err
	})
}

// ─── Escrituras ──────────────────────────────────────────────────────────────

// publish crea una publicación diminuta en el tablón. Es la escritura canónica
// del harness: bloquea colateral real por el camino real del CCRI (publicación +
// garantía en el mismo acto) con un coste de céntimos.
//
// Una fracción (II_STRESS_SELL_SHARE) sale como SELL mientras al bot le quede
// dotación: sin ofertas propias en el lado vendedor, la única contraparte
// aceptable sería exógena y finita, y la operación de aceptación —el camino de
// escritura caro— dejaría de escalar con la población.
func (s *session) publish(ctx context.Context) {
	if len(s.livePubs) >= maxTrackedPubs {
		s.skip(OpPublish, "tope de publicaciones vivas del bot")
		return
	}
	if s.stockLeft > 0 && s.rng.Float64() < s.sellShare {
		s.publishSell(ctx)
		return
	}
	s.publishBuy(ctx)
}

// publishBuy publica una solicitud de compra: escrow del 100% del valor.
func (s *session) publishBuy(ctx context.Context) {
	product := s.randomProduct()
	node := s.randomNode()
	if product == "" || node.id == "" {
		s.skip(OpPublish, "mundo sin producto o sin nodo con almacén cacheado")
		return
	}
	qty := int64(1 + s.rng.IntN(pubMaxQty))
	price := int64(1 + s.rng.IntN(pubMaxUnitPrice))
	cost := qty * price
	if s.cash < cost*acceptSafetyDivider {
		s.skip(OpPublish, "caja insuficiente para el escrow")
		return
	}
	_ = s.measure(OpPublish, func() error {
		pub, err := s.client.CreatePublication(ctx, botsdk.PublicationCreate{
			Kind:               botsdk.PublicationBuy,
			ProductID:          product,
			QuantityTotal:      botsdk.Qty(strconv.FormatInt(qty, 10)),
			UnitPrice:          botsdk.MoneyFromInt64(price),
			MinLot:             botsdk.Qty(strconv.FormatInt(qty, 10)),
			DestinationNodeID:  node.id,
			DeliverySimSeconds: pubDeliverySimSecs,
		})
		if err != nil {
			return err
		}
		s.cash -= cost
		s.livePubs = append(s.livePubs, livePub{id: pub.ID, cooldownUntil: pub.CancelCooldownUntil})
		return nil
	})
}

// publishSell publica una oferta de venta sobre la dotación del bot: congela el
// stock en el almacén de origen y bloquea la garantía del 10%. El lote mínimo es
// 1 unidad a propósito: así una misma oferta admite MUCHAS aceptaciones y la
// ventana de sorteo se ejercita con contención real en vez de agotarse al primer
// aceptante.
func (s *session) publishSell(ctx context.Context) {
	qty := min(int64(1+s.rng.IntN(pubMaxQty)), s.stockLeft)
	price := int64(sellMinUnitPrice + s.rng.IntN(pubMaxUnitPrice))
	guarantee := qty * price * sellGuaranteePercent / 100
	if s.cash < guarantee*acceptSafetyDivider {
		s.skip(OpPublish, "caja insuficiente para la garantía de la venta")
		return
	}
	_ = s.measure(OpPublish, func() error {
		pub, err := s.client.CreatePublication(ctx, botsdk.PublicationCreate{
			Kind:               botsdk.PublicationSell,
			ProductID:          s.stockProduct,
			QuantityTotal:      botsdk.Qty(strconv.FormatInt(qty, 10)),
			UnitPrice:          botsdk.MoneyFromInt64(price),
			MinLot:             botsdk.Qty("1"),
			OriginNodeID:       s.stockNode,
			DeliverySimSeconds: pubDeliverySimSecs,
		})
		if err != nil {
			return err
		}
		s.cash -= guarantee
		s.stockLeft -= qty
		s.livePubs = append(s.livePubs, livePub{id: pub.ID, cooldownUntil: pub.CancelCooldownUntil})
		return nil
	})
}

// cancel libera una publicación propia cuyo cooldown anti-parpadeo YA venció.
// Si ninguna lo ha vencido, la operación se omite: el harness genera carga, no
// 409 artificiales contra una regla de dominio conocida.
func (s *session) cancel(ctx context.Context) {
	idx := s.cancellablePub()
	if idx < 0 {
		if len(s.livePubs) == 0 {
			s.skip(OpCancel, "sin publicaciones propias vivas")
		} else {
			s.skip(OpCancel, "cooldown anti-parpadeo aún activo")
		}
		return
	}
	target := s.livePubs[idx]
	s.livePubs = append(s.livePubs[:idx], s.livePubs[idx+1:]...)
	_ = s.measure(OpCancel, func() error {
		_, err := s.client.CancelPublication(ctx, target.id)
		return err
	})
}

// accept acepta una oferta sell del tablón, dentro del presupuesto del bot. Es
// el camino caliente de la ventana de sorteo (escrow + sorteo + contención
// SERIALIZABLE), y por tanto el que NO puede quedarse sin emitir: si la caché de
// candidatas viene vacía, el bot fuerza una consulta DIRIGIDA del tablón antes
// de rendirse, para que la tasa de aceptación siga a la población en lugar de
// depender de que una lectura anterior del arquetipo trajera ofertas.
func (s *session) accept(ctx context.Context) {
	pub, lot, ok := s.affordableSell()
	if !ok {
		s.refreshSells(ctx)
		if pub, lot, ok = s.affordableSell(); !ok {
			s.skip(OpAccept, "sin oferta sell asequible tras consultar el tablón")
			return
		}
	}
	_ = s.measure(OpAccept, func() error {
		if _, err := s.client.Accept(ctx, pub.ID, botsdk.Qty(strconv.FormatInt(lot.quantity, 10)), ""); err != nil {
			return err
		}
		s.cash -= lot.cost
		return nil
	})
}

// routePlan pide un plan de ruta entre dos nodos con almacén de la MISMA región
// (pathfinding sobre la congestión EMA; no persiste nada).
func (s *session) routePlan(ctx context.Context) {
	origin, dest, ok := s.nodePair()
	if !ok {
		s.skip(OpRoutePlan, "sin par de nodos en la misma región")
		return
	}
	_ = s.measure(OpRoutePlan, func() error {
		_, err := s.client.PlanRoute(ctx, botsdk.RoutePlanRequest{
			OriginNodeID:      origin,
			DestinationNodeID: dest,
			Optimize:          pickString(s.rng, botsdk.OptimizeTime, botsdk.OptimizeCost),
		})
		return err
	})
}

// Drain cancela, al terminar la corrida, las publicaciones propias que el
// cooldown permita: higiene del entorno de pruebas. Las que sigan en cooldown
// expiran solas por el TTL de publicación.
//
// NO forma parte del perfil de carga —por eso se mide bajo OpDrainCancel y no
// bajo OpCancel—, pero se MIDE: son peticiones reales del harness contra el
// sistema bajo prueba, y las que fallan aparecen en las métricas del sistema.
// Dejarlas sin contar hacía que el informe atribuyera sus 5xx a «otra ruta u
// otro cliente», que es exactamente la clase de lectura engañosa que este
// harness debe evitar.
func (s *session) Drain(ctx context.Context) (cancelled, left int) {
	for _, pub := range s.livePubs {
		if time.Now().Before(pub.cooldownUntil) {
			left++
			continue
		}
		if err := s.measure(OpDrainCancel, func() error {
			_, err := s.client.CancelPublication(ctx, pub.id)
			return err
		}); err != nil {
			left++
			continue
		}
		cancelled++
	}
	s.livePubs = nil
	return cancelled, left
}

// ─── Selección y caché ───────────────────────────────────────────────────────

// sellLot es la cantidad y el coste de una aceptación candidata.
type sellLot struct {
	quantity int64
	cost     int64
}

// affordableSell elige de la caché una oferta sell ajena que quepa holgada en la
// caja del bot, aceptando exactamente su lote mínimo.
func (s *session) affordableSell() (botsdk.Publication, sellLot, bool) {
	for i := len(s.sellCache) - 1; i >= 0; i-- {
		pub := s.sellCache[i]
		s.sellCache = append(s.sellCache[:i], s.sellCache[i+1:]...)
		if pub.PublisherAccountID == s.accountID {
			continue
		}
		lot, err := pub.MinLot.Int64()
		if err != nil || lot <= 0 {
			continue
		}
		remaining, err := pub.QuantityRemaining.Int64()
		if err != nil || remaining <= 0 {
			continue
		}
		lot = min(lot, remaining)
		price, err := pub.UnitPrice.Int64()
		if err != nil || price <= 0 {
			continue
		}
		cost := lot * price
		if cost <= 0 || s.cash < cost*acceptSafetyDivider {
			continue
		}
		return pub, sellLot{quantity: lot, cost: cost}, true
	}
	return botsdk.Publication{}, sellLot{}, false
}

// refreshSells recarga la caché con una consulta DIRIGIDA del tablón (kind=sell,
// las más baratas primero). Se mide como board_read porque eso es exactamente lo
// que es: una lectura real de la API que el bot necesita para poder aceptar.
func (s *session) refreshSells(ctx context.Context) {
	_ = s.measure(OpBoardRead, func() error {
		page, err := s.client.Board(ctx, botsdk.BoardQuery{
			Kind:      botsdk.PublicationSell,
			Sort:      botsdk.SortUnitPriceAsc,
			PageQuery: botsdk.PageQuery{Limit: boardPageLimit},
		})
		if err != nil {
			return err
		}
		s.cacheSells(page.Items)
		return nil
	})
}

// cacheSells guarda las últimas ofertas sell vistas para la operación de
// aceptación (cota fija: la caché nunca crece con el tamaño del tablón).
func (s *session) cacheSells(pubs []botsdk.Publication) {
	for _, pub := range pubs {
		if pub.PublisherAccountID == s.accountID {
			continue
		}
		s.sellCache = append(s.sellCache, pub)
	}
	if len(s.sellCache) > maxCachedSells {
		s.sellCache = s.sellCache[len(s.sellCache)-maxCachedSells:]
	}
}

// cancellablePub devuelve el índice de la primera publicación propia cuyo
// cooldown ya venció (-1 si ninguna).
func (s *session) cancellablePub() int {
	now := time.Now()
	for i, pub := range s.livePubs {
		if !now.Before(pub.cooldownUntil) {
			return i
		}
	}
	return -1
}

// nodePair elige dos nodos DISTINTOS de la misma región (origen y destino de un
// plan de ruta ejecutable).
func (s *session) nodePair() (string, string, bool) {
	if len(s.nodes) < 2 {
		return "", "", false
	}
	origin := s.nodes[s.rng.IntN(len(s.nodes))]
	// Búsqueda acotada de un compañero de la misma región: barrido circular
	// desde una posición aleatoria (sin asignaciones ni bucles no acotados).
	start := s.rng.IntN(len(s.nodes))
	for i := range s.nodes {
		cand := s.nodes[(start+i)%len(s.nodes)]
		if cand.id != origin.id && cand.regionID == origin.regionID {
			return origin.id, cand.id, true
		}
	}
	return "", "", false
}

// randomProduct devuelve un producto cacheado ("" si el mundo no dio ninguno).
func (s *session) randomProduct() string {
	if len(s.products) == 0 {
		return ""
	}
	return s.products[s.rng.IntN(len(s.products))]
}

// randomRegion devuelve una región cacheada ("" si no hay ninguna).
func (s *session) randomRegion() string {
	if len(s.regions) == 0 {
		return ""
	}
	return s.regions[s.rng.IntN(len(s.regions))]
}

// randomNode devuelve un nodo con almacén cacheado (cero si no hay ninguno).
func (s *session) randomNode() nodeRef {
	if len(s.nodes) == 0 {
		return nodeRef{}
	}
	return s.nodes[s.rng.IntN(len(s.nodes))]
}

// pick elige una operación de una mezcla ponderada (fallback si la mezcla está
// vacía: un arquetipo sin mezcla declarada nunca deja al bot sin acción).
func pick(rng *rand.Rand, options []weighted, fallback Op) Op {
	total := 0
	for _, o := range options {
		total += o.weight
	}
	if total <= 0 {
		return fallback
	}
	n := rng.IntN(total)
	for _, o := range options {
		if n < o.weight {
			return o.op
		}
		n -= o.weight
	}
	return fallback
}

// pickString elige uno de dos valores con igual probabilidad.
func pickString(rng *rand.Rand, a, b string) string {
	if rng.IntN(2) == 0 {
		return a
	}
	return b
}

// pickKind elige un tipo de publicación para variar los filtros del tablón.
func pickKind(rng *rand.Rand) botsdk.PublicationKind {
	if rng.IntN(2) == 0 {
		return botsdk.PublicationSell
	}
	return botsdk.PublicationBuy
}

// jitter aplica jitter uniforme ±20% a una duración (los bots no deben latir
// todos a la vez: una carga sincronizada mide picos artificiales).
func jitter(rng *rand.Rand, d time.Duration) time.Duration {
	j := time.Duration(float64(d) * (0.8 + 0.4*rng.Float64()))
	if j <= 0 {
		return d
	}
	return j
}

// sleepCtx duerme d respetando la cancelación del contexto.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// describeSession formatea el bot para los logs de arranque.
func describeSession(bot StressBot) string {
	return fmt.Sprintf("%s (%s)", bot.Name, bot.Archetype)
}
