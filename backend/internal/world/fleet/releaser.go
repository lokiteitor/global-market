package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/outbox"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
	"github.com/lokiteitor/global-market/backend/internal/world/sqlcgen"
)

// ConsumerShipmentReleaser es el nombre lógico del consumidor del outbox que
// libera in situ los cargamentos de un contrato vencido (cursor propio en
// outbox.consumer_cursors).
const ConsumerShipmentReleaser = "shipment_releaser"

// eventContractExpiredUndelivered es el tipo del evento que emite el Contract
// Service al vencer un contrato con cantidad SIN entregar. Se declara aquí como
// string: world NUNCA importa internal/contracts (integración SOLO por el outbox,
// SAD §7 / ADR-006).
const eventContractExpiredUndelivered = "contract.expired_undelivered"

// ShipmentReleaser implementa el consumidor world "shipment_releaser" (GDD
// 7.1/5.3 paso 6c): consume contract.expired_undelivered y, por CADA cargamento
// aún vivo del contrato (in_warehouse/in_transit/at_terminal), lo DETIENE y libera
// su stock físico in situ. En Fase 1 la ruta sólo tiene almacén en sus extremos
// (el junction intermedio no), de modo que el stock se reintegra a
// world.building_inventories del almacén de ORIGEN — el MISMO almacén donde el
// Contract Service liberó en el ledger el stock reservado no entregado
// (settle_contract_prorata con p_seller_stock_release), preservando la coherencia
// físico↔contable (nada se teletransporta). Exactly-once por el cursor del outbox
// y, además, idempotente por el filtro de estado (un cargamento ya
// released_in_situ/delivered no se reprocesa).
type ShipmentReleaser struct {
	baseRepo *Repo
	logger   *slog.Logger

	released prometheus.Counter
}

// NewShipmentReleaser construye el liberador de cargamentos. reg registra sus
// métricas (nil las deja sin instrumentar: tests); logger nil usa slog.Default.
func NewShipmentReleaser(logger *slog.Logger, reg prometheus.Registerer) *ShipmentReleaser {
	if logger == nil {
		logger = slog.Default()
	}
	c := &ShipmentReleaser{
		logger: logger,
		released: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_shipments_released_in_situ_total",
			Help: "Total de cargamentos detenidos y liberados in situ por vencimiento de su contrato.",
		}),
	}
	if reg != nil {
		reg.MustRegister(c.released)
	}
	return c
}

// NewConsumer construye el consumidor lógico del outbox para
// contract.expired_undelivered. Lo arranca el engine con Handle como handler.
func (c *ShipmentReleaser) NewConsumer(pool *pgxpool.Pool, opts ...outbox.ConsumerOption) *outbox.Consumer {
	c.baseRepo = NewRepo(pool)
	return outbox.NewConsumer(pool, ConsumerShipmentReleaser, []string{eventContractExpiredUndelivered}, opts...)
}

// contractExpiredUndeliveredEvent es el payload que consume el shipment_releaser.
// Sólo necesita el contrato (el nodo/almacén de origen y los cargamentos vivos se
// resuelven en la BD).
type contractExpiredUndeliveredEvent struct {
	ContractID string `json:"contract_id"`
}

// Handle procesa un contract.expired_undelivered dentro de la transacción del
// lote del consumidor (los efectos se confirman con el avance del cursor:
// exactly-once).
func (c *ShipmentReleaser) Handle(ctx context.Context, tx pgx.Tx, ev outbox.Event) error {
	if ev.EventType != eventContractExpiredUndelivered {
		return nil
	}
	var e contractExpiredUndeliveredEvent
	if err := json.Unmarshal(ev.Payload, &e); err != nil {
		return fmt.Errorf("world/fleet: leyendo contract.expired_undelivered (seq %d): %w", ev.Seq, err)
	}
	contractID, err := uuid.Parse(e.ContractID)
	if err != nil {
		return fmt.Errorf("world/fleet: contract_id inválido en contract.expired_undelivered (seq %d): %w", ev.Seq, err)
	}

	r := c.baseRepo.WithTx(tx)
	shipments, err := r.ListContractShipmentsToRelease(ctx, contractID)
	if err != nil {
		return err
	}
	simNow := simtime.SimTime(ev.SimTimeAt)
	for _, sh := range shipments {
		if err := r.ReleaseShipmentInSitu(ctx, sh.ID, sh.OriginNodeID, simNow); err != nil {
			return err
		}
		// Reintegra el stock físico al almacén de origen (donde el ledger liberó el
		// reservado no entregado): así físico(building_inventories) y contable
		// (stock_free) casan. El nodo de origen SIEMPRE tiene almacén (es el warehouse
		// del contrato); la guarda es defensiva.
		if sh.OriginBuildingID != nil {
			if err := r.AddInventory(ctx, *sh.OriginBuildingID, sh.ProductID, sh.Quantity, simNow); err != nil {
				return err
			}
		} else {
			c.logger.Warn("world/fleet: nodo de origen sin almacén; liberación in situ sin reintegrar inventario físico",
				slog.String("contract_id", contractID.String()), slog.String("shipment_id", sh.ID.String()),
				slog.String("origin_node_id", sh.OriginNodeID.String()))
		}
		if err := outbox.Emit(ctx, tx, ev.SimTimeAt, AggregateShipment, sh.ID, EventShipmentReleased, ShipmentReleasedPayload{
			ShipmentID: sh.ID.String(), ContractID: contractID.String(), OwnerAccountID: sh.OwnerAccountID.String(),
			ProductID: sh.ProductID.String(), Quantity: fixed(sh.Quantity), NodeID: sh.OriginNodeID.String(),
			ReleasedAtSim: ev.SimTimeAt,
		}); err != nil {
			return err
		}
		c.released.Inc()
		c.logger.Info("cargamento liberado in situ por vencimiento del contrato",
			slog.String("contract_id", contractID.String()), slog.String("shipment_id", sh.ID.String()),
			slog.String("owner", sh.OwnerAccountID.String()), slog.String("product_id", sh.ProductID.String()),
			slog.Int64("quantity", sh.Quantity), slog.String("node_id", sh.OriginNodeID.String()))
	}
	return nil
}

// ─── Repo: liberación in situ ──────────────────────────────────────────────────

// shipmentToRelease es un cargamento vivo de un contrato vencido con el nodo y el
// almacén de origen donde reintegrar su stock físico.
type shipmentToRelease struct {
	ID               uuid.UUID
	OwnerAccountID   uuid.UUID
	ProductID        uuid.UUID
	Quantity         int64
	OriginNodeID     uuid.UUID
	OriginBuildingID *uuid.UUID
}

// ListContractShipmentsToRelease lista los cargamentos aún vivos de un contrato
// con su nodo/almacén de origen (FOR UPDATE dentro de la tx del consumidor).
func (r *Repo) ListContractShipmentsToRelease(ctx context.Context, contractID uuid.UUID) ([]shipmentToRelease, error) {
	rows, err := r.q.ListContractShipmentsToRelease(ctx, &contractID)
	if err != nil {
		return nil, fmt.Errorf("world/fleet: listando cargamentos a liberar del contrato %s: %w", contractID, err)
	}
	out := make([]shipmentToRelease, len(rows))
	for i, row := range rows {
		out[i] = shipmentToRelease{
			ID: row.ID, OwnerAccountID: row.OwnerAccountID, ProductID: row.ProductID,
			Quantity: row.Quantity, OriginNodeID: row.OriginNodeID, OriginBuildingID: row.OriginBuildingID,
		}
	}
	return out, nil
}

// ReleaseShipmentInSitu marca un cargamento released_in_situ en el nodo de origen.
func (r *Repo) ReleaseShipmentInSitu(ctx context.Context, id, atNode uuid.UUID, simNow simtime.SimTime) error {
	node := atNode
	if err := r.q.ReleaseShipmentInSitu(ctx, sqlcgen.ReleaseShipmentInSituParams{
		AtNodeID: &node, SimNow: int64(simNow), ID: id,
	}); err != nil {
		return fmt.Errorf("world/fleet: liberando in situ el cargamento %s: %w", id, err)
	}
	return nil
}
