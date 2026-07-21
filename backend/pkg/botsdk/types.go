package botsdk

import (
	"encoding/json"
	"time"
)

// ── Tipos primitivos del dominio ──

// Money es un importe monetario en unidades menores, serializado como string
// de dígitos (schema MoneyAmount) — nunca floats (invariante del ledger).
// En los campos contables del ledger (balance, amount) puede llevar signo
// negativo (solo la cuenta emission del banco central es negativa).
type Money string

// Int64 valida y convierte el importe a int64. Ver ParseMoney.
func (m Money) Int64() (int64, error) { return ParseMoney(string(m)) }

// Qty es una cantidad de stock en la unidad mínima del producto, serializada
// como string de dígitos sin signo (schema StockQty) — nunca floats.
type Qty string

// Int64 valida y convierte la cantidad a int64. Ver ParseQty.
func (q Qty) Int64() (int64, error) { return ParseQty(string(q)) }

// SimTime es el sim-time en segundos desde el génesis del mundo (ratio 24×).
type SimTime = int64

// ── Enums de dominio ──

// AccountKind es el tipo de actor (el motor no distingue el origen de un comando).
type AccountKind string

const (
	AccountHuman  AccountKind = "human"
	AccountBot    AccountKind = "bot"
	AccountCity   AccountKind = "city"
	AccountSystem AccountKind = "system"
)

// AccountStatus es el estado de una cuenta.
type AccountStatus string

const (
	AccountActive    AccountStatus = "active"
	AccountSuspended AccountStatus = "suspended"
	AccountRetired   AccountStatus = "retired"
)

// BotArchetype es el arquetipo de un bot (solo presente cuando kind = bot).
type BotArchetype string

const (
	ArchetypePrimaryProducer       BotArchetype = "primary_producer"
	ArchetypeIndustrialTransformer BotArchetype = "industrial_transformer"
	ArchetypeArbitrageur           BotArchetype = "arbitrageur"
	ArchetypeFreighter             BotArchetype = "freighter"
)

// Biome es el bioma de una región.
type Biome string

const (
	BiomePlains   Biome = "plains"
	BiomeForest   Biome = "forest"
	BiomeDesert   Biome = "desert"
	BiomeMountain Biome = "mountain"
	BiomeOcean    Biome = "ocean"
	BiomeCoast    Biome = "coast"
)

// ProductClass distingue demanda urbana inelástica (basic) de elástica (luxury).
type ProductClass string

const (
	ClassBasic  ProductClass = "basic"
	ClassLuxury ProductClass = "luxury"
)

// ConcessionStatus es el estado de una concesión de suelo.
type ConcessionStatus string

const (
	ConcessionActive     ConcessionStatus = "active"
	ConcessionDelinquent ConcessionStatus = "delinquent"
	ConcessionGrace      ConcessionStatus = "grace"
	ConcessionReverted   ConcessionStatus = "reverted"
)

// BuildingStatus es el estado de un edificio.
type BuildingStatus string

const (
	BuildingUnderConstruction BuildingStatus = "under_construction"
	BuildingOperational       BuildingStatus = "operational"
	BuildingDamaged           BuildingStatus = "damaged"
	BuildingInMaintenance     BuildingStatus = "in_maintenance"
	BuildingAbandoned         BuildingStatus = "abandoned"
	BuildingSeized            BuildingStatus = "seized"
)

// BatchStatus es el estado de una orden de producción.
type BatchStatus string

const (
	BatchQueued         BatchStatus = "queued"
	BatchRunning        BatchStatus = "running"
	BatchPausedNoFuel   BatchStatus = "paused_no_fuel"
	BatchPausedNoWorker BatchStatus = "paused_no_workers"
	BatchCompleted      BatchStatus = "completed"
	BatchCancelled      BatchStatus = "cancelled"
)

// NodeKind es el tipo de nodo del grafo logístico.
type NodeKind string

const (
	NodeMine               NodeKind = "mine"
	NodeFactory            NodeKind = "factory"
	NodeWarehouse          NodeKind = "warehouse"
	NodePort               NodeKind = "port"
	NodeStation            NodeKind = "station"
	NodeDistributionCenter NodeKind = "distribution_center"
	NodeJunction           NodeKind = "junction"
	NodeCityGate           NodeKind = "city_gate"
)

// LinkMode es el modo de transporte de un enlace.
type LinkMode string

const (
	ModeRoad LinkMode = "road"
	ModeRail LinkMode = "rail"
	ModeSea  LinkMode = "sea"
)

// RouteKind distingue líneas regulares fijas de servicios bajo demanda.
type RouteKind string

const (
	RouteFixedLine RouteKind = "fixed_line"
	RouteOnDemand  RouteKind = "on_demand"
)

// VehicleStatus es el estado de un vehículo. sealed = SELLADO durante un
// handoff entre shards: visible pero no comandable.
type VehicleStatus string

const (
	VehicleIdle          VehicleStatus = "idle"
	VehicleLoading       VehicleStatus = "loading"
	VehicleInTransit     VehicleStatus = "in_transit"
	VehicleUnloading     VehicleStatus = "unloading"
	VehicleBroken        VehicleStatus = "broken"
	VehicleInMaintenance VehicleStatus = "in_maintenance"
	VehicleSealed        VehicleStatus = "sealed"
)

// ShipmentStatus es el estado de un cargamento.
type ShipmentStatus string

const (
	ShipmentInWarehouse    ShipmentStatus = "in_warehouse"
	ShipmentInTransit      ShipmentStatus = "in_transit"
	ShipmentAtTerminal     ShipmentStatus = "at_terminal"
	ShipmentDelivered      ShipmentStatus = "delivered"
	ShipmentReleasedInSitu ShipmentStatus = "released_in_situ"
)

// LedgerAccountKind es el tipo de cuenta del ledger de doble entrada.
type LedgerAccountKind string

const (
	LedgerCash          LedgerAccountKind = "cash"
	LedgerEscrow        LedgerAccountKind = "escrow"
	LedgerGuarantee     LedgerAccountKind = "guarantee"
	LedgerStockFree     LedgerAccountKind = "stock_free"
	LedgerStockReserved LedgerAccountKind = "stock_reserved"
	LedgerCustody       LedgerAccountKind = "custody"
	LedgerSink          LedgerAccountKind = "sink"
	LedgerEmission      LedgerAccountKind = "emission"
)

// PublicationKind es el tipo de publicación del tablón.
type PublicationKind string

const (
	PublicationSell    PublicationKind = "sell"
	PublicationBuy     PublicationKind = "buy"
	PublicationFreight PublicationKind = "freight"
)

// PublicationStatus es el estado de una publicación.
type PublicationStatus string

const (
	PublicationDrawWindow  PublicationStatus = "draw_window"
	PublicationOpen        PublicationStatus = "open"
	PublicationMicroWindow PublicationStatus = "micro_window"
	PublicationExhausted   PublicationStatus = "exhausted"
	PublicationCancelled   PublicationStatus = "cancelled"
	PublicationExpired     PublicationStatus = "expired"
)

// AcceptanceStatus es el estado de una aceptación tras (o antes de) el sorteo.
type AcceptanceStatus string

const (
	AcceptancePendingDraw AcceptanceStatus = "pending_draw"
	AcceptanceServed      AcceptanceStatus = "served"
	AcceptanceReleased    AcceptanceStatus = "released"
)

// ContractStatus es el estado de un contrato CCRI.
type ContractStatus string

const (
	ContractActive  ContractStatus = "active"
	ContractSettled ContractStatus = "settled"
	ContractFailed  ContractStatus = "failed"
)

// ContractChannel es el canal de descubrimiento de una publicación/contrato.
type ContractChannel string

const (
	ChannelBoard   ContractChannel = "board"
	ChannelPrivate ContractChannel = "private"
)

// Orden del tablón (parámetro sort de Board).
const (
	SortUnitPriceAsc    = "unit_price_asc"
	SortUnitPriceDesc   = "unit_price_desc"
	SortPublishedAtDesc = "published_at_desc"
	SortDeadlineAsc     = "deadline_asc"
)

// Criterios de optimización de PlanRoute (parámetro optimize).
const (
	OptimizeTime = "time"
	OptimizeCost = "cost"
)

// ── Geometrías (GeoJSON-like; coordenadas planas de mundo en metros, SRID 0) ──

// GeoPoint es un punto en coordenadas planas de mundo [x_m, y_m].
type GeoPoint struct {
	Type        string     `json:"type"` // "Point"
	Coordinates [2]float64 `json:"coordinates"`
}

// NewGeoPoint construye un GeoPoint bien formado.
func NewGeoPoint(xM, yM float64) GeoPoint {
	return GeoPoint{Type: "Point", Coordinates: [2]float64{xM, yM}}
}

// GeoLineString es una línea con vértices [x_m, y_m].
type GeoLineString struct {
	Type        string       `json:"type"` // "LineString"
	Coordinates [][2]float64 `json:"coordinates"`
}

// GeoPolygon es un polígono (anillos cerrados) con vértices [x_m, y_m].
type GeoPolygon struct {
	Type        string         `json:"type"` // "Polygon"
	Coordinates [][][2]float64 `json:"coordinates"`
}

// NewGeoPolygon construye un GeoPolygon bien formado a partir de su anillo
// exterior (debe estar cerrado: primer y último vértice iguales).
func NewGeoPolygon(exterior [][2]float64) GeoPolygon {
	return GeoPolygon{Type: "Polygon", Coordinates: [][][2]float64{exterior}}
}

// ── Envoltorio de metadatos ──

// Meta son los metadatos comunes de toda respuesta exitosa.
type Meta struct {
	// SimTime es el sim-time legible `AÑO-DÍA-HH:MM`.
	SimTime string `json:"sim_time"`
	// SimTimeSeconds es el sim-time canónico en segundos desde el génesis.
	SimTimeSeconds SimTime `json:"sim_time_seconds"`
	// ServerTime es el wall-clock del servidor (solo informativo).
	ServerTime time.Time `json:"server_time"`
	// NextCursor es el cursor de la página siguiente; vacío si no hay más.
	NextCursor string `json:"next_cursor,omitempty"`
}

// ── Auth ──

// SessionCreated es la respuesta de POST /auth/sessions. El token se devuelve
// una única vez (el servidor solo almacena su hash).
type SessionCreated struct {
	SessionID string    `json:"session_id"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	Account   Account   `json:"account"`
}

// Account es una corporación del mundo (humano, bot, ciudad o sistema).
type Account struct {
	ID           string        `json:"id"`
	Kind         AccountKind   `json:"kind"`
	Name         string        `json:"name"`
	Status       AccountStatus `json:"status"`
	BotArchetype BotArchetype  `json:"bot_archetype,omitempty"`
	CreatedAt    time.Time     `json:"created_at"`
}

// ── Ledger ──

// LedgerAccount es una cuenta del ledger de doble entrada (solo lectura por API).
type LedgerAccount struct {
	ID                  string            `json:"id"`
	Kind                LedgerAccountKind `json:"kind"`
	OwnerAccountID      string            `json:"owner_account_id,omitempty"`
	ProductID           string            `json:"product_id,omitempty"`
	WarehouseBuildingID string            `json:"warehouse_building_id,omitempty"`
	ReferenceID         string            `json:"reference_id,omitempty"`
	Balance             Money             `json:"balance"`
	UpdatedAt           time.Time         `json:"updated_at,omitzero"`
	CreatedAt           time.Time         `json:"created_at"`
}

// LedgerEntry es una partida append-only del ledger.
type LedgerEntry struct {
	ID              string    `json:"id"`
	TransactionID   string    `json:"transaction_id"`
	AccountID       string    `json:"account_id"`
	Amount          Money     `json:"amount"`
	TransactionKind string    `json:"transaction_kind"`
	ReferenceID     string    `json:"reference_id,omitempty"`
	Description     string    `json:"description,omitempty"`
	SimTimeAt       SimTime   `json:"sim_time_at"`
	CreatedAt       time.Time `json:"created_at"`
}

// ── Contratos: tablón ──

// Publication es una publicación del tablón global (toda publicación visible
// es ejecutable al 100%: su garantía íntegra quedó bloqueada al publicar).
type Publication struct {
	ID                    string            `json:"id"`
	Kind                  PublicationKind   `json:"kind"`
	PublisherAccountID    string            `json:"publisher_account_id"`
	Channel               ContractChannel   `json:"channel"`
	CounterpartyAccountID string            `json:"counterparty_account_id,omitempty"`
	ProductID             string            `json:"product_id,omitempty"`
	QuantityTotal         Qty               `json:"quantity_total"`
	QuantityRemaining     Qty               `json:"quantity_remaining"`
	UnitPrice             Money             `json:"unit_price"`
	MinLot                Qty               `json:"min_lot"`
	OriginNodeID          string            `json:"origin_node_id,omitempty"`
	DestinationNodeID     string            `json:"destination_node_id,omitempty"`
	DeliverySimSeconds    SimTime           `json:"delivery_sim_seconds"`
	Status                PublicationStatus `json:"status"`
	WindowClosesAt        time.Time         `json:"window_closes_at,omitzero"`
	CancelCooldownUntil   time.Time         `json:"cancel_cooldown_until,omitzero"`
	DeclaredValue         Money             `json:"declared_value,omitempty"`
	PublishedAtSim        SimTime           `json:"published_at_sim"`
	CreatedAt             time.Time         `json:"created_at,omitzero"`
}

// PublicationCreate es el cuerpo de POST /contracts/publications.
type PublicationCreate struct {
	Kind                  PublicationKind `json:"kind"`
	Channel               ContractChannel `json:"channel,omitempty"`
	CounterpartyAccountID string          `json:"counterparty_account_id,omitempty"`
	ProductID             string          `json:"product_id,omitempty"`
	QuantityTotal         Qty             `json:"quantity_total"`
	UnitPrice             Money           `json:"unit_price"`
	MinLot                Qty             `json:"min_lot,omitempty"`
	OriginNodeID          string          `json:"origin_node_id,omitempty"`
	DestinationNodeID     string          `json:"destination_node_id,omitempty"`
	DeliverySimSeconds    SimTime         `json:"delivery_sim_seconds"`
	DeclaredValue         Money           `json:"declared_value,omitempty"`
}

// AcceptanceCreate es el cuerpo de POST /contracts/publications/{id}/acceptances.
// OriginNodeID es requerido al aceptar publicaciones buy (almacén propio del
// vendedor del que sale el stock); ignorado en sell.
type AcceptanceCreate struct {
	Quantity     Qty    `json:"quantity"`
	OriginNodeID string `json:"origin_node_id,omitempty"`
}

// Acceptance es una aceptación registrada en la ventana de sorteo.
type Acceptance struct {
	ID                string           `json:"id"`
	PublicationID     string           `json:"publication_id"`
	AcceptorAccountID string           `json:"acceptor_account_id"`
	Quantity          Qty              `json:"quantity"`
	QuantityServed    Qty              `json:"quantity_served"`
	Status            AcceptanceStatus `json:"status"`
	DrawOrder         int              `json:"draw_order,omitempty"`
	ContractID        string           `json:"contract_id,omitempty"`
	FreightContractID string           `json:"freight_contract_id,omitempty"`
	AcceptedAt        time.Time        `json:"accepted_at"`
	ResolvedAt        time.Time        `json:"resolved_at,omitzero"`
}

// ── Contratos: CCRI ──

// Contract es el CCRI de bienes — la unidad económica atómica del juego.
type Contract struct {
	ID                       string          `json:"id"`
	PublicationID            string          `json:"publication_id,omitempty"`
	Channel                  ContractChannel `json:"channel"`
	BuyerAccountID           string          `json:"buyer_account_id"`
	SellerAccountID          string          `json:"seller_account_id"`
	ProductID                string          `json:"product_id"`
	QuantityAgreed           Qty             `json:"quantity_agreed"`
	QuantityDelivered        Qty             `json:"quantity_delivered"`
	UnitPrice                Money           `json:"unit_price"`
	OriginNodeID             string          `json:"origin_node_id"`
	DestinationNodeID        string          `json:"destination_node_id"`
	DeadlineSim              SimTime         `json:"deadline_sim"`
	Status                   ContractStatus  `json:"status"`
	FillBP                   int             `json:"fill_bp,omitempty"`
	StockReserveAccountID    string          `json:"stock_reserve_account_id,omitempty"`
	SellerGuaranteeAccountID string          `json:"seller_guarantee_account_id,omitempty"`
	EscrowAccountID          string          `json:"escrow_account_id,omitempty"`
	ConfirmedAtSim           SimTime         `json:"confirmed_at_sim"`
	SettledAtSim             SimTime         `json:"settled_at_sim,omitempty"`
	CreatedAt                time.Time       `json:"created_at,omitzero"`
}

// ContractDelivery es una entrega parcial confirmada por llegada física.
type ContractDelivery struct {
	ID             string  `json:"id"`
	ContractID     string  `json:"contract_id"`
	ShipmentID     string  `json:"shipment_id"`
	Quantity       Qty     `json:"quantity"`
	DeliveredAtSim SimTime `json:"delivered_at_sim"`
	OnTime         bool    `json:"on_time"`
}

// FreightContract es el CCRI-Flete (Fase 2).
type FreightContract struct {
	ID                        string          `json:"id"`
	PublicationID             string          `json:"publication_id,omitempty"`
	Channel                   ContractChannel `json:"channel"`
	ShipperAccountID          string          `json:"shipper_account_id"`
	CarrierAccountID          string          `json:"carrier_account_id"`
	OriginNodeID              string          `json:"origin_node_id"`
	DestinationNodeID         string          `json:"destination_node_id"`
	FreightPrice              Money           `json:"freight_price"`
	DeclaredValue             Money           `json:"declared_value"`
	DeadlineSim               SimTime         `json:"deadline_sim"`
	Status                    ContractStatus  `json:"status"`
	FillBP                    int             `json:"fill_bp,omitempty"`
	EscrowAccountID           string          `json:"escrow_account_id,omitempty"`
	CarrierGuaranteeAccountID string          `json:"carrier_guarantee_account_id,omitempty"`
	CustodyAccountID          string          `json:"custody_account_id,omitempty"`
	ConfirmedAtSim            SimTime         `json:"confirmed_at_sim"`
	SettledAtSim              SimTime         `json:"settled_at_sim,omitempty"`
	CreatedAt                 time.Time       `json:"created_at,omitzero"`
}

// ── Market ──

// OhlcCandle es una vela OHLC construida a partir de contratos liquidados.
type OhlcCandle struct {
	ProductID      string  `json:"product_id"`
	RegionID       string  `json:"region_id"`
	BucketStartSim SimTime `json:"bucket_start_sim"`
	BucketSimSecs  int64   `json:"bucket_sim_secs"`
	OpenPrice      Money   `json:"open_price"`
	HighPrice      Money   `json:"high_price"`
	LowPrice       Money   `json:"low_price"`
	ClosePrice     Money   `json:"close_price"`
	Volume         Qty     `json:"volume"`
	ContractCount  int     `json:"contract_count"`
}

// ── World: estático y catálogos ──

// Region es una macro-región (jurisdicción fiscal y unidad de sharding).
type Region struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	GridX         int        `json:"grid_x"`
	GridY         int        `json:"grid_y"`
	Bounds        GeoPolygon `json:"bounds,omitzero"`
	Biome         Biome      `json:"biome"`
	TaxRateBP     int        `json:"tax_rate_bp"`
	CustomsRateBP int        `json:"customs_rate_bp"`
	CanonBase     Money      `json:"canon_base"`
	OpenedAtSim   SimTime    `json:"opened_at_sim"`
}

// Product es un bien del catálogo con sus clamps de precio.
type Product struct {
	ID           string       `json:"id"`
	Code         string       `json:"code"`
	Name         string       `json:"name"`
	Class        ProductClass `json:"class"`
	UnitVolume   int          `json:"unit_volume"`
	BasePrice    Money        `json:"base_price"`
	PriceFloor   Money        `json:"price_floor"`
	PriceCeiling Money        `json:"price_ceiling"`
	IsFuel       bool         `json:"is_fuel"`
}

// BuildingType es un tipo de edificio del catálogo.
type BuildingType struct {
	ID              string         `json:"id"`
	Code            string         `json:"code"`
	Name            string         `json:"name"`
	FootprintCells  int            `json:"footprint_cells"`
	MaxLevel        int            `json:"max_level"`
	BaseStorage     Qty            `json:"base_storage"`
	PlacementRules  map[string]any `json:"placement_rules,omitempty"`
	LevelCurve      map[string]any `json:"level_curve,omitempty"`
	BuildCost       Money          `json:"build_cost"`
	MaintenanceCost Money          `json:"maintenance_cost"`
}

// RecipeIngredient es un insumo o producto de una receta.
type RecipeIngredient struct {
	ProductID string `json:"product_id"`
	Role      string `json:"role"` // "input" | "output"
	Quantity  Qty    `json:"quantity"`
}

// Recipe es una receta del catálogo (fija en estructura).
type Recipe struct {
	ID                string             `json:"id"`
	BuildingTypeID    string             `json:"building_type_id"`
	Code              string             `json:"code"`
	Name              string             `json:"name"`
	BatchSimSeconds   SimTime            `json:"batch_sim_seconds"`
	FuelProductID     string             `json:"fuel_product_id,omitempty"`
	FuelPerBatch      Qty                `json:"fuel_per_batch"`
	WorkersRequired   int                `json:"workers_required"`
	MinCityLevel      int                `json:"min_city_level"`
	ChangeoverSeconds SimTime            `json:"changeover_seconds"`
	Ingredients       []RecipeIngredient `json:"ingredients"`
}

// ResourceDeposit es un yacimiento de recursos naturales.
type ResourceDeposit struct {
	ID              string   `json:"id"`
	RegionID        string   `json:"region_id"`
	ProductID       string   `json:"product_id"`
	Location        GeoPoint `json:"location"`
	InitialAmount   Qty      `json:"initial_amount"`
	RemainingAmount Qty      `json:"remaining_amount"`
	Renewable       bool     `json:"renewable"`
	RegenPerSimDay  Qty      `json:"regen_per_sim_day"`
}

// ── World: ciudades ──

// City es el único consumidor final de la economía.
type City struct {
	ID               string   `json:"id"`
	RegionID         string   `json:"region_id"`
	AccountID        string   `json:"account_id"`
	Name             string   `json:"name"`
	Location         GeoPoint `json:"location"`
	Level            int      `json:"level"`
	Population       int64    `json:"population"`
	SupplyIndex      float64  `json:"supply_index"`
	InfluenceRadiusM int      `json:"influence_radius_m"`
	BaseSalary       Money    `json:"base_salary"`
}

// CityDemand es la curva de demanda vigente de una ciudad para un producto.
type CityDemand struct {
	CityID           string  `json:"city_id"`
	ProductID        string  `json:"product_id"`
	D0PerSimDay      Qty     `json:"d0_per_sim_day"`
	SaturationFactor float64 `json:"saturation_factor"`
	CurrentPrice     Money   `json:"current_price"`
	UnlockedAtLevel  int     `json:"unlocked_at_level"`
	UpdatedAtSim     SimTime `json:"updated_at_sim"`
}

// ── World: suelo ──

// Concession es una concesión de suelo renovable del sistema.
type Concession struct {
	ID              string           `json:"id"`
	RegionID        string           `json:"region_id"`
	HolderAccountID string           `json:"holder_account_id"`
	Parcel          GeoPolygon       `json:"parcel"`
	CanonAmount     Money            `json:"canon_amount"`
	PeriodSimDays   int              `json:"period_sim_days"`
	ExpiresAtSim    SimTime          `json:"expires_at_sim"`
	Status          ConcessionStatus `json:"status"`
	GrantedAtSim    SimTime          `json:"granted_at_sim"`
}

// ConcessionCreate es el cuerpo de POST /world/concessions.
type ConcessionCreate struct {
	RegionID string     `json:"region_id"`
	Parcel   GeoPolygon `json:"parcel"`
}

// ConcessionTransferCreate es el cuerpo de POST /world/concession-transfers.
type ConcessionTransferCreate struct {
	ConcessionID string `json:"concession_id"`
	ToAccountID  string `json:"to_account_id"`
	Price        Money  `json:"price"`
}

// ConcessionTransfer es un traspaso ejecutado de concesión.
type ConcessionTransfer struct {
	ID            string  `json:"id"`
	ConcessionID  string  `json:"concession_id"`
	FromAccountID string  `json:"from_account_id"`
	ToAccountID   string  `json:"to_account_id"`
	Price         Money   `json:"price"`
	SystemFee     Money   `json:"system_fee"`
	OccurredAtSim SimTime `json:"occurred_at_sim"`
}

// ── World: edificios y producción ──

// Building es un edificio propio.
type Building struct {
	ID             string         `json:"id"`
	OwnerAccountID string         `json:"owner_account_id"`
	RegionID       string         `json:"region_id"`
	ConcessionID   string         `json:"concession_id"`
	BuildingTypeID string         `json:"building_type_id"`
	Footprint      GeoPolygon     `json:"footprint"`
	Level          int            `json:"level"`
	Status         BuildingStatus `json:"status"`
	ActiveRecipeID string         `json:"active_recipe_id,omitempty"`
	ConditionPct   int            `json:"condition_pct"`
	FuelStock      Qty            `json:"fuel_stock"`
	UpdatedAtSim   SimTime        `json:"updated_at_sim,omitempty"`
	CreatedAt      time.Time      `json:"created_at,omitzero"`
}

// BuildingCreate es el cuerpo de POST /world/buildings.
type BuildingCreate struct {
	BuildingTypeID string     `json:"building_type_id"`
	ConcessionID   string     `json:"concession_id"`
	Footprint      GeoPolygon `json:"footprint"`
}

// BuildingUpdate es el cuerpo de PATCH /world/buildings/{id}. Distingue tres
// estados para la receta activa: sin cambio (ActiveRecipeID == nil y
// ClearActiveRecipe == false), fijar receta (ActiveRecipeID != nil) o detener
// la línea enviando null explícito (ClearActiveRecipe == true).
type BuildingUpdate struct {
	ActiveRecipeID    *string
	ClearActiveRecipe bool
	StartMaintenance  *bool
}

// MarshalJSON serializa solo los campos presentes, con null explícito para
// detener la línea (contrato BuildingUpdate, minProperties 1).
func (u BuildingUpdate) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, 2)
	switch {
	case u.ClearActiveRecipe:
		m["active_recipe_id"] = nil
	case u.ActiveRecipeID != nil:
		m["active_recipe_id"] = *u.ActiveRecipeID
	}
	if u.StartMaintenance != nil {
		m["start_maintenance"] = *u.StartMaintenance
	}
	return json.Marshal(m)
}

// InventoryItem es la vista física del stock de un edificio por producto.
type InventoryItem struct {
	BuildingID   string  `json:"building_id"`
	ProductID    string  `json:"product_id"`
	Quantity     Qty     `json:"quantity"`
	UpdatedAtSim SimTime `json:"updated_at_sim"`
}

// ProductionBatchCreate es el cuerpo de POST .../production-batches.
type ProductionBatchCreate struct {
	RecipeID      string `json:"recipe_id"`
	BatchesQueued int    `json:"batches_queued"`
	QueuePosition *int   `json:"queue_position,omitempty"`
}

// ProductionBatch es una orden de producción encolada o en curso.
type ProductionBatch struct {
	ID            string      `json:"id"`
	BuildingID    string      `json:"building_id"`
	RecipeID      string      `json:"recipe_id"`
	BatchesQueued int         `json:"batches_queued"`
	BatchesDone   int         `json:"batches_done"`
	Status        BatchStatus `json:"status"`
	QueuePosition int         `json:"queue_position"`
	StartedAtSim  SimTime     `json:"started_at_sim,omitempty"`
	ProgressPct   float64     `json:"progress_pct,omitempty"`
	EtaSim        SimTime     `json:"eta_sim,omitempty"`
}

// ── World: flota y cargamentos ──

// VehicleType es un tipo de vehículo del catálogo.
type VehicleType struct {
	ID                  string   `json:"id"`
	Code                string   `json:"code"`
	Name                string   `json:"name"`
	Mode                LinkMode `json:"mode"`
	CargoCapacity       Qty      `json:"cargo_capacity"`
	SpeedKmh            int      `json:"speed_kmh"`
	FuelProductID       string   `json:"fuel_product_id"`
	FuelPer100Km        Qty      `json:"fuel_per_100km"`
	AutonomyKm          int      `json:"autonomy_km"`
	PurchasePrice       Money    `json:"purchase_price"`
	OperatingCostPerDay Money    `json:"operating_cost_per_day"`
}

// VehiclePurchase es el cuerpo de POST /world/vehicles.
type VehiclePurchase struct {
	VehicleTypeID  string `json:"vehicle_type_id"`
	DeliveryNodeID string `json:"delivery_node_id"`
}

// VehicleUpdate es el cuerpo de PATCH /world/vehicles/{id}. Distingue tres
// estados para la ruta: sin cambio (RouteID == nil y ClearRoute == false),
// asignar ruta (RouteID != nil) o retirarla con null explícito (ClearRoute).
type VehicleUpdate struct {
	RouteID             *string
	ClearRoute          bool
	ScheduleMaintenance *bool
}

// MarshalJSON serializa solo los campos presentes, con null explícito para
// retirar la ruta (contrato VehicleUpdate, minProperties 1).
func (u VehicleUpdate) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, 2)
	switch {
	case u.ClearRoute:
		m["route_id"] = nil
	case u.RouteID != nil:
		m["route_id"] = *u.RouteID
	}
	if u.ScheduleMaintenance != nil {
		m["schedule_maintenance"] = *u.ScheduleMaintenance
	}
	return json.Marshal(m)
}

// VehiclePosition es la posición física derivada analíticamente al observarla.
type VehiclePosition struct {
	AtNodeID           string   `json:"at_node_id,omitempty"`
	OnSegmentID        string   `json:"on_segment_id,omitempty"`
	SegmentProgressPct float64  `json:"segment_progress_pct,omitempty"`
	Location           GeoPoint `json:"location,omitzero"`
}

// Vehicle es un vehículo de la flota propia.
type Vehicle struct {
	ID             string          `json:"id"`
	VehicleTypeID  string          `json:"vehicle_type_id"`
	OwnerAccountID string          `json:"owner_account_id"`
	Status         VehicleStatus   `json:"status"`
	WearPct        int             `json:"wear_pct"`
	Fuel           Qty             `json:"fuel"`
	RouteID        string          `json:"route_id,omitempty"`
	RouteLegIndex  int             `json:"route_leg_index,omitempty"`
	Position       VehiclePosition `json:"position"`
	RepairUntilSim SimTime         `json:"repair_until_sim,omitempty"`
	UpdatedAtSim   SimTime         `json:"updated_at_sim,omitempty"`
}

// Shipment es un cargamento etiquetado por contrato: el stock reservado viaja
// sin dejar de estar reservado — nada se teletransporta.
type Shipment struct {
	ID                string         `json:"id"`
	OwnerAccountID    string         `json:"owner_account_id"`
	ProductID         string         `json:"product_id"`
	Quantity          Qty            `json:"quantity"`
	ContractID        string         `json:"contract_id,omitempty"`
	FreightContractID string         `json:"freight_contract_id,omitempty"`
	VehicleID         string         `json:"vehicle_id,omitempty"`
	AtNodeID          string         `json:"at_node_id,omitempty"`
	Status            ShipmentStatus `json:"status"`
	UpdatedAtSim      SimTime        `json:"updated_at_sim,omitempty"`
}

// ShipmentDispatch es el cuerpo de POST /world/shipments/{id}/dispatch.
type ShipmentDispatch struct {
	VehicleID string `json:"vehicle_id"`
	RouteID   string `json:"route_id"`
}

// Terminal es una terminal con dueño y cola de transbordo.
type Terminal struct {
	ID                   string  `json:"id"`
	NodeID               string  `json:"node_id"`
	OwnerAccountID       string  `json:"owner_account_id"`
	TransshipmentPerHour int     `json:"transshipment_per_hour"`
	QueueLength          int     `json:"queue_length"`
	UpdatedAtSim         SimTime `json:"updated_at_sim,omitempty"`
}

// TerminalSlot es un slot de prioridad de atraque/transbordo (Fase 2).
type TerminalSlot struct {
	ID              string  `json:"id"`
	TerminalID      string  `json:"terminal_id"`
	PriorityTier    int     `json:"priority_tier"`
	Price           Money   `json:"price"`
	HolderAccountID string  `json:"holder_account_id,omitempty"`
	ValidUntilSim   SimTime `json:"valid_until_sim,omitempty"`
}

// ── Logistics ──

// NetworkNode es un nodo del grafo logístico.
type NetworkNode struct {
	ID         string   `json:"id"`
	Kind       NodeKind `json:"kind"`
	RegionID   string   `json:"region_id"`
	BuildingID string   `json:"building_id,omitempty"`
	CityID     string   `json:"city_id,omitempty"`
	Location   GeoPoint `json:"location"`
}

// LinkSegment es un segmento de enlace con su congestión suavizada (EMA).
type LinkSegment struct {
	ID            string  `json:"id"`
	RegionID      string  `json:"region_id"`
	Seq           int     `json:"seq"`
	LengthM       int     `json:"length_m"`
	CongestionEma float64 `json:"congestion_ema"`
	UpdatedAtSim  SimTime `json:"updated_at_sim"`
}

// NetworkLink es un enlace de uso común del grafo logístico.
type NetworkLink struct {
	ID              string        `json:"id"`
	Mode            LinkMode      `json:"mode"`
	FromNodeID      string        `json:"from_node_id"`
	ToNodeID        string        `json:"to_node_id"`
	Path            GeoLineString `json:"path,omitzero"`
	LengthM         int           `json:"length_m"`
	CapacityPerHour int           `json:"capacity_per_hour"`
	BaseSpeedKmh    int           `json:"base_speed_kmh"`
	Segments        []LinkSegment `json:"segments"`
}

// RoutePlanRequest es el cuerpo de POST /logistics/route-plans.
type RoutePlanRequest struct {
	OriginNodeID      string     `json:"origin_node_id"`
	DestinationNodeID string     `json:"destination_node_id"`
	Modes             []LinkMode `json:"modes,omitempty"`
	Optimize          string     `json:"optimize,omitempty"` // OptimizeTime | OptimizeCost
	CargoVolume       Qty        `json:"cargo_volume,omitempty"`
}

// RoutePlanLeg es un tramo del plan sugerido.
type RoutePlanLeg struct {
	Seq                     int      `json:"seq"`
	LinkID                  string   `json:"link_id"`
	Mode                    LinkMode `json:"mode"`
	EtaSimSeconds           SimTime  `json:"eta_sim_seconds"`
	TransshipmentTerminalID string   `json:"transshipment_terminal_id,omitempty"`
}

// RoutePlan es el plan sugerido por el asistente (ETAs informativas, no garantías).
type RoutePlan struct {
	OriginNodeID       string         `json:"origin_node_id"`
	DestinationNodeID  string         `json:"destination_node_id"`
	Legs               []RoutePlanLeg `json:"legs"`
	TotalEtaSimSeconds SimTime        `json:"total_eta_sim_seconds"`
	EstimatedCost      Money          `json:"estimated_cost,omitempty"`
}

// RouteCreate es el cuerpo de POST /logistics/routes.
type RouteCreate struct {
	Name string    `json:"name"`
	Kind RouteKind `json:"kind"`
	// Legs es la secuencia contigua de enlaces (IDs de NetworkLink).
	Legs []string `json:"legs"`
}

// RouteUpdate es el cuerpo de PATCH /logistics/routes/{id} (minProperties 1).
type RouteUpdate struct {
	Name   string   `json:"name,omitempty"`
	Active *bool    `json:"active,omitempty"`
	Legs   []string `json:"legs,omitempty"`
}

// RouteLeg es un tramo persistido de una ruta.
type RouteLeg struct {
	LegIndex int    `json:"leg_index"`
	LinkID   string `json:"link_id"`
}

// Route es una ruta propia (línea fija o servicio bajo demanda).
type Route struct {
	ID             string     `json:"id"`
	OwnerAccountID string     `json:"owner_account_id"`
	Name           string     `json:"name"`
	Kind           RouteKind  `json:"kind"`
	Active         bool       `json:"active"`
	Legs           []RouteLeg `json:"legs"`
	CreatedAt      time.Time  `json:"created_at,omitzero"`
}
