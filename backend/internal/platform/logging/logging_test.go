package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/lokiteitor/global-market/backend/internal/platform/config"
)

func testConfig(level string) config.Config {
	return config.Config{LogLevel: level}
}

func TestNewWithWriterEmitsJSONWithService(t *testing.T) {
	var buf bytes.Buffer
	logger := NewWithWriter(&buf, testConfig("info"), "gateway")
	logger.Info("hola", slog.String("k", "v"))

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("la salida no es JSON: %v (%q)", err, buf.String())
	}
	if line["msg"] != "hola" || line[AttrService] != "gateway" || line["k"] != "v" {
		t.Fatalf("línea inesperada: %v", line)
	}
}

func TestNewWithWriterRespectsLevel(t *testing.T) {
	var buf bytes.Buffer
	logger := NewWithWriter(&buf, testConfig("warn"), "engine")
	logger.Info("filtrado")
	if buf.Len() != 0 {
		t.Fatalf("un info con nivel warn no debe emitirse: %q", buf.String())
	}
	logger.Warn("visible")
	if buf.Len() == 0 {
		t.Fatal("un warn con nivel warn debe emitirse")
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug":       slog.LevelDebug,
		"info":        slog.LevelInfo,
		"warn":        slog.LevelWarn,
		"error":       slog.LevelError,
		"desconocido": slog.LevelInfo,
		"":            slog.LevelInfo,
	}
	for in, want := range cases {
		if got := ParseLevel(in); got != want {
			t.Fatalf("ParseLevel(%q) = %v, quiero %v", in, got, want)
		}
	}
}

func TestWithRequestID(t *testing.T) {
	var buf bytes.Buffer
	logger := NewWithWriter(&buf, testConfig("info"), "gateway")

	WithRequestID(logger, "req-123").Info("con id")
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("salida no JSON: %v", err)
	}
	if line[AttrRequestID] != "req-123" {
		t.Fatalf("request_id = %v, quiero %q", line[AttrRequestID], "req-123")
	}

	// Con id vacío devuelve el mismo logger sin atributo.
	if got := WithRequestID(logger, ""); got != logger {
		t.Fatal("WithRequestID(logger, \"\") debe devolver el logger original")
	}
}
