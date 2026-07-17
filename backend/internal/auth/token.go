package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// tokenBytes es la entropía del token de sesión en bytes (256 bits).
const tokenBytes = 32

// NewToken genera un token de sesión opaco: 32 bytes de crypto/rand en
// base64url sin padding (43 caracteres). El token viaja al cliente una única
// vez; el servidor solo persiste HashToken(token).
func NewToken() (string, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("auth: generando token de sesión: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// HashToken deriva el hash persistible de un token de sesión: SHA-256 en
// hexadecimal (auth.sessions.token_hash). SHA-256 basta porque el token es
// aleatorio de alta entropía: no hay diccionario que atacar.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
