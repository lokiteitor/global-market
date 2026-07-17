package outbox

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestEmitValidation cubre las validaciones de Emit que fallan ANTES de tocar
// la transacción (por eso tx puede ser nil en todos los casos).
func TestEmitValidation(t *testing.T) {
	ctx := context.Background()
	agg := uuid.Must(uuid.NewV7())

	tests := []struct {
		name    string
		call    func() error
		wantSub string
	}{
		{
			name:    "aggregateType vacío",
			call:    func() error { return Emit(ctx, nil, 0, "   ", agg, "contract.settled", nil) },
			wantSub: "aggregateType",
		},
		{
			name:    "aggregateID nulo",
			call:    func() error { return Emit(ctx, nil, 0, "contract", uuid.Nil, "contract.settled", nil) },
			wantSub: "aggregateID",
		},
		{
			name:    "eventType vacío",
			call:    func() error { return Emit(ctx, nil, 0, "contract", agg, "", nil) },
			wantSub: "eventType",
		},
		{
			name:    "simTime negativo",
			call:    func() error { return Emit(ctx, nil, -1, "contract", agg, "contract.settled", nil) },
			wantSub: "simTime",
		},
		{
			name:    "payload no serializable",
			call:    func() error { return Emit(ctx, nil, 0, "contract", agg, "contract.settled", make(chan int)) },
			wantSub: "payload",
		},
		{
			name: "tx nil con argumentos válidos",
			call: func() error {
				return Emit(ctx, nil, 0, "contract", agg, "contract.settled", map[string]string{"k": "v"})
			},
			wantSub: "tx nil",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %v, esperado que contenga %q", err, tc.wantSub)
			}
		})
	}
}

// TestNewConsumerDefaults verifica los valores por defecto, las opciones y
// que eventTypes se copia (el llamante no puede mutarlo después).
func TestNewConsumerDefaults(t *testing.T) {
	types := []string{"publication.created"}
	c := NewConsumer(nil, "notifier", types)
	if c.batchSize != DefaultBatchSize {
		t.Fatalf("batchSize por defecto: %d, esperado %d", c.batchSize, DefaultBatchSize)
	}
	if c.logger == nil {
		t.Fatal("logger por defecto ausente")
	}
	types[0] = "mutado"
	if c.eventTypes[0] != "publication.created" {
		t.Fatalf("eventTypes no se copió: %v", c.eventTypes)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	c2 := NewConsumer(nil, "notifier", []string{"a.b"}, WithBatchSize(7), WithLogger(logger))
	if c2.batchSize != 7 {
		t.Fatalf("WithBatchSize(7): batchSize %d", c2.batchSize)
	}
	if c2.logger != logger {
		t.Fatal("WithLogger no aplicó el logger")
	}

	// WithLogger(nil) conserva el logger anterior (nunca deja nil).
	c3 := NewConsumer(nil, "notifier", []string{"a.b"}, WithLogger(nil))
	if c3.logger == nil {
		t.Fatal("WithLogger(nil) dejó el logger en nil")
	}
}

// TestRunValidation cubre las configuraciones inválidas: Run devuelve error
// de inmediato, sin tocar la BD (el pool es perezoso: no conecta).
func TestRunValidation(t *testing.T) {
	ctx := context.Background()
	handler := func(context.Context, pgx.Tx, Event) error { return nil }

	// Pool perezoso: se construye sin conectar (igual que platform/db).
	pool, err := pgxpool.New(ctx, "postgres://outbox:unused@127.0.0.1:1/unused")
	if err != nil {
		t.Fatalf("creando el pool perezoso: %v", err)
	}
	defer pool.Close()

	tests := []struct {
		name     string
		consumer *Consumer
		interval time.Duration
		handler  Handler
		wantSub  string
	}{
		{"sin pool", NewConsumer(nil, "c", []string{"a.b"}), time.Second, handler, "pool"},
		{"sin nombre", NewConsumer(pool, "  ", []string{"a.b"}), time.Second, handler, "nombre"},
		{"sin tipos", NewConsumer(pool, "c", nil), time.Second, handler, "tipos de evento"},
		{"tipo vacío", NewConsumer(pool, "c", []string{"a.b", " "}), time.Second, handler, "tipo de evento vacío"},
		{"lote inválido", NewConsumer(pool, "c", []string{"a.b"}, WithBatchSize(0)), time.Second, handler, "lote"},
		{"intervalo inválido", NewConsumer(pool, "c", []string{"a.b"}), 0, handler, "intervalo"},
		{"sin handler", NewConsumer(pool, "c", []string{"a.b"}), time.Second, nil, "handler"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.consumer.Run(ctx, tc.interval, tc.handler)
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("Run: %v, esperado error que contenga %q", err, tc.wantSub)
			}
		})
	}
}

// TestSleepHonorsContext verifica que la espera se interrumpe al cancelar.
func TestSleepHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if sleep(ctx, time.Hour) {
		t.Fatal("sleep debería devolver false con el contexto cancelado")
	}
	if !sleep(context.Background(), time.Millisecond) {
		t.Fatal("sleep debería devolver true al agotar la espera")
	}
}
