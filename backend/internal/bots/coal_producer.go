package bots

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/lokiteitor/global-market/backend/pkg/botsdk"
)

// CoalProducerConfig son los umbrales auditables del arquetipo coal_producer.
// El carbonero NO añade umbrales sobre el núcleo del productor primario:
// atender solicitudes de compra y despachar lo aceptado son del núcleo
// (producerCore.trade), no de este arquetipo.
type CoalProducerConfig = ProducerConfig

// DefaultCoalProducerConfig son los umbrales por defecto del coal_producer.
func DefaultCoalProducerConfig() CoalProducerConfig {
	return defaultProducerConfig("coal", "coal_mine", "mine_coal")
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
//     entregado en su nodo si no tiene; si su único camión quedó varado en el
//     nodo de la ENTREGA anterior lo trae de vuelta en vacío), computa el plan
//     de ruta origen→destino, crea la ruta y DESPACHA. Solo espera si el camión
//     está ocupado.
type CoalProducer struct {
	producerCore
}

// NewCoalProducer construye el arquetipo con sus umbrales.
func NewCoalProducer(cfg CoalProducerConfig, botName string, logger *slog.Logger, metrics *Metrics) *CoalProducer {
	return &CoalProducer{
		producerCore: producerCore{base: newBase(botName, "coal_producer", logger, metrics), cfg: cfg},
	}
}

// Name implementa Behavior.
func (b *CoalProducer) Name() string { return "coal_producer" }

// ConfigJSON serializa los umbrales para auth.bot_profiles.behavior.
func (b *CoalProducer) ConfigJSON() ([]byte, error) { return json.Marshal(b.cfg) }

// Decide implementa Behavior: una pasada idempotente del ciclo completo. La
// envuelve base.pass: una pasada sin acción (venta ya activa, sin compras que
// atender, nada que despachar) emite igualmente su decisión `wait` con motivo.
func (b *CoalProducer) Decide(ctx context.Context, c *botsdk.Client, st *State) error {
	return b.pass(func() error {
		ready, err := b.ensureSetup(ctx, c, st)
		if err != nil || !ready {
			return err
		}
		if _, err := cashBalance(ctx, c, st); err != nil {
			return err
		}
		return b.trade(ctx, c, st)
	})
}
