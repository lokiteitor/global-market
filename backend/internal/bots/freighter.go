package bots

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/lokiteitor/global-market/backend/pkg/botsdk"
)

// FreighterConfig son los umbrales auditables del arquetipo freighter. Se
// persisten como behavior JSON en auth.bot_profiles al aprovisionar.
type FreighterConfig struct {
	// VehicleTypeCode es el vehículo que compra para prestar el servicio.
	// v1 opera SOLO por CARRETERA (truck_small): el despacho es por tramo de un
	// único modo y un tramo rail/sea exige terminal de transbordo y slot de
	// prioridad en ambos extremos. El plan de ruta se restringe al modo del
	// vehículo, así que ampliar la flota a tren/barco es cambiar este código y
	// MaxVehicles — la evaluación de rentabilidad ya es genérica por modo.
	VehicleTypeCode string `json:"vehicle_type_code"`
	// MaxVehicles acota la flota: cada vehículo libre atiende un flete a la vez.
	// Con la flota completa, un vehículo libre en OTRO nodo no está perdido: se
	// reposiciona EN VACÍO hasta la recogida (uno por pasada).
	MaxVehicles int `json:"max_vehicles"`
	// MarginBP es el margen exigido sobre el coste estimado del trayecto, en
	// basis points (2000 ⇒ acepta si ingreso >= coste × 1,20).
	MarginBP int64 `json:"margin_bp"`
	// GuaranteeBP replica la garantía del transportista del servidor
	// (II_FREIGHT_GUARANTEE_BP, no expuesta por la API): fracción del valor
	// declarado que se inmoviliza al aceptar. Si el servidor usara otra, el bot
	// solo se equivocaría en su margen estimado, nunca en la contabilidad (la
	// garantía real la bloquea el ledger).
	GuaranteeBP int64 `json:"guarantee_bp"`
	// GuaranteeRiskBP es el coste de oportunidad imputado a la garantía
	// inmovilizada, en basis points sobre ella (500 ⇒ el 5% de la garantía
	// entra en el coste del viaje).
	GuaranteeRiskBP int64 `json:"guarantee_risk_bp"`
	// CashCushionBP es el colchón de caja que NUNCA se compromete, como
	// fracción del capital en basis points.
	CashCushionBP int64 `json:"cash_cushion_bp"`
	// Capital es el capital de referencia del bot (ancla del colchón).
	Capital int64 `json:"capital"`
	// PriceWindowSimSeconds es la ventana de sim-time del precio de referencia
	// OHLC del combustible.
	PriceWindowSimSeconds int64 `json:"price_window_sim_seconds"`
	// MaxEvaluationsPerPass acota cuántas solicitudes de flete se evalúan (y
	// por tanto se auditan) en una pasada.
	MaxEvaluationsPerPass int `json:"max_evaluations_per_pass"`
}

// DefaultFreighterConfig son los umbrales por defecto del transportista.
// marginBP es el margen exigido (II_BOTS_FREIGHTER_MARGIN_BP) y capital la
// capitalización del bot (II_BOTS_CAPITAL).
func DefaultFreighterConfig(marginBP, capital int64) FreighterConfig {
	return FreighterConfig{
		VehicleTypeCode:       "truck_small",
		MaxVehicles:           2,
		MarginBP:              marginBP,
		GuaranteeBP:           1_000,
		GuaranteeRiskBP:       500,
		CashCushionBP:         2_000,
		Capital:               capital,
		PriceWindowSimSeconds: 7 * simDaySeconds,
		MaxEvaluationsPerPass: 20,
	}
}

// Freighter es el arquetipo transportista (GDD 13.2: "ofrecen servicios de
// flete"), posible desde el CCRI-Flete del GDD 5.3.2. NO produce ni comercia
// mercancía: vende capacidad de transporte. Una pasada de Decide:
//
//  1. FLOTA: asegura al menos un vehículo. No compra "por si acaso": compra
//     cuando un flete rentable lo exige, entregado en el nodo de origen de ese
//     flete (así el vehículo nace donde hace falta). Con la flota completa,
//     manda EN VACÍO hasta la recogida un camión libre que quedó en el destino
//     de su entrega anterior (uno por pasada), en vez de quedarse esperando una
//     carga que casualmente nazca donde está aparcado.
//  2. EVALUACIÓN: escanea el tablón por publicaciones kind=freight (solicitudes
//     de OTROS cargadores) y para cada candidata computa
//     ingreso = cantidad × tarifa y coste = combustible del trayecto
//     (fuel_per_100km del vehículo × distancia del plan × precio de referencia
//     del combustible) + opex (operating_cost_per_day × días de la ETA) +
//     riesgo de la garantía inmovilizada (valor declarado × GuaranteeBP ×
//     GuaranteeRiskBP). Decisión auditable evaluate_freight con las cifras.
//  3. ACEPTACIÓN: acepta UNA por pasada si ingreso >= coste × (1 + MarginBP),
//     la ETA del plan cabe en el plazo pactado (una entrega tardía no cobra y
//     penaliza la garantía), hay vehículo libre en el origen y la caja cubre la
//     garantía sin tocar el colchón. Si no: skip_freight con el motivo
//     (below_margin, eta_over_deadline, guarantee_over_budget…).
//  4. EJECUCIÓN: con el flete confirmado localiza SU cargamento (dueño = el
//     cargador, visible para el transportista), planifica la ruta
//     origen→destino en el modo de su vehículo, la crea y DESPACHA. La entrega
//     la liquida el settler al llegar físicamente: cobra el flete y recupera la
//     garantía.
type Freighter struct {
	base
	cfg FreighterConfig
}

// NewFreighter construye el arquetipo con sus umbrales.
func NewFreighter(cfg FreighterConfig, botName string, logger *slog.Logger, metrics *Metrics) *Freighter {
	return &Freighter{base: newBase(botName, "freighter", logger, metrics), cfg: cfg}
}

// Name implementa Behavior.
func (b *Freighter) Name() string { return "freighter" }

// ConfigJSON serializa los umbrales para auth.bot_profiles.behavior.
func (b *Freighter) ConfigJSON() ([]byte, error) { return json.Marshal(b.cfg) }

// cushion es la caja mínima intocable.
func (b *Freighter) cushion() int64 { return applyBP(b.cfg.Capital, b.cfg.CashCushionBP) }

// vehiclePolicy es la política de flota del transportista.
func (b *Freighter) vehiclePolicy() vehiclePolicy {
	return vehiclePolicy{typeCode: b.cfg.VehicleTypeCode, max: b.cfg.MaxVehicles}
}

// Decide implementa Behavior: primero cumple lo aceptado (despachar es
// obligación contractual con garantía en juego), luego busca carga nueva.
func (b *Freighter) Decide(ctx context.Context, c *botsdk.Client, st *State) error {
	if err := ensureIdentity(ctx, c, st); err != nil {
		return err
	}
	if _, err := cashBalance(ctx, c, st); err != nil {
		return err
	}
	if err := b.dispatchAcceptedFreights(ctx, c, st); err != nil {
		return err
	}
	cash, err := cashBalance(ctx, c, st)
	if err != nil {
		return err
	}
	return b.scanFreightBoard(ctx, c, st, cash)
}

// ─── Evaluación y aceptación ─────────────────────────────────────────────────

// freightQuote es la valoración de una solicitud de flete (todo en unidades
// menores de dinero).
type freightQuote struct {
	quantity   int64
	revenue    int64
	distanceM  int64
	etaSim     int64
	fuelUnits  int64
	fuelCost   int64
	opexCost   int64
	guarantee  int64
	riskCost   int64
	totalCost  int64
	required   int64 // coste × (1 + MarginBP): el ingreso mínimo aceptable
	profitable bool
}

// scanFreightBoard recorre las solicitudes de flete del tablón (mejor tarifa
// primero) y acepta como máximo UNA por pasada. Todo final del barrido sin
// aceptación emite una decisión terminal (wait/no_freight_on_board o
// wait/evaluation_cap_reached): una pasada ociosa DEBE dejar rastro, porque
// un transportista sin carga rentable no puede ser indistinguible de uno
// colgado mirando ii_bot_decisions_total.
func (b *Freighter) scanFreightBoard(ctx context.Context, c *botsdk.Client, st *State, cash int64) error {
	budget := cash - b.cushion()
	if budget <= 0 {
		b.decide("wait", "cash_at_cushion", slog.Int64("cash", cash), slog.Int64("cushion", b.cushion()))
		return nil
	}
	vt, err := vehicleTypeByCode(ctx, c, st, b.cfg.VehicleTypeCode)
	if err != nil {
		return err
	}
	evaluated := 0
	scanned := 0
	for pub, err := range botsdk.All(ctx, func(ctx context.Context, cursor string) (botsdk.Page[botsdk.Publication], error) {
		return c.Board(ctx, botsdk.BoardQuery{
			Kind:      botsdk.PublicationFreight,
			Sort:      botsdk.SortUnitPriceDesc,
			PageQuery: botsdk.PageQuery{Cursor: cursor, Limit: 200},
		})
	}) {
		if err != nil {
			return fmt.Errorf("bots: consultando las solicitudes de flete: %w", err)
		}
		scanned++
		if pub.PublisherAccountID == st.AccountID {
			continue
		}
		if _, already := st.pendingAcceptances[pub.ID]; already {
			continue
		}
		if evaluated >= b.cfg.MaxEvaluationsPerPass {
			b.decide("wait", "evaluation_cap_reached",
				slog.Int("scanned", scanned), slog.Int("evaluated", evaluated),
				slog.Int("max_evaluations_per_pass", b.cfg.MaxEvaluationsPerPass))
			return nil
		}
		if pub.OriginNodeID == "" || pub.DestinationNodeID == "" || pub.ProductID == "" {
			continue
		}
		qty, ok, err := b.haulableQty(ctx, c, st, pub, vt)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		plan, ok, err := planRoute(ctx, c, st, &b.base, vt.Mode, pub.OriginNodeID, pub.DestinationNodeID)
		if err != nil {
			return err
		}
		if !ok {
			continue // sin ruta ejecutable: espera ya registrada
		}
		quote, err := b.quote(ctx, c, st, pub, vt, plan, qty)
		if err != nil {
			return err
		}
		evaluated++
		b.decide("evaluate_freight", "board_candidate",
			slog.String("publication_id", pub.ID),
			slog.Int64("quantity", quote.quantity),
			slog.Int64("revenue", quote.revenue),
			slog.Int64("distance_m", quote.distanceM),
			slog.Int64("eta_sim_seconds", quote.etaSim),
			slog.Int64("fuel_cost", quote.fuelCost),
			slog.Int64("opex_cost", quote.opexCost),
			slog.Int64("guarantee", quote.guarantee),
			slog.Int64("risk_cost", quote.riskCost),
			slog.Int64("total_cost", quote.totalCost),
			slog.Int64("required_revenue", quote.required))
		if !quote.profitable {
			b.decide("skip_freight", "below_margin",
				slog.String("publication_id", pub.ID),
				slog.Int64("revenue", quote.revenue),
				slog.Int64("required_revenue", quote.required),
				slog.Int64("margin_bp", b.cfg.MarginBP))
			continue
		}
		// Plazo: una entrega fuera de plazo NO cobra y penaliza la garantía
		// (GDD 5.3.2). Si la ETA del plan ya no cabe en el plazo pactado, el
		// flete es una pérdida segura por muy bien pagado que esté.
		if pub.DeliverySimSeconds > 0 && quote.etaSim > pub.DeliverySimSeconds {
			b.decide("skip_freight", "eta_over_deadline",
				slog.String("publication_id", pub.ID),
				slog.Int64("eta_sim_seconds", quote.etaSim),
				slog.Int64("delivery_sim_seconds", pub.DeliverySimSeconds))
			continue
		}
		if quote.guarantee > budget {
			b.decide("skip_freight", "guarantee_over_budget",
				slog.String("publication_id", pub.ID),
				slog.Int64("guarantee", quote.guarantee), slog.Int64("budget", budget))
			continue
		}
		// Vehículo libre EN EL ORIGEN (compra uno entregado allí, o manda en
		// vacío uno varado en otro nodo): sin él no se puede prestar el servicio
		// y la garantía quedaría en riesgo. No se acepta el flete hasta tenerlo
		// en el origen: aceptar sin poder recoger es incumplir y quemar la
		// garantía.
		slot, err := ensureVehicleAt(ctx, c, st, &b.base, b.vehiclePolicy(), pub.OriginNodeID)
		if err != nil {
			return err
		}
		if slot.repositioning {
			// Un viaje en vacío por pasada: la flota no se dispersa hacia varias
			// recogidas especulativas a la vez.
			return nil
		}
		if !slot.ready {
			continue // espera ya registrada
		}
		vehicleID := slot.id
		qtyStr, err := botsdk.QtyFromInt64(quote.quantity)
		if err != nil {
			return err
		}
		acc, err := c.Accept(ctx, pub.ID, qtyStr, "")
		if err != nil {
			if code, blocked := blockedCode(err); blocked {
				b.decide("blocked", code, slog.String("step", "accept_freight"),
					slog.String("publication_id", pub.ID))
				return nil
			}
			return fmt.Errorf("bots: aceptando la solicitud de flete %s: %w", pub.ID, err)
		}
		st.pendingAcceptances[pub.ID] = acc.ID
		b.decide("accept_freight", "revenue_covers_cost_and_margin",
			slog.String("publication_id", pub.ID),
			slog.String("acceptance_id", acc.ID),
			slog.String("vehicle_id", vehicleID),
			slog.Int64("quantity", quote.quantity),
			slog.Int64("revenue", quote.revenue),
			slog.Int64("total_cost", quote.totalCost),
			slog.Int64("guarantee", quote.guarantee))
		return nil // una aceptación por pasada: disciplina de flota y garantía
	}
	// Barrido completo sin flete aceptado: decisión terminal auditable.
	b.decide("wait", "no_freight_on_board",
		slog.Int("scanned", scanned),
		slog.Int("evaluated", evaluated),
		slog.Int64("budget", budget),
		slog.Int64("margin_bp", b.cfg.MarginBP))
	return nil
}

// haulableQty decide cuánta carga de una solicitud puede llevar: lo restante
// acotado por la capacidad del vehículo (cargo_capacity >= cantidad × volumen
// unitario, la misma regla que valida el despacho) y por su min_lot.
func (b *Freighter) haulableQty(ctx context.Context, c *botsdk.Client, st *State, pub botsdk.Publication, vt botsdk.VehicleType) (int64, bool, error) {
	remaining, err := pub.QuantityRemaining.Int64()
	if err != nil || remaining <= 0 {
		return 0, false, nil
	}
	minLot, err := pub.MinLot.Int64()
	if err != nil {
		return 0, false, nil
	}
	cargo, err := productByID(ctx, c, st, pub.ProductID)
	if err != nil {
		return 0, false, err
	}
	capacity, err := vt.CargoCapacity.Int64()
	if err != nil {
		return 0, false, fmt.Errorf("bots: cargo_capacity inválida de %s: %w", vt.Code, err)
	}
	unitVolume := int64(cargo.UnitVolume)
	if unitVolume <= 0 {
		unitVolume = 1
	}
	qty := acceptQty(remaining, minLot, capacity/unitVolume)
	if qty <= 0 {
		b.decide("skip_freight", "cargo_over_vehicle_capacity",
			slog.String("publication_id", pub.ID),
			slog.String("product", cargo.Code),
			slog.Int64("remaining", remaining), slog.Int64("min_lot", minLot),
			slog.Int64("capacity", capacity), slog.Int64("unit_volume", unitVolume))
		return 0, false, nil
	}
	return qty, true, nil
}

// quote valora una solicitud de flete para una cantidad concreta: ingreso de la
// tarifa contra coste del trayecto (combustible + opex + riesgo de la garantía).
func (b *Freighter) quote(ctx context.Context, c *botsdk.Client, st *State, pub botsdk.Publication, vt botsdk.VehicleType, plan botsdk.RoutePlan, qty int64) (freightQuote, error) {
	tariff, err := pub.UnitPrice.Int64()
	if err != nil {
		return freightQuote{}, fmt.Errorf("bots: tarifa inválida en la solicitud %s: %w", pub.ID, err)
	}
	declared, err := pub.DeclaredValue.Int64()
	if err != nil {
		return freightQuote{}, fmt.Errorf("bots: valor declarado inválido en la solicitud %s: %w", pub.ID, err)
	}
	total, err := pub.QuantityTotal.Int64()
	if err != nil || total <= 0 {
		return freightQuote{}, fmt.Errorf("bots: cantidad total inválida en la solicitud %s: %q", pub.ID, pub.QuantityTotal)
	}
	distanceM, err := planDistanceM(ctx, c, st, plan)
	if err != nil {
		return freightQuote{}, err
	}

	fuelPer100km, err := vt.FuelPer100Km.Int64()
	if err != nil {
		return freightQuote{}, fmt.Errorf("bots: fuel_per_100km inválido de %s: %w", vt.Code, err)
	}
	// Combustible del trayecto (100 km = 100 000 m), redondeado al alza: la
	// estimación del bot nunca queda por debajo del consumo que el motor de
	// tránsito le exigirá al despachar.
	fuelUnits := ceilDiv(fuelPer100km*distanceM, 100_000)
	fuelPrice := int64(0)
	if vt.FuelProductID != "" && fuelUnits > 0 {
		fuelProduct, perr := productByID(ctx, c, st, vt.FuelProductID)
		if perr != nil {
			return freightQuote{}, perr
		}
		fuelPrice, _, perr = referencePrice(ctx, c, fuelProduct, b.cfg.PriceWindowSimSeconds)
		if perr != nil {
			return freightQuote{}, perr
		}
	}
	opexPerDay, err := vt.OperatingCostPerDay.Int64()
	if err != nil {
		return freightQuote{}, fmt.Errorf("bots: operating_cost_per_day inválido de %s: %w", vt.Code, err)
	}
	days := ceilDiv(plan.TotalEtaSimSeconds, simDaySeconds)
	if days < 1 {
		days = 1
	}
	// Garantía inmovilizada: la parte proporcional del valor declarado (misma
	// regla pro-rata del servidor) por la fracción de garantía.
	declaredPortion := declared * qty / total
	guarantee := applyBP(declaredPortion, b.cfg.GuaranteeBP)

	q := freightQuote{
		quantity:  qty,
		revenue:   qty * tariff,
		distanceM: distanceM,
		etaSim:    plan.TotalEtaSimSeconds,
		fuelUnits: fuelUnits,
		fuelCost:  fuelUnits * fuelPrice,
		opexCost:  opexPerDay * days,
		guarantee: guarantee,
		riskCost:  applyBPCeil(guarantee, b.cfg.GuaranteeRiskBP),
	}
	q.totalCost = q.fuelCost + q.opexCost + q.riskCost
	q.required = applyBPCeil(q.totalCost, 10_000+b.cfg.MarginBP)
	q.profitable = q.revenue >= q.required
	return q, nil
}

// ─── Ejecución de los fletes aceptados ───────────────────────────────────────

// dispatchAcceptedFreights despacha los cargamentos de los fletes activos en
// los que es transportista: cargamento en almacén (o en terminal) → vehículo
// libre en ese nodo → ruta hasta el destino → dispatch. Con varios vehículos
// atiende varios fletes en la misma pasada.
func (b *Freighter) dispatchAcceptedFreights(ctx context.Context, c *botsdk.Client, st *State) error {
	freights, err := botsdk.CollectAll(ctx, func(ctx context.Context, cursor string) (botsdk.Page[botsdk.FreightContract], error) {
		return c.ListFreightContracts(ctx, botsdk.FreightContractsQuery{
			Role:      botsdk.RoleCarrier,
			Status:    botsdk.ContractActive,
			PageQuery: botsdk.PageQuery{Cursor: cursor, Limit: 200},
		})
	})
	if err != nil {
		return fmt.Errorf("bots: listando los fletes propios: %w", err)
	}
	if len(freights) == 0 {
		return nil
	}
	vt, err := vehicleTypeByCode(ctx, c, st, b.cfg.VehicleTypeCode)
	if err != nil {
		return err
	}
	for _, fc := range freights {
		shipments, err := botsdk.CollectAll(ctx, func(ctx context.Context, cursor string) (botsdk.Page[botsdk.Shipment], error) {
			return c.ListShipments(ctx, botsdk.ShipmentsQuery{
				FreightContractID: fc.ID,
				PageQuery:         botsdk.PageQuery{Cursor: cursor, Limit: 200},
			})
		})
		if err != nil {
			return fmt.Errorf("bots: listando los cargamentos del flete %s: %w", fc.ID, err)
		}
		for _, sh := range shipments {
			if sh.AtNodeID == "" {
				continue // ya viaja a bordo (in_transit) o fue entregado
			}
			if sh.Status != botsdk.ShipmentInWarehouse && sh.Status != botsdk.ShipmentAtTerminal {
				continue
			}
			if sh.AtNodeID == fc.DestinationNodeID {
				continue // ya está en destino: la liquidación es del settler
			}
			slot, err := ensureVehicleAt(ctx, c, st, &b.base, b.vehiclePolicy(), sh.AtNodeID)
			if err != nil {
				return err
			}
			if !slot.ready {
				continue // espera o viaje en vacío hacia la recogida ya registrado
			}
			vehicleID := slot.id
			routeID, ok, err := ensureRoute(ctx, c, st, &b.base, vt.Mode, sh.AtNodeID, fc.DestinationNodeID)
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
				return fmt.Errorf("bots: despachando el cargamento %s del flete %s: %w", sh.ID, fc.ID, err)
			}
			b.decide("dispatch", "freight_cargo_ready",
				slog.String("shipment_id", sh.ID),
				slog.String("freight_contract_id", fc.ID),
				slog.String("vehicle_id", vehicleID),
				slog.String("route_id", routeID),
				slog.String("origin_node_id", sh.AtNodeID),
				slog.String("destination_node_id", fc.DestinationNodeID))
		}
	}
	return nil
}
