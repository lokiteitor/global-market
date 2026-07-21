package balancer

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// Variables de entorno del Balancer (prefijo II_, 12-factor). Las nombradas por
// el mandato de implementación se leen del entorno; los knobs de forma de la
// curva (elasticidad, clamps de saturación, crecimiento de ciudad, bono de
// variedad) tienen default documentado y se inyectan en tests vía Options.
const (
	// EnvDemandInterval es el periodo de recálculo de curvas y publicación de
	// buys de ciudad (time.ParseDuration). Default 60s.
	EnvDemandInterval = "II_BALANCER_DEMAND_INTERVAL"
	// EnvAnalyticsInterval es el periodo del refresco macro (gauges de masa
	// monetaria emitida y nivel por ciudad). Default 120s.
	EnvAnalyticsInterval = "II_BALANCER_ANALYTICS_INTERVAL"
	// EnvCityBuyDeadlineSim es el plazo de entrega de las buys de ciudad, en
	// sim-time. Default 172800 (2 días de juego).
	EnvCityBuyDeadlineSim = "II_CITY_BUY_DEADLINE_SIM"
	// EnvSupplyEMAAlpha es el peso de la muestra reciente en el EMA de oferta
	// (0 < alpha <= 1). Default 0.3.
	EnvSupplyEMAAlpha = "II_SUPPLY_EMA_ALPHA"
	// EnvSupplyEMAFloor es el SUELO del EMA de oferta (nunca 0, GDD 5.6). Default 1.
	EnvSupplyEMAFloor = "II_SUPPLY_EMA_FLOOR"
	// EnvLevelupIndexBase es el umbral base de supply_index para subir de nivel,
	// escalado por nivel (umbral(nivel) = base * nivel). Default 100000.
	EnvLevelupIndexBase = "II_CITY_LEVELUP_INDEX_BASE"
	// EnvSupplyIndexDecayPerSimDay es el decaimiento de supply_index por día de
	// juego sin suministro. Default 2000.
	EnvSupplyIndexDecayPerSimDay = "II_SUPPLY_INDEX_DECAY_PER_SIM_DAY"

	// ── Macro (Incremento 6b): analítica, fórmula laboral y ajuste fiscal ──

	// EnvAnalyticsBucketSim es el tamaño del bucket de analítica en sim-time
	// (bucketización de analytics.*). Default 86400 (1 día de juego).
	EnvAnalyticsBucketSim = "II_BALANCER_ANALYTICS_BUCKET_SIM"
	// EnvLaborCapacityRef es la capacidad industrial de referencia por región
	// (nº de edificios operativos que da ocupación = 1). Default 20.
	EnvLaborCapacityRef = "II_LABOR_CAPACITY_REF"
	// EnvSalaryBase es el salario base a nivel de ciudad 1. Default 100.
	EnvSalaryBase = "II_SALARY_BASE"
	// EnvSalaryPerLevelBP es el incremento de salario por nivel de ciudad extra,
	// en puntos básicos del salario base (p. ej. 2500 = +25% por nivel). Default 2500.
	EnvSalaryPerLevelBP = "II_SALARY_PER_LEVEL_BP"
	// EnvLaborSaturationK es el peso de la ocupación industrial regional en el
	// salario efectivo (GDD 5.7). Default 0.5.
	EnvLaborSaturationK = "II_LABOR_SATURATION_K"
	// EnvLaborSalaryMinMult / EnvLaborSalaryMaxMult acotan el multiplicador de
	// saturación laboral (1 + k·ocupación). Defaults 1.0 y 3.0.
	EnvLaborSalaryMinMult = "II_LABOR_SALARY_MIN_MULT"
	EnvLaborSalaryMaxMult = "II_LABOR_SALARY_MAX_MULT"
	// EnvTaxMinBP / EnvTaxMaxBP acotan el tax_rate_bp del lazo fiscal (nunca fuera
	// de rango, GDD 5.5). Defaults 0 y 2000 (0%–20%).
	EnvTaxMinBP = "II_TAX_MIN_BP"
	EnvTaxMaxBP = "II_TAX_MAX_BP"
	// EnvTaxStepBP es el paso de ajuste de tax_rate_bp por barrido fiscal (lazo
	// suave). Default 50 (0,5%).
	EnvTaxStepBP = "II_TAX_STEP_BP"
	// EnvCanonMin / EnvCanonMax acotan el canon_base del lazo fiscal. Defaults 100 y 100000.
	EnvCanonMin = "II_CANON_MIN"
	EnvCanonMax = "II_CANON_MAX"
	// EnvCanonStepBP es el paso proporcional del canon_base por barrido, en bp del
	// canon vigente. Default 200 (2%).
	EnvCanonStepBP = "II_CANON_STEP_BP"
	// EnvFiscalInflationThreshold es el umbral de la señal inflación/deflación
	// (crecimiento de masa monetaria − crecimiento del PIB) por encima del cual el
	// lazo fiscal actúa. Default 0.01 (1%).
	EnvFiscalInflationThreshold = "II_FISCAL_INFLATION_THRESHOLD"
	// EnvPowerSpotIntervalSim es el intervalo del tick del mercado spot
	// eléctrico por región, en sim-time (múltiplo de 3600 para que la energía
	// por tick sea entera exacta). Default 3600 (1 hora-sim).
	EnvPowerSpotIntervalSim = "II_POWER_SPOT_INTERVAL_SIM"
	// EnvPowerSpotSweepInterval es el periodo wall-clock del barrido del
	// PowerWorker que busca buckets vencidos (time.ParseDuration). Default 5s.
	EnvPowerSpotSweepInterval = "II_POWER_SPOT_SWEEP_INTERVAL"
	// EnvPowerConnectRadiusM es el radio de conexión al pool regional: un
	// edificio participa si está a <= este radio (metros de mundo) de una línea
	// operativa de su región (ADR-025 §3). Default 2000.
	EnvPowerConnectRadiusM = "II_POWER_CONNECT_RADIUS_M"
	// EnvPowerDefaultBidPrice es la puja máxima por unidad de energía de los
	// consumidores SIN puja explícita (world.power_bids). Default 200.
	EnvPowerDefaultBidPrice = "II_POWER_DEFAULT_BID_PRICE"
	// EnvDepletionHorizonSimDays es el horizonte de la proyección de agotamiento
	// de recursos finitos, en días de juego. Default 360 (~12 meses de juego).
	EnvDepletionHorizonSimDays = "II_DEPLETION_HORIZON_SIM_DAYS"
)

// Defaults documentados.
const (
	DefaultDemandInterval               = 60 * time.Second
	DefaultAnalyticsInterval            = 120 * time.Second
	DefaultCityBuyDeadlineSim     int64 = 172_800 // 2 días de juego
	DefaultSupplyEMAAlpha               = 0.3
	DefaultSupplyEMAFloor               = 1.0
	DefaultLevelupIndexBase             = 100_000.0
	DefaultSupplyIndexDecayPerDay       = 2000.0

	// DefaultSaturationMin/Max acotan factor_saturación (⊂ [0,10] de la BD): sin
	// cota, una ciudad sin suministro reciente daría demanda efectiva ∞ (GDD 5.6).
	DefaultSaturationMin = 0.1
	DefaultSaturationMax = 10.0

	// Elasticidad por clase (exponente del ratio demanda/oferta sobre el precio):
	// basic inelástica (< 1: el precio se mueve poco), luxury elástica (> 1: muy
	// sensible a saturación). Dos clases, no un parámetro por producto (GDD 5.6).
	DefaultElasticityBasic  = 0.5
	DefaultElasticityLuxury = 1.5

	// Crecimiento de ciudad al subir de nivel (GDD 5.6).
	DefaultPopulationGrowthPct = 10 // población +10%
	DefaultD0GrowthPct         = 20 // demanda base D0 +20%

	// VarietyBonusPct pondera el supply_index del PRIMER suministro de un producto
	// en la ventana (producto "nuevo"): +50% => variedad premiada (GDD 5.6).
	DefaultVarietyBonusPct = 50

	// BuyTargetDays es el horizonte de compra: la ciudad pide ~D0 * este número
	// de días de juego, escalado por el factor de déficit (saturation_factor).
	DefaultBuyTargetDays = 2.0

	// ── Macro (Incremento 6b) ──
	DefaultAnalyticsBucketSim       int64 = 86_400 // 1 día de juego
	DefaultLaborCapacityRef               = 20.0
	DefaultSalaryBase               int64 = 100
	DefaultSalaryPerLevelBP         int64 = 2500 // +25% por nivel
	DefaultLaborSaturationK               = 0.5
	DefaultLaborSalaryMinMult             = 1.0
	DefaultLaborSalaryMaxMult             = 3.0
	DefaultTaxMinBP                       = 0
	DefaultTaxMaxBP                       = 2000 // 20%
	DefaultTaxStepBP                      = 50   // 0,5% por barrido
	DefaultCanonMin                 int64 = 100
	DefaultCanonMax                 int64 = 100_000
	DefaultCanonStepBP              int64 = 200 // 2% por barrido
	DefaultFiscalInflationThreshold       = 0.01
	DefaultDepletionHorizonSimDays  int64 = 360 // ~12 meses de juego

	// ── Mercado spot eléctrico (Fase 3, ADR-025) ──
	DefaultPowerSpotIntervalSim   int64 = 3_600 // 1 hora-sim (~2,5 min reales)
	DefaultPowerSpotSweepInterval       = 5 * time.Second
	DefaultPowerConnectRadiusM    int64 = 2_000
	DefaultPowerDefaultBidPrice   int64 = 200
)

// Options es la configuración del Balancer.
type Options struct {
	// DemandInterval: periodo de recálculo de curvas + publicación de buys (> 0).
	DemandInterval time.Duration
	// AnalyticsInterval: periodo del refresco de gauges macro (> 0).
	AnalyticsInterval time.Duration
	// CityBuyDeadlineSim: plazo de entrega de las buys de ciudad, sim-time (> 0).
	CityBuyDeadlineSim simtime.SimTime
	// SupplyEMAAlpha: peso del EMA de oferta (0 < alpha <= 1).
	SupplyEMAAlpha float64
	// SupplyEMAFloor: suelo del EMA de oferta (> 0).
	SupplyEMAFloor float64
	// LevelupIndexBase: umbral base de supply_index para subir de nivel (> 0).
	LevelupIndexBase float64
	// SupplyIndexDecayPerSimDay: decaimiento de supply_index por día sin oferta (>= 0).
	SupplyIndexDecayPerSimDay float64
	// SaturationMin/Max: clamp de factor_saturación (0 <= min < max <= 10).
	SaturationMin float64
	SaturationMax float64
	// ElasticityBasic/Luxury: exponente de precio por clase (> 0).
	ElasticityBasic  float64
	ElasticityLuxury float64
	// PopulationGrowthPct/D0GrowthPct: crecimiento al subir de nivel (>= 0).
	PopulationGrowthPct int
	D0GrowthPct         int
	// VarietyBonusPct: bono de supply_index por producto nuevo en la ventana (>= 0).
	VarietyBonusPct int
	// BuyTargetDays: horizonte de compra en días de juego (> 0).
	BuyTargetDays float64

	// ── Macro (Incremento 6b): analítica, fórmula laboral y ajuste fiscal ──

	// AnalyticsBucketSim: tamaño del bucket de analítica en sim-time (> 0).
	AnalyticsBucketSim int64
	// LaborCapacityRef: edificios operativos de referencia por región (ocupación
	// = 1); denominador del factor de saturación laboral (> 0).
	LaborCapacityRef float64
	// SalaryBase: salario efectivo base a nivel de ciudad 1 (> 0).
	SalaryBase int64
	// SalaryPerLevelBP: incremento de salario por nivel extra, bp del base (>= 0).
	SalaryPerLevelBP int64
	// LaborSaturationK: peso de la ocupación industrial en el salario (>= 0).
	LaborSaturationK float64
	// LaborSalaryMinMult/MaxMult: cotas del multiplicador de saturación laboral
	// (0 < min <= max).
	LaborSalaryMinMult float64
	LaborSalaryMaxMult float64
	// TaxMinBP/TaxMaxBP: rango del tax_rate_bp del lazo fiscal (0 <= min < max <= 10000).
	TaxMinBP int
	TaxMaxBP int
	// TaxStepBP: paso de ajuste de tax_rate_bp por barrido (> 0).
	TaxStepBP int
	// CanonMin/CanonMax: rango del canon_base del lazo fiscal (0 < min <= max).
	CanonMin int64
	CanonMax int64
	// CanonStepBP: paso proporcional del canon_base por barrido, bp (>= 0).
	CanonStepBP int64
	// FiscalInflationThreshold: umbral de la señal inflación/deflación (>= 0).
	FiscalInflationThreshold float64
	// DepletionHorizonSimDays: horizonte de la proyección de agotamiento (> 0).
	DepletionHorizonSimDays int64

	// ── Mercado spot eléctrico (Fase 3, ADR-025) ──

	// PowerSpotIntervalSim: intervalo del tick del spot por región, sim-time
	// (> 0 y múltiplo de 3600: energía por tick entera exacta).
	PowerSpotIntervalSim int64
	// PowerSpotSweepInterval: periodo wall-clock del barrido de buckets (> 0).
	PowerSpotSweepInterval time.Duration
	// PowerConnectRadiusM: radio de conexión al pool regional, metros (> 0).
	PowerConnectRadiusM int64
	// PowerDefaultBidPrice: puja default por unidad de energía (> 0).
	PowerDefaultBidPrice int64
}

// DefaultOptions devuelve la configuración por defecto del Balancer.
func DefaultOptions() Options {
	return Options{
		DemandInterval:            DefaultDemandInterval,
		AnalyticsInterval:         DefaultAnalyticsInterval,
		CityBuyDeadlineSim:        simtime.SimTime(DefaultCityBuyDeadlineSim),
		SupplyEMAAlpha:            DefaultSupplyEMAAlpha,
		SupplyEMAFloor:            DefaultSupplyEMAFloor,
		LevelupIndexBase:          DefaultLevelupIndexBase,
		SupplyIndexDecayPerSimDay: DefaultSupplyIndexDecayPerDay,
		SaturationMin:             DefaultSaturationMin,
		SaturationMax:             DefaultSaturationMax,
		ElasticityBasic:           DefaultElasticityBasic,
		ElasticityLuxury:          DefaultElasticityLuxury,
		PopulationGrowthPct:       DefaultPopulationGrowthPct,
		D0GrowthPct:               DefaultD0GrowthPct,
		VarietyBonusPct:           DefaultVarietyBonusPct,
		BuyTargetDays:             DefaultBuyTargetDays,

		AnalyticsBucketSim:       DefaultAnalyticsBucketSim,
		LaborCapacityRef:         DefaultLaborCapacityRef,
		SalaryBase:               DefaultSalaryBase,
		SalaryPerLevelBP:         DefaultSalaryPerLevelBP,
		LaborSaturationK:         DefaultLaborSaturationK,
		LaborSalaryMinMult:       DefaultLaborSalaryMinMult,
		LaborSalaryMaxMult:       DefaultLaborSalaryMaxMult,
		TaxMinBP:                 DefaultTaxMinBP,
		TaxMaxBP:                 DefaultTaxMaxBP,
		TaxStepBP:                DefaultTaxStepBP,
		CanonMin:                 DefaultCanonMin,
		CanonMax:                 DefaultCanonMax,
		CanonStepBP:              DefaultCanonStepBP,
		FiscalInflationThreshold: DefaultFiscalInflationThreshold,
		DepletionHorizonSimDays:  DefaultDepletionHorizonSimDays,

		PowerSpotIntervalSim:   DefaultPowerSpotIntervalSim,
		PowerSpotSweepInterval: DefaultPowerSpotSweepInterval,
		PowerConnectRadiusM:    DefaultPowerConnectRadiusM,
		PowerDefaultBidPrice:   DefaultPowerDefaultBidPrice,
	}
}

// OptionsFromEnv construye las Options desde las variables II_* con sus
// defaults. Un valor inválido devuelve error: la configuración rota debe
// impedir el arranque.
func OptionsFromEnv() (Options, error) {
	opts := DefaultOptions()
	if err := readDuration(EnvDemandInterval, &opts.DemandInterval); err != nil {
		return Options{}, err
	}
	if err := readDuration(EnvAnalyticsInterval, &opts.AnalyticsInterval); err != nil {
		return Options{}, err
	}
	if err := readSimTime(EnvCityBuyDeadlineSim, &opts.CityBuyDeadlineSim); err != nil {
		return Options{}, err
	}
	if err := readFloat(EnvSupplyEMAAlpha, &opts.SupplyEMAAlpha); err != nil {
		return Options{}, err
	}
	if err := readFloat(EnvSupplyEMAFloor, &opts.SupplyEMAFloor); err != nil {
		return Options{}, err
	}
	if err := readFloat(EnvLevelupIndexBase, &opts.LevelupIndexBase); err != nil {
		return Options{}, err
	}
	if err := readFloat(EnvSupplyIndexDecayPerSimDay, &opts.SupplyIndexDecayPerSimDay); err != nil {
		return Options{}, err
	}
	if err := readInt64(EnvAnalyticsBucketSim, &opts.AnalyticsBucketSim); err != nil {
		return Options{}, err
	}
	if err := readFloat(EnvLaborCapacityRef, &opts.LaborCapacityRef); err != nil {
		return Options{}, err
	}
	if err := readInt64(EnvSalaryBase, &opts.SalaryBase); err != nil {
		return Options{}, err
	}
	if err := readInt64(EnvSalaryPerLevelBP, &opts.SalaryPerLevelBP); err != nil {
		return Options{}, err
	}
	if err := readFloat(EnvLaborSaturationK, &opts.LaborSaturationK); err != nil {
		return Options{}, err
	}
	if err := readFloat(EnvLaborSalaryMinMult, &opts.LaborSalaryMinMult); err != nil {
		return Options{}, err
	}
	if err := readFloat(EnvLaborSalaryMaxMult, &opts.LaborSalaryMaxMult); err != nil {
		return Options{}, err
	}
	if err := readInt(EnvTaxMinBP, &opts.TaxMinBP); err != nil {
		return Options{}, err
	}
	if err := readInt(EnvTaxMaxBP, &opts.TaxMaxBP); err != nil {
		return Options{}, err
	}
	if err := readInt(EnvTaxStepBP, &opts.TaxStepBP); err != nil {
		return Options{}, err
	}
	if err := readInt64(EnvCanonMin, &opts.CanonMin); err != nil {
		return Options{}, err
	}
	if err := readInt64(EnvCanonMax, &opts.CanonMax); err != nil {
		return Options{}, err
	}
	if err := readInt64(EnvCanonStepBP, &opts.CanonStepBP); err != nil {
		return Options{}, err
	}
	if err := readFloat(EnvFiscalInflationThreshold, &opts.FiscalInflationThreshold); err != nil {
		return Options{}, err
	}
	if err := readInt64(EnvDepletionHorizonSimDays, &opts.DepletionHorizonSimDays); err != nil {
		return Options{}, err
	}
	if err := readInt64(EnvPowerSpotIntervalSim, &opts.PowerSpotIntervalSim); err != nil {
		return Options{}, err
	}
	if err := readDuration(EnvPowerSpotSweepInterval, &opts.PowerSpotSweepInterval); err != nil {
		return Options{}, err
	}
	if err := readInt64(EnvPowerConnectRadiusM, &opts.PowerConnectRadiusM); err != nil {
		return Options{}, err
	}
	if err := readInt64(EnvPowerDefaultBidPrice, &opts.PowerDefaultBidPrice); err != nil {
		return Options{}, err
	}
	if err := opts.Validate(); err != nil {
		return Options{}, err
	}
	return opts, nil
}

// Validate comprueba las invariantes de la configuración.
func (o Options) Validate() error {
	if o.DemandInterval <= 0 {
		return fmt.Errorf("balancer: %s debe ser > 0 (actual %s)", EnvDemandInterval, o.DemandInterval)
	}
	if o.AnalyticsInterval <= 0 {
		return fmt.Errorf("balancer: %s debe ser > 0 (actual %s)", EnvAnalyticsInterval, o.AnalyticsInterval)
	}
	if o.CityBuyDeadlineSim <= 0 {
		return fmt.Errorf("balancer: %s debe ser > 0 (actual %d)", EnvCityBuyDeadlineSim, o.CityBuyDeadlineSim)
	}
	if o.SupplyEMAAlpha <= 0 || o.SupplyEMAAlpha > 1 {
		return fmt.Errorf("balancer: %s debe estar en (0, 1] (actual %g)", EnvSupplyEMAAlpha, o.SupplyEMAAlpha)
	}
	if o.SupplyEMAFloor <= 0 {
		return fmt.Errorf("balancer: %s debe ser > 0 (actual %g)", EnvSupplyEMAFloor, o.SupplyEMAFloor)
	}
	if o.LevelupIndexBase <= 0 {
		return fmt.Errorf("balancer: %s debe ser > 0 (actual %g)", EnvLevelupIndexBase, o.LevelupIndexBase)
	}
	if o.SupplyIndexDecayPerSimDay < 0 {
		return fmt.Errorf("balancer: %s debe ser >= 0 (actual %g)", EnvSupplyIndexDecayPerSimDay, o.SupplyIndexDecayPerSimDay)
	}
	if o.SaturationMin < 0 || o.SaturationMax > 10 || o.SaturationMin >= o.SaturationMax {
		return fmt.Errorf("balancer: clamp de saturación inválido: 0 <= min(%g) < max(%g) <= 10", o.SaturationMin, o.SaturationMax)
	}
	if o.ElasticityBasic <= 0 || o.ElasticityLuxury <= 0 {
		return fmt.Errorf("balancer: elasticidades deben ser > 0 (basic %g, luxury %g)", o.ElasticityBasic, o.ElasticityLuxury)
	}
	if o.PopulationGrowthPct < 0 || o.D0GrowthPct < 0 || o.VarietyBonusPct < 0 {
		return fmt.Errorf("balancer: crecimientos y bono de variedad deben ser >= 0")
	}
	if o.BuyTargetDays <= 0 {
		return fmt.Errorf("balancer: BuyTargetDays debe ser > 0 (actual %g)", o.BuyTargetDays)
	}
	if o.AnalyticsBucketSim <= 0 {
		return fmt.Errorf("balancer: %s debe ser > 0 (actual %d)", EnvAnalyticsBucketSim, o.AnalyticsBucketSim)
	}
	if o.LaborCapacityRef <= 0 {
		return fmt.Errorf("balancer: %s debe ser > 0 (actual %g)", EnvLaborCapacityRef, o.LaborCapacityRef)
	}
	if o.SalaryBase <= 0 {
		return fmt.Errorf("balancer: %s debe ser > 0 (actual %d)", EnvSalaryBase, o.SalaryBase)
	}
	if o.SalaryPerLevelBP < 0 {
		return fmt.Errorf("balancer: %s debe ser >= 0 (actual %d)", EnvSalaryPerLevelBP, o.SalaryPerLevelBP)
	}
	if o.LaborSaturationK < 0 {
		return fmt.Errorf("balancer: %s debe ser >= 0 (actual %g)", EnvLaborSaturationK, o.LaborSaturationK)
	}
	if o.LaborSalaryMinMult <= 0 || o.LaborSalaryMaxMult < o.LaborSalaryMinMult {
		return fmt.Errorf("balancer: multiplicador de salario inválido: 0 < min(%g) <= max(%g)", o.LaborSalaryMinMult, o.LaborSalaryMaxMult)
	}
	if o.TaxMinBP < 0 || o.TaxMaxBP > 10000 || o.TaxMinBP >= o.TaxMaxBP {
		return fmt.Errorf("balancer: rango fiscal inválido: 0 <= %s(%d) < %s(%d) <= 10000", EnvTaxMinBP, o.TaxMinBP, EnvTaxMaxBP, o.TaxMaxBP)
	}
	if o.TaxStepBP <= 0 {
		return fmt.Errorf("balancer: %s debe ser > 0 (actual %d)", EnvTaxStepBP, o.TaxStepBP)
	}
	if o.CanonMin <= 0 || o.CanonMax < o.CanonMin {
		return fmt.Errorf("balancer: rango de canon inválido: 0 < %s(%d) <= %s(%d)", EnvCanonMin, o.CanonMin, EnvCanonMax, o.CanonMax)
	}
	if o.CanonStepBP < 0 {
		return fmt.Errorf("balancer: %s debe ser >= 0 (actual %d)", EnvCanonStepBP, o.CanonStepBP)
	}
	if o.FiscalInflationThreshold < 0 {
		return fmt.Errorf("balancer: %s debe ser >= 0 (actual %g)", EnvFiscalInflationThreshold, o.FiscalInflationThreshold)
	}
	if o.DepletionHorizonSimDays <= 0 {
		return fmt.Errorf("balancer: %s debe ser > 0 (actual %d)", EnvDepletionHorizonSimDays, o.DepletionHorizonSimDays)
	}
	if o.PowerSpotIntervalSim <= 0 || o.PowerSpotIntervalSim%3600 != 0 {
		return fmt.Errorf("balancer: %s debe ser > 0 y múltiplo de 3600 (actual %d)", EnvPowerSpotIntervalSim, o.PowerSpotIntervalSim)
	}
	if o.PowerSpotSweepInterval <= 0 {
		return fmt.Errorf("balancer: %s debe ser una duración positiva (actual %s)", EnvPowerSpotSweepInterval, o.PowerSpotSweepInterval)
	}
	if o.PowerConnectRadiusM <= 0 {
		return fmt.Errorf("balancer: %s debe ser > 0 (actual %d)", EnvPowerConnectRadiusM, o.PowerConnectRadiusM)
	}
	if o.PowerDefaultBidPrice <= 0 {
		return fmt.Errorf("balancer: %s debe ser > 0 (actual %d)", EnvPowerDefaultBidPrice, o.PowerDefaultBidPrice)
	}
	return nil
}

// readDuration lee una variable time.ParseDuration sobre dst; ausente conserva
// el default.
func readDuration(key string, dst *time.Duration) error {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fmt.Errorf("balancer: %s inválido %q (formato de time.ParseDuration): %w", key, v, err)
	}
	*dst = d
	return nil
}

// readSimTime lee un entero de sim-time sobre dst; ausente conserva el default.
func readSimTime(key string, dst *simtime.SimTime) error {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fmt.Errorf("balancer: %s inválido %q (entero de sim-time): %w", key, v, err)
	}
	*dst = simtime.SimTime(n)
	return nil
}

// readFloat lee un float64 sobre dst; ausente conserva el default.
func readFloat(key string, dst *float64) error {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fmt.Errorf("balancer: %s inválido %q (número): %w", key, v, err)
	}
	*dst = f
	return nil
}

// readInt64 lee un entero de 64 bits sobre dst; ausente conserva el default.
func readInt64(key string, dst *int64) error {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fmt.Errorf("balancer: %s inválido %q (entero): %w", key, v, err)
	}
	*dst = n
	return nil
}

// readInt lee un entero sobre dst; ausente conserva el default.
func readInt(key string, dst *int) error {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("balancer: %s inválido %q (entero): %w", key, v, err)
	}
	*dst = n
	return nil
}
