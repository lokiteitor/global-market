// Package logging construye los loggers estructurados del backend.
//
// Todo el backend loguea JSON a stderr con log/slog (ADR-017: ningún logger
// de terceros). El nivel mínimo llega de la configuración.
package logging

import (
	"io"
	"log/slog"
	"os"

	"github.com/lokiteitor/global-market/backend/internal/platform/config"
)

// AttrRequestID es la clave del atributo de correlación de peticiones.
const AttrRequestID = "request_id"

// AttrService es la clave del atributo que identifica el binario emisor.
const AttrService = "service"

// New construye el logger raíz de un servicio: JSON a stderr, nivel desde la
// configuración y atributo service=gateway|engine en todas las líneas.
func New(cfg config.Config, service string) *slog.Logger {
	return NewWithWriter(os.Stderr, cfg, service)
}

// NewWithWriter es New con destino explícito (tests).
func NewWithWriter(w io.Writer, cfg config.Config, service string) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: ParseLevel(cfg.LogLevel)})
	return slog.New(h).With(slog.String(AttrService, service))
}

// ParseLevel traduce el nivel textual de la configuración a slog.Level.
// Los valores desconocidos caen a info: la validación estricta es
// responsabilidad de config.Validate.
func ParseLevel(level string) slog.Level {
	switch level {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// WithRequestID devuelve un logger hijo con el atributo request_id, para que
// toda línea emitida durante una petición quede correlacionada.
func WithRequestID(logger *slog.Logger, requestID string) *slog.Logger {
	if requestID == "" {
		return logger
	}
	return logger.With(slog.String(AttrRequestID, requestID))
}
