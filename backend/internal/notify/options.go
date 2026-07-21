package notify

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Variables de entorno del módulo (prefijo II_WS_*, 12-factor).
const (
	// EnvSendBuffer es la capacidad del buffer de envío por conexión, en
	// frames. Si el buffer se llena (consumidor lento) la conexión se cierra
	// con el código 1013 y el cliente re-sincroniza por REST al reconectar
	// (ADR-023). Default 256.
	EnvSendBuffer = "II_WS_SEND_BUFFER"

	// EnvPingInterval es el periodo del ping WS a nivel de protocolo, en
	// formato time.ParseDuration. La conexión se cierra tras 2 fallos
	// consecutivos. Default 20s.
	EnvPingInterval = "II_WS_PING_INTERVAL"

	// EnvMaxConnsPerAccount es el máximo de conexiones WS simultáneas por
	// cuenta; el exceso recibe un frame error TOO_MANY_CONNECTIONS y cierre.
	// Default 4.
	EnvMaxConnsPerAccount = "II_WS_MAX_CONNS_PER_ACCOUNT"

	// EnvAuthTimeout es el plazo máximo para recibir el frame auth tras el
	// upgrade (cierre 4401 si vence), en formato time.ParseDuration. El
	// ADR-023 fija ≤5s; default 5s (los tests usan ventanas cortas).
	EnvAuthTimeout = "II_WS_AUTH_TIMEOUT"

	// EnvRouterInterval es el periodo de polling del consumidor outbox
	// notification_gateway, en formato time.ParseDuration. El drenaje encadena
	// lotes llenos sin esperar, así que solo acota la latencia en reposo.
	// Default 1s.
	EnvRouterInterval = "II_WS_ROUTER_INTERVAL"

	// EnvRouteCacheTTL es el TTL de la caché de lookups de enrutado
	// (publication→publisher, contract→buyer+seller, ...), en formato
	// time.ParseDuration. Corto a propósito: la titularidad puede cambiar
	// (traspasos). Default 30s.
	EnvRouteCacheTTL = "II_WS_ROUTE_CACHE_TTL"

	// EnvAllowedOrigins es la lista separada por comas de patrones de host
	// autorizados para upgrades cross-origin de navegador (AcceptOptions.
	// OriginPatterns de coder/websocket). Vacío (default) = solo mismo
	// origen; los clientes no-navegador (SDK de bots) no envían Origin y
	// pasan siempre.
	EnvAllowedOrigins = "II_WS_ALLOWED_ORIGINS"
)

// Defaults documentados del módulo.
const (
	DefaultSendBuffer         = 256
	DefaultPingInterval       = 20 * time.Second
	DefaultMaxConnsPerAccount = 4
	DefaultAuthTimeout        = 5 * time.Second
	DefaultRouterInterval     = time.Second
	DefaultRouteCacheTTL      = 30 * time.Second
)

// Options es la configuración del Notification Gateway (hub, handler del
// upgrade y router del outbox).
type Options struct {
	// SendBuffer es la capacidad del buffer de envío por conexión, en frames (> 0).
	SendBuffer int
	// PingInterval es el periodo del ping WS de protocolo (> 0).
	PingInterval time.Duration
	// MaxConnsPerAccount es el máximo de conexiones simultáneas por cuenta (> 0).
	MaxConnsPerAccount int
	// AuthTimeout es el plazo máximo del frame auth tras el upgrade (> 0).
	AuthTimeout time.Duration
	// RouterInterval es el periodo de polling del consumidor outbox (> 0).
	RouterInterval time.Duration
	// RouteCacheTTL es el TTL de la caché de lookups de enrutado (>= 0; 0
	// desactiva la caché).
	RouteCacheTTL time.Duration
	// AllowedOrigins son los patrones de origen autorizados para upgrades
	// cross-origin de navegador (vacío = solo mismo origen).
	AllowedOrigins []string
}

// DefaultOptions devuelve la configuración por defecto del módulo.
func DefaultOptions() Options {
	return Options{
		SendBuffer:         DefaultSendBuffer,
		PingInterval:       DefaultPingInterval,
		MaxConnsPerAccount: DefaultMaxConnsPerAccount,
		AuthTimeout:        DefaultAuthTimeout,
		RouterInterval:     DefaultRouterInterval,
		RouteCacheTTL:      DefaultRouteCacheTTL,
	}
}

// OptionsFromEnv construye las Options desde las variables II_WS_*. Cualquier
// valor inválido devuelve error: la configuración rota impide el arranque.
func OptionsFromEnv() (Options, error) {
	opts := DefaultOptions()
	if v := strings.TrimSpace(os.Getenv(EnvSendBuffer)); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return Options{}, fmt.Errorf("notify: %s inválido %q (entero): %w", EnvSendBuffer, v, err)
		}
		opts.SendBuffer = n
	}
	if v := strings.TrimSpace(os.Getenv(EnvPingInterval)); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Options{}, fmt.Errorf("notify: %s inválido %q (duración Go, p. ej. 20s): %w", EnvPingInterval, v, err)
		}
		opts.PingInterval = d
	}
	if v := strings.TrimSpace(os.Getenv(EnvMaxConnsPerAccount)); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return Options{}, fmt.Errorf("notify: %s inválido %q (entero): %w", EnvMaxConnsPerAccount, v, err)
		}
		opts.MaxConnsPerAccount = n
	}
	if v := strings.TrimSpace(os.Getenv(EnvAuthTimeout)); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Options{}, fmt.Errorf("notify: %s inválido %q (duración Go, p. ej. 5s): %w", EnvAuthTimeout, v, err)
		}
		opts.AuthTimeout = d
	}
	if v := strings.TrimSpace(os.Getenv(EnvRouterInterval)); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Options{}, fmt.Errorf("notify: %s inválido %q (duración Go, p. ej. 1s): %w", EnvRouterInterval, v, err)
		}
		opts.RouterInterval = d
	}
	if v := strings.TrimSpace(os.Getenv(EnvRouteCacheTTL)); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Options{}, fmt.Errorf("notify: %s inválido %q (duración Go, p. ej. 30s): %w", EnvRouteCacheTTL, v, err)
		}
		opts.RouteCacheTTL = d
	}
	if v := strings.TrimSpace(os.Getenv(EnvAllowedOrigins)); v != "" {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				opts.AllowedOrigins = append(opts.AllowedOrigins, p)
			}
		}
	}
	if err := opts.Validate(); err != nil {
		return Options{}, err
	}
	return opts, nil
}

// Validate comprueba las invariantes de la configuración.
func (o Options) Validate() error {
	if o.SendBuffer <= 0 {
		return fmt.Errorf("notify: %s debe ser > 0 (actual %d)", EnvSendBuffer, o.SendBuffer)
	}
	if o.PingInterval <= 0 {
		return fmt.Errorf("notify: %s debe ser una duración positiva (actual %s)", EnvPingInterval, o.PingInterval)
	}
	if o.MaxConnsPerAccount <= 0 {
		return fmt.Errorf("notify: %s debe ser > 0 (actual %d)", EnvMaxConnsPerAccount, o.MaxConnsPerAccount)
	}
	if o.AuthTimeout <= 0 {
		return fmt.Errorf("notify: %s debe ser una duración positiva (actual %s)", EnvAuthTimeout, o.AuthTimeout)
	}
	if o.RouterInterval <= 0 {
		return fmt.Errorf("notify: %s debe ser una duración positiva (actual %s)", EnvRouterInterval, o.RouterInterval)
	}
	if o.RouteCacheTTL < 0 {
		return fmt.Errorf("notify: %s debe ser >= 0 (actual %s)", EnvRouteCacheTTL, o.RouteCacheTTL)
	}
	return nil
}
