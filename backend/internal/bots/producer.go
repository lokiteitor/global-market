package bots

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/lokiteitor/global-market/backend/pkg/botsdk"
)

// ProducerConfig son los umbrales auditables comunes de los productores
// primarios (coal_producer, iron_producer). Se persisten como behavior JSON
// en auth.bot_profiles al aprovisionar.
type ProducerConfig struct {
	// ProductCode es el producto extraído ("coal" / "iron_ore").
	ProductCode string `json:"product_code"`
	// MineTypeCode es el tipo de mina ("coal_mine" / "iron_mine").
	MineTypeCode string `json:"mine_type_code"`
	// RecipeCode es la receta extractiva ("mine_coal" / "mine_iron").
	RecipeCode string `json:"recipe_code"`
	// ParcelHalfM es el semilado del cuadrado de la concesión centrada en el
	// yacimiento, en metros.
	ParcelHalfM float64 `json:"parcel_half_m"`
	// FootprintHalfM es el semilado del footprint de la mina, en metros.
	FootprintHalfM float64 `json:"footprint_half_m"`
	// MinPendingBatches y BatchesPerQueue mantienen la cola: si los lotes
	// pendientes son < MinPendingBatches se encolan BatchesPerQueue más.
	MinPendingBatches int `json:"min_pending_batches"`
	BatchesPerQueue   int `json:"batches_per_queue"`
	// SellLotMax acota la cantidad de la publicación de venta:
	// min(stock libre, SellLotMax).
	SellLotMax int64 `json:"sell_lot_max"`
	// SellMinLot es el min_lot de la publicación de venta y el stock mínimo
	// para publicarla.
	SellMinLot int64 `json:"sell_min_lot"`
	// SellPriceBP fija el precio de venta como fracción del base_price en
	// basis points (10000 = base_price ±0).
	SellPriceBP int64 `json:"sell_price_bp"`
	// SellDeliverySimSeconds es el plazo declarado de la venta (la entrega de
	// una sell es in situ; el plazo solo acota el contrato).
	SellDeliverySimSeconds int64 `json:"sell_delivery_sim_seconds"`
	// BuyAcceptMinPriceBP es el precio mínimo (fracción del base_price en
	// basis points) al que el productor acepta solicitudes de compra de su
	// producto: 9000 ⇒ acepta si unit_price >= 90% del base_price.
	BuyAcceptMinPriceBP int64 `json:"buy_accept_min_price_bp"`
	// VehicleTypeCode es el tipo de camión que compra para despachar.
	VehicleTypeCode string `json:"vehicle_type_code"`
	// MaxVehicles acota la flota: sin camión propio se compra uno (entregado
	// en su nodo); con la flota completa y el camión libre pero en OTRO nodo
	// (donde lo dejó su última entrega), se REPOSICIONA en vacío hasta el
	// cargamento; solo se espera si está ocupado.
	MaxVehicles int `json:"max_vehicles"`
}

// defaultProducerConfig son los umbrales comunes por defecto.
func defaultProducerConfig(product, mineType, recipe string) ProducerConfig {
	return ProducerConfig{
		ProductCode:            product,
		MineTypeCode:           mineType,
		RecipeCode:             recipe,
		ParcelHalfM:            1_500,
		FootprintHalfM:         200,
		MinPendingBatches:      2,
		BatchesPerQueue:        3,
		SellLotMax:             500,
		SellMinLot:             50,
		SellPriceBP:            10_000,
		SellDeliverySimSeconds: simDaySeconds,
		BuyAcceptMinPriceBP:    9_000,
		VehicleTypeCode:        "truck_small",
		MaxVehicles:            1,
	}
}

// producerCore implementa el ciclo común de un productor primario: SETUP
// incremental vía API (yacimiento → concesión → mina → operational → receta →
// cola) y el COMERCIO de su producto (trade): UNA venta activa, atención de las
// solicitudes de compra del tablón y despacho de lo aceptado. Cada paso solo
// actúa si el estado observable de la API lo pide (idempotencia).
type producerCore struct {
	base
	cfg ProducerConfig
}

// ensureSetup avanza lo que falte del setup. Devuelve ready=true cuando la
// mina está operational con la receta activa fijada (la cola se mantiene en
// la misma pasada).
func (p *producerCore) ensureSetup(ctx context.Context, c *botsdk.Client, st *State) (bool, error) {
	if err := ensureIdentity(ctx, c, st); err != nil {
		return false, err
	}
	product, err := productByCode(ctx, c, st, p.cfg.ProductCode)
	if err != nil {
		return false, err
	}

	// (1) Yacimiento del producto: ancla geográfica de toda la implantación.
	if st.depositID == "" {
		deposits, err := c.ResourceDeposits(ctx, botsdk.ResourceDepositsQuery{
			ProductID: product.ID,
			PageQuery: botsdk.PageQuery{Limit: 1},
		})
		if err != nil {
			return false, fmt.Errorf("bots: buscando el yacimiento de %s: %w", p.cfg.ProductCode, err)
		}
		if len(deposits.Items) == 0 {
			p.decide("wait", "no_deposit", slog.String("product", p.cfg.ProductCode))
			return false, nil
		}
		d := deposits.Items[0]
		st.depositID = d.ID
		st.depositX = d.Location.Coordinates[0]
		st.depositY = d.Location.Coordinates[1]
		st.regionID = d.RegionID
	}

	// (2) Concesión de suelo alrededor del yacimiento.
	if st.concessionID == "" {
		page, err := c.ListConcessions(ctx, botsdk.ConcessionsQuery{
			Status:    botsdk.ConcessionActive,
			PageQuery: botsdk.PageQuery{Limit: 1},
		})
		if err != nil {
			return false, fmt.Errorf("bots: listando concesiones: %w", err)
		}
		if len(page.Items) > 0 {
			st.concessionID = page.Items[0].ID
		} else {
			parcel := squareAround(st.depositX, st.depositY, p.cfg.ParcelHalfM)
			conc, err := c.CreateConcession(ctx, botsdk.ConcessionCreate{RegionID: st.regionID, Parcel: parcel})
			if err != nil {
				if code, ok := blockedCode(err); ok {
					p.decide("blocked", code, slog.String("step", "create_concession"))
					return false, nil
				}
				return false, fmt.Errorf("bots: creando la concesión: %w", err)
			}
			st.concessionID = conc.ID
			p.decide("create_concession", "setup",
				slog.String("concession_id", conc.ID), slog.String("region_id", st.regionID))
		}
	}

	// (3) Mina sobre el yacimiento (placement near_resource).
	if st.mineID == "" {
		mineType, err := buildingTypeByCode(ctx, c, st, p.cfg.MineTypeCode)
		if err != nil {
			return false, err
		}
		page, err := c.ListBuildings(ctx, botsdk.BuildingsQuery{
			BuildingTypeID: mineType.ID,
			PageQuery:      botsdk.PageQuery{Limit: 1},
		})
		if err != nil {
			return false, fmt.Errorf("bots: listando edificios: %w", err)
		}
		if len(page.Items) > 0 {
			st.mineID = page.Items[0].ID
		} else {
			footprint := squareAround(st.depositX, st.depositY, p.cfg.FootprintHalfM)
			b, err := c.CreateBuilding(ctx, botsdk.BuildingCreate{
				BuildingTypeID: mineType.ID,
				ConcessionID:   st.concessionID,
				Footprint:      footprint,
			})
			if err != nil {
				if code, ok := blockedCode(err); ok {
					p.decide("blocked", code, slog.String("step", "build_mine"))
					return false, nil
				}
				return false, fmt.Errorf("bots: construyendo la mina: %w", err)
			}
			st.mineID = b.ID
			p.decide("build_mine", "setup",
				slog.String("building_id", b.ID), slog.String("type", p.cfg.MineTypeCode))
			return false, nil // en construcción: nada más que hacer esta pasada
		}
	}

	// (4) Esperar operational.
	building, err := c.GetBuilding(ctx, st.mineID)
	if err != nil {
		return false, fmt.Errorf("bots: consultando la mina %s: %w", st.mineID, err)
	}
	if building.Status != botsdk.BuildingOperational {
		p.decide("wait", "mine_"+string(building.Status), slog.String("building_id", st.mineID))
		return false, nil
	}

	// (5) Fijar la receta extractiva.
	recipe, err := recipeByCode(ctx, c, st, p.cfg.RecipeCode)
	if err != nil {
		return false, err
	}
	if building.ActiveRecipeID != recipe.ID {
		recipeID := recipe.ID
		if _, err := c.UpdateBuilding(ctx, st.mineID, botsdk.BuildingUpdate{ActiveRecipeID: &recipeID}); err != nil {
			return false, fmt.Errorf("bots: fijando la receta %s: %w", p.cfg.RecipeCode, err)
		}
		p.decide("set_recipe", "setup",
			slog.String("building_id", st.mineID), slog.String("recipe", p.cfg.RecipeCode))
	}

	// (6) Nodo del grafo de la mina (para publicar/aceptar/despachar).
	if st.mineNodeID == "" {
		nodeID, err := nodeOfBuilding(ctx, c, st, st.regionID, st.mineID)
		if err != nil {
			return false, err
		}
		st.mineNodeID = nodeID
	}

	// (7) Mantener la cola de producción: pendientes < MinPendingBatches ⇒
	// encolar BatchesPerQueue.
	pending, err := pendingBatches(ctx, c, st.mineID)
	if err != nil {
		return false, err
	}
	if pending < p.cfg.MinPendingBatches {
		if _, err := c.QueueProduction(ctx, st.mineID, botsdk.ProductionBatchCreate{
			RecipeID:      recipe.ID,
			BatchesQueued: p.cfg.BatchesPerQueue,
		}); err != nil {
			if code, ok := blockedCode(err); ok {
				p.decide("blocked", code, slog.String("step", "queue_batches"))
				return true, nil
			}
			return false, fmt.Errorf("bots: encolando lotes: %w", err)
		}
		p.decide("queue_batches", "pending_below_min",
			slog.String("building_id", st.mineID),
			slog.Int("pending", pending), slog.Int("queued", p.cfg.BatchesPerQueue))
	}
	return true, nil
}

// maintainSell mantiene UNA publicación de venta activa del producto: si no
// hay ninguna propia visible y el stock libre en la mina alcanza SellMinLot,
// publica min(stock, SellLotMax) al precio base_price×SellPriceBP.
func (p *producerCore) maintainSell(ctx context.Context, c *botsdk.Client, st *State) error {
	product := st.products[p.cfg.ProductCode]
	mine, err := myOpenPublication(ctx, c, st, botsdk.PublicationSell, product.ID)
	if err != nil {
		return err
	}
	if mine != nil {
		// Ya hay una venta activa: regla de UNA publicación.
		p.idle("sell_already_active",
			slog.String("product", p.cfg.ProductCode),
			slog.String("publication_id", mine.ID))
		return nil
	}
	free, err := stockFreeAt(ctx, c, product.ID, st.mineID)
	if err != nil {
		return err
	}
	if free < p.cfg.SellMinLot {
		p.idle("stock_below_min_lot",
			slog.String("product", p.cfg.ProductCode),
			slog.Int64("stock", free), slog.Int64("min_lot", p.cfg.SellMinLot))
		return nil
	}
	basePrice, err := product.BasePrice.Int64()
	if err != nil {
		return fmt.Errorf("bots: base_price inválido de %s: %w", p.cfg.ProductCode, err)
	}
	price := applyBP(basePrice, p.cfg.SellPriceBP)
	qty := min(free, p.cfg.SellLotMax)
	qtyStr, err := botsdk.QtyFromInt64(qty)
	if err != nil {
		return err
	}
	minLot, err := botsdk.QtyFromInt64(p.cfg.SellMinLot)
	if err != nil {
		return err
	}
	pub, err := c.CreatePublication(ctx, botsdk.PublicationCreate{
		Kind:               botsdk.PublicationSell,
		ProductID:          product.ID,
		QuantityTotal:      qtyStr,
		UnitPrice:          botsdk.MoneyFromInt64(price),
		MinLot:             minLot,
		OriginNodeID:       st.mineNodeID,
		DeliverySimSeconds: p.cfg.SellDeliverySimSeconds,
	})
	if err != nil {
		if code, ok := blockedCode(err); ok {
			p.decide("blocked", code, slog.String("step", "publish_sell"))
			return nil
		}
		return fmt.Errorf("bots: publicando la venta de %s: %w", p.cfg.ProductCode, err)
	}
	p.decide("publish_sell", "no_active_sell",
		slog.String("publication_id", pub.ID),
		slog.String("product", p.cfg.ProductCode),
		slog.Int64("quantity", qty), slog.Int64("unit_price", price))
	return nil
}

// trade es la parte COMERCIAL común a TODO productor primario: mantiene su
// venta activa, ATIENDE las solicitudes de compra de su producto que pagan por
// encima de su umbral y despacha lo aceptado.
//
// Que atender compras sea del NÚCLEO y no de un arquetipo concreto es lo que
// cierra la cadena industrial entre bots (GDD §13.4, "mundo vivo"): el
// transformador solo se abastece publicando solicitudes de compra con destino
// su planta, así que un productor que solo publica ventas y nunca cruza una
// buy deja al horno sin insumo indefinidamente aunque tenga stock y aunque el
// comprador pague MÁS que su propio precio de venta. La población es
// backstop de liquidez de los dos lados del tablón o no lo es de ninguno.
func (p *producerCore) trade(ctx context.Context, c *botsdk.Client, st *State) error {
	if err := p.maintainSell(ctx, c, st); err != nil {
		return err
	}
	if err := p.attendBuys(ctx, c, st); err != nil {
		return err
	}
	return p.dispatchShipments(ctx, c, st)
}

// attendBuys busca en el tablón solicitudes de compra del producto propio y
// acepta como máximo UNA por pasada (origen = su mina) si el precio alcanza el
// umbral y hay stock libre que la cubra.
func (p *producerCore) attendBuys(ctx context.Context, c *botsdk.Client, st *State) error {
	product := st.products[p.cfg.ProductCode]
	basePrice, err := product.BasePrice.Int64()
	if err != nil {
		return fmt.Errorf("bots: base_price inválido de %s: %w", p.cfg.ProductCode, err)
	}
	minPrice := applyBP(basePrice, p.cfg.BuyAcceptMinPriceBP)

	free, err := stockFreeAt(ctx, c, product.ID, st.mineID)
	if err != nil {
		return err
	}
	if free <= 0 {
		p.idle("no_stock_to_serve_buys", slog.String("product", p.cfg.ProductCode))
		return nil
	}
	cash := st.LastCash

	scanned := 0
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
		scanned++
		if pub.PublisherAccountID == st.AccountID {
			continue
		}
		again, err := acceptableAgain(ctx, c, st, pub.ID)
		if err != nil {
			return err
		}
		if !again {
			continue // aceptación propia aún en sorteo sobre esta solicitud
		}
		price, err := pub.UnitPrice.Int64()
		if err != nil {
			continue
		}
		if price < minPrice {
			// Orden unit_price_desc: lo que sigue paga aún menos.
			break
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
			p.decide("skip_buy", "insufficient_cash_for_guarantee",
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
				p.decide("blocked", code, slog.String("step", "accept_buy"),
					slog.String("publication_id", pub.ID))
				return nil
			}
			return fmt.Errorf("bots: aceptando la compra %s: %w", pub.ID, err)
		}
		st.pendingAcceptances[pub.ID] = acc.ID
		p.decide("accept_buy", "price_at_or_above_threshold",
			slog.String("publication_id", pub.ID),
			slog.String("acceptance_id", acc.ID),
			slog.String("product", p.cfg.ProductCode),
			slog.Int64("quantity", qty),
			slog.Int64("unit_price", price),
			slog.Int64("min_price", minPrice))
		return nil // una aceptación por pasada
	}
	// Barrido completo sin aceptar nada: se anota el motivo para el latido de
	// la pasada (§ base.pass).
	p.idle("no_buy_on_board",
		slog.String("product", p.cfg.ProductCode),
		slog.Int("scanned", scanned),
		slog.Int64("min_price", minPrice),
		slog.Int64("stock_free", free))
	return nil
}

// dispatchShipments despacha los cargamentos in_warehouse de contratos
// propios: asegura camión, plan de ruta origen→destino, ruta y dispatch.
func (p *producerCore) dispatchShipments(ctx context.Context, c *botsdk.Client, st *State) error {
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
		slot, err := ensureVehicleAt(ctx, c, st, &p.base, p.vehiclePolicy(), sh.AtNodeID)
		if err != nil {
			return err
		}
		if !slot.ready {
			// Espera (o viaje en vacío en curso) ya registrada: el camión llegará
			// al nodo del cargamento y una pasada posterior lo despachará.
			continue
		}
		vehicleID := slot.id
		routeID, ok, err := ensureRoute(ctx, c, st, &p.base, botsdk.ModeRoad, sh.AtNodeID, contract.DestinationNodeID)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if _, err := c.Dispatch(ctx, sh.ID, vehicleID, routeID); err != nil {
			if code, blocked := blockedCode(err); blocked {
				p.decide("blocked", code, slog.String("step", "dispatch"),
					slog.String("shipment_id", sh.ID))
				continue
			}
			return fmt.Errorf("bots: despachando el cargamento %s: %w", sh.ID, err)
		}
		p.decide("dispatch", "shipment_in_warehouse",
			slog.String("shipment_id", sh.ID),
			slog.String("contract_id", sh.ContractID),
			slog.String("vehicle_id", vehicleID),
			slog.String("route_id", routeID),
			slog.String("origin_node_id", sh.AtNodeID),
			slog.String("destination_node_id", contract.DestinationNodeID))
	}
	p.idle("no_shipment_to_dispatch",
		slog.Int("shipments_in_warehouse", len(shipments)))
	return nil
}

// vehiclePolicy es la política de flota del productor primario: un camión
// ligero para despachar sus propias ventas.
func (p *producerCore) vehiclePolicy() vehiclePolicy {
	return vehiclePolicy{typeCode: p.cfg.VehicleTypeCode, max: p.cfg.MaxVehicles}
}

// squareAround construye un polígono cuadrado cerrado (CCW) centrado en
// (x, y) con semilado half.
func squareAround(x, y, half float64) botsdk.GeoPolygon {
	return botsdk.NewGeoPolygon([][2]float64{
		{x - half, y - half},
		{x + half, y - half},
		{x + half, y + half},
		{x - half, y + half},
		{x - half, y - half},
	})
}

// blockedCode clasifica los rechazos de dominio ESPERABLES (falta de fondos o
// colateral, solape, emplazamiento inválido, sin ruta, cargamento/vehículo no
// disponibles, publicación agotada por carrera) que un bot trata como
// decisión de espera auditada en lugar de error. Devuelve el código y si
// aplica.
func blockedCode(err error) (string, bool) {
	for _, code := range []string{
		"INSUFFICIENT_FUNDS",
		"INSUFFICIENT_COLLATERAL",
		"CONCESSION_OVERLAP",
		"PLACEMENT_INVALID",
		"NO_ROUTE_FOUND",
		"PUBLICATION_EXHAUSTED",
		"BELOW_MIN_LOT",
		"VEHICLE_NOT_IDLE",
		"SHIPMENT_NOT_DISPATCHABLE",
		"CANCEL_COOLDOWN_ACTIVE",
	} {
		if botsdk.IsCode(err, code) {
			return code, true
		}
	}
	return "", false
}
