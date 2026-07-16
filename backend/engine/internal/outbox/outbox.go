// Package outbox inserta eventos en outbox.events SIEMPRE dentro de la misma
// transacción que el cambio de estado que los causa (patrón transactional
// outbox, ADR-008), y construye los payloads con la entidad serializada
// exactamente como su DTO REST (specs/openapi.yaml) bajo la clave "entity".
package outbox

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"imperio/engine/internal/core"
)

// Location es la posición espacial (solo eventos vehicle.*,
// building.status_changed y city.*, según el contrato compartido).
type Location struct {
	Lon float64 `json:"lon"`
	Lat float64 `json:"lat"`
}

// Insert añade un evento al outbox. payload debe seguir el contrato
// compartido: {"entity": DTO REST, "location": {...} solo espaciales, campos
// extra al nivel raíz}.
func Insert(ctx context.Context, q core.Querier, aggregateType string, aggregateID uuid.UUID, eventType string, simTime int64, payload map[string]any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("outbox: marshal %s: %w", eventType, err)
	}
	if _, err := q.Exec(ctx,
		`INSERT INTO outbox.events (aggregate_type, aggregate_id, event_type, payload, sim_time_at)
		 VALUES ($1, $2, $3, $4, $5)`,
		aggregateType, aggregateID, eventType, b, simTime); err != nil {
		return fmt.Errorf("outbox: insert %s: %w", eventType, err)
	}
	return nil
}

// Payload construye el mapa base {"entity": ..., "location": ...?, extras...}.
func Payload(entity map[string]any, loc *Location, extra map[string]any) map[string]any {
	p := map[string]any{"entity": entity}
	if loc != nil {
		p["location"] = map[string]any{"lon": loc.Lon, "lat": loc.Lat}
	}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

// NodeLocation devuelve lon/lat de un nodo del grafo.
func NodeLocation(ctx context.Context, q core.Querier, nodeID uuid.UUID) (*Location, error) {
	var lon, lat float64
	err := q.QueryRow(ctx,
		`SELECT ST_X(location), ST_Y(location) FROM world.network_nodes WHERE id = $1`, nodeID).
		Scan(&lon, &lat)
	if err != nil {
		return nil, fmt.Errorf("outbox: location de nodo %s: %w", nodeID, err)
	}
	return &Location{Lon: lon, Lat: lat}, nil
}
