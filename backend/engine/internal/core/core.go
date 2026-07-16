// Package core contiene los tipos y funciones puras compartidos entre los
// módulos del motor (sin dependencias de base de datos). Es el único punto de
// import cruzado permitido entre procesadores.
package core

import (
	"context"
	"fmt"
	"math"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier es la interfaz mínima de acceso a datos que satisfacen pgx.Tx y
// *pgxpool.Pool. Los módulos dependen de ella, no de pgx directamente.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// BankRefs referencia las cuentas de sistema del banco central, resueltas una
// vez en el arranque.
type BankRefs struct {
	BancoCentralID  uuid.UUID // auth.accounts del Banco Central (dueño de emisiones de stock)
	SinkAccountID   uuid.UUID // ledger.accounts kind='sink'
	EmissionMoneyID uuid.UUID // ledger.accounts kind='emission' product IS NULL
}

// AdvanceFn es el contrato compartido engine<->gateway para
// world.vehicles.advance_fn (JSONB). Ver tarea, punto 2 del contrato.
type AdvanceFn struct {
	Path               []uuid.UUID `json:"path"`                 // link_segments en orden de viaje
	LegIndex           int         `json:"leg_index"`            // índice del segmento actual dentro de Path
	DurationSimSeconds int64       `json:"duration_sim_seconds"` // duración del segmento ACTUAL
	SpeedKmhEff        float64     `json:"speed_kmh_eff"`
	DestNodeID         uuid.UUID   `json:"dest_node_id"`
	ContractID         *uuid.UUID  `json:"contract_id"`
}

// Money serializa un importe/stock BIGINT como string decimal (convención
// JSON del proyecto: nunca floats para valor económico).
func Money(v int64) string { return strconv.FormatInt(v, 10) }

// FormatSimTime convierte sim_seconds al formato legible A-DDD-HH:MM
// (año = floor(dias/360)+1, DDD día del año 001..360). ADR-IMPL-06.
func FormatSimTime(simSeconds int64) string {
	if simSeconds < 0 {
		simSeconds = 0
	}
	days := simSeconds / 86400
	year := days/360 + 1
	ddd := days%360 + 1
	hh := (simSeconds % 86400) / 3600
	mm := (simSeconds % 3600) / 60
	return fmt.Sprintf("%d-%03d-%02d:%02d", year, ddd, hh, mm)
}

// ParseSimTime invierte FormatSimTime (con precisión de minuto).
func ParseSimTime(s string) (int64, error) {
	var year, ddd, hh, mm int64
	if _, err := fmt.Sscanf(s, "%d-%d-%d:%d", &year, &ddd, &hh, &mm); err != nil {
		return 0, fmt.Errorf("sim_time inválido %q: %w", s, err)
	}
	if year < 1 || ddd < 1 || ddd > 360 || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, fmt.Errorf("sim_time fuera de rango: %q", s)
	}
	days := (year-1)*360 + (ddd - 1)
	return days*86400 + hh*3600 + mm*60, nil
}

// CityPrice aplica la fórmula del Balancer (GDD 5.6 simplificada):
//
//	ratio  = d0 / max(supply_ema, 1)
//	k      = 0.3 (basic) | 0.7 (luxury)
//	precio = clamp(round(base * ratio^k), floor, ceiling)
//	saturación = clamp(1/max(ratio, 0.1), 0, 10)
func CityPrice(basePrice, priceFloor, priceCeiling int64, d0 int64, supplyEma float64, class string) (price int64, saturation float64) {
	ratio := float64(d0) / math.Max(supplyEma, 1)
	k := 0.3
	if class == "luxury" {
		k = 0.7
	}
	raw := math.Round(float64(basePrice) * math.Pow(ratio, k))
	price = int64(raw)
	if price < priceFloor {
		price = priceFloor
	}
	if price > priceCeiling {
		price = priceCeiling
	}
	saturation = 1 / math.Max(ratio, 0.1)
	if saturation < 0 {
		saturation = 0
	}
	if saturation > 10 {
		saturation = 10
	}
	return price, saturation
}

// SegmentDuration calcula la velocidad efectiva y la duración sim de un tramo:
// speed_kmh_eff = min(vehículo, base del enlace) / congestion_ema;
// duración = round(length_m * 3.6 / speed_kmh_eff).
func SegmentDuration(lengthM int64, baseSpeedKmh, vehicleSpeedKmh int64, congestion float64) (durationSimSeconds int64, speedKmhEff float64) {
	if congestion < 1 {
		// La EMA de congestión está acotada en [1,10]; defensivo ante datos raros.
		congestion = 1
	}
	speed := float64(min64(baseSpeedKmh, vehicleSpeedKmh))
	speedKmhEff = speed / congestion
	if speedKmhEff <= 0 {
		speedKmhEff = 1
	}
	durationSimSeconds = int64(math.Round(float64(lengthM) * 3.6 / speedKmhEff))
	if durationSimSeconds < 1 {
		durationSimSeconds = 1
	}
	return durationSimSeconds, speedKmhEff
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
