package contracts_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/lokiteitor/global-market/backend/internal/contracts"
	"github.com/lokiteitor/global-market/backend/internal/platform/httpx"
)

// fakeAPI implementa contracts.API con respuestas programables (sin BD): prueba
// el ruteo, el mapeo de errores tipados a códigos del contrato y la
// serialización (dinero/stock como string, sim-time int64).
type fakeAPI struct {
	board       []contracts.Publication
	boardCursor string
	boardErr    error

	pub        contracts.Publication
	pubErr     error
	createErr  error
	cancelErr  error
	acc        contracts.Acceptance
	acceptErr  error
	getAccErr  error
	contractID *uuid.UUID

	contracts    []contracts.Contract
	contractsErr error
	contract     contracts.Contract
	contractErr  error
	deliveries   []contracts.ContractDelivery
	deliveriesEr error
}

func (f *fakeAPI) QueryBoard(context.Context, contracts.BoardFilter) ([]contracts.Publication, string, error) {
	return f.board, f.boardCursor, f.boardErr
}
func (f *fakeAPI) CreatePublication(context.Context, uuid.UUID, contracts.PublicationInput) (contracts.Publication, error) {
	return f.pub, f.createErr
}
func (f *fakeAPI) GetPublication(context.Context, uuid.UUID, uuid.UUID) (contracts.Publication, error) {
	return f.pub, f.pubErr
}
func (f *fakeAPI) CancelPublication(context.Context, uuid.UUID, uuid.UUID) (contracts.Publication, error) {
	return f.pub, f.cancelErr
}
func (f *fakeAPI) Accept(context.Context, uuid.UUID, uuid.UUID, contracts.AcceptInput) (contracts.Acceptance, error) {
	return f.acc, f.acceptErr
}
func (f *fakeAPI) GetAcceptance(context.Context, uuid.UUID, uuid.UUID) (contracts.Acceptance, error) {
	return f.acc, f.getAccErr
}
func (f *fakeAPI) ResolveAcceptanceContract(context.Context, contracts.Acceptance) (*uuid.UUID, error) {
	return f.contractID, nil
}
func (f *fakeAPI) ListContracts(context.Context, uuid.UUID, contracts.ContractFilter) ([]contracts.Contract, string, error) {
	return f.contracts, "", f.contractsErr
}
func (f *fakeAPI) GetContract(context.Context, uuid.UUID, uuid.UUID) (contracts.Contract, error) {
	return f.contract, f.contractErr
}
func (f *fakeAPI) ListContractDeliveries(context.Context, uuid.UUID, uuid.UUID) ([]contracts.ContractDelivery, error) {
	return f.deliveries, f.deliveriesEr
}

// fakeIdentity/fakeMeta implementan las interfaces del composition root.
type fakeIdentity struct {
	account uuid.UUID
	ok      bool
}

func (f fakeIdentity) AccountID(context.Context) (uuid.UUID, bool) { return f.account, f.ok }

type fakeMeta struct{}

func (fakeMeta) Meta(context.Context) httpx.Meta {
	return httpx.Meta{SimTime: "1-001-00:00", SimTimeSeconds: 42, ServerTime: time.Unix(0, 0).UTC()}
}

func newTestServer(api contracts.API, authed bool) http.Handler {
	id := fakeIdentity{account: uuid.New(), ok: authed}
	h := contracts.NewHandlers(api, id, fakeMeta{}, nil)
	mux := http.NewServeMux()
	h.Register(mux)
	return mux
}

func do(t *testing.T, handler http.Handler, method, target, body string) (int, map[string]any) {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, r)
	var decoded map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("respuesta no es JSON (%d): %s", rec.Code, rec.Body.String())
		}
	}
	return rec.Code, decoded
}

func TestHandlersUnauthorized(t *testing.T) {
	srv := newTestServer(&fakeAPI{}, false)
	for _, tc := range []struct{ method, path string }{
		{"GET", "/contracts/board"},
		{"POST", "/contracts/publications"},
		{"GET", "/contracts/contracts"},
		{"GET", "/contracts/freight-contracts"},
	} {
		code, body := do(t, srv, tc.method, tc.path, "{}")
		if code != http.StatusUnauthorized {
			t.Fatalf("%s %s: %d, esperado 401", tc.method, tc.path, code)
		}
		if errObj, _ := body["error"].(map[string]any); errObj["code"] != "UNAUTHORIZED" {
			t.Fatalf("%s %s: code %v, esperado UNAUTHORIZED", tc.method, tc.path, errObj["code"])
		}
	}
}

func TestHandlersFreightPhase2(t *testing.T) {
	srv := newTestServer(&fakeAPI{}, true)

	// Lista vacía paginada.
	code, body := do(t, srv, "GET", "/contracts/freight-contracts", "")
	if code != http.StatusOK {
		t.Fatalf("freight list: %d, esperado 200", code)
	}
	if data, ok := body["data"].([]any); !ok || len(data) != 0 {
		t.Fatalf("freight list: data %v, esperado []", body["data"])
	}
	if _, ok := body["meta"]; !ok {
		t.Fatal("freight list: falta meta")
	}

	// Detalle → 404.
	code, _ = do(t, srv, "GET", "/contracts/freight-contracts/"+uuid.New().String(), "")
	if code != http.StatusNotFound {
		t.Fatalf("freight detail: %d, esperado 404", code)
	}
}

func TestHandlersCreatePublicationSerialization(t *testing.T) {
	product := uuid.New()
	origin := uuid.New()
	api := &fakeAPI{pub: contracts.Publication{
		ID:                 uuid.New(),
		Kind:               contracts.KindSell,
		PublisherAccountID: uuid.New(),
		Channel:            contracts.ChannelBoard,
		ProductID:          &product,
		QuantityTotal:      500,
		QuantityRemaining:  500,
		UnitPrice:          120,
		MinLot:             50,
		OriginNodeID:       &origin,
		DeliverySimSeconds: 172800,
		Status:             contracts.StatusDrawWindow,
		PublishedAtSim:     1000,
	}}
	srv := newTestServer(api, true)

	body := `{"kind":"sell","product_id":"` + product.String() + `","quantity_total":"500","unit_price":"120","min_lot":"50","origin_node_id":"` + origin.String() + `","delivery_sim_seconds":172800}`
	code, resp := do(t, srv, "POST", "/contracts/publications", body)
	if code != http.StatusCreated {
		t.Fatalf("create: %d, esperado 201 (%v)", code, resp)
	}
	data := resp["data"].(map[string]any)
	// Dinero/stock como string; sim-time como número.
	if data["quantity_total"] != "500" || data["unit_price"] != "120" || data["min_lot"] != "50" {
		t.Fatalf("importes no serializados como string: %v", data)
	}
	if data["delivery_sim_seconds"] != float64(172800) || data["published_at_sim"] != float64(1000) {
		t.Fatalf("sim-time no serializado como entero: %v", data)
	}
}

func TestHandlersErrorMapping(t *testing.T) {
	cases := []struct {
		name     string
		api      *fakeAPI
		method   string
		path     string
		body     string
		wantCode int
		wantErr  string
	}{
		{
			name:   "colateral insuficiente → 422",
			api:    &fakeAPI{createErr: &contracts.CollateralError{Resource: "cash", Required: 1000, Available: 740}},
			method: "POST", path: "/contracts/publications",
			body:     `{"kind":"buy","product_id":"` + uuid.New().String() + `","quantity_total":"10","unit_price":"100","destination_node_id":"` + uuid.New().String() + `","delivery_sim_seconds":3600}`,
			wantCode: http.StatusUnprocessableEntity, wantErr: "INSUFFICIENT_COLLATERAL",
		},
		{
			name:   "cooldown → 409",
			api:    &fakeAPI{cancelErr: &contracts.CooldownError{Until: time.Unix(100, 0).UTC()}},
			method: "DELETE", path: "/contracts/publications/" + uuid.New().String(),
			wantCode: http.StatusConflict, wantErr: "CANCEL_COOLDOWN_ACTIVE",
		},
		{
			name:   "min_lot → 422",
			api:    &fakeAPI{acceptErr: &contracts.MinLotError{MinLot: 30, QuantityRemaining: 100}},
			method: "POST", path: "/contracts/publications/" + uuid.New().String() + "/acceptances",
			body:     `{"quantity":"10"}`,
			wantCode: http.StatusUnprocessableEntity, wantErr: "BELOW_MIN_LOT",
		},
		{
			name:   "publicación agotada → 409",
			api:    &fakeAPI{acceptErr: contracts.ErrPublicationExhausted},
			method: "POST", path: "/contracts/publications/" + uuid.New().String() + "/acceptances",
			body:     `{"quantity":"10"}`,
			wantCode: http.StatusConflict, wantErr: "PUBLICATION_EXHAUSTED",
		},
		{
			name:   "no encontrada → 404",
			api:    &fakeAPI{pubErr: contracts.ErrPublicationNotFound},
			method: "GET", path: "/contracts/publications/" + uuid.New().String(),
			wantCode: http.StatusNotFound, wantErr: "NOT_FOUND",
		},
		{
			name:   "contrato ajeno → 403",
			api:    &fakeAPI{contractErr: contracts.ErrNotContractParty},
			method: "GET", path: "/contracts/contracts/" + uuid.New().String(),
			wantCode: http.StatusForbidden, wantErr: "NOT_RESOURCE_OWNER",
		},
		{
			name:   "freight al publicar → 422",
			api:    &fakeAPI{createErr: contracts.ErrFreightPhase2},
			method: "POST", path: "/contracts/publications",
			body:     `{"kind":"freight","quantity_total":"1","unit_price":"1","delivery_sim_seconds":1}`,
			wantCode: http.StatusUnprocessableEntity, wantErr: "VALIDATION_ERROR",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(tc.api, true)
			code, body := do(t, srv, tc.method, tc.path, tc.body)
			if code != tc.wantCode {
				t.Fatalf("%s: %d, esperado %d (%v)", tc.name, code, tc.wantCode, body)
			}
			errObj, _ := body["error"].(map[string]any)
			if errObj["code"] != tc.wantErr {
				t.Fatalf("%s: code %v, esperado %s", tc.name, errObj["code"], tc.wantErr)
			}
		})
	}
}

func TestHandlersAcceptanceExposesContractID(t *testing.T) {
	contractID := uuid.New()
	api := &fakeAPI{
		acc: contracts.Acceptance{
			ID:                uuid.New(),
			PublicationID:     uuid.New(),
			AcceptorAccountID: uuid.New(),
			Quantity:          50,
			QuantityServed:    50,
			Status:            contracts.AcceptanceServed,
		},
		contractID: &contractID,
	}
	srv := newTestServer(api, true)
	code, resp := do(t, srv, "GET", "/contracts/acceptances/"+uuid.New().String(), "")
	if code != http.StatusOK {
		t.Fatalf("get acceptance: %d, esperado 200", code)
	}
	data := resp["data"].(map[string]any)
	if data["contract_id"] != contractID.String() {
		t.Fatalf("contract_id no expuesto: %v", data["contract_id"])
	}
	if data["quantity_served"] != "50" || data["status"] != "served" {
		t.Fatalf("aceptación mal serializada: %v", data)
	}
}
