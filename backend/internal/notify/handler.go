package notify

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// writeTimeout acota cada escritura individual al socket (frames y cierres).
const writeTimeout = 10 * time.Second

// pingTimeout acota la espera del pong de cada ping WS de protocolo.
const pingTimeout = 10 * time.Second

// maxPingFailures es el número de fallos consecutivos de ping tras el cual se
// cierra la conexión (ADR-023: "cierre ante 2 fallos").
const maxPingFailures = 2

// TokenValidator resuelve un token bearer en su cuenta. El composition root
// (internal/gateway) lo implementa con internal/auth —búsqueda de la sesión
// por hash, igual que el middleware REST—; este paquete NO importa auth.
// Cualquier error se trata como token inválido hacia el cliente (401 en
// banda: frame error UNAUTHORIZED + cierre 4401); los errores que no sean de
// autorización quedan logueados.
type TokenValidator interface {
	Validate(ctx context.Context, token string) (accountID uuid.UUID, err error)
}

// SimSource es el reloj de simulación del handler (el mismo lector cacheado
// que estampa el meta REST). clock.Reader lo satisface directamente.
type SimSource interface {
	Now(ctx context.Context) simtime.SimTime
}

// WatermarkSource entrega el último seq de outbox ya despachado por el
// router (o el max(seq) actual si el router aún no despachó nada). Router lo
// implementa.
type WatermarkSource interface {
	Watermark(ctx context.Context) (int64, error)
}

// Handler sirve el upgrade WebSocket de GET /api/v1/ws y ejecuta el
// protocolo v1 del ADR-023 sobre cada conexión: auth en banda, join/leave de
// rooms, ping/pong de aplicación y el ping WS de protocolo.
type Handler struct {
	hub       *Hub
	validator TokenValidator
	sim       SimSource
	watermark WatermarkSource
	opts      Options
	logger    *slog.Logger
}

// NewHandler construye el handler del endpoint WS.
func NewHandler(hub *Hub, validator TokenValidator, sim SimSource, watermark WatermarkSource, opts Options, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		hub:       hub,
		validator: validator,
		sim:       sim,
		watermark: watermark,
		opts:      opts,
		logger:    logger,
	}
}

// ServeHTTP acepta el upgrade y ejecuta la sesión WS completa. Se monta SIN
// middleware de autenticación: la auth va en banda (primer frame, plazo
// AuthTimeout) porque los navegadores no pueden fijar cabeceras en el
// upgrade (ADR-023).
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: h.opts.AllowedOrigins,
	})
	if err != nil {
		// Accept ya respondió el error HTTP (origen no autorizado, upgrade roto).
		h.logger.Debug("notify: upgrade WS rechazado", slog.Any("error", err))
		return
	}
	defer ws.CloseNow() //nolint:errcheck // cierre defensivo; el camino normal ya cerró

	ctx := r.Context()

	// ── Auth en banda: primer frame obligatorio dentro del plazo ────────────
	accountID, ok := h.authenticate(ctx, ws)
	if !ok {
		return
	}

	// ── Alta en el hub (límite de conexiones por cuenta) ────────────────────
	conn, err := h.hub.Register(accountID)
	if err != nil {
		h.writeDirect(ctx, ws, NewError(ErrCodeTooManyConnections,
			"demasiadas conexiones simultáneas para esta cuenta"))
		ws.Close(websocket.StatusPolicyViolation, "demasiadas conexiones") //nolint:errcheck
		h.logger.Warn("notify: conexión rechazada por límite por cuenta",
			slog.String("account_id", accountID.String()))
		return
	}
	defer h.hub.Unregister(conn)

	// auth_ok con el sim-time actual, antes de arrancar el bucle de escritura
	// (a partir de aquí TODO frame saliente pasa por el buffer del hub).
	simNow := int64(h.sim.Now(ctx))
	if !h.writeDirect(ctx, ws, NewAuthOK(accountID, simNow)) {
		return
	}
	h.logger.Info("notify: sesión WS autenticada",
		slog.String("account_id", accountID.String()),
		slog.Int64("sim_time_seconds", simNow))

	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		h.writeLoop(connCtx, ws, conn)
	}()

	h.readLoop(connCtx, ws, conn)
	cancel()
	<-writerDone
}

// authenticate espera el frame auth (plazo AuthTimeout) y valida el token.
// Ante timeout, frame inválido o token rechazado cierra con 4401 (ADR-023).
//
// La lectura corre en una goroutine y el plazo lo marca un timer propio: si
// la cancelación llegara por el contexto del Read, coder/websocket cerraría
// el transporte en seco y el cliente vería EOF en lugar del close 4401.
func (h *Handler) authenticate(ctx context.Context, ws *websocket.Conn) (uuid.UUID, bool) {
	type readResult struct {
		data []byte
		err  error
	}
	readCh := make(chan readResult, 1)
	go func() {
		_, data, err := ws.Read(ctx)
		readCh <- readResult{data: data, err: err}
	}()

	timer := time.NewTimer(h.opts.AuthTimeout)
	defer timer.Stop()
	var res readResult
	select {
	case <-timer.C:
		// El Close desbloquea el Read pendiente; la goroutine termina sola.
		ws.Close(StatusAuthRequired, "auth requerida: no llegó el frame auth a tiempo") //nolint:errcheck
		h.logger.Debug("notify: cierre 4401 por timeout del frame auth")
		return uuid.Nil, false
	case res = <-readCh:
	}
	if res.err != nil {
		ws.Close(StatusAuthRequired, "auth requerida") //nolint:errcheck
		h.logger.Debug("notify: cierre 4401 sin frame auth", slog.Any("error", res.err))
		return uuid.Nil, false
	}
	frame, err := ParseClientFrame(res.data)
	if err != nil || frame.Type != FrameTypeAuth {
		h.writeDirect(ctx, ws, NewError(ErrCodeBadFrame, "el primer frame debe ser auth"))
		ws.Close(StatusAuthRequired, "auth requerida") //nolint:errcheck
		return uuid.Nil, false
	}
	vctx, cancel := context.WithTimeout(ctx, h.opts.AuthTimeout)
	defer cancel()
	accountID, err := h.validator.Validate(vctx, frame.Token)
	if err != nil {
		// Nunca se filtra la causa hacia el cliente; los errores internos (BD
		// caída) quedan logueados para operación.
		h.logger.Warn("notify: token WS rechazado", slog.Any("error", err))
		h.writeDirect(ctx, ws, NewError(ErrCodeUnauthorized, "credenciales o sesión inválidas"))
		ws.Close(StatusAuthRequired, "token inválido") //nolint:errcheck
		return uuid.Nil, false
	}
	return accountID, true
}

// readLoop procesa los frames del cliente hasta el cierre de la conexión.
func (h *Handler) readLoop(ctx context.Context, ws *websocket.Conn, conn *Conn) {
	accountID := conn.AccountID()
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			status := websocket.CloseStatus(err)
			if status == -1 && ctx.Err() == nil {
				h.logger.Debug("notify: lectura WS terminada", slog.Any("error", err))
			}
			return
		}
		frame, err := ParseClientFrame(data)
		if err != nil {
			h.hub.Send(conn, NewError(ErrCodeBadFrame, err.Error()))
			continue
		}
		switch frame.Type {
		case FrameTypeAuth:
			// La sesión ya está autenticada: re-auth no forma parte del v1.
			h.hub.Send(conn, NewError(ErrCodeBadFrame, "la conexión ya está autenticada"))
		case FrameTypeJoin:
			if frame.Room != RoomCorp {
				h.hub.Send(conn, NewError(ErrCodeUnsupportedRoom,
					"room desconocida: el protocolo v1 solo define \"corp\""))
				continue
			}
			h.hub.Join(conn, RoomCorp)
			wm, err := h.watermark.Watermark(ctx)
			if err != nil {
				h.logger.Error("notify: no se pudo calcular el watermark",
					slog.String("account_id", accountID.String()), slog.Any("error", err))
				h.hub.Leave(conn, RoomCorp)
				h.hub.Send(conn, NewError(ErrCodeInternal, "no se pudo completar el join"))
				continue
			}
			h.hub.Send(conn, NewJoined(RoomCorp, wm))
			h.logger.Info("notify: join a room",
				slog.String("account_id", accountID.String()),
				slog.String("room", RoomCorp),
				slog.Int64("watermark", wm))
		case FrameTypeLeave:
			if frame.Room != RoomCorp {
				h.hub.Send(conn, NewError(ErrCodeUnsupportedRoom,
					"room desconocida: el protocolo v1 solo define \"corp\""))
				continue
			}
			h.hub.Leave(conn, RoomCorp)
			h.logger.Info("notify: leave de room",
				slog.String("account_id", accountID.String()),
				slog.String("room", RoomCorp))
		case FrameTypePing:
			h.hub.Send(conn, NewPong(frame.Nonce))
		}
	}
}

// writeLoop drena el buffer de la conexión hacia el socket, ejecuta el ping
// WS de protocolo cada PingInterval (cierre tras maxPingFailures fallos
// consecutivos) y aplica el cierre 1013 de cliente lento.
func (h *Handler) writeLoop(ctx context.Context, ws *websocket.Conn, conn *Conn) {
	ticker := time.NewTicker(h.opts.PingInterval)
	defer ticker.Stop()
	pingFailures := 0
	for {
		select {
		case <-ctx.Done():
			ws.Close(websocket.StatusGoingAway, "apagado del servidor") //nolint:errcheck
			return
		case b := <-conn.Outbound():
			wctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := ws.Write(wctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				h.logger.Debug("notify: escritura WS fallida; se cierra la conexión",
					slog.String("account_id", conn.AccountID().String()), slog.Any("error", err))
				ws.CloseNow() //nolint:errcheck
				return
			}
		case <-conn.SlowClose():
			// El hub llenó el buffer: el cliente no consume a tiempo. 1013 y el
			// cliente re-sincroniza por REST al reconectar (ADR-023).
			ws.Close(websocket.StatusTryAgainLater, "cliente lento: buffer de envío lleno") //nolint:errcheck
			return
		case <-ticker.C:
			pctx, cancel := context.WithTimeout(ctx, pingTimeout)
			err := ws.Ping(pctx)
			cancel()
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}
				pingFailures++
				h.logger.Debug("notify: ping WS fallido",
					slog.String("account_id", conn.AccountID().String()),
					slog.Int("failures", pingFailures), slog.Any("error", err))
				if pingFailures >= maxPingFailures {
					h.logger.Warn("notify: conexión sin pong; se cierra",
						slog.String("account_id", conn.AccountID().String()))
					ws.CloseNow() //nolint:errcheck
					return
				}
			} else {
				pingFailures = 0
			}
		}
	}
}

// writeDirect escribe un frame directamente al socket (solo se usa ANTES de
// arrancar el bucle de escritura: auth y errores de alta). Devuelve si la
// escritura tuvo éxito.
func (h *Handler) writeDirect(ctx context.Context, ws *websocket.Conn, frame any) bool {
	b, err := json.Marshal(frame)
	if err != nil {
		h.logger.Error("notify: serializando frame directo", slog.Any("error", err))
		return false
	}
	wctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	if err := ws.Write(wctx, websocket.MessageText, b); err != nil {
		h.logger.Debug("notify: escritura directa fallida", slog.Any("error", err))
		return false
	}
	return true
}
