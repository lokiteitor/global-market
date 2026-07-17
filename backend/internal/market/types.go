package market

import (
	"github.com/google/uuid"

	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// Candle es una vela OHLC del historial de precios (GDD 5.2), tal y como está
// almacenada en analytics.market_ohlc: agregado por producto, región de
// destino y bucket de sim-time. Dinero y stock son int64 de punto fijo (string
// en el JSON del contrato, jamás float).
type Candle struct {
	// ProductID es el producto de la vela.
	ProductID uuid.UUID
	// RegionID es la región de DESTINO del contrato liquidado (el payload de
	// contract.settled la nombra destination_region_id).
	RegionID uuid.UUID
	// BucketStartSim es el inicio del bucket en sim-time (segundos desde el
	// génesis): floor(settled_at_sim / bucket) * bucket.
	BucketStartSim simtime.SimTime
	// BucketSimSecs es el tamaño del bucket con el que se agregó esta vela: la
	// granularidad REALMENTE almacenada, no la que pudiera pedir el cliente.
	BucketSimSecs int64
	// OpenPrice es el precio de la primera entrega del bucket (por seq).
	OpenPrice int64
	// HighPrice es el precio máximo de las entregas del bucket.
	HighPrice int64
	// LowPrice es el precio mínimo de las entregas del bucket.
	LowPrice int64
	// ClosePrice es el precio de la última entrega del bucket (por seq).
	ClosePrice int64
	// Volume es la suma de cantidades entregadas del bucket.
	Volume int64
	// ContractCount es el número de contratos liquidados que forman la vela.
	ContractCount int32
}

// CandleFilter son los filtros de la consulta del historial OHLC (contrato
// GET /market/ohlc). ProductID es obligatorio; el resto, opcional.
type CandleFilter struct {
	// ProductID acota la serie a un producto (obligatorio).
	ProductID uuid.UUID
	// RegionID acota la serie a una región de destino (opcional).
	RegionID *uuid.UUID
	// FromSim y ToSim acotan el rango de bucket_start_sim (inclusive, opcional).
	FromSim *simtime.SimTime
	ToSim   *simtime.SimTime
	// Limit es el tamaño máximo de página del contrato (1..200; 0 = default 50).
	Limit int
}
