package bots

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Variables de entorno del módulo (prefijo II_BOTS_*, 12-factor). La densidad
// de población es la válvula de carga del GDD §19.
const (
	// EnvCoalProducers es el número de productores de carbón. Default 1.
	EnvCoalProducers = "II_BOTS_COAL_PRODUCERS"
	// EnvIronProducers es el número de productores de hierro. Default 1.
	EnvIronProducers = "II_BOTS_IRON_PRODUCERS"
	// EnvTraders es el número de comerciantes. Default 1.
	EnvTraders = "II_BOTS_TRADERS"
	// EnvTransformers es el número de transformadores industriales. Default 1.
	EnvTransformers = "II_BOTS_TRANSFORMERS"
	// EnvFreighters es el número de transportistas. Default 1.
	EnvFreighters = "II_BOTS_FREIGHTERS"
	// EnvTransformerMarginBP es el margen de venta del transformador sobre el
	// coste unitario estimado de sus insumos, en basis points. Default 2500.
	EnvTransformerMarginBP = "II_BOTS_TRANSFORMER_MARGIN_BP"
	// EnvFreighterMarginBP es el margen exigido por el transportista sobre el
	// coste estimado del trayecto, en basis points. Default 2000.
	EnvFreighterMarginBP = "II_BOTS_FREIGHTER_MARGIN_BP"
	// EnvSecretSeed es la semilla de derivación de los secretos de los bots
	// (HMAC-SHA256(seed, nombre)): reproducible sin almacenar el secreto en
	// claro. Default "dev-bots-seed" (solo desarrollo).
	EnvSecretSeed = "II_BOTS_SECRET_SEED"
	// EnvCapital es la capitalización única de cada bot nuevo, en unidades
	// menores (emisión contabilizada del banco central: +cash/−emission).
	// Default 500000.
	EnvCapital = "II_BOTS_CAPITAL"
	// EnvTick es el periodo del bucle de decisión de cada bot (formato
	// time.ParseDuration), jitterizado ±20%. Default 5s.
	EnvTick = "II_BOTS_TICK"
	// EnvAddr es la dirección de escucha del servidor de observabilidad
	// propio (/healthz, /readyz, /metrics). Default :8082.
	EnvAddr = "II_BOTS_ADDR"
	// EnvAPIURL es la raíz de la API pública que juegan los bots, incluido el
	// prefijo de versión. Default http://localhost:8080/api/v1.
	EnvAPIURL = "II_BOTS_API_URL"
)

// Defaults documentados del módulo.
const (
	DefaultCoalProducers = 1
	DefaultIronProducers = 1
	DefaultTraders       = 1
	DefaultTransformers  = 1
	DefaultFreighters    = 1
	DefaultSecretSeed    = "dev-bots-seed"
	DefaultCapital       = 500_000
	DefaultTick          = 5 * time.Second
	DefaultAddr          = ":8082"
	DefaultAPIURL        = "http://localhost:8080/api/v1"
	// DefaultTransformerMarginBP es el margen de venta del transformador
	// (25% sobre el coste unitario estimado de sus insumos).
	DefaultTransformerMarginBP int64 = 2_500
	// DefaultFreighterMarginBP es el margen exigido por el transportista
	// (20% sobre el coste estimado del trayecto).
	DefaultFreighterMarginBP int64 = 2_000
)

// Options es la configuración del Bot Orchestration Service.
type Options struct {
	// CoalProducers es el número de bots coal_producer (>= 0).
	CoalProducers int
	// IronProducers es el número de bots iron_producer (>= 0).
	IronProducers int
	// Traders es el número de bots trader (>= 0).
	Traders int
	// Transformers es el número de bots industrial_transformer (>= 0).
	Transformers int
	// Freighters es el número de bots freighter (>= 0).
	Freighters int
	// TransformerMarginBP es el margen de venta del transformador en basis
	// points (>= 0).
	TransformerMarginBP int64
	// FreighterMarginBP es el margen exigido por el transportista en basis
	// points (>= 0).
	FreighterMarginBP int64
	// SecretSeed es la semilla de derivación de secretos (no vacía).
	SecretSeed string
	// Capital es la capitalización única de cada bot nuevo (> 0).
	Capital int64
	// Tick es el periodo del bucle de decisión (> 0), jitterizado ±20%.
	Tick time.Duration
	// Addr es la dirección del servidor de observabilidad (host:port o :port).
	Addr string
	// APIURL es la raíz de la API pública que juegan los bots.
	APIURL string
}

// DefaultOptions devuelve la configuración por defecto del módulo.
func DefaultOptions() Options {
	return Options{
		CoalProducers:       DefaultCoalProducers,
		IronProducers:       DefaultIronProducers,
		Traders:             DefaultTraders,
		Transformers:        DefaultTransformers,
		Freighters:          DefaultFreighters,
		TransformerMarginBP: DefaultTransformerMarginBP,
		FreighterMarginBP:   DefaultFreighterMarginBP,
		SecretSeed:          DefaultSecretSeed,
		Capital:             DefaultCapital,
		Tick:                DefaultTick,
		Addr:                DefaultAddr,
		APIURL:              DefaultAPIURL,
	}
}

// OptionsFromEnv construye las Options desde las variables II_BOTS_*.
// Cualquier valor inválido devuelve error: la configuración rota impide el
// arranque.
func OptionsFromEnv() (Options, error) {
	opts := DefaultOptions()
	var err error
	if opts.CoalProducers, err = intFromEnv(EnvCoalProducers, opts.CoalProducers); err != nil {
		return Options{}, err
	}
	if opts.IronProducers, err = intFromEnv(EnvIronProducers, opts.IronProducers); err != nil {
		return Options{}, err
	}
	if opts.Traders, err = intFromEnv(EnvTraders, opts.Traders); err != nil {
		return Options{}, err
	}
	if opts.Transformers, err = intFromEnv(EnvTransformers, opts.Transformers); err != nil {
		return Options{}, err
	}
	if opts.Freighters, err = intFromEnv(EnvFreighters, opts.Freighters); err != nil {
		return Options{}, err
	}
	if opts.TransformerMarginBP, err = int64FromEnv(EnvTransformerMarginBP, opts.TransformerMarginBP); err != nil {
		return Options{}, err
	}
	if opts.FreighterMarginBP, err = int64FromEnv(EnvFreighterMarginBP, opts.FreighterMarginBP); err != nil {
		return Options{}, err
	}
	if v := os.Getenv(EnvSecretSeed); v != "" {
		opts.SecretSeed = v
	}
	if v := strings.TrimSpace(os.Getenv(EnvCapital)); v != "" {
		n, perr := strconv.ParseInt(v, 10, 64)
		if perr != nil {
			return Options{}, fmt.Errorf("bots: %s inválido %q (entero de unidades menores): %w", EnvCapital, v, perr)
		}
		opts.Capital = n
	}
	if v := strings.TrimSpace(os.Getenv(EnvTick)); v != "" {
		d, perr := time.ParseDuration(v)
		if perr != nil {
			return Options{}, fmt.Errorf("bots: %s inválido %q (duración Go, p. ej. 5s): %w", EnvTick, v, perr)
		}
		opts.Tick = d
	}
	if v := strings.TrimSpace(os.Getenv(EnvAddr)); v != "" {
		opts.Addr = v
	}
	if v := strings.TrimSpace(os.Getenv(EnvAPIURL)); v != "" {
		opts.APIURL = v
	}
	if err := opts.Validate(); err != nil {
		return Options{}, err
	}
	return opts, nil
}

// Validate comprueba las invariantes de la configuración.
func (o Options) Validate() error {
	if o.CoalProducers < 0 || o.IronProducers < 0 || o.Traders < 0 || o.Transformers < 0 || o.Freighters < 0 {
		return fmt.Errorf("bots: la población no puede ser negativa (coal %d, iron %d, traders %d, transformers %d, freighters %d)",
			o.CoalProducers, o.IronProducers, o.Traders, o.Transformers, o.Freighters)
	}
	if o.TransformerMarginBP < 0 {
		return fmt.Errorf("bots: %s debe ser >= 0 (actual %d)", EnvTransformerMarginBP, o.TransformerMarginBP)
	}
	if o.FreighterMarginBP < 0 {
		return fmt.Errorf("bots: %s debe ser >= 0 (actual %d)", EnvFreighterMarginBP, o.FreighterMarginBP)
	}
	if strings.TrimSpace(o.SecretSeed) == "" {
		return fmt.Errorf("bots: %s no puede estar vacía", EnvSecretSeed)
	}
	if o.Capital <= 0 {
		return fmt.Errorf("bots: %s debe ser > 0 (actual %d)", EnvCapital, o.Capital)
	}
	if o.Tick <= 0 {
		return fmt.Errorf("bots: %s debe ser una duración positiva (actual %s)", EnvTick, o.Tick)
	}
	if strings.TrimSpace(o.Addr) == "" {
		return fmt.Errorf("bots: %s no puede estar vacía", EnvAddr)
	}
	if strings.TrimSpace(o.APIURL) == "" {
		return fmt.Errorf("bots: %s no puede estar vacía", EnvAPIURL)
	}
	return nil
}

// intFromEnv lee un entero >= 0 del entorno con default.
func intFromEnv(key string, def int) (int, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("bots: %s inválido %q (entero): %w", key, v, err)
	}
	return n, nil
}

// int64FromEnv lee un entero de 64 bits del entorno con default (basis points).
func int64FromEnv(key string, def int64) (int64, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("bots: %s inválido %q (entero de basis points): %w", key, v, err)
	}
	return n, nil
}
