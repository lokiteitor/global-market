package bots

import (
	"testing"
	"time"
)

func TestRetirementOptionsFromEnvDefaultsAndOverrides(t *testing.T) {
	opts, err := RetirementOptionsFromEnv()
	if err != nil {
		t.Fatalf("RetirementOptionsFromEnv con entorno limpio: %v", err)
	}
	if opts != DefaultRetirementOptions() {
		t.Fatalf("defaults inesperados: %+v", opts)
	}

	t.Setenv(EnvRetireInterval, "30s")
	t.Setenv(EnvRetireCashFloor, "500")
	t.Setenv(EnvRetireIdleSimSeconds, "1209600")
	opts, err = RetirementOptionsFromEnv()
	if err != nil {
		t.Fatalf("RetirementOptionsFromEnv con overrides: %v", err)
	}
	want := RetirementOptions{
		Interval:       30 * time.Second,
		CashFloor:      500,
		IdleSimSeconds: 1_209_600,
	}
	if opts != want {
		t.Fatalf("overrides: %+v, esperado %+v", opts, want)
	}
}

func TestRetirementOptionsFromEnvInvalid(t *testing.T) {
	t.Setenv(EnvRetireInterval, "nada")
	if _, err := RetirementOptionsFromEnv(); err == nil {
		t.Fatal("intervalo inválido debía fallar")
	}
	t.Setenv(EnvRetireInterval, "")

	t.Setenv(EnvRetireCashFloor, "-1")
	if _, err := RetirementOptionsFromEnv(); err == nil {
		t.Fatal("piso de caja negativo debía fallar")
	}
	t.Setenv(EnvRetireCashFloor, "")

	t.Setenv(EnvRetireIdleSimSeconds, "-1")
	if _, err := RetirementOptionsFromEnv(); err == nil {
		t.Fatal("ventana de inactividad negativa debía fallar")
	}
}

func TestRetirementOptionsValidate(t *testing.T) {
	if err := DefaultRetirementOptions().Validate(); err != nil {
		t.Fatalf("los defaults deben ser válidos: %v", err)
	}
	if err := (RetirementOptions{Interval: 0, CashFloor: 1000, IdleSimSeconds: 0}).Validate(); err == nil {
		t.Fatal("Interval 0 debía ser inválido")
	}
	// IdleSimSeconds 0 es válido: retira en cuanto se cumple la condición.
	if err := (RetirementOptions{Interval: time.Second, CashFloor: 0, IdleSimSeconds: 0}).Validate(); err != nil {
		t.Fatalf("IdleSimSeconds 0 debe ser válido: %v", err)
	}
}
