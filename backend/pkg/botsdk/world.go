package botsdk

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
)

// ── Catálogos y mundo estático ──

// RegionsQuery filtra GET /world/regions.
type RegionsQuery struct {
	Biome Biome
	PageQuery
}

// values serializa la query.
func (q RegionsQuery) values() url.Values {
	v := url.Values{}
	if q.Biome != "" {
		v.Set("biome", string(q.Biome))
	}
	q.apply(v)
	return v
}

// Regions devuelve las macro-regiones del mundo (GET /world/regions).
func (c *Client) Regions(ctx context.Context, q RegionsQuery) (Page[Region], error) {
	return getList[Region](ctx, c, "/world/regions", q.values())
}

// GetRegion devuelve una región (GET /world/regions/{id}).
func (c *Client) GetRegion(ctx context.Context, regionID string) (Region, error) {
	return getOne[Region](ctx, c, "/world/regions/"+pathID(regionID), nil)
}

// ProductsQuery filtra GET /world/products.
type ProductsQuery struct {
	Class  ProductClass
	IsFuel *bool
	PageQuery
}

// values serializa la query.
func (q ProductsQuery) values() url.Values {
	v := url.Values{}
	if q.Class != "" {
		v.Set("class", string(q.Class))
	}
	if q.IsFuel != nil {
		v.Set("is_fuel", strconv.FormatBool(*q.IsFuel))
	}
	q.apply(v)
	return v
}

// Products devuelve el catálogo de productos (GET /world/products).
func (c *Client) Products(ctx context.Context, q ProductsQuery) (Page[Product], error) {
	return getList[Product](ctx, c, "/world/products", q.values())
}

// BuildingTypesQuery pagina GET /world/building-types.
type BuildingTypesQuery struct {
	PageQuery
}

// values serializa la query.
func (q BuildingTypesQuery) values() url.Values {
	v := url.Values{}
	q.apply(v)
	return v
}

// BuildingTypes devuelve el catálogo de tipos de edificio
// (GET /world/building-types).
func (c *Client) BuildingTypes(ctx context.Context, q BuildingTypesQuery) (Page[BuildingType], error) {
	return getList[BuildingType](ctx, c, "/world/building-types", q.values())
}

// RecipesQuery filtra GET /world/recipes.
type RecipesQuery struct {
	BuildingTypeID string
	// ProductID filtra recetas que producen o consumen este producto.
	ProductID string
	PageQuery
}

// values serializa la query.
func (q RecipesQuery) values() url.Values {
	v := url.Values{}
	if q.BuildingTypeID != "" {
		v.Set("building_type_id", q.BuildingTypeID)
	}
	if q.ProductID != "" {
		v.Set("product_id", q.ProductID)
	}
	q.apply(v)
	return v
}

// Recipes devuelve el catálogo de recetas (GET /world/recipes).
func (c *Client) Recipes(ctx context.Context, q RecipesQuery) (Page[Recipe], error) {
	return getList[Recipe](ctx, c, "/world/recipes", q.values())
}

// ResourceDepositsQuery filtra GET /world/resource-deposits.
type ResourceDepositsQuery struct {
	RegionID  string
	ProductID string
	// OnlyAvailable filtra yacimientos con cantidad restante > 0
	// (nil = default del servidor: true).
	OnlyAvailable *bool
	PageQuery
}

// values serializa la query.
func (q ResourceDepositsQuery) values() url.Values {
	v := url.Values{}
	if q.RegionID != "" {
		v.Set("region_id", q.RegionID)
	}
	if q.ProductID != "" {
		v.Set("product_id", q.ProductID)
	}
	if q.OnlyAvailable != nil {
		v.Set("only_available", strconv.FormatBool(*q.OnlyAvailable))
	}
	q.apply(v)
	return v
}

// ResourceDeposits devuelve los yacimientos de recursos naturales
// (GET /world/resource-deposits).
func (c *Client) ResourceDeposits(ctx context.Context, q ResourceDepositsQuery) (Page[ResourceDeposit], error) {
	return getList[ResourceDeposit](ctx, c, "/world/resource-deposits", q.values())
}

// CitiesQuery filtra GET /world/cities.
type CitiesQuery struct {
	RegionID string
	MinLevel int
	PageQuery
}

// values serializa la query.
func (q CitiesQuery) values() url.Values {
	v := url.Values{}
	if q.RegionID != "" {
		v.Set("region_id", q.RegionID)
	}
	if q.MinLevel > 0 {
		v.Set("min_level", strconv.Itoa(q.MinLevel))
	}
	q.apply(v)
	return v
}

// Cities devuelve las ciudades del mundo — el único consumidor final
// (GET /world/cities).
func (c *Client) Cities(ctx context.Context, q CitiesQuery) (Page[City], error) {
	return getList[City](ctx, c, "/world/cities", q.values())
}

// GetCity devuelve una ciudad (GET /world/cities/{id}).
func (c *Client) GetCity(ctx context.Context, cityID string) (City, error) {
	return getOne[City](ctx, c, "/world/cities/"+pathID(cityID), nil)
}

// CityDemand devuelve las curvas de demanda activas de una ciudad
// (GET /world/cities/{id}/demand). productID vacío = todos los productos.
func (c *Client) CityDemand(ctx context.Context, cityID, productID string) ([]CityDemand, error) {
	v := url.Values{}
	if productID != "" {
		v.Set("product_id", productID)
	}
	var out []CityDemand
	_, err := c.do(ctx, http.MethodGet, "/world/cities/"+pathID(cityID)+"/demand", v, nil, &out)
	return out, err
}

// ── Concesiones de suelo ──

// ConcessionsQuery filtra GET /world/concessions.
type ConcessionsQuery struct {
	Status   ConcessionStatus
	RegionID string
	PageQuery
}

// values serializa la query.
func (q ConcessionsQuery) values() url.Values {
	v := url.Values{}
	if q.Status != "" {
		v.Set("status", string(q.Status))
	}
	if q.RegionID != "" {
		v.Set("region_id", q.RegionID)
	}
	q.apply(v)
	return v
}

// ListConcessions devuelve las concesiones de suelo propias
// (GET /world/concessions).
func (c *Client) ListConcessions(ctx context.Context, q ConcessionsQuery) (Page[Concession], error) {
	return getList[Concession](ctx, c, "/world/concessions", q.values())
}

// CreateConcession obtiene una concesión de suelo del sistema; el primer
// canon se cobra al conceder (POST /world/concessions).
func (c *Client) CreateConcession(ctx context.Context, in ConcessionCreate) (Concession, error) {
	return mutate[Concession](ctx, c, http.MethodPost, "/world/concessions", in)
}

// GetConcession devuelve una concesión (GET /world/concessions/{id}).
func (c *Client) GetConcession(ctx context.Context, concessionID string) (Concession, error) {
	return getOne[Concession](ctx, c, "/world/concessions/"+pathID(concessionID), nil)
}

// RenewConcession extiende la concesión otro periodo pagando el canon vigente
// (POST /world/concessions/{id}/renew).
func (c *Client) RenewConcession(ctx context.Context, concessionID string) (Concession, error) {
	return mutate[Concession](ctx, c, http.MethodPost, "/world/concessions/"+pathID(concessionID)+"/renew", nil)
}

// TransferConcession traspasa una concesión a otra corporación; el sistema
// cobra una tasa (POST /world/concession-transfers).
func (c *Client) TransferConcession(ctx context.Context, in ConcessionTransferCreate) (ConcessionTransfer, error) {
	return mutate[ConcessionTransfer](ctx, c, http.MethodPost, "/world/concession-transfers", in)
}

// ── Edificios ──

// BuildingsQuery filtra GET /world/buildings.
type BuildingsQuery struct {
	RegionID       string
	Status         BuildingStatus
	BuildingTypeID string
	PageQuery
}

// values serializa la query.
func (q BuildingsQuery) values() url.Values {
	v := url.Values{}
	if q.RegionID != "" {
		v.Set("region_id", q.RegionID)
	}
	if q.Status != "" {
		v.Set("status", string(q.Status))
	}
	if q.BuildingTypeID != "" {
		v.Set("building_type_id", q.BuildingTypeID)
	}
	q.apply(v)
	return v
}

// ListBuildings devuelve los edificios propios (GET /world/buildings).
func (c *Client) ListBuildings(ctx context.Context, q BuildingsQuery) (Page[Building], error) {
	return getList[Building](ctx, c, "/world/buildings", q.values())
}

// CreateBuilding inicia la construcción sobre una concesión propia
// (POST /world/buildings; 422 PLACEMENT_INVALID si el emplazamiento no cumple).
func (c *Client) CreateBuilding(ctx context.Context, in BuildingCreate) (Building, error) {
	return mutate[Building](ctx, c, http.MethodPost, "/world/buildings", in)
}

// GetBuilding devuelve un edificio (GET /world/buildings/{id}).
func (c *Client) GetBuilding(ctx context.Context, buildingID string) (Building, error) {
	return getOne[Building](ctx, c, "/world/buildings/"+pathID(buildingID), nil)
}

// UpdateBuilding configura un edificio: cambia la receta activa o inicia
// mantenimiento (PATCH /world/buildings/{id}).
func (c *Client) UpdateBuilding(ctx context.Context, buildingID string, in BuildingUpdate) (Building, error) {
	return mutate[Building](ctx, c, http.MethodPatch, "/world/buildings/"+pathID(buildingID), in)
}

// UpgradeBuilding sube el nivel del edificio con coste según la level_curve
// (POST /world/buildings/{id}/upgrade).
func (c *Client) UpgradeBuilding(ctx context.Context, buildingID string) (Building, error) {
	return mutate[Building](ctx, c, http.MethodPost, "/world/buildings/"+pathID(buildingID)+"/upgrade", nil)
}

// BuildingInventory devuelve la vista física del stock por producto
// (GET /world/buildings/{id}/inventory). La partición libre/reservado vive en
// el ledger (ListAccounts).
func (c *Client) BuildingInventory(ctx context.Context, buildingID string) ([]InventoryItem, error) {
	var out []InventoryItem
	_, err := c.do(ctx, http.MethodGet, "/world/buildings/"+pathID(buildingID)+"/inventory", nil, nil, &out)
	return out, err
}

// ── Producción ──

// ProductionBatchesQuery filtra GET /world/buildings/{id}/production-batches.
type ProductionBatchesQuery struct {
	Status BatchStatus
	PageQuery
}

// values serializa la query.
func (q ProductionBatchesQuery) values() url.Values {
	v := url.Values{}
	if q.Status != "" {
		v.Set("status", string(q.Status))
	}
	q.apply(v)
	return v
}

// ListProductionBatches devuelve la cola de producción de un edificio
// (GET /world/buildings/{id}/production-batches).
func (c *Client) ListProductionBatches(ctx context.Context, buildingID string, q ProductionBatchesQuery) (Page[ProductionBatch], error) {
	return getList[ProductionBatch](ctx, c, "/world/buildings/"+pathID(buildingID)+"/production-batches", q.values())
}

// QueueProduction encola lotes de una receta soportada por el edificio
// (POST /world/buildings/{id}/production-batches).
func (c *Client) QueueProduction(ctx context.Context, buildingID string, in ProductionBatchCreate) (ProductionBatch, error) {
	return mutate[ProductionBatch](ctx, c, http.MethodPost, "/world/buildings/"+pathID(buildingID)+"/production-batches", in)
}

// CancelProductionBatch cancela los lotes aún no producidos de una orden; lo
// ya producido queda asentado (DELETE /world/production-batches/{id}).
func (c *Client) CancelProductionBatch(ctx context.Context, batchID string) (ProductionBatch, error) {
	return mutate[ProductionBatch](ctx, c, http.MethodDelete, "/world/production-batches/"+pathID(batchID), nil)
}
