// Package config carga la configuración del motor desde el entorno.
package config

import (
	"os"
	"time"
)

type Config struct {
	DatabaseURL  string
	TickInterval time.Duration // periodo del bucle principal (1 s wall por defecto)
	// Ventanas en tiempo real del contrato compartido (valores dev, GDD 5.3.1).
	DrawWindow     time.Duration
	MicroWindow    time.Duration
	CancelCooldown time.Duration
}

func Load() Config {
	cfg := Config{
		DatabaseURL:    "postgres://imperio:imperio@localhost:5440/imperio",
		TickInterval:   time.Second,
		DrawWindow:     45 * time.Second,
		MicroWindow:    20 * time.Second,
		CancelCooldown: 30 * time.Second,
	}
	if v := os.Getenv("DATABASE_URL"); v != "" {
		cfg.DatabaseURL = v
	}
	if v := os.Getenv("ENGINE_TICK_MS"); v != "" {
		if d, err := time.ParseDuration(v + "ms"); err == nil && d > 0 {
			cfg.TickInterval = d
		}
	}
	return cfg
}
