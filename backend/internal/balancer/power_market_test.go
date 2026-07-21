package balancer

import (
	"testing"

	"github.com/google/uuid"
)

// Tests de la casación PURA del mercado spot (GDD 5.8, ADR-025 §6): orden de
// mérito, precio de cierre uniforme, recorte por prioridad inversa de puja con
// rotación, y exclusión por insolvencia. Sin BD.

func pid(n byte) uuid.UUID {
	var b [16]byte
	b[6] = 0x70 // versión 7 nominal (irrelevante para la lógica)
	b[15] = n
	return uuid.UUID(b)
}

func offer(id byte, price, capacity int64) powerOffer {
	return powerOffer{BuildingID: pid(id), OwnerID: pid(0xF0 + id), Price: price, Capacity: capacity}
}

func bid(id byte, price, energy, cash int64) powerBid {
	return powerBid{BuildingID: pid(id), OwnerID: pid(0xA0 + id), Price: price, Energy: energy, OwnerCash: cash}
}

func TestClearSpotMarketMeritOrderAndUniformClosing(t *testing.T) {
	// Hidro barata (5/u, 6 u) + térmica cara (80/u, 10 u); demanda 10 u.
	// El mérito despacha 6 hidro + 4 térmica; el cierre es la oferta MARGINAL
	// (80) y lo pagan/cobran TODOS los despachados (no pay-as-bid).
	res := clearSpotMarket(
		[]powerOffer{offer(2, 80, 10), offer(1, 5, 6)},
		[]powerBid{bid(10, 200, 10, 1_000_000)},
	)
	if res.ClosingPrice != 80 {
		t.Fatalf("cierre = %d, esperado 80 (oferta marginal)", res.ClosingPrice)
	}
	if res.SuppliedUnits != 10 || res.CurtailedUnits != 0 {
		t.Fatalf("servido %d / recortado %d, esperado 10/0", res.SuppliedUnits, res.CurtailedUnits)
	}
	if len(res.Dispatch) != 2 {
		t.Fatalf("despachos = %d, esperado 2", len(res.Dispatch))
	}
	if res.Dispatch[0].BuildingID != pid(1) || res.Dispatch[0].Units != 6 || res.Dispatch[0].Revenue != 6*80 {
		t.Fatalf("la hidro debe despachar 6 u a 80: %+v", res.Dispatch[0])
	}
	if res.Dispatch[1].BuildingID != pid(2) || res.Dispatch[1].Units != 4 || res.Dispatch[1].Revenue != 4*80 {
		t.Fatalf("la térmica marginal debe despachar 4 u a 80: %+v", res.Dispatch[1])
	}
	if res.Served[0].Amount != 10*80 {
		t.Fatalf("el consumidor paga %d, esperado %d (energía × cierre)", res.Served[0].Amount, 10*80)
	}
	// Invariante del asiento power_spot: Σ pagos = Σ ingresos por activo dinero.
	var paid, earned int64
	for _, s := range res.Served {
		paid += s.Amount
	}
	for _, d := range res.Dispatch {
		earned += d.Revenue
	}
	if paid != earned {
		t.Fatalf("asiento desbalanceado: pagos %d != ingresos %d", paid, earned)
	}
}

func TestClearSpotMarketCurtailsLowestBidFirst(t *testing.T) {
	// Capacidad 10; dos consumidores de 10 con pujas distintas: se recorta al
	// que MENOS paga (GDD 5.8, prioridad inversa de precio).
	res := clearSpotMarket(
		[]powerOffer{offer(1, 10, 10)},
		[]powerBid{bid(10, 50, 10, 1_000_000), bid(11, 200, 10, 1_000_000)},
	)
	if res.SuppliedUnits != 10 || len(res.Unserved) != 1 {
		t.Fatalf("servido %d / no servidos %d, esperado 10/1", res.SuppliedUnits, len(res.Unserved))
	}
	if res.Unserved[0].BuildingID != pid(10) || res.Unserved[0].Reason != unservedCurtailed {
		t.Fatalf("debe recortarse la puja baja (edificio 10): %+v", res.Unserved[0])
	}
	if res.Served[0].BuildingID != pid(11) {
		t.Fatalf("debe servirse la puja alta (edificio 11): %+v", res.Served[0])
	}
	if res.ClosingPrice != 10 {
		t.Fatalf("cierre = %d, esperado 10", res.ClosingPrice)
	}
}

func TestClearSpotMarketRotatesAmongEqualBids(t *testing.T) {
	// Pujas IGUALES y capacidad para uno solo: entre iguales se sirve primero
	// al recortado MÁS reciente (rota el castigo, GDD 5.8).
	mk := func(lastA, lastB int64) (servedID, cutID uuid.UUID) {
		a := bid(10, 100, 10, 1_000_000)
		a.LastCurtailedSim = lastA
		b := bid(11, 100, 10, 1_000_000)
		b.LastCurtailedSim = lastB
		res := clearSpotMarket([]powerOffer{offer(1, 10, 10)}, []powerBid{a, b})
		if len(res.Served) != 1 || len(res.Unserved) != 1 {
			t.Fatalf("esperado 1 servido y 1 recortado: %+v", res)
		}
		return res.Served[0].BuildingID, res.Unserved[0].BuildingID
	}
	// A fue recortado en el tick anterior (marca mayor) → ahora se sirve A y
	// se recorta B; en el tick siguiente (marca de B mayor) la rotación invierte.
	if servedID, cutID := mk(1000, 0); servedID != pid(10) || cutID != pid(11) {
		t.Fatalf("tick 1: debe servirse el recortado reciente (10) y recortarse 11: servido %s, recortado %s", servedID, cutID)
	}
	if servedID, cutID := mk(1000, 2000); servedID != pid(11) || cutID != pid(10) {
		t.Fatalf("tick 2: la rotación debe invertir el recorte: servido %s, recortado %s", servedID, cutID)
	}
}

func TestClearSpotMarketExcludesInsolventOwners(t *testing.T) {
	// La caja del dueño no cubre puja × energía → excluido SIN deuda (GDD 5.9);
	// el presupuesto es por corporación: dos edificios del mismo dueño
	// comparten caja y solo entra el primero en prioridad.
	rich := bid(10, 100, 10, 1_000_000)
	poor := bid(11, 200, 10, 500) // puja alta pero caja insuficiente (200×10 > 500)
	res := clearSpotMarket([]powerOffer{offer(1, 10, 100)}, []powerBid{rich, poor})
	if len(res.Unserved) != 1 || res.Unserved[0].Reason != unservedInsolvent {
		t.Fatalf("el insolvente debe excluirse con reason insolvent: %+v", res.Unserved)
	}
	if len(res.Served) != 1 || res.Served[0].BuildingID != pid(10) {
		t.Fatalf("el solvente debe servirse: %+v", res.Served)
	}

	// Mismo dueño, presupuesto compartido: 2 edificios × (100×10) con caja 1500
	// → solo el primero en prioridad entra (el conservador usa puja, no cierre).
	a := powerBid{BuildingID: pid(20), OwnerID: pid(0xEE), Price: 100, Energy: 10, OwnerCash: 1_500}
	b := powerBid{BuildingID: pid(21), OwnerID: pid(0xEE), Price: 100, Energy: 10, OwnerCash: 1_500}
	res = clearSpotMarket([]powerOffer{offer(1, 10, 100)}, []powerBid{a, b})
	if len(res.Served) != 1 || len(res.Unserved) != 1 || res.Unserved[0].Reason != unservedInsolvent {
		t.Fatalf("presupuesto por dueño: esperado 1 servido + 1 insolvente: %+v", res)
	}
}

func TestClearSpotMarketContinuesAfterBigBlock(t *testing.T) {
	// Un bloque grande no cabe pero uno pequeño posterior sí (se maximiza la
	// demanda servida dentro del orden de mérito).
	big := bid(10, 200, 100, 1_000_000) // no cabe (capacidad 10)
	small := bid(11, 100, 5, 1_000_000) // cabe tras saltar al grande
	res := clearSpotMarket([]powerOffer{offer(1, 10, 10)}, []powerBid{big, small})
	if len(res.Served) != 1 || res.Served[0].BuildingID != pid(11) {
		t.Fatalf("el bloque pequeño debe servirse tras saltar el grande: %+v", res.Served)
	}
	if res.Unserved[0].BuildingID != pid(10) || res.Unserved[0].Reason != unservedCurtailed {
		t.Fatalf("el grande queda recortado: %+v", res.Unserved)
	}
}

func TestClearSpotMarketPriceCapByBid(t *testing.T) {
	// La oferta marginal necesaria supera la puja → no se sirve aunque haya
	// capacidad (el cierre nunca supera la puja de un servido).
	res := clearSpotMarket(
		[]powerOffer{offer(1, 50, 5), offer(2, 500, 100)},
		[]powerBid{bid(10, 100, 10, 1_000_000)},
	)
	if res.SuppliedUnits != 0 || len(res.Unserved) != 1 {
		t.Fatalf("no debe servirse por precio (marginal 500 > puja 100): %+v", res)
	}
	if res.ClosingPrice != 0 {
		t.Fatalf("sin despacho el cierre es 0: %d", res.ClosingPrice)
	}
}

func TestClearSpotMarketNoOffers(t *testing.T) {
	res := clearSpotMarket(nil, []powerBid{bid(10, 100, 10, 1_000_000)})
	if res.SuppliedUnits != 0 || res.CurtailedUnits != 10 || len(res.Unserved) != 1 {
		t.Fatalf("sin oferta todo se recorta: %+v", res)
	}
}

func TestPowerCapacityForLevel(t *testing.T) {
	curve := []byte(`{"capacity_mult":[1,2,3,4]}`)
	if got := powerCapacityForLevel(10, curve, 3); got != 30 {
		t.Fatalf("capacity nivel 3 = %d, esperado 30", got)
	}
	// Default sin curva: multiplica por el propio nivel (coherente con v1.3).
	if got := powerCapacityForLevel(10, nil, 2); got != 20 {
		t.Fatalf("capacity default nivel 2 = %d, esperado 20", got)
	}
	// Índice fuera de la curva → default.
	if got := powerCapacityForLevel(10, curve, 6); got != 60 {
		t.Fatalf("capacity nivel 6 (fuera de curva) = %d, esperado 60", got)
	}
}
