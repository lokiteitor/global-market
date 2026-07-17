package ledger

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Cursores keyset opacos (meta.next_cursor del contrato). Codifican la clave
// de ordenación de la última fila devuelta, en binario + base64url sin
// padding; el cliente los trata como strings opacos.
//
//   - Cuentas: orden ASC por id (UUIDv7 ≈ orden de creación) → 16 bytes.
//   - Partidas: orden DESC por (created_at, id) → 8 bytes de µs Unix
//     big-endian + 16 bytes de id. timestamptz almacena microsegundos, por lo
//     que la ida y vuelta es exacta y la comparación keyset no pierde filas.

const (
	accountCursorLen = 16
	entryCursorLen   = 8 + 16
)

// encodeAccountCursor serializa el cursor de la página siguiente de cuentas.
func encodeAccountCursor(id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString(id[:])
}

// decodeAccountCursor valida y deserializa un cursor de cuentas.
func decodeAccountCursor(s string) (uuid.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return uuid.UUID{}, fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}
	if len(raw) != accountCursorLen {
		return uuid.UUID{}, fmt.Errorf("%w: longitud %d", ErrInvalidCursor, len(raw))
	}
	var id uuid.UUID
	copy(id[:], raw)
	return id, nil
}

// encodeEntryCursor serializa el cursor de la página siguiente de partidas.
func encodeEntryCursor(createdAt time.Time, id uuid.UUID) string {
	raw := make([]byte, entryCursorLen)
	binary.BigEndian.PutUint64(raw[:8], uint64(createdAt.UnixMicro()))
	copy(raw[8:], id[:])
	return base64.RawURLEncoding.EncodeToString(raw)
}

// decodeEntryCursor valida y deserializa un cursor de partidas.
func decodeEntryCursor(s string) (time.Time, uuid.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return time.Time{}, uuid.UUID{}, fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}
	if len(raw) != entryCursorLen {
		return time.Time{}, uuid.UUID{}, fmt.Errorf("%w: longitud %d", ErrInvalidCursor, len(raw))
	}
	createdAt := time.UnixMicro(int64(binary.BigEndian.Uint64(raw[:8]))).UTC()
	var id uuid.UUID
	copy(id[:], raw[8:])
	return createdAt, id, nil
}
