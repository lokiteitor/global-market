package botsdk

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBoardPaginacionSigueCursores(t *testing.T) {
	var cursors []string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/contracts/board", func(w http.ResponseWriter, r *http.Request) {
		cursors = append(cursors, r.URL.Query().Get("cursor"))
		if got := r.URL.Query().Get("kind"); got != "sell" {
			t.Errorf("kind = %q, esperaba sell", got)
		}
		if got := r.URL.Query().Get("limit"); got != "2" {
			t.Errorf("limit = %q, esperaba 2", got)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		pub := func(id string) string {
			return `{"id":"` + id + `","kind":"sell","publisher_account_id":"a-1","channel":"board","product_id":"p-1","quantity_total":"100","quantity_remaining":"100","unit_price":"120","min_lot":"10","origin_node_id":"n-1","delivery_sim_seconds":172800,"status":"open","published_at_sim":31104000}`
		}
		switch r.URL.Query().Get("cursor") {
		case "":
			fmt.Fprint(w, `{"data":[`+pub("pub-1")+`,`+pub("pub-2")+`],"meta":{"sim_time":"360-045-12:30","sim_time_seconds":31104000,"server_time":"2026-07-15T10:00:00Z","next_cursor":"cur-2"}}`)
		case "cur-2":
			fmt.Fprint(w, `{"data":[`+pub("pub-3")+`],"meta":`+metaJSON+`}`)
		default:
			t.Errorf("cursor inesperado %q", r.URL.Query().Get("cursor"))
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, _ := newTestClient(t, srv)

	pubs, err := CollectAll(context.Background(), func(ctx context.Context, cursor string) (Page[Publication], error) {
		return c.Board(ctx, BoardQuery{
			Kind:      PublicationSell,
			PageQuery: PageQuery{Cursor: cursor, Limit: 2},
		})
	})
	if err != nil {
		t.Fatalf("CollectAll: %v", err)
	}
	if len(pubs) != 3 || pubs[0].ID != "pub-1" || pubs[2].ID != "pub-3" {
		t.Errorf("pubs = %+v", pubs)
	}
	if len(cursors) != 2 || cursors[0] != "" || cursors[1] != "cur-2" {
		t.Errorf("cursors vistos por el servidor = %v", cursors)
	}
}

func TestAcceptEnviaOriginNodeIDSoloSiSeDa(t *testing.T) {
	var bodies []map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/contracts/publications/pub-1/acceptances", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"data":{"id":"acc-1","publication_id":"pub-1","acceptor_account_id":"a-2","quantity":"50","quantity_served":"0","status":"pending_draw","accepted_at":"2026-07-15T10:00:05Z"},"meta":`+metaJSON+`}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	c, _ := newTestClient(t, srv)

	// Aceptación de una publicación buy: el vendedor aporta su almacén.
	acc, err := c.Accept(context.Background(), "pub-1", "50", "n-origen")
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if acc.Status != AcceptancePendingDraw {
		t.Errorf("status = %q", acc.Status)
	}
	// Aceptación de una publicación sell: entrega in situ, sin origen propio.
	if _, err := c.Accept(context.Background(), "pub-1", "50", ""); err != nil {
		t.Fatalf("Accept sin origen: %v", err)
	}

	if got := bodies[0]["origin_node_id"]; got != "n-origen" {
		t.Errorf("origin_node_id = %v, esperaba n-origen", got)
	}
	if bodies[0]["quantity"] != "50" {
		t.Errorf("quantity = %v", bodies[0]["quantity"])
	}
	if _, present := bodies[1]["origin_node_id"]; present {
		t.Errorf("origin_node_id no debe enviarse vacío: %v", bodies[1])
	}
}

func TestUpdatesTriEstadoMarshalJSON(t *testing.T) {
	receta := "rec-1"
	cierto := true
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"fijar receta", BuildingUpdate{ActiveRecipeID: &receta}, `{"active_recipe_id":"rec-1"}`},
		{"detener línea", BuildingUpdate{ClearActiveRecipe: true}, `{"active_recipe_id":null}`},
		{"solo mantenimiento", BuildingUpdate{StartMaintenance: &cierto}, `{"start_maintenance":true}`},
		{"asignar ruta", VehicleUpdate{RouteID: &receta}, `{"route_id":"rec-1"}`},
		{"retirar ruta", VehicleUpdate{ClearRoute: true}, `{"route_id":null}`},
	}
	for _, tc := range cases {
		got, err := json.Marshal(tc.in)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if string(got) != tc.want {
			t.Errorf("%s: json = %s, esperaba %s", tc.name, got, tc.want)
		}
	}
}
