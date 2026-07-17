package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type cuerpoPrueba struct {
	Name   string `json:"name"`
	Amount string `json:"amount"`
}

func postJSON(body string) *http.Request {
	return httptest.NewRequest(http.MethodPost, "/recurso", strings.NewReader(body))
}

func TestReadJSONOk(t *testing.T) {
	rr := httptest.NewRecorder()
	var dst cuerpoPrueba
	err := ReadJSON(rr, postJSON(`{"name":"acme","amount":"1000"}`), &dst, 0)
	if err != nil {
		t.Fatalf("ReadJSON: %v", err)
	}
	if dst.Name != "acme" || dst.Amount != "1000" {
		t.Fatalf("dst = %+v", dst)
	}
	if rr.Body.Len() != 0 {
		t.Fatalf("no debe escribirse respuesta en el caso feliz: %q", rr.Body.String())
	}
}

// decodeError ejecuta ReadJSON esperando fallo y devuelve el envelope escrito.
func decodeError(t *testing.T, body string, maxBytes int64) (int, map[string]any) {
	t.Helper()
	rr := httptest.NewRecorder()
	var dst cuerpoPrueba
	if err := ReadJSON(rr, postJSON(body), &dst, maxBytes); err == nil {
		t.Fatal("ReadJSON sin error, quiero error")
	}
	var envelope struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("la respuesta de error no es JSON: %v (%q)", err, rr.Body.String())
	}
	return rr.Code, envelope.Error
}

func TestReadJSONErrores(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		maxBytes int64
	}{
		{"cuerpo vacío", "", 0},
		{"JSON malformado", `{"name":`, 0},
		{"tipo inválido", `{"name":123}`, 0},
		{"datos tras el valor JSON", `{"name":"a"}{"name":"b"}`, 0},
		{"cuerpo demasiado grande", `{"name":"` + strings.Repeat("x", 100) + `"}`, 16},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, errBody := decodeError(t, tc.body, tc.maxBytes)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, quiero 400", status)
			}
			if errBody["code"] != CodeValidationError {
				t.Fatalf("code = %v, quiero %q", errBody["code"], CodeValidationError)
			}
			if msg, _ := errBody["message"].(string); msg == "" {
				t.Fatal("message vacío en el envelope de error")
			}
		})
	}
}
