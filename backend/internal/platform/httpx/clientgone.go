package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/lokiteitor/global-market/backend/internal/platform/logging"
)

// StatusClientClosedRequest es el status no estándar (convención de nginx) con
// el que se cierra una petición que abortó el propio cliente. No hay nadie
// escuchando la respuesta, así que el código solo existe para el observador:
// mantener las desconexiones fuera de la familia 5xx es lo que hace fiables
// los contadores de error del gateway (SAD §13: disparadores MEDIDOS) cuando
// cientos de miles de bots abren y cierran clientes.
const StatusClientClosedRequest = 499

// ClientGone indica que err es consecuencia del final anticipado del contexto
// de la petición —el cliente cerró la conexión o su plazo expiró— y no de un
// fallo del servicio. Es la regla única que comparten los sitios que responden
// (WriteClientGone) y los que solo registran algo ya fuera del camino de la
// respuesta, para que ambos coincidan en qué cuenta como fallo del servidor.
//
// Exige que el contexto de la petición esté efectivamente terminado: así un
// context.Canceled que provenga de un contexto interno del servidor —un bug de
// verdad— no se disfraza de desconexión del cliente.
func ClientGone(r *http.Request, err error) bool {
	if r.Context().Err() == nil {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// LogLevelFor devuelve el nivel con el que registrar err en un sitio que NO
// responde (la respuesta ya salió o es best-effort): ERROR si es un fallo real
// del servicio, WARN si solo refleja que el cliente ya no está.
func LogLevelFor(r *http.Request, err error) slog.Level {
	if ClientGone(r, err) {
		return slog.LevelWarn
	}
	return slog.LevelError
}

// WriteClientGone distingue el final anticipado del contexto de la petición de
// un fallo real del servidor. Si err viene de que el cliente se fue o de que
// el plazo de la petición expiró, responde el status adecuado, lo registra al
// nivel que le corresponde (no ERROR) y devuelve true; el llamante debe
// entonces cortar sin escribir nada más. Devuelve false para cualquier otro
// error, que sigue su camino normal hacia el 500 INTERNAL.
//
// Exige que el contexto de la petición esté efectivamente terminado: así un
// context.Canceled que provenga de un contexto interno del servidor —un bug
// de verdad— sigue emitiendo su 500 y su línea de ERROR.
func WriteClientGone(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error, doing string) bool {
	if !ClientGone(r, err) {
		return false
	}
	ctx := r.Context()

	var (
		status int
		level  slog.Level
		msg    string
	)
	switch {
	case errors.Is(err, context.Canceled):
		// El cliente cerró la conexión a medias: no es un fallo del servicio.
		status, level, msg = StatusClientClosedRequest, slog.LevelInfo, "petición abortada por el cliente"
	case errors.Is(err, context.DeadlineExceeded):
		// El plazo lo impone el servidor, así que esto sí es un problema del
		// servicio (una consulta lenta) y debe seguir contando como 5xx; lo
		// que cambia es que se nombra como lo que es, no como un INTERNAL.
		status, level, msg = http.StatusGatewayTimeout, slog.LevelWarn, "plazo de la petición agotado"
	default:
		return false
	}

	logging.WithRequestID(logger, RequestIDFromContext(ctx)).LogAttrs(ctx, level, msg,
		slog.String("doing", doing),
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.String("error", err.Error()),
	)
	w.WriteHeader(status)
	return true
}
