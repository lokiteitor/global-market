package catalog

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lokiteitor/global-market/backend/internal/world/sqlcgen"
)

// Service es el lado de LECTURA de los catálogos y el estado estático del
// mundo. Sirve los endpoints world/* de catálogo desde el esquema world tal
// cual está almacenado; no escribe. Comparte el paquete sqlc del contexto
// (internal/world/sqlcgen) con el resto de subpaquetes de world.
type Service struct {
	q    *sqlcgen.Queries
	opts Options
}

// NewService construye el servicio de lectura sobre el pool compartido de la
// plataforma, aplicando los defaults del módulo si opts trae valores no
// válidos (mismo criterio que ledger/market.NewService).
func NewService(pool *pgxpool.Pool, opts Options) *Service {
	if opts.QueryTimeout <= 0 {
		opts.QueryTimeout = DefaultQueryTimeout
	}
	return &Service{q: sqlcgen.New(pool), opts: opts}
}

func (s *Service) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, s.opts.QueryTimeout)
}

// normalizeLimit aplica el default y el máximo del contrato (50/200).
func normalizeLimit(limit int) int32 {
	switch {
	case limit <= 0:
		return DefaultPageLimit
	case limit > MaxPageLimit:
		return MaxPageLimit
	default:
		return int32(limit)
	}
}

// decodeAfter interpreta el cursor keyset de una entidad (vacío = primera
// página).
func decodeAfter(cursor string, kind cursorKind) (*uuid.UUID, error) {
	if cursor == "" {
		return nil, nil
	}
	id, err := decodeCursor(cursor, kind)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// ─── Regiones ────────────────────────────────────────────────────────────────

// ListRegions devuelve las regiones que satisfacen el filtro y el cursor de la
// página siguiente (vacío si no hay más).
func (s *Service) ListRegions(ctx context.Context, f RegionFilter) ([]Region, string, error) {
	after, err := decodeAfter(f.Cursor, cursorRegion)
	if err != nil {
		return nil, "", err
	}
	limit := normalizeLimit(f.Limit)

	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	rows, err := s.q.ListRegions(ctx, sqlcgen.ListRegionsParams{
		Biome: sqlcgen.NullWorldBiome{
			WorldBiome: sqlcgen.WorldBiome(f.Biome),
			Valid:      f.Biome != "",
		},
		AfterID:   after,
		PageLimit: limit + 1, // +1: detección de página siguiente
	})
	if err != nil {
		return nil, "", fmt.Errorf("world/catalog: listando regiones: %w", err)
	}

	next := ""
	if len(rows) > int(limit) {
		rows = rows[:limit]
		next = encodeCursor(cursorRegion, rows[len(rows)-1].ID)
	}
	regions := make([]Region, len(rows))
	for i, r := range rows {
		regions[i] = Region{
			ID: r.ID, Name: r.Name, GridX: r.GridX, GridY: r.GridY,
			Bounds: r.Bounds, Biome: string(r.Biome),
			TaxRateBp: r.TaxRateBp, CustomsRateBp: r.CustomsRateBp,
			CanonBase: r.CanonBase, OpenedAtSim: r.OpenedAtSim,
		}
	}
	return regions, next, nil
}

// GetRegion devuelve una región por id (ErrRegionNotFound si no existe).
func (s *Service) GetRegion(ctx context.Context, id uuid.UUID) (Region, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	r, err := s.q.GetRegion(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Region{}, ErrRegionNotFound
		}
		return Region{}, fmt.Errorf("world/catalog: obteniendo la región %s: %w", id, err)
	}
	return Region{
		ID: r.ID, Name: r.Name, GridX: r.GridX, GridY: r.GridY,
		Bounds: r.Bounds, Biome: string(r.Biome),
		TaxRateBp: r.TaxRateBp, CustomsRateBp: r.CustomsRateBp,
		CanonBase: r.CanonBase, OpenedAtSim: r.OpenedAtSim,
	}, nil
}

// ─── Productos ───────────────────────────────────────────────────────────────

// ListProducts devuelve los productos que satisfacen el filtro y el cursor de
// la página siguiente.
func (s *Service) ListProducts(ctx context.Context, f ProductFilter) ([]Product, string, error) {
	after, err := decodeAfter(f.Cursor, cursorProduct)
	if err != nil {
		return nil, "", err
	}
	limit := normalizeLimit(f.Limit)

	isFuel := pgtype.Bool{}
	if f.IsFuel != nil {
		isFuel = pgtype.Bool{Bool: *f.IsFuel, Valid: true}
	}

	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	rows, err := s.q.ListProducts(ctx, sqlcgen.ListProductsParams{
		Class: sqlcgen.NullWorldProductClass{
			WorldProductClass: sqlcgen.WorldProductClass(f.Class),
			Valid:             f.Class != "",
		},
		IsFuel:    isFuel,
		AfterID:   after,
		PageLimit: limit + 1,
	})
	if err != nil {
		return nil, "", fmt.Errorf("world/catalog: listando productos: %w", err)
	}

	next := ""
	if len(rows) > int(limit) {
		rows = rows[:limit]
		next = encodeCursor(cursorProduct, rows[len(rows)-1].ID)
	}
	products := make([]Product, len(rows))
	for i, r := range rows {
		products[i] = Product{
			ID: r.ID, Code: r.Code, Name: r.Name, Class: string(r.Class),
			UnitVolume: r.UnitVolume, BasePrice: r.BasePrice,
			PriceFloor: r.PriceFloor, PriceCeiling: r.PriceCeiling, IsFuel: r.IsFuel,
		}
	}
	return products, next, nil
}

// ─── Tipos de edificio ───────────────────────────────────────────────────────

// ListBuildingTypes devuelve el catálogo de tipos de edificio y el cursor de la
// página siguiente.
func (s *Service) ListBuildingTypes(ctx context.Context, f BuildingTypeFilter) ([]BuildingType, string, error) {
	after, err := decodeAfter(f.Cursor, cursorBuildingType)
	if err != nil {
		return nil, "", err
	}
	limit := normalizeLimit(f.Limit)

	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	rows, err := s.q.ListBuildingTypes(ctx, sqlcgen.ListBuildingTypesParams{
		AfterID:   after,
		PageLimit: limit + 1,
	})
	if err != nil {
		return nil, "", fmt.Errorf("world/catalog: listando tipos de edificio: %w", err)
	}

	next := ""
	if len(rows) > int(limit) {
		rows = rows[:limit]
		next = encodeCursor(cursorBuildingType, rows[len(rows)-1].ID)
	}
	types := make([]BuildingType, len(rows))
	for i, r := range rows {
		types[i] = BuildingType{
			ID: r.ID, Code: r.Code, Name: r.Name,
			FootprintCells: r.FootprintCells, MaxLevel: r.MaxLevel,
			BaseStorage: r.BaseStorage, PlacementRules: r.PlacementRules,
			LevelCurve: r.LevelCurve, BuildCost: r.BuildCost,
			MaintenanceCost: r.MaintenanceCost,
		}
	}
	return types, next, nil
}

// ─── Recetas ─────────────────────────────────────────────────────────────────

// ListRecipes devuelve las recetas que satisfacen el filtro (con sus
// ingredientes) y el cursor de la página siguiente.
func (s *Service) ListRecipes(ctx context.Context, f RecipeFilter) ([]Recipe, string, error) {
	after, err := decodeAfter(f.Cursor, cursorRecipe)
	if err != nil {
		return nil, "", err
	}
	limit := normalizeLimit(f.Limit)

	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	rows, err := s.q.ListRecipes(ctx, sqlcgen.ListRecipesParams{
		BuildingTypeID: f.BuildingTypeID,
		ProductID:      f.ProductID,
		AfterID:        after,
		PageLimit:      limit + 1,
	})
	if err != nil {
		return nil, "", fmt.Errorf("world/catalog: listando recetas: %w", err)
	}

	next := ""
	if len(rows) > int(limit) {
		rows = rows[:limit]
		next = encodeCursor(cursorRecipe, rows[len(rows)-1].ID)
	}

	recipes := make([]Recipe, len(rows))
	ids := make([]uuid.UUID, len(rows))
	byID := make(map[uuid.UUID]int, len(rows))
	for i, r := range rows {
		recipes[i] = Recipe{
			ID: r.ID, BuildingTypeID: r.BuildingTypeID, Code: r.Code, Name: r.Name,
			BatchSimSeconds: r.BatchSimSeconds, FuelProductID: r.FuelProductID,
			FuelPerBatch: r.FuelPerBatch, WorkersRequired: r.WorkersRequired,
			MinCityLevel: r.MinCityLevel, ChangeoverSeconds: r.ChangeoverSeconds,
			Ingredients: []RecipeIngredient{},
		}
		ids[i] = r.ID
		byID[r.ID] = i
	}

	if len(ids) > 0 {
		ings, err := s.q.ListRecipeIngredients(ctx, ids)
		if err != nil {
			return nil, "", fmt.Errorf("world/catalog: listando ingredientes de recetas: %w", err)
		}
		for _, ing := range ings {
			idx, ok := byID[ing.RecipeID]
			if !ok {
				continue // defensivo: ingrediente sin receta en la página
			}
			recipes[idx].Ingredients = append(recipes[idx].Ingredients, RecipeIngredient{
				ProductID: ing.ProductID,
				Role:      string(ing.Role),
				Quantity:  ing.Quantity,
			})
		}
	}
	return recipes, next, nil
}

// ─── Yacimientos ─────────────────────────────────────────────────────────────

// ListResourceDeposits devuelve los yacimientos que satisfacen el filtro y el
// cursor de la página siguiente.
func (s *Service) ListResourceDeposits(ctx context.Context, f DepositFilter) ([]ResourceDeposit, string, error) {
	after, err := decodeAfter(f.Cursor, cursorDeposit)
	if err != nil {
		return nil, "", err
	}
	limit := normalizeLimit(f.Limit)

	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	rows, err := s.q.ListResourceDeposits(ctx, sqlcgen.ListResourceDepositsParams{
		RegionID:      f.RegionID,
		ProductID:     f.ProductID,
		OnlyAvailable: f.OnlyAvailable,
		AfterID:       after,
		PageLimit:     limit + 1,
	})
	if err != nil {
		return nil, "", fmt.Errorf("world/catalog: listando yacimientos: %w", err)
	}

	next := ""
	if len(rows) > int(limit) {
		rows = rows[:limit]
		next = encodeCursor(cursorDeposit, rows[len(rows)-1].ID)
	}
	deposits := make([]ResourceDeposit, len(rows))
	for i, r := range rows {
		deposits[i] = ResourceDeposit{
			ID: r.ID, RegionID: r.RegionID, ProductID: r.ProductID,
			Location: r.Location, InitialAmount: r.InitialAmount,
			RemainingAmount: r.RemainingAmount, Renewable: r.Renewable,
			RegenPerSimDay: r.RegenPerSimDay,
		}
	}
	return deposits, next, nil
}

// ─── Ciudades ────────────────────────────────────────────────────────────────

// ListCities devuelve las ciudades que satisfacen el filtro y el cursor de la
// página siguiente.
func (s *Service) ListCities(ctx context.Context, f CityFilter) ([]City, string, error) {
	after, err := decodeAfter(f.Cursor, cursorCity)
	if err != nil {
		return nil, "", err
	}
	limit := normalizeLimit(f.Limit)

	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	rows, err := s.q.ListCities(ctx, sqlcgen.ListCitiesParams{
		RegionID:  f.RegionID,
		MinLevel:  f.MinLevel,
		AfterID:   after,
		PageLimit: limit + 1,
	})
	if err != nil {
		return nil, "", fmt.Errorf("world/catalog: listando ciudades: %w", err)
	}

	next := ""
	if len(rows) > int(limit) {
		rows = rows[:limit]
		next = encodeCursor(cursorCity, rows[len(rows)-1].ID)
	}
	cities := make([]City, len(rows))
	for i, r := range rows {
		cities[i] = City{
			ID: r.ID, RegionID: r.RegionID, AccountID: r.AccountID, Name: r.Name,
			Location: r.Location, Level: r.Level, Population: r.Population,
			SupplyIndex: r.SupplyIndex, InfluenceRadiusM: r.InfluenceRadiusM,
			BaseSalary: r.BaseSalary,
		}
	}
	return cities, next, nil
}

// GetCity devuelve una ciudad por id (ErrCityNotFound si no existe).
func (s *Service) GetCity(ctx context.Context, id uuid.UUID) (City, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()
	r, err := s.q.GetCity(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return City{}, ErrCityNotFound
		}
		return City{}, fmt.Errorf("world/catalog: obteniendo la ciudad %s: %w", id, err)
	}
	return City{
		ID: r.ID, RegionID: r.RegionID, AccountID: r.AccountID, Name: r.Name,
		Location: r.Location, Level: r.Level, Population: r.Population,
		SupplyIndex: r.SupplyIndex, InfluenceRadiusM: r.InfluenceRadiusM,
		BaseSalary: r.BaseSalary,
	}, nil
}

// ListCityDemand devuelve la curva de demanda vigente de una ciudad, con filtro
// opcional por producto. Distingue 404 (ciudad inexistente, ErrCityNotFound) de
// una ciudad sin filas de demanda (lista vacía).
func (s *Service) ListCityDemand(ctx context.Context, cityID uuid.UUID, productID *uuid.UUID) ([]CityDemand, error) {
	ctx, cancel := s.withTimeout(ctx)
	defer cancel()

	exists, err := s.q.CityExists(ctx, cityID)
	if err != nil {
		return nil, fmt.Errorf("world/catalog: comprobando la ciudad %s: %w", cityID, err)
	}
	if !exists {
		return nil, ErrCityNotFound
	}

	rows, err := s.q.ListCityDemand(ctx, sqlcgen.ListCityDemandParams{
		CityID:    cityID,
		ProductID: productID,
	})
	if err != nil {
		return nil, fmt.Errorf("world/catalog: listando la demanda de la ciudad %s: %w", cityID, err)
	}
	demand := make([]CityDemand, len(rows))
	for i, r := range rows {
		demand[i] = CityDemand{
			CityID: r.CityID, ProductID: r.ProductID, D0PerSimDay: r.D0PerSimDay,
			SaturationFactor: r.SaturationFactor, CurrentPrice: r.CurrentPrice,
			UnlockedAtLevel: r.UnlockedAtLevel, UpdatedAtSim: r.UpdatedAtSim,
		}
	}
	return demand, nil
}
