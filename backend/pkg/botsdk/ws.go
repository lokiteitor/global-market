package botsdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// Valores por defecto de WSOptions.
const (
	// DefaultWSEventBuffer es la capacidad del canal de eventos entregados al
	// bot. Si el bot no consume y el buffer se llena, el lector se bloquea y el
	// servidor terminará cerrando la conexión con 1013 (consumidor lento,
	// ADR-023); el cliente reconecta y el bot re-sincroniza por REST.
	DefaultWSEventBuffer = 256
	// DefaultWSReconnectBackoffBase es la espera del primer reintento de
	// reconexión; cada reintento la duplica (con jitter ±20%).
	DefaultWSReconnectBackoffBase = time.Second
	// DefaultWSReconnectBackoffMax acota la espera entre reconexiones.
	DefaultWSReconnectBackoffMax = 30 * time.Second
	// DefaultWSHandshakeTimeout acota el upgrade + auth en banda de cada
	// intento de conexión (el servidor exige el frame auth en ≤5 s, ADR-023).
	DefaultWSHandshakeTimeout = 10 * time.Second
)

// RoomCorp es la única room del protocolo v1 (ADR-023): los eventos de la
// propia corporación.
const RoomCorp = "corp"

// Tipos de frame del protocolo v1 (ADR-023).
const (
	wsFrameAuth   = "auth"
	wsFrameJoin   = "join"
	wsFramePing   = "ping"
	wsFrameAuthOK = "auth_ok"
	wsFrameJoined = "joined"
	wsFrameEvent  = "event"
	wsFramePong   = "pong"
	wsFrameError  = "error"
)

// Event es un evento de dominio entregado por el Notification/Event Gateway
// (frame event del ADR-023). Payload viaja TAL CUAL lo emitió el outbox
// (dinero/stock como strings del contrato, jamás floats). Seq es el orden
// total del outbox: el cliente detecta huecos por seq creciente y
// re-sincroniza por REST.
type Event struct {
	Seq           int64
	EventID       string
	EventType     string
	SimTime       SimTime
	AggregateType string
	AggregateID   string
	Payload       json.RawMessage
}

// WSOptions configura la conexión WebSocket del SDK. El cero funciona: todo
// tiene default documentado.
type WSOptions struct {
	// URL es el endpoint WS explícito (ws:// o http://, coder/websocket acepta
	// ambos). Vacío = derivada de Options.BaseURL + "/ws" (GET /api/v1/ws).
	URL string
	// EventBuffer es la capacidad del canal de eventos (> 0). Default
	// DefaultWSEventBuffer.
	EventBuffer int
	// ReconnectBackoffBase es la espera del primer reintento de reconexión.
	// Default DefaultWSReconnectBackoffBase.
	ReconnectBackoffBase time.Duration
	// ReconnectBackoffMax acota la espera entre reconexiones. Default
	// DefaultWSReconnectBackoffMax.
	ReconnectBackoffMax time.Duration
	// HandshakeTimeout acota el upgrade + auth en banda de cada intento.
	// Default DefaultWSHandshakeTimeout.
	HandshakeTimeout time.Duration
}

// WSError es un frame error del protocolo recibido del servidor.
type WSError struct {
	// Code es el código estable del protocolo (BAD_FRAME, UNAUTHORIZED,
	// UNSUPPORTED_ROOM, TOO_MANY_CONNECTIONS, INTERNAL).
	Code string
	// Message es la descripción legible.
	Message string
}

// Error implementa error.
func (e *WSError) Error() string {
	return fmt.Sprintf("botsdk: ws error %s: %s", e.Code, e.Message)
}

// wsClientFrame es un frame cliente→servidor del protocolo v1.
type wsClientFrame struct {
	Type  string `json:"type"`
	Token string `json:"token,omitempty"`
	Room  string `json:"room,omitempty"`
	Nonce string `json:"nonce,omitempty"`
}

// wsServerFrame es cualquier frame servidor→cliente del protocolo v1 (un solo
// struct: los campos de cada tipo no colisionan).
type wsServerFrame struct {
	Type string `json:"type"`
	// auth_ok
	AccountID      string `json:"account_id,omitempty"`
	SimTimeSeconds int64  `json:"sim_time_seconds,omitempty"`
	// joined / event
	Room      string `json:"room,omitempty"`
	Watermark int64  `json:"watermark,omitempty"`
	// event
	Seq           int64           `json:"seq,omitempty"`
	EventID       string          `json:"event_id,omitempty"`
	EventType     string          `json:"event_type,omitempty"`
	SimTime       int64           `json:"sim_time,omitempty"`
	AggregateType string          `json:"aggregate_type,omitempty"`
	AggregateID   string          `json:"aggregate_id,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"`
	// pong
	Nonce string `json:"nonce,omitempty"`
	// error
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// joinResult es la respuesta interna de un JoinCorp en vuelo.
type joinResult struct {
	watermark int64
}

// WSConn es la sesión WebSocket gestionada del SDK: auth en banda con el
// token del Client, entrega de eventos tipados, ping/pong de aplicación y
// RECONEXIÓN AUTOMÁTICA con backoff exponencial + jitter. Tras cada
// reconexión re-hace el join (si JoinCorp se llamó) y publica el nuevo
// watermark en Reconnected(): el bot re-sincroniza su estado por REST y sigue
// (no hay replay histórico por el socket, ADR-023).
//
// El token se relee del Client en cada intento de conexión: si el bot renueva
// la sesión con Login, la siguiente reconexión usa el token nuevo.
type WSConn struct {
	client           *Client
	url              string
	eventBuffer      int
	backoffBase      time.Duration
	backoffMax       time.Duration
	handshakeTimeout time.Duration

	events      chan Event
	reconnected chan int64

	cancel context.CancelFunc
	done   chan struct{}

	mu        sync.Mutex
	conn      *websocket.Conn
	wantJoin  bool
	joinWait  chan joinResult // no nil mientras un JoinCorp espera respuesta
	pongWait  map[string]chan struct{}
	pingSeq   uint64
	closed    bool
	watermark atomic.Int64
	accountID atomic.Value // string
	simTime   atomic.Int64
}

// Connect abre la sesión WebSocket del Notification/Event Gateway (ADR-023):
// upgrade de GET {BaseURL}/ws y auth en banda con el token del Client. Debe
// llamarse tras Login. El primer intento es síncrono: si falla (red, token
// inválido → cierre 4401) devuelve el error y no queda nada corriendo. Tras
// conectar, la sesión se auto-gestiona (reconexión con backoff + jitter)
// hasta Close.
func (c *Client) Connect(ctx context.Context, opts WSOptions) (*WSConn, error) {
	if c.Token() == "" {
		return nil, errors.New("botsdk: Connect requiere una sesión (llama a Login primero)")
	}
	wsURL := opts.URL
	if wsURL == "" {
		derived, err := deriveWSURL(c.baseURL)
		if err != nil {
			return nil, err
		}
		wsURL = derived
	}
	w := &WSConn{
		client:           c,
		url:              wsURL,
		eventBuffer:      opts.EventBuffer,
		backoffBase:      opts.ReconnectBackoffBase,
		backoffMax:       opts.ReconnectBackoffMax,
		handshakeTimeout: opts.HandshakeTimeout,
		reconnected:      make(chan int64, 1),
		done:             make(chan struct{}),
		pongWait:         make(map[string]chan struct{}),
	}
	if w.eventBuffer <= 0 {
		w.eventBuffer = DefaultWSEventBuffer
	}
	if w.backoffBase <= 0 {
		w.backoffBase = DefaultWSReconnectBackoffBase
	}
	if w.backoffMax <= 0 {
		w.backoffMax = DefaultWSReconnectBackoffMax
	}
	if w.handshakeTimeout <= 0 {
		w.handshakeTimeout = DefaultWSHandshakeTimeout
	}
	w.events = make(chan Event, w.eventBuffer)

	conn, err := w.dialAndAuth(ctx)
	if err != nil {
		return nil, err
	}
	w.mu.Lock()
	w.conn = conn
	w.mu.Unlock()

	runCtx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	go w.run(runCtx, conn)
	return w, nil
}

// deriveWSURL construye la URL del endpoint WS desde la BaseURL de la API
// (http(s) → ws(s), path + "/ws"). coder/websocket también acepta http(s)
// directamente; se normaliza a ws(s) por claridad.
func deriveWSURL(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("botsdk: derivando la URL del WS de %q: %w", base, err)
	}
	switch u.Scheme {
	case "http", "ws":
		u.Scheme = "ws"
	case "https", "wss":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("botsdk: esquema no soportado %q en la BaseURL", u.Scheme)
	}
	u.Path = u.Path + "/ws"
	return u.String(), nil
}

// Events devuelve el canal de eventos de dominio. Se cierra al terminar la
// sesión (Close). Los eventos de una misma conexión llegan en orden de seq;
// tras una reconexión pueden faltar seq intermedios: el bot lo detecta por el
// hueco (o por Reconnected) y re-sincroniza por REST.
func (w *WSConn) Events() <-chan Event { return w.events }

// Reconnected notifica cada re-join automático tras una reconexión con el
// watermark nuevo (semántica último-gana: si el bot no lo lee a tiempo solo
// conserva el más reciente). Al recibirlo, el bot debe re-sincronizar su
// estado por REST: todo evento posterior llegará con seq > watermark.
func (w *WSConn) Reconnected() <-chan int64 { return w.reconnected }

// Watermark devuelve el último watermark recibido en un frame joined (0 si
// aún no hubo join).
func (w *WSConn) Watermark() int64 { return w.watermark.Load() }

// AccountID devuelve el account_id confirmado por auth_ok ("" antes del
// primer auth).
func (w *WSConn) AccountID() string {
	if v, ok := w.accountID.Load().(string); ok {
		return v
	}
	return ""
}

// SimTimeSeconds devuelve el sim-time del último auth_ok.
func (w *WSConn) SimTimeSeconds() SimTime { return w.simTime.Load() }

// JoinCorp se suscribe a la room corp y devuelve su watermark (último seq del
// outbox ya despachado): el bot hace bootstrap por REST y sabe que todo
// evento posterior llegará por el socket con seq > watermark. La suscripción
// queda registrada: tras cada reconexión el cliente re-hace el join
// automáticamente y publica el watermark nuevo en Reconnected().
func (w *WSConn) JoinCorp(ctx context.Context) (int64, error) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return 0, errors.New("botsdk: la sesión WS está cerrada")
	}
	if w.joinWait != nil {
		w.mu.Unlock()
		return 0, errors.New("botsdk: ya hay un JoinCorp en curso")
	}
	wait := make(chan joinResult, 1)
	w.joinWait = wait
	w.wantJoin = true
	conn := w.conn
	w.mu.Unlock()

	if conn != nil {
		if err := w.writeFrame(ctx, conn, wsClientFrame{Type: wsFrameJoin, Room: RoomCorp}); err != nil {
			// La conexión pudo caer entre medias: la reconexión re-enviará el
			// join (wantJoin ya está fijado) y satisfará a este waiter.
			w.client.log.DebugContext(ctx, "botsdk: join no enviado; la reconexión lo reintentará", "error", err)
		}
	}
	select {
	case <-ctx.Done():
		w.mu.Lock()
		if w.joinWait == wait {
			w.joinWait = nil
		}
		w.mu.Unlock()
		return 0, ctx.Err()
	case res := <-wait:
		return res.watermark, nil
	case <-w.done:
		return 0, errors.New("botsdk: la sesión WS terminó durante el join")
	}
}

// Ping envía un ping de aplicación del protocolo ({"type":"ping","nonce":..})
// y espera su pong. Complementa al ping WS de protocolo del servidor, que
// coder/websocket responde automáticamente mientras hay una lectura activa.
func (w *WSConn) Ping(ctx context.Context) error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return errors.New("botsdk: la sesión WS está cerrada")
	}
	w.pingSeq++
	nonce := "sdk-" + strconv.FormatUint(w.pingSeq, 10)
	wait := make(chan struct{})
	w.pongWait[nonce] = wait
	conn := w.conn
	w.mu.Unlock()

	defer func() {
		w.mu.Lock()
		delete(w.pongWait, nonce)
		w.mu.Unlock()
	}()
	if conn == nil {
		return errors.New("botsdk: sin conexión WS activa")
	}
	if err := w.writeFrame(ctx, conn, wsClientFrame{Type: wsFramePing, Nonce: nonce}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-wait:
		return nil
	case <-w.done:
		return errors.New("botsdk: la sesión WS terminó durante el ping")
	}
}

// Close termina la sesión: detiene la reconexión, cierra la conexión activa y
// cierra el canal de eventos. Idempotente.
func (w *WSConn) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		<-w.done
		return nil
	}
	w.closed = true
	conn := w.conn
	w.mu.Unlock()

	w.cancel()
	if conn != nil {
		_ = conn.Close(websocket.StatusNormalClosure, "cierre del cliente")
	}
	<-w.done
	return nil
}

// run es el bucle de sesión: lee la conexión activa y, cuando cae, reconecta
// con backoff + jitter, re-autentica y re-hace el join. Termina con Close (o
// cancelación interna) cerrando el canal de eventos.
func (w *WSConn) run(ctx context.Context, conn *websocket.Conn) {
	defer close(w.done)
	defer close(w.events)

	for {
		w.readLoop(ctx, conn)
		_ = conn.CloseNow()
		if ctx.Err() != nil || w.isClosed() {
			return
		}

		// Reconexión: backoff exponencial con jitter ±20%; el token se relee
		// del Client en cada intento (dialAndAuth).
		attempt := 0
		for {
			wait := jitterBackoff(w.backoffBase, w.backoffMax, attempt)
			w.client.log.Debug("botsdk: reconexión WS", "attempt", attempt+1, "wait", wait)
			if err := sleepCtx(ctx, wait); err != nil {
				return
			}
			next, err := w.dialAndAuth(ctx)
			if err != nil {
				if ctx.Err() != nil || w.isClosed() {
					return
				}
				w.client.log.Debug("botsdk: reconexión WS fallida", "attempt", attempt+1, "error", err)
				attempt++
				continue
			}
			conn = next
			break
		}

		w.mu.Lock()
		w.conn = conn
		rejoin := w.wantJoin
		w.mu.Unlock()
		if rejoin {
			if err := w.writeFrame(ctx, conn, wsClientFrame{Type: wsFrameJoin, Room: RoomCorp}); err != nil {
				w.client.log.Debug("botsdk: re-join no enviado; se reintenta en la siguiente reconexión", "error", err)
			}
		}
	}
}

// dialAndAuth abre una conexión y ejecuta la auth en banda (primer frame auth
// con el token actual del Client; respuesta auth_ok o error/4401).
func (w *WSConn) dialAndAuth(ctx context.Context) (*websocket.Conn, error) {
	hctx, cancel := context.WithTimeout(ctx, w.handshakeTimeout)
	defer cancel()

	conn, _, err := websocket.Dial(hctx, w.url, &websocket.DialOptions{HTTPClient: w.client.httpc})
	if err != nil {
		return nil, fmt.Errorf("botsdk: conectando al WS %s: %w", w.url, err)
	}
	conn.SetReadLimit(1 << 20)

	if err := w.writeFrame(hctx, conn, wsClientFrame{Type: wsFrameAuth, Token: w.client.Token()}); err != nil {
		_ = conn.CloseNow()
		return nil, err
	}
	frame, err := readFrame(hctx, conn)
	if err != nil {
		_ = conn.CloseNow()
		return nil, fmt.Errorf("botsdk: esperando auth_ok: %w", err)
	}
	switch frame.Type {
	case wsFrameAuthOK:
		w.accountID.Store(frame.AccountID)
		w.simTime.Store(frame.SimTimeSeconds)
		return conn, nil
	case wsFrameError:
		_ = conn.CloseNow()
		return nil, &WSError{Code: frame.Code, Message: frame.Message}
	default:
		_ = conn.CloseNow()
		return nil, fmt.Errorf("botsdk: frame inesperado %q en la auth WS", frame.Type)
	}
}

// readLoop procesa los frames del servidor hasta que la conexión cae.
func (w *WSConn) readLoop(ctx context.Context, conn *websocket.Conn) {
	for {
		frame, err := readFrame(ctx, conn)
		if err != nil {
			if status := websocket.CloseStatus(err); status != -1 {
				w.client.log.Debug("botsdk: conexión WS cerrada por el servidor", "status", int(status))
			}
			return
		}
		switch frame.Type {
		case wsFrameEvent:
			ev := Event{
				Seq:           frame.Seq,
				EventID:       frame.EventID,
				EventType:     frame.EventType,
				SimTime:       frame.SimTime,
				AggregateType: frame.AggregateType,
				AggregateID:   frame.AggregateID,
				Payload:       frame.Payload,
			}
			select {
			case w.events <- ev:
			case <-ctx.Done():
				return
			}
		case wsFrameJoined:
			w.watermark.Store(frame.Watermark)
			w.mu.Lock()
			wait := w.joinWait
			w.joinWait = nil
			w.mu.Unlock()
			if wait != nil {
				wait <- joinResult{watermark: frame.Watermark}
			} else {
				w.notifyReconnected(frame.Watermark)
			}
		case wsFramePong:
			w.mu.Lock()
			wait, ok := w.pongWait[frame.Nonce]
			if ok {
				delete(w.pongWait, frame.Nonce)
			}
			w.mu.Unlock()
			if ok {
				close(wait)
			}
		case wsFrameError:
			w.client.log.Warn("botsdk: frame error del gateway WS",
				"code", frame.Code, "message", frame.Message)
		default:
			w.client.log.Debug("botsdk: frame WS desconocido ignorado", "type", frame.Type)
		}
	}
}

// notifyReconnected publica el watermark de un re-join automático con
// semántica último-gana (nunca bloquea el lector del socket).
func (w *WSConn) notifyReconnected(watermark int64) {
	for {
		select {
		case w.reconnected <- watermark:
			return
		default:
		}
		select {
		case <-w.reconnected: // descarta el anterior no consumido
		default:
		}
	}
}

// isClosed informa de si Close ya fue invocado.
func (w *WSConn) isClosed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.closed
}

// writeFrame serializa y escribe un frame de texto.
func (w *WSConn) writeFrame(ctx context.Context, conn *websocket.Conn, frame wsClientFrame) error {
	b, err := json.Marshal(frame)
	if err != nil {
		return fmt.Errorf("botsdk: serializando el frame %s: %w", frame.Type, err)
	}
	if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
		return fmt.Errorf("botsdk: escribiendo el frame %s: %w", frame.Type, err)
	}
	return nil
}

// readFrame lee y decodifica el siguiente frame del servidor.
func readFrame(ctx context.Context, conn *websocket.Conn) (wsServerFrame, error) {
	_, data, err := conn.Read(ctx)
	if err != nil {
		return wsServerFrame{}, err
	}
	var frame wsServerFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		return wsServerFrame{}, fmt.Errorf("botsdk: frame del servidor no es JSON válido: %w", err)
	}
	return frame, nil
}

// jitterBackoff calcula la espera del intento attempt (0-based): base×2^n
// acotada por max, con jitter uniforme ±20%.
func jitterBackoff(base, maxWait time.Duration, attempt int) time.Duration {
	d := base
	for i := 0; i < attempt && d < maxWait; i++ {
		d *= 2
	}
	d = min(d, maxWait)
	jittered := time.Duration(float64(d) * (0.8 + 0.4*rand.Float64()))
	if jittered <= 0 {
		return d
	}
	return jittered
}
