package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lokiteitor/global-market/backend/internal/outbox"
)

// ConsumerName es el nombre del consumidor lógico del outbox que alimenta el
// fan-out WS (ADR-023). Su cursor AVANZA SIEMPRE tras el fan-out: los sockets
// son efímeros y la entrega hacia clientes es at-least-once desde el
// watermark; un cliente ausente bootstrapea por REST.
const ConsumerName = "notification_gateway"

// RoutedEventTypes es la lista EXPLÍCITA (auditable) de los tipos de evento
// que el router despacha, derivada de la tabla de enrutado del ADR-023. Un
// tipo nuevo debe añadirse aquí y en resolveTargets a la vez.
var RoutedEventTypes = []string{
	// publication.* → corp del publicador
	"publication.created", "publication.cancelled", "publication.expired",
	// acceptance.* → corps del aceptante y del publicador
	"acceptance.registered", "acceptance.resolved",
	// contract.* → corps de comprador y vendedor
	"contract.confirmed", "contract.delivered", "contract.settled",
	"contract.expired_undelivered",
	// shipment.* → corp del dueño (+ comprador en shipment.arrived)
	"shipment.created", "shipment.dispatched", "shipment.arrived", "shipment.released",
	// vehicle.* / building.* / batch.* → corp del dueño
	"vehicle.purchased", "vehicle.updated", "vehicle.arrived", "vehicle.broken", "vehicle.stranded",
	"building.created", "building.updated", "building.upgraded", "building.constructed",
	"batch.queued", "batch.completed", "batch.paused", "batch.cancelled",
	// concession.* → corp del titular (en el traspaso, ambas partes)
	"concession.granted", "concession.renewed", "concession.transferred",
}

// Router es el consumidor outbox notification_gateway: por cada evento
// resuelve las corporaciones destino según la tabla de enrutado del ADR-023
// (payload primero; lookup puntual por SQL con caché TTL si faltan cuentas) y
// lo difunde por el hub como frame event a la room corp.
type Router struct {
	pool    *pgxpool.Pool
	hub     *Hub
	opts    Options
	metrics *Metrics
	logger  *slog.Logger
	cache   *routeCache

	// lastSeq es el último seq despachado a los sockets (watermark). Se
	// actualiza tras cada fan-out, ANTES del commit del lote: aunque el lote
	// se reintente, el frame ya salió hacia los buffers (at-least-once).
	lastSeq atomic.Int64
}

// NewRouter construye el router. metrics puede ser nil (sin instrumentar).
func NewRouter(pool *pgxpool.Pool, hub *Hub, opts Options, metrics *Metrics, logger *slog.Logger) *Router {
	if logger == nil {
		logger = slog.Default()
	}
	return &Router{
		pool:    pool,
		hub:     hub,
		opts:    opts,
		metrics: metrics,
		logger:  logger,
		cache:   newRouteCache(opts.RouteCacheTTL),
	}
}

// Run ejecuta el bucle de consumo hasta que ctx se cancele (nil en el
// apagado limpio; error solo ante configuración inválida). El polling usa
// RouterInterval (II_WS_ROUTER_INTERVAL).
func (r *Router) Run(ctx context.Context) error {
	consumer := outbox.NewConsumer(r.pool, ConsumerName, RoutedEventTypes, outbox.WithLogger(r.logger))
	return consumer.Run(ctx, r.opts.RouterInterval, r.Handle)
}

// sqlMaxSeq es el fallback del watermark cuando el router aún no despachó
// ningún evento.
const sqlMaxSeq = `SELECT COALESCE(max(seq), 0) FROM outbox.events`

// Watermark devuelve el último seq despachado por el router; si aún no
// despachó nada (proceso recién arrancado o sin eventos), el max(seq) actual
// del outbox. Implementa WatermarkSource para el frame joined.
func (r *Router) Watermark(ctx context.Context) (int64, error) {
	if v := r.lastSeq.Load(); v > 0 {
		return v, nil
	}
	var maxSeq int64
	if err := r.pool.QueryRow(ctx, sqlMaxSeq).Scan(&maxSeq); err != nil {
		return 0, fmt.Errorf("notify: consultando max(seq) del outbox: %w", err)
	}
	return maxSeq, nil
}

// Handle procesa UN evento dentro de la transacción del lote del consumidor:
// resuelve las cuentas destino (lecturas puntuales sobre tx si el payload no
// las trae) y difunde el frame event. NUNCA devuelve error por un evento
// inenrutable —el cursor debe avanzar siempre—; solo los fallos de
// infraestructura (SQL) abortan el lote para reintentarlo.
func (r *Router) Handle(ctx context.Context, tx pgx.Tx, ev outbox.Event) error {
	targets, err := r.resolveTargets(ctx, tx, ev)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		// Evento sin destinatario resoluble (fila ya inexistente, payload
		// incompleto): se registra y el cursor avanza igualmente.
		r.logger.Warn("notify: evento sin corps destino; se descarta del fan-out",
			slog.Int64("seq", ev.Seq),
			slog.String("event_type", ev.EventType),
			slog.String("aggregate_id", ev.AggregateID.String()))
	} else {
		delivered := r.hub.Broadcast(targets, RoomCorp, EventFrame{
			Type:          FrameTypeEvent,
			Room:          RoomCorp,
			Seq:           ev.Seq,
			EventID:       ev.EventID.String(),
			EventType:     ev.EventType,
			SimTime:       ev.SimTimeAt,
			AggregateType: ev.AggregateType,
			AggregateID:   ev.AggregateID.String(),
			Payload:       ev.Payload,
		})
		r.logger.Debug("notify: evento despachado",
			slog.Int64("seq", ev.Seq),
			slog.String("event_type", ev.EventType),
			slog.Int("target_accounts", len(targets)),
			slog.Int("delivered_conns", delivered))
	}
	r.storeSeq(ev.Seq)
	r.metrics.eventRouted(ev.EventType)
	return nil
}

// storeSeq avanza el watermark de forma monótona.
func (r *Router) storeSeq(seq int64) {
	for {
		cur := r.lastSeq.Load()
		if seq <= cur || r.lastSeq.CompareAndSwap(cur, seq) {
			return
		}
	}
}

// ─── Tabla de enrutado (ADR-023) ────────────────────────────────────────────

// routingPayload proyecta los campos de cuenta/referencia que los payloads
// del outbox pueden traer. Los que falten se resuelven por SQL.
type routingPayload struct {
	PublisherAccountID string `json:"publisher_account_id"`
	AcceptorAccountID  string `json:"acceptor_account_id"`
	BuyerAccountID     string `json:"buyer_account_id"`
	SellerAccountID    string `json:"seller_account_id"`
	OwnerAccountID     string `json:"owner_account_id"`
	HolderAccountID    string `json:"holder_account_id"`
	FromAccountID      string `json:"from_account_id"`
	ToAccountID        string `json:"to_account_id"`
	PublicationID      string `json:"publication_id"`
	ContractID         string `json:"contract_id"`
	BuildingID         string `json:"building_id"`
}

// resolveTargets aplica la tabla de enrutado del ADR-023: primero las cuentas
// presentes en el payload; los huecos se completan con lookups puntuales por
// SQL (cacheados con TTL corto). Devuelve error SOLO ante fallos de
// infraestructura; un evento irresoluble devuelve lista vacía.
func (r *Router) resolveTargets(ctx context.Context, tx pgx.Tx, ev outbox.Event) ([]uuid.UUID, error) {
	var p routingPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		r.logger.Warn("notify: payload de evento ilegible para el enrutado",
			slog.Int64("seq", ev.Seq), slog.String("event_type", ev.EventType),
			slog.Any("error", err))
		p = routingPayload{}
	}
	family, _, _ := strings.Cut(ev.EventType, ".")

	switch family {
	case "publication":
		if id, ok := parseUUID(p.PublisherAccountID); ok {
			return []uuid.UUID{id}, nil
		}
		return r.lookup(ctx, tx, "publication", ev.AggregateID, sqlPublicationPublisher)

	case "acceptance":
		var targets []uuid.UUID
		if id, ok := parseUUID(p.AcceptorAccountID); ok {
			targets = append(targets, id)
		}
		if pubID, ok := parseUUID(p.PublicationID); ok {
			pub, err := r.lookup(ctx, tx, "publication", pubID, sqlPublicationPublisher)
			if err != nil {
				return nil, err
			}
			targets = append(targets, pub...)
		}
		if len(targets) > 0 {
			return targets, nil
		}
		// Sin cuentas en el payload: aceptante y publicador desde la BD.
		return r.lookup(ctx, tx, "acceptance", ev.AggregateID, sqlAcceptanceParties)

	case "contract":
		buyer, okB := parseUUID(p.BuyerAccountID)
		seller, okS := parseUUID(p.SellerAccountID)
		if okB && okS {
			return []uuid.UUID{buyer, seller}, nil
		}
		return r.lookup(ctx, tx, "contract", ev.AggregateID, sqlContractParties)

	case "shipment":
		var targets []uuid.UUID
		contractID, hasContract := parseUUID(p.ContractID)
		if id, ok := parseUUID(p.OwnerAccountID); ok {
			targets = append(targets, id)
		} else {
			owner, shipContract, err := r.lookupShipment(ctx, tx, ev.AggregateID)
			if err != nil {
				return nil, err
			}
			targets = append(targets, owner...)
			if !hasContract && shipContract != uuid.Nil {
				contractID, hasContract = shipContract, true
			}
		}
		// shipment.arrived interesa también al comprador del contrato (ADR-023).
		if ev.EventType == "shipment.arrived" && hasContract {
			buyer, err := r.lookup(ctx, tx, "contract_buyer", contractID, sqlContractBuyer)
			if err != nil {
				return nil, err
			}
			targets = append(targets, buyer...)
		}
		return targets, nil

	case "vehicle":
		if id, ok := parseUUID(p.OwnerAccountID); ok {
			return []uuid.UUID{id}, nil
		}
		return r.lookup(ctx, tx, "vehicle", ev.AggregateID, sqlVehicleOwner)

	case "building":
		if id, ok := parseUUID(p.OwnerAccountID); ok {
			return []uuid.UUID{id}, nil
		}
		return r.lookup(ctx, tx, "building", ev.AggregateID, sqlBuildingOwner)

	case "batch":
		if buildingID, ok := parseUUID(p.BuildingID); ok {
			return r.lookup(ctx, tx, "building", buildingID, sqlBuildingOwner)
		}
		return r.lookup(ctx, tx, "batch", ev.AggregateID, sqlBatchOwner)

	case "concession":
		var targets []uuid.UUID
		// concession.transferred notifica a ambas partes del traspaso: el
		// titular saliente y el entrante.
		if id, ok := parseUUID(p.FromAccountID); ok {
			targets = append(targets, id)
		}
		if id, ok := parseUUID(p.ToAccountID); ok {
			targets = append(targets, id)
		}
		if id, ok := parseUUID(p.HolderAccountID); ok {
			targets = append(targets, id)
		}
		if len(targets) > 0 {
			return targets, nil
		}
		return r.lookup(ctx, tx, "concession", ev.AggregateID, sqlConcessionHolder)
	}

	r.logger.Warn("notify: familia de evento sin regla de enrutado",
		slog.String("event_type", ev.EventType))
	return nil, nil
}

// SQL de los lookups puntuales de enrutado. Devuelven una o dos cuentas.
const (
	sqlPublicationPublisher = `SELECT publisher_account_id FROM ledger.publications WHERE id = $1`
	sqlAcceptanceParties    = `
SELECT a.acceptor_account_id, p.publisher_account_id
FROM ledger.publication_acceptances a
JOIN ledger.publications p ON p.id = a.publication_id
WHERE a.id = $1`
	sqlContractParties = `SELECT buyer_account_id, seller_account_id FROM ledger.contracts WHERE id = $1`
	sqlContractBuyer   = `SELECT buyer_account_id FROM ledger.contracts WHERE id = $1`
	sqlShipmentOwner   = `SELECT owner_account_id, contract_id FROM world.shipments WHERE id = $1`
	sqlVehicleOwner    = `SELECT owner_account_id FROM world.vehicles WHERE id = $1`
	sqlBuildingOwner   = `SELECT owner_account_id FROM world.buildings WHERE id = $1`
	sqlBatchOwner      = `
SELECT b.owner_account_id
FROM world.production_batches pb
JOIN world.buildings b ON b.id = pb.building_id
WHERE pb.id = $1`
	sqlConcessionHolder = `SELECT holder_account_id FROM world.land_concessions WHERE id = $1`
)

// lookup resuelve las cuentas de una query de enrutado con caché TTL. Una
// fila inexistente devuelve lista vacía (evento irresoluble, no un error);
// cualquier otro fallo SQL sí es error (el lote se reintenta).
func (r *Router) lookup(ctx context.Context, tx pgx.Tx, kind string, id uuid.UUID, sql string) ([]uuid.UUID, error) {
	key := kind + ":" + id.String()
	if ids, ok := r.cache.get(key); ok {
		return ids, nil
	}
	rows, err := tx.Query(ctx, sql, id)
	if err != nil {
		return nil, fmt.Errorf("notify: lookup de enrutado %s(%s): %w", kind, id, err)
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return nil, fmt.Errorf("notify: leyendo lookup %s(%s): %w", kind, id, err)
		}
		for _, v := range values {
			switch acc := v.(type) {
			case [16]byte:
				ids = append(ids, uuid.UUID(acc))
			case nil:
				// columna opcional (p. ej. contract_id NULL): se ignora
			default:
				return nil, fmt.Errorf("notify: lookup %s(%s): columna inesperada %T", kind, id, v)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("notify: iterando lookup %s(%s): %w", kind, id, err)
	}
	r.cache.put(key, ids)
	return ids, nil
}

// lookupShipment resuelve dueño y contrato de un cargamento (el contrato no
// se cachea junto al dueño para no mezclar claves; la fila se lee una vez).
func (r *Router) lookupShipment(ctx context.Context, tx pgx.Tx, shipmentID uuid.UUID) ([]uuid.UUID, uuid.UUID, error) {
	var owner uuid.UUID
	var contractID *uuid.UUID
	err := tx.QueryRow(ctx, sqlShipmentOwner, shipmentID).Scan(&owner, &contractID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, uuid.Nil, nil
	}
	if err != nil {
		return nil, uuid.Nil, fmt.Errorf("notify: lookup de enrutado shipment(%s): %w", shipmentID, err)
	}
	out := uuid.Nil
	if contractID != nil {
		out = *contractID
	}
	return []uuid.UUID{owner}, out, nil
}

// parseUUID interpreta un uuid opcional del payload ("" o inválido = ausente).
func parseUUID(s string) (uuid.UUID, bool) {
	if s == "" {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(s)
	if err != nil || id == uuid.Nil {
		return uuid.Nil, false
	}
	return id, true
}

// ─── Caché TTL de lookups ───────────────────────────────────────────────────

// routeCache es una caché TTL mínima para los lookups de enrutado. TTL corto
// a propósito: la titularidad puede cambiar (traspasos de concesión). ttl 0
// la desactiva.
type routeCache struct {
	ttl time.Duration
	now func() time.Time // inyectable en tests

	mu      sync.Mutex
	entries map[string]routeCacheEntry
}

type routeCacheEntry struct {
	ids     []uuid.UUID
	expires time.Time
}

func newRouteCache(ttl time.Duration) *routeCache {
	return &routeCache{ttl: ttl, now: time.Now, entries: make(map[string]routeCacheEntry)}
}

func (c *routeCache) get(key string) ([]uuid.UUID, bool) {
	if c.ttl <= 0 {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || c.now().After(e.expires) {
		delete(c.entries, key)
		return nil, false
	}
	return e.ids, true
}

func (c *routeCache) put(key string, ids []uuid.UUID) {
	if c.ttl <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Poda perezosa: al insertar se retiran las entradas vencidas para que la
	// caché no crezca sin límite entre lotes.
	now := c.now()
	for k, e := range c.entries {
		if now.After(e.expires) {
			delete(c.entries, k)
		}
	}
	c.entries[key] = routeCacheEntry{ids: ids, expires: now.Add(c.ttl)}
}
