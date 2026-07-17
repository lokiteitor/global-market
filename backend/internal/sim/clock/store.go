// Package clock implementa el reloj de simulación del backend (GDD 1.1,
// ADR-002): un único reloj lógico cuyo estado persistido es un ancla en
// world.sim_clock (fila única id = 1). El sim-time actual nunca se almacena
// tick a tick: se deriva analíticamente con internal/sim/simtime.Derive a
// partir del ancla (sim_time_at, wall_anchor, ratio, frozen).
//
// El paquete ofrece dos consumidores del ancla:
//
//   - Clock: el reloj del motor (cmd/engine). Cachea el ancla en memoria,
//     la re-persiste periódicamente y la refresca por si otro proceso la
//     cambió (p. ej. frozen durante la ventana de mantenimiento, ADR-003).
//   - Reader: lector ligero para otros procesos (p. ej. el gateway), con
//     caché por TTL y derivación local; nunca rompe una respuesta por un
//     fallo transitorio de la BD.
package clock

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// Anchor es el contenido de la fila única de world.sim_clock: el ancla desde
// la que se deriva el sim-time actual (migración 0003).
type Anchor struct {
	// SimTimeAt es el sim-time en el instante del anclaje.
	SimTimeAt simtime.SimTime
	// WallAnchor es el wall-clock del anclaje.
	WallAnchor time.Time
	// Ratio es la relación sim-time/wall-clock (24 por defecto, GDD 1.1).
	Ratio int64
	// Frozen indica que el mundo está congelado y el tiempo no avanza.
	Frozen bool
	// UpdatedAt es la última escritura de la fila.
	UpdatedAt time.Time
}

// Store es el acceso pgx a world.sim_clock (fila única id = 1).
type Store struct {
	pool *pgxpool.Pool
}

// La implementación pgx satisface los contratos de Clock y Reader.
var _ AnchorStore = (*Store)(nil)

// NewStore construye el acceso al ancla sobre un pool existente.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// EnsureExists garantiza la fila única del reloj: la crea en génesis
// (sim_time_at = 0, wall_anchor = now(), ratio = 24) si no existe y no toca
// nada si ya existe. Idempotente.
func (s *Store) EnsureExists(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO world.sim_clock (id, sim_time_at, wall_anchor, ratio)
		VALUES (1, 0, now(), $1)
		ON CONFLICT (id) DO NOTHING`, simtime.Ratio)
	if err != nil {
		return fmt.Errorf("clock: garantizando la fila de world.sim_clock: %w", err)
	}
	return nil
}

// Load lee el ancla completa.
func (s *Store) Load(ctx context.Context) (Anchor, error) {
	var (
		simAt              int64
		ratio              int64
		frozen             bool
		wallAnchor, update time.Time
	)
	err := s.pool.QueryRow(ctx, `
		SELECT sim_time_at, wall_anchor, ratio, frozen, updated_at
		  FROM world.sim_clock
		 WHERE id = 1`).Scan(&simAt, &wallAnchor, &ratio, &frozen, &update)
	if err != nil {
		return Anchor{}, fmt.Errorf("clock: leyendo world.sim_clock: %w", err)
	}
	return Anchor{
		SimTimeAt:  simtime.SimTime(simAt),
		WallAnchor: wallAnchor,
		Ratio:      ratio,
		Frozen:     frozen,
		UpdatedAt:  update,
	}, nil
}

// PersistAnchor re-ancla el reloj: fija sim_time_at al sim-time derivado y
// wall_anchor al instante actual de la BD. Si el mundo está congelado la fila
// no se toca (el WHERE lo garantiza aunque el llamador tenga una caché vieja)
// y no es un error: el tiempo simplemente no avanza (ADR-003).
func (s *Store) PersistAnchor(ctx context.Context, derived simtime.SimTime) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE world.sim_clock
		   SET sim_time_at = $1, wall_anchor = now(), updated_at = now()
		 WHERE id = 1 AND NOT frozen`, int64(derived))
	if err != nil {
		return fmt.Errorf("clock: persistiendo el ancla: %w", err)
	}
	return nil
}
