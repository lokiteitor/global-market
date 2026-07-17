package auth

// Test de integración del módulo auth contra PostgreSQL real, siguiendo el
// patrón de internal/platform/migrate: gated por II_TEST_DATABASE_URL, que
// solo identifica el servidor — el test crea una base de datos EFÍMERA
// propia (el rol necesita CREATEDB), aplica las migraciones reales sobre
// ella y la destruye al terminar.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/platform/migrate"
)

// migrationsDir es la ruta de las migraciones reales relativa a este paquete.
const migrationsDir = "../../db/migrations"

func TestAuthIntegration(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test de integración omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// BD efímera propia.
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("conectando a %s: %v", adminURL, err)
	}
	dbName := fmt.Sprintf("authtest_%d_%d", os.Getpid(), time.Now().UnixNano())
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

	// Migraciones reales sobre la BD efímera.
	connCfg, err := pgx.ParseConfig(adminURL)
	if err != nil {
		t.Fatalf("interpretando II_TEST_DATABASE_URL: %v", err)
	}
	connCfg.Database = dbName
	migConn, err := pgx.ConnectConfig(ctx, connCfg)
	if err != nil {
		t.Fatalf("conectando a la BD efímera: %v", err)
	}
	if _, err := migrate.New(migConn, migrationsDir, "dev", io.Discard).Up(ctx); err != nil {
		migConn.Close(context.Background())
		t.Fatalf("aplicando migraciones: %v", err)
	}
	migConn.Close(context.Background())

	// Pool + módulo real.
	poolCfg, err := pgxpool.ParseConfig(adminURL)
	if err != nil {
		t.Fatalf("interpretando URL para el pool: %v", err)
	}
	poolCfg.ConnConfig.Database = dbName
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		t.Fatalf("creando el pool: %v", err)
	}
	t.Cleanup(pool.Close)

	repo := NewPGRepository(pool)
	svc, err := NewService(repo, testLogger())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	// Semilla: cuenta humana y bot con credenciales (y perfil de bot).
	const humanSecret = "secreto-humano-1"
	humanID := seedAccount(t, ctx, pool, "human", "Ferro SA", humanSecret)
	const botSecret = "secreto-bot-1"
	botID := seedAccount(t, ctx, pool, "bot", "Bot-01", botSecret)
	if _, err := pool.Exec(ctx,
		`INSERT INTO auth.bot_profiles (account_id, archetype) VALUES ($1, 'primary_producer')`,
		botID); err != nil {
		t.Fatalf("insertando bot_profile: %v", err)
	}

	// Montaje HTTP como lo haría el composition root.
	opts := Options{LoginPerMin: 100, APIRPS: 100, APIBurst: 100}
	metrics := NewMetrics(prometheus.NewRegistry())
	mw := NewMiddleware(svc, opts, metrics, testLogger())
	handlers := NewHandlers(svc, stubMeta{}, opts, metrics, testLogger())
	mux := http.NewServeMux()
	mux.Handle("POST /auth/sessions", handlers.CreateSession())
	mux.Handle("DELETE /auth/sessions/current", mw.RequireAuth(handlers.DeleteCurrentSession()))
	mux.Handle("GET /auth/me", mw.RequireAuth(mw.RateLimitAPI(handlers.Me())))

	do := func(method, path, token, body string) *httptest.ResponseRecorder {
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, path, rd)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	var humanToken, humanSessionID string

	t.Run("login ok (case-insensitive) devuelve 201 con el contrato", func(t *testing.T) {
		rec := do(http.MethodPost, "/auth/sessions", "",
			`{"account_name":"ferro sa","secret":"`+humanSecret+`","client_info":{"app":"integ"}}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, cuerpo %s", rec.Code, rec.Body.String())
		}
		var env struct {
			Data struct {
				SessionID string         `json:"session_id"`
				Token     string         `json:"token"`
				ExpiresAt time.Time      `json:"expires_at"`
				Account   map[string]any `json:"account"`
			} `json:"data"`
			Meta json.RawMessage `json:"meta"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decodificando envelope: %v", err)
		}
		if len(env.Data.Token) != 43 {
			t.Errorf("token de %d caracteres, esperado 43", len(env.Data.Token))
		}
		if d := time.Until(env.Data.ExpiresAt); d < 23*time.Hour || d > 25*time.Hour {
			t.Errorf("expires_at %v no es ~now+24h", env.Data.ExpiresAt)
		}
		if env.Data.Account["id"] != humanID.String() || env.Data.Account["name"] != "Ferro SA" ||
			env.Data.Account["kind"] != "human" || env.Data.Account["status"] != "active" {
			t.Errorf("account inesperada: %v", env.Data.Account)
		}
		if _, ok := env.Data.Account["bot_archetype"]; ok {
			t.Error("bot_archetype presente en cuenta humana")
		}
		humanToken = env.Data.Token
		humanSessionID = env.Data.SessionID

		// La BD solo guarda el hash del token, nunca el token en claro.
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM auth.sessions WHERE token_hash = $1`,
			HashToken(humanToken)).Scan(&n); err != nil || n != 1 {
			t.Errorf("sesión por token_hash: n=%d err=%v", n, err)
		}
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM auth.sessions WHERE token_hash = $1`,
			humanToken).Scan(&n); err != nil || n != 0 {
			t.Errorf("el token en claro aparece en la BD: n=%d err=%v", n, err)
		}
	})

	t.Run("me con token válido devuelve la cuenta", func(t *testing.T) {
		rec := do(http.MethodGet, "/auth/me", humanToken, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, cuerpo %s", rec.Code, rec.Body.String())
		}
		var env struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decodificando envelope: %v", err)
		}
		if env.Data["id"] != humanID.String() || env.Data["name"] != "Ferro SA" {
			t.Errorf("account = %v", env.Data)
		}
	})

	t.Run("login de bot incluye bot_archetype", func(t *testing.T) {
		rec := do(http.MethodPost, "/auth/sessions", "",
			`{"account_name":"Bot-01","secret":"`+botSecret+`"}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, cuerpo %s", rec.Code, rec.Body.String())
		}
		var env struct {
			Data struct {
				Account map[string]any `json:"account"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decodificando envelope: %v", err)
		}
		if env.Data.Account["bot_archetype"] != "primary_producer" {
			t.Errorf("bot_archetype = %v, esperado primary_producer", env.Data.Account["bot_archetype"])
		}
	})

	t.Run("credenciales inválidas devuelven 401 genérico", func(t *testing.T) {
		for name, body := range map[string]string{
			"cuenta inexistente": `{"account_name":"NoExiste","secret":"x"}`,
			"secreto incorrecto": `{"account_name":"Ferro SA","secret":"malo"}`,
		} {
			rec := do(http.MethodPost, "/auth/sessions", "", body)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s: status = %d, esperado 401", name, rec.Code)
			}
		}
	})

	t.Run("token inválido devuelve 401", func(t *testing.T) {
		fake, _ := NewToken()
		rec := do(http.MethodGet, "/auth/me", fake, "")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, esperado 401", rec.Code)
		}
	})

	t.Run("sesión expirada devuelve 401", func(t *testing.T) {
		expiredToken, err := NewToken()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO auth.sessions (account_id, token_hash, expires_at)
			 VALUES ($1, $2, now() - interval '1 hour')`,
			humanID, HashToken(expiredToken)); err != nil {
			t.Fatalf("insertando sesión expirada: %v", err)
		}
		rec := do(http.MethodGet, "/auth/me", expiredToken, "")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, esperado 401", rec.Code)
		}
	})

	t.Run("touch de last_seen_at está throttled", func(t *testing.T) {
		var sessID uuid.UUID
		var before time.Time
		if err := pool.QueryRow(ctx,
			`SELECT id, last_seen_at FROM auth.sessions WHERE token_hash = $1`,
			HashToken(humanToken)).Scan(&sessID, &before); err != nil {
			t.Fatalf("leyendo la sesión: %v", err)
		}
		// Reciente: el UPDATE no debe aplicar.
		if err := repo.TouchSessionLastSeen(ctx, sessID); err != nil {
			t.Fatalf("TouchSessionLastSeen: %v", err)
		}
		var after time.Time
		if err := pool.QueryRow(ctx,
			`SELECT last_seen_at FROM auth.sessions WHERE id = $1`, sessID).Scan(&after); err != nil {
			t.Fatalf("releyendo last_seen_at: %v", err)
		}
		if !after.Equal(before) {
			t.Errorf("last_seen_at cambió con antigüedad < 60s: %v -> %v", before, after)
		}
		// Antiguo: el UPDATE debe aplicar.
		if _, err := pool.Exec(ctx,
			`UPDATE auth.sessions SET last_seen_at = now() - interval '2 minutes' WHERE id = $1`,
			sessID); err != nil {
			t.Fatalf("envejeciendo last_seen_at: %v", err)
		}
		if err := repo.TouchSessionLastSeen(ctx, sessID); err != nil {
			t.Fatalf("TouchSessionLastSeen: %v", err)
		}
		if err := pool.QueryRow(ctx,
			`SELECT last_seen_at FROM auth.sessions WHERE id = $1`, sessID).Scan(&after); err != nil {
			t.Fatalf("releyendo last_seen_at: %v", err)
		}
		if time.Since(after) > time.Minute {
			t.Errorf("last_seen_at no se actualizó: %v", after)
		}
	})

	t.Run("logout invalida la sesión", func(t *testing.T) {
		rec := do(http.MethodDelete, "/auth/sessions/current", humanToken, "")
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, esperado 204 (cuerpo %s)", rec.Code, rec.Body.String())
		}
		if rec.Body.Len() != 0 {
			t.Errorf("cuerpo no vacío en 204: %q", rec.Body.String())
		}
		rec = do(http.MethodGet, "/auth/me", humanToken, "")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("me tras logout: status = %d, esperado 401", rec.Code)
		}
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM auth.sessions WHERE id = $1`,
			uuid.MustParse(humanSessionID)).Scan(&n); err != nil || n != 0 {
			t.Errorf("la sesión sigue en BD tras logout: n=%d err=%v", n, err)
		}
	})
}

// seedAccount inserta una cuenta con su credencial argon2id y devuelve su id.
func seedAccount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, kind, name, secret string) uuid.UUID {
	t.Helper()
	hash, err := HashSecret(secret)
	if err != nil {
		t.Fatalf("HashSecret: %v", err)
	}
	var id uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO auth.accounts (kind, name) VALUES ($1, $2) RETURNING id`,
		kind, name).Scan(&id); err != nil {
		t.Fatalf("insertando cuenta %s: %v", name, err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO auth.account_credentials (account_id, secret_hash) VALUES ($1, $2)`,
		id, hash); err != nil {
		t.Fatalf("insertando credencial de %s: %v", name, err)
	}
	return id
}
