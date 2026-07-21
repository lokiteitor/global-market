// Package notify implementa el Notification/Event Gateway por WebSocket del
// backend (ADR-023): el endpoint GET /api/v1/ws del gateway, el hub de
// conexiones con suscripción por rooms, y el router que consume el outbox
// (consumidor lógico "notification_gateway") y hace fan-out de los eventos de
// dominio a las corporaciones interesadas.
//
// El protocolo v1 usa frames JSON de texto. La autenticación va EN BANDA (los
// navegadores no pueden fijar cabeceras en el upgrade): el primer frame debe
// ser auth y llegar dentro del plazo o la conexión se cierra con el código
// 4401. No hay snapshots ni replay por el socket: el cliente bootstrapea por
// REST y recibe deltas con seq > watermark (entrega at-least-once por
// conexión).
//
// El paquete pertenece al contexto del gateway: puede leer la BD para el
// enrutado, pero NO importa internal/auth — la validación de tokens llega por
// la interfaz TokenValidator que implementa el composition root.
package notify

import (
	"encoding/json"
	"fmt"

	"github.com/coder/websocket"
	"github.com/google/uuid"
)

// Tipos de frame del protocolo v1 (ADR-023). Cliente → servidor.
const (
	FrameTypeAuth  = "auth"
	FrameTypeJoin  = "join"
	FrameTypeLeave = "leave"
	FrameTypePing  = "ping"
)

// Tipos de frame del protocolo v1 (ADR-023). Servidor → cliente.
const (
	FrameTypeAuthOK = "auth_ok"
	FrameTypeJoined = "joined"
	FrameTypeEvent  = "event"
	FrameTypePong   = "pong"
	FrameTypeError  = "error"
)

// RoomCorp es la única room del protocolo v1: los eventos de la propia
// corporación. viewport:<bbox> y alerts son extensiones aditivas futuras.
const RoomCorp = "corp"

// StatusAuthRequired es el código de cierre 4401 del ADR-023: el frame auth
// no llegó a tiempo o el token es inválido.
const StatusAuthRequired websocket.StatusCode = 4401

// Códigos del frame error (campo code). Son parte del protocolo observable:
// el SDK y el cliente web distinguen la causa sin parsear el mensaje.
const (
	// ErrCodeBadFrame: el frame no es JSON válido, su type es desconocido o le
	// faltan campos obligatorios.
	ErrCodeBadFrame = "BAD_FRAME"
	// ErrCodeUnauthorized: token inválido o sesión expirada en el auth en banda.
	ErrCodeUnauthorized = "UNAUTHORIZED"
	// ErrCodeUnsupportedRoom: join/leave de una room que el protocolo v1 no
	// define (solo existe "corp").
	ErrCodeUnsupportedRoom = "UNSUPPORTED_ROOM"
	// ErrCodeTooManyConnections: la cuenta superó II_WS_MAX_CONNS_PER_ACCOUNT.
	ErrCodeTooManyConnections = "TOO_MANY_CONNECTIONS"
	// ErrCodeInternal: fallo interno del servidor procesando el frame.
	ErrCodeInternal = "INTERNAL"
)

// ─── Cliente → servidor ─────────────────────────────────────────────────────

// ClientFrame es un frame entrante ya validado. Un único struct cubre los
// cuatro tipos del protocolo; ParseClientFrame garantiza que los campos
// obligatorios de cada tipo están presentes.
type ClientFrame struct {
	// Type es el tipo del frame: auth | join | leave | ping.
	Type string `json:"type"`
	// Token es el bearer de sesión (solo type=auth).
	Token string `json:"token,omitempty"`
	// Room es la room objetivo (solo type=join|leave).
	Room string `json:"room,omitempty"`
	// Nonce es el eco opcional del keepalive (solo type=ping).
	Nonce string `json:"nonce,omitempty"`
}

// ParseClientFrame interpreta y valida un frame entrante. Devuelve un error
// descriptivo (nunca el JSON crudo) ante JSON malformado, type desconocido o
// campos obligatorios ausentes; el handler lo traduce al frame error
// BAD_FRAME del protocolo.
func ParseClientFrame(data []byte) (ClientFrame, error) {
	var f ClientFrame
	if err := json.Unmarshal(data, &f); err != nil {
		return ClientFrame{}, fmt.Errorf("notify: frame no es JSON válido: %w", err)
	}
	switch f.Type {
	case FrameTypeAuth:
		if f.Token == "" {
			return ClientFrame{}, fmt.Errorf("notify: frame auth sin token")
		}
	case FrameTypeJoin, FrameTypeLeave:
		if f.Room == "" {
			return ClientFrame{}, fmt.Errorf("notify: frame %s sin room", f.Type)
		}
	case FrameTypePing:
		// nonce es opcional
	case "":
		return ClientFrame{}, fmt.Errorf("notify: frame sin type")
	default:
		return ClientFrame{}, fmt.Errorf("notify: type de frame desconocido %q", f.Type)
	}
	return f, nil
}

// ─── Servidor → cliente ─────────────────────────────────────────────────────

// AuthOKFrame confirma la sesión validada:
// {"type":"auth_ok","account_id":uuid,"sim_time_seconds":N}.
type AuthOKFrame struct {
	Type           string `json:"type"`
	AccountID      string `json:"account_id"`
	SimTimeSeconds int64  `json:"sim_time_seconds"`
}

// NewAuthOK construye el frame auth_ok.
func NewAuthOK(accountID uuid.UUID, simTimeSeconds int64) AuthOKFrame {
	return AuthOKFrame{Type: FrameTypeAuthOK, AccountID: accountID.String(), SimTimeSeconds: simTimeSeconds}
}

// JoinedFrame confirma la suscripción a una room:
// {"type":"joined","room":"corp","watermark":N}. El watermark es el último
// seq de outbox ya despachado: el cliente bootstrapea por REST y sabe que
// todo evento posterior llegará por el socket con seq > watermark.
type JoinedFrame struct {
	Type      string `json:"type"`
	Room      string `json:"room"`
	Watermark int64  `json:"watermark"`
}

// NewJoined construye el frame joined.
func NewJoined(room string, watermark int64) JoinedFrame {
	return JoinedFrame{Type: FrameTypeJoined, Room: room, Watermark: watermark}
}

// EventFrame entrega un evento de dominio del outbox. El payload viaja TAL
// CUAL se emitió (dinero/stock como strings del contrato, jamás float).
type EventFrame struct {
	Type          string          `json:"type"`
	Room          string          `json:"room"`
	Seq           int64           `json:"seq"`
	EventID       string          `json:"event_id"`
	EventType     string          `json:"event_type"`
	SimTime       int64           `json:"sim_time"`
	AggregateType string          `json:"aggregate_type"`
	AggregateID   string          `json:"aggregate_id"`
	Payload       json.RawMessage `json:"payload"`
}

// PongFrame responde a un ping de aplicación con el mismo nonce:
// {"type":"pong","nonce":"..."}.
type PongFrame struct {
	Type  string `json:"type"`
	Nonce string `json:"nonce,omitempty"`
}

// NewPong construye el frame pong.
func NewPong(nonce string) PongFrame {
	return PongFrame{Type: FrameTypePong, Nonce: nonce}
}

// ErrorFrame informa de un error de protocolo sin cerrar necesariamente la
// conexión: {"type":"error","code":"...","message":"..."}.
type ErrorFrame struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// NewError construye el frame error.
func NewError(code, message string) ErrorFrame {
	return ErrorFrame{Type: FrameTypeError, Code: code, Message: message}
}
