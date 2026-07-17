package logistics_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lokiteitor/global-market/backend/internal/ledger"
	"github.com/lokiteitor/global-market/backend/internal/logistics"
	"github.com/lokiteitor/global-market/backend/internal/platform/httpx"
	"github.com/lokiteitor/global-market/backend/internal/platform/migrate"
	"github.com/lokiteitor/global-market/backend/internal/seed"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// TestLogisticsIntegration ejercita el Logistics Service (grafo, route-plans y
// CRUD de rutas) contra una BD real con el esquema migrado y el seed mínimo
// (región Askadia + corporaciones Demo/Norte). El seed no crea red vial, así que
// el test siembra su propio grafo de nodos/enlaces/segmentos por SQL.
//
// Se omite si II_TEST_DATABASE_URL no está definida. La URL solo identifica el
// servidor: el test crea una BD EFÍMERA propia (rol con CREATEDB), le aplica las
// migraciones reales y la destruye al terminar.
func TestLogisticsIntegration(t *testing.T) {
	adminURL := os.Getenv("II_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("II_TEST_DATABASE_URL no definida: test de integración omitido")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	pool := newEphemeralDB(t, ctx, adminURL)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := seed.Run(ctx, pool, seed.Options{
		DemoName: seed.DefaultDemoName, DemoSecret: "demo-secret-test",
		TraderName: seed.DefaultTraderName, TraderSecret: "norte-secret-test",
		Ledger:              ledger.DefaultOptions(),
		SkipIndustrialWorld: true,
	}, logger); err != nil {
		t.Fatalf("seed: %v", err)
	}

	region := regionID(t, ctx, pool, seed.RegionName)
	demo := accountID(t, ctx, pool, seed.DefaultDemoName)
	norte := accountID(t, ctx, pool, seed.DefaultTraderName)

	// ── Grafo de prueba ───────────────────────────────────────────────────────
	// Dos caminos A→B a escala de HORAS (la ETA del planner replica la fórmula
	// vinculante world.segment_travel_seconds: ceil(length_km*congestion/speed)*3600):
	// el directo (linkAB, 100 km ⇒ 1 h) y el desvío A→C→B (60+60 km ⇒ 2 h). D queda
	// aislado (sin arista entrante) para el caso NO_ROUTE_FOUND. Un ramal multimodal
	// A→E (road) + E→B (rail) prueba la validación de terminal.
	a := insertNode(t, ctx, pool, region, "junction", 0, 0)
	b := insertNode(t, ctx, pool, region, "warehouse", 100000, 0)
	c := insertNode(t, ctx, pool, region, "junction", 60000, 30000)
	d := insertNode(t, ctx, pool, region, "junction", 200000, 0)
	e := insertNode(t, ctx, pool, region, "station", 4000, -3000)

	linkAB := insertLink(t, ctx, pool, region, "road", a, b, 100000, 1000, 100, "0 0", "100000 0", 1.0)
	linkAC := insertLink(t, ctx, pool, region, "road", a, c, 60000, 1000, 100, "0 0", "60000 30000", 1.0)
	linkCB := insertLink(t, ctx, pool, region, "road", c, b, 60000, 1000, 100, "60000 30000", "100000 0", 1.0)
	linkAE := insertLink(t, ctx, pool, region, "road", a, e, 5000, 800, 90, "0 0", "4000 -3000", 1.0)
	linkEBrail := insertLink(t, ctx, pool, region, "rail", e, b, 7000, 5000, 120, "4000 -3000", "100000 0", 1.0)

	svc, err := logistics.NewService(pool, logistics.DefaultOptions(), logger, nil)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	demoMux := newMux(svc, demo, logger)
	norteMux := newMux(svc, norte, logger)

	// ── Grafo: nodos y enlaces ────────────────────────────────────────────────
	t.Run("grafo: nodos con filtro por región/clase y geometría", func(t *testing.T) {
		nodes, _ := listOf[nodeDTO](t, demoMux, "/logistics/network/nodes?region_id="+region.String())
		// A, B, C, D, E del fixture + los 2 almacenes del seed (Demo/Norte) = 7.
		if len(nodes) < 5 {
			t.Fatalf("nodos: %d, esperado >= 5", len(nodes))
		}
		byID := map[string]nodeDTO{}
		for _, n := range nodes {
			byID[n.ID] = n
		}
		if na, ok := byID[a.String()]; !ok || na.Kind != "junction" {
			t.Fatalf("nodo A ausente o mal serializado: %+v", na)
		}
		assertGeo(t, byID[a.String()].Location, "Point")

		// Filtro por clase: solo las estaciones (E).
		stations, _ := listOf[nodeDTO](t, demoMux, "/logistics/network/nodes?kind=station")
		if len(stations) != 1 || stations[0].ID != e.String() {
			t.Fatalf("kind=station: %+v", stations)
		}
		// Clase inválida → 400.
		if code, status := errorOf(t, demoMux, http.MethodGet, "/logistics/network/nodes?kind=nope", ""); status != http.StatusBadRequest || code != httpx.CodeValidationError {
			t.Fatalf("kind inválido: code=%s status=%d", code, status)
		}
	})

	t.Run("grafo: enlaces con sus segmentos y congestión EMA", func(t *testing.T) {
		links, _ := listOf[linkDTO](t, demoMux, "/logistics/network/links?region_id="+region.String())
		byID := map[string]linkDTO{}
		for _, l := range links {
			byID[l.ID] = l
		}
		ab := byID[linkAB.String()]
		if ab.Mode != "road" || ab.FromNodeID != a.String() || ab.ToNodeID != b.String() || ab.LengthM != 100000 {
			t.Fatalf("linkAB mal serializado: %+v", ab)
		}
		if len(ab.Segments) != 1 || ab.Segments[0].CongestionEma != 1.0 || ab.Segments[0].RegionID != region.String() {
			t.Fatalf("segmentos de linkAB inesperados: %+v", ab.Segments)
		}
		// Filtro por modo rail: solo el ramal ferroviario.
		rails, _ := listOf[linkDTO](t, demoMux, "/logistics/network/links?mode=rail")
		if len(rails) != 1 || rails[0].ID != linkEBrail.String() {
			t.Fatalf("mode=rail: %+v", rails)
		}
		// Filtro por from_node_id = A: linkAB, linkAC, linkAE (3 salientes de A).
		fromA, _ := listOf[linkDTO](t, demoMux, "/logistics/network/links?from_node_id="+a.String())
		if len(fromA) != 3 {
			t.Fatalf("from_node_id=A: %d enlaces, esperado 3", len(fromA))
		}
	})

	// ── Route-plan ────────────────────────────────────────────────────────────
	t.Run("route-plan: elige el camino de menor tiempo", func(t *testing.T) {
		plan := planOf(t, demoMux, fmt.Sprintf(`{"origin_node_id":%q,"destination_node_id":%q,"optimize":"time","modes":["road"]}`, a, b))
		// Fluido, el directo (100 km a 100 km/h ⇒ ceil(1 h)=3600 s) gana al desvío
		// (60+60 km ⇒ 3600+3600 = 7200 s).
		if len(plan.Legs) != 1 || plan.Legs[0].LinkID != linkAB.String() {
			t.Fatalf("plan fluido esperado [AB], obtenido %+v", plan.Legs)
		}
		if plan.TotalEtaSimSeconds != 3600 {
			t.Fatalf("ETA total esperada 3600, obtenida %d", plan.TotalEtaSimSeconds)
		}
		if plan.EstimatedCost == "" {
			t.Fatalf("estimated_cost ausente")
		}
	})

	t.Run("route-plan: congestión alta desvía la ruta", func(t *testing.T) {
		setCongestion(t, ctx, pool, linkAB, 2.5) // directo: ceil(100 km*2.5/100)=3 h=10800 s > desvío 7200 s
		defer setCongestion(t, ctx, pool, linkAB, 1.0)
		plan := planOf(t, demoMux, fmt.Sprintf(`{"origin_node_id":%q,"destination_node_id":%q,"optimize":"time","modes":["road"]}`, a, b))
		if len(plan.Legs) != 2 || plan.Legs[0].LinkID != linkAC.String() || plan.Legs[1].LinkID != linkCB.String() {
			t.Fatalf("plan con congestión esperado [AC, CB], obtenido %+v", plan.Legs)
		}
	})

	t.Run("route-plan: coste estima y devuelve estimated_cost", func(t *testing.T) {
		plan := planOf(t, demoMux, fmt.Sprintf(`{"origin_node_id":%q,"destination_node_id":%q,"optimize":"cost","cargo_volume":"10","modes":["road"]}`, a, b))
		cost, err := strconv.ParseInt(plan.EstimatedCost, 10, 64)
		if err != nil || cost <= 0 {
			t.Fatalf("estimated_cost inválido: %q (%v)", plan.EstimatedCost, err)
		}
	})

	t.Run("route-plan: NO_ROUTE_FOUND si desconectado", func(t *testing.T) {
		code, status := errorOf(t, demoMux, http.MethodPost, "/logistics/route-plans",
			fmt.Sprintf(`{"origin_node_id":%q,"destination_node_id":%q}`, a, d))
		if status != http.StatusUnprocessableEntity || code != "NO_ROUTE_FOUND" {
			t.Fatalf("desconectado: code=%s status=%d", code, status)
		}
	})

	t.Run("route-plan: 404 si el nodo no existe", func(t *testing.T) {
		ghost := uuid.Must(uuid.NewV7())
		code, status := errorOf(t, demoMux, http.MethodPost, "/logistics/route-plans",
			fmt.Sprintf(`{"origin_node_id":%q,"destination_node_id":%q}`, ghost, b))
		if status != http.StatusNotFound || code != httpx.CodeNotFound {
			t.Fatalf("nodo inexistente: code=%s status=%d", code, status)
		}
	})

	// ── Rutas: creación y validación ──────────────────────────────────────────
	t.Run("route create: secuencia contigua se acepta", func(t *testing.T) {
		route := createRoute(t, demoMux, fmt.Sprintf(`{"name":"Askadia Norte","kind":"fixed_line","legs":[%q,%q]}`, linkAC, linkCB))
		if route.OwnerAccountID != demo.String() || route.Kind != "fixed_line" || !route.Active {
			t.Fatalf("ruta creada inesperada: %+v", route)
		}
		if len(route.Legs) != 2 || route.Legs[0].LinkID != linkAC.String() || route.Legs[1].LinkID != linkCB.String() {
			t.Fatalf("legs de la ruta inesperados: %+v", route.Legs)
		}
	})

	t.Run("route create: legs no contiguos → 422", func(t *testing.T) {
		// linkAB termina en B; linkCB empieza en C → discontinuo.
		code, status := errorOf(t, demoMux, http.MethodPost, "/logistics/routes",
			fmt.Sprintf(`{"name":"rota","kind":"on_demand","legs":[%q,%q]}`, linkAB, linkCB))
		if status != http.StatusUnprocessableEntity || code != httpx.CodeValidationError {
			t.Fatalf("no contigua: code=%s status=%d", code, status)
		}
	})

	t.Run("route create: multimodal sin terminal → 422; con terminal → 201", func(t *testing.T) {
		// A→E (road) + E→B (rail): cambia de modo en E, que aún no tiene terminal.
		body := fmt.Sprintf(`{"name":"multimodal","kind":"fixed_line","legs":[%q,%q]}`, linkAE, linkEBrail)
		code, status := errorOf(t, demoMux, http.MethodPost, "/logistics/routes", body)
		if status != http.StatusUnprocessableEntity || code != httpx.CodeValidationError {
			t.Fatalf("multimodal sin terminal: code=%s status=%d", code, status)
		}
		// Con terminal en E, el mismo salto multimodal es válido.
		insertTerminal(t, ctx, pool, e, demo)
		route := createRoute(t, demoMux, body)
		if len(route.Legs) != 2 {
			t.Fatalf("multimodal con terminal: legs %+v", route.Legs)
		}
	})

	t.Run("route create: enlace inexistente → 422", func(t *testing.T) {
		ghost := uuid.Must(uuid.NewV7())
		code, status := errorOf(t, demoMux, http.MethodPost, "/logistics/routes",
			fmt.Sprintf(`{"name":"fantasma","kind":"on_demand","legs":[%q]}`, ghost))
		if status != http.StatusUnprocessableEntity || code != httpx.CodeValidationError {
			t.Fatalf("enlace inexistente: code=%s status=%d", code, status)
		}
	})

	// ── Rutas: CRUD, listado, propiedad ───────────────────────────────────────
	t.Run("route CRUD: listado, detalle, patch, delete y 403 ajeno", func(t *testing.T) {
		route := createRoute(t, demoMux, fmt.Sprintf(`{"name":"CRUD","kind":"on_demand","legs":[%q,%q]}`, linkAC, linkCB))
		id := route.ID

		// Listado propio contiene la ruta; filtro por kind.
		routes, _ := listOf[routeDTO](t, demoMux, "/logistics/routes?kind=on_demand")
		if !containsRoute(routes, id) {
			t.Fatalf("la ruta %s no aparece en el listado on_demand", id)
		}
		// Norte NO ve la ruta de Demo (listado por propiedad).
		norteRoutes, _ := listOf[routeDTO](t, norteMux, "/logistics/routes")
		if containsRoute(norteRoutes, id) {
			t.Fatalf("Norte ve una ruta ajena %s", id)
		}

		// Detalle: Demo 200; Norte 403.
		got := objectOf[routeDTO](t, demoMux, "/logistics/routes/"+id)
		if got.ID != id || len(got.Legs) != 2 {
			t.Fatalf("detalle inesperado: %+v", got)
		}
		if _, status := errorOf(t, norteMux, http.MethodGet, "/logistics/routes/"+id, ""); status != http.StatusForbidden {
			t.Fatalf("detalle ajeno: status %d, esperado 403", status)
		}

		// Patch de name/active.
		patched := patchRoute(t, demoMux, id, `{"name":"CRUD renombrada","active":false}`)
		if patched.Name != "CRUD renombrada" || patched.Active {
			t.Fatalf("patch de name/active no aplicado: %+v", patched)
		}
		// Patch de legs: reemplaza la secuencia por un solo tramo directo.
		relegged := patchRoute(t, demoMux, id, fmt.Sprintf(`{"legs":[%q]}`, linkAB))
		if len(relegged.Legs) != 1 || relegged.Legs[0].LinkID != linkAB.String() {
			t.Fatalf("patch de legs no aplicado: %+v", relegged.Legs)
		}
		// Patch ajeno → 403.
		if _, status := errorOf(t, norteMux, http.MethodPatch, "/logistics/routes/"+id, `{"active":true}`); status != http.StatusForbidden {
			t.Fatalf("patch ajeno: status %d, esperado 403", status)
		}
		// Patch vacío → 400.
		if _, status := errorOf(t, demoMux, http.MethodPatch, "/logistics/routes/"+id, `{}`); status != http.StatusBadRequest {
			t.Fatalf("patch vacío: status %d, esperado 400", status)
		}

		// Delete ajeno → 403; propio → 204; luego 404.
		if _, status := errorOf(t, norteMux, http.MethodDelete, "/logistics/routes/"+id, ""); status != http.StatusForbidden {
			t.Fatalf("delete ajeno: status %d, esperado 403", status)
		}
		if rec := do(t, demoMux, http.MethodDelete, "/logistics/routes/"+id, ""); rec.Code != http.StatusNoContent {
			t.Fatalf("delete propio: status %d, esperado 204 (body %s)", rec.Code, rec.Body.String())
		}
		if _, status := errorOf(t, demoMux, http.MethodGet, "/logistics/routes/"+id, ""); status != http.StatusNotFound {
			t.Fatalf("get tras delete: status %d, esperado 404", status)
		}
	})
}

// ─── DTOs del test ───────────────────────────────────────────────────────────

type nodeDTO struct {
	ID       string          `json:"id"`
	Kind     string          `json:"kind"`
	RegionID string          `json:"region_id"`
	Location json.RawMessage `json:"location"`
}

type linkSegDTO struct {
	ID            string  `json:"id"`
	RegionID      string  `json:"region_id"`
	CongestionEma float64 `json:"congestion_ema"`
}

type linkDTO struct {
	ID         string       `json:"id"`
	Mode       string       `json:"mode"`
	FromNodeID string       `json:"from_node_id"`
	ToNodeID   string       `json:"to_node_id"`
	LengthM    int          `json:"length_m"`
	Segments   []linkSegDTO `json:"segments"`
}

type planLegDTO struct {
	Seq                     int    `json:"seq"`
	LinkID                  string `json:"link_id"`
	Mode                    string `json:"mode"`
	EtaSimSeconds           int64  `json:"eta_sim_seconds"`
	TransshipmentTerminalID string `json:"transshipment_terminal_id"`
}

type planDTO struct {
	OriginNodeID       string       `json:"origin_node_id"`
	DestinationNodeID  string       `json:"destination_node_id"`
	Legs               []planLegDTO `json:"legs"`
	TotalEtaSimSeconds int64        `json:"total_eta_sim_seconds"`
	EstimatedCost      string       `json:"estimated_cost"`
}

type routeLegDTO struct {
	LegIndex int    `json:"leg_index"`
	LinkID   string `json:"link_id"`
}

type routeDTO struct {
	ID             string        `json:"id"`
	OwnerAccountID string        `json:"owner_account_id"`
	Name           string        `json:"name"`
	Kind           string        `json:"kind"`
	Active         bool          `json:"active"`
	Legs           []routeLegDTO `json:"legs"`
}

func containsRoute(routes []routeDTO, id string) bool {
	for _, r := range routes {
		if r.ID == id {
			return true
		}
	}
	return false
}

// ─── Helpers HTTP del test ───────────────────────────────────────────────────

type fakeMeta struct{}

func (fakeMeta) Meta(context.Context) httpx.Meta {
	return httpx.Meta{SimTime: simtime.Format(7200), SimTimeSeconds: 7200, ServerTime: time.Now().UTC()}
}

type fakeIdentity struct{ acc uuid.UUID }

func (i fakeIdentity) AccountID(context.Context) (uuid.UUID, bool) { return i.acc, true }

func newMux(svc *logistics.Service, acc uuid.UUID, logger *slog.Logger) *http.ServeMux {
	h := logistics.NewHandlers(svc, fakeIdentity{acc}, fakeMeta{}, logger)
	mux := http.NewServeMux()
	h.Register(mux)
	return mux
}

func do(t *testing.T, mux *http.ServeMux, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, r)
	return rec
}

func listOf[T any](t *testing.T, mux *http.ServeMux, target string) ([]T, string) {
	t.Helper()
	rec := do(t, mux, http.MethodGet, target, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d (body %s)", target, rec.Code, rec.Body.String())
	}
	var resp struct {
		Data []T `json:"data"`
		Meta struct {
			NextCursor string `json:"next_cursor"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("GET %s: json inválido: %v", target, err)
	}
	return resp.Data, resp.Meta.NextCursor
}

func objectOf[T any](t *testing.T, mux *http.ServeMux, target string) T {
	t.Helper()
	rec := do(t, mux, http.MethodGet, target, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d (body %s)", target, rec.Code, rec.Body.String())
	}
	var resp struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("GET %s: json inválido: %v", target, err)
	}
	return resp.Data
}

func planOf(t *testing.T, mux *http.ServeMux, body string) planDTO {
	t.Helper()
	rec := do(t, mux, http.MethodPost, "/logistics/route-plans", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("route-plan: status %d (body %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data planDTO `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("route-plan: json inválido: %v", err)
	}
	return resp.Data
}

func createRoute(t *testing.T, mux *http.ServeMux, body string) routeDTO {
	t.Helper()
	rec := do(t, mux, http.MethodPost, "/logistics/routes", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /logistics/routes: status %d, esperado 201 (body %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data routeDTO `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("POST /logistics/routes: json inválido: %v", err)
	}
	return resp.Data
}

func patchRoute(t *testing.T, mux *http.ServeMux, id, body string) routeDTO {
	t.Helper()
	rec := do(t, mux, http.MethodPatch, "/logistics/routes/"+id, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH /logistics/routes/%s: status %d (body %s)", id, rec.Code, rec.Body.String())
	}
	var resp struct {
		Data routeDTO `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("PATCH: json inválido: %v", err)
	}
	return resp.Data
}

// errorOf ejecuta una petición que se espera fallida y devuelve (code, status).
func errorOf(t *testing.T, mux *http.ServeMux, method, target, body string) (string, int) {
	t.Helper()
	rec := do(t, mux, method, target, body)
	var resp struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return resp.Error.Code, rec.Code
}

func assertGeo(t *testing.T, raw json.RawMessage, wantType string) {
	t.Helper()
	var geo struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	}
	if err := json.Unmarshal(raw, &geo); err != nil {
		t.Fatalf("geometría no es objeto GeoJSON: %v (%s)", err, raw)
	}
	if geo.Type != wantType {
		t.Fatalf("geometría type=%q, esperado %q (%s)", geo.Type, wantType, raw)
	}
}

// ─── Fixtures de grafo por SQL ───────────────────────────────────────────────

func insertNode(t *testing.T, ctx context.Context, pool *pgxpool.Pool, region uuid.UUID, kind string, x, y int) uuid.UUID {
	t.Helper()
	id := uuid.New()
	exec(t, ctx, pool,
		`INSERT INTO world.network_nodes (id, kind, region_id, location)
		 VALUES ($1, $2::world.node_kind, $3, ST_GeomFromText($4, 0))`,
		id, kind, region, fmt.Sprintf("POINT(%d %d)", x, y))
	return id
}

func insertLink(t *testing.T, ctx context.Context, pool *pgxpool.Pool, region uuid.UUID, mode string, from, to uuid.UUID, lengthM, capacity, speed int, p1, p2 string, congestion float64) uuid.UUID {
	t.Helper()
	id := uuid.New()
	path := fmt.Sprintf("LINESTRING(%s, %s)", p1, p2)
	exec(t, ctx, pool,
		`INSERT INTO world.network_links (id, mode, from_node_id, to_node_id, path, length_m, capacity_per_hour, base_speed_kmh)
		 VALUES ($1, $2::world.link_mode, $3, $4, ST_GeomFromText($5, 0), $6, $7, $8)`,
		id, mode, from, to, path, lengthM, capacity, speed)
	seg := uuid.New()
	exec(t, ctx, pool,
		`INSERT INTO world.link_segments (id, link_id, region_id, seq, portion, length_m, congestion_ema)
		 VALUES ($1, $2, $3, 0, ST_GeomFromText($4, 0), $5, $6)`,
		seg, id, region, path, lengthM, congestion)
	return id
}

func setCongestion(t *testing.T, ctx context.Context, pool *pgxpool.Pool, linkID uuid.UUID, ema float64) {
	t.Helper()
	exec(t, ctx, pool, `UPDATE world.link_segments SET congestion_ema = $1 WHERE link_id = $2`, ema, linkID)
}

func insertTerminal(t *testing.T, ctx context.Context, pool *pgxpool.Pool, node, owner uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	exec(t, ctx, pool,
		`INSERT INTO world.terminals (id, node_id, owner_account_id, transshipment_per_hour)
		 VALUES ($1, $2, $3, 100)`,
		id, node, owner)
	return id
}

// ─── Infraestructura del test (mismo patrón que los demás módulos) ───────────

func newEphemeralDB(t *testing.T, ctx context.Context, adminURL string) *pgxpool.Pool {
	t.Helper()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("conectando a %s: %v", adminURL, err)
	}
	dbName := fmt.Sprintf("logisticstest_%d_%d", os.Getpid(), time.Now().UnixNano())
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

func exec(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func regionID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM world.regions WHERE name = $1`, name).Scan(&id); err != nil {
		t.Fatalf("región %q: %v", name, err)
	}
	return id
}

func accountID(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM auth.accounts WHERE name = $1`, name).Scan(&id); err != nil {
		t.Fatalf("cuenta %q: %v", name, err)
	}
	return id
}
