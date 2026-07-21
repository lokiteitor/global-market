package stress

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Variables de entorno de la salvaguarda.
const (
	// EnvEnvironment es el entorno de ejecución compartido de la plataforma
	// (II_ENV). El harness REHÚSA arrancar si vale prod/production.
	EnvEnvironment = "II_ENV"
	// EnvAllowHosts es la allowlist de hosts no productivos, separada por comas.
	// Si se define, SUSTITUYE a DefaultAllowHosts (override explícito del
	// operador). Nunca relaja la negativa por II_ENV=prod.
	EnvAllowHosts = "II_STRESS_ALLOW_HOSTS"
)

// DefaultAllowHosts es la allowlist por defecto de entornos NO productivos.
// Acepta comodines '*' (cualquier secuencia, incluidos puntos). El harness
// exige que tanto el host de la API como el de la BD del provisioner casen
// alguna de estas entradas.
var DefaultAllowHosts = []string{
	"localhost",
	"*.localhost",
	"127.0.0.1",
	"::1",
	"host.docker.internal",
	"stress.*",
	"*.stress.*",
	"staging.*",
	"*.staging.*",
}

// Errores de la salvaguarda (GDD §13.4: el modo stress test corre en un
// entorno de pruebas independiente y NUNCA toca el mundo de producción).
var (
	// ErrAPIURLRequired indica que II_STRESS_API_URL no se definió: apuntar el
	// harness a un target es siempre una decisión consciente, sin default.
	ErrAPIURLRequired = errors.New("stress: II_STRESS_API_URL es obligatoria y no tiene valor por defecto")
	// ErrProductionEnv indica que II_ENV declara un entorno de producción.
	ErrProductionEnv = errors.New("stress: II_ENV declara un entorno de producción")
	// ErrHostNotAllowed indica que el host del target no está en la allowlist
	// de entornos no productivos.
	ErrHostNotAllowed = errors.New("stress: el host del target no está en la allowlist de entornos no productivos")
)

// gddSafeguard es la cita normativa que acompaña a todo rechazo de la
// salvaguarda: el operador debe entender POR QUÉ el harness se niega.
const gddSafeguard = "GDD §13.4: el modo stress test corre en un ENTORNO DE PRUEBAS INDEPENDIENTE y nunca toca el mundo de producción"

// isProductionEnv informa de si el valor de II_ENV designa producción.
func isProductionEnv(env string) bool {
	switch strings.ToLower(strings.TrimSpace(env)) {
	case "prod", "production", "prd", "live":
		return true
	}
	return false
}

// GuardTarget es la SALVAGUARDA del harness. Verifica, en este orden, que:
//
//  1. apiURL está definida y es una URL absoluta con host (sin default: el
//     operador siempre elige el target de forma explícita);
//  2. env no declara producción (II_ENV);
//  3. el host de apiURL casa alguna entrada de allowHosts (nil = DefaultAllowHosts).
//
// Devuelve el patrón de la allowlist que autorizó el target (útil para dejarlo
// registrado en el log de arranque y en el informe).
func GuardTarget(apiURL, env string, allowHosts []string) (matched string, err error) {
	apiURL = strings.TrimSpace(apiURL)
	if apiURL == "" {
		return "", fmt.Errorf("%w (%s)", ErrAPIURLRequired, gddSafeguard)
	}
	u, perr := url.Parse(apiURL)
	if perr != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("stress: II_STRESS_API_URL inválida %q (se espera http(s)://host[:puerto]/api/v1)", apiURL)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("stress: II_STRESS_API_URL con esquema no soportado %q (http o https)", u.Scheme)
	}
	if isProductionEnv(env) {
		return "", fmt.Errorf("%w (%s=%q): %s", ErrProductionEnv, EnvEnvironment, env, gddSafeguard)
	}
	host := u.Hostname()
	pattern, ok := hostAllowed(host, allowHosts)
	if !ok {
		return "", fmt.Errorf("%w: %q (allowlist: %s; ajústala con %s): %s",
			ErrHostNotAllowed, host, strings.Join(effectiveAllowHosts(allowHosts), ", "), EnvAllowHosts, gddSafeguard)
	}
	return pattern, nil
}

// GuardDatabaseURL aplica la MISMA allowlist al host de la base de datos que
// usará el provisioner: el harness escribe cuentas en la BD del entorno bajo
// prueba, así que apuntar a una BD de producción sería tan grave como apuntar
// la API. Una URL sin host (socket unix local) se considera local y se acepta.
func GuardDatabaseURL(databaseURL, env string, allowHosts []string) (matched string, err error) {
	if strings.TrimSpace(databaseURL) == "" {
		return "", errors.New("stress: la URL de base de datos del provisioner no puede estar vacía")
	}
	if isProductionEnv(env) {
		return "", fmt.Errorf("%w (%s=%q): %s", ErrProductionEnv, EnvEnvironment, env, gddSafeguard)
	}
	host, herr := databaseHost(databaseURL)
	if herr != nil {
		return "", herr
	}
	if host == "" {
		// Socket unix / host implícito: la conexión no sale de la máquina.
		return "local", nil
	}
	pattern, ok := hostAllowed(host, allowHosts)
	if !ok {
		return "", fmt.Errorf("%w: base de datos en %q (allowlist: %s; ajústala con %s): %s",
			ErrHostNotAllowed, host, strings.Join(effectiveAllowHosts(allowHosts), ", "), EnvAllowHosts, gddSafeguard)
	}
	return pattern, nil
}

// databaseHost extrae el host de una cadena de conexión de PostgreSQL, tanto en
// forma URL (postgres://usuario@host:5432/db) como en forma DSN de pares
// (host=... port=...). Devuelve "" si la conexión no declara host.
func databaseHost(databaseURL string) (string, error) {
	s := strings.TrimSpace(databaseURL)
	if strings.HasPrefix(s, "postgres://") || strings.HasPrefix(s, "postgresql://") {
		u, err := url.Parse(s)
		if err != nil {
			return "", fmt.Errorf("stress: URL de base de datos inválida: %w", err)
		}
		return u.Hostname(), nil
	}
	// DSN de pares clave=valor: el último host= gana (como libpq).
	host := ""
	for _, field := range strings.Fields(s) {
		k, v, ok := strings.Cut(field, "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), "host") {
			host = strings.Trim(strings.TrimSpace(v), `'"`)
		}
	}
	if strings.HasPrefix(host, "/") {
		return "", nil // socket unix
	}
	return host, nil
}

// effectiveAllowHosts devuelve la allowlist en uso (nil ⇒ la de por defecto).
func effectiveAllowHosts(allowHosts []string) []string {
	if len(allowHosts) == 0 {
		return DefaultAllowHosts
	}
	return allowHosts
}

// hostAllowed informa de si host casa alguna entrada de la allowlist y devuelve
// el patrón que lo autorizó.
func hostAllowed(host string, allowHosts []string) (string, bool) {
	for _, pattern := range effectiveAllowHosts(allowHosts) {
		if matchHostPattern(pattern, host) {
			return pattern, true
		}
	}
	return "", false
}

// matchHostPattern casa un host contra un patrón con comodines '*' (cada '*'
// admite cualquier secuencia, incluidos puntos). La comparación es
// case-insensitive y sin recursión: prefijo, trozos intermedios en orden y
// sufijo.
func matchHostPattern(pattern, host string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	host = strings.ToLower(strings.TrimSpace(host))
	if pattern == "" || host == "" {
		return false
	}
	if !strings.Contains(pattern, "*") {
		return pattern == host
	}
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(host, parts[0]) {
		return false
	}
	rest := host[len(parts[0]):]
	last := parts[len(parts)-1]
	for _, mid := range parts[1 : len(parts)-1] {
		if mid == "" {
			continue
		}
		i := strings.Index(rest, mid)
		if i < 0 {
			return false
		}
		rest = rest[i+len(mid):]
	}
	if last == "" {
		return true
	}
	return len(rest) >= len(last) && strings.HasSuffix(rest, last)
}
