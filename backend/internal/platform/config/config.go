// Package config carga y valida la configuración 12-factor del backend.
//
// Toda la configuración llega por variables de entorno con prefijo II_ y
// valores por defecto pensados para el entorno local de desarrollo. El
// paquete no tiene dependencias externas.
package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// Nombres de las variables de entorno reconocidas.
const (
	EnvDatabaseURL   = "II_DATABASE_URL"
	EnvHTTPAddr      = "II_HTTP_ADDR"
	EnvEngineAddr    = "II_ENGINE_ADDR"
	EnvLogLevel      = "II_LOG_LEVEL"
	EnvEnvironment   = "II_ENV"
	EnvMigrationsDir = "II_MIGRATIONS_DIR"
)

// Valores por defecto (entorno local dev).
const (
	DefaultDatabaseURL   = "postgres://imperio:imperio@localhost:5432/imperio?sslmode=disable"
	DefaultHTTPAddr      = ":8080"
	DefaultEngineAddr    = ":8081"
	DefaultLogLevel      = "info"
	DefaultEnvironment   = "dev"
	DefaultMigrationsDir = "db/migrations"
)

// Config es la configuración tipada y validada del backend.
type Config struct {
	// DatabaseURL es la cadena de conexión de PostgreSQL.
	DatabaseURL string
	// HTTPAddr es la dirección de escucha del gateway (host:port o :port).
	HTTPAddr string
	// EngineAddr es la dirección de escucha del engine (host:port o :port).
	EngineAddr string
	// LogLevel es el nivel mínimo de log: debug|info|warn|error.
	LogLevel string
	// Env es el entorno de ejecución: dev|prod.
	Env string
	// MigrationsDir es el directorio de migraciones SQL, relativo a /backend.
	MigrationsDir string
}

// Load construye la configuración desde el entorno, aplica los valores por
// defecto y la valida. Devuelve un error descriptivo ante cualquier valor
// inválido: la configuración rota debe impedir el arranque.
func Load() (Config, error) {
	cfg := Config{
		DatabaseURL:   getenv(EnvDatabaseURL, DefaultDatabaseURL),
		HTTPAddr:      getenv(EnvHTTPAddr, DefaultHTTPAddr),
		EngineAddr:    getenv(EnvEngineAddr, DefaultEngineAddr),
		LogLevel:      strings.ToLower(getenv(EnvLogLevel, DefaultLogLevel)),
		Env:           strings.ToLower(getenv(EnvEnvironment, DefaultEnvironment)),
		MigrationsDir: getenv(EnvMigrationsDir, DefaultMigrationsDir),
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate comprueba las invariantes de la configuración.
func (c Config) Validate() error {
	if strings.TrimSpace(c.DatabaseURL) == "" {
		return fmt.Errorf("config: %s no puede estar vacía", EnvDatabaseURL)
	}
	if err := validateAddr(EnvHTTPAddr, c.HTTPAddr); err != nil {
		return err
	}
	if err := validateAddr(EnvEngineAddr, c.EngineAddr); err != nil {
		return err
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("config: %s inválido %q (valores: debug|info|warn|error)", EnvLogLevel, c.LogLevel)
	}
	switch c.Env {
	case "dev", "prod":
	default:
		return fmt.Errorf("config: %s inválido %q (valores: dev|prod)", EnvEnvironment, c.Env)
	}
	if strings.TrimSpace(c.MigrationsDir) == "" {
		return fmt.Errorf("config: %s no puede estar vacío", EnvMigrationsDir)
	}
	return nil
}

// IsDev indica si el entorno de ejecución es de desarrollo.
func (c Config) IsDev() bool { return c.Env == "dev" }

// validateAddr acepta direcciones de escucha con formato host:port o :port.
func validateAddr(name, addr string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("config: %s inválida %q (formato host:port o :port): %w", name, addr, err)
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("config: %s inválida %q (puerto numérico 1-65535)", name, addr)
	}
	return nil
}

// getenv devuelve el valor de la variable (recortado de espacios) o el valor
// por defecto si está ausente o en blanco.
func getenv(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
