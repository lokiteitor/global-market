// Package auth implementa el bounded context de autenticación e identidad
// del gateway: credenciales argon2id, tokens de sesión opacos, repositorio
// del esquema auth, rate limiting (idéntico para humanos y bots, GDD §9),
// servicio de sesiones y los handlers HTTP del contrato OpenAPI v1.1.0.
//
// El módulo no importa otros bounded contexts (SAD v1.1 §7): solo la
// plataforma (internal/platform/*). El composition root de cmd/gateway
// compone los constructores públicos y monta las rutas.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Parámetros de argon2id (RFC 9106, perfil de memoria moderada). Están
// codificados en cada hash PHC, de modo que pueden evolucionar sin invalidar
// credenciales existentes: Verify usa siempre los parámetros del hash.
const (
	// argonTime es el número de pasadas (t).
	argonTime uint32 = 1
	// argonMemoryKiB es la memoria en KiB (m): 64 MiB.
	argonMemoryKiB uint32 = 64 * 1024
	// argonThreads es el paralelismo (p).
	argonThreads uint8 = 4
	// argonSaltLen es la longitud del salt en bytes (crypto/rand).
	argonSaltLen = 16
	// argonKeyLen es la longitud de la clave derivada en bytes.
	argonKeyLen uint32 = 32
)

// ErrMalformedHash indica una codificación PHC irreconocible o corrupta.
var ErrMalformedHash = errors.New("auth: hash PHC malformado")

// phcB64 es la variante base64 de PHC: alfabeto estándar sin padding.
var phcB64 = base64.RawStdEncoding

// HashSecret deriva un hash argon2id del secreto con un salt aleatorio de 16
// bytes y lo codifica en formato PHC estándar:
//
//	$argon2id$v=19$m=65536,t=1,p=4$<salt b64>$<hash b64>
func HashSecret(secret string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: generando salt: %w", err)
	}
	key := argon2.IDKey([]byte(secret), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonTime, argonThreads,
		phcB64.EncodeToString(salt), phcB64.EncodeToString(key)), nil
}

// VerifySecret comprueba el secreto contra un hash PHC argon2id. La
// comparación de la clave derivada es en tiempo constante
// (subtle.ConstantTimeCompare). Devuelve (false, nil) ante un secreto
// incorrecto y error solo ante un hash malformado.
func VerifySecret(secret, encoded string) (bool, error) {
	params, salt, key, err := decodePHC(encoded)
	if err != nil {
		return false, err
	}
	derived := argon2.IDKey([]byte(secret), salt, params.time, params.memoryKiB, params.threads, uint32(len(key)))
	return subtle.ConstantTimeCompare(derived, key) == 1, nil
}

// phcParams son los parámetros argon2id extraídos de un hash PHC.
type phcParams struct {
	memoryKiB uint32
	time      uint32
	threads   uint8
}

// decodePHC descompone una codificación PHC argon2id en parámetros, salt y
// clave derivada.
func decodePHC(encoded string) (phcParams, []byte, []byte, error) {
	// "" $ "argon2id" $ "v=19" $ "m=..,t=..,p=.." $ salt $ hash
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return phcParams{}, nil, nil, ErrMalformedHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return phcParams{}, nil, nil, ErrMalformedHash
	}
	var p phcParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memoryKiB, &p.time, &p.threads); err != nil {
		return phcParams{}, nil, nil, ErrMalformedHash
	}
	if p.memoryKiB == 0 || p.time == 0 || p.threads == 0 {
		return phcParams{}, nil, nil, ErrMalformedHash
	}
	salt, err := phcB64.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return phcParams{}, nil, nil, ErrMalformedHash
	}
	key, err := phcB64.DecodeString(parts[5])
	if err != nil || len(key) == 0 {
		return phcParams{}, nil, nil, ErrMalformedHash
	}
	return p, salt, key, nil
}
