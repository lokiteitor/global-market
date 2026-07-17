package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// SessionTTL es la vida de una sesión desde su creación. Las sesiones son la
// única capa del sistema donde el wall-clock es regla legítima (GDD 1.1).
const SessionTTL = 24 * time.Hour

// accountStatusActive es el único estado de cuenta que puede iniciar sesión.
const accountStatusActive = "active"

// ErrUnauthorized es el fallo genérico de autenticación: cuenta inexistente,
// secreto inválido, cuenta no activa o sesión ausente/expirada. Nunca se
// distingue la causa hacia el cliente (401 UNAUTHORIZED del contrato).
var ErrUnauthorized = errors.New("auth: credenciales o sesión inválidas")

// SessionCreated es el resultado de un Login: la única vez que el token
// viaja en claro (el servidor solo persiste su hash).
type SessionCreated struct {
	Session Session
	Token   string
	Account Account
}

// Service implementa los casos de uso de autenticación sobre un Repository.
type Service struct {
	repo   Repository
	logger *slog.Logger
	// now es inyectable en tests; time.Now en producción.
	now func() time.Time
	// dummyHash es un hash argon2id de un secreto aleatorio, usado para
	// igualar el coste de Login cuando la cuenta no existe (timing parejo).
	dummyHash string
}

// NewService construye el servicio. Deriva en el arranque el hash dummy con
// los mismos parámetros argon2id que las credenciales reales.
func NewService(repo Repository, logger *slog.Logger) (*Service, error) {
	dummySecret, err := NewToken()
	if err != nil {
		return nil, err
	}
	dummyHash, err := HashSecret(dummySecret)
	if err != nil {
		return nil, err
	}
	return &Service{
		repo:      repo,
		logger:    logger,
		now:       time.Now,
		dummyHash: dummyHash,
	}, nil
}

// Login autentica una cuenta por nombre+secreto y crea una sesión de 24h
// wall-clock. Ante cuenta inexistente o secreto inválido devuelve el mismo
// ErrUnauthorized, con coste temporal parejo: si la cuenta no existe se
// verifica el secreto contra un hash dummy antes de rechazar.
func (s *Service) Login(ctx context.Context, name, secret string, clientInfo map[string]any) (SessionCreated, error) {
	acc, secretHash, err := s.repo.FindAccountByName(ctx, name)
	switch {
	case errors.Is(err, ErrNotFound):
		// Timing parejo: mismo trabajo argon2id que una cuenta real.
		if _, vErr := VerifySecret(secret, s.dummyHash); vErr != nil {
			return SessionCreated{}, fmt.Errorf("auth: verificando hash dummy: %w", vErr)
		}
		return SessionCreated{}, ErrUnauthorized
	case err != nil:
		return SessionCreated{}, err
	}

	ok, err := VerifySecret(secret, secretHash)
	if err != nil {
		// Hash corrupto en BD: error interno, nunca un 401 silencioso.
		return SessionCreated{}, fmt.Errorf("auth: credencial ilegible de la cuenta %s: %w", acc.ID, err)
	}
	if !ok {
		return SessionCreated{}, ErrUnauthorized
	}
	if acc.Status != accountStatusActive {
		// Cuenta suspendida/retirada: mismo 401 genérico (no filtrar estado).
		return SessionCreated{}, ErrUnauthorized
	}

	token, err := NewToken()
	if err != nil {
		return SessionCreated{}, err
	}
	sess, err := s.repo.CreateSession(ctx, acc.ID, HashToken(token), clientInfo, s.now().Add(SessionTTL))
	if err != nil {
		return SessionCreated{}, err
	}
	s.logger.LogAttrs(ctx, slog.LevelInfo, "sesión creada",
		slog.String("account_id", acc.ID.String()),
		slog.String("session_id", sess.ID.String()),
		slog.String("account_kind", acc.Kind),
	)
	return SessionCreated{Session: sess, Token: token, Account: acc}, nil
}

// Authenticate resuelve el token bearer en su sesión vigente y cuenta.
// Cualquier token desconocido o expirado es ErrUnauthorized.
func (s *Service) Authenticate(ctx context.Context, token string) (Session, Account, error) {
	if token == "" {
		return Session{}, Account{}, ErrUnauthorized
	}
	sess, acc, err := s.repo.FindSessionByTokenHash(ctx, HashToken(token))
	if errors.Is(err, ErrNotFound) {
		return Session{}, Account{}, ErrUnauthorized
	}
	if err != nil {
		return Session{}, Account{}, err
	}
	return sess, acc, nil
}

// Logout invalida una sesión. Idempotente.
func (s *Service) Logout(ctx context.Context, sessionID uuid.UUID) error {
	if err := s.repo.DeleteSession(ctx, sessionID); err != nil {
		return err
	}
	s.logger.LogAttrs(ctx, slog.LevelInfo, "sesión cerrada",
		slog.String("session_id", sessionID.String()))
	return nil
}

// Me devuelve la cuenta autenticada del contexto (inyectada por RequireAuth).
func (s *Service) Me(ctx context.Context) (Account, error) {
	acc, ok := FromContext(ctx)
	if !ok {
		return Account{}, ErrUnauthorized
	}
	return acc, nil
}
