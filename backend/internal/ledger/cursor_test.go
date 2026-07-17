package ledger

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestAccountCursorRoundtrip(t *testing.T) {
	id := uuid.MustParse("01981c5e-7d2a-7f3b-9e41-a2c4d6e8f012")
	cur := encodeAccountCursor(id)
	if cur == "" || strings.ContainsAny(cur, "+/=") {
		t.Fatalf("cursor no es base64url sin padding: %q", cur)
	}
	got, err := decodeAccountCursor(cur)
	if err != nil {
		t.Fatalf("decodeAccountCursor: %v", err)
	}
	if got != id {
		t.Fatalf("ida y vuelta: %s != %s", got, id)
	}
}

func TestAccountCursorInvalid(t *testing.T) {
	for _, cur := range []string{
		"",                                    // vacío
		"!!!no-base64",                        // base64 inválido
		"YWJj",                                // longitud incorrecta (3 bytes)
		"YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXo", // demasiado largo
	} {
		if _, err := decodeAccountCursor(cur); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("decodeAccountCursor(%q) = %v, esperado ErrInvalidCursor", cur, err)
		}
	}
}

func TestEntryCursorRoundtrip(t *testing.T) {
	id := uuid.MustParse("01981c5e-7d2a-7f3b-9e41-a2c4d6e8f012")
	// timestamptz almacena microsegundos: la ida y vuelta debe ser exacta.
	createdAt := time.Date(2026, 7, 16, 10, 30, 45, 123456000, time.UTC)
	cur := encodeEntryCursor(createdAt, id)
	gotAt, gotID, err := decodeEntryCursor(cur)
	if err != nil {
		t.Fatalf("decodeEntryCursor: %v", err)
	}
	if !gotAt.Equal(createdAt) {
		t.Fatalf("created_at ida y vuelta: %v != %v", gotAt, createdAt)
	}
	if gotID != id {
		t.Fatalf("id ida y vuelta: %s != %s", gotID, id)
	}
}

func TestEntryCursorPreservesMicroseconds(t *testing.T) {
	id := uuid.New()
	at := time.UnixMicro(1_752_661_845_000_007).UTC() // µs impares
	gotAt, _, err := decodeEntryCursor(encodeEntryCursor(at, id))
	if err != nil {
		t.Fatalf("decodeEntryCursor: %v", err)
	}
	if gotAt.UnixMicro() != at.UnixMicro() {
		t.Fatalf("microsegundos perdidos: %d != %d", gotAt.UnixMicro(), at.UnixMicro())
	}
}

func TestEntryCursorInvalid(t *testing.T) {
	for _, cur := range []string{"", "###", "YWJjZGVm"} {
		if _, _, err := decodeEntryCursor(cur); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("decodeEntryCursor(%q) = %v, esperado ErrInvalidCursor", cur, err)
		}
	}
	// Un cursor de cuentas (16 bytes) no vale como cursor de partidas (24).
	if _, _, err := decodeEntryCursor(encodeAccountCursor(uuid.New())); !errors.Is(err, ErrInvalidCursor) {
		t.Error("cursor de cuentas aceptado como cursor de partidas")
	}
}
