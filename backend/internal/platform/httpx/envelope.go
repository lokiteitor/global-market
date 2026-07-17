// Package httpx implementa la plataforma HTTP común del backend: los
// envelopes del contrato OpenAPI (docs/api/openapi.yaml), el decode JSON
// defensivo y la cadena de middlewares de los binarios.
package httpx

import (
	"encoding/json"
	"net/http"
	"time"
)

// Códigos de error de dominio usados por la plataforma. El catálogo completo
// vive en el contrato (schema Error); aquí solo los que emite esta capa.
const (
	CodeValidationError = "VALIDATION_ERROR"
	CodeInternal        = "INTERNAL"
	CodeNotFound        = "NOT_FOUND"
)

// Meta son los metadatos comunes de toda respuesta exitosa (schema Meta del
// contrato). Se construye fuera de este paquete: el reloj de simulación que
// alimenta sim_time llega en la fase siguiente.
type Meta struct {
	// SimTime es el sim-time legible `AAA-DDD-HH:MM` (simtime.Format).
	SimTime string `json:"sim_time"`
	// SimTimeSeconds es el sim-time canónico en segundos desde el génesis.
	SimTimeSeconds int64 `json:"sim_time_seconds"`
	// ServerTime es el wall-clock del servidor (RFC 3339, solo informativo).
	ServerTime time.Time `json:"server_time"`
	// NextCursor es el cursor de la página siguiente en listados paginados.
	NextCursor string `json:"next_cursor,omitempty"`
}

// dataEnvelope es el sobre de las respuestas exitosas: {"data":…,"meta":…}.
type dataEnvelope struct {
	Data any  `json:"data"`
	Meta Meta `json:"meta"`
}

// errorBody es el schema Error del contrato.
type errorBody struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// errorEnvelope es el sobre de error: {"error":{"code","message","details"}}.
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

// WriteData emite una respuesta exitosa con el envelope del contrato.
// Si data no es serializable (bug de programación) degrada a un 500 INTERNAL
// para no emitir una respuesta truncada.
func WriteData(w http.ResponseWriter, status int, data any, meta Meta) {
	body, err := json.Marshal(dataEnvelope{Data: data, Meta: meta})
	if err != nil {
		writeRawError(w, http.StatusInternalServerError, CodeInternal, "error interno serializando la respuesta")
		return
	}
	writeJSON(w, status, body)
}

// WriteError emite una respuesta de error con el envelope del contrato.
func WriteError(w http.ResponseWriter, status int, code, message string, details map[string]any) {
	body, err := json.Marshal(errorEnvelope{Error: errorBody{Code: code, Message: message, Details: details}})
	if err != nil {
		// Solo alcanzable con details no serializables (bug de programación).
		writeRawError(w, http.StatusInternalServerError, CodeInternal, "error interno serializando el error")
		return
	}
	writeJSON(w, status, body)
}

// writeJSON escribe cabeceras y cuerpo de una respuesta JSON ya serializada.
func writeJSON(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// writeRawError emite un envelope de error construido a mano, sin pasar por
// json.Marshal; último recurso ante fallos de serialización.
func writeRawError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, []byte(`{"error":{"code":"`+code+`","message":"`+message+`"}}`))
}
