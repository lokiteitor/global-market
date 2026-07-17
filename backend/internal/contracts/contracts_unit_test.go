package contracts

import (
	"errors"
	"math"
	"testing"

	"github.com/google/uuid"
)

// TestLockAmounts verifica el cálculo del valor y la garantía del 10% y la
// validación de desbordamiento con math/big (incluido el margen ×10 de las
// fórmulas SQL de garantía).
func TestLockAmounts(t *testing.T) {
	value, guarantee, err := lockAmounts(100, 50)
	if err != nil {
		t.Fatalf("lockAmounts(100, 50): %v", err)
	}
	if value != 5000 || guarantee != 500 {
		t.Fatalf("value=%d guarantee=%d, esperado 5000/500", value, guarantee)
	}

	// Redondeo entero exactamente como (v*10)/100 en SQL.
	_, guarantee, err = lockAmounts(3, 3) // valor 9 → garantía 90/100 = 0
	if err != nil || guarantee != 0 {
		t.Fatalf("garantía de valor 9: %d (err %v), esperado 0", guarantee, err)
	}

	// qty*price desborda int64.
	if _, _, err := lockAmounts(math.MaxInt64, 2); !errorsIsAll(err, ErrOverflow, ErrValidation) {
		t.Fatalf("desbordamiento directo: %v, esperado ErrOverflow (y ErrValidation)", err)
	}
	// qty*price cabe pero el intermedio ×10 de la garantía SQL no.
	if _, _, err := lockAmounts(1_000_000_000, 1_200_000_000); !errors.Is(err, ErrOverflow) {
		t.Fatalf("desbordamiento del margen ×10: %v, esperado ErrOverflow", err)
	}
	// Justo por debajo del límite: MaxInt64/10.
	if _, _, err := lockAmounts(1, math.MaxInt64/10); err != nil {
		t.Fatalf("valor en el límite: %v, esperado nil", err)
	}
}

// TestBoardCursorRoundTrip verifica el cursor keyset del tablón: ida y
// vuelta, rechazo de basura y rechazo de cursores de otro orden.
func TestBoardCursorRoundTrip(t *testing.T) {
	pub := Publication{ID: uuid.New(), UnitPrice: 120, PublishedAtSim: 999, DeliverySimSeconds: 3600}

	for _, sort := range []BoardSort{SortUnitPriceAsc, SortUnitPriceDesc, SortPublishedAtDesc, SortDeadlineAsc} {
		cur := encodeBoardCursor(sort, pub)
		key, id, err := decodeBoardCursor(cur, sort)
		if err != nil {
			t.Fatalf("decode(%s): %v", sort, err)
		}
		if id != pub.ID || key != boardSortKey(sort, pub) {
			t.Fatalf("cursor de %s: key=%d id=%s, esperado key=%d id=%s",
				sort, key, id, boardSortKey(sort, pub), pub.ID)
		}
	}

	// La clave codificada depende del orden activo.
	if k := boardSortKey(SortPublishedAtDesc, pub); k != 999 {
		t.Fatalf("clave published_at_desc: %d, esperado 999", k)
	}
	if k := boardSortKey(SortDeadlineAsc, pub); k != 3600 {
		t.Fatalf("clave deadline_asc: %d, esperado 3600", k)
	}

	// Cursor de un orden usado con otro → ErrInvalidCursor.
	cur := encodeBoardCursor(SortUnitPriceAsc, pub)
	if _, _, err := decodeBoardCursor(cur, SortUnitPriceDesc); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("cursor de otro orden: %v, esperado ErrInvalidCursor", err)
	}
	// Basura → ErrInvalidCursor.
	if _, _, err := decodeBoardCursor("no-es-un-cursor", SortUnitPriceAsc); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("cursor basura: %v, esperado ErrInvalidCursor", err)
	}
}

// TestNormalizePublicationInput cubre la validación de forma (sin BD).
func TestNormalizePublicationInput(t *testing.T) {
	publisher := uuid.New()
	counterparty := uuid.New()
	product := uuid.New()
	node := uuid.New()

	valid := func() PublicationInput {
		return PublicationInput{
			Kind:               KindSell,
			ProductID:          &product,
			QuantityTotal:      100,
			UnitPrice:          50,
			OriginNodeID:       &node,
			DeliverySimSeconds: 3600,
		}
	}

	in := valid()
	if err := normalizePublicationInput(publisher, &in); err != nil {
		t.Fatalf("entrada válida: %v", err)
	}
	if in.Channel != ChannelBoard || in.MinLot != 1 {
		t.Fatalf("defaults no aplicados: channel=%q min_lot=%d", in.Channel, in.MinLot)
	}

	cases := []struct {
		name   string
		mutate func(*PublicationInput)
		want   error
	}{
		{"freight es Fase 2", func(i *PublicationInput) { i.Kind = KindFreight }, ErrFreightPhase2},
		{"kind inválido", func(i *PublicationInput) { i.Kind = "swap" }, ErrValidation},
		{"channel inválido", func(i *PublicationInput) { i.Channel = "dark" }, ErrValidation},
		{"private sin counterparty", func(i *PublicationInput) { i.Channel = ChannelPrivate }, ErrValidation},
		{"private consigo mismo", func(i *PublicationInput) {
			i.Channel = ChannelPrivate
			i.CounterpartyAccountID = &publisher
		}, ErrValidation},
		{"board con counterparty", func(i *PublicationInput) { i.CounterpartyAccountID = &counterparty }, ErrValidation},
		{"sin producto", func(i *PublicationInput) { i.ProductID = nil }, ErrValidation},
		{"sell sin origen", func(i *PublicationInput) { i.OriginNodeID = nil }, ErrValidation},
		{"sell con destino", func(i *PublicationInput) { i.DestinationNodeID = &node }, ErrValidation},
		{"buy sin destino", func(i *PublicationInput) {
			i.Kind = KindBuy
			i.OriginNodeID = nil
		}, ErrValidation},
		{"buy con origen", func(i *PublicationInput) {
			i.Kind = KindBuy
			i.DestinationNodeID = &node
		}, ErrValidation},
		{"cantidad cero", func(i *PublicationInput) { i.QuantityTotal = 0 }, ErrValidation},
		{"precio cero", func(i *PublicationInput) { i.UnitPrice = 0 }, ErrValidation},
		{"min_lot negativo", func(i *PublicationInput) { i.MinLot = -1 }, ErrValidation},
		{"plazo cero", func(i *PublicationInput) { i.DeliverySimSeconds = 0 }, ErrValidation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := valid()
			tc.mutate(&in)
			if err := normalizePublicationInput(publisher, &in); !errors.Is(err, tc.want) {
				t.Fatalf("%s: %v, esperado %v", tc.name, err, tc.want)
			}
		})
	}
}

// TestErrorDetails verifica que los errores con details responden a errors.As
// y a errors.Is de su sentinela (contrato con los handlers).
func TestErrorDetails(t *testing.T) {
	var collErr *CollateralError
	err := error(&CollateralError{Resource: "cash", Required: 1000, Available: 740})
	if !errors.Is(err, ErrInsufficientCollateral) || !errors.As(err, &collErr) {
		t.Fatalf("CollateralError no responde a Is/As: %v", err)
	}
	if collErr.Required != 1000 || collErr.Available != 740 {
		t.Fatalf("details perdidos: %+v", collErr)
	}

	var lotErr *MinLotError
	err = error(&MinLotError{MinLot: 30, QuantityRemaining: 100})
	if !errors.Is(err, ErrBelowMinLot) || !errors.As(err, &lotErr) {
		t.Fatalf("MinLotError no responde a Is/As: %v", err)
	}
}
