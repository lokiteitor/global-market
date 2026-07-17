package ledger

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/lokiteitor/global-market/backend/internal/platform/keyset"
)

// Cursores keyset opacos (meta.next_cursor del contrato), definidos como
// structs de platform/keyset: los campos, en orden de declaración, son la
// clave de ordenación de la última fila devuelta (binario de anchura fija +
// base64url sin padding; el cliente los trata como strings opacos).
//
//   - Cuentas: orden ASC por id (UUIDv7 ≈ orden de creación) → 16 bytes.
//   - Partidas: orden DESC por (created_at, id) → 8 bytes de µs Unix
//     big-endian + 16 bytes de id.
//
// Los errores de keyset se envuelven en ErrInvalidCursor, el error tipado que
// los handlers mapean a 400 VALIDATION_ERROR.

// accountCursor es la clave keyset de la página de cuentas.
type accountCursor struct {
	ID uuid.UUID
}

// entryCursor es la clave keyset de la página del extracto.
type entryCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// encodeAccountCursor serializa el cursor de la página siguiente de cuentas.
func encodeAccountCursor(id uuid.UUID) string {
	return keyset.Encode(accountCursor{ID: id})
}

// decodeAccountCursor valida y deserializa un cursor de cuentas.
func decodeAccountCursor(s string) (uuid.UUID, error) {
	c, err := keyset.Decode[accountCursor](s)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}
	return c.ID, nil
}

// encodeEntryCursor serializa el cursor de la página siguiente de partidas.
func encodeEntryCursor(createdAt time.Time, id uuid.UUID) string {
	return keyset.Encode(entryCursor{CreatedAt: createdAt, ID: id})
}

// decodeEntryCursor valida y deserializa un cursor de partidas.
func decodeEntryCursor(s string) (time.Time, uuid.UUID, error) {
	c, err := keyset.Decode[entryCursor](s)
	if err != nil {
		return time.Time{}, uuid.UUID{}, fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}
	return c.CreatedAt, c.ID, nil
}
