package bots

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/lokiteitor/global-market/backend/pkg/botsdk"
)

// IndustrialTransformerConfig son los umbrales auditables del arquetipo
// industrial_transformer. Se persisten como behavior JSON en auth.bot_profiles
// al aprovisionar.
type IndustrialTransformerConfig struct {
	// OutputProductCode es el bien manufacturado que vende ("steel_ingot").
	OutputProductCode string `json:"output_product_code"`
	// PlantTypeCode es el tipo de instalación de manufactura ("blast_furnace").
	PlantTypeCode string `json:"plant_type_code"`
	// RecipeCode es la receta de manufactura ("smelt_steel").
	RecipeCode string `json:"recipe_code"`
	// AnchorInputCode es el insumo cuyo yacimiento ancla la implantación: el
	// transformador se instala en la REGIÓN de sus proveedores naturales.
	AnchorInputCode string `json:"anchor_input_code"`
	// ParcelHalfM es el semilado del cuadrado de la concesión, en metros.
	ParcelHalfM float64 `json:"parcel_half_m"`
	// FootprintHalfM es el semilado del footprint de la planta, en metros.
	FootprintHalfM float64 `json:"footprint_half_m"`
	// SiteBaseRadiusM y SiteRingStepM definen los anillos de la búsqueda de
	// suelo libre alrededor del yacimiento ancla; MaxSites acota los intentos.
	SiteBaseRadiusM float64 `json:"site_base_radius_m"`
	SiteRingStepM   float64 `json:"site_ring_step_m"`
	MaxSites        int     `json:"max_sites"`
	// MinPendingBatches y BatchesPerQueue mantienen la cola: si los lotes
	// pendientes son < MinPendingBatches se encolan BatchesPerQueue más.
	MinPendingBatches int `json:"min_pending_batches"`
	BatchesPerQueue   int `json:"batches_per_queue"`
	// InputBufferBatches fija el umbral de reposición de cada insumo en LOTES
	// (umbral = consumo por lote × InputBufferBatches): el bot no cablea
	// cantidades, las deriva de la receta.
	InputBufferBatches int64 `json:"input_buffer_batches"`
	// InputBuyBatches fija el tamaño de cada solicitud de compra en LOTES
	// (cantidad = consumo por lote × InputBuyBatches).
	InputBuyBatches int64 `json:"input_buy_batches"`
	// InputBuyPriceBP es el precio ofrecido por cada insumo como fracción del
	// precio de referencia en basis points (>= 10000: paga por encima del
	// mercado para atraer vendedores).
	InputBuyPriceBP int64 `json:"input_buy_price_bp"`
	// InputBuyDeliverySimSeconds es el plazo de entrega de las solicitudes de
	// compra (generoso: el vendedor debe mover camiones reales).
	InputBuyDeliverySimSeconds int64 `json:"input_buy_delivery_sim_seconds"`
	// MarginBP es el margen de venta sobre el COSTE UNITARIO estimado de los
	// insumos, en basis points (2500 ⇒ precio = coste × 1,25).
	MarginBP int64 `json:"margin_bp"`
	// PriceWindowSimSeconds es la ventana de sim-time de la que se toma el
	// precio de referencia OHLC (cierre de la vela más reciente de la ventana).
	PriceWindowSimSeconds int64 `json:"price_window_sim_seconds"`
	// SellLotMax acota la cantidad de la publicación de venta y SellMinLot es
	// su min_lot (y el stock mínimo para publicarla).
	SellLotMax int64 `json:"sell_lot_max"`
	SellMinLot int64 `json:"sell_min_lot"`
	// SellDeliverySimSeconds es el plazo declarado de la venta.
	SellDeliverySimSeconds int64 `json:"sell_delivery_sim_seconds"`
}

// DefaultIndustrialTransformerConfig son los umbrales por defecto del
// transformador. marginBP es el margen de venta (II_BOTS_TRANSFORMER_MARGIN_BP).
func DefaultIndustrialTransformerConfig(marginBP int64) IndustrialTransformerConfig {
	return IndustrialTransformerConfig{
		OutputProductCode:          "steel_ingot",
		PlantTypeCode:              "blast_furnace",
		RecipeCode:                 "smelt_steel",
		AnchorInputCode:            "iron_ore",
		ParcelHalfM:                1_500,
		FootprintHalfM:             200,
		SiteBaseRadiusM:            6_000,
		SiteRingStepM:              4_000,
		MaxSites:                   24,
		MinPendingBatches:          2,
		BatchesPerQueue:            3,
		InputBufferBatches:         5,
		InputBuyBatches:            10,
		InputBuyPriceBP:            11_000,
		InputBuyDeliverySimSeconds: 2 * simDaySeconds,
		MarginBP:                   DefaultTransformerMarginBP,
		PriceWindowSimSeconds:      7 * simDaySeconds,
		SellLotMax:                 200,
		SellMinLot:                 8,
		SellDeliverySimSeconds:     simDaySeconds,
	}
}

// IndustrialTransformer es el arquetipo transformador industrial (GDD 13.2:
// "compran insumos, producen bienes intermedios/finales"; bot INTERMEDIO del
// GDD 13.3: reglas + optimización simple). Una pasada de Decide:
//
//  1. SETUP incremental: yacimiento del insumo ancla (región de sus
//     proveedores) → concesión sobre el primer emplazamiento libre de la
//     búsqueda por anillos → blast_furnace → operational → receta smelt_steel.
//  2. ABASTECIMIENTO: la receta consume iron_ore (input) y coal (combustible).
//     Si el stock libre de un insumo en el horno cae por debajo de su umbral
//     (consumo por lote × InputBufferBatches), publica UNA solicitud de compra
//     (buy) con DESTINO su horno, por consumo por lote × InputBuyBatches, a
//     base_price × InputBuyPriceBP (prima sobre el catálogo, no sobre el último
//     precio impreso: no persigue sus propias operaciones). Una activa por
//     insumo — nunca dos solicitudes abiertas del mismo material. La
//     ENTREGA no exige nada del comprador: el vendedor que acepta despacha el
//     cargamento y su llegada física al nodo del horno confirma el contrato y
//     deja el stock en el almacén de la planta.
//  3. OPTIMIZACIÓN SIMPLE (margen): estima el coste unitario del bien
//     manufacturado desde la RECETA y el precio de MERCADO de los insumos
//     —coste = (Σ consumo_por_lote × precio_de_referencia) / salida_por_lote,
//     con precio de referencia = cierre OHLC reciente o base_price— y lo
//     compara con el precio de referencia del propio bien. Si el margen
//     esperado NO es positivo (insumos caros), PARA LA COLA (no encola lotes) y
//     no publica venta: decisión auditable skip_production/negative_margin.
//  4. VENTA: con margen positivo mantiene UNA venta activa del bien a
//     coste × (1 + MarginBP), acotada al techo de precio del catálogo.
type IndustrialTransformer struct {
	base
	cfg IndustrialTransformerConfig
}

// NewIndustrialTransformer construye el arquetipo con sus umbrales.
func NewIndustrialTransformer(cfg IndustrialTransformerConfig, botName string, logger *slog.Logger, metrics *Metrics) *IndustrialTransformer {
	return &IndustrialTransformer{base: newBase(botName, "industrial_transformer", logger, metrics), cfg: cfg}
}

// Name implementa Behavior.
func (b *IndustrialTransformer) Name() string { return "industrial_transformer" }

// ConfigJSON serializa los umbrales para auth.bot_profiles.behavior.
func (b *IndustrialTransformer) ConfigJSON() ([]byte, error) { return json.Marshal(b.cfg) }

// Decide implementa Behavior: una pasada idempotente del ciclo completo. La
// envuelve base.pass: una pasada sin acción (insumos en camino, cola llena,
// venta ya activa) emite igualmente su decisión `wait` con motivo — un horno
// esperando mineral no puede ser indistinguible de un bot colgado.
func (b *IndustrialTransformer) Decide(ctx context.Context, c *botsdk.Client, st *State) error {
	return b.pass(func() error {
		recipe, ready, err := b.ensureSetup(ctx, c, st)
		if err != nil || !ready {
			return err
		}
		if _, err := cashBalance(ctx, c, st); err != nil {
			return err
		}
		needs, outputPerBatch, err := b.recipeNeeds(ctx, c, st, recipe)
		if err != nil {
			return err
		}
		if err := b.maintainInputBuys(ctx, c, st, needs); err != nil {
			return err
		}

		// Optimización simple: sin margen esperado no se funde ni se publica.
		unitCost, err := b.estimatedUnitCost(ctx, c, needs, outputPerBatch)
		if err != nil {
			return err
		}
		output := st.products[b.cfg.OutputProductCode]
		reference, source, err := referencePrice(ctx, c, output, b.cfg.PriceWindowSimSeconds)
		if err != nil {
			return err
		}
		if reference <= unitCost {
			b.decide("skip_production", "negative_margin",
				slog.String("product", b.cfg.OutputProductCode),
				slog.Int64("unit_cost", unitCost),
				slog.Int64("reference_price", reference),
				slog.String("price_source", source))
			return nil
		}
		if err := b.maintainQueue(ctx, c, st, recipe, unitCost, reference); err != nil {
			return err
		}
		return b.maintainSell(ctx, c, st, output, unitCost)
	})
}

// ─── Setup incremental ───────────────────────────────────────────────────────

// ensureSetup avanza lo que falte del setup y devuelve la receta activa con
// ready=true cuando el horno está operational con ella fijada.
func (b *IndustrialTransformer) ensureSetup(ctx context.Context, c *botsdk.Client, st *State) (botsdk.Recipe, bool, error) {
	var recipe botsdk.Recipe
	if err := ensureIdentity(ctx, c, st); err != nil {
		return recipe, false, err
	}
	if _, err := productByCode(ctx, c, st, b.cfg.OutputProductCode); err != nil {
		return recipe, false, err
	}
	recipe, err := recipeByCode(ctx, c, st, b.cfg.RecipeCode)
	if err != nil {
		return recipe, false, err
	}

	// (1) Yacimiento del insumo ancla: fija la región de implantación y el
	// centro de la búsqueda de suelo (cerca de los proveedores del insumo).
	if st.depositID == "" {
		anchor, err := productByCode(ctx, c, st, b.cfg.AnchorInputCode)
		if err != nil {
			return recipe, false, err
		}
		deposits, err := c.ResourceDeposits(ctx, botsdk.ResourceDepositsQuery{
			ProductID: anchor.ID,
			PageQuery: botsdk.PageQuery{Limit: 1},
		})
		if err != nil {
			return recipe, false, fmt.Errorf("bots: buscando el yacimiento de %s: %w", b.cfg.AnchorInputCode, err)
		}
		if len(deposits.Items) == 0 {
			b.decide("wait", "no_deposit", slog.String("product", b.cfg.AnchorInputCode))
			return recipe, false, nil
		}
		d := deposits.Items[0]
		st.depositID = d.ID
		st.depositX = d.Location.Coordinates[0]
		st.depositY = d.Location.Coordinates[1]
		st.regionID = d.RegionID
	}

	// (2) Concesión de suelo: se reutiliza la propia si ya existe; si no, se
	// prueban emplazamientos libres por anillos alrededor del yacimiento.
	if st.concessionID == "" {
		page, err := c.ListConcessions(ctx, botsdk.ConcessionsQuery{
			Status:    botsdk.ConcessionActive,
			PageQuery: botsdk.PageQuery{Limit: 1},
		})
		if err != nil {
			return recipe, false, fmt.Errorf("bots: listando concesiones: %w", err)
		}
		if len(page.Items) > 0 {
			st.concessionID = page.Items[0].ID
		} else {
			ok, err := b.claimSite(ctx, c, st)
			if err != nil || !ok {
				return recipe, false, err
			}
		}
	}

	// (3) Planta de manufactura dentro de la parcela (placement libre: el alto
	// horno no exige yacimiento).
	if st.plantID == "" {
		plantType, err := buildingTypeByCode(ctx, c, st, b.cfg.PlantTypeCode)
		if err != nil {
			return recipe, false, err
		}
		page, err := c.ListBuildings(ctx, botsdk.BuildingsQuery{
			BuildingTypeID: plantType.ID,
			PageQuery:      botsdk.PageQuery{Limit: 1},
		})
		if err != nil {
			return recipe, false, fmt.Errorf("bots: listando edificios: %w", err)
		}
		if len(page.Items) > 0 {
			st.plantID = page.Items[0].ID
		} else {
			conc, err := c.GetConcession(ctx, st.concessionID)
			if err != nil {
				return recipe, false, fmt.Errorf("bots: consultando la concesión %s: %w", st.concessionID, err)
			}
			cx, cy, ok := polygonCenter(conc.Parcel)
			if !ok {
				return recipe, false, fmt.Errorf("bots: la parcela de la concesión %s no tiene anillo exterior", conc.ID)
			}
			plant, err := c.CreateBuilding(ctx, botsdk.BuildingCreate{
				BuildingTypeID: plantType.ID,
				ConcessionID:   st.concessionID,
				Footprint:      squareAround(cx, cy, b.cfg.FootprintHalfM),
			})
			if err != nil {
				if code, blocked := blockedCode(err); blocked {
					b.decide("blocked", code, slog.String("step", "build_plant"))
					return recipe, false, nil
				}
				return recipe, false, fmt.Errorf("bots: construyendo la planta: %w", err)
			}
			st.plantID = plant.ID
			b.decide("build_plant", "setup",
				slog.String("building_id", plant.ID), slog.String("type", b.cfg.PlantTypeCode))
			return recipe, false, nil // en construcción: nada más que hacer esta pasada
		}
	}

	// (4) Esperar operational.
	plant, err := c.GetBuilding(ctx, st.plantID)
	if err != nil {
		return recipe, false, fmt.Errorf("bots: consultando la planta %s: %w", st.plantID, err)
	}
	if plant.Status != botsdk.BuildingOperational {
		b.decide("wait", "plant_"+string(plant.Status), slog.String("building_id", st.plantID))
		return recipe, false, nil
	}

	// (5) Fijar la receta de manufactura.
	if plant.ActiveRecipeID != recipe.ID {
		recipeID := recipe.ID
		if _, err := c.UpdateBuilding(ctx, st.plantID, botsdk.BuildingUpdate{ActiveRecipeID: &recipeID}); err != nil {
			return recipe, false, fmt.Errorf("bots: fijando la receta %s: %w", b.cfg.RecipeCode, err)
		}
		b.decide("set_recipe", "setup",
			slog.String("building_id", st.plantID), slog.String("recipe", b.cfg.RecipeCode))
	}

	// (6) Nodo del grafo de la planta (destino de sus compras, origen de sus
	// ventas).
	if st.plantNodeID == "" {
		nodeID, err := nodeOfBuilding(ctx, c, st, st.regionID, st.plantID)
		if err != nil {
			return recipe, false, err
		}
		st.plantNodeID = nodeID
	}
	return recipe, true, nil
}

// claimSite intenta tomar la concesión del emplazamiento candidato en curso.
// Un rechazo esperable del sistema (parcela solapada o fuera de la región)
// AVANZA al siguiente candidato: la búsqueda de suelo libre es la regla, no un
// error. ok=true solo si la concesión quedó otorgada.
func (b *IndustrialTransformer) claimSite(ctx context.Context, c *botsdk.Client, st *State) (bool, error) {
	if st.siteIndex >= b.cfg.MaxSites {
		b.decide("wait", "no_free_site",
			slog.Int("sites_tried", st.siteIndex), slog.String("region_id", st.regionID))
		return false, nil
	}
	x, y := siteCandidate(st.depositX, st.depositY, st.siteIndex, b.cfg.SiteBaseRadiusM, b.cfg.SiteRingStepM)
	conc, err := c.CreateConcession(ctx, botsdk.ConcessionCreate{
		RegionID: st.regionID,
		Parcel:   squareAround(x, y, b.cfg.ParcelHalfM),
	})
	if err != nil {
		// Suelo no disponible (ocupado o fuera de la región): avanza la
		// búsqueda. Cualquier otro rechazo esperable (p. ej. sin fondos para el
		// canon) NO gasta candidato: se reintenta el mismo.
		switch {
		case botsdk.IsCode(err, "CONCESSION_OVERLAP"):
			st.siteIndex++
			b.decide("next_site", "concession_overlap",
				slog.Int("site_index", st.siteIndex), slog.Float64("x", x), slog.Float64("y", y))
			return false, nil
		case botsdk.IsCode(err, "VALIDATION_ERROR"):
			st.siteIndex++
			b.decide("next_site", "parcel_outside_region",
				slog.Int("site_index", st.siteIndex), slog.Float64("x", x), slog.Float64("y", y))
			return false, nil
		}
		if code, blocked := blockedCode(err); blocked {
			b.decide("blocked", code, slog.String("step", "create_concession"))
			return false, nil
		}
		return false, fmt.Errorf("bots: creando la concesión: %w", err)
	}
	st.concessionID = conc.ID
	b.decide("create_concession", "setup",
		slog.String("concession_id", conc.ID), slog.String("region_id", st.regionID),
		slog.Int("site_index", st.siteIndex), slog.Float64("x", x), slog.Float64("y", y))
	return true, nil
}

// ─── Receta, abastecimiento y margen ─────────────────────────────────────────

// inputNeed es un insumo de la receta con su consumo por lote. Fuel distingue
// el combustible (fuel_per_batch) de los ingredientes de rol input.
type inputNeed struct {
	product  botsdk.Product
	perBatch int64
	fuel     bool
}

// recipeNeeds deriva de la receta los insumos por lote (ingredientes input +
// combustible) y la salida por lote del producto objetivo. El bot NO cablea
// cantidades: las lee del catálogo.
func (b *IndustrialTransformer) recipeNeeds(ctx context.Context, c *botsdk.Client, st *State, recipe botsdk.Recipe) ([]inputNeed, int64, error) {
	output := st.products[b.cfg.OutputProductCode]
	var needs []inputNeed
	var outputPerBatch int64
	for _, ing := range recipe.Ingredients {
		qty, err := ing.Quantity.Int64()
		if err != nil {
			return nil, 0, fmt.Errorf("bots: cantidad inválida en la receta %s: %w", b.cfg.RecipeCode, err)
		}
		switch {
		case ing.Role == "output" && ing.ProductID == output.ID:
			outputPerBatch = qty
		case ing.Role == "input":
			p, err := productByID(ctx, c, st, ing.ProductID)
			if err != nil {
				return nil, 0, err
			}
			needs = append(needs, inputNeed{product: p, perBatch: qty})
		}
	}
	if recipe.FuelProductID != "" {
		fuelQty, err := recipe.FuelPerBatch.Int64()
		if err != nil {
			return nil, 0, fmt.Errorf("bots: fuel_per_batch inválido en la receta %s: %w", b.cfg.RecipeCode, err)
		}
		if fuelQty > 0 {
			p, err := productByID(ctx, c, st, recipe.FuelProductID)
			if err != nil {
				return nil, 0, err
			}
			needs = append(needs, inputNeed{product: p, perBatch: fuelQty, fuel: true})
		}
	}
	if outputPerBatch <= 0 {
		return nil, 0, fmt.Errorf("bots: la receta %s no produce %s", b.cfg.RecipeCode, b.cfg.OutputProductCode)
	}
	return needs, outputPerBatch, nil
}

// bidPrice es el precio que el transformador OFRECE por un insumo:
// base_price × InputBuyPriceBP. El ancla es el CATÁLOGO, no el mercado, a
// propósito: un comprador que puje siempre un 10% sobre el último precio
// impreso perseguiría sus propias operaciones y espiralaría el precio del
// insumo. La prima sobre el catálogo (10%) atrae vendedores y queda absorbida
// por el margen de venta (25% >> 10%).
func (b *IndustrialTransformer) bidPrice(product botsdk.Product) (int64, error) {
	base, err := product.BasePrice.Int64()
	if err != nil {
		return 0, fmt.Errorf("bots: base_price inválido de %s: %w", product.Code, err)
	}
	return applyBP(base, b.cfg.InputBuyPriceBP), nil
}

// estimatedUnitCost estima el coste unitario del bien manufacturado a PRECIO DE
// MERCADO de los insumos: (Σ consumo_por_lote × precio_de_referencia) /
// salida_por_lote, redondeado al alza (jamás subestima el coste). El precio de
// referencia es el cierre OHLC reciente —lo que el insumo cuesta de verdad— o
// el base_price si el mercado aún no tiene historia.
func (b *IndustrialTransformer) estimatedUnitCost(ctx context.Context, c *botsdk.Client, needs []inputNeed, outputPerBatch int64) (int64, error) {
	var batchCost int64
	for _, need := range needs {
		price, _, err := referencePrice(ctx, c, need.product, b.cfg.PriceWindowSimSeconds)
		if err != nil {
			return 0, err
		}
		batchCost += need.perBatch * price
	}
	return ceilDiv(batchCost, outputPerBatch), nil
}

// maintainInputBuys publica UNA solicitud de compra por insumo cuyo stock libre
// en el horno esté por debajo de su umbral (consumo por lote ×
// InputBufferBatches), con destino el nodo de la planta.
func (b *IndustrialTransformer) maintainInputBuys(ctx context.Context, c *botsdk.Client, st *State, needs []inputNeed) error {
	inputs := make([]any, 0, len(needs))
	awaiting := false
	for _, need := range needs {
		lowWater := need.perBatch * b.cfg.InputBufferBatches
		stock, err := stockFreeAt(ctx, c, need.product.ID, st.plantID)
		if err != nil {
			return err
		}
		if stock >= lowWater {
			inputs = append(inputs, slog.Group(need.product.Code,
				slog.Int64("stock", stock), slog.Int64("low_water", lowWater)))
			continue
		}
		active, err := myOpenPublication(ctx, c, st, botsdk.PublicationBuy, need.product.ID)
		if err != nil {
			return err
		}
		if active != nil {
			// Ya hay una solicitud activa de ese insumo: el horno espera una
			// entrega en curso, no está colgado.
			awaiting = true
			inputs = append(inputs, slog.Group(need.product.Code,
				slog.Int64("stock", stock), slog.Int64("low_water", lowWater),
				slog.String("buy_publication_id", active.ID)))
			continue
		}
		price, err := b.bidPrice(need.product)
		if err != nil {
			return err
		}
		qty := need.perBatch * b.cfg.InputBuyBatches
		qtyStr, err := botsdk.QtyFromInt64(qty)
		if err != nil {
			return err
		}
		pub, err := c.CreatePublication(ctx, botsdk.PublicationCreate{
			Kind:               botsdk.PublicationBuy,
			ProductID:          need.product.ID,
			QuantityTotal:      qtyStr,
			UnitPrice:          botsdk.MoneyFromInt64(price),
			DestinationNodeID:  st.plantNodeID,
			DeliverySimSeconds: b.cfg.InputBuyDeliverySimSeconds,
		})
		if err != nil {
			if code, blocked := blockedCode(err); blocked {
				b.decide("blocked", code, slog.String("step", "publish_input_buy"),
					slog.String("product", need.product.Code))
				continue
			}
			return fmt.Errorf("bots: publicando la compra de %s: %w", need.product.Code, err)
		}
		b.decide("publish_input_buy", "stock_below_low_water",
			slog.String("publication_id", pub.ID),
			slog.String("product", need.product.Code),
			slog.Bool("fuel", need.fuel),
			slog.Int64("stock", stock), slog.Int64("low_water", lowWater),
			slog.Int64("quantity", qty), slog.Int64("unit_price", price))
	}
	// Motivo del no-op de la etapa (§ base.pass): con insumos por debajo del
	// umbral y su compra ya publicada el bot ESPERA la entrega; con todos por
	// encima, simplemente no hay nada que comprar.
	reason := "inputs_stocked"
	if awaiting {
		reason = "awaiting_inputs"
	}
	b.idle(reason, inputs...)
	return nil
}

// ─── Producción y venta ──────────────────────────────────────────────────────

// maintainQueue mantiene la cola de fundición: pendientes < MinPendingBatches ⇒
// encolar BatchesPerQueue. Solo se llega aquí con margen esperado positivo.
func (b *IndustrialTransformer) maintainQueue(ctx context.Context, c *botsdk.Client, st *State, recipe botsdk.Recipe, unitCost, reference int64) error {
	pending, err := pendingBatches(ctx, c, st.plantID)
	if err != nil {
		return err
	}
	if pending >= b.cfg.MinPendingBatches {
		b.idle("queue_full",
			slog.String("building_id", st.plantID),
			slog.Int("pending", pending), slog.Int("min_pending", b.cfg.MinPendingBatches))
		return nil
	}
	if _, err := c.QueueProduction(ctx, st.plantID, botsdk.ProductionBatchCreate{
		RecipeID:      recipe.ID,
		BatchesQueued: b.cfg.BatchesPerQueue,
	}); err != nil {
		if code, blocked := blockedCode(err); blocked {
			b.decide("blocked", code, slog.String("step", "queue_batches"))
			return nil
		}
		return fmt.Errorf("bots: encolando lotes: %w", err)
	}
	b.decide("queue_batches", "positive_margin",
		slog.String("building_id", st.plantID),
		slog.Int("pending", pending), slog.Int("queued", b.cfg.BatchesPerQueue),
		slog.Int64("unit_cost", unitCost), slog.Int64("reference_price", reference))
	return nil
}

// maintainSell mantiene UNA venta activa del bien manufacturado: si no hay
// ninguna propia visible y el stock libre en la planta alcanza SellMinLot,
// publica min(stock, SellLotMax) a coste × (1 + MarginBP), acotado al techo de
// precio del catálogo.
func (b *IndustrialTransformer) maintainSell(ctx context.Context, c *botsdk.Client, st *State, output botsdk.Product, unitCost int64) error {
	mine, err := myOpenPublication(ctx, c, st, botsdk.PublicationSell, output.ID)
	if err != nil {
		return err
	}
	if mine != nil {
		// Regla de UNA venta activa.
		b.idle("sell_already_active",
			slog.String("product", b.cfg.OutputProductCode),
			slog.String("publication_id", mine.ID))
		return nil
	}
	free, err := stockFreeAt(ctx, c, output.ID, st.plantID)
	if err != nil {
		return err
	}
	if free < b.cfg.SellMinLot {
		b.idle("output_below_min_lot",
			slog.String("product", b.cfg.OutputProductCode),
			slog.Int64("stock", free), slog.Int64("min_lot", b.cfg.SellMinLot))
		return nil
	}
	price := applyBPCeil(unitCost, 10_000+b.cfg.MarginBP)
	if ceiling, cerr := output.PriceCeiling.Int64(); cerr == nil && ceiling > 0 && price > ceiling {
		price = ceiling
	}
	qty := min(free, b.cfg.SellLotMax)
	qtyStr, err := botsdk.QtyFromInt64(qty)
	if err != nil {
		return err
	}
	minLot, err := botsdk.QtyFromInt64(b.cfg.SellMinLot)
	if err != nil {
		return err
	}
	pub, err := c.CreatePublication(ctx, botsdk.PublicationCreate{
		Kind:               botsdk.PublicationSell,
		ProductID:          output.ID,
		QuantityTotal:      qtyStr,
		UnitPrice:          botsdk.MoneyFromInt64(price),
		MinLot:             minLot,
		OriginNodeID:       st.plantNodeID,
		DeliverySimSeconds: b.cfg.SellDeliverySimSeconds,
	})
	if err != nil {
		if code, blocked := blockedCode(err); blocked {
			b.decide("blocked", code, slog.String("step", "publish_sell"))
			return nil
		}
		return fmt.Errorf("bots: publicando la venta de %s: %w", b.cfg.OutputProductCode, err)
	}
	b.decide("publish_sell", "no_active_sell",
		slog.String("publication_id", pub.ID),
		slog.String("product", b.cfg.OutputProductCode),
		slog.Int64("quantity", qty),
		slog.Int64("unit_price", price),
		slog.Int64("unit_cost", unitCost),
		slog.Int64("margin_bp", b.cfg.MarginBP))
	return nil
}

// ─── Geometría de la implantación (reglas puras) ─────────────────────────────

// siteDirections son las ocho direcciones de la búsqueda de suelo libre
// (anillos alrededor del yacimiento ancla), en orden determinista.
var siteDirections = [8][2]float64{
	{1, 0}, {1, 1}, {0, 1}, {-1, 1}, {-1, 0}, {-1, -1}, {0, -1}, {1, -1},
}

// siteCandidate devuelve el centro del emplazamiento candidato i: anillos
// concéntricos de ocho direcciones alrededor de (ax, ay), con radio
// baseRadius + (i/8)·ringStep. Determinista: el mismo bot reintenta la misma
// secuencia tras un reinicio.
func siteCandidate(ax, ay float64, i int, baseRadius, ringStep float64) (float64, float64) {
	if i < 0 {
		i = 0
	}
	dir := siteDirections[i%len(siteDirections)]
	radius := baseRadius + float64(i/len(siteDirections))*ringStep
	return ax + dir[0]*radius, ay + dir[1]*radius
}

// polygonCenter devuelve el centro del anillo exterior de un polígono (media de
// sus vértices distintos; exacto para los cuadrados que publican los bots).
func polygonCenter(p botsdk.GeoPolygon) (float64, float64, bool) {
	if len(p.Coordinates) == 0 || len(p.Coordinates[0]) < 4 {
		return 0, 0, false
	}
	ring := p.Coordinates[0]
	// El anillo está cerrado: el último vértice repite el primero.
	ring = ring[:len(ring)-1]
	var sx, sy float64
	for _, v := range ring {
		sx += v[0]
		sy += v[1]
	}
	n := float64(len(ring))
	return sx / n, sy / n, true
}
