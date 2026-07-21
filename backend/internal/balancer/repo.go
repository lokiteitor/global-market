package balancer

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lokiteitor/global-market/backend/internal/balancer/sqlcgen"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// Kinds de asiento del ledger que el Balancer escribe (enums de 0004_ledger; se
// referencian por su alias generado). El fondeo de ciudad reutiliza seed_capital
// (emisión del banco central: +caja/-emisión), el mismo mecanismo que el capital
// semilla — NO se añade un kind al enum (evita migración de enum, decisión del
// Incremento 6b). El consumo urbano usa consumption (ADR-022).
const (
	txKindCityFunding = sqlcgen.LedgerTransactionKindSeedCapital
	txKindConsumption = sqlcgen.LedgerTransactionKindConsumption
)

// Repo es la capa de acceso a datos del Balancer sobre el código generado por
// sqlc. No abre transacciones: el worker/consumer decide el ámbito transaccional
// y deriva un Repo con WithTx.
type Repo struct {
	q *sqlcgen.Queries
}

// NewRepo construye el repositorio sobre un pool o una transacción pgx.
func NewRepo(db sqlcgen.DBTX) *Repo {
	return &Repo{q: sqlcgen.New(db)}
}

// WithTx devuelve un Repo que ejecuta sus queries dentro de tx.
func (r *Repo) WithTx(tx pgx.Tx) *Repo {
	return &Repo{q: r.q.WithTx(tx)}
}

// ─── Ciudades ────────────────────────────────────────────────────────────────

// city es la vista de una ciudad para el recálculo y las métricas.
type city struct {
	ID           uuid.UUID
	RegionID     uuid.UUID
	AccountID    uuid.UUID
	Name         string
	Level        int32
	Population   int64
	SupplyIndex  float64
	BaseSalary   int64
	UpdatedAtSim int64
}

// ListCities devuelve todas las ciudades.
func (r *Repo) ListCities(ctx context.Context) ([]city, error) {
	rows, err := r.q.ListCities(ctx)
	if err != nil {
		return nil, fmt.Errorf("balancer: listando ciudades: %w", err)
	}
	out := make([]city, len(rows))
	for i, row := range rows {
		out[i] = city{
			ID: row.ID, RegionID: row.RegionID, AccountID: row.AccountID, Name: row.Name,
			Level: row.Level, Population: row.Population, SupplyIndex: row.SupplyIndex,
			BaseSalary: row.BaseSalary, UpdatedAtSim: row.UpdatedAtSim,
		}
	}
	return out, nil
}

// LockCity bloquea una ciudad (FOR UPDATE); pgx.ErrNoRows si desapareció.
func (r *Repo) LockCity(ctx context.Context, id uuid.UUID) (city, error) {
	row, err := r.q.LockCity(ctx, id)
	if err != nil {
		return city{}, err
	}
	return city{
		ID: row.ID, RegionID: row.RegionID, AccountID: row.AccountID, Name: row.Name,
		Level: row.Level, Population: row.Population, SupplyIndex: row.SupplyIndex,
		BaseSalary: row.BaseSalary, UpdatedAtSim: row.UpdatedAtSim,
	}, nil
}

// UpdateCityGrowth escribe nivel, población, supply_index y updated_at_sim.
func (r *Repo) UpdateCityGrowth(ctx context.Context, id uuid.UUID, level int32, population int64, supplyIndex float64, simNow simtime.SimTime) error {
	if err := r.q.UpdateCityGrowth(ctx, sqlcgen.UpdateCityGrowthParams{
		ID: id, Level: level, Population: population, SupplyIndex: supplyIndex, UpdatedAtSim: int64(simNow),
	}); err != nil {
		return fmt.Errorf("balancer: actualizando crecimiento de la ciudad %s: %w", id, err)
	}
	return nil
}

// AddCitySupplyIndex incrementa supply_index (consumer) y devuelve el resultante.
func (r *Repo) AddCitySupplyIndex(ctx context.Context, id uuid.UUID, delta float64) (float64, error) {
	v, err := r.q.AddCitySupplyIndex(ctx, sqlcgen.AddCitySupplyIndexParams{ID: id, Delta: delta})
	if err != nil {
		return 0, fmt.Errorf("balancer: incrementando supply_index de %s: %w", id, err)
	}
	return v, nil
}

// ─── Curva de demanda ─────────────────────────────────────────────────────────

// demandRow es una fila de la curva de demanda para el recálculo.
type demandRow struct {
	ProductID        uuid.UUID
	D0PerSimDay      int64
	SupplyEMA        float64
	SaturationFactor float64
	CurrentPrice     int64
	UnlockedAtLevel  int32
	RecentSupply     int64
	UpdatedAtSim     int64
}

// ListCityDemand devuelve la curva de demanda completa de una ciudad.
func (r *Repo) ListCityDemand(ctx context.Context, cityID uuid.UUID) ([]demandRow, error) {
	rows, err := r.q.ListCityDemand(ctx, cityID)
	if err != nil {
		return nil, fmt.Errorf("balancer: listando la demanda de la ciudad %s: %w", cityID, err)
	}
	out := make([]demandRow, len(rows))
	for i, row := range rows {
		out[i] = demandRow{
			ProductID: row.ProductID, D0PerSimDay: row.D0PerSimDay, SupplyEMA: row.SupplyEma,
			SaturationFactor: row.SaturationFactor, CurrentPrice: row.CurrentPrice,
			UnlockedAtLevel: row.UnlockedAtLevel, RecentSupply: row.RecentSupply, UpdatedAtSim: row.UpdatedAtSim,
		}
	}
	return out, nil
}

// UpdateCityDemandCurve escribe el resultado del recálculo de una (ciudad,
// producto) y resetea recent_supply a 0.
func (r *Repo) UpdateCityDemandCurve(ctx context.Context, cityID, productID uuid.UUID, supplyEMA, saturation float64, currentPrice int64, simNow simtime.SimTime) error {
	if err := r.q.UpdateCityDemandCurve(ctx, sqlcgen.UpdateCityDemandCurveParams{
		CityID: cityID, ProductID: productID, SupplyEma: supplyEMA, SaturationFactor: saturation,
		CurrentPrice: currentPrice, UpdatedAtSim: int64(simNow),
	}); err != nil {
		return fmt.Errorf("balancer: actualizando la curva de (%s, %s): %w", cityID, productID, err)
	}
	return nil
}

// GrowCityDemandD0 escala D0 de todas las filas de una ciudad por factorBP/10000.
func (r *Repo) GrowCityDemandD0(ctx context.Context, cityID uuid.UUID, factorBP int64) error {
	if err := r.q.GrowCityDemandD0(ctx, sqlcgen.GrowCityDemandD0Params{CityID: cityID, FactorBp: factorBP}); err != nil {
		return fmt.Errorf("balancer: escalando D0 de la ciudad %s: %w", cityID, err)
	}
	return nil
}

// AddRecentSupply acumula la oferta reciente de una (ciudad, producto) y devuelve
// el acumulado resultante; found=false si no hay curva para ese producto.
func (r *Repo) AddRecentSupply(ctx context.Context, cityID, productID uuid.UUID, qty int64) (resulting int64, found bool, err error) {
	v, err := r.q.AddRecentSupply(ctx, sqlcgen.AddRecentSupplyParams{CityID: cityID, ProductID: productID, Qty: qty})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return 0, false, nil
	case err != nil:
		return 0, false, fmt.Errorf("balancer: acumulando oferta reciente de (%s, %s): %w", cityID, productID, err)
	}
	return v, true, nil
}

// ─── Productos ────────────────────────────────────────────────────────────────

// productAnchor es el ancla de precio y la clase de un producto.
type productAnchor struct {
	BasePrice    int64
	PriceFloor   int64
	PriceCeiling int64
	Class        string
}

// GetProduct devuelve el ancla de precio de un producto; pgx.ErrNoRows si no existe.
func (r *Repo) GetProduct(ctx context.Context, id uuid.UUID) (productAnchor, error) {
	row, err := r.q.GetProduct(ctx, id)
	if err != nil {
		return productAnchor{}, err
	}
	return productAnchor{
		BasePrice: row.BasePrice, PriceFloor: row.PriceFloor, PriceCeiling: row.PriceCeiling, Class: string(row.Class),
	}, nil
}

// ─── Tablón / identidad / contratos ───────────────────────────────────────────

// CountLiveCityBuys cuenta las buys vivas de una ciudad para un producto.
func (r *Repo) CountLiveCityBuys(ctx context.Context, cityAccount, productID uuid.UUID) (int64, error) {
	p := productID
	n, err := r.q.CountLiveCityBuys(ctx, sqlcgen.CountLiveCityBuysParams{PublisherAccountID: cityAccount, ProductID: &p})
	if err != nil {
		return 0, fmt.Errorf("balancer: contando buys vivas de (%s, %s): %w", cityAccount, productID, err)
	}
	return n, nil
}

// IsCityAccount indica si una cuenta es una ciudad.
func (r *Repo) IsCityAccount(ctx context.Context, id uuid.UUID) (bool, error) {
	ok, err := r.q.IsCityAccount(ctx, id)
	if err != nil {
		return false, fmt.Errorf("balancer: comprobando si %s es ciudad: %w", id, err)
	}
	return ok, nil
}

// settledContract es la vista del contrato liquidado para consumir la entrega.
type settledContract struct {
	BuyerAccountID    uuid.UUID
	ProductID         uuid.UUID
	DestinationNodeID uuid.UUID
	QuantityDelivered int64
	Status            string
}

// GetContractForConsume devuelve los datos del contrato; pgx.ErrNoRows si no existe.
func (r *Repo) GetContractForConsume(ctx context.Context, id uuid.UUID) (settledContract, error) {
	row, err := r.q.GetContractForConsume(ctx, id)
	if err != nil {
		return settledContract{}, err
	}
	return settledContract{
		BuyerAccountID: row.BuyerAccountID, ProductID: row.ProductID, DestinationNodeID: row.DestinationNodeID,
		QuantityDelivered: row.QuantityDelivered, Status: string(row.Status),
	}, nil
}

// nodeBuilding es el edificio y la ciudad ligados a un nodo del grafo.
type nodeBuilding struct {
	BuildingID *uuid.UUID
	CityID     *uuid.UUID
	RegionID   uuid.UUID
}

// GetNodeBuilding devuelve el edificio y la ciudad de un nodo; pgx.ErrNoRows si
// no existe.
func (r *Repo) GetNodeBuilding(ctx context.Context, id uuid.UUID) (nodeBuilding, error) {
	row, err := r.q.GetNodeBuilding(ctx, id)
	if err != nil {
		return nodeBuilding{}, err
	}
	return nodeBuilding{BuildingID: row.BuildingID, CityID: row.CityID, RegionID: row.RegionID}, nil
}

// GetCityDistributionNode devuelve el nodo del centro de distribución de una
// ciudad (destino de sus buys); pgx.ErrNoRows si no está sembrado.
func (r *Repo) GetCityDistributionNode(ctx context.Context, cityID uuid.UUID) (uuid.UUID, error) {
	c := cityID
	id, err := r.q.GetCityDistributionNode(ctx, &c)
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// ─── Inventario físico ────────────────────────────────────────────────────────

// ConsumeBuildingInventory descuenta stock físico consumido por la ciudad.
func (r *Repo) ConsumeBuildingInventory(ctx context.Context, buildingID, productID uuid.UUID, amount int64, simNow simtime.SimTime) error {
	if err := r.q.ConsumeBuildingInventory(ctx, sqlcgen.ConsumeBuildingInventoryParams{
		BuildingID: buildingID, ProductID: productID, Amount: amount, SimNow: int64(simNow),
	}); err != nil {
		return fmt.Errorf("balancer: consumiendo inventario físico (%s, %s, -%d): %w", buildingID, productID, amount, err)
	}
	return nil
}

// ─── Ledger ───────────────────────────────────────────────────────────────────

// ledgerAccount es la vista mínima de una cuenta del ledger (id y saldo).
type ledgerAccount struct {
	ID      uuid.UUID
	Balance int64
}

// EnsureCashAccount localiza (o crea on-demand) la caja de una cuenta.
func (r *Repo) EnsureCashAccount(ctx context.Context, owner uuid.UUID) (ledgerAccount, error) {
	o := owner
	row, err := r.q.GetCashAccount(ctx, &o)
	switch {
	case err == nil:
		return ledgerAccount{ID: row.ID, Balance: row.Balance}, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return ledgerAccount{}, fmt.Errorf("balancer: consultando la caja de %s: %w", owner, err)
	}
	id, err := newUUIDv7()
	if err != nil {
		return ledgerAccount{}, err
	}
	created, err := r.q.CreateCashAccount(ctx, sqlcgen.CreateCashAccountParams{ID: id, OwnerAccountID: &o})
	if err != nil {
		return ledgerAccount{}, fmt.Errorf("balancer: creando la caja de %s: %w", owner, err)
	}
	return ledgerAccount{ID: created.ID, Balance: created.Balance}, nil
}

// GetEmissionAccount devuelve la cuenta de emisión del banco central;
// pgx.ErrNoRows si el seed no la creó.
func (r *Repo) GetEmissionAccount(ctx context.Context) (ledgerAccount, error) {
	row, err := r.q.GetEmissionAccount(ctx)
	if err != nil {
		return ledgerAccount{}, err
	}
	return ledgerAccount{ID: row.ID, Balance: row.Balance}, nil
}

// GetStockFreeAccount devuelve la cuenta stock_free de (dueño, producto,
// almacén); pgx.ErrNoRows si no existe.
func (r *Repo) GetStockFreeAccount(ctx context.Context, owner, product, warehouse uuid.UUID) (ledgerAccount, error) {
	o, p, w := owner, product, warehouse
	row, err := r.q.GetStockFreeAccount(ctx, sqlcgen.GetStockFreeAccountParams{OwnerAccountID: &o, ProductID: &p, WarehouseBuildingID: &w})
	if err != nil {
		return ledgerAccount{}, err
	}
	return ledgerAccount{ID: row.ID, Balance: row.Balance}, nil
}

// EnsureWorldSourceAccount localiza (o crea on-demand) la cuenta world_source de
// un producto (contrapartida física del banco central, ADR-022).
func (r *Repo) EnsureWorldSourceAccount(ctx context.Context, product uuid.UUID) (uuid.UUID, error) {
	p := product
	row, err := r.q.GetWorldSourceAccount(ctx, &p)
	switch {
	case err == nil:
		return row.ID, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return uuid.Nil, fmt.Errorf("balancer: consultando world_source de %s: %w", product, err)
	}
	owner, err := r.q.GetWorldSourceOwner(ctx)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("balancer: localizando el banco central: %w", err)
	}
	id, err := newUUIDv7()
	if err != nil {
		return uuid.Nil, err
	}
	created, err := r.q.CreateWorldSourceAccount(ctx, sqlcgen.CreateWorldSourceAccountParams{ID: id, OwnerAccountID: owner, ProductID: &p})
	if err != nil {
		return uuid.Nil, fmt.Errorf("balancer: creando world_source de %s: %w", product, err)
	}
	return created.ID, nil
}

// entryAmount es una partida de un asiento del ledger (importe con signo).
type entryAmount struct {
	AccountID uuid.UUID
	Amount    int64
}

// PostLedgerTransaction asienta cabecera + partidas dentro de la transacción SQL
// del Repo (los triggers de 0004 garantizan saldo, no-negatividad y doble
// entrada por activo). Los IDs (UUIDv7) los genera la aplicación (ADR-018).
func (r *Repo) PostLedgerTransaction(ctx context.Context, kind sqlcgen.LedgerTransactionKind, simAt simtime.SimTime, reference uuid.UUID, description string, entries []entryAmount) (uuid.UUID, error) {
	txID, err := newUUIDv7()
	if err != nil {
		return uuid.Nil, err
	}
	var desc *string
	if description != "" {
		desc = &description
	}
	ref := reference
	if err := r.q.InsertLedgerTransaction(ctx, sqlcgen.InsertLedgerTransactionParams{
		ID: txID, Kind: kind, SimTimeAt: int64(simAt), ReferenceID: &ref, Description: desc,
	}); err != nil {
		return uuid.Nil, fmt.Errorf("balancer: asentando la cabecera %s de %s: %w", kind, reference, err)
	}
	for _, e := range entries {
		entryID, err := newUUIDv7()
		if err != nil {
			return uuid.Nil, err
		}
		if err := r.q.InsertLedgerEntry(ctx, sqlcgen.InsertLedgerEntryParams{
			ID: entryID, TransactionID: txID, AccountID: e.AccountID, Amount: e.Amount,
		}); err != nil {
			return uuid.Nil, fmt.Errorf("balancer: asentando la partida de %s (cuenta %s): %w", reference, e.AccountID, err)
		}
	}
	return txID, nil
}

// MoneySupply devuelve la masa monetaria total (cash+escrow+guarantee).
func (r *Repo) MoneySupply(ctx context.Context) (int64, error) {
	total, err := r.q.MoneySupply(ctx)
	if err != nil {
		return 0, fmt.Errorf("balancer: consultando la masa monetaria: %w", err)
	}
	return total, nil
}

// ─── Macro: analítica, fórmula laboral y ajuste fiscal (Incremento 6b) ─────────

// BucketGDP devuelve el PIB simulado del bucket [start, end).
func (r *Repo) BucketGDP(ctx context.Context, start, end simtime.SimTime) (int64, error) {
	v, err := r.q.BucketGdp(ctx, sqlcgen.BucketGdpParams{BucketStart: int64(start), BucketEnd: int64(end)})
	if err != nil {
		return 0, fmt.Errorf("balancer: PIB del bucket [%d,%d): %w", start, end, err)
	}
	return v, nil
}

// BucketEmission devuelve la emisión (faucet) del bucket [start, end).
func (r *Repo) BucketEmission(ctx context.Context, start, end simtime.SimTime) (int64, error) {
	v, err := r.q.BucketEmission(ctx, sqlcgen.BucketEmissionParams{BucketStart: int64(start), BucketEnd: int64(end)})
	if err != nil {
		return 0, fmt.Errorf("balancer: emisión del bucket [%d,%d): %w", start, end, err)
	}
	return v, nil
}

// BucketAbsorption devuelve la absorción (sinks) del bucket [start, end).
func (r *Repo) BucketAbsorption(ctx context.Context, start, end simtime.SimTime) (int64, error) {
	v, err := r.q.BucketAbsorption(ctx, sqlcgen.BucketAbsorptionParams{BucketStart: int64(start), BucketEnd: int64(end)})
	if err != nil {
		return 0, fmt.Errorf("balancer: absorción del bucket [%d,%d): %w", start, end, err)
	}
	return v, nil
}

// CountActiveAccounts devuelve las cuentas activas de bots y humanos.
func (r *Repo) CountActiveAccounts(ctx context.Context) (bots, humans int32, err error) {
	row, err := r.q.CountActiveAccounts(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("balancer: contando cuentas activas: %w", err)
	}
	return row.BotCount, row.HumanCount, nil
}

// depletionProduct es el agotamiento de un producto finito (código + magnitudes).
type depletionProduct struct {
	Code      string
	Remaining int64
	Extracted int64
}

// FiniteDepletionByProduct devuelve el agotamiento desglosado por producto finito.
func (r *Repo) FiniteDepletionByProduct(ctx context.Context) ([]depletionProduct, error) {
	rows, err := r.q.FiniteDepletionByProduct(ctx)
	if err != nil {
		return nil, fmt.Errorf("balancer: desglosando agotamiento por producto: %w", err)
	}
	out := make([]depletionProduct, len(rows))
	for i, row := range rows {
		out[i] = depletionProduct{Code: row.ProductCode, Remaining: row.Remaining, Extracted: row.Extracted}
	}
	return out, nil
}

// economyIndicators es la fila del bucket de analytics.economy_indicators.
type economyIndicators struct {
	BucketStart         simtime.SimTime
	MoneySupply         int64
	SimulatedGDP        int64
	EmissionTotal       int64
	AbsorptionTotal     int64
	ActiveBotCount      int32
	ActiveHumanCount    int32
	GlobalDepletionRate float64
	DepletionProjection []byte
}

// UpsertEconomyIndicators asienta (o reescribe) la fila del bucket.
func (r *Repo) UpsertEconomyIndicators(ctx context.Context, e economyIndicators) error {
	if err := r.q.UpsertEconomyIndicators(ctx, sqlcgen.UpsertEconomyIndicatorsParams{
		BucketStartSim:      int64(e.BucketStart),
		MoneySupply:         e.MoneySupply,
		SimulatedGdp:        e.SimulatedGDP,
		EmissionTotal:       e.EmissionTotal,
		AbsorptionTotal:     e.AbsorptionTotal,
		ActiveBotCount:      e.ActiveBotCount,
		ActiveHumanCount:    e.ActiveHumanCount,
		GlobalDepletionRate: e.GlobalDepletionRate,
		DepletionProjection: e.DepletionProjection,
	}); err != nil {
		return fmt.Errorf("balancer: upsert de economy_indicators (bucket %d): %w", e.BucketStart, err)
	}
	return nil
}

// regionFiscal es la vista de una región para el job (fiscalidad vigente).
type regionFiscal struct {
	ID        uuid.UUID
	TaxRateBP int32
	CanonBase int64
}

// ListRegions devuelve todas las regiones con su fiscalidad vigente.
func (r *Repo) ListRegions(ctx context.Context) ([]regionFiscal, error) {
	rows, err := r.q.ListRegions(ctx)
	if err != nil {
		return nil, fmt.Errorf("balancer: listando regiones: %w", err)
	}
	out := make([]regionFiscal, len(rows))
	for i, row := range rows {
		out[i] = regionFiscal{ID: row.ID, TaxRateBP: row.TaxRateBp, CanonBase: row.CanonBase}
	}
	return out, nil
}

// RegionActiveBuildings devuelve el nº de edificios operativos por región.
func (r *Repo) RegionActiveBuildings(ctx context.Context) (map[uuid.UUID]int32, error) {
	rows, err := r.q.RegionActiveBuildings(ctx)
	if err != nil {
		return nil, fmt.Errorf("balancer: contando edificios operativos por región: %w", err)
	}
	out := make(map[uuid.UUID]int32, len(rows))
	for _, row := range rows {
		out[row.RegionID] = row.ActiveBuildings
	}
	return out, nil
}

// regionSettled es el par (contratos liquidados, volumen) del bucket por región.
type regionSettled struct {
	ContractsSettled int32
	TradeVolume      int64
}

// RegionSettledStats devuelve, por región, los contratos liquidados y el volumen
// negociado del bucket [start, end) (atribuidos por nodo de destino).
func (r *Repo) RegionSettledStats(ctx context.Context, start, end simtime.SimTime) (map[uuid.UUID]regionSettled, error) {
	rows, err := r.q.RegionSettledStats(ctx, sqlcgen.RegionSettledStatsParams{BucketStart: int64(start), BucketEnd: int64(end)})
	if err != nil {
		return nil, fmt.Errorf("balancer: agregando contratos liquidados por región [%d,%d): %w", start, end, err)
	}
	out := make(map[uuid.UUID]regionSettled, len(rows))
	for _, row := range rows {
		out[row.RegionID] = regionSettled{ContractsSettled: row.ContractsSettled, TradeVolume: row.TradeVolume}
	}
	return out, nil
}

// UpsertRegionStats asienta (o reescribe) la fila (región, bucket).
func (r *Repo) UpsertRegionStats(ctx context.Context, regionID uuid.UUID, bucketStart simtime.SimTime, occupation float64, activeBuildings, contractsSettled int32, tradeVolume int64) error {
	if err := r.q.UpsertRegionStats(ctx, sqlcgen.UpsertRegionStatsParams{
		RegionID:             regionID,
		BucketStartSim:       int64(bucketStart),
		IndustrialOccupation: occupation,
		ActiveBuildings:      activeBuildings,
		ContractsSettled:     contractsSettled,
		TradeVolume:          tradeVolume,
	}); err != nil {
		return fmt.Errorf("balancer: upsert de region_stats (%s, bucket %d): %w", regionID, bucketStart, err)
	}
	return nil
}

// UpsertCitySnapshot asienta (o reescribe) la foto (ciudad, bucket).
func (r *Repo) UpsertCitySnapshot(ctx context.Context, cityID uuid.UUID, bucketStart simtime.SimTime, level int32, population int64, supplyIndex float64) error {
	if err := r.q.UpsertCitySnapshot(ctx, sqlcgen.UpsertCitySnapshotParams{
		CityID:         cityID,
		BucketStartSim: int64(bucketStart),
		Level:          level,
		Population:     population,
		SupplyIndex:    supplyIndex,
	}); err != nil {
		return fmt.Errorf("balancer: upsert de city_snapshots (%s, bucket %d): %w", cityID, bucketStart, err)
	}
	return nil
}

// LatestRegionOccupation devuelve la ocupación industrial más reciente por región.
func (r *Repo) LatestRegionOccupation(ctx context.Context) (map[uuid.UUID]float64, error) {
	rows, err := r.q.LatestRegionOccupation(ctx)
	if err != nil {
		return nil, fmt.Errorf("balancer: leyendo la ocupación industrial reciente: %w", err)
	}
	out := make(map[uuid.UUID]float64, len(rows))
	for _, row := range rows {
		out[row.RegionID] = row.IndustrialOccupation
	}
	return out, nil
}

// UpdateCityBaseSalary escribe el salario efectivo recalculado de una ciudad.
func (r *Repo) UpdateCityBaseSalary(ctx context.Context, id uuid.UUID, salary int64) error {
	if err := r.q.UpdateCityBaseSalary(ctx, sqlcgen.UpdateCityBaseSalaryParams{ID: id, BaseSalary: salary}); err != nil {
		return fmt.Errorf("balancer: actualizando base_salary de la ciudad %s: %w", id, err)
	}
	return nil
}

// macroPoint es un punto de la serie macro para la tendencia del lazo fiscal.
type macroPoint struct {
	BucketStart  simtime.SimTime
	MoneySupply  int64
	SimulatedGDP int64
}

// RecentEconomyIndicators devuelve los últimos `limit` indicadores macro (del más
// reciente al más antiguo).
func (r *Repo) RecentEconomyIndicators(ctx context.Context, limit int32) ([]macroPoint, error) {
	rows, err := r.q.RecentEconomyIndicators(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("balancer: leyendo indicadores macro recientes: %w", err)
	}
	out := make([]macroPoint, len(rows))
	for i, row := range rows {
		out[i] = macroPoint{BucketStart: simtime.SimTime(row.BucketStartSim), MoneySupply: row.MoneySupply, SimulatedGDP: row.SimulatedGdp}
	}
	return out, nil
}

// UpdateRegionFiscal escribe la fiscalidad ajustada de una región (dentro de rango).
func (r *Repo) UpdateRegionFiscal(ctx context.Context, id uuid.UUID, taxRateBP int32, canonBase int64) error {
	if err := r.q.UpdateRegionFiscal(ctx, sqlcgen.UpdateRegionFiscalParams{ID: id, TaxRateBp: taxRateBP, CanonBase: canonBase}); err != nil {
		return fmt.Errorf("balancer: actualizando la fiscalidad de la región %s: %w", id, err)
	}
	return nil
}

// newUUIDv7 genera un UUIDv7 (ADR-018: los IDs los produce la aplicación).
func newUUIDv7() (uuid.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("balancer: generando UUIDv7: %w", err)
	}
	return id, nil
}
