package botsdk

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestParseMoney(t *testing.T) {
	valid := map[string]int64{
		"0":                0,
		"125000":           125_000,
		"-740":             -740,                  // saldos contables (cuenta emission) pueden ser negativos
		"9007199254740993": 9_007_199_254_740_993, // supera la precisión de float64
	}
	for in, want := range valid {
		got, err := ParseMoney(in)
		if err != nil || got != want {
			t.Errorf("ParseMoney(%q) = %d, %v; esperaba %d", in, got, err, want)
		}
	}
	invalid := []string{"", "-", "+5", " 5", "5 ", "1.5", "1e3", "abc", "12a", "--1", "١٢٣", "9223372036854775808"}
	for _, in := range invalid {
		if _, err := ParseMoney(in); err == nil {
			t.Errorf("ParseMoney(%q): esperaba error", in)
		}
	}
}

func TestParseQty(t *testing.T) {
	got, err := ParseQty("500")
	if err != nil || got != 500 {
		t.Errorf("ParseQty(500) = %d, %v", got, err)
	}
	for _, in := range []string{"", "-1", "+1", "1.0", "abc"} {
		if _, err := ParseQty(in); err == nil {
			t.Errorf("ParseQty(%q): esperaba error", in)
		}
	}
}

func TestConversionesInt64(t *testing.T) {
	if MoneyFromInt64(-5) != Money("-5") || MoneyFromInt64(120) != Money("120") {
		t.Error("MoneyFromInt64 incorrecto")
	}
	q, err := QtyFromInt64(500)
	if err != nil || q != Qty("500") {
		t.Errorf("QtyFromInt64(500) = %q, %v", q, err)
	}
	if _, err := QtyFromInt64(-1); err == nil {
		t.Error("QtyFromInt64(-1): esperaba error")
	}
	if v, err := Money("120").Int64(); err != nil || v != 120 {
		t.Errorf("Money.Int64 = %d, %v", v, err)
	}
	if v, err := Qty("50").Int64(); err != nil || v != 50 {
		t.Errorf("Qty.Int64 = %d, %v", v, err)
	}
}

func TestWaitFor(t *testing.T) {
	// Se cumple a la tercera evaluación.
	n := 0
	err := WaitFor(context.Background(), time.Millisecond, func() (bool, error) {
		n++
		return n >= 3, nil
	})
	if err != nil || n != 3 {
		t.Errorf("WaitFor: n = %d, err = %v", n, err)
	}

	// El error de cond se propaga.
	boom := errors.New("boom")
	if err := WaitFor(context.Background(), time.Millisecond, func() (bool, error) { return false, boom }); !errors.Is(err, boom) {
		t.Errorf("WaitFor error = %v, esperaba boom", err)
	}

	// La cancelación del contexto corta la espera.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = WaitFor(ctx, time.Minute, func() (bool, error) { return false, nil })
	if !errors.Is(err, context.Canceled) {
		t.Errorf("WaitFor tras cancel = %v", err)
	}
}

func TestAllDetectaCursorRepetido(t *testing.T) {
	fetch := func(_ context.Context, cursor string) (Page[int], error) {
		return Page[int]{Items: []int{1}, Meta: Meta{NextCursor: "bucle"}}, nil
	}
	_, err := CollectAll(context.Background(), fetch)
	if err == nil {
		t.Fatal("esperaba error por cursor repetido")
	}
}
