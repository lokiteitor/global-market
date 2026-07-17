package idempotency

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// storedResponse es la respuesta persistida del primer intento de un comando.
type storedResponse struct {
	Status      int
	ContentType string
	Body        []byte
}

// store es el contrato de persistencia del middleware; pgStore es la
// implementación real (public.idempotency_keys) y los tests unitarios usan
// un fake.
type store interface {
	// find devuelve la respuesta almacenada de (key, account), si existe.
	find(ctx context.Context, key, account uuid.UUID) (storedResponse, bool, error)
	// save persiste la respuesta del primer intento. Devuelve false si otra
	// petición concurrente ganó la carrera (ON CONFLICT DO NOTHING).
	save(ctx context.Context, key, account uuid.UUID, method, path string, resp storedResponse) (bool, error)
}

// querier es el subconjunto de pgx que usa el almacén; lo satisfacen
// *pgxpool.Pool, *pgx.Conn y pgx.Tx.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// pgStore es el almacén real sobre public.idempotency_keys (migración 0008).
type pgStore struct {
	db querier
}

const (
	findSQL = `SELECT response_status, content_type, response_body
                 FROM public.idempotency_keys
                WHERE key = $1 AND account_id = $2`
	insertSQL = `INSERT INTO public.idempotency_keys
                     (key, account_id, method, path, response_status, content_type, response_body)
              VALUES ($1, $2, $3, $4, $5, $6, $7)
         ON CONFLICT (key, account_id) DO NOTHING`
)

func (s *pgStore) find(ctx context.Context, key, account uuid.UUID) (storedResponse, bool, error) {
	var resp storedResponse
	err := s.db.QueryRow(ctx, findSQL, key, account).
		Scan(&resp.Status, &resp.ContentType, &resp.Body)
	if errors.Is(err, pgx.ErrNoRows) {
		return storedResponse{}, false, nil
	}
	if err != nil {
		return storedResponse{}, false, fmt.Errorf("idempotency: buscando la clave: %w", err)
	}
	return resp, true, nil
}

func (s *pgStore) save(ctx context.Context, key, account uuid.UUID, method, path string, resp storedResponse) (bool, error) {
	tag, err := s.db.Exec(ctx, insertSQL,
		key, account, method, path, resp.Status, resp.ContentType, resp.Body)
	if err != nil {
		return false, fmt.Errorf("idempotency: persistiendo la clave: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// ─── Captura de la respuesta ────────────────────────────────────────────────

// recorder es un http.ResponseWriter que retiene la respuesta completa en
// memoria: nada llega al cliente hasta decidir si se persiste, se entrega tal
// cual o se sustituye por la almacenada (carrera perdida). Las respuestas de
// los comandos del contrato son JSON pequeños: el buffering es inocuo.
type recorder struct {
	header      http.Header
	body        bytes.Buffer
	code        int
	wroteHeader bool
}

func newRecorder() *recorder {
	return &recorder{header: make(http.Header)}
}

func (r *recorder) Header() http.Header { return r.header }

func (r *recorder) WriteHeader(status int) {
	if !r.wroteHeader {
		r.code = status
		r.wroteHeader = true
	}
}

func (r *recorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	return r.body.Write(b)
}

// status devuelve el código efectivo (200 si el handler nunca lo fijó,
// misma convención que net/http).
func (r *recorder) status() int {
	if !r.wroteHeader {
		return http.StatusOK
	}
	return r.code
}

// stored proyecta la captura al formato persistible.
func (r *recorder) stored() storedResponse {
	return storedResponse{
		Status:      r.status(),
		ContentType: r.header.Get("Content-Type"),
		Body:        r.body.Bytes(),
	}
}

// flush vuelca la respuesta capturada al writer real: cabeceras del handler
// (sin pisar las ya fijadas por middlewares externos, p. ej. X-Request-Id),
// status y cuerpo.
func (r *recorder) flush(w http.ResponseWriter) {
	h := w.Header()
	for k, vv := range r.header {
		h[k] = vv
	}
	w.WriteHeader(r.status())
	_, _ = w.Write(r.body.Bytes())
}
