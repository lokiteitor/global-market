package production_test

import (
	"testing"

	"github.com/lokiteitor/global-market/backend/internal/world/production"
)

// TestWorkerOptionsFromEnvDefaults verifica los defaults documentados del motor
// cuando el entorno no fija ninguna variable.
func TestWorkerOptionsFromEnvDefaults(t *testing.T) {
	opts, err := production.WorkerOptionsFromEnv()
	if err != nil {
		t.Fatalf("WorkerOptionsFromEnv: %v", err)
	}
	if opts.SweepInterval != production.DefaultSweepInterval {
		t.Errorf("SweepInterval = %s, quiero %s", opts.SweepInterval, production.DefaultSweepInterval)
	}
	if opts.BatchSize != production.DefaultSweepBatchSize {
		t.Errorf("BatchSize = %d, quiero %d", opts.BatchSize, production.DefaultSweepBatchSize)
	}
	if opts.BuildSimSeconds != production.DefaultBuildSimSeconds {
		t.Errorf("BuildSimSeconds = %d, quiero %d", opts.BuildSimSeconds, production.DefaultBuildSimSeconds)
	}
	if opts.ReconcileInterval != production.DefaultReconcileInterval {
		t.Errorf("ReconcileInterval = %s, quiero %s", opts.ReconcileInterval, production.DefaultReconcileInterval)
	}
	if opts.ReconcileGrace != production.DefaultReconcileGrace {
		t.Errorf("ReconcileGrace = %d, quiero %d", opts.ReconcileGrace, production.DefaultReconcileGrace)
	}
}

// TestWorkerOptionsFromEnvIntegersRejectSuffix fija la regresión: los enteros del
// motor (pasadas, lotes, segundos de sim) NO son duraciones. Un operador que
// asuma el formato de las II_*_INTERVAL vecinas y escriba "1s" debe romper el
// arranque, no obtener silenciosamente 1 (fmt.Sscanf("%d") aceptaba el sufijo).
func TestWorkerOptionsFromEnvIntegersRejectSuffix(t *testing.T) {
	for _, env := range []string{
		production.EnvReconcileGrace,
		production.EnvSweepBatchSize,
		production.EnvBuildSimSeconds,
	} {
		for _, v := range []string{"1s", "2m", "300ms", "3x", "1.5", "0x10"} {
			t.Setenv(env, v)
			if opts, err := production.WorkerOptionsFromEnv(); err == nil {
				t.Errorf("WorkerOptionsFromEnv(%s=%q) sin error: %+v", env, v, opts)
			}
		}
		t.Setenv(env, "4")
		if _, err := production.WorkerOptionsFromEnv(); err != nil {
			t.Errorf("WorkerOptionsFromEnv(%s=4): %v", env, err)
		}
		t.Setenv(env, "")
	}
}

// TestWorkerOptionsFromEnvValidate comprueba que los rangos siguen aplicándose
// tras el parseo (grace >= 1, lote > 0).
func TestWorkerOptionsFromEnvValidate(t *testing.T) {
	t.Setenv(production.EnvReconcileGrace, "0")
	if _, err := production.WorkerOptionsFromEnv(); err == nil {
		t.Errorf("WorkerOptionsFromEnv(%s=0) sin error", production.EnvReconcileGrace)
	}
	t.Setenv(production.EnvReconcileGrace, "")
	t.Setenv(production.EnvSweepBatchSize, "-1")
	if _, err := production.WorkerOptionsFromEnv(); err == nil {
		t.Errorf("WorkerOptionsFromEnv(%s=-1) sin error", production.EnvSweepBatchSize)
	}
}
