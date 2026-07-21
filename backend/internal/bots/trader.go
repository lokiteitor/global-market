package bots

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/lokiteitor/global-market/backend/pkg/botsdk"
)

// TraderConfig son los umbrales auditables del arquetipo trader.
type TraderConfig struct {
	// ProductCodes son los productos que el comerciante arbitra.
	ProductCodes []string `json:"product_codes"`
	// BuyMaxPriceBP es el precio máximo de compra como fracción del
	// base_price en basis points: 9500 ⇒ compra si unit_price <= 95%.
	BuyMaxPriceBP int64 `json:"buy_max_price_bp"`
	// MarginBP es el margen de re-listado sobre el precio pagado en basis
	// points (al alza): 11500 ⇒ re-lista a paid × 1,15 (techo).
	MarginBP int64 `json:"margin_bp"`
	// Capital es el capital de referencia del bot (su capitalización).
	Capital int64 `json:"capital"`
	// CashCushionBP es el colchón de caja que NUNCA se compromete, como
	// fracción del capital en basis points: 2000 ⇒ jamás gasta por debajo del
	// 20% del capital (límite de exposición).
	CashCushionBP int64 `json:"cash_cushion_bp"`
	// MaxLotQty acota la cantidad de cada compra y de cada re-listado.
	MaxLotQty int64 `json:"max_lot_qty"`
	// RelistMinLot es el min_lot del re-listado y el stock mínimo para
	// re-listar.
	RelistMinLot int64 `json:"relist_min_lot"`
	// SellDeliverySimSeconds es el plazo declarado del re-listado.
	SellDeliverySimSeconds int64 `json:"sell_delivery_sim_seconds"`
}

// DefaultTraderConfig son los umbrales por defecto del trader. capital es la
// capitalización del bot (II_BOTS_CAPITAL): ancla del colchón de caja.
func DefaultTraderConfig(capital int64) TraderConfig {
	return TraderConfig{
		ProductCodes:           []string{"coal", "iron_ore"},
		BuyMaxPriceBP:          9_500,
		MarginBP:               11_500,
		Capital:                capital,
		CashCushionBP:          2_000,
		MaxLotQty:              500,
		RelistMinLot:           50,
		SellDeliverySimSeconds: simDaySeconds,
	}
}

// Trader es el arquetipo comerciante (ADR-024, Fase 0): compra barato en el
// tablón y re-lista con margen.
//
// REGLA FINAL DE RE-LISTADO (verificada contra internal/contracts): al
// aceptar una venta (sell) la entrega es in situ y la liquidación deja el
// stock comprado como stock_free del comprador EN EL ALMACÉN DE ORIGEN del
// vendedor. Publicar una venta exige que el nodo de origen tenga edificio y
// que el PUBLICADOR tenga stock_free suficiente en ese almacén — NO exige ser
// dueño del edificio (a diferencia de aceptar una compra, que valida
// ErrNotNodeOwner). Por tanto el trader SÍ puede re-listar desde el almacén
// ajeno donde reposa lo comprado, y esa es su regla: re-lista cada lote desde
// el nodo donde está su stock, a precio pagado × margen (techo), manteniendo
// UNA venta activa por producto.
//
// Límite de exposición: nunca compromete caja (escrow de compra ni garantía
// de re-listado) por debajo del colchón CashCushionBP × Capital.
type Trader struct {
	base
	cfg TraderConfig
}

// NewTrader construye el arquetipo con sus umbrales.
func NewTrader(cfg TraderConfig, botName string, logger *slog.Logger, metrics *Metrics) *Trader {
	return &Trader{base: newBase(botName, "trader", logger, metrics), cfg: cfg}
}

// Name implementa Behavior.
func (b *Trader) Name() string { return "trader" }

// ConfigJSON serializa los umbrales para auth.bot_profiles.behavior.
func (b *Trader) ConfigJSON() ([]byte, error) { return json.Marshal(b.cfg) }

// cushion es la caja mínima intocable.
func (b *Trader) cushion() int64 { return applyBP(b.cfg.Capital, b.cfg.CashCushionBP) }

// Decide implementa Behavior: re-lista el stock comprado y escanea el tablón
// en busca de gangas (una aceptación por pasada).
func (b *Trader) Decide(ctx context.Context, c *botsdk.Client, st *State) error {
	if err := ensureIdentity(ctx, c, st); err != nil {
		return err
	}
	cash, err := cashBalance(ctx, c, st)
	if err != nil {
		return err
	}
	if err := b.relist(ctx, c, st, cash); err != nil {
		return err
	}
	// La caja pudo cambiar al bloquear garantías de re-listado.
	cash, err = cashBalance(ctx, c, st)
	if err != nil {
		return err
	}
	return b.scanBargains(ctx, c, st, cash)
}

// relist publica una venta por producto con stock propio pendiente,
// manteniendo UNA venta activa por producto.
func (b *Trader) relist(ctx context.Context, c *botsdk.Client, st *State, cash int64) error {
	for _, code := range b.cfg.ProductCodes {
		product, err := productByCode(ctx, c, st, code)
		if err != nil {
			return err
		}
		active, err := myOpenPublication(ctx, c, st, botsdk.PublicationSell, product.ID)
		if err != nil {
			return err
		}
		if active != nil {
			continue // una venta activa por producto
		}
		accs, err := botsdk.CollectAll(ctx, func(ctx context.Context, cursor string) (botsdk.Page[botsdk.LedgerAccount], error) {
			return c.ListAccounts(ctx, botsdk.LedgerAccountsQuery{
				Kind:      botsdk.LedgerStockFree,
				ProductID: product.ID,
				PageQuery: botsdk.PageQuery{Cursor: cursor, Limit: 200},
			})
		})
		if err != nil {
			return fmt.Errorf("bots: listando el stock del trader: %w", err)
		}
		for _, acc := range accs {
			balance, err := acc.Balance.Int64()
			if err != nil || balance < b.cfg.RelistMinLot || acc.WarehouseBuildingID == "" {
				continue
			}
			price, err := b.relistPrice(st, product)
			if err != nil {
				return err
			}
			qty := min(balance, b.cfg.MaxLotQty)
			guarantee := qty * price / 10
			if cash-guarantee < b.cushion() {
				b.decide("skip_relist", "cash_cushion",
					slog.String("product", code),
					slog.Int64("guarantee", guarantee),
					slog.Int64("cash", cash),
					slog.Int64("cushion", b.cushion()))
				continue
			}
			// El stock reposa en un almacén (posiblemente ajeno): se re-lista
			// desde su nodo — la regla verificada del arquetipo.
			nodeID, err := nodeOfBuilding(ctx, c, st, "", acc.WarehouseBuildingID)
			if err != nil {
				b.decide("skip_relist", "warehouse_without_node",
					slog.String("warehouse_building_id", acc.WarehouseBuildingID))
				continue
			}
			qtyStr, err := botsdk.QtyFromInt64(qty)
			if err != nil {
				return err
			}
			minLot, err := botsdk.QtyFromInt64(b.cfg.RelistMinLot)
			if err != nil {
				return err
			}
			pub, err := c.CreatePublication(ctx, botsdk.PublicationCreate{
				Kind:               botsdk.PublicationSell,
				ProductID:          product.ID,
				QuantityTotal:      qtyStr,
				UnitPrice:          botsdk.MoneyFromInt64(price),
				MinLot:             minLot,
				OriginNodeID:       nodeID,
				DeliverySimSeconds: b.cfg.SellDeliverySimSeconds,
			})
			if err != nil {
				if code2, ok := blockedCode(err); ok {
					b.decide("blocked", code2, slog.String("step", "relist"),
						slog.String("product", code))
					continue
				}
				return fmt.Errorf("bots: re-listando %s: %w", code, err)
			}
			b.decide("relist", "stock_on_hand",
				slog.String("publication_id", pub.ID),
				slog.String("product", code),
				slog.Int64("quantity", qty),
				slog.Int64("unit_price", price),
				slog.String("origin_node_id", nodeID),
				slog.String("warehouse_building_id", acc.WarehouseBuildingID))
			break // una venta nueva por producto y pasada
		}
	}
	return nil
}

// relistPrice calcula el precio de re-listado: precio pagado × margen (techo)
// si este proceso recuerda la compra; si no (reinicio), el equivalente
// conservador base_price × BuyMaxPriceBP × margen.
func (b *Trader) relistPrice(st *State, product botsdk.Product) (int64, error) {
	if paid, ok := st.lastBuyPrice[product.ID]; ok && paid > 0 {
		return applyBPCeil(paid, b.cfg.MarginBP), nil
	}
	base, err := product.BasePrice.Int64()
	if err != nil {
		return 0, fmt.Errorf("bots: base_price inválido de %s: %w", product.Code, err)
	}
	return applyBPCeil(applyBP(base, b.cfg.BuyMaxPriceBP), b.cfg.MarginBP), nil
}

// scanBargains recorre las ventas del tablón (más baratas primero) y acepta
// como máximo UNA por pasada si el precio está en o bajo el umbral y el
// presupuesto (caja − colchón) la cubre. Si termina el barrido sin aceptar
// nada emite la decisión terminal wait/no_bargain_on_board con los umbrales
// evaluados: una pasada ociosa DEBE dejar rastro (un bot sin oportunidades no
// puede ser indistinguible de uno colgado en ii_bot_decisions_total).
func (b *Trader) scanBargains(ctx context.Context, c *botsdk.Client, st *State, cash int64) error {
	budget := cash - b.cushion()
	if budget <= 0 {
		b.decide("wait", "cash_at_cushion", slog.Int64("cash", cash), slog.Int64("cushion", b.cushion()))
		return nil
	}
	scanned := 0
	thresholds := make([]any, 0, len(b.cfg.ProductCodes))
	for _, code := range b.cfg.ProductCodes {
		product, err := productByCode(ctx, c, st, code)
		if err != nil {
			return err
		}
		base, err := product.BasePrice.Int64()
		if err != nil {
			return fmt.Errorf("bots: base_price inválido de %s: %w", code, err)
		}
		maxPrice := applyBP(base, b.cfg.BuyMaxPriceBP)
		thresholds = append(thresholds, slog.Int64(code, maxPrice))

		for pub, err := range botsdk.All(ctx, func(ctx context.Context, cursor string) (botsdk.Page[botsdk.Publication], error) {
			return c.Board(ctx, botsdk.BoardQuery{
				Kind:         botsdk.PublicationSell,
				ProductID:    product.ID,
				MaxUnitPrice: botsdk.MoneyFromInt64(maxPrice),
				Sort:         botsdk.SortUnitPriceAsc,
				PageQuery:    botsdk.PageQuery{Cursor: cursor, Limit: 200},
			})
		}) {
			if err != nil {
				return fmt.Errorf("bots: consultando ventas de %s: %w", code, err)
			}
			scanned++
			if pub.PublisherAccountID == st.AccountID {
				continue
			}
			if _, already := st.pendingAcceptances[pub.ID]; already {
				continue
			}
			price, err := pub.UnitPrice.Int64()
			if err != nil || price <= 0 || price > maxPrice {
				continue
			}
			remaining, err := pub.QuantityRemaining.Int64()
			if err != nil || remaining <= 0 {
				continue
			}
			minLot, err := pub.MinLot.Int64()
			if err != nil {
				continue
			}
			qty := acceptQty(remaining, minLot, b.cfg.MaxLotQty, budget/price)
			if qty <= 0 {
				continue
			}
			qtyStr, err := botsdk.QtyFromInt64(qty)
			if err != nil {
				return err
			}
			acc, err := c.Accept(ctx, pub.ID, qtyStr, "")
			if err != nil {
				if code2, ok := blockedCode(err); ok {
					b.decide("blocked", code2, slog.String("step", "accept_sell"),
						slog.String("publication_id", pub.ID))
					return nil
				}
				return fmt.Errorf("bots: aceptando la venta %s: %w", pub.ID, err)
			}
			st.pendingAcceptances[pub.ID] = acc.ID
			st.lastBuyPrice[product.ID] = price
			b.decide("accept_sell", "price_at_or_below_threshold",
				slog.String("publication_id", pub.ID),
				slog.String("acceptance_id", acc.ID),
				slog.String("product", code),
				slog.Int64("quantity", qty),
				slog.Int64("unit_price", price),
				slog.Int64("max_price", maxPrice))
			return nil // una aceptación por pasada: disciplina de presupuesto
		}
	}
	// Barrido completo sin ganga: decisión terminal auditable.
	b.decide("wait", "no_bargain_on_board",
		slog.Int("scanned", scanned),
		slog.Int64("budget", budget),
		slog.Int64("buy_max_price_bp", b.cfg.BuyMaxPriceBP),
		slog.Group("max_price", thresholds...))
	return nil
}
