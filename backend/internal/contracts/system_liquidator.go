package contracts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/outbox"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// ConsumerSystemLiquidator es el nombre lógico del consumidor del outbox que
// subasta el stock embargado (cursor propio en outbox.consumer_cursors).
const ConsumerSystemLiquidator = "system_liquidator"

// eventBuildingSeized es el tipo del evento que emite world/enforcement cuando
// un edificio es embargado (cascada de insolvencia, GDD 11.2). Se declara aquí
// como string: contracts NUNCA importa internal/world — la integración es SOLO
// por el outbox (SAD §7 / ADR-006).
const eventBuildingSeized = "building.seized"

// SystemLiquidator implementa el consumidor contracts "system_liquidator" (GDD
// 11.2, cierre de la cascada de insolvencia): consume building.seized y subasta
// PÚBLICAMENTE el stock libre embargado. Por cada línea de stock del embargo, en
// la MISMA transacción del lote (exactly-once por el cursor del outbox):
//
//  1. TRANSFIERE el stock del edificio embargado a la cuenta stock_free del banco
//     central en ese mismo almacén (transacción 'auction', doble entrada por
//     producto: -qty al moroso, +qty al sistema). La mercancía no se mueve —
//     cambia de dueño in situ, se retira en la subasta desde su nodo de origen.
//  2. PUBLICA una oferta sell del sistema por esa cantidad, al precio de remate
//     (base_price * II_LIQUIDATION_PRICE_BP / 10000), por el MISMO camino que
//     cualquier venta del CCRI (bloqueo de stock_reserved + garantía del 10%).
//
// Cuando la oferta se venda, el comprador paga y los proceeds los cobra el banco
// central (el vendedor de la subasta es su caja): un efecto SINK/ABSORCIÓN,
// coherente con "remanente destruido como sink" del GDD — el moroso no tiene
// deuda monetaria residual (su caja se agotó en la cascada, saldo = 0 nunca
// deuda). La garantía del 10% la aporta el banco central de su caja; si no la
// cubre (típicamente la primera subasta, con la tesorería a cero), la EMITE de
// su cuenta de emisión (única cuenta que puede quedar negativa) como colateral,
// que retorna a su caja al liquidarse la venta: la operación es neutral para la
// masa monetaria de los jugadores.
//
// Es idempotente por building_id (tabla ledger.system_liquidations): además del
// exactly-once del cursor, un mismo embargo re-emitido o un redespliegue no
// re-subastan.
type SystemLiquidator struct {
	svc    *Service
	logger *slog.Logger

	publications    prometheus.Counter
	liquidatedStock *prometheus.CounterVec
	skipped         prometheus.Counter
}

// NewSystemLiquidator construye el liquidador sobre el Service del módulo
// (reutiliza su pool, repo, opciones y el núcleo de creación de publicación
// sell). reg registra sus métricas (nil las deja sin instrumentar: tests);
// logger nil usa el del servicio.
func NewSystemLiquidator(svc *Service, logger *slog.Logger, reg prometheus.Registerer) (*SystemLiquidator, error) {
	if svc == nil {
		return nil, errors.New("contracts: el system_liquidator requiere un Service")
	}
	if logger == nil {
		logger = svc.logger
	}
	l := &SystemLiquidator{
		svc:    svc,
		logger: logger,
		publications: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_liquidation_publications_total",
			Help: "Total de ofertas sell del sistema publicadas por la subasta del stock embargado.",
		}),
		liquidatedStock: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ii_liquidated_stock_total",
			Help: "Total de stock embargado subastado por el sistema, por producto.",
		}, []string{"product"}),
		skipped: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_liquidation_skipped_total",
			Help: "Total de building.seized ignorados por idempotencia (embargo ya subastado).",
		}),
	}
	if reg != nil {
		reg.MustRegister(l.publications, l.liquidatedStock, l.skipped)
	}
	return l, nil
}

// NewConsumer construye el consumidor lógico del outbox para building.seized. Lo
// arranca el engine con Handle como handler.
func (l *SystemLiquidator) NewConsumer(pool *pgxpool.Pool, opts ...outbox.ConsumerOption) *outbox.Consumer {
	return outbox.NewConsumer(pool, ConsumerSystemLiquidator, []string{eventBuildingSeized}, opts...)
}

// seizedStockItem es una línea de stock libre embargado (contrato de evento fijo
// del Incremento 6a). quantity viaja como string de punto fijo.
type seizedStockItem struct {
	ProductID           string `json:"product_id"`
	Quantity            string `json:"quantity"`
	WarehouseBuildingID string `json:"warehouse_building_id"`
}

// buildingSeizedEvent es el payload FIJO de building.seized (contrato de
// integración world/enforcement↔contracts). origin_node_id es el nodo logístico
// del edificio (retirada in situ); stock es TODO el stock_free del edificio en el
// momento del embargo.
type buildingSeizedEvent struct {
	BuildingID     string            `json:"building_id"`
	OwnerAccountID string            `json:"owner_account_id"`
	RegionID       string            `json:"region_id"`
	OriginNodeID   string            `json:"origin_node_id"`
	Reason         string            `json:"reason"`
	Stock          []seizedStockItem `json:"stock"`
	SeizedAtSim    int64             `json:"seized_at_sim"`
}

// liqOutcome acumula los efectos de una subasta para volcarlos a las métricas UNA
// sola vez tras el retorno con éxito del handler (un rollback+reintento del lote
// re-ejecuta el cuerpo; solo el resultado definitivo cuenta).
type liqOutcome struct {
	publications int
	perProduct   map[uuid.UUID]int64
}

// Handle procesa un building.seized dentro de la transacción del lote del
// consumidor (los efectos se confirman con el avance del cursor: exactly-once).
// Es, además, idempotente por building_id: el embargo se reclama ANTES de mover
// stock o publicar, de modo que un reproceso no re-subasta ni duplica el trabajo.
func (l *SystemLiquidator) Handle(ctx context.Context, tx pgx.Tx, ev outbox.Event) error {
	if ev.EventType != eventBuildingSeized {
		return nil
	}
	var e buildingSeizedEvent
	if err := json.Unmarshal(ev.Payload, &e); err != nil {
		return fmt.Errorf("contracts: leyendo building.seized (seq %d): %w", ev.Seq, err)
	}
	buildingID, err := uuid.Parse(e.BuildingID)
	if err != nil {
		return fmt.Errorf("contracts: building_id inválido en building.seized (seq %d): %w", ev.Seq, err)
	}
	formerOwner, err := uuid.Parse(e.OwnerAccountID)
	if err != nil {
		return fmt.Errorf("contracts: owner_account_id inválido en building.seized %s: %w", buildingID, err)
	}
	seizedAtSim := simtime.SimTime(e.SeizedAtSim)
	simNow := l.svc.sim.Now(ctx)

	r := l.svc.repo.WithTx(tx)

	// Idempotencia por building_id: reclama el embargo. Si ya estaba liquidado,
	// se ignora (reproceso o redespliegue) sin re-subastar.
	claimed, err := r.ClaimSystemLiquidation(ctx, buildingID, seizedAtSim, simNow)
	if err != nil {
		return err
	}
	if !claimed {
		l.skipped.Inc()
		l.logger.Debug("contracts: building.seized ya subastado; ignorado por idempotencia",
			slog.String("building_id", buildingID.String()))
		return nil
	}

	// El banco central (cuenta de sistema) es el dueño de la cuenta sink: de ahí
	// derivamos su cuenta de auth, publicador de la subasta y perceptor de los
	// proceeds (absorción).
	sink, err := r.GetSinkAccount(ctx)
	if err != nil {
		return fmt.Errorf("contracts: localizando la cuenta sink del banco central: %w", err)
	}
	if sink.OwnerAccountID == nil {
		return fmt.Errorf("contracts: la cuenta sink no tiene titular; no se puede identificar el banco central para la subasta de %s", buildingID)
	}
	systemAccount := *sink.OwnerAccountID

	// Sin nodo logístico no hay dónde publicar la retirada in situ: se registra y
	// se omite (el embargo ya quedó reclamado — no se reintenta en bucle). Es
	// defensivo: world/enforcement siempre aporta el nodo del edificio.
	if e.OriginNodeID == "" {
		l.logger.Warn("contracts: building.seized sin nodo de origen; subasta omitida",
			slog.String("building_id", buildingID.String()))
		return nil
	}
	originNodeID, err := uuid.Parse(e.OriginNodeID)
	if err != nil {
		return fmt.Errorf("contracts: origin_node_id inválido en building.seized %s: %w", buildingID, err)
	}

	oc := &liqOutcome{perProduct: make(map[uuid.UUID]int64)}
	for _, item := range e.Stock {
		product, err := uuid.Parse(item.ProductID)
		if err != nil {
			return fmt.Errorf("contracts: product_id inválido en building.seized %s: %w", buildingID, err)
		}
		warehouse, err := uuid.Parse(item.WarehouseBuildingID)
		if err != nil {
			return fmt.Errorf("contracts: warehouse_building_id inválido en building.seized %s: %w", buildingID, err)
		}
		qty, err := strconv.ParseInt(item.Quantity, 10, 64)
		if err != nil {
			return fmt.Errorf("contracts: quantity inválida en building.seized %s: %q", buildingID, item.Quantity)
		}
		if qty <= 0 {
			continue // línea vacía (defensivo): nada que subastar
		}

		if err := l.liquidateLine(ctx, r, tx, systemAccount, formerOwner, originNodeID, buildingID, product, warehouse, qty, simNow); err != nil {
			return err
		}
		oc.publications++
		oc.perProduct[product] += qty
	}

	l.flush(oc)
	l.logger.Info("stock embargado subastado por el sistema",
		slog.String("building_id", buildingID.String()),
		slog.String("reason", e.Reason),
		slog.Int("publications", oc.publications))
	return nil
}

// liquidateLine subasta UNA línea de stock embargado: transfiere el stock al
// banco central y publica la oferta sell del sistema por el núcleo compartido del
// módulo. Todo en la transacción del lote (tx) sobre el Repo ya ligado (r).
func (l *SystemLiquidator) liquidateLine(ctx context.Context, r *Repo, tx pgx.Tx, systemAccount, formerOwner, originNodeID, buildingID, product, warehouse uuid.UUID, qty int64, simNow simtime.SimTime) error {
	// (1) Transferencia del stock: stock_free(moroso) → stock_free(sistema) en el
	// mismo almacén (doble entrada por producto, transacción 'auction').
	from, err := r.GetStockFreeAccount(ctx, formerOwner, product, warehouse)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("contracts: el embargo %s declara stock de %s en %s sin cuenta stock_free (payload inconsistente)", buildingID, product, warehouse)
	case err != nil:
		return fmt.Errorf("contracts: leyendo el stock_free embargado de %s: %w", formerOwner, err)
	}
	to, err := r.EnsureStockFreeAccount(ctx, systemAccount, product, warehouse)
	if err != nil {
		return err
	}
	if err := r.PostLedgerTransaction(ctx, txKindAuction, simNow, buildingID,
		fmt.Sprintf("Subasta de embargo: %d transferido al banco central", qty),
		[]entryAmount{
			{AccountID: from.ID, Amount: -qty},
			{AccountID: to.ID, Amount: qty},
		}); err != nil {
		return err
	}

	// (2) Precio de remate y garantía del 10% del valor de la oferta.
	basePrice, err := r.GetProductBasePrice(ctx, product)
	if err != nil {
		return fmt.Errorf("contracts: leyendo el base_price de %s para la subasta: %w", product, err)
	}
	price := liquidationPrice(basePrice, l.svc.opts.LiquidationPriceBP)
	value, guarantee, err := lockAmounts(qty, price)
	if err != nil {
		return err
	}

	// (3) Colateral de la garantía: el banco central la aporta de su caja; si no
	// la cubre, la EMITE (colateral de subasta) — retorna a su caja al liquidarse.
	if err := l.ensureGuaranteeCollateral(ctx, r, systemAccount, guarantee, buildingID, simNow); err != nil {
		return err
	}

	// (4) Publicación sell del sistema por el MISMO camino que cualquier venta:
	// bloquea el stock recién transferido en stock_reserved y la garantía.
	origin := originNodeID
	prod := product
	in := PublicationInput{
		Kind:               KindSell,
		Channel:            ChannelBoard,
		ProductID:          &prod,
		QuantityTotal:      qty,
		UnitPrice:          price,
		MinLot:             1,
		OriginNodeID:       &origin,
		DeliverySimSeconds: l.svc.opts.PublicationTTLSimSeconds, // plazo generoso (retirada in situ)
	}
	if err := normalizePublicationInput(systemAccount, &in); err != nil {
		return err
	}
	pub, err := l.svc.createPublicationTx(ctx, r, tx, systemAccount, in, value, guarantee, simNow)
	if err != nil {
		return mapLedgerError(err)
	}
	l.logger.Info("oferta de subasta del sistema publicada",
		slog.String("publication_id", pub.ID.String()),
		slog.String("building_id", buildingID.String()),
		slog.String("product_id", product.String()),
		slog.Int64("quantity", qty),
		slog.Int64("unit_price", price))
	return nil
}

// ensureGuaranteeCollateral garantiza que la caja del banco central cubre la
// garantía del 10% de la próxima oferta. Si no la cubre, EMITE el faltante de la
// cuenta de emisión a la caja del sistema (asiento 'auction'): +faltante caja /
// -faltante emisión (la emisión puede quedar negativa; la caja jamás). El
// colateral retorna a la caja al liquidarse la venta (settle_contract_prorata
// devuelve la garantía al vendedor con fill 100%), neutral para la masa monetaria
// de los jugadores.
func (l *SystemLiquidator) ensureGuaranteeCollateral(ctx context.Context, r *Repo, systemAccount uuid.UUID, guarantee int64, buildingID uuid.UUID, simNow simtime.SimTime) error {
	cash, err := r.EnsureCashAccount(ctx, systemAccount)
	if err != nil {
		return err
	}
	if cash.Balance >= guarantee {
		return nil
	}
	shortfall := guarantee - cash.Balance
	emission, err := r.GetEmissionAccount(ctx)
	if err != nil {
		return fmt.Errorf("contracts: localizando la cuenta de emisión del banco central: %w", err)
	}
	return r.PostLedgerTransaction(ctx, txKindAuction, simNow, buildingID,
		fmt.Sprintf("Subasta de embargo: emisión de colateral de garantía (%d)", shortfall),
		[]entryAmount{
			{AccountID: cash.ID, Amount: shortfall},
			{AccountID: emission.ID, Amount: -shortfall},
		})
}

// flush vuelca los efectos acumulados a las métricas tras el retorno con éxito
// del handler (evita el doble conteo si un reintento re-ejecutó el cuerpo).
func (l *SystemLiquidator) flush(oc *liqOutcome) {
	if oc.publications > 0 {
		l.publications.Add(float64(oc.publications))
	}
	for product, qty := range oc.perProduct {
		l.liquidatedStock.WithLabelValues(product.String()).Add(float64(qty))
	}
}

// liquidationPrice calcula el precio de remate base_price * bp / 10000 con
// math/big (sin desbordar) y lo acota a >= 1 (unit_price debe ser > 0).
func liquidationPrice(basePrice int64, bp int) int64 {
	p := new(big.Int).Mul(big.NewInt(basePrice), big.NewInt(int64(bp)))
	p.Quo(p, big.NewInt(bpDenominator))
	if !p.IsInt64() || p.Int64() < 1 {
		return 1
	}
	return p.Int64()
}
