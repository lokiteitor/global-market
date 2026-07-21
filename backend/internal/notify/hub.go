package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/google/uuid"
)

// ErrTooManyConns lo devuelve Register cuando la cuenta ya tiene
// MaxConnsPerAccount conexiones registradas (II_WS_MAX_CONNS_PER_ACCOUNT).
var ErrTooManyConns = errors.New("notify: la cuenta superó el máximo de conexiones WS simultáneas")

// Conn es una conexión registrada en el hub. El hub NUNCA escribe en el
// socket: encola frames serializados en el buffer acotado de la conexión y el
// bucle de escritura del handler los drena (Outbound). Si el buffer se llena
// —consumidor lento— el hub marca la conexión para cierre 1013 (SlowClose) y
// deja de encolar (ADR-023: el cliente re-sincroniza por REST al reconectar).
type Conn struct {
	accountID uuid.UUID
	send      chan []byte
	slow      chan struct{}

	// rooms y closed están protegidos por el mutex del hub.
	rooms  map[string]struct{}
	closed bool
}

// AccountID devuelve la cuenta autenticada de la conexión.
func (c *Conn) AccountID() uuid.UUID { return c.accountID }

// Outbound es el canal de frames serializados pendientes de escritura.
func (c *Conn) Outbound() <-chan []byte { return c.send }

// SlowClose se cierra cuando el buffer de envío se llenó: el bucle de
// escritura debe cerrar el socket con 1013 y abandonar.
func (c *Conn) SlowClose() <-chan struct{} { return c.slow }

// Hub es el registro concurrente-seguro de conexiones WS por cuenta con
// suscripción por rooms (v1: solo RoomCorp). Todo el estado compartido se
// protege con un único mutex; los envíos a los buffers son no bloqueantes,
// así que ningún cliente lento puede frenar el fan-out del router.
type Hub struct {
	sendBuffer    int
	maxPerAccount int
	metrics       *Metrics
	logger        *slog.Logger

	mu        sync.Mutex
	byAccount map[uuid.UUID]map[*Conn]struct{}
}

// NewHub construye el hub. metrics puede ser nil (sin instrumentación).
func NewHub(opts Options, metrics *Metrics, logger *slog.Logger) *Hub {
	if logger == nil {
		logger = slog.Default()
	}
	return &Hub{
		sendBuffer:    opts.SendBuffer,
		maxPerAccount: opts.MaxConnsPerAccount,
		metrics:       metrics,
		logger:        logger,
		byAccount:     make(map[uuid.UUID]map[*Conn]struct{}),
	}
}

// Register da de alta una conexión de la cuenta. Devuelve ErrTooManyConns si
// la cuenta ya alcanzó el máximo (el handler responde con el frame error
// TOO_MANY_CONNECTIONS y cierra).
func (h *Hub) Register(accountID uuid.UUID) (*Conn, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.byAccount[accountID]) >= h.maxPerAccount {
		return nil, fmt.Errorf("%w (máximo %d)", ErrTooManyConns, h.maxPerAccount)
	}
	c := &Conn{
		accountID: accountID,
		send:      make(chan []byte, h.sendBuffer),
		slow:      make(chan struct{}),
		rooms:     make(map[string]struct{}),
	}
	if h.byAccount[accountID] == nil {
		h.byAccount[accountID] = make(map[*Conn]struct{})
	}
	h.byAccount[accountID][c] = struct{}{}
	h.metrics.connOpened()
	return c, nil
}

// Unregister da de baja la conexión. Idempotente: solo la primera baja
// decrementa el gauge.
func (h *Hub) Unregister(c *Conn) {
	if c == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	conns, ok := h.byAccount[c.accountID]
	if !ok {
		return
	}
	if _, ok := conns[c]; !ok {
		return
	}
	delete(conns, c)
	if len(conns) == 0 {
		delete(h.byAccount, c.accountID)
	}
	if !c.closed {
		c.closed = true
	}
	h.metrics.connClosed()
}

// Join suscribe la conexión a una room.
func (h *Hub) Join(c *Conn, room string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c.rooms[room] = struct{}{}
}

// Leave da de baja la conexión de una room.
func (h *Hub) Leave(c *Conn, room string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(c.rooms, room)
}

// Send encola un frame de control hacia UNA conexión, con la misma semántica
// de buffer acotado que Broadcast (buffer lleno = cierre 1013). Devuelve si
// el frame quedó encolado.
func (h *Hub) Send(c *Conn, frame any) bool {
	b, err := json.Marshal(frame)
	if err != nil {
		h.logger.Error("notify: serializando frame de control", slog.Any("error", err))
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.enqueueLocked(c, b)
}

// Broadcast serializa el frame UNA vez y lo encola hacia todas las conexiones
// de las cuentas dadas suscritas a la room. Devuelve cuántas conexiones lo
// recibieron. Las conexiones con el buffer lleno se marcan para cierre 1013
// (métrica ii_ws_slow_client_closes_total) y no reciben más frames.
func (h *Hub) Broadcast(accountIDs []uuid.UUID, room string, frame any) int {
	b, err := json.Marshal(frame)
	if err != nil {
		h.logger.Error("notify: serializando frame de broadcast", slog.Any("error", err))
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	delivered := 0
	seen := make(map[uuid.UUID]struct{}, len(accountIDs))
	for _, accountID := range accountIDs {
		if _, dup := seen[accountID]; dup {
			continue // una cuenta repetida (p. ej. buyer == owner) no duplica frames
		}
		seen[accountID] = struct{}{}
		for c := range h.byAccount[accountID] {
			if _, sub := c.rooms[room]; !sub {
				continue
			}
			if h.enqueueLocked(c, b) {
				delivered++
			}
		}
	}
	return delivered
}

// enqueueLocked intenta encolar el frame sin bloquear. Requiere h.mu. Si el
// buffer está lleno marca la conexión como cerrada, cierra su canal slow y
// cuenta el cierre por cliente lento.
func (h *Hub) enqueueLocked(c *Conn, b []byte) bool {
	if c == nil || c.closed {
		return false
	}
	select {
	case c.send <- b:
		h.metrics.frameSent()
		return true
	default:
		c.closed = true
		close(c.slow)
		h.metrics.slowClientClose()
		h.logger.Warn("notify: buffer de envío lleno; se cierra la conexión (1013)",
			slog.String("account_id", c.accountID.String()),
			slog.Int("buffer", cap(c.send)))
		return false
	}
}

// Connections devuelve cuántas conexiones tiene registradas la cuenta
// (observabilidad y tests).
func (h *Hub) Connections(accountID uuid.UUID) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.byAccount[accountID])
}
