package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// DefaultMaxBodyBytes es el límite por defecto del cuerpo de una petición.
const DefaultMaxBodyBytes int64 = 1 << 20 // 1 MiB

// ReadJSON decodifica el cuerpo JSON de la petición en dst aplicando un
// límite de tamaño (maxBytes; si es <= 0 se usa DefaultMaxBodyBytes).
//
// Ante cuerpo inválido escribe una respuesta 400 con code VALIDATION_ERROR
// (envelope del contrato) y devuelve el error: el handler solo debe abortar.
func ReadJSON(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBodyBytes
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		writeDecodeError(w, err, maxBytes)
		return err
	}
	// Un cuerpo válido contiene exactamente un valor JSON.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		err = errors.New("el cuerpo contiene datos tras el valor JSON")
		WriteError(w, http.StatusBadRequest, CodeValidationError, err.Error(), nil)
		return err
	}
	return nil
}

// writeDecodeError traduce los errores de decode a una respuesta 400 tipada
// con detalles útiles para el cliente.
func writeDecodeError(w http.ResponseWriter, err error, maxBytes int64) {
	var (
		syntaxErr  *json.SyntaxError
		typeErr    *json.UnmarshalTypeError
		maxBytesEr *http.MaxBytesError
	)
	switch {
	case errors.As(err, &maxBytesEr):
		WriteError(w, http.StatusBadRequest, CodeValidationError,
			"el cuerpo de la petición excede el tamaño máximo",
			map[string]any{"max_bytes": maxBytes})
	case errors.Is(err, io.EOF):
		WriteError(w, http.StatusBadRequest, CodeValidationError,
			"el cuerpo de la petición está vacío", nil)
	case errors.As(err, &syntaxErr):
		WriteError(w, http.StatusBadRequest, CodeValidationError,
			"el cuerpo no es JSON válido",
			map[string]any{"offset": syntaxErr.Offset})
	case errors.As(err, &typeErr):
		WriteError(w, http.StatusBadRequest, CodeValidationError,
			fmt.Sprintf("tipo inválido para el campo %q", typeErr.Field),
			map[string]any{"field": typeErr.Field, "expected": typeErr.Type.String()})
	default:
		WriteError(w, http.StatusBadRequest, CodeValidationError,
			"no se pudo decodificar el cuerpo de la petición", nil)
	}
}
