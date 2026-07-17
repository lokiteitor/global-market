package catalog

import "github.com/google/uuid"

// Tipos de dominio del subpaquete de catálogos. Son la superficie pública del
// contexto world (independiente del código generado por sqlc): dinero y stock
// como int64 de punto fijo, geometrías como GeoJSON plano (string), números del
// contrato como float64 y JSONB como bytes crudos que el handler embebe.

// Region es una macro-región del mundo (schema Region del contrato).
type Region struct {
	ID            uuid.UUID
	Name          string
	GridX         int32
	GridY         int32
	Bounds        string // GeoJSON Polygon plano (SRID 0)
	Biome         string
	TaxRateBp     int32
	CustomsRateBp int32
	CanonBase     int64
	OpenedAtSim   int64
}

// Product es un bien del catálogo (schema Product del contrato).
type Product struct {
	ID           uuid.UUID
	Code         string
	Name         string
	Class        string
	UnitVolume   int32
	BasePrice    int64
	PriceFloor   int64
	PriceCeiling int64
	IsFuel       bool
}

// BuildingType es un tipo de edificio construible (schema BuildingType).
type BuildingType struct {
	ID              uuid.UUID
	Code            string
	Name            string
	FootprintCells  int32
	MaxLevel        int32
	BaseStorage     int64
	PlacementRules  []byte // JSONB crudo (objeto)
	LevelCurve      []byte // JSONB crudo (objeto)
	BuildCost       int64
	MaintenanceCost int64
}

// RecipeIngredient es un insumo (input) o producto (output) de una receta.
type RecipeIngredient struct {
	ProductID uuid.UUID
	Role      string // input | output
	Quantity  int64
}

// Recipe es una receta de producción (schema Recipe), con sus ingredientes.
type Recipe struct {
	ID                uuid.UUID
	BuildingTypeID    uuid.UUID
	Code              string
	Name              string
	BatchSimSeconds   int64
	FuelProductID     *uuid.UUID
	FuelPerBatch      int64
	WorkersRequired   int32
	MinCityLevel      int32
	ChangeoverSeconds int64
	Ingredients       []RecipeIngredient
}

// ResourceDeposit es un yacimiento de recursos (schema ResourceDeposit).
type ResourceDeposit struct {
	ID              uuid.UUID
	RegionID        uuid.UUID
	ProductID       uuid.UUID
	Location        string // GeoJSON Point plano (SRID 0)
	InitialAmount   int64
	RemainingAmount int64
	Renewable       bool
	RegenPerSimDay  int64
}

// City es una ciudad — único consumidor final (schema City).
type City struct {
	ID               uuid.UUID
	RegionID         uuid.UUID
	AccountID        uuid.UUID
	Name             string
	Location         string // GeoJSON Point plano (SRID 0)
	Level            int32
	Population       int64
	SupplyIndex      float64
	InfluenceRadiusM int32
	BaseSalary       int64
}

// CityDemand es una fila de la curva de demanda vigente (schema CityDemand).
type CityDemand struct {
	CityID           uuid.UUID
	ProductID        uuid.UUID
	D0PerSimDay      int64
	SaturationFactor float64
	CurrentPrice     int64
	UnlockedAtLevel  int32
	UpdatedAtSim     int64
}

// ─── Filtros de los listados ─────────────────────────────────────────────────

// RegionFilter son los filtros de GET /world/regions.
type RegionFilter struct {
	Biome  string // "" = sin filtro
	Cursor string
	Limit  int
}

// ProductFilter son los filtros de GET /world/products.
type ProductFilter struct {
	Class  string // "" = sin filtro
	IsFuel *bool
	Cursor string
	Limit  int
}

// BuildingTypeFilter son los filtros de GET /world/building-types.
type BuildingTypeFilter struct {
	Cursor string
	Limit  int
}

// RecipeFilter son los filtros de GET /world/recipes.
type RecipeFilter struct {
	BuildingTypeID *uuid.UUID
	ProductID      *uuid.UUID
	Cursor         string
	Limit          int
}

// DepositFilter son los filtros de GET /world/resource-deposits.
type DepositFilter struct {
	RegionID  *uuid.UUID
	ProductID *uuid.UUID
	// OnlyAvailable recorta a los yacimientos con remaining_amount > 0
	// (default true en el contrato).
	OnlyAvailable bool
	Cursor        string
	Limit         int
}

// CityFilter son los filtros de GET /world/cities.
type CityFilter struct {
	RegionID *uuid.UUID
	MinLevel *int32
	Cursor   string
	Limit    int
}

// ─── Enums observables del contrato ──────────────────────────────────────────

// validBiomes es el conjunto Biome del contrato (world.biome).
var validBiomes = map[string]struct{}{
	"plains": {}, "forest": {}, "desert": {},
	"mountain": {}, "ocean": {}, "coast": {},
}

// validProductClasses es el conjunto ProductClass del contrato
// (world.product_class).
var validProductClasses = map[string]struct{}{
	"basic": {}, "luxury": {},
}

// validBiome indica si s es un bioma válido del contrato.
func validBiome(s string) bool {
	_, ok := validBiomes[s]
	return ok
}

// validProductClass indica si s es una clase de producto válida del contrato.
func validProductClass(s string) bool {
	_, ok := validProductClasses[s]
	return ok
}
