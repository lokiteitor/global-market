package botsdk

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// ── Tablón ──

// BoardQuery filtra GET /contracts/board (pull con filtros; nunca hay push
// mundial de mercado).
type BoardQuery struct {
	Kind                  PublicationKind
	ProductID             string
	OriginRegionID        string
	DestinationRegionID   string
	MaxUnitPrice          Money
	MinUnitPrice          Money
	MinQuantityRemaining  Qty
	MaxDeliverySimSeconds SimTime
	// Sort es uno de los SortXxx (default del servidor: unit_price_asc).
	Sort string
	PageQuery
}

// values serializa la query.
func (q BoardQuery) values() url.Values {
	v := url.Values{}
	if q.Kind != "" {
		v.Set("kind", string(q.Kind))
	}
	if q.ProductID != "" {
		v.Set("product_id", q.ProductID)
	}
	if q.OriginRegionID != "" {
		v.Set("origin_region_id", q.OriginRegionID)
	}
	if q.DestinationRegionID != "" {
		v.Set("destination_region_id", q.DestinationRegionID)
	}
	if q.MaxUnitPrice != "" {
		v.Set("max_unit_price", string(q.MaxUnitPrice))
	}
	if q.MinUnitPrice != "" {
		v.Set("min_unit_price", string(q.MinUnitPrice))
	}
	if q.MinQuantityRemaining != "" {
		v.Set("min_quantity_remaining", string(q.MinQuantityRemaining))
	}
	if q.MaxDeliverySimSeconds > 0 {
		v.Set("max_delivery_sim_seconds", strconv.FormatInt(q.MaxDeliverySimSeconds, 10))
	}
	if q.Sort != "" {
		v.Set("sort", q.Sort)
	}
	q.apply(v)
	return v
}

// Board consulta el tablón global único e interregional (GET /contracts/board).
// Toda publicación visible es ejecutable al 100%.
func (c *Client) Board(ctx context.Context, q BoardQuery) (Page[Publication], error) {
	return getList[Publication](ctx, c, "/contracts/board", q.values())
}

// CreatePublication publica en el tablón (o negocia en privado) bloqueando la
// garantía íntegra en el mismo acto (POST /contracts/publications).
func (c *Client) CreatePublication(ctx context.Context, in PublicationCreate) (Publication, error) {
	return mutate[Publication](ctx, c, http.MethodPost, "/contracts/publications", in)
}

// GetPublication devuelve el detalle de una publicación
// (GET /contracts/publications/{id}).
func (c *Client) GetPublication(ctx context.Context, publicationID string) (Publication, error) {
	return getOne[Publication](ctx, c, "/contracts/publications/"+pathID(publicationID), nil)
}

// CancelPublication cancela la cantidad restante de una publicación y libera
// su garantía, respetando el cooldown anti-parpadeo
// (DELETE /contracts/publications/{id}; 409 CANCEL_COOLDOWN_ACTIVE).
func (c *Client) CancelPublication(ctx context.Context, publicationID string) (Publication, error) {
	return mutate[Publication](ctx, c, http.MethodDelete, "/contracts/publications/"+pathID(publicationID), nil)
}

// Accept registra una aceptación en la ventana de sorteo
// (POST /contracts/publications/{id}/acceptances). originNodeID es requerido
// al aceptar publicaciones buy (almacén propio del que sale el stock); pásalo
// vacío en publicaciones sell (entrega in situ).
func (c *Client) Accept(ctx context.Context, publicationID string, quantity Qty, originNodeID string) (Acceptance, error) {
	in := AcceptanceCreate{Quantity: quantity, OriginNodeID: originNodeID}
	return mutate[Acceptance](ctx, c, http.MethodPost, "/contracts/publications/"+pathID(publicationID)+"/acceptances", in)
}

// GetAcceptance devuelve el resultado de una aceptación tras el sorteo
// (GET /contracts/acceptances/{id}): served (con contrato) o released.
func (c *Client) GetAcceptance(ctx context.Context, acceptanceID string) (Acceptance, error) {
	return getOne[Acceptance](ctx, c, "/contracts/acceptances/"+pathID(acceptanceID), nil)
}

// ── Contratos CCRI ──

// ContractsQuery filtra GET /contracts/contracts.
type ContractsQuery struct {
	// Role es "buyer" o "seller" (vacío = ambos).
	Role      string
	Status    ContractStatus
	ProductID string
	PageQuery
}

// values serializa la query.
func (q ContractsQuery) values() url.Values {
	v := url.Values{}
	if q.Role != "" {
		v.Set("role", q.Role)
	}
	if q.Status != "" {
		v.Set("status", string(q.Status))
	}
	if q.ProductID != "" {
		v.Set("product_id", q.ProductID)
	}
	q.apply(v)
	return v
}

// ListContracts devuelve los contratos CCRI en los que la corporación es
// compradora o vendedora (GET /contracts/contracts).
func (c *Client) ListContracts(ctx context.Context, q ContractsQuery) (Page[Contract], error) {
	return getList[Contract](ctx, c, "/contracts/contracts", q.values())
}

// GetContract devuelve un contrato con sus cuentas espejo del bloqueo triple
// (GET /contracts/contracts/{id}).
func (c *Client) GetContract(ctx context.Context, contractID string) (Contract, error) {
	return getOne[Contract](ctx, c, "/contracts/contracts/"+pathID(contractID), nil)
}

// ListDeliveries devuelve las entregas parciales confirmadas de un contrato
// (GET /contracts/contracts/{id}/deliveries).
func (c *Client) ListDeliveries(ctx context.Context, contractID string) ([]ContractDelivery, error) {
	var out []ContractDelivery
	_, err := c.do(ctx, http.MethodGet, "/contracts/contracts/"+pathID(contractID)+"/deliveries", nil, nil, &out)
	return out, err
}

// ── Contratos CCRI-Flete (GDD 5.3.2) ──

// Rol de una parte en un CCRI-Flete (query param role de
// /contracts/freight-contracts).
const (
	RoleShipper = "shipper"
	RoleCarrier = "carrier"
)

// FreightContractsQuery filtra GET /contracts/freight-contracts.
type FreightContractsQuery struct {
	// Role es RoleShipper (cargador) o RoleCarrier (transportista); vacío =
	// ambos.
	Role   string
	Status ContractStatus
	PageQuery
}

// values serializa la query.
func (q FreightContractsQuery) values() url.Values {
	v := url.Values{}
	if q.Role != "" {
		v.Set("role", q.Role)
	}
	if q.Status != "" {
		v.Set("status", string(q.Status))
	}
	q.apply(v)
	return v
}

// ListFreightContracts devuelve los contratos de flete en los que la
// corporación es cargadora o transportista (GET /contracts/freight-contracts).
// La carga viaja en la cuenta custody del contrato: el transportista la lleva
// físicamente pero el ledger le impide venderla.
func (c *Client) ListFreightContracts(ctx context.Context, q FreightContractsQuery) (Page[FreightContract], error) {
	return getList[FreightContract](ctx, c, "/contracts/freight-contracts", q.values())
}

// GetFreightContract devuelve un contrato de flete propio
// (GET /contracts/freight-contracts/{id}; 403 si no se es parte).
func (c *Client) GetFreightContract(ctx context.Context, freightContractID string) (FreightContract, error) {
	return getOne[FreightContract](ctx, c, "/contracts/freight-contracts/"+pathID(freightContractID), nil)
}

// ── Historial de mercado ──

// OhlcQuery parametriza GET /market/ohlc. ProductID es obligatorio.
type OhlcQuery struct {
	ProductID string
	RegionID  string
	// BucketSimSecs es el tamaño del bucket en sim-time (0 = default 3600).
	BucketSimSecs int64
	FromSim       SimTime
	ToSim         SimTime
	// Limit es el máximo de velas (0 = default del servidor).
	Limit int
}

// values serializa la query.
func (q OhlcQuery) values() url.Values {
	v := url.Values{}
	v.Set("product_id", q.ProductID)
	if q.RegionID != "" {
		v.Set("region_id", q.RegionID)
	}
	if q.BucketSimSecs > 0 {
		v.Set("bucket_sim_secs", strconv.FormatInt(q.BucketSimSecs, 10))
	}
	if q.FromSim > 0 {
		v.Set("from_sim", strconv.FormatInt(q.FromSim, 10))
	}
	if q.ToSim > 0 {
		v.Set("to_sim", strconv.FormatInt(q.ToSim, 10))
	}
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	return v
}

// OHLC devuelve velas construidas a partir de contratos efectivamente
// liquidados (GET /market/ohlc) — la referencia de precio de mercado.
func (c *Client) OHLC(ctx context.Context, q OhlcQuery) ([]OhlcCandle, error) {
	var out []OhlcCandle
	_, err := c.do(ctx, http.MethodGet, "/market/ohlc", q.values(), nil, &out)
	return out, err
}
