package botsdk

import (
	"context"
	"net/http"
	"net/url"
)

// ── Catálogo y flota ──

// VehicleTypesQuery filtra GET /world/vehicle-types.
type VehicleTypesQuery struct {
	Mode LinkMode
	PageQuery
}

// values serializa la query.
func (q VehicleTypesQuery) values() url.Values {
	v := url.Values{}
	if q.Mode != "" {
		v.Set("mode", string(q.Mode))
	}
	q.apply(v)
	return v
}

// VehicleTypes devuelve el catálogo de tipos de vehículo
// (GET /world/vehicle-types).
func (c *Client) VehicleTypes(ctx context.Context, q VehicleTypesQuery) (Page[VehicleType], error) {
	return getList[VehicleType](ctx, c, "/world/vehicle-types", q.values())
}

// VehiclesQuery filtra GET /world/vehicles.
type VehiclesQuery struct {
	Status  VehicleStatus
	RouteID string
	PageQuery
}

// values serializa la query.
func (q VehiclesQuery) values() url.Values {
	v := url.Values{}
	if q.Status != "" {
		v.Set("status", string(q.Status))
	}
	if q.RouteID != "" {
		v.Set("route_id", q.RouteID)
	}
	q.apply(v)
	return v
}

// ListVehicles devuelve la flota propia (GET /world/vehicles).
func (c *Client) ListVehicles(ctx context.Context, q VehiclesQuery) (Page[Vehicle], error) {
	return getList[Vehicle](ctx, c, "/world/vehicles", q.values())
}

// PurchaseVehicle compra un vehículo a precio de catálogo con entrega en un
// nodo propio o terminal accesible (POST /world/vehicles).
func (c *Client) PurchaseVehicle(ctx context.Context, in VehiclePurchase) (Vehicle, error) {
	return mutate[Vehicle](ctx, c, http.MethodPost, "/world/vehicles", in)
}

// GetVehicle devuelve un vehículo con su posición derivada analíticamente
// (GET /world/vehicles/{id}).
func (c *Client) GetVehicle(ctx context.Context, vehicleID string) (Vehicle, error) {
	return getOne[Vehicle](ctx, c, "/world/vehicles/"+pathID(vehicleID), nil)
}

// UpdateVehicle comanda un vehículo: asigna/retira ruta o programa
// mantenimiento (PATCH /world/vehicles/{id}; 403 VEHICLE_SEALED si está
// SELLADO durante un handoff).
func (c *Client) UpdateVehicle(ctx context.Context, vehicleID string, in VehicleUpdate) (Vehicle, error) {
	return mutate[Vehicle](ctx, c, http.MethodPatch, "/world/vehicles/"+pathID(vehicleID), in)
}

// RepositionVehicle pone en ruta EN VACÍO un vehículo propio idle por una ruta
// propia que empieza en su nodo actual (POST /world/vehicles/{id}/reposition).
// Es el viaje de reposicionamiento (deadhead) del transporte real: sin él un
// vehículo se queda varado donde terminó su última entrega, porque la carga
// nueva nace en OTROS nodos. El vehículo queda in_transit; la llegada al nodo
// final lo devuelve a idle allí.
func (c *Client) RepositionVehicle(ctx context.Context, vehicleID, routeID string) (Vehicle, error) {
	in := VehicleReposition{RouteID: routeID}
	return mutate[Vehicle](ctx, c, http.MethodPost, "/world/vehicles/"+pathID(vehicleID)+"/reposition", in)
}

// ── Cargamentos ──

// ShipmentsQuery filtra GET /world/shipments.
type ShipmentsQuery struct {
	Status ShipmentStatus
	// ContractID filtra por el CCRI de bienes del cargamento.
	ContractID string
	// FreightContractID filtra por el CCRI-Flete del cargamento (el que un
	// transportista debe despachar).
	FreightContractID string
	VehicleID         string
	PageQuery
}

// values serializa la query.
func (q ShipmentsQuery) values() url.Values {
	v := url.Values{}
	if q.Status != "" {
		v.Set("status", string(q.Status))
	}
	if q.ContractID != "" {
		v.Set("contract_id", q.ContractID)
	}
	if q.FreightContractID != "" {
		v.Set("freight_contract_id", q.FreightContractID)
	}
	if q.VehicleID != "" {
		v.Set("vehicle_id", q.VehicleID)
	}
	q.apply(v)
	return v
}

// ListShipments devuelve los cargamentos visibles, etiquetados por contrato: los
// propios y los de un CCRI-Flete en el que la corporación es TRANSPORTISTA —el
// dueño es el cargador, pero el despacho es del transportista, GDD 5.3.2
// (GET /world/shipments).
func (c *Client) ListShipments(ctx context.Context, q ShipmentsQuery) (Page[Shipment], error) {
	return getList[Shipment](ctx, c, "/world/shipments", q.values())
}

// GetShipment devuelve un cargamento (GET /world/shipments/{id}).
func (c *Client) GetShipment(ctx context.Context, shipmentID string) (Shipment, error) {
	return getOne[Shipment](ctx, c, "/world/shipments/"+pathID(shipmentID), nil)
}

// Dispatch despacha un cargamento in_warehouse: lo carga en un vehículo
// propio idle situado en el nodo del cargamento y lo pone en ruta hasta el
// destino del contrato (POST /world/shipments/{id}/dispatch). Nada se
// teletransporta: la llegada física confirma la entrega del CCRI.
func (c *Client) Dispatch(ctx context.Context, shipmentID, vehicleID, routeID string) (Shipment, error) {
	in := ShipmentDispatch{VehicleID: vehicleID, RouteID: routeID}
	return mutate[Shipment](ctx, c, http.MethodPost, "/world/shipments/"+pathID(shipmentID)+"/dispatch", in)
}
