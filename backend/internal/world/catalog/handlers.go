package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"github.com/google/uuid"

	"github.com/lokiteitor/global-market/backend/internal/platform/httpx"
	"github.com/lokiteitor/global-market/backend/internal/platform/logging"
)

// MetaSource construye los metadatos comunes (schema Meta del contrato:
// sim_time actual del mundo) de toda respuesta exitosa. Lo implementa el
// composition root con el reloj de simulación (mismo patrón que ledger/market).
type MetaSource interface {
	Meta(ctx context.Context) httpx.Meta
}

// Reader es la superficie de lectura que consumen los handlers; la implementa
// *Service. Todos los listados devuelven además el cursor de la página
// siguiente (vacío si no hay más).
type Reader interface {
	ListRegions(ctx context.Context, f RegionFilter) ([]Region, string, error)
	GetRegion(ctx context.Context, id uuid.UUID) (Region, error)
	ListProducts(ctx context.Context, f ProductFilter) ([]Product, string, error)
	ListBuildingTypes(ctx context.Context, f BuildingTypeFilter) ([]BuildingType, string, error)
	ListRecipes(ctx context.Context, f RecipeFilter) ([]Recipe, string, error)
	ListResourceDeposits(ctx context.Context, f DepositFilter) ([]ResourceDeposit, string, error)
	ListCities(ctx context.Context, f CityFilter) ([]City, string, error)
	GetCity(ctx context.Context, id uuid.UUID) (City, error)
	ListCityDemand(ctx context.Context, cityID uuid.UUID, productID *uuid.UUID) ([]CityDemand, error)
}

var _ Reader = (*Service)(nil)

// Handlers sirve los endpoints de catálogo del contrato OpenAPI (world/*
// estático y observable). La autorización es la sesión del gateway
// (RequireAuth); no hay filtro de propiedad.
type Handlers struct {
	reader Reader
	meta   MetaSource
	logger *slog.Logger
}

// NewHandlers construye los handlers del subpaquete de catálogos.
func NewHandlers(reader Reader, meta MetaSource, logger *slog.Logger) *Handlers {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handlers{reader: reader, meta: meta, logger: logger}
}

// Register monta las rutas del contrato en el mux del gateway (sin prefijo: lo
// añade el composition root, como el resto de módulos).
func (h *Handlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /world/regions", h.listRegions)
	mux.HandleFunc("GET /world/regions/{regionId}", h.getRegion)
	mux.HandleFunc("GET /world/products", h.listProducts)
	mux.HandleFunc("GET /world/building-types", h.listBuildingTypes)
	mux.HandleFunc("GET /world/recipes", h.listRecipes)
	mux.HandleFunc("GET /world/resource-deposits", h.listResourceDeposits)
	mux.HandleFunc("GET /world/cities", h.listCities)
	mux.HandleFunc("GET /world/cities/{cityId}", h.getCity)
	mux.HandleFunc("GET /world/cities/{cityId}/demand", h.getCityDemand)
}

// ─── GET /world/regions ──────────────────────────────────────────────────────

func (h *Handlers) listRegions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := RegionFilter{Cursor: q.Get("cursor")}

	if biome := q.Get("biome"); biome != "" {
		if !validBiome(biome) {
			writeValidationError(w, "biome", "no es un bioma válido")
			return
		}
		filter.Biome = biome
	}
	limit, err := parseLimit(q)
	if err != nil {
		writeValidationError(w, "limit", err.Error())
		return
	}
	filter.Limit = limit

	regions, next, err := h.reader.ListRegions(r.Context(), filter)
	if err != nil {
		h.writeListError(w, r, err, "listando regiones")
		return
	}
	data := make([]regionJSON, len(regions))
	for i, reg := range regions {
		data[i] = toRegionJSON(reg)
	}
	h.writeList(w, r, data, next)
}

// ─── GET /world/regions/{regionId} ───────────────────────────────────────────

func (h *Handlers) getRegion(w http.ResponseWriter, r *http.Request) {
	// Un id de ruta que no es UUID no puede resolver a ninguna entidad: 404
	// (el contrato no define 400 para el path).
	id, err := uuid.Parse(r.PathValue("regionId"))
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "la región no existe", nil)
		return
	}
	region, err := h.reader.GetRegion(r.Context(), id)
	if err != nil {
		h.writeListError(w, r, err, "obteniendo la región")
		return
	}
	httpx.WriteData(w, http.StatusOK, toRegionJSON(region), h.meta.Meta(r.Context()))
}

// ─── GET /world/products ─────────────────────────────────────────────────────

func (h *Handlers) listProducts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := ProductFilter{Cursor: q.Get("cursor")}

	if class := q.Get("class"); class != "" {
		if !validProductClass(class) {
			writeValidationError(w, "class", "no es una clase de producto válida")
			return
		}
		filter.Class = class
	}
	if raw := q.Get("is_fuel"); raw != "" {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			writeValidationError(w, "is_fuel", "debe ser un booleano")
			return
		}
		filter.IsFuel = &b
	}
	limit, err := parseLimit(q)
	if err != nil {
		writeValidationError(w, "limit", err.Error())
		return
	}
	filter.Limit = limit

	products, next, err := h.reader.ListProducts(r.Context(), filter)
	if err != nil {
		h.writeListError(w, r, err, "listando productos")
		return
	}
	data := make([]productJSON, len(products))
	for i, p := range products {
		data[i] = toProductJSON(p)
	}
	h.writeList(w, r, data, next)
}

// ─── GET /world/building-types ───────────────────────────────────────────────

func (h *Handlers) listBuildingTypes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := BuildingTypeFilter{Cursor: q.Get("cursor")}
	limit, err := parseLimit(q)
	if err != nil {
		writeValidationError(w, "limit", err.Error())
		return
	}
	filter.Limit = limit

	types, next, err := h.reader.ListBuildingTypes(r.Context(), filter)
	if err != nil {
		h.writeListError(w, r, err, "listando tipos de edificio")
		return
	}
	data := make([]buildingTypeJSON, len(types))
	for i, t := range types {
		data[i] = toBuildingTypeJSON(t)
	}
	h.writeList(w, r, data, next)
}

// ─── GET /world/recipes ──────────────────────────────────────────────────────

func (h *Handlers) listRecipes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := RecipeFilter{Cursor: q.Get("cursor")}

	if id, ok, err := optionalUUID(q, "building_type_id"); err != nil {
		writeValidationError(w, "building_type_id", "no es un UUID válido")
		return
	} else if ok {
		filter.BuildingTypeID = &id
	}
	if id, ok, err := optionalUUID(q, "product_id"); err != nil {
		writeValidationError(w, "product_id", "no es un UUID válido")
		return
	} else if ok {
		filter.ProductID = &id
	}
	limit, err := parseLimit(q)
	if err != nil {
		writeValidationError(w, "limit", err.Error())
		return
	}
	filter.Limit = limit

	recipes, next, err := h.reader.ListRecipes(r.Context(), filter)
	if err != nil {
		h.writeListError(w, r, err, "listando recetas")
		return
	}
	data := make([]recipeJSON, len(recipes))
	for i, rc := range recipes {
		data[i] = toRecipeJSON(rc)
	}
	h.writeList(w, r, data, next)
}

// ─── GET /world/resource-deposits ────────────────────────────────────────────

func (h *Handlers) listResourceDeposits(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	// only_available: default true (contrato).
	filter := DepositFilter{Cursor: q.Get("cursor"), OnlyAvailable: true}

	if id, ok, err := optionalUUID(q, "region_id"); err != nil {
		writeValidationError(w, "region_id", "no es un UUID válido")
		return
	} else if ok {
		filter.RegionID = &id
	}
	if id, ok, err := optionalUUID(q, "product_id"); err != nil {
		writeValidationError(w, "product_id", "no es un UUID válido")
		return
	} else if ok {
		filter.ProductID = &id
	}
	if raw := q.Get("only_available"); raw != "" {
		b, err := strconv.ParseBool(raw)
		if err != nil {
			writeValidationError(w, "only_available", "debe ser un booleano")
			return
		}
		filter.OnlyAvailable = b
	}
	limit, err := parseLimit(q)
	if err != nil {
		writeValidationError(w, "limit", err.Error())
		return
	}
	filter.Limit = limit

	deposits, next, err := h.reader.ListResourceDeposits(r.Context(), filter)
	if err != nil {
		h.writeListError(w, r, err, "listando yacimientos")
		return
	}
	data := make([]depositJSON, len(deposits))
	for i, d := range deposits {
		data[i] = toDepositJSON(d)
	}
	h.writeList(w, r, data, next)
}

// ─── GET /world/cities ───────────────────────────────────────────────────────

func (h *Handlers) listCities(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := CityFilter{Cursor: q.Get("cursor")}

	if id, ok, err := optionalUUID(q, "region_id"); err != nil {
		writeValidationError(w, "region_id", "no es un UUID válido")
		return
	} else if ok {
		filter.RegionID = &id
	}
	if raw := q.Get("min_level"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			writeValidationError(w, "min_level", "debe ser un entero")
			return
		}
		if n < 1 {
			writeValidationError(w, "min_level", "debe ser >= 1")
			return
		}
		lvl := int32(n)
		filter.MinLevel = &lvl
	}
	limit, err := parseLimit(q)
	if err != nil {
		writeValidationError(w, "limit", err.Error())
		return
	}
	filter.Limit = limit

	cities, next, err := h.reader.ListCities(r.Context(), filter)
	if err != nil {
		h.writeListError(w, r, err, "listando ciudades")
		return
	}
	data := make([]cityJSON, len(cities))
	for i, c := range cities {
		data[i] = toCityJSON(c)
	}
	h.writeList(w, r, data, next)
}

// ─── GET /world/cities/{cityId} ──────────────────────────────────────────────

func (h *Handlers) getCity(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("cityId"))
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "la ciudad no existe", nil)
		return
	}
	city, err := h.reader.GetCity(r.Context(), id)
	if err != nil {
		h.writeListError(w, r, err, "obteniendo la ciudad")
		return
	}
	httpx.WriteData(w, http.StatusOK, toCityJSON(city), h.meta.Meta(r.Context()))
}

// ─── GET /world/cities/{cityId}/demand ───────────────────────────────────────

func (h *Handlers) getCityDemand(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("cityId"))
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "la ciudad no existe", nil)
		return
	}
	q := r.URL.Query()
	var productID *uuid.UUID
	if pid, ok, err := optionalUUID(q, "product_id"); err != nil {
		writeValidationError(w, "product_id", "no es un UUID válido")
		return
	} else if ok {
		productID = &pid
	}

	demand, err := h.reader.ListCityDemand(r.Context(), id, productID)
	if err != nil {
		h.writeListError(w, r, err, "obteniendo la demanda de la ciudad")
		return
	}
	data := make([]cityDemandJSON, len(demand))
	for i, d := range demand {
		data[i] = toCityDemandJSON(d)
	}
	httpx.WriteData(w, http.StatusOK, data, h.meta.Meta(r.Context()))
}

// ─── Respuesta y errores ─────────────────────────────────────────────────────

// writeList emite una página con el cursor siguiente en el meta.
func (h *Handlers) writeList(w http.ResponseWriter, r *http.Request, data any, next string) {
	meta := h.meta.Meta(r.Context())
	meta.NextCursor = next
	httpx.WriteData(w, http.StatusOK, data, meta)
}

// writeListError mapea los errores tipados del servicio a los códigos del
// contrato; lo no reconocido es un 500 INTERNAL logueado con request id.
func (h *Handlers) writeListError(w http.ResponseWriter, r *http.Request, err error, doing string) {
	switch {
	case errors.Is(err, ErrRegionNotFound):
		httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "la región no existe", nil)
	case errors.Is(err, ErrCityNotFound):
		httpx.WriteError(w, http.StatusNotFound, httpx.CodeNotFound, "la ciudad no existe", nil)
	case errors.Is(err, ErrInvalidCursor):
		writeValidationError(w, "cursor", "no es un cursor válido de este listado")
	default:
		// Petición abortada por el cliente o plazo agotado: no es un fallo
		// del servicio y no debe contarse como 5xx ni loguearse como ERROR.
		if httpx.WriteClientGone(w, r, h.logger, err, doing) {
			return
		}
		logging.WithRequestID(h.logger, httpx.RequestIDFromContext(r.Context())).LogAttrs(
			r.Context(), slog.LevelError, "error "+doing,
			slog.String("error", err.Error()),
		)
		httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternal, "error interno del servidor", nil)
	}
}

// writeValidationError responde 400 VALIDATION_ERROR con el campo culpable.
func writeValidationError(w http.ResponseWriter, field, reason string) {
	httpx.WriteError(w, http.StatusBadRequest, httpx.CodeValidationError,
		fmt.Sprintf("parámetro %s inválido: %s", field, reason),
		map[string]any{"field": field})
}

// parseLimit interpreta el query param limit del contrato (entero 1..200;
// ausente = 0, el servicio aplica el default 50).
func parseLimit(q url.Values) (int, error) {
	raw := q.Get("limit")
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, errors.New("debe ser un entero")
	}
	if n < 1 || n > MaxPageLimit {
		return 0, fmt.Errorf("debe estar entre 1 y %d", MaxPageLimit)
	}
	return n, nil
}

// optionalUUID interpreta un query param UUID opcional: ausente → (_, false,
// nil); presente y válido → (id, true, nil); presente e inválido → (_, false,
// error).
func optionalUUID(q url.Values, name string) (uuid.UUID, bool, error) {
	raw := q.Get(name)
	if raw == "" {
		return uuid.UUID{}, false, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.UUID{}, false, err
	}
	return id, true, nil
}

// ─── DTOs del contrato (snake_case; dinero/stock como string, geometrías
// GeoJSON planas, sim-time int64) ────────────────────────────────────────────

type regionJSON struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	GridX         int32           `json:"grid_x"`
	GridY         int32           `json:"grid_y"`
	Bounds        json.RawMessage `json:"bounds,omitempty"`
	Biome         string          `json:"biome"`
	TaxRateBp     int32           `json:"tax_rate_bp"`
	CustomsRateBp int32           `json:"customs_rate_bp"`
	CanonBase     string          `json:"canon_base"`
	OpenedAtSim   int64           `json:"opened_at_sim"`
}

func toRegionJSON(r Region) regionJSON {
	return regionJSON{
		ID: r.ID.String(), Name: r.Name, GridX: r.GridX, GridY: r.GridY,
		Bounds: rawGeo(r.Bounds), Biome: r.Biome,
		TaxRateBp: r.TaxRateBp, CustomsRateBp: r.CustomsRateBp,
		CanonBase: fixed(r.CanonBase), OpenedAtSim: r.OpenedAtSim,
	}
}

type productJSON struct {
	ID           string `json:"id"`
	Code         string `json:"code"`
	Name         string `json:"name"`
	Class        string `json:"class"`
	UnitVolume   int32  `json:"unit_volume"`
	BasePrice    string `json:"base_price"`
	PriceFloor   string `json:"price_floor"`
	PriceCeiling string `json:"price_ceiling"`
	IsFuel       bool   `json:"is_fuel"`
}

func toProductJSON(p Product) productJSON {
	return productJSON{
		ID: p.ID.String(), Code: p.Code, Name: p.Name, Class: p.Class,
		UnitVolume: p.UnitVolume, BasePrice: fixed(p.BasePrice),
		PriceFloor: fixed(p.PriceFloor), PriceCeiling: fixed(p.PriceCeiling),
		IsFuel: p.IsFuel,
	}
}

type powerGenerationJSON struct {
	CapacityPerHour string `json:"capacity_per_hour"`
	FuelProductID   string `json:"fuel_product_id,omitempty"`
	FuelPerUnit     string `json:"fuel_per_unit"`
}

type buildingTypeJSON struct {
	ID              string               `json:"id"`
	Code            string               `json:"code"`
	Name            string               `json:"name"`
	FootprintCells  int32                `json:"footprint_cells"`
	MaxLevel        int32                `json:"max_level"`
	BaseStorage     string               `json:"base_storage"`
	PlacementRules  json.RawMessage      `json:"placement_rules,omitempty"`
	LevelCurve      json.RawMessage      `json:"level_curve,omitempty"`
	BuildCost       string               `json:"build_cost"`
	MaintenanceCost string               `json:"maintenance_cost"`
	PowerGeneration *powerGenerationJSON `json:"power_generation,omitempty"`
}

func toBuildingTypeJSON(t BuildingType) buildingTypeJSON {
	out := buildingTypeJSON{
		ID: t.ID.String(), Code: t.Code, Name: t.Name,
		FootprintCells: t.FootprintCells, MaxLevel: t.MaxLevel,
		BaseStorage:    fixed(t.BaseStorage),
		PlacementRules: rawObject(t.PlacementRules),
		LevelCurve:     rawObject(t.LevelCurve),
		BuildCost:      fixed(t.BuildCost), MaintenanceCost: fixed(t.MaintenanceCost),
	}
	if t.PowerGeneration != nil {
		out.PowerGeneration = &powerGenerationJSON{
			CapacityPerHour: fixed(t.PowerGeneration.Capacity),
			FuelProductID:   uuidPtrOrEmpty(t.PowerGeneration.FuelProductID),
			FuelPerUnit:     fixed(t.PowerGeneration.FuelPerUnit),
		}
	}
	return out
}

type recipeIngredientJSON struct {
	ProductID string `json:"product_id"`
	Role      string `json:"role"`
	Quantity  string `json:"quantity"`
}

type recipeJSON struct {
	ID                string                 `json:"id"`
	BuildingTypeID    string                 `json:"building_type_id"`
	Code              string                 `json:"code"`
	Name              string                 `json:"name"`
	BatchSimSeconds   int64                  `json:"batch_sim_seconds"`
	FuelProductID     string                 `json:"fuel_product_id,omitempty"`
	FuelPerBatch      string                 `json:"fuel_per_batch"`
	PowerPerHour      string                 `json:"power_per_hour"`
	WorkersRequired   int32                  `json:"workers_required"`
	MinCityLevel      int32                  `json:"min_city_level"`
	ChangeoverSeconds int64                  `json:"changeover_seconds"`
	Ingredients       []recipeIngredientJSON `json:"ingredients"`
}

func toRecipeJSON(rc Recipe) recipeJSON {
	ings := make([]recipeIngredientJSON, len(rc.Ingredients))
	for i, ing := range rc.Ingredients {
		ings[i] = recipeIngredientJSON{
			ProductID: ing.ProductID.String(),
			Role:      ing.Role,
			Quantity:  fixed(ing.Quantity),
		}
	}
	return recipeJSON{
		ID: rc.ID.String(), BuildingTypeID: rc.BuildingTypeID.String(),
		Code: rc.Code, Name: rc.Name, BatchSimSeconds: rc.BatchSimSeconds,
		FuelProductID: uuidPtrOrEmpty(rc.FuelProductID), FuelPerBatch: fixed(rc.FuelPerBatch),
		PowerPerHour:    fixed(rc.PowerPerHour),
		WorkersRequired: rc.WorkersRequired, MinCityLevel: rc.MinCityLevel,
		ChangeoverSeconds: rc.ChangeoverSeconds, Ingredients: ings,
	}
}

type depositJSON struct {
	ID              string          `json:"id"`
	RegionID        string          `json:"region_id"`
	ProductID       string          `json:"product_id"`
	Location        json.RawMessage `json:"location"`
	InitialAmount   string          `json:"initial_amount"`
	RemainingAmount string          `json:"remaining_amount"`
	Renewable       bool            `json:"renewable"`
	RegenPerSimDay  string          `json:"regen_per_sim_day"`
}

func toDepositJSON(d ResourceDeposit) depositJSON {
	return depositJSON{
		ID: d.ID.String(), RegionID: d.RegionID.String(), ProductID: d.ProductID.String(),
		Location: rawGeo(d.Location), InitialAmount: fixed(d.InitialAmount),
		RemainingAmount: fixed(d.RemainingAmount), Renewable: d.Renewable,
		RegenPerSimDay: fixed(d.RegenPerSimDay),
	}
}

type cityJSON struct {
	ID               string          `json:"id"`
	RegionID         string          `json:"region_id"`
	AccountID        string          `json:"account_id"`
	Name             string          `json:"name"`
	Location         json.RawMessage `json:"location"`
	Level            int32           `json:"level"`
	Population       int64           `json:"population"`
	SupplyIndex      float64         `json:"supply_index"`
	InfluenceRadiusM int32           `json:"influence_radius_m"`
	BaseSalary       string          `json:"base_salary"`
}

func toCityJSON(c City) cityJSON {
	return cityJSON{
		ID: c.ID.String(), RegionID: c.RegionID.String(), AccountID: c.AccountID.String(),
		Name: c.Name, Location: rawGeo(c.Location), Level: c.Level,
		Population: c.Population, SupplyIndex: c.SupplyIndex,
		InfluenceRadiusM: c.InfluenceRadiusM, BaseSalary: fixed(c.BaseSalary),
	}
}

type cityDemandJSON struct {
	CityID           string  `json:"city_id"`
	ProductID        string  `json:"product_id"`
	D0PerSimDay      string  `json:"d0_per_sim_day"`
	SaturationFactor float64 `json:"saturation_factor"`
	CurrentPrice     string  `json:"current_price"`
	UnlockedAtLevel  int32   `json:"unlocked_at_level"`
	UpdatedAtSim     int64   `json:"updated_at_sim"`
}

func toCityDemandJSON(d CityDemand) cityDemandJSON {
	return cityDemandJSON{
		CityID: d.CityID.String(), ProductID: d.ProductID.String(),
		D0PerSimDay: fixed(d.D0PerSimDay), SaturationFactor: d.SaturationFactor,
		CurrentPrice: fixed(d.CurrentPrice), UnlockedAtLevel: d.UnlockedAtLevel,
		UpdatedAtSim: d.UpdatedAtSim,
	}
}

// ─── Helpers de serialización ────────────────────────────────────────────────

// fixed serializa un entero de punto fijo (dinero/stock) como string (jamás
// float; invariante del ledger/contrato).
func fixed(v int64) string { return strconv.FormatInt(v, 10) }

// rawGeo embebe un GeoJSON plano (de ST_AsGeoJSON) como objeto JSON; vacío se
// omite (omitempty sobre RawMessage nil).
func rawGeo(s string) json.RawMessage {
	if s == "" {
		return nil
	}
	return json.RawMessage(s)
}

// rawObject embebe un JSONB (placement_rules/level_curve) como objeto; NULL o
// vacío se normaliza a un objeto vacío para respetar el schema (type: object).
func rawObject(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("{}")
	}
	return json.RawMessage(b)
}

// uuidPtrOrEmpty serializa un UUID opcional ("" se omite con omitempty).
func uuidPtrOrEmpty(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}
