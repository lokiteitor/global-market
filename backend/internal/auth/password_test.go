package auth

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/crypto/argon2"
)

func TestHashSecretRoundtrip(t *testing.T) {
	const secret = "correct horse battery staple"
	encoded, err := HashSecret(secret)
	if err != nil {
		t.Fatalf("HashSecret: %v", err)
	}
	ok, err := VerifySecret(secret, encoded)
	if err != nil {
		t.Fatalf("VerifySecret: %v", err)
	}
	if !ok {
		t.Fatal("el secreto correcto fue rechazado")
	}
}

func TestVerifySecretRejectsWrongSecret(t *testing.T) {
	encoded, err := HashSecret("secreto-bueno")
	if err != nil {
		t.Fatalf("HashSecret: %v", err)
	}
	for _, wrong := range []string{"", "secreto-malo", "secreto-buenO", "secreto-bueno "} {
		ok, err := VerifySecret(wrong, encoded)
		if err != nil {
			t.Fatalf("VerifySecret(%q): %v", wrong, err)
		}
		if ok {
			t.Errorf("VerifySecret(%q) aceptó un secreto incorrecto", wrong)
		}
	}
}

func TestHashSecretPHCFormat(t *testing.T) {
	encoded, err := HashSecret("s")
	if err != nil {
		t.Fatalf("HashSecret: %v", err)
	}
	wantPrefix := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$", argon2.Version, argonMemoryKiB, argonTime, argonThreads)
	if !strings.HasPrefix(encoded, wantPrefix) {
		t.Fatalf("codificación PHC %q sin el prefijo esperado %q", encoded, wantPrefix)
	}
	params, salt, key, err := decodePHC(encoded)
	if err != nil {
		t.Fatalf("decodePHC: %v", err)
	}
	if params.memoryKiB != argonMemoryKiB || params.time != argonTime || params.threads != argonThreads {
		t.Errorf("parámetros decodificados %+v no coinciden con los de generación", params)
	}
	if len(salt) != argonSaltLen {
		t.Errorf("salt de %d bytes, esperado %d", len(salt), argonSaltLen)
	}
	if len(key) != int(argonKeyLen) {
		t.Errorf("clave de %d bytes, esperado %d", len(key), argonKeyLen)
	}
	if strings.Count(encoded, "$") != 5 {
		t.Errorf("estructura PHC inesperada (segmentos): %q", encoded)
	}
	// PHC usa base64 sin padding: los dos últimos segmentos no llevan '='.
	parts := strings.Split(encoded, "$")
	if strings.ContainsAny(parts[4]+parts[5], "=") {
		t.Errorf("salt/hash con padding base64: %q", encoded)
	}
}

func TestHashSecretUniqueSalt(t *testing.T) {
	a, err := HashSecret("mismo-secreto")
	if err != nil {
		t.Fatalf("HashSecret: %v", err)
	}
	b, err := HashSecret("mismo-secreto")
	if err != nil {
		t.Fatalf("HashSecret: %v", err)
	}
	if a == b {
		t.Fatal("dos hashes del mismo secreto son idénticos: el salt no es aleatorio")
	}
}

func TestVerifySecretUsesParamsFromHash(t *testing.T) {
	// Un hash generado con parámetros distintos a los actuales debe seguir
	// verificando: los parámetros se leen de la codificación PHC.
	salt := []byte("0123456789abcdef")
	key := argon2.IDKey([]byte("legacy"), salt, 2, 32*1024, 1, 32)
	encoded := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, 32*1024, 2, 1,
		phcB64.EncodeToString(salt), phcB64.EncodeToString(key))
	ok, err := VerifySecret("legacy", encoded)
	if err != nil {
		t.Fatalf("VerifySecret: %v", err)
	}
	if !ok {
		t.Fatal("hash con parámetros legacy rechazado")
	}
}

func TestVerifySecretMalformedHash(t *testing.T) {
	cases := []string{
		"",
		"no-es-phc",
		"$argon2i$v=19$m=65536,t=1,p=4$c2FsdA$aGFzaA",   // variante incorrecta
		"$argon2id$v=18$m=65536,t=1,p=4$c2FsdA$aGFzaA",  // versión incorrecta
		"$argon2id$v=19$m=0,t=1,p=4$c2FsdA$aGFzaA",      // memoria cero
		"$argon2id$v=19$m=65536,t=1,p=4$!!$aGFzaA",      // salt no base64
		"$argon2id$v=19$m=65536,t=1,p=4$c2FsdA$!!",      // hash no base64
		"$argon2id$v=19$m=65536,t=1,p=4$c2FsdA",         // faltan segmentos
		"$argon2id$v=19$m=65536,t=1,p=4$c2FsdA$aGFzaA$", // segmento extra
	}
	for _, c := range cases {
		if _, err := VerifySecret("x", c); !errors.Is(err, ErrMalformedHash) {
			t.Errorf("VerifySecret con hash %q: error %v, esperado ErrMalformedHash", c, err)
		}
	}
}
