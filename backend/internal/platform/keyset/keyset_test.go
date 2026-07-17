package keyset_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/lokiteitor/global-market/backend/internal/platform/keyset"
)

// Formas de cursor de los tests: las mismas que usan los módulos reales
// (id solo, (created_at, id) y una con un int64 derivado tipo sim-time).
type idCursor struct {
	ID uuid.UUID
}

type entryCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

type simTime int64

type simCursor struct {
	SimTimeAt simTime
	ID        uuid.UUID
}

func TestRoundtripUUID(t *testing.T) {
	want := idCursor{ID: uuid.MustParse("01981c5e-7d2a-7f3b-9e41-a2c4d6e8f012")}
	cur := keyset.Encode(want)
	if cur == "" || strings.ContainsAny(cur, "+/=") {
		t.Fatalf("cursor no es base64url sin padding: %q", cur)
	}
	got, err := keyset.Decode[idCursor](cur)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got != want {
		t.Fatalf("ida y vuelta: %+v != %+v", got, want)
	}
}

func TestRoundtripComposite(t *testing.T) {
	// timestamptz almacena microsegundos: la ida y vuelta debe ser exacta.
	want := entryCursor{
		CreatedAt: time.Date(2026, 7, 16, 10, 30, 45, 123456000, time.UTC),
		ID:        uuid.MustParse("01981c5e-7d2a-7f3b-9e41-a2c4d6e8f012"),
	}
	got, err := keyset.Decode[entryCursor](keyset.Encode(want))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !got.CreatedAt.Equal(want.CreatedAt) || got.ID != want.ID {
		t.Fatalf("ida y vuelta: %+v != %+v", got, want)
	}
	if got.CreatedAt.Location() != time.UTC {
		t.Fatalf("el instante decodificado debe estar en UTC: %v", got.CreatedAt.Location())
	}
}

func TestRoundtripPreservesMicroseconds(t *testing.T) {
	want := entryCursor{
		CreatedAt: time.UnixMicro(1_752_661_845_000_007).UTC(), // µs impares
		ID:        uuid.New(),
	}
	got, err := keyset.Decode[entryCursor](keyset.Encode(want))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.CreatedAt.UnixMicro() != want.CreatedAt.UnixMicro() {
		t.Fatalf("microsegundos perdidos: %d != %d", got.CreatedAt.UnixMicro(), want.CreatedAt.UnixMicro())
	}
}

func TestRoundtripNamedInt64(t *testing.T) {
	for _, at := range []simTime{0, 604800, -1} {
		want := simCursor{SimTimeAt: at, ID: uuid.New()}
		got, err := keyset.Decode[simCursor](keyset.Encode(want))
		if err != nil {
			t.Fatalf("Decode(sim=%d): %v", at, err)
		}
		if got != want {
			t.Fatalf("ida y vuelta: %+v != %+v", got, want)
		}
	}
}

func TestDecodeInvalid(t *testing.T) {
	for _, cur := range []string{
		"",                                    // vacío
		"!!!no-base64",                        // base64 inválido
		"YWJj",                                // longitud incorrecta (3 bytes)
		"YWJjZGVmZ2hpamtsbW5vcHFyc3R1dnd4eXo", // demasiado largo
	} {
		if _, err := keyset.Decode[idCursor](cur); !errors.Is(err, keyset.ErrInvalidCursor) {
			t.Errorf("Decode(%q) = %v, esperado ErrInvalidCursor", cur, err)
		}
	}
}

func TestDecodeRejectsForeignShape(t *testing.T) {
	// Un cursor de 16 bytes no vale para una forma de 24, y viceversa.
	if _, err := keyset.Decode[entryCursor](keyset.Encode(idCursor{ID: uuid.New()})); !errors.Is(err, keyset.ErrInvalidCursor) {
		t.Errorf("cursor de idCursor aceptado como entryCursor: %v", err)
	}
	cur := keyset.Encode(entryCursor{CreatedAt: time.Now(), ID: uuid.New()})
	if _, err := keyset.Decode[idCursor](cur); !errors.Is(err, keyset.ErrInvalidCursor) {
		t.Errorf("cursor de entryCursor aceptado como idCursor: %v", err)
	}
}

func TestUnsupportedShapesPanic(t *testing.T) {
	type withString struct{ Name string }
	type withUnexported struct{ id uuid.UUID } //nolint:unused // la forma es el test
	type empty struct{}

	mustPanic(t, "T no struct", func() { keyset.Encode(42) })
	mustPanic(t, "campo string", func() { keyset.Encode(withString{Name: "x"}) })
	mustPanic(t, "campo no exportado", func() { keyset.Encode(withUnexported{}) })
	mustPanic(t, "struct vacío", func() { keyset.Encode(empty{}) })
	mustPanic(t, "Decode con forma no soportada", func() {
		_, _ = keyset.Decode[withString]("YWJjZGVmZ2g")
	})
}

func mustPanic(t *testing.T, name string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s: se esperaba pánico (error de programación)", name)
		}
	}()
	fn()
}
