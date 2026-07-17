package ledger

import (
	"testing"
	"time"
)

func TestOptionsFromEnvDefaults(t *testing.T) {
	t.Setenv(EnvQueryTimeout, "")
	opts, err := OptionsFromEnv()
	if err != nil {
		t.Fatalf("OptionsFromEnv: %v", err)
	}
	if opts.QueryTimeout != DefaultQueryTimeout {
		t.Errorf("QueryTimeout %v, esperado %v", opts.QueryTimeout, DefaultQueryTimeout)
	}
}

func TestOptionsFromEnvCustom(t *testing.T) {
	t.Setenv(EnvQueryTimeout, "2500ms")
	opts, err := OptionsFromEnv()
	if err != nil {
		t.Fatalf("OptionsFromEnv: %v", err)
	}
	if opts.QueryTimeout != 2500*time.Millisecond {
		t.Errorf("QueryTimeout %v, esperado 2.5s", opts.QueryTimeout)
	}
}

func TestOptionsFromEnvInvalid(t *testing.T) {
	for _, v := range []string{"nope", "-3s", "0"} {
		t.Setenv(EnvQueryTimeout, v)
		if _, err := OptionsFromEnv(); err == nil {
			t.Errorf("%s=%q aceptado, esperado error", EnvQueryTimeout, v)
		}
	}
}
