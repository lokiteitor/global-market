package bots

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/lokiteitor/global-market/backend/pkg/botsdk"
)

// CoalProducerConfig son los umbrales auditables del arquetipo coal_producer.
type CoalProducerConfig struct {
	ProducerConfig
	// BuyAcceptMinPriceBP es el precio mínimo (fracción del base_price en
	// basis points) al que el bot acepta solicitudes de compra de su
	// producto: 9000 ⇒ acepta si unit_price >= 90% del base_price.
	BuyAcceptMinPriceBP int64 `json:"buy_accept_min_price_bp"`
	// VehicleTypeCode es el tipo de camión que compra para despachar.
	VehicleTypeCode string `json:"vehicle_type_code"`
	// MaxVehicles acota la flota: sin camión propio se compra uno (entregado
	// en su nodo); con la flota completa y ningún camión disponible en el
	// nodo del cargamento, el bot espera.
	MaxVehicles int `json:"max_vehicles"`
}

// DefaultCoalProducerConfig son los umbrales por defecto del coal_producer.
func DefaultCoalProducerConfig() CoalProducerConfig {
	return CoalProducerConfig{
		ProducerConfig:      defaultProducerConfig("coal", "coal_mine", "mine_coal"),
		BuyAcceptMinPriceBP: 9_000,
		VehicleTypeCode:     "truck_small",
		MaxVehicles:         1,
	}
}

// CoalProducer es el arquetipo productor de carbón (ADR-024, Fase 0):
//
//  1. SETUP incremental: yacimiento de coal → concesión → coal_mine →
//     operational → receta mine_coal → cola (pendientes < 2 ⇒ encolar 3).
//  2. COMERCIO: mantiene UNA venta activa de coal (min(stock, 500) al
//     base_price, min_lot 50) y ATIENDE solicitudes de compra del tablón:
//     acepta (origen = su mina) si unit_price >= 90% del base_price y tiene
//     stock libre.
//  3. LOGÍSTICA: cuando una compra aceptada confirma contrato y aparece su
//     cargamento in_warehouse, asegura un camión (compra truck_small
//     entregado en su nodo si no tiene), computa el plan de ruta
//     origen→destino, crea la ruta y DESPACHA. Si el camión está ocupado (o
//     quedó en otro nodo), espera.
type CoalProducer struct {
	producerCore
	cfg CoalProducerConfig
}

// NewCoalProducer construye el arquetipo con sus umbrales.
func NewCoalProducer(cfg CoalProducerConfig, botName string, logger *slog.Logger, metrics *Metrics) *CoalProducer {
	return &CoalProducer{
		producerCore: producerCore{base: newBase(botName, "coal_producer", logger, metrics), cfg: cfg.ProducerConfig},
		cfg:          cfg,
	}
}

// Name implementa Behavior.
func (b *CoalProducer) Name() string { return "coal_producer" }

// ConfigJSON serializa los umbrales para auth.bot_profiles.behavior.
func (b *CoalProducer) ConfigJSON() ([]byte, error) { return json.Marshal(b.cfg) }

// Decide implementa Behavior: una pasada idempotente del ciclo completo.
func (b *CoalProducer) Decide(ctx context.Context, c *botsdk.Client, st *State) error {
	ready, err := b.ensureSetup(ctx, c, st)
	if err != nil || !ready {
		return err
	}
	if _, err := cashBalance(ctx, c, st); err != nil {
		return err
	}
	if err := b.maintainSell(ctx, c, st); err != nil {
		return err
	}
	if err := b.attendBuys(ctx, c, st); err != nil {
		return err
	}
	return b.dispatchShipments(ctx, c, st)
}

// attendBuys busca en el tablón solicitudes de compra de coal y acepta como
// máximo UNA por pasada (origen = su mina) si el precio alcanza el umbral y
// hay stock libre que la cubra.
func (b *CoalProducer) attendBuys(ctx context.Context, c *botsdk.Client, st *State) error {
	product := st.products[b.cfg.ProductCode]
	basePrice, err := product.BasePrice.Int64()
	if err != nil {
		return fmt.Errorf("bots: base_price inválido de coal: %w", err)
	}
	minPrice := applyBP(basePrice, b.cfg.BuyAcceptMinPriceBP)

	free, err := stockFreeAt(ctx, c, product.ID, st.mineID)
	if err != nil {
		return err
	}
	if free <= 0 {
		return nil
	}
	cash := st.LastCash

	for pub, err := range botsdk.All(ctx, func(ctx context.Context, cursor string) (botsdk.Page[botsdk.Publication], error) {
		return c.Board(ctx, botsdk.BoardQuery{
			Kind:      botsdk.PublicationBuy,
			ProductID: product.ID,
			Sort:      botsdk.SortUnitPriceDesc,
			PageQuery: botsdk.PageQuery{Cursor: cursor, Limit: 200},
		})
	}) {
		if err != nil {
			return fmt.Errorf("bots: consultando solicitudes de compra: %w", err)
		}
		if pub.PublisherAccountID == st.AccountID {
			continue
		}
		if _, already := st.pendingAcceptances[pub.ID]; already {
			continue
		}
		price, err := pub.UnitPrice.Int64()
		if err != nil {
			continue
		}
		if price < minPrice {
			// Orden unit_price_desc: lo que sigue paga aún menos.
			return nil
		}
		remaining, err := pub.QuantityRemaining.Int64()
		if err != nil || remaining <= 0 {
			continue
		}
		minLot, err := pub.MinLot.Int64()
		if err != nil {
			continue
		}
		qty := acceptQty(remaining, minLot, free)
		if qty <= 0 {
			continue
		}
		// Garantía del vendedor: 10% del valor en caja.
		if guarantee := qty * price / 10; cash < guarantee {
			b.decide("skip_buy", "insufficient_cash_for_guarantee",
				slog.String("publication_id", pub.ID),
				slog.Int64("guarantee", guarantee), slog.Int64("cash", cash))
			continue
		}
		qtyStr, err := botsdk.QtyFromInt64(qty)
		if err != nil {
			return err
		}
		acc, err := c.Accept(ctx, pub.ID, qtyStr, st.mineNodeID)
		if err != nil {
			if code, ok := blockedCode(err); ok {
				b.decide("blocked", code, slog.String("step", "accept_buy"),
					slog.String("publication_id", pub.ID))
				return nil
			}
			return fmt.Errorf("bots: aceptando la compra %s: %w", pub.ID, err)
		}
		st.pendingAcceptances[pub.ID] = acc.ID
		b.decide("accept_buy", "price_at_or_above_threshold",
			slog.String("publication_id", pub.ID),
			slog.String("acceptance_id", acc.ID),
			slog.Int64("quantity", qty),
			slog.Int64("unit_price", price),
			slog.Int64("min_price", minPrice))
		return nil // una aceptación por pasada
	}
	return nil
}

// dispatchShipments despacha los cargamentos in_warehouse de contratos
// propios: asegura camión, plan de ruta origen→destino, ruta y dispatch.
func (b *CoalProducer) dispatchShipments(ctx context.Context, c *botsdk.Client, st *State) error {
	shipments, err := botsdk.CollectAll(ctx, func(ctx context.Context, cursor string) (botsdk.Page[botsdk.Shipment], error) {
		return c.ListShipments(ctx, botsdk.ShipmentsQuery{
			Status:    botsdk.ShipmentInWarehouse,
			PageQuery: botsdk.PageQuery{Cursor: cursor, Limit: 200},
		})
	})
	if err != nil {
		return fmt.Errorf("bots: listando cargamentos: %w", err)
	}
	for _, sh := range shipments {
		if sh.ContractID == "" || sh.AtNodeID == "" {
			continue
		}
		contract, err := c.GetContract(ctx, sh.ContractID)
		if err != nil {
			return fmt.Errorf("bots: consultando el contrato %s del cargamento: %w", sh.ContractID, err)
		}
		if contract.Status != botsdk.ContractActive || contract.DestinationNodeID == sh.AtNodeID {
			continue
		}
		vehicleID, ok, err := b.ensureVehicleAt(ctx, c, st, sh.AtNodeID)
		if err != nil {
			return err
		}
		if !ok {
			continue // decisión de espera ya registrada
		}
		routeID, ok, err := b.ensureRoute(ctx, c, st, sh.AtNodeID, contract.DestinationNodeID)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if _, err := c.Dispatch(ctx, sh.ID, vehicleID, routeID); err != nil {
			if code, blocked := blockedCode(err); blocked {
				b.decide("blocked", code, slog.String("step", "dispatch"),
					slog.String("shipment_id", sh.ID))
				continue
			}
			return fmt.Errorf("bots: despachando el cargamento %s: %w", sh.ID, err)
		}
		b.decide("dispatch", "shipment_in_warehouse",
			slog.String("shipment_id", sh.ID),
			slog.String("contract_id", sh.ContractID),
			slog.String("vehicle_id", vehicleID),
			slog.String("route_id", routeID),
			slog.String("origin_node_id", sh.AtNodeID),
			slog.String("destination_node_id", contract.DestinationNodeID))
	}
	return nil
}

// ensureVehicleAt devuelve un camión idle situado en el nodo dado, comprando
// uno (entregado en ese nodo) si la flota aún no está completa. Si el camión
// existe pero está ocupado o en otro nodo, registra la espera.
func (b *CoalProducer) ensureVehicleAt(ctx context.Context, c *botsdk.Client, st *State, nodeID string) (string, bool, error) {
	vehicles, err := botsdk.CollectAll(ctx, func(ctx context.Context, cursor string) (botsdk.Page[botsdk.Vehicle], error) {
		return c.ListVehicles(ctx, botsdk.VehiclesQuery{PageQuery: botsdk.PageQuery{Cursor: cursor, Limit: 200}})
	})
	if err != nil {
		return "", false, fmt.Errorf("bots: listando la flota: %w", err)
	}
	for _, v := range vehicles {
		if v.Status == botsdk.VehicleIdle && v.Position.AtNodeID == nodeID {
			return v.ID, true, nil
		}
	}
	if len(vehicles) >= b.cfg.MaxVehicles {
		b.decide("wait", "vehicle_busy_or_elsewhere",
			slog.String("node_id", nodeID), slog.Int("fleet", len(vehicles)))
		return "", false, nil
	}
	vt, err := vehicleTypeByCode(ctx, c, st, b.cfg.VehicleTypeCode)
	if err != nil {
		return "", false, err
	}
	v, err := c.PurchaseVehicle(ctx, botsdk.VehiclePurchase{
		VehicleTypeID:  vt.ID,
		DeliveryNodeID: nodeID,
	})
	if err != nil {
		if code, ok := blockedCode(err); ok {
			b.decide("blocked", code, slog.String("step", "buy_vehicle"), slog.String("node_id", nodeID))
			return "", false, nil
		}
		if botsdk.IsCode(err, "VALIDATION_ERROR") {
			// Nodo sin enlace del modo del vehículo: sin red vial no hay
			// entrega posible; se espera a que exista infraestructura.
			b.decide("wait", "node_not_road_accessible", slog.String("node_id", nodeID))
			return "", false, nil
		}
		return "", false, fmt.Errorf("bots: comprando el camión: %w", err)
	}
	price, _ := vt.PurchasePrice.Int64()
	b.decide("buy_vehicle", "no_vehicle_owned",
		slog.String("vehicle_id", v.ID),
		slog.String("vehicle_type", b.cfg.VehicleTypeCode),
		slog.Int64("purchase_price", price),
		slog.String("delivery_node_id", nodeID))
	return v.ID, true, nil
}

// ensureRoute devuelve la ruta propia origen→destino, creándola desde un plan
// del Logistics Service si no existe (idempotente por nombre determinista).
func (b *CoalProducer) ensureRoute(ctx context.Context, c *botsdk.Client, st *State, originNodeID, destNodeID string) (string, bool, error) {
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
	plan, err := c.PlanRoute(ctx, botsdk.RoutePlanRequest{
		OriginNodeID:      originNodeID,
		DestinationNodeID: destNodeID,
		Modes:             []botsdk.LinkMode{botsdk.ModeRoad},
	})
	if err != nil {
		if botsdk.IsCode(err, "NO_ROUTE_FOUND") {
			b.decide("wait", "no_route",
				slog.String("origin_node_id", originNodeID),
				slog.String("destination_node_id", destNodeID))
			return "", false, nil
		}
		return "", false, fmt.Errorf("bots: calculando el plan de ruta: %w", err)
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
