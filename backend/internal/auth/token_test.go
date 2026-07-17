package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

func TestNewToken(t *testing.T) {
	token, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	if strings.ContainsAny(token, "=+/") {
		t.Errorf("token %q no es base64url sin padding", token)
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("el token %q no decodifica como base64url: %v", token, err)
	}
	if len(raw) != tokenBytes {
		t.Errorf("token de %d bytes, esperado %d", len(raw), tokenBytes)
	}
}

func TestNewTokenUnique(t *testing.T) {
	seen := make(map[string]struct{}, 256)
	for range 256 {
		token, err := NewToken()
		if err != nil {
			t.Fatalf("NewToken: %v", err)
		}
		if _, dup := seen[token]; dup {
			t.Fatalf("token repetido: %q", token)
		}
		seen[token] = struct{}{}
	}
}

func TestHashToken(t *testing.T) {
	const token = "token-de-prueba"
	want := sha256.Sum256([]byte(token))
	if got := HashToken(token); got != hex.EncodeToString(want[:]) {
		t.Errorf("HashToken = %q, esperado sha256 hex %q", got, hex.EncodeToString(want[:]))
	}
	if got := HashToken(token); len(got) != 64 {
		t.Errorf("hash de longitud %d, esperado 64 hex", len(got))
	}
	if HashToken("a") == HashToken("b") {
		t.Error("tokens distintos con el mismo hash")
	}
}
