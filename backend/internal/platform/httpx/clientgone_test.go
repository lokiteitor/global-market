package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// canceledRequest devuelve una petición cuyo contexto YA terminó, que es el
// estado en el que llega un handler cuando el cliente cerró la conexión.
func canceledRequest(t *testing.T) *http.Request {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return httptest.NewRequest(http.MethodGet, "/api/v1/ledger/accounts", nil).WithContext(ctx)
}

// expiredRequest devuelve una petición cuyo plazo ya venció.
func expiredRequest(t *testing.T) *http.Request {
	t.Helper()
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	t.Cleanup(cancel)
	return httptest.NewRequest(http.MethodGet, "/api/v1/ledger/accounts", nil).WithContext(ctx)
}

// logLines decodifica las líneas JSON emitidas por el logger de prueba.
func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if raw == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatalf("línea de log no es JSON (%q): %v", raw, err)
		}
		out = append(out, m)
	}
	return out
}

// TestWriteClientGoneCancelado cubre el caso reproducido en la corrida de
// stress: el harness cierra sus clientes, la consulta en vuelo devuelve
// context.Canceled y eso NO puede salir como 500 ni como ERROR.
func TestWriteClientGoneCancelado(t *testing.T) {
	var buf bytes.Buffer
	rr := httptest.NewRecorder()
	err := fmt.Errorf("ledger: listando cuentas: %w", context.Canceled)

	if !WriteClientGone(rr, canceledRequest(t), testLogger(&buf), err, "listando cuentas del ledger") {
		t.Fatal("WriteClientGone devolvió false para una petición abortada por el cliente")
	}
	if rr.Code != StatusClientClosedRequest {
		t.Fatalf("status = %d, esperado %d", rr.Code, StatusClientClosedRequest)
	}
	if rr.Code >= 500 {
		t.Fatalf("status %d cae en la familia 5xx: contaminaría los contadores del gateway", rr.Code)
	}
	if body := rr.Body.String(); body != "" {
		t.Fatalf("no debe escribirse cuerpo de error interno, se escribió %q", body)
	}

	lines := logLines(t, &buf)
	if len(lines) != 1 {
		t.Fatalf("líneas de log = %d, esperada 1", len(lines))
	}
	if lvl := lines[0]["level"]; lvl != "INFO" {
		t.Fatalf("nivel de log = %v, esperado INFO (no es un fallo del servidor)", lvl)
	}
}

// TestWriteClientGonePlazoAgotado comprueba que el plazo vencido —que sí es un
// problema del servicio— se nombra como 504 y se registra como WARN, no como un
// INTERNAL genérico.
func TestWriteClientGonePlazoAgotado(t *testing.T) {
	var buf bytes.Buffer
	rr := httptest.NewRecorder()
	err := fmt.Errorf("ledger: listando cuentas: %w", context.DeadlineExceeded)

	if !WriteClientGone(rr, expiredRequest(t), testLogger(&buf), err, "listando cuentas del ledger") {
		t.Fatal("WriteClientGone devolvió false para un plazo agotado")
	}
	if rr.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, esperado %d", rr.Code, http.StatusGatewayTimeout)
	}
	lines := logLines(t, &buf)
	if len(lines) != 1 || lines[0]["level"] != "WARN" {
		t.Fatalf("esperada 1 línea WARN, se obtuvo %v", lines)
	}
}

// TestWriteClientGoneNoTapaBugsInternos es la guarda que impide que este
// helper se coma un fallo de verdad: un context.Canceled nacido de un contexto
// INTERNO del servidor llega con la petición todavía viva y debe seguir su
// camino al 500 con su línea de ERROR.
func TestWriteClientGoneNoTapaBugsInternos(t *testing.T) {
	var buf bytes.Buffer
	rr := httptest.NewRecorder()
	vivo := httptest.NewRequest(http.MethodGet, "/api/v1/ledger/accounts", nil)

	if WriteClientGone(rr, vivo, testLogger(&buf), context.Canceled, "listando cuentas del ledger") {
		t.Fatal("un context.Canceled con la petición viva se tomó por desconexión del cliente")
	}
	if rr.Code != http.StatusOK || rr.Body.Len() != 0 {
		t.Fatal("WriteClientGone escribió en la respuesta pese a devolver false")
	}
	if len(logLines(t, &buf)) != 0 {
		t.Fatal("WriteClientGone logueó pese a devolver false")
	}
}

// TestWriteClientGoneOtrosErrores comprueba que cualquier otro error sigue
// intacto hacia el 500 aunque la petición se haya cancelado: el fallo real no
// se pierde solo porque el cliente además se fuera.
func TestWriteClientGoneOtrosErrores(t *testing.T) {
	var buf bytes.Buffer
	rr := httptest.NewRecorder()
	err := errors.New("ledger: la consulta reventó")

	if WriteClientGone(rr, canceledRequest(t), testLogger(&buf), err, "listando cuentas del ledger") {
		t.Fatal("un error real se tomó por desconexión del cliente")
	}
}

// TestLogLevelFor cubre la elección de nivel en los sitios que ya no pueden
// responder (best-effort tras ejecutar la operación).
func TestLogLevelFor(t *testing.T) {
	if got := LogLevelFor(canceledRequest(t), context.Canceled); got.String() != "WARN" {
		t.Fatalf("nivel para cliente ido = %s, esperado WARN", got)
	}
	if got := LogLevelFor(canceledRequest(t), errors.New("fallo real")); got.String() != "ERROR" {
		t.Fatalf("nivel para fallo real = %s, esperado ERROR", got)
	}
	if got := LogLevelFor(httptest.NewRequest(http.MethodGet, "/x", nil), context.Canceled); got.String() != "ERROR" {
		t.Fatalf("nivel para cancelación interna = %s, esperado ERROR", got)
	}
}
