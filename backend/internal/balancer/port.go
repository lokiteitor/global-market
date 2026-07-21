package balancer

import (
	"context"

	"github.com/google/uuid"

	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// PublicationCreator es el PORT por el que el Balancer publica las solicitudes
// de compra de las ciudades. Lo define el Balancer (dependencia dirigida
// balancer → contracts como CLIENTE, GDD 18.1) y lo implementa el composition
// root (cmd/engine) con internal/contracts.CreatePublication: la buy de ciudad
// pasa por la MISMA validación estándar, bloqueo de escrow y ventana de sorteo
// que cualquier otra publicación del tablón — sin canal privilegiado.
//
// El Balancer PRE-FONDEA la caja de la ciudad (buys.go) ANTES de invocar el
// port, de modo que CreateCityBuy siempre encuentra caja suficiente para el
// escrow (una ciudad nunca incumple el pago, GDD 5.6). El port no mueve dinero
// nuevo: solo traslada la caja de la ciudad a escrow por el camino estándar.
type PublicationCreator interface {
	// CreateCityBuy publica una solicitud de compra (kind=buy, canal board) de la
	// ciudad by.CityAccountID por by.Quantity unidades de by.ProductID al precio
	// by.UnitPrice, con entrega en by.DestinationNodeID (el centro de distribución
	// de la ciudad) y plazo by.DeliverySimSeconds. Devuelve error si la
	// publicación no se pudo asentar (p. ej. escrow insuficiente pese al
	// pre-fondeo, o nodo inexistente).
	CreateCityBuy(ctx context.Context, by CityBuy) error
}

// CityBuy son los parámetros de una solicitud de compra de ciudad para el PORT.
// Cantidad y precio en int64 de punto fijo.
type CityBuy struct {
	// CityAccountID es la cuenta de mercado de la ciudad (publicador de la buy).
	CityAccountID uuid.UUID
	// ProductID es el producto que la ciudad demanda.
	ProductID uuid.UUID
	// Quantity es la cantidad objetivo de la solicitud (> 0).
	Quantity int64
	// UnitPrice es el precio efectivo de la curva de demanda, ya clampado en
	// [price_floor, price_ceiling] (> 0).
	UnitPrice int64
	// DestinationNodeID es el nodo del grafo del centro de distribución de la
	// ciudad (destino de la entrega urbana).
	DestinationNodeID uuid.UUID
	// DeliverySimSeconds es el plazo de entrega pactado, en sim-time (> 0).
	DeliverySimSeconds simtime.SimTime
}
