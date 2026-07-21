package bots

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/lokiteitor/global-market/backend/pkg/botsdk"
)

// IronProducerConfig son los umbrales auditables del arquetipo iron_producer.
type IronProducerConfig struct {
	ProducerConfig
	// FuelProductCode es el combustible de la receta ("coal").
	FuelProductCode string `json:"fuel_product_code"`
	// FuelLowWater dispara la compra de combustible: si el stock libre de
	// coal en la mina cae por debajo, se publica una solicitud de compra.
	FuelLowWater int64 `json:"fuel_low_water"`
	// FuelBuyQty es la cantidad de cada solicitud de compra de coal.
	FuelBuyQty int64 `json:"fuel_buy_qty"`
	// FuelBuyPriceBP fija el precio ofrecido como fracción del base_price en
	// basis points (11000 ⇒ paga el 110% para atraer vendedores).
	FuelBuyPriceBP int64 `json:"fuel_buy_price_bp"`
	// FuelBuyDeliverySimSeconds es el plazo de entrega de la solicitud
	// (generoso: el vendedor debe mover camiones reales).
	FuelBuyDeliverySimSeconds int64 `json:"fuel_buy_delivery_sim_seconds"`
}

// DefaultIronProducerConfig son los umbrales por defecto del iron_producer.
func DefaultIronProducerConfig() IronProducerConfig {
	return IronProducerConfig{
		ProducerConfig:            defaultProducerConfig("iron_ore", "iron_mine", "mine_iron"),
		FuelProductCode:           "coal",
		FuelLowWater:              50,
		FuelBuyQty:                200,
		FuelBuyPriceBP:            11_000,
		FuelBuyDeliverySimSeconds: 2 * simDaySeconds,
	}
}

// IronProducer es el arquetipo productor de hierro (ADR-024, Fase 0): mismo
// patrón de setup que el carbonero (iron_mine sobre el yacimiento de hierro +
// receta mine_iron, que consume coal como combustible), COMBUSTIBLE por
// mercado — si su stock de coal en la mina baja de FuelLowWater publica UNA
// solicitud de compra (buy) con destino su nodo, manteniendo solo una activa
// — y COMERCIO del núcleo (producerCore.trade): UNA venta activa de iron_ore,
// ATENCIÓN de las solicitudes de compra de iron_ore del tablón por encima de
// su umbral —lo que le convierte en el proveedor natural del transformador
// industrial, cuyo único canal de abastecimiento son sus buy— y DESPACHO con
// camión propio de los cargamentos que genera.
type IronProducer struct {
	producerCore
	cfg IronProducerConfig
}

// NewIronProducer construye el arquetipo con sus umbrales.
func NewIronProducer(cfg IronProducerConfig, botName string, logger *slog.Logger, metrics *Metrics) *IronProducer {
	return &IronProducer{
		producerCore: producerCore{base: newBase(botName, "iron_producer", logger, metrics), cfg: cfg.ProducerConfig},
		cfg:          cfg,
	}
}

// Name implementa Behavior.
func (b *IronProducer) Name() string { return "iron_producer" }

// ConfigJSON serializa los umbrales para auth.bot_profiles.behavior.
func (b *IronProducer) ConfigJSON() ([]byte, error) { return json.Marshal(b.cfg) }

// Decide implementa Behavior: una pasada idempotente del ciclo completo. La
// envuelve base.pass: una pasada sin acción (combustible en camino, venta ya
// activa, nada que despachar) emite igualmente su decisión `wait` con motivo.
func (b *IronProducer) Decide(ctx context.Context, c *botsdk.Client, st *State) error {
	return b.pass(func() error {
		ready, err := b.ensureSetup(ctx, c, st)
		if err != nil || !ready {
			return err
		}
		if _, err := cashBalance(ctx, c, st); err != nil {
			return err
		}
		if err := b.maintainFuel(ctx, c, st); err != nil {
			return err
		}
		return b.trade(ctx, c, st)
	})
}

// maintainFuel publica una solicitud de compra de coal (destino: su mina)
// cuando el stock libre de combustible cae por debajo del umbral, manteniendo
// SOLO UNA activa a la vez.
func (b *IronProducer) maintainFuel(ctx context.Context, c *botsdk.Client, st *State) error {
	fuel, err := productByCode(ctx, c, st, b.cfg.FuelProductCode)
	if err != nil {
		return err
	}
	stock, err := stockFreeAt(ctx, c, fuel.ID, st.mineID)
	if err != nil {
		return err
	}
	if stock >= b.cfg.FuelLowWater {
		b.idle("fuel_stocked",
			slog.Int64("fuel_stock", stock), slog.Int64("low_water", b.cfg.FuelLowWater))
		return nil
	}
	active, err := myOpenPublication(ctx, c, st, botsdk.PublicationBuy, fuel.ID)
	if err != nil {
		return err
	}
	if active != nil {
		// Ya hay una solicitud activa: regla de UNA publicación. La mina está
		// parada esperando ese combustible, no colgada.
		b.idle("awaiting_fuel",
			slog.String("publication_id", active.ID),
			slog.Int64("fuel_stock", stock), slog.Int64("low_water", b.cfg.FuelLowWater))
		return nil
	}
	basePrice, err := fuel.BasePrice.Int64()
	if err != nil {
		return fmt.Errorf("bots: base_price inválido de %s: %w", b.cfg.FuelProductCode, err)
	}
	price := applyBP(basePrice, b.cfg.FuelBuyPriceBP)
	qtyStr, err := botsdk.QtyFromInt64(b.cfg.FuelBuyQty)
	if err != nil {
		return err
	}
	pub, err := c.CreatePublication(ctx, botsdk.PublicationCreate{
		Kind:               botsdk.PublicationBuy,
		ProductID:          fuel.ID,
		QuantityTotal:      qtyStr,
		UnitPrice:          botsdk.MoneyFromInt64(price),
		DestinationNodeID:  st.mineNodeID,
		DeliverySimSeconds: b.cfg.FuelBuyDeliverySimSeconds,
	})
	if err != nil {
		if code, ok := blockedCode(err); ok {
			b.decide("blocked", code, slog.String("step", "publish_fuel_buy"))
			return nil
		}
		return fmt.Errorf("bots: publicando la compra de combustible: %w", err)
	}
	b.decide("publish_fuel_buy", "fuel_below_low_water",
		slog.String("publication_id", pub.ID),
		slog.Int64("fuel_stock", stock),
		slog.Int64("low_water", b.cfg.FuelLowWater),
		slog.Int64("quantity", b.cfg.FuelBuyQty),
		slog.Int64("unit_price", price))
	return nil
}
