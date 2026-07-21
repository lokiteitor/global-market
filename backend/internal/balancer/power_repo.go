package balancer

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/lokiteitor/global-market/backend/internal/balancer/sqlcgen"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// Acceso a datos del tick del mercado spot eléctrico (PowerWorker). Mismo Repo
// del módulo: queries sqlc propias contra world.* y ledger.* (la frontera de
// servicio es de código Go, no de esquema).

// powerRegion es una región con pool eléctrico (>= 1 línea operativa).
type powerRegion struct {
	ID          uuid.UUID
	Name        string
	LastTickSim int64 // -1 si nunca hubo tick
}

// ListPowerRegions lista las regiones con líneas operativas y su último tick.
func (r *Repo) ListPowerRegions(ctx context.Context) ([]powerRegion, error) {
	rows, err := r.q.ListPowerRegions(ctx)
	if err != nil {
		return nil, fmt.Errorf("balancer: listando regiones con red eléctrica: %w", err)
	}
	out := make([]powerRegion, 0, len(rows))
	for _, row := range rows {
		out = append(out, powerRegion{ID: row.RegionID, Name: row.Name, LastTickSim: row.LastTickSim})
	}
	return out, nil
}

// ExistsPowerSpotTick es la guarda de idempotencia del tick.
func (r *Repo) ExistsPowerSpotTick(ctx context.Context, region uuid.UUID, tickSim int64) (bool, error) {
	present, err := r.q.ExistsPowerSpotTick(ctx, sqlcgen.ExistsPowerSpotTickParams{
		RegionID: region, TickSim: tickSim,
	})
	if err != nil {
		return false, fmt.Errorf("balancer: comprobando el tick %d de %s: %w", tickSim, region, err)
	}
	return present, nil
}

// powerConsumerRow es un lote candidato de la demanda (puede haber más de una
// fila por edificio; el worker deduplica).
type powerConsumerRow struct {
	BatchID            uuid.UUID
	BuildingID         uuid.UUID
	OwnerAccountID     uuid.UUID
	LastCurtailedAtSim int64
	PowerPerHour       int64
	BidPrice           int64 // 0 = sin puja explícita (rige el default)
	OwnerCash          int64
}

// ListPowerConsumers lista la demanda candidata de la región (conectada).
func (r *Repo) ListPowerConsumers(ctx context.Context, region uuid.UUID, connectRadiusM int64) ([]powerConsumerRow, error) {
	rows, err := r.q.ListPowerConsumers(ctx, sqlcgen.ListPowerConsumersParams{
		RegionID: region, ConnectRadiusM: float64(connectRadiusM),
	})
	if err != nil {
		return nil, fmt.Errorf("balancer: listando consumidores eléctricos de %s: %w", region, err)
	}
	out := make([]powerConsumerRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, powerConsumerRow{
			BatchID:            row.BatchID,
			BuildingID:         row.BuildingID,
			OwnerAccountID:     row.OwnerAccountID,
			LastCurtailedAtSim: row.LastCurtailedAtSim,
			PowerPerHour:       row.PowerPerHour,
			BidPrice:           row.BidPrice,
			OwnerCash:          row.OwnerCash,
		})
	}
	return out, nil
}

// powerGeneratorRow es una central candidata de la oferta (conectada, con
// oferta publicada).
type powerGeneratorRow struct {
	BuildingID     uuid.UUID
	OwnerAccountID uuid.UUID
	Level          int32
	LevelCurve     []byte
	Capacity       int64
	FuelProductID  *uuid.UUID
	FuelPerUnit    int64
	FuelCode       string
	OfferPrice     int64
	FuelPhysical   int64
	FuelLedger     int64
}

// ListPowerGenerators lista la oferta candidata de la región (conectada).
func (r *Repo) ListPowerGenerators(ctx context.Context, region uuid.UUID, connectRadiusM int64) ([]powerGeneratorRow, error) {
	rows, err := r.q.ListPowerGenerators(ctx, sqlcgen.ListPowerGeneratorsParams{
		RegionID: region, ConnectRadiusM: float64(connectRadiusM),
	})
	if err != nil {
		return nil, fmt.Errorf("balancer: listando generadores de %s: %w", region, err)
	}
	out := make([]powerGeneratorRow, 0, len(rows))
	for _, row := range rows {
		g := powerGeneratorRow{
			BuildingID:     row.BuildingID,
			OwnerAccountID: row.OwnerAccountID,
			Level:          row.Level,
			LevelCurve:     row.LevelCurve,
			Capacity:       row.Capacity,
			FuelProductID:  row.FuelProductID,
			FuelPerUnit:    row.FuelPerUnit,
			OfferPrice:     row.OfferPrice,
			FuelPhysical:   row.FuelPhysical,
			FuelLedger:     row.FuelLedger,
		}
		if row.FuelCode != nil {
			g.FuelCode = *row.FuelCode
		}
		out = append(out, g)
	}
	return out, nil
}

// InsertPowerSpotTick registra el resultado agregado del tick.
func (r *Repo) InsertPowerSpotTick(ctx context.Context, res sqlcgen.InsertPowerSpotTickParams) error {
	if err := r.q.InsertPowerSpotTick(ctx, res); err != nil {
		return fmt.Errorf("balancer: registrando el tick %d de %s: %w", res.TickSim, res.RegionID, err)
	}
	return nil
}

// InsertPowerDispatch registra un despacho/consumo del tick.
func (r *Repo) InsertPowerDispatch(ctx context.Context, d sqlcgen.InsertPowerDispatchParams) error {
	if err := r.q.InsertPowerDispatch(ctx, d); err != nil {
		return fmt.Errorf("balancer: registrando el despacho de %s: %w", d.BuildingID, err)
	}
	return nil
}

// SetBuildingPowered fija la cobertura de suministro de un edificio: until y
// la tasa facturada al servir; (tick, 0) al no servir (cierra la gracia
// residual del tick anterior).
func (r *Repo) SetBuildingPowered(ctx context.Context, building uuid.UUID, until, rate int64, simNow simtime.SimTime) error {
	if err := r.q.SetBuildingPowered(ctx, sqlcgen.SetBuildingPoweredParams{
		PoweredUntilSim: until, PoweredRate: rate, SimNow: int64(simNow), ID: building,
	}); err != nil {
		return fmt.Errorf("balancer: marcando suministro de %s: %w", building, err)
	}
	return nil
}

// MarkBuildingCurtailed sella la rotación del recorte de un edificio.
func (r *Repo) MarkBuildingCurtailed(ctx context.Context, building uuid.UUID, simNow simtime.SimTime) error {
	if err := r.q.MarkBuildingCurtailed(ctx, sqlcgen.MarkBuildingCurtailedParams{
		SimNow: int64(simNow), ID: building,
	}); err != nil {
		return fmt.Errorf("balancer: sellando recorte de %s: %w", building, err)
	}
	return nil
}

// PauseRunningBatchesNoPower pausa los lotes en marcha de un edificio sin
// suministro; devuelve los ids pausados.
func (r *Repo) PauseRunningBatchesNoPower(ctx context.Context, building uuid.UUID, simNow simtime.SimTime) ([]uuid.UUID, error) {
	ids, err := r.q.PauseRunningBatchesNoPower(ctx, sqlcgen.PauseRunningBatchesNoPowerParams{
		SimNow: int64(simNow), BuildingID: building,
	})
	if err != nil {
		return nil, fmt.Errorf("balancer: pausando lotes de %s: %w", building, err)
	}
	return ids, nil
}

// ResumeNoPowerBatches reanuda los lotes pausados por suministro de un
// edificio servido; devuelve los ids reanudados.
func (r *Repo) ResumeNoPowerBatches(ctx context.Context, building uuid.UUID, simNow simtime.SimTime) ([]uuid.UUID, error) {
	now := int64(simNow)
	ids, err := r.q.ResumeNoPowerBatches(ctx, sqlcgen.ResumeNoPowerBatchesParams{
		SimNow: &now, BuildingID: building,
	})
	if err != nil {
		return nil, fmt.Errorf("balancer: reanudando lotes de %s: %w", building, err)
	}
	return ids, nil
}

// RefreshPlantFuelMirror refresca la columna espejo fuel_stock de una térmica.
func (r *Repo) RefreshPlantFuelMirror(ctx context.Context, building, product uuid.UUID) error {
	if err := r.q.RefreshPlantFuelMirror(ctx, sqlcgen.RefreshPlantFuelMirrorParams{
		BuildingID: building, ProductID: product,
	}); err != nil {
		return fmt.Errorf("balancer: refrescando fuel_stock de %s: %w", building, err)
	}
	return nil
}
