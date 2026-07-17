package contracts

import (
	"errors"
	"testing"
)

// TestOptionsFromEnvDefaults verifica los defaults documentados del módulo.
func TestOptionsFromEnvDefaults(t *testing.T) {
	opts, err := OptionsFromEnv()
	if err != nil {
		t.Fatalf("OptionsFromEnv sin entorno: %v", err)
	}
	want := Options{
		DrawWindowSeconds:        45,
		MicroWindowSeconds:       20,
		CancelCooldownSeconds:    10,
		PublicationTTLSimSeconds: 604_800,
		CompensationBP:           5000,
	}
	if opts != want {
		t.Fatalf("defaults: %+v, esperado %+v", opts, want)
	}
}

// TestOptionsFromEnvCustom verifica la lectura de las variables II_*.
func TestOptionsFromEnvCustom(t *testing.T) {
	t.Setenv(EnvDrawWindowSeconds, "60")
	t.Setenv(EnvMicroWindowSeconds, "30")
	t.Setenv(EnvCancelCooldownSeconds, "0")
	t.Setenv(EnvPublicationTTLSimSeconds, "86400")
	t.Setenv(EnvCompensationBP, "2500")

	opts, err := OptionsFromEnv()
	if err != nil {
		t.Fatalf("OptionsFromEnv: %v", err)
	}
	want := Options{
		DrawWindowSeconds:        60,
		MicroWindowSeconds:       30,
		CancelCooldownSeconds:    0,
		PublicationTTLSimSeconds: 86_400,
		CompensationBP:           2500,
	}
	if opts != want {
		t.Fatalf("opciones: %+v, esperado %+v", opts, want)
	}
}

// TestOptionsFromEnvInvalid verifica que la configuración rota impide el
// arranque (error, jamás valores corregidos en silencio).
func TestOptionsFromEnvInvalid(t *testing.T) {
	cases := []struct{ key, value string }{
		{EnvDrawWindowSeconds, "abc"},
		{EnvDrawWindowSeconds, "0"},
		{EnvDrawWindowSeconds, "-1"},
		{EnvMicroWindowSeconds, "0"},
		{EnvCancelCooldownSeconds, "-5"},
		{EnvPublicationTTLSimSeconds, "0"},
		{EnvCompensationBP, "10001"},
		{EnvCompensationBP, "-1"},
		{EnvCompensationBP, "x"},
	}
	for _, tc := range cases {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			if _, err := OptionsFromEnv(); err == nil {
				t.Fatalf("%s=%q debería ser inválido", tc.key, tc.value)
			}
		})
	}
}

// TestValidateBounds cubre Validate directamente (NewService la usa como
// guarda de arranque).
func TestValidateBounds(t *testing.T) {
	opts := DefaultOptions()
	if err := opts.Validate(); err != nil {
		t.Fatalf("DefaultOptions debe ser válida: %v", err)
	}
	opts.MicroWindowSeconds = 0
	if err := opts.Validate(); err == nil {
		t.Fatal("MicroWindowSeconds=0 debería ser inválido")
	}
}

// errorsIsAll ayuda a asegurar que un error responde a todos los sentinelas.
func errorsIsAll(err error, targets ...error) bool {
	for _, target := range targets {
		if !errors.Is(err, target) {
			return false
		}
	}
	return true
}
