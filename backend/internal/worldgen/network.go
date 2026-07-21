package worldgen

// Fase de red inter-región (GDD 7.2/7.3, 15.1): conecta los junctions de regiones
// adyacentes en la grilla con enlaces RAIL (ambas terrestres no litorales) o SEA
// (alguna litoral/oceánica), y PARTE cada enlace por la frontera común en dos
// link_segments —uno por región, con su region_id— de modo que cada shard simule
// la congestión de su lado. Después crea TERMINALES intermodales (owner = banco
// central) en los junctions donde coinciden road y rail/sea, habilitando el
// transbordo road↔rail↔sea. Askadia se conecta por su junction existente sin
// tocar su red road interna. Todo idempotente por clave natural.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// connectRegions tiende los enlaces inter-región y crea las terminales. Recorre
// cada celda presente y conecta con sus vecinas al este y al sur (cada adyacencia
// una sola vez).
func connectRegions(ctx context.Context, st *genState) error {
	half := st.opts.half()
	for gy := -half; gy <= half; gy++ {
		for gx := -half; gx <= half; gx++ {
			a, ok := st.regions[[2]int{gx, gy}]
			if !ok {
				continue
			}
			// Vecina al este: frontera vertical x = (gx+1)*size.
			if b, ok := st.regions[[2]int{gx + 1, gy}]; ok {
				axis := int64(gx+1) * st.opts.RegionSizeM
				if err := linkNeighbors(ctx, st, a, b, true, axis); err != nil {
					return err
				}
			}
			// Vecina al sur: frontera horizontal y = (gy+1)*size.
			if b, ok := st.regions[[2]int{gx, gy + 1}]; ok {
				axis := int64(gy+1) * st.opts.RegionSizeM
				if err := linkNeighbors(ctx, st, a, b, false, axis); err != nil {
					return err
				}
			}
		}
	}
	return ensureTerminals(ctx, st)
}

// linkNeighbors crea el par de enlaces inter-región bidireccionales entre dos
// regiones adyacentes, cada uno partido en dos segmentos por la frontera.
func linkNeighbors(ctx context.Context, st *genState, a, b *genRegion, vertical bool, axis int64) error {
	// Marítimo (sea) cuando el cruce es agua: alguna región es océano abierto
	// (no se puede tender vía férrea sobre el mar) o AMBAS son litorales/oceánicas
	// (ruta de cabotaje entre puertos). En el resto —al menos un extremo terrestre
	// interior sin océano de por medio— el enlace es ferroviario (rail). GDD 7.2.
	mode := "rail"
	if a.Biome == BiomeOcean || b.Biome == BiomeOcean ||
		(isCoastalOrOcean(a.Biome) && isCoastalOrOcean(b.Biome)) {
		mode = "sea"
	}
	aJ := point{X: a.JunctionX, Y: a.JunctionY}
	bJ := point{X: b.JunctionX, Y: b.JunctionY}
	crossing := borderCrossing(aJ, bJ, vertical, axis)

	created1, err := ensureInterRegionLink(ctx, st, mode, a, aJ, b, bJ, crossing)
	if err != nil {
		return err
	}
	created2, err := ensureInterRegionLink(ctx, st, mode, b, bJ, a, aJ, crossing)
	if err != nil {
		return err
	}
	if created1 || created2 {
		created := 0
		if created1 {
			created++
		}
		if created2 {
			created++
		}
		if mode == "rail" {
			st.summary.RailLinks += created
		} else {
			st.summary.SeaLinks += created
		}
		st.logger.Info("enlace inter-región creado",
			slog.String("mode", mode),
			slog.String("from", fmt.Sprintf("%d,%d", a.GridX, a.GridY)),
			slog.String("to", fmt.Sprintf("%d,%d", b.GridX, b.GridY)))
	}
	return nil
}

// ensureInterRegionLink garantiza UN enlace dirigido from→to del modo dado (clave
// natural: (from_node_id, to_node_id, mode)), con su trazado from→crossing→to y
// sus DOS segmentos: seq 1 en la región de origen (from→crossing) y seq 2 en la de
// destino (crossing→to), cada uno con su region_id. Devuelve si se creó.
func ensureInterRegionLink(ctx context.Context, st *genState, mode string, from *genRegion, fromJ point, to *genRegion, toJ point, crossing point) (bool, error) {
	var linkID uuid.UUID
	err := st.pool.QueryRow(ctx, `
		SELECT id FROM world.network_links
		 WHERE from_node_id = $1 AND to_node_id = $2 AND mode = $3::world.link_mode
		 LIMIT 1`, from.JunctionID, to.JunctionID, mode).Scan(&linkID)
	switch {
	case err == nil:
		return false, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return false, fmt.Errorf("worldgen: consultando enlace %s %s→%s: %w", mode, from.JunctionID, to.JunctionID, err)
	}

	len1 := euclideanM(fromJ.X, fromJ.Y, crossing.X, crossing.Y)
	len2 := euclideanM(crossing.X, crossing.Y, toJ.X, toJ.Y)
	total := len1 + len2
	path := line3WKT(fromJ.X, fromJ.Y, crossing.X, crossing.Y, toJ.X, toJ.Y)

	capacity, speed := seaCapacityPerHour, seaBaseSpeedKmh
	if mode == "rail" {
		capacity, speed = railCapacityPerHour, railBaseSpeedKmh
	}

	linkID, err = newID()
	if err != nil {
		return false, err
	}
	if _, err := st.pool.Exec(ctx, `
		INSERT INTO world.network_links
		       (id, mode, from_node_id, to_node_id, path, length_m, capacity_per_hour, base_speed_kmh)
		VALUES ($1, $2::world.link_mode, $3, $4, ST_GeomFromText($5, 0), $6, $7, $8)`,
		linkID, mode, from.JunctionID, to.JunctionID, path, total, capacity, speed); err != nil {
		return false, fmt.Errorf("worldgen: creando enlace %s %s→%s: %w", mode, from.JunctionID, to.JunctionID, err)
	}

	// Segmento 1: lado de la región de origen (from→crossing).
	if err := insertSegment(ctx, st, linkID, from.RegionID, 1,
		lineWKT(fromJ.X, fromJ.Y, crossing.X, crossing.Y), len1); err != nil {
		return false, err
	}
	// Segmento 2: lado de la región de destino (crossing→to).
	if err := insertSegment(ctx, st, linkID, to.RegionID, 2,
		lineWKT(crossing.X, crossing.Y, toJ.X, toJ.Y), len2); err != nil {
		return false, err
	}
	return true, nil
}

// insertSegment inserta un link_segment (congestión fluida 1.0) del enlace en la
// región indicada.
func insertSegment(ctx context.Context, st *genState, linkID, regionID uuid.UUID, seq int, portionWKT string, lengthM int64) error {
	segID, err := newID()
	if err != nil {
		return err
	}
	if _, err := st.pool.Exec(ctx, `
		INSERT INTO world.link_segments (id, link_id, region_id, seq, portion, length_m, congestion_ema)
		VALUES ($1, $2, $3, $4, ST_GeomFromText($5, 0), $6, 1.0)`,
		segID, linkID, regionID, seq, portionWKT, lengthM); err != nil {
		return fmt.Errorf("worldgen: creando segmento seq %d del enlace %s: %w", seq, linkID, err)
	}
	return nil
}

// ensureTerminals crea una terminal intermodal (owner = banco central) en cada
// junction donde coinciden road y rail/sea, habilitando el transbordo, y le asegura
// sus slots de prioridad vendibles (GDD 7.3). Recorre los junctions de todas las
// regiones del estado (incluido el de Askadia). Idempotente.
func ensureTerminals(ctx context.Context, st *genState) error {
	for _, reg := range st.regions {
		terminalID, created, err := ensureTerminalIfIntermodal(ctx, st, reg.JunctionID)
		if err != nil {
			return err
		}
		if terminalID == uuid.Nil {
			continue // el nodo no es intermodal
		}
		if created {
			st.summary.TerminalsCreated++
			st.logger.Info("terminal intermodal creada",
				slog.String("node_id", reg.JunctionID.String()),
				slog.String("region", fmt.Sprintf("%d,%d", reg.GridX, reg.GridY)))
		}
		// Slots de prioridad a la venta (idempotente: solo si la terminal no tiene).
		// Cubre también terminales de un worldgen previo sin slots.
		n, err := ensureTerminalSlots(ctx, st, terminalID)
		if err != nil {
			return err
		}
		st.summary.SlotsCreated += n
	}
	return nil
}

// ensureTerminalIfIntermodal crea la terminal en el nodo si tiene incidentes a la
// vez enlaces road y enlaces rail/sea (cambio de modo posible) y aún no la tiene
// (clave natural: node_id UNIQUE). Devuelve el id de la terminal (uuid.Nil si el
// nodo no es intermodal) y si se creó en esta pasada.
func ensureTerminalIfIntermodal(ctx context.Context, st *genState, nodeID uuid.UUID) (uuid.UUID, bool, error) {
	var hasRoad, hasRailSea bool
	if err := st.pool.QueryRow(ctx, `
		SELECT
		  EXISTS(SELECT 1 FROM world.network_links WHERE (from_node_id = $1 OR to_node_id = $1) AND mode = 'road'),
		  EXISTS(SELECT 1 FROM world.network_links WHERE (from_node_id = $1 OR to_node_id = $1) AND mode IN ('rail','sea'))`,
		nodeID).Scan(&hasRoad, &hasRailSea); err != nil {
		return uuid.Nil, false, fmt.Errorf("worldgen: comprobando modos del nodo %s: %w", nodeID, err)
	}
	if !hasRoad || !hasRailSea {
		return uuid.Nil, false, nil
	}

	var existing uuid.UUID
	err := st.pool.QueryRow(ctx, `SELECT id FROM world.terminals WHERE node_id = $1`, nodeID).Scan(&existing)
	switch {
	case err == nil:
		return existing, false, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return uuid.Nil, false, fmt.Errorf("worldgen: consultando terminal del nodo %s: %w", nodeID, err)
	}
	id, err := newID()
	if err != nil {
		return uuid.Nil, false, err
	}
	if _, err := st.pool.Exec(ctx, `
		INSERT INTO world.terminals (id, node_id, owner_account_id, transshipment_per_hour)
		VALUES ($1, $2, $3, $4)`,
		id, nodeID, st.bank.ID, terminalTransshipmentPerHour); err != nil {
		return uuid.Nil, false, fmt.Errorf("worldgen: creando terminal del nodo %s: %w", nodeID, err)
	}
	return id, true, nil
}

// ensureTerminalSlots crea los slots de prioridad de una terminal si aún no tiene
// ninguno (idempotente por conteo). Ofrece terminalSlotTiers slots de priority_tier
// 1..N a la venta (holder_account_id NULL), con precio creciente con la prioridad
// (tier 1 = más caro). Devuelve cuántos slots creó.
func ensureTerminalSlots(ctx context.Context, st *genState, terminalID uuid.UUID) (int, error) {
	var have int
	if err := st.pool.QueryRow(ctx,
		`SELECT count(*) FROM world.terminal_slots WHERE terminal_id = $1`, terminalID).Scan(&have); err != nil {
		return 0, fmt.Errorf("worldgen: contando slots de la terminal %s: %w", terminalID, err)
	}
	if have > 0 {
		return 0, nil // ya tiene slots
	}
	created := 0
	for tier := 1; tier <= terminalSlotTiers; tier++ {
		id, err := newID()
		if err != nil {
			return created, err
		}
		price := terminalSlotBasePrice * int64(terminalSlotTiers-tier+1)
		if _, err := st.pool.Exec(ctx, `
			INSERT INTO world.terminal_slots (id, terminal_id, priority_tier, price)
			VALUES ($1, $2, $3, $4)`,
			id, terminalID, tier, price); err != nil {
			return created, fmt.Errorf("worldgen: creando slot tier %d de la terminal %s: %w", tier, terminalID, err)
		}
		created++
	}
	st.logger.Info("slots de prioridad de terminal creados",
		slog.String("terminal_id", terminalID.String()), slog.Int("slots", created))
	return created, nil
}
