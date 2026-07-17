package auth

// Soporte compartido de los tests unitarios del módulo: repositorio fake en
// memoria tras la interfaz Repository, reloj falso y MetaSource de prueba.

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/lokiteitor/global-market/backend/internal/platform/httpx"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// fakeClock es un reloj determinista para tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// fakeAccount agrupa una cuenta con su hash de credencial.
type fakeAccount struct {
	acc        Account
	secretHash string
}

// fakeRepo implementa Repository en memoria, concurrente-seguro.
type fakeRepo struct {
	mu       sync.Mutex
	accounts map[string]fakeAccount // clave: lower(name)
	sessions map[string]Session     // clave: token_hash
	touched  map[uuid.UUID]int      // veces que se reescribió last_seen_at
	now      func() time.Time
	// failWith fuerza un error en todas las operaciones (caminos 500).
	failWith error
}

func newFakeRepo(now func() time.Time) *fakeRepo {
	if now == nil {
		now = time.Now
	}
	return &fakeRepo{
		accounts: make(map[string]fakeAccount),
		sessions: make(map[string]Session),
		touched:  make(map[uuid.UUID]int),
		now:      now,
	}
}

// addAccount registra una cuenta con su secreto ya hasheado.
func (f *fakeRepo) addAccount(t *testing.T, acc Account, secret string) Account {
	t.Helper()
	if acc.ID == uuid.Nil {
		acc.ID = uuid.Must(uuid.NewV7())
	}
	if acc.CreatedAt.IsZero() {
		acc.CreatedAt = f.now()
	}
	hash, err := HashSecret(secret)
	if err != nil {
		t.Fatalf("HashSecret: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accounts[strings.ToLower(acc.Name)] = fakeAccount{acc: acc, secretHash: hash}
	return acc
}

// addSession registra una sesión para un token en claro y la devuelve.
func (f *fakeRepo) addSession(acc Account, token string, lastSeen, expires time.Time) Session {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := Session{
		ID:         uuid.Must(uuid.NewV7()),
		AccountID:  acc.ID,
		CreatedAt:  f.now(),
		LastSeenAt: lastSeen,
		ExpiresAt:  expires,
	}
	f.sessions[HashToken(token)] = s
	return s
}

func (f *fakeRepo) FindAccountByName(_ context.Context, name string) (Account, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return Account{}, "", f.failWith
	}
	fa, ok := f.accounts[strings.ToLower(name)]
	if !ok {
		return Account{}, "", ErrNotFound
	}
	return fa.acc, fa.secretHash, nil
}

func (f *fakeRepo) CreateSession(_ context.Context, accountID uuid.UUID, tokenHash string, _ map[string]any, expiresAt time.Time) (Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return Session{}, f.failWith
	}
	now := f.now()
	s := Session{
		ID:         uuid.Must(uuid.NewV7()),
		AccountID:  accountID,
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  expiresAt,
	}
	f.sessions[tokenHash] = s
	return s, nil
}

func (f *fakeRepo) FindSessionByTokenHash(_ context.Context, tokenHash string) (Session, Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return Session{}, Account{}, f.failWith
	}
	s, ok := f.sessions[tokenHash]
	if !ok || !s.ExpiresAt.After(f.now()) {
		return Session{}, Account{}, ErrNotFound
	}
	for _, fa := range f.accounts {
		if fa.acc.ID == s.AccountID {
			return s, fa.acc, nil
		}
	}
	return Session{}, Account{}, ErrNotFound
}

func (f *fakeRepo) TouchSessionLastSeen(_ context.Context, sessionID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return f.failWith
	}
	now := f.now()
	for hash, s := range f.sessions {
		if s.ID == sessionID && now.Sub(s.LastSeenAt) >= touchInterval {
			s.LastSeenAt = now
			f.sessions[hash] = s
			f.touched[sessionID]++
		}
	}
	return nil
}

func (f *fakeRepo) DeleteSession(_ context.Context, sessionID uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return f.failWith
	}
	for hash, s := range f.sessions {
		if s.ID == sessionID {
			delete(f.sessions, hash)
		}
	}
	return nil
}

// touchCount devuelve cuántas veces se reescribió last_seen_at de la sesión.
func (f *fakeRepo) touchCount(sessionID uuid.UUID) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.touched[sessionID]
}

// hasSession indica si la sesión sigue viva en el repositorio.
func (f *fakeRepo) hasSession(sessionID uuid.UUID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.sessions {
		if s.ID == sessionID {
			return true
		}
	}
	return false
}

// stubMeta es un MetaSource fijo de tests (génesis del mundo).
type stubMeta struct{}

func (stubMeta) Meta(context.Context) httpx.Meta {
	return httpx.Meta{
		SimTime:        simtime.Format(0),
		SimTimeSeconds: 0,
		ServerTime:     time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC),
	}
}

// testLogger descarta la salida (los tests no verifican logs).
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestService construye un Service sobre el fake con reloj determinista.
func newTestService(t *testing.T, repo *fakeRepo, clock *fakeClock) *Service {
	t.Helper()
	svc, err := NewService(repo, testLogger())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if clock != nil {
		svc.now = clock.now
	}
	return svc
}
