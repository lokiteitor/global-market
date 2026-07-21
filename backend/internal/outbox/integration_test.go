package outbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/outbox"
	"github.com/lokiteitor/global-market/backend/internal/platform/migrate"
)

// settledPayload es el payload documentado del evento contract.settled
// (contrato entre agentes del Incremento 1): dinero y stock como string,
// jamás float.
type settledPayload struct {
	ContractID          string `json:"contract_id"`
	ProductID           string `json:"product_id"`
	DestinationRegionID string `json:"destination_region_id"`
	UnitPrice           string `json:"unit_price"`
	QuantityAgreed      string `json:"quantity_agreed"`
	QuantityDelivered   string `json:"quantity_delivered"`
	FillBp              int    `json:"fill_bp"`
	SettledAtSim        int64  `json:"settled_at_sim"`
	Status              string `json:"status"`
}

// TestOutboxIntegration ejercita el módulo contra una BD real con el esquema
// completo: Emit transaccional (visible tras COMMIT, invisible tras
// ROLLBACK), consumo en orden con cursor propio, exactly-once tras el fallo
// del handler (efectos y cursor se revierten juntos), filtrado por tipo,
// métricas e instancias concurrentes del mismo consumidor.
//
// Se omite si II_TEST_DATABASE_URL no está definida. La URL solo identifica
// el servidor: el test crea una base de datos EFÍMERA propia (el rol debe
// tener CREATEDB), le aplica las migraciones reales de db/migrations y la
// destruye al terminar (mismo patrón que el resto de módulos).
func TestOutboxIntegration(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test de integración omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	pool := newEphemeralDB(t, ctx, adminURL)
	reg := prometheus.NewRegistry()
	outbox.RegisterMetrics(reg)
	discard := slog.New(slog.NewTextHandler(io.Discard, nil))

	// ── Emit transaccional ──────────────────────────────────────────────────
	confirmedAgg := uuid.Must(uuid.NewV7())
	t.Run("emit visible tras commit e invisible tras rollback", func(t *testing.T) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("Begin: %v", err)
		}
		defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
		payload := map[string]any{"contract_id": confirmedAgg.String(), "unit_price": "1500"}
		if err := outbox.Emit(ctx, tx, 100, "contract", confirmedAgg, "contract.confirmed", payload); err != nil {
			t.Fatalf("Emit: %v", err)
		}
		// Antes del COMMIT, otra conexión no ve nada (transactional outbox).
		if got := countRows(t, ctx, pool, `SELECT count(*) FROM outbox.events`); got != 0 {
			t.Fatalf("eventos visibles antes del COMMIT: %d, esperado 0", got)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		if got := countRows(t, ctx, pool, `SELECT count(*) FROM outbox.events`); got != 1 {
			t.Fatalf("eventos tras el COMMIT: %d, esperado 1", got)
		}

		// La fila conserva lo emitido: payload JSONB, sim_time_at y agregado.
		var (
			eventID   uuid.UUID
			aggType   string
			aggID     uuid.UUID
			unitPrice string
			simAt     int64
		)
		err = pool.QueryRow(ctx, `
			SELECT event_id, aggregate_type, aggregate_id, payload->>'unit_price', sim_time_at
			FROM outbox.events WHERE event_type = 'contract.confirmed'`).
			Scan(&eventID, &aggType, &aggID, &unitPrice, &simAt)
		if err != nil {
			t.Fatalf("leyendo el evento: %v", err)
		}
		if eventID == uuid.Nil || aggType != "contract" || aggID != confirmedAgg ||
			unitPrice != "1500" || simAt != 100 {
			t.Fatalf("evento inesperado: id=%s agg=%s/%s unit_price=%q sim=%d",
				eventID, aggType, aggID, unitPrice, simAt)
		}

		// ROLLBACK del emisor: el evento desaparece con él.
		tx2, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("Begin (rollback): %v", err)
		}
		if err := outbox.Emit(ctx, tx2, 101, "contract", confirmedAgg, "contract.confirmed", payload); err != nil {
			t.Fatalf("Emit (rollback): %v", err)
		}
		if err := tx2.Rollback(ctx); err != nil {
			t.Fatalf("Rollback: %v", err)
		}
		if got := countRows(t, ctx, pool, `SELECT count(*) FROM outbox.events`); got != 1 {
			t.Fatalf("eventos tras el ROLLBACK: %d, esperado 1", got)
		}
	})

	// ── Datos del consumo: 8 eventos suscritos + 2 contract.settled ─────────
	pubAgg := uuid.Must(uuid.NewV7())
	accAgg := uuid.Must(uuid.NewV7())
	emitOrder := []string{
		"publication.created", "publication.created", "acceptance.registered",
		"publication.created", "acceptance.registered", "publication.created",
		"acceptance.registered", "publication.created",
	}
	for i, et := range emitOrder {
		agg, aggType := pubAgg, "publication"
		if et == "acceptance.registered" {
			agg, aggType = accAgg, "acceptance"
		}
		mustEmit(t, ctx, pool, int64(200+i), aggType, agg, et, map[string]any{"n": i})
	}
	settledAggs := []uuid.UUID{uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())}
	for i, agg := range settledAggs {
		mustEmit(t, ctx, pool, int64(300+i), "contract", agg, "contract.settled", settledPayload{
			ContractID:          agg.String(),
			ProductID:           uuid.Must(uuid.NewV7()).String(),
			DestinationRegionID: uuid.Must(uuid.NewV7()).String(),
			UnitPrice:           "1500",
			QuantityAgreed:      "1000",
			QuantityDelivered:   "600",
			FillBp:              6000,
			SettledAtSim:        int64(300 + i),
			Status:              "settled",
		})
	}
	matchSeqs := seqsOf(t, ctx, pool, "publication.created", "acceptance.registered")
	if len(matchSeqs) != 8 {
		t.Fatalf("eventos suscribibles: %d, esperado 8", len(matchSeqs))
	}
	failSeq := matchSeqs[3] // primer evento del segundo lote (batch 3)

	// ── Consumidor: orden, cursor y exactly-once tras un fallo ──────────────
	t.Run("consumidor procesa en orden y exactly-once tras fallo del handler", func(t *testing.T) {
		mustExec(t, ctx, pool, `
			CREATE TABLE consumed (
			    ord        BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			    seq        BIGINT NOT NULL UNIQUE,
			    event_type TEXT   NOT NULL
			)`)

		var failed atomic.Bool
		var failSeqAttempts atomic.Int64
		handler := func(ctx context.Context, tx pgx.Tx, ev outbox.Event) error {
			// El efecto se escribe ANTES del fallo inyectado: si el rollback
			// del lote no lo revirtiera, el UNIQUE(seq) delataría el duplicado.
			if _, err := tx.Exec(ctx,
				`INSERT INTO consumed (seq, event_type) VALUES ($1, $2)`, ev.Seq, ev.EventType); err != nil {
				return err
			}
			if ev.Seq == failSeq {
				failSeqAttempts.Add(1)
				if failed.CompareAndSwap(false, true) {
					return errors.New("fallo transitorio inyectado")
				}
			}
			return nil
		}

		consumer := outbox.NewConsumer(pool, "notifier",
			[]string{"publication.created", "acceptance.registered"},
			outbox.WithBatchSize(3), outbox.WithLogger(discard))
		runCtx, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- consumer.Run(runCtx, 10*time.Millisecond, handler) }()

		waitFor(t, 30*time.Second, "los 8 eventos consumidos", func() bool {
			return countRows(t, ctx, pool, `SELECT count(*) FROM consumed`) == 8
		})
		stop()
		if err := <-done; err != nil {
			t.Fatalf("Run devolvió error en el apagado: %v", err)
		}

		// Exactamente una vez y en orden de seq (pese al lote revertido).
		gotSeqs := seqsFrom(t, ctx, pool, `SELECT seq FROM consumed ORDER BY ord`)
		if len(gotSeqs) != len(matchSeqs) {
			t.Fatalf("efectos: %d, esperado %d", len(gotSeqs), len(matchSeqs))
		}
		for i, seq := range gotSeqs {
			if seq != matchSeqs[i] {
				t.Fatalf("orden roto en la posición %d: seq %d, esperado %d (todos: %v)", i, seq, matchSeqs[i], gotSeqs)
			}
		}
		if got := failSeqAttempts.Load(); got != 2 {
			t.Fatalf("intentos sobre el evento fallido: %d, esperado 2 (fallo + reintento)", got)
		}
		// Los tipos no suscritos no se consumen.
		if got := countRows(t, ctx, pool,
			`SELECT count(*) FROM consumed WHERE event_type NOT IN ('publication.created','acceptance.registered')`); got != 0 {
			t.Fatalf("eventos de tipos no suscritos consumidos: %d", got)
		}
		// Cursor registrado on-demand y avanzado hasta el último seq suscrito.
		if got := cursorOf(t, ctx, pool, "notifier"); got != matchSeqs[len(matchSeqs)-1] {
			t.Fatalf("cursor de notifier: %d, esperado %d", got, matchSeqs[len(matchSeqs)-1])
		}
	})

	// ── Segundo consumidor con cursor independiente y payload íntegro ───────
	t.Run("consumidor independiente recibe su tipo con el payload íntegro", func(t *testing.T) {
		var mu sync.Mutex
		var received []outbox.Event
		handler := func(ctx context.Context, tx pgx.Tx, ev outbox.Event) error {
			mu.Lock()
			defer mu.Unlock()
			received = append(received, ev)
			return nil
		}
		consumer := outbox.NewConsumer(pool, "settler", []string{"contract.settled"},
			outbox.WithLogger(discard))
		runCtx, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- consumer.Run(runCtx, 10*time.Millisecond, handler) }()

		waitFor(t, 30*time.Second, "los 2 contract.settled consumidos", func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(received) == 2
		})
		stop()
		if err := <-done; err != nil {
			t.Fatalf("Run devolvió error en el apagado: %v", err)
		}

		for i, ev := range received {
			if ev.EventType != "contract.settled" || ev.AggregateType != "contract" ||
				ev.AggregateID != settledAggs[i] || ev.SimTimeAt != int64(300+i) ||
				ev.EventID == uuid.Nil || ev.CreatedAt.IsZero() {
				t.Fatalf("evento %d inesperado: %+v", i, ev)
			}
			var p settledPayload
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				t.Fatalf("payload del evento %d: %v", i, err)
			}
			if p.ContractID != settledAggs[i].String() || p.UnitPrice != "1500" ||
				p.QuantityAgreed != "1000" || p.QuantityDelivered != "600" ||
				p.FillBp != 6000 || p.SettledAtSim != int64(300+i) || p.Status != "settled" {
				t.Fatalf("payload %d no coincide con lo emitido: %+v", i, p)
			}
		}
		if received[0].EventID == received[1].EventID {
			t.Fatal("event_id repetido entre eventos")
		}
		if received[0].Seq >= received[1].Seq {
			t.Fatalf("orden de seq roto: %d >= %d", received[0].Seq, received[1].Seq)
		}
		// Su cursor es independiente del de notifier: llega al final del outbox.
		maxSeq := int64(countRows(t, ctx, pool, `SELECT COALESCE(max(seq), 0) FROM outbox.events`))
		if got := cursorOf(t, ctx, pool, "settler"); got != maxSeq {
			t.Fatalf("cursor de settler: %d, esperado %d", got, maxSeq)
		}
	})

	// ── Métricas del contrato de observabilidad ──────────────────────────────
	t.Run("métricas", func(t *testing.T) {
		checks := []struct {
			metric, label, value string
			want                 float64
		}{
			{"ii_outbox_events_emitted_total", "event_type", "contract.confirmed", 2}, // 1 commit + 1 rollback (cuenta intentos)
			{"ii_outbox_events_emitted_total", "event_type", "publication.created", 5},
			{"ii_outbox_events_emitted_total", "event_type", "acceptance.registered", 3},
			{"ii_outbox_events_emitted_total", "event_type", "contract.settled", 2},
			{"ii_outbox_events_processed_total", "consumer", "notifier", 8},
			{"ii_outbox_events_processed_total", "consumer", "settler", 2},
		}
		for _, c := range checks {
			if got := metricValue(t, reg, c.metric, c.label, c.value); got < c.want {
				t.Errorf("%s{%s=%q} = %v, esperado >= %v", c.metric, c.label, c.value, got, c.want)
			}
		}
		// El lag es un gauge (Set en cada polling): valores exactos. Mide el
		// retraso REAL —eventos de LOS TIPOS SUSCRITOS pendientes—, no la
		// distancia a la cabecera global del outbox: notifier terminó con 2
		// contract.settled por encima de su cursor, pero NO son su trabajo (no
		// los consume), así que está al día y vale 0. Con la resta a max(seq)
		// este mismo caso daba 2 y el retraso fantasma crecía con la historia
		// del mundo.
		for _, consumer := range []string{"notifier", "settler"} {
			if got := metricValue(t, reg, "ii_outbox_consumer_lag", "consumer", consumer); got != 0 {
				t.Errorf("lag de %s: %v, esperado 0 (consumidor al día en sus tipos)", consumer, got)
			}
		}
		// La suscripción queda declarada en la fila del cursor (migración 0016):
		// es lo que permite medir ese retraso desde fuera del proceso.
		if got := subscriptionOf(t, ctx, pool, "settler"); len(got) != 1 || got[0] != "contract.settled" {
			t.Errorf("suscripción registrada de settler: %v, esperado [contract.settled]", got)
		}
	})

	// ── Instancias concurrentes del mismo consumidor (cursor compartido) ────
	t.Run("instancias concurrentes del mismo consumidor no duplican efectos", func(t *testing.T) {
		mustExec(t, ctx, pool, `CREATE TABLE consumed_stress (seq BIGINT NOT NULL)`)
		agg := uuid.Must(uuid.NewV7())
		const total = 30
		for i := range total {
			mustEmit(t, ctx, pool, int64(1000+i), "stress", agg, "stress.tick", map[string]any{"n": i})
		}
		handler := func(ctx context.Context, tx pgx.Tx, ev outbox.Event) error {
			_, err := tx.Exec(ctx, `INSERT INTO consumed_stress (seq) VALUES ($1)`, ev.Seq)
			return err
		}

		runCtx, stop := context.WithCancel(ctx)
		done := make(chan error, 2)
		for range 2 {
			c := outbox.NewConsumer(pool, "stress", []string{"stress.tick"},
				outbox.WithBatchSize(5), outbox.WithLogger(discard))
			go func() { done <- c.Run(runCtx, 5*time.Millisecond, handler) }()
		}
		waitFor(t, 30*time.Second, "los 30 stress.tick consumidos", func() bool {
			return countRows(t, ctx, pool, `SELECT count(*) FROM consumed_stress`) == total
		})
		// Margen para delatar un hipotético duplicado tardío.
		time.Sleep(100 * time.Millisecond)
		stop()
		for range 2 {
			if err := <-done; err != nil {
				t.Fatalf("Run devolvió error en el apagado: %v", err)
			}
		}

		if got := countRows(t, ctx, pool, `SELECT count(*) FROM consumed_stress`); got != total {
			t.Fatalf("efectos: %d, esperado %d (duplicados o pérdidas)", got, total)
		}
		if got := countRows(t, ctx, pool, `SELECT count(DISTINCT seq) FROM consumed_stress`); got != total {
			t.Fatalf("seqs distintos: %d, esperado %d (duplicados)", got, total)
		}
		stressSeqs := seqsOf(t, ctx, pool, "stress.tick")
		if got := cursorOf(t, ctx, pool, "stress"); got != stressSeqs[len(stressSeqs)-1] {
			t.Fatalf("cursor de stress: %d, esperado %d", got, stressSeqs[len(stressSeqs)-1])
		}
	})
}

// ─── Infraestructura del test ───────────────────────────────────────────────

// newEphemeralDB crea la BD efímera, aplica las migraciones reales y devuelve
// un pool sobre ella. Todo se destruye al terminar el test (mismo patrón que
// el resto de módulos).
func newEphemeralDB(t *testing.T, ctx context.Context, adminURL string) *pgxpool.Pool {
	t.Helper()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("conectando a %s: %v", adminURL, err)
	}
	dbName := fmt.Sprintf("outboxtest_%d_%d", os.Getpid(), time.Now().UnixNano())
	quoted := pgx.Identifier{dbName}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+quoted); err != nil {
		t.Fatalf("creando la BD efímera: %v", err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		defer admin.Close(dropCtx)
		if _, err := admin.Exec(dropCtx, "DROP DATABASE "+quoted+" WITH (FORCE)"); err != nil {
			t.Errorf("eliminando la BD efímera %s: %v", dbName, err)
		}
	})

	connCfg, err := pgx.ParseConfig(adminURL)
	if err != nil {
		t.Fatalf("interpretando II_TEST_DATABASE_URL: %v", err)
	}
	connCfg.Database = dbName
	conn, err := pgx.ConnectConfig(ctx, connCfg)
	if err != nil {
		t.Fatalf("conectando a la BD efímera: %v", err)
	}
	if _, err := migrate.New(conn, "../../db/migrations", "dev", io.Discard).Up(ctx); err != nil {
		t.Fatalf("aplicando las migraciones: %v", err)
	}
	if err := conn.Close(ctx); err != nil {
		t.Fatalf("cerrando la conexión de migraciones: %v", err)
	}

	poolCfg, err := pgxpool.ParseConfig(adminURL)
	if err != nil {
		t.Fatalf("interpretando la URL del pool: %v", err)
	}
	poolCfg.ConnConfig.Database = dbName
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		t.Fatalf("creando el pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// mustEmit emite un evento en una transacción propia y la confirma.
func mustEmit(t *testing.T, ctx context.Context, pool *pgxpool.Pool, simTime int64, aggType string, aggID uuid.UUID, eventType string, payload any) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck
	if err := outbox.Emit(ctx, tx, simTime, aggType, aggID, eventType, payload); err != nil {
		t.Fatalf("Emit(%s): %v", eventType, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("Commit(%s): %v", eventType, err)
	}
}

func mustExec(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string) {
	t.Helper()
	if _, err := pool.Exec(ctx, sql); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func countRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, sql).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return n
}

// seqsOf devuelve los seq de los eventos de los tipos dados, en orden.
func seqsOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, types ...string) []int64 {
	t.Helper()
	rows, err := pool.Query(ctx,
		`SELECT seq FROM outbox.events WHERE event_type = ANY($1) ORDER BY seq`, types)
	if err != nil {
		t.Fatalf("consultando seqs de %v: %v", types, err)
	}
	defer rows.Close()
	var seqs []int64
	for rows.Next() {
		var s int64
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan seq: %v", err)
		}
		seqs = append(seqs, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterando seqs: %v", err)
	}
	return seqs
}

// seqsFrom devuelve la columna de seqs que produce la query dada.
func seqsFrom(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string) []int64 {
	t.Helper()
	rows, err := pool.Query(ctx, sql)
	if err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	defer rows.Close()
	var seqs []int64
	for rows.Next() {
		var s int64
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan seq: %v", err)
		}
		seqs = append(seqs, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterando %q: %v", sql, err)
	}
	return seqs
}

// cursorOf devuelve el last_seq del consumidor (falla si no está registrado).
func cursorOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) int64 {
	t.Helper()
	var seq int64
	if err := pool.QueryRow(ctx,
		`SELECT last_seq FROM outbox.consumer_cursors WHERE consumer_name = $1`, name).Scan(&seq); err != nil {
		t.Fatalf("cursor de %s: %v", name, err)
	}
	return seq
}

// subscriptionOf devuelve los tipos de evento que el consumidor dejó
// declarados en su fila de cursor (migración 0016).
func subscriptionOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) []string {
	t.Helper()
	var types []string
	if err := pool.QueryRow(ctx,
		`SELECT event_types FROM outbox.consumer_cursors WHERE consumer_name = $1`, name).Scan(&types); err != nil {
		t.Fatalf("suscripción de %s: %v", name, err)
	}
	return types
}

// waitFor espera a que cond sea cierta con un plazo máximo.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout esperando %s", what)
}

// metricValue suma las series (counter o gauge) de una métrica cuyo par
// etiqueta=valor coincide.
func metricValue(t *testing.T, reg *prometheus.Registry, name, label, value string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("recogiendo métricas: %v", err)
	}
	var sum float64
	for _, mf := range families {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == label && lp.GetValue() == value {
					sum += m.GetCounter().GetValue() + m.GetGauge().GetValue()
				}
			}
		}
	}
	return sum
}
