package power

// Eventos de dominio del subpaquete, emitidos con outbox.Emit en la MISMA
// transacción que el cambio de estado (SAD/ADR-008). Los consumidores externos
// declaran estos nombres como constantes locales, nunca importan este paquete.
const (
	// AggregatePowerLine es el agregado de los eventos de líneas.
	AggregatePowerLine = "power_line"

	// EventPowerLineCreated se emite al dar de alta una línea de transmisión.
	EventPowerLineCreated = "power_line.created"
)

// PowerLineCreatedPayload es el contrato del evento power_line.created.
// Dinero como string de punto fijo; sim-time como entero.
type PowerLineCreatedPayload struct {
	PowerLineID    string `json:"power_line_id"`
	OwnerAccountID string `json:"owner_account_id"`
	RegionID       string `json:"region_id"`
	LengthM        int32  `json:"length_m"`
	BuildCost      string `json:"build_cost"`
	CreatedAtSim   int64  `json:"created_at_sim"`
}
