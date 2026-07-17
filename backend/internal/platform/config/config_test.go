package config

import (
	"strings"
	"testing"
)

// clearEnv deja el entorno del test sin ninguna variable II_* definida.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{EnvDatabaseURL, EnvHTTPAddr, EnvEngineAddr, EnvLogLevel, EnvEnvironment, EnvMigrationsDir} {
		t.Setenv(k, "")
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() con entorno vacío: %v", err)
	}
	want := Config{
		DatabaseURL:   DefaultDatabaseURL,
		HTTPAddr:      DefaultHTTPAddr,
		EngineAddr:    DefaultEngineAddr,
		LogLevel:      DefaultLogLevel,
		Env:           DefaultEnvironment,
		MigrationsDir: DefaultMigrationsDir,
	}
	if cfg != want {
		t.Fatalf("Load() = %+v, quiero %+v", cfg, want)
	}
	if !cfg.IsDev() {
		t.Fatalf("IsDev() = false con Env=%q, quiero true", cfg.Env)
	}
}

func TestLoadOverrides(t *testing.T) {
	clearEnv(t)
	t.Setenv(EnvDatabaseURL, "postgres://user:pass@db.example:6543/imperio")
	t.Setenv(EnvHTTPAddr, "127.0.0.1:9090")
	t.Setenv(EnvEngineAddr, ":9091")
	t.Setenv(EnvLogLevel, "DEBUG") // se normaliza a minúsculas
	t.Setenv(EnvEnvironment, "prod")
	t.Setenv(EnvMigrationsDir, "db/otras")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	want := Config{
		DatabaseURL:   "postgres://user:pass@db.example:6543/imperio",
		HTTPAddr:      "127.0.0.1:9090",
		EngineAddr:    ":9091",
		LogLevel:      "debug",
		Env:           "prod",
		MigrationsDir: "db/otras",
	}
	if cfg != want {
		t.Fatalf("Load() = %+v, quiero %+v", cfg, want)
	}
	if cfg.IsDev() {
		t.Fatal("IsDev() = true con Env=prod, quiero false")
	}
}

func TestLoadBlankFallsBackToDefault(t *testing.T) {
	clearEnv(t)
	t.Setenv(EnvHTTPAddr, "   ") // en blanco = ausente

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(): %v", err)
	}
	if cfg.HTTPAddr != DefaultHTTPAddr {
		t.Fatalf("HTTPAddr = %q, quiero el default %q", cfg.HTTPAddr, DefaultHTTPAddr)
	}
}

func TestLoadInvalid(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		wantSub string
	}{
		{
			name:    "addr sin puerto",
			env:     map[string]string{EnvHTTPAddr: "localhost"},
			wantSub: EnvHTTPAddr,
		},
		{
			name:    "addr con puerto no numérico",
			env:     map[string]string{EnvHTTPAddr: "localhost:http"},
			wantSub: EnvHTTPAddr,
		},
		{
			name:    "addr con puerto fuera de rango",
			env:     map[string]string{EnvEngineAddr: ":70000"},
			wantSub: EnvEngineAddr,
		},
		{
			name:    "addr solo puerto sin dos puntos",
			env:     map[string]string{EnvEngineAddr: "8081"},
			wantSub: EnvEngineAddr,
		},
		{
			name:    "nivel de log desconocido",
			env:     map[string]string{EnvLogLevel: "trace"},
			wantSub: EnvLogLevel,
		},
		{
			name:    "entorno desconocido",
			env:     map[string]string{EnvEnvironment: "staging"},
			wantSub: EnvEnvironment,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			_, err := Load()
			if err == nil {
				t.Fatal("Load() sin error, quiero error de validación")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q no menciona %q", err, tc.wantSub)
			}
		})
	}
}

// TestValidateDirect cubre invariantes que Load no puede producir porque los
// valores en blanco caen al default (construcción directa de Config).
func TestValidateDirect(t *testing.T) {
	valid := Config{
		DatabaseURL:   DefaultDatabaseURL,
		HTTPAddr:      DefaultHTTPAddr,
		EngineAddr:    DefaultEngineAddr,
		LogLevel:      DefaultLogLevel,
		Env:           DefaultEnvironment,
		MigrationsDir: DefaultMigrationsDir,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() de configuración válida: %v", err)
	}

	dbEmpty := valid
	dbEmpty.DatabaseURL = " \t"
	if err := dbEmpty.Validate(); err == nil {
		t.Fatal("Validate() sin error con DatabaseURL en blanco")
	}

	migEmpty := valid
	migEmpty.MigrationsDir = " "
	if err := migEmpty.Validate(); err == nil {
		t.Fatal("Validate() sin error con MigrationsDir en blanco")
	}
}
