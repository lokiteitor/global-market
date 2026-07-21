package balancer

import (
	"cmp"
	"slices"

	"github.com/google/uuid"
)

// Casación PURA del mercado spot eléctrico regional (GDD 5.8, ADR-025 §6):
// orden de mérito con precio de cierre uniforme (no pay-as-bid), recorte por
// prioridad inversa de puja con rotación entre pujas iguales, y exclusión por
// insolvencia (presupuesto de caja por corporación, GDD 5.9: sin compra, sin
// deuda). Sin acceso a BD: testeable en aislamiento.

// powerOffer es un bloque de oferta de una central (divisible).
type powerOffer struct {
	BuildingID uuid.UUID
	OwnerID    uuid.UUID
	Price      int64 // oferta por unidad de energía (> 0)
	Capacity   int64 // unidades ofertables este tick (nivel + límite de combustible)
}

// powerBid es un bloque de demanda de un edificio (todo-o-nada: un edificio a
// medias no produce).
type powerBid struct {
	BuildingID       uuid.UUID
	OwnerID          uuid.UUID
	Price            int64 // puja máxima por unidad (explícita o default)
	Energy           int64 // unidades necesarias este tick (> 0)
	LastCurtailedSim int64 // marcador de rotación del recorte
	OwnerCash        int64 // caja de la corporación (presupuesto conjunto)
}

// Motivos de no-servicio de un consumidor.
const (
	unservedCurtailed = "curtailed" // recorte: oferta insuficiente a un precio <= su puja
	unservedInsolvent = "insolvent" // la caja no cubre su pago máximo posible (puja × energía)
)

// servedConsumer es un consumidor servido al precio de cierre.
type servedConsumer struct {
	BuildingID uuid.UUID
	OwnerID    uuid.UUID
	Energy     int64
	Amount     int64 // Energy × ClosingPrice
}

// unservedConsumer es un consumidor sin suministro este tick.
type unservedConsumer struct {
	BuildingID uuid.UUID
	OwnerID    uuid.UUID
	Energy     int64
	Reason     string
}

// generatorDispatch es el despacho de una central al precio de cierre.
type generatorDispatch struct {
	BuildingID uuid.UUID
	OwnerID    uuid.UUID
	Units      int64
	Revenue    int64 // Units × ClosingPrice
}

// clearingResult es el resultado completo de un tick.
type clearingResult struct {
	ClosingPrice   int64 // oferta del generador marginal despachado (0 = sin despacho)
	DemandUnits    int64 // Σ energía de todos los candidatos
	SuppliedUnits  int64 // Σ energía servida (= Σ despacho, invariante del asiento)
	CurtailedUnits int64 // demanda no servida (recorte + insolvencia)
	Served         []servedConsumer
	Unserved       []unservedConsumer
	Dispatch       []generatorDispatch
}

// clearSpotMarket casa oferta y demanda:
//
//  1. Oferta por orden de mérito (precio asc; id como desempate determinista).
//  2. Demanda por puja desc; entre pujas iguales, el recortado MÁS reciente se
//     sirve primero (rota el castigo, GDD 5.8); id como desempate.
//  3. Un consumidor se sirve entero si la oferta acumulada necesaria tiene un
//     precio marginal <= su puja Y el presupuesto de su corporación cubre su
//     pago máximo (puja × energía, conservador: el cierre nunca la supera).
//     La pasada continúa tras un no-servido: un bloque pequeño posterior puede
//     caber donde uno grande no cupo (se maximiza la demanda servida dentro
//     del orden de mérito).
//  4. Precio de cierre = oferta del bloque que sirve la ÚLTIMA unidad
//     despachada; lo pagan todos los servidos y lo cobran todos los
//     despachados (uniforme). Por construcción cierre <= puja de todo servido.
func clearSpotMarket(offers []powerOffer, bids []powerBid) clearingResult {
	res := clearingResult{}

	offers = slices.Clone(offers)
	offers = slices.DeleteFunc(offers, func(o powerOffer) bool { return o.Capacity <= 0 || o.Price <= 0 })
	slices.SortFunc(offers, func(a, b powerOffer) int {
		if c := cmp.Compare(a.Price, b.Price); c != 0 {
			return c
		}
		return cmp.Compare(a.BuildingID.String(), b.BuildingID.String())
	})

	bids = slices.Clone(bids)
	slices.SortFunc(bids, func(a, b powerBid) int {
		if c := cmp.Compare(b.Price, a.Price); c != 0 { // puja DESC
			return c
		}
		if c := cmp.Compare(b.LastCurtailedSim, a.LastCurtailedSim); c != 0 { // recortado reciente primero
			return c
		}
		return cmp.Compare(a.BuildingID.String(), b.BuildingID.String())
	})

	// Prefijos de capacidad para localizar el precio marginal de la unidad N.
	totalCapacity := int64(0)
	cumCapacity := make([]int64, len(offers))
	for i, o := range offers {
		totalCapacity += o.Capacity
		cumCapacity[i] = totalCapacity
	}
	// marginalPrice devuelve la oferta del bloque que contiene la unidad n (1-based).
	marginalPrice := func(n int64) int64 {
		idx, _ := slices.BinarySearchFunc(cumCapacity, n, func(c, target int64) int {
			return cmp.Compare(c, target)
		})
		// BinarySearch da la primera posición con cum >= n.
		if idx >= len(offers) {
			return 0 // inalcanzable (guardado por el caller)
		}
		return offers[idx].Price
	}

	budget := make(map[uuid.UUID]int64, len(bids))
	for _, b := range bids {
		if _, ok := budget[b.OwnerID]; !ok {
			budget[b.OwnerID] = b.OwnerCash
		}
	}

	var servedUnits int64
	closing := int64(0)
	for _, b := range bids {
		res.DemandUnits += b.Energy
		maxPay, errOverflow := mulOverflow(b.Price, b.Energy)
		if errOverflow != nil || budget[b.OwnerID] < maxPay {
			res.Unserved = append(res.Unserved, unservedConsumer{
				BuildingID: b.BuildingID, OwnerID: b.OwnerID, Energy: b.Energy, Reason: unservedInsolvent,
			})
			res.CurtailedUnits += b.Energy
			continue
		}
		need := servedUnits + b.Energy
		if need > totalCapacity || marginalPrice(need) > b.Price {
			res.Unserved = append(res.Unserved, unservedConsumer{
				BuildingID: b.BuildingID, OwnerID: b.OwnerID, Energy: b.Energy, Reason: unservedCurtailed,
			})
			res.CurtailedUnits += b.Energy
			continue
		}
		budget[b.OwnerID] -= maxPay
		servedUnits = need
		if p := marginalPrice(need); p > closing {
			closing = p
		}
		res.Served = append(res.Served, servedConsumer{
			BuildingID: b.BuildingID, OwnerID: b.OwnerID, Energy: b.Energy,
		})
	}
	res.SuppliedUnits = servedUnits
	if servedUnits == 0 {
		return res
	}
	res.ClosingPrice = closing

	// Importes al precio de cierre uniforme (exactos: enteros × enteros).
	for i := range res.Served {
		res.Served[i].Amount = res.Served[i].Energy * closing
	}

	// Despacho por orden de mérito hasta cubrir la energía servida (los
	// generadores son divisibles; el marginal despacha parcial).
	remaining := servedUnits
	for _, o := range offers {
		if remaining <= 0 {
			break
		}
		units := min(o.Capacity, remaining)
		remaining -= units
		res.Dispatch = append(res.Dispatch, generatorDispatch{
			BuildingID: o.BuildingID, OwnerID: o.OwnerID, Units: units, Revenue: units * closing,
		})
	}
	return res
}
