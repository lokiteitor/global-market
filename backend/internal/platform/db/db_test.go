package db

import (
	"context"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/platform/config"
)

func testConfig(url string) config.Config {
	return config.Config{DatabaseURL: url}
}

// El pool es perezoso: se construye sin BD accesible (readyz decide después).
func TestNewPoolLazy(t *testing.T) {
	pool, err := NewPool(context.Background(), testConfig("postgres://nadie:nada@127.0.0.1:1/inexistente?sslmode=disable"))
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()
}

func TestNewPoolInvalidURL(t *testing.T) {
	if _, err := NewPool(context.Background(), testConfig("://esto-no-es-una-url")); err == nil {
		t.Fatal("NewPool sin error con URL inválida")
	}
}

func TestPoolCollectorExposesStats(t *testing.T) {
	pool, err := NewPool(context.Background(), testConfig("postgres://nadie:nada@127.0.0.1:1/inexistente?sslmode=disable"))
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

	reg := prometheus.NewRegistry()
	reg.MustRegister(NewPoolCollector(pool))

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	got := make(map[string]bool, len(families))
	for _, f := range families {
		got[f.GetName()] = true
	}
	for _, want := range []string{
		"ii_pgxpool_acquires_total",
		"ii_pgxpool_acquired_conns",
		"ii_pgxpool_idle_conns",
		"ii_pgxpool_max_conns",
		"ii_pgxpool_total_conns",
	} {
		if !got[want] {
			names := make([]string, 0, len(got))
			for n := range got {
				names = append(names, n)
			}
			t.Fatalf("falta la métrica %q; presentes: %s", want, strings.Join(names, ", "))
		}
	}
}
