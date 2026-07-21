package fleet

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/outbox"
	"github.com/lokiteitor/global-market/backend/internal/platform/db"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
	"github.com/lokiteitor/global-market/backend/internal/world/sqlcgen"
)

// SQLSTATE y constraints que este subpaquete traduce a errores tipados.
const (
	sqlstateCheckViolation = "23514" // check_violation
	sqlstateFKViolation    = "23503" // foreign_key_violation

	constraintNonNegative = "ck_accounts_non_negative"
)

// SimSource entrega el sim-time actual del mundo. Producción: *clock.Reader; los
// tests inyectan un reloj fijo.
type SimSource interface {
	Now(ctx context.Context) simtime.SimTime
}

// Service implementa la flota y el despacho de cargamentos del contrato (handlers
// world/vehicles|shipments). Toda operación que mueve valor (compra de vehículo)
// o pone en tránsito (despacho) corre en una única transacción SERIALIZABLE
// (platform/db.RunSerializable) con el evento del outbox en la misma tx.
type Service struct {
	pool   *pgxpool.Pool
	repo   *Repo
	sim    SimSource
	opts   Options
	logger *slog.Logger

	purchased     prometheus.Counter
	dispatched    prometheus.Counter
	repositioned  prometheus.Counter
	slotPurchases prometheus.Counter
}

// NewService construye el servicio sobre el pool compartido de la plataforma.
func NewService(pool *pgxpool.Pool, sim SimSource, opts Options, logger *slog.Logger, reg prometheus.Registerer) (*Service, error) {
	if pool == nil {
		return nil, errors.New("world/fleet: el pool de BD es obligatorio")
	}
	if sim == nil {
		return nil, errors.New("world/fleet: el SimSource es obligatorio")
	}
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{
		pool:   pool,
		repo:   NewRepo(pool),
		sim:    sim,
		opts:   opts,
		logger: logger,
		purchased: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_vehicles_purchased_total",
			Help: "Total de vehículos comprados en el mercado primario.",
		}),
		dispatched: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_shipments_dispatched_total",
			Help: "Total de cargamentos despachados (puestos en tránsito).",
		}),
		repositioned: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_vehicles_repositioned_total",
			Help: "Total de vehículos puestos en ruta EN VACÍO (viajes de reposicionamiento).",
		}),
		slotPurchases: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_slot_purchases_total",
			Help: "Total de slots de prioridad de terminal comprados (GDD 7.3).",
		}),
	}
	if reg != nil {
		reg.MustRegister(s.purchased, s.dispatched, s.repositioned, s.slotPurchases)
	}
	return s, nil
}

// ─── Catálogo y flota (lectura) ───────────────────────────────────────────────

// ListVehicleTypes devuelve el catálogo con filtro por modo.
func (s *Service) ListVehicleTypes(ctx context.Context, f VehicleTypeFilter) ([]VehicleType, string, error) {
	if f.Mode != "" && !validLinkMode(f.Mode) {
		return nil, "", fmt.Errorf("%w: modo inválido %q", ErrValidation, f.Mode)
	}
	limit := normalizeLimit(f.Limit)
	afterID, err := cursorID(f.Cursor)
	if err != nil {
		return nil, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.QueryTimeout)
	defer cancel()

	rows, err := s.repo.ListVehicleTypes(ctx, f.Mode, afterID, limit+1)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > int(limit) {
		rows = rows[:limit]
		next = encodeCursor(rows[len(rows)-1].ID)
	}
	return rows, next, nil
}

// ListVehicles devuelve la flota del titular con la posición derivada.
func (s *Service) ListVehicles(ctx context.Context, owner uuid.UUID, f VehicleFilter) ([]Vehicle, string, error) {
	if f.Status != "" && !validVehicleStatus(f.Status) {
		return nil, "", fmt.Errorf("%w: status inválido %q", ErrValidation, f.Status)
	}
	limit := normalizeLimit(f.Limit)
	afterID, err := cursorID(f.Cursor)
	if err != nil {
		return nil, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.QueryTimeout)
	defer cancel()

	rows, err := s.repo.ListVehicles(ctx, owner, f, afterID, limit+1, s.sim.Now(ctx))
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > int(limit) {
		rows = rows[:limit]
		next = encodeCursor(rows[len(rows)-1].ID)
	}
	return rows, next, nil
}

// GetVehicle devuelve un vehículo propio con posición. 404/403 por propiedad.
func (s *Service) GetVehicle(ctx context.Context, owner, id uuid.UUID) (Vehicle, error) {
	ctx, cancel := context.WithTimeout(ctx, s.opts.QueryTimeout)
	defer cancel()
	v, err := s.repo.GetVehicle(ctx, id, s.sim.Now(ctx))
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Vehicle{}, fmt.Errorf("%w (%s)", ErrVehicleNotFound, id)
	case err != nil:
		return Vehicle{}, fmt.Errorf("world/fleet: consultando el vehículo %s: %w", id, err)
	}
	if v.OwnerAccountID != owner {
		return Vehicle{}, fmt.Errorf("%w (%s)", ErrForbidden, id)
	}
	return v, nil
}

// ─── Compra de vehículo ───────────────────────────────────────────────────────

// PurchaseVehicle cobra el precio de catálogo al sink y da de alta un vehículo
// idle en el nodo de entrega, con el tanque lleno equivalente a autonomy_km.
// DECISIÓN (Fase 1): el vehículo arranca con combustible suficiente para su
// autonomía —fuel = fuel_per_100km * autonomy_km / 100— para que pueda operar sin
// un endpoint de repostaje (el combustible llega como cualquier insumo, GDD 5.8).
func (s *Service) PurchaseVehicle(ctx context.Context, owner uuid.UUID, in VehiclePurchase) (Vehicle, error) {
	if owner == uuid.Nil {
		return Vehicle{}, fmt.Errorf("%w: dueño vacío", ErrValidation)
	}
	simNow := s.sim.Now(ctx)

	var vehicleID uuid.UUID
	var price, fuel int64
	err := db.RunSerializable(ctx, s.pool, func(tx pgx.Tx) error {
		r := s.repo.WithTx(tx)

		vt, err := r.GetVehicleType(ctx, in.VehicleTypeID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("%w: el tipo de vehículo %s no existe", ErrNotFound, in.VehicleTypeID)
		case err != nil:
			return fmt.Errorf("world/fleet: consultando el tipo %s: %w", in.VehicleTypeID, err)
		}
		price = vt.PurchasePrice

		node, err := r.GetNode(ctx, in.DeliveryNodeID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("%w: el nodo de entrega %s no existe", ErrNotFound, in.DeliveryNodeID)
		case err != nil:
			return fmt.Errorf("world/fleet: consultando el nodo %s: %w", in.DeliveryNodeID, err)
		}
		// Compatibilidad con el modo: el nodo debe ser accesible por un enlace del
		// modo del vehículo (un almacén/instalación conectado a la red vial).
		accessible, err := r.NodeHasModeLink(ctx, node.ID, vt.Mode)
		if err != nil {
			return err
		}
		if !accessible {
			return fmt.Errorf("%w: el nodo de entrega %s no es accesible por modo %s", ErrValidation, in.DeliveryNodeID, vt.Mode)
		}

		fuel = fuelForDistance(vt.FuelPer100km, int64(vt.AutonomyKm)*1000)

		id, err := newUUIDv7()
		if err != nil {
			return err
		}
		if err := s.chargeToSink(ctx, r, owner, id, vt.PurchasePrice, simNow,
			fmt.Sprintf("Compra de vehículo %s (%d)", vt.Code, vt.PurchasePrice)); err != nil {
			return err
		}
		vehicleID, err = r.InsertVehicle(ctx, insertVehicleParams{
			ID: id, VehicleTypeID: vt.ID, Owner: owner, Fuel: fuel, AtNode: node.ID, SimNow: simNow,
		})
		if err != nil {
			return err
		}
		return outbox.Emit(ctx, tx, int64(simNow), AggregateVehicle, vehicleID, EventVehiclePurchased, VehiclePurchasedPayload{
			VehicleID: vehicleID.String(), OwnerAccountID: owner.String(), VehicleTypeID: vt.ID.String(),
			DeliveryNodeID: node.ID.String(), PurchasePrice: fixed(vt.PurchasePrice), Fuel: fixed(fuel),
			PurchasedAtSim: int64(simNow),
		})
	})
	if err != nil {
		return Vehicle{}, mapLedgerError(err)
	}
	s.purchased.Inc()
	s.logger.Info("vehículo comprado",
		slog.String("vehicle_id", vehicleID.String()), slog.String("owner", owner.String()),
		slog.String("vehicle_type_id", in.VehicleTypeID.String()), slog.Int64("purchase_price", price),
		slog.Int64("fuel", fuel))

	v, err := s.repo.GetVehicle(ctx, vehicleID, simNow)
	if err != nil {
		return Vehicle{}, fmt.Errorf("world/fleet: releyendo el vehículo comprado %s: %w", vehicleID, err)
	}
	return v, nil
}

// ─── Comando de vehículo ──────────────────────────────────────────────────────

// UpdateVehicle asigna/retira ruta (route_id nullable) e/o programa mantenimiento
// (in_maintenance, reduce el desgaste). 403 si es ajeno o está sellado.
func (s *Service) UpdateVehicle(ctx context.Context, owner, id uuid.UUID, in VehicleUpdate) (Vehicle, error) {
	if !in.SetRoute && !in.ScheduleMaintenance {
		return Vehicle{}, fmt.Errorf("%w: la actualización no cambia nada (route_id o schedule_maintenance)", ErrValidation)
	}
	simNow := s.sim.Now(ctx)

	var finalStatus string
	var finalRoute *uuid.UUID
	err := db.RunSerializable(ctx, s.pool, func(tx pgx.Tx) error {
		r := s.repo.WithTx(tx)

		v, err := r.LockVehicle(ctx, id)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("%w (%s)", ErrVehicleNotFound, id)
		case err != nil:
			return fmt.Errorf("world/fleet: bloqueando el vehículo %s: %w", id, err)
		}
		if v.OwnerAccountID != owner {
			return fmt.Errorf("%w (%s)", ErrForbidden, id)
		}
		if v.Status == string(sqlcgen.WorldVehicleStatusSealed) {
			return fmt.Errorf("%w (%s)", ErrVehicleSealed, id)
		}
		finalStatus = v.Status
		finalRoute = v.RouteID

		if in.SetRoute {
			if in.RouteID != nil {
				routeOwner, _, rerr := r.GetRouteOwnerActive(ctx, *in.RouteID)
				switch {
				case errors.Is(rerr, pgx.ErrNoRows):
					return fmt.Errorf("%w: la ruta %s no existe", ErrValidation, *in.RouteID)
				case rerr != nil:
					return fmt.Errorf("world/fleet: consultando la ruta %s: %w", *in.RouteID, rerr)
				}
				if routeOwner != owner {
					return fmt.Errorf("%w: la ruta %s pertenece a otra corporación", ErrValidation, *in.RouteID)
				}
			}
			if err := r.SetVehicleRoute(ctx, id, in.RouteID, simNow); err != nil {
				return err
			}
			finalRoute = in.RouteID
		}
		if in.ScheduleMaintenance {
			if v.Status != string(sqlcgen.WorldVehicleStatusIdle) {
				return fmt.Errorf("%w: solo se puede dar mantenimiento a un vehículo idle (estado %s)", ErrVehicleNotIdle, v.Status)
			}
			repairUntil := simNow + simtime.SimTime(s.opts.MaintenanceSimSeconds)
			if err := r.SetVehicleMaintenance(ctx, id, repairUntil, simNow); err != nil {
				return err
			}
			finalStatus = string(sqlcgen.WorldVehicleStatusInMaintenance)
		}

		return outbox.Emit(ctx, tx, int64(simNow), AggregateVehicle, id, EventVehicleUpdated, VehicleUpdatedPayload{
			VehicleID: id.String(), OwnerAccountID: owner.String(), Status: finalStatus,
			RouteID: uuidOrEmpty(finalRoute), UpdatedAtSim: int64(simNow),
		})
	})
	if err != nil {
		return Vehicle{}, mapLedgerError(err)
	}
	s.logger.Info("vehículo actualizado",
		slog.String("vehicle_id", id.String()), slog.String("owner", owner.String()),
		slog.String("status", finalStatus), slog.String("route_id", uuidOrEmpty(finalRoute)))

	v, err := s.repo.GetVehicle(ctx, id, simNow)
	if err != nil {
		return Vehicle{}, fmt.Errorf("world/fleet: releyendo el vehículo %s: %w", id, err)
	}
	return v, nil
}

// ─── Reposicionamiento en vacío ───────────────────────────────────────────────

// RepositionVehicle pone en ruta un vehículo propio idle SIN carga por una ruta
// propia que empieza en su nodo actual: el viaje en vacío (deadhead) del
// transporte real.
//
// Sin esta primitiva un vehículo solo podría moverse llevando un cargamento y,
// como la entrega lo deja idle en el nodo DESTINO, quedaría varado allí para
// siempre: ningún cargamento futuro nace en ese nodo, así que el vehículo nunca
// volvería a estar donde hay carga. Es el mismo motor de tránsito del despacho
// (mismas validaciones de modo, extremos y combustible); solo cambia que no hay
// cargamento a bordo, y por eso se exige explícitamente que el vehículo vaya
// vacío.
func (s *Service) RepositionVehicle(ctx context.Context, owner, vehicleID uuid.UUID, in VehicleReposition) (Vehicle, error) {
	simNow := s.sim.Now(ctx)

	var originNode, destNode uuid.UUID
	var distanceM int64
	err := db.RunSerializable(ctx, s.pool, func(tx pgx.Tx) error {
		r := s.repo.WithTx(tx)

		v, err := r.LockVehicle(ctx, vehicleID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("%w (%s)", ErrVehicleNotFound, vehicleID)
		case err != nil:
			return fmt.Errorf("world/fleet: bloqueando el vehículo %s: %w", vehicleID, err)
		}
		if v.OwnerAccountID != owner {
			return fmt.Errorf("%w (vehículo %s)", ErrForbidden, vehicleID)
		}
		if v.Status == string(sqlcgen.WorldVehicleStatusSealed) {
			return fmt.Errorf("%w (%s)", ErrVehicleSealed, vehicleID)
		}
		if v.Status != string(sqlcgen.WorldVehicleStatusIdle) {
			return fmt.Errorf("%w (estado %s)", ErrVehicleNotIdle, v.Status)
		}
		if v.AtNodeID == nil {
			return fmt.Errorf("%w: el vehículo no está en un nodo", ErrValidation)
		}
		originNode = *v.AtNodeID

		// EN VACÍO: un vehículo con carga a bordo se mueve despachando el
		// cargamento (POST /world/shipments/{id}/dispatch), nunca por aquí; si no,
		// la carga viajaría sin contrato que la ampare.
		aboard, err := r.ListShipments(ctx, owner, ShipmentFilter{
			VehicleID: &vehicleID, Status: string(sqlcgen.WorldShipmentStatusInTransit),
		}, nil, 1)
		if err != nil {
			return err
		}
		if len(aboard) > 0 {
			return fmt.Errorf("%w: el vehículo lleva carga a bordo (%s): se mueve despachando el cargamento", ErrValidation, aboard[0].ID)
		}

		vt, err := r.GetVehicleType(ctx, v.VehicleTypeID)
		if err != nil {
			return fmt.Errorf("world/fleet: consultando el tipo del vehículo %s: %w", vehicleID, err)
		}

		// Modo: un vehículo solo circula por enlaces de SU modo (GDD 7.3).
		wrong, err := r.CountRouteLegsWrongMode(ctx, in.RouteID, vt.Mode)
		if err != nil {
			return err
		}
		if wrong > 0 {
			return fmt.Errorf("%w (vehículo %s modo %s)", ErrWrongVehicleMode, vehicleID, vt.Mode)
		}

		routeOwner, _, err := r.GetRouteOwnerActive(ctx, in.RouteID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("%w (%s)", ErrNotFound, in.RouteID)
		case err != nil:
			return fmt.Errorf("world/fleet: consultando la ruta %s: %w", in.RouteID, err)
		}
		if routeOwner != owner {
			return fmt.Errorf("%w (ruta %s)", ErrForbidden, in.RouteID)
		}
		first, err := r.GetRouteFirstSegment(ctx, in.RouteID)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: la ruta %s no tiene tramos", ErrValidation, in.RouteID)
		} else if err != nil {
			return fmt.Errorf("world/fleet: consultando el primer segmento de la ruta %s: %w", in.RouteID, err)
		}
		ep, err := r.GetRouteEndpoints(ctx, in.RouteID)
		if err != nil {
			return fmt.Errorf("world/fleet: consultando los extremos de la ruta %s: %w", in.RouteID, err)
		}
		if ep.FirstFromNode != originNode {
			return fmt.Errorf("%w: la ruta no empieza en el nodo del vehículo", ErrValidation)
		}
		destNode = ep.LastToNode
		distanceM = ep.TotalLengthM

		// Combustible: alcanza la distancia total de la ruta (el vehículo nace con
		// el tanque de su autonomía y no hay repostaje, ver PurchaseVehicle).
		fuelNeeded := fuelForDistance(vt.FuelPer100km, ep.TotalLengthM)
		if v.Fuel < fuelNeeded {
			return fmt.Errorf("%w: el combustible del vehículo (%d) no cubre la distancia de la ruta (necesita %d)", ErrValidation, v.Fuel, fuelNeeded)
		}

		af := advanceFn{
			BaseSpeedKmh:  minInt32(vt.SpeedKmh, first.BaseSpeedKmh),
			CongestionEma: first.CongestionEma,
			LengthM:       first.LengthM,
			Dir:           1,
		}
		afBytes, err := af.marshal()
		if err != nil {
			return err
		}
		if err := r.DispatchVehicle(ctx, dispatchVehicleParams{
			ID: vehicleID, RouteID: in.RouteID, OnSegment: first.SegmentID, AdvanceFn: afBytes, SimNow: simNow,
		}); err != nil {
			return err
		}
		return outbox.Emit(ctx, tx, int64(simNow), AggregateVehicle, vehicleID, EventVehicleRepositioned, VehicleRepositionedPayload{
			VehicleID: vehicleID.String(), OwnerAccountID: owner.String(), RouteID: in.RouteID.String(),
			OriginNodeID: originNode.String(), DestinationNodeID: destNode.String(),
			DistanceM: distanceM, RepositionedAtSim: int64(simNow),
		})
	})
	if err != nil {
		return Vehicle{}, mapLedgerError(err)
	}
	s.repositioned.Inc()
	s.logger.Info("vehículo reposicionado en vacío",
		slog.String("vehicle_id", vehicleID.String()), slog.String("owner", owner.String()),
		slog.String("route_id", in.RouteID.String()),
		slog.String("origin_node_id", originNode.String()), slog.String("destination_node_id", destNode.String()),
		slog.Int64("distance_m", distanceM))

	v, err := s.repo.GetVehicle(ctx, vehicleID, simNow)
	if err != nil {
		return Vehicle{}, fmt.Errorf("world/fleet: releyendo el vehículo reposicionado %s: %w", vehicleID, err)
	}
	return v, nil
}

// ─── Cargamentos (lectura) ────────────────────────────────────────────────────

// ListShipments devuelve los cargamentos VISIBLES para la corporación: los
// propios y los de un CCRI-Flete en el que es TRANSPORTISTA (dueño = cargador,
// pero el despacho es suyo: sin verlos no podría ejecutar el flete que aceptó).
func (s *Service) ListShipments(ctx context.Context, owner uuid.UUID, f ShipmentFilter) ([]Shipment, string, error) {
	if f.Status != "" && !validShipmentStatus(f.Status) {
		return nil, "", fmt.Errorf("%w: status inválido %q", ErrValidation, f.Status)
	}
	limit := normalizeLimit(f.Limit)
	afterID, err := cursorID(f.Cursor)
	if err != nil {
		return nil, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, s.opts.QueryTimeout)
	defer cancel()

	rows, err := s.repo.ListShipments(ctx, owner, f, afterID, limit+1)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > int(limit) {
		rows = rows[:limit]
		next = encodeCursor(rows[len(rows)-1].ID)
	}
	return rows, next, nil
}

// GetShipment devuelve un cargamento visible para la corporación: propio o, en
// un CCRI-Flete, el que transporta como transportista (misma visibilidad que
// ListShipments y misma autorización que el despacho). 404/403 en otro caso.
func (s *Service) GetShipment(ctx context.Context, owner, id uuid.UUID) (Shipment, error) {
	ctx, cancel := context.WithTimeout(ctx, s.opts.QueryTimeout)
	defer cancel()
	sh, err := s.repo.GetShipment(ctx, id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Shipment{}, fmt.Errorf("%w (%s)", ErrShipmentNotFound, id)
	case err != nil:
		return Shipment{}, fmt.Errorf("world/fleet: consultando el cargamento %s: %w", id, err)
	}
	if sh.OwnerAccountID == owner {
		return sh, nil
	}
	if sh.FreightContractID != nil {
		carrier, cerr := s.repo.GetFreightCarrier(ctx, *sh.FreightContractID)
		switch {
		case errors.Is(cerr, pgx.ErrNoRows):
			// Flete inexistente: el cargamento solo lo ve su dueño.
		case cerr != nil:
			return Shipment{}, fmt.Errorf("world/fleet: consultando el transportista del flete %s: %w", *sh.FreightContractID, cerr)
		case carrier == owner:
			return sh, nil
		}
	}
	return Shipment{}, fmt.Errorf("%w (%s)", ErrForbidden, id)
}

// ─── Despacho (ejecución logística del CCRI) ──────────────────────────────────

// DispatchShipment carga un cargamento in_warehouse en un vehículo idle del mismo
// nodo y lo pone en ruta hasta el destino del contrato. Valida propiedad, estado,
// coincidencia de nodos, extremos de la ruta, capacidad de carga y combustible
// para la distancia total. Todo en una transacción SERIALIZABLE con
// shipment.dispatched en la misma tx.
func (s *Service) DispatchShipment(ctx context.Context, owner, shipmentID uuid.UUID, in ShipmentDispatch) (Shipment, error) {
	simNow := s.sim.Now(ctx)

	err := db.RunSerializable(ctx, s.pool, func(tx pgx.Tx) error {
		r := s.repo.WithTx(tx)

		sh, err := r.LockShipmentForDispatch(ctx, shipmentID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("%w (%s)", ErrShipmentNotFound, shipmentID)
		case err != nil:
			return fmt.Errorf("world/fleet: bloqueando el cargamento %s: %w", shipmentID, err)
		}
		// Autorización del despacho: un cargamento de CCRI de bienes lo despacha su
		// dueño (el vendedor); un cargamento de CCRI-Flete lo despacha el
		// TRANSPORTISTA (dueño=cargador, pero la carga viaja en el vehículo del
		// transportista). world resuelve el transportista leyendo el freight_contract
		// (cross-schema), sin importar internal/contracts.
		authorized := sh.OwnerAccountID
		if sh.FreightContractID != nil {
			carrier, cerr := r.GetFreightCarrier(ctx, *sh.FreightContractID)
			switch {
			case errors.Is(cerr, pgx.ErrNoRows):
				return fmt.Errorf("%w: el flete %s del cargamento no existe", ErrValidation, *sh.FreightContractID)
			case cerr != nil:
				return fmt.Errorf("world/fleet: consultando el transportista del flete %s: %w", *sh.FreightContractID, cerr)
			}
			authorized = carrier
		}
		if authorized != owner {
			return fmt.Errorf("%w (cargamento %s)", ErrForbidden, shipmentID)
		}
		// Despachable en el primer tramo (in_warehouse) o en un tramo posterior de
		// una ruta multimodal (at_terminal, tras un transbordo). GDD 7.3.
		if sh.Status != string(sqlcgen.WorldShipmentStatusInWarehouse) &&
			sh.Status != string(sqlcgen.WorldShipmentStatusAtTerminal) {
			return fmt.Errorf("%w (estado %s)", ErrShipmentNotDispatchable, sh.Status)
		}
		if sh.AtNodeID == nil {
			return fmt.Errorf("%w: el cargamento no está en un nodo", ErrShipmentNotDispatchable)
		}
		if sh.DestinationNodeID == nil {
			return fmt.Errorf("%w: el cargamento no tiene nodo destino registrado", ErrValidation)
		}
		originNode := *sh.AtNodeID
		destNode := *sh.DestinationNodeID

		v, err := r.LockVehicle(ctx, in.VehicleID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("%w (%s)", ErrVehicleNotFound, in.VehicleID)
		case err != nil:
			return fmt.Errorf("world/fleet: bloqueando el vehículo %s: %w", in.VehicleID, err)
		}
		if v.OwnerAccountID != owner {
			return fmt.Errorf("%w (vehículo %s)", ErrForbidden, in.VehicleID)
		}
		if v.Status == string(sqlcgen.WorldVehicleStatusSealed) {
			return fmt.Errorf("%w (%s)", ErrVehicleSealed, in.VehicleID)
		}
		if v.Status != string(sqlcgen.WorldVehicleStatusIdle) {
			return fmt.Errorf("%w (estado %s)", ErrVehicleNotIdle, v.Status)
		}
		if v.AtNodeID == nil || *v.AtNodeID != originNode {
			return fmt.Errorf("%w: el vehículo no está en el nodo del cargamento", ErrValidation)
		}

		vt, err := r.GetVehicleType(ctx, v.VehicleTypeID)
		if err != nil {
			return fmt.Errorf("world/fleet: consultando el tipo del vehículo %s: %w", in.VehicleID, err)
		}

		// Modo: un vehículo SOLO circula por enlaces de SU modo (un tren no va por
		// road). El despacho es POR TRAMO DE UN SOLO MODO (GDD 7.3): la ruta no puede
		// contener tramos de otro modo. Una ruta multimodal se recorre en varios
		// despachos, uno por modo, con transbordo en terminal entre ellos.
		wrong, err := r.CountRouteLegsWrongMode(ctx, in.RouteID, vt.Mode)
		if err != nil {
			return err
		}
		if wrong > 0 {
			return fmt.Errorf("%w (vehículo %s modo %s)", ErrWrongVehicleMode, in.VehicleID, vt.Mode)
		}

		// Ruta: propiedad y extremos (empieza en el nodo del cargamento, termina en
		// el destino del contrato O en una terminal de transbordo intermedia).
		routeOwner, _, err := r.GetRouteOwnerActive(ctx, in.RouteID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("%w (%s)", ErrNotFound, in.RouteID)
		case err != nil:
			return fmt.Errorf("world/fleet: consultando la ruta %s: %w", in.RouteID, err)
		}
		if routeOwner != owner {
			return fmt.Errorf("%w (ruta %s)", ErrForbidden, in.RouteID)
		}
		first, err := r.GetRouteFirstSegment(ctx, in.RouteID)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: la ruta %s no tiene tramos", ErrValidation, in.RouteID)
		} else if err != nil {
			return fmt.Errorf("world/fleet: consultando el primer segmento de la ruta %s: %w", in.RouteID, err)
		}
		ep, err := r.GetRouteEndpoints(ctx, in.RouteID)
		if err != nil {
			return fmt.Errorf("world/fleet: consultando los extremos de la ruta %s: %w", in.RouteID, err)
		}
		if ep.FirstFromNode != originNode {
			return fmt.Errorf("%w: la ruta no empieza en el nodo del cargamento", ErrValidation)
		}
		// El tramo termina en el destino final (entrega) O en una terminal de
		// transbordo intermodal (el cargamento seguirá en otro modo, GDD 7.3). Una
		// ruta que acaba en un nodo cualquiera dejaría el cargamento varado.
		if ep.LastToNode != destNode {
			if _, terr := r.GetTerminalByNode(ctx, ep.LastToNode); errors.Is(terr, pgx.ErrNoRows) {
				return fmt.Errorf("%w: la ruta no termina en el destino del contrato ni en una terminal de transbordo", ErrValidation)
			} else if terr != nil {
				return fmt.Errorf("world/fleet: consultando la terminal del nodo final %s: %w", ep.LastToNode, terr)
			}
		}

		// Capacidad de carga: cargo_capacity >= quantity * unit_volume.
		unitVolume, err := r.GetProductUnitVolume(ctx, sh.ProductID)
		if err != nil {
			return fmt.Errorf("world/fleet: consultando el volumen del producto %s: %w", sh.ProductID, err)
		}
		needVol, err := requiredVolume(sh.Quantity, unitVolume)
		if err != nil {
			return err
		}
		if vt.CargoCapacity < needVol {
			return fmt.Errorf("%w: la capacidad del vehículo (%d) no cubre el volumen del cargamento (%d)", ErrValidation, vt.CargoCapacity, needVol)
		}

		// Puerta de tiempo de transbordo: un cargamento at_terminal no puede
		// re-despacharse hasta consumir el tiempo de transbordo de la terminal
		// (transshipment_per_hour · volumen; GDD 7.3). Si la COLA de transbordo ya lo
		// sirvió (transship_ready_at_sim fijado por sweepTransship), esa es la puerta
		// real —refleja su posición en la cola con la prioridad de slots—; si aún no
		// (la cola no corrió, o un fixture directo), se recae en el cálculo aislado
		// desde la llegada (updated_at_sim), retrocompatible.
		if sh.Status == string(sqlcgen.WorldShipmentStatusAtTerminal) {
			if sh.TransshipReadyAtSim != nil {
				if int64(simNow) < *sh.TransshipReadyAtSim {
					return &TransshipmentPendingError{ReadyAtSim: *sh.TransshipReadyAtSim, NowSim: int64(simNow)}
				}
			} else {
				term, terr := r.GetTerminalByNode(ctx, originNode)
				switch {
				case errors.Is(terr, pgx.ErrNoRows):
					// Defensivo: at_terminal en un nodo sin terminal — sin tasa, sin espera.
				case terr != nil:
					return fmt.Errorf("world/fleet: consultando la terminal del nodo %s: %w", originNode, terr)
				default:
					readyAt := sh.UpdatedAtSim + transshipmentSeconds(needVol, term.TransshipmentPerHour)
					if int64(simNow) < readyAt {
						return &TransshipmentPendingError{ReadyAtSim: readyAt, NowSim: int64(simNow)}
					}
				}
			}
		}

		// Combustible: alcanza la distancia total de la ruta.
		fuelNeeded := fuelForDistance(vt.FuelPer100km, ep.TotalLengthM)
		if v.Fuel < fuelNeeded {
			return fmt.Errorf("%w: el combustible del vehículo (%d) no cubre la distancia de la ruta (necesita %d)", ErrValidation, v.Fuel, fuelNeeded)
		}

		// Efecto: vehículo in_transit sobre el primer segmento; cargamento a bordo.
		af := advanceFn{
			BaseSpeedKmh:  minInt32(vt.SpeedKmh, first.BaseSpeedKmh),
			CongestionEma: first.CongestionEma,
			LengthM:       first.LengthM,
			Dir:           1,
		}
		afBytes, err := af.marshal()
		if err != nil {
			return err
		}
		if err := r.DispatchVehicle(ctx, dispatchVehicleParams{
			ID: in.VehicleID, RouteID: in.RouteID, OnSegment: first.SegmentID, AdvanceFn: afBytes, SimNow: simNow,
		}); err != nil {
			return err
		}
		if err := r.DispatchShipment(ctx, shipmentID, in.VehicleID, simNow); err != nil {
			return err
		}
		return outbox.Emit(ctx, tx, int64(simNow), AggregateShipment, shipmentID, EventShipmentDispatched, ShipmentDispatchedPayload{
			ShipmentID: shipmentID.String(), VehicleID: in.VehicleID.String(), RouteID: in.RouteID.String(),
			ContractID: uuidOrEmpty(sh.ContractID), ProductID: sh.ProductID.String(), Quantity: fixed(sh.Quantity),
			OriginNodeID: originNode.String(), DestinationNodeID: destNode.String(), DispatchedAtSim: int64(simNow),
		})
	})
	if err != nil {
		return Shipment{}, mapLedgerError(err)
	}
	s.dispatched.Inc()
	s.logger.Info("cargamento despachado",
		slog.String("shipment_id", shipmentID.String()), slog.String("owner", owner.String()),
		slog.String("vehicle_id", in.VehicleID.String()), slog.String("route_id", in.RouteID.String()))

	sh, err := s.repo.GetShipment(ctx, shipmentID)
	if err != nil {
		return Shipment{}, fmt.Errorf("world/fleet: releyendo el cargamento despachado %s: %w", shipmentID, err)
	}
	return sh, nil
}

// ─── Asientos del ledger ──────────────────────────────────────────────────────

// chargeToSink cobra amount (cash del dueño → sink) con un asiento maintenance
// (el precio de compra de un vehículo es un sink del banco central, GDD 5.5/8).
// amount <= 0 no genera asiento. FundsError (422) si la caja no cubre el coste.
func (s *Service) chargeToSink(ctx context.Context, r *Repo, owner, reference uuid.UUID, amount int64, simNow simtime.SimTime, description string) error {
	if amount <= 0 {
		return nil
	}
	cash, err := r.GetCashAccount(ctx, owner)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return &FundsError{Required: amount, Available: 0}
	case err != nil:
		return fmt.Errorf("world/fleet: consultando la caja de %s: %w", owner, err)
	case cash.Balance < amount:
		return &FundsError{Required: amount, Available: cash.Balance}
	}
	sink, err := r.GetSinkAccount(ctx)
	if err != nil {
		return fmt.Errorf("world/fleet: localizando la cuenta sink del banco central: %w", err)
	}
	return r.PostLedgerTransaction(ctx, sqlcgen.LedgerTransactionKindMaintenance, simNow, reference, description, []entryAmount{
		{AccountID: cash.ID, Amount: -amount},
		{AccountID: sink.ID, Amount: amount},
	})
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func normalizeLimit(limit int) int32 {
	switch {
	case limit <= 0:
		return DefaultPageLimit
	case int32(limit) > MaxPageLimit:
		return MaxPageLimit
	default:
		return int32(limit)
	}
}

func cursorID(cursor string) (*uuid.UUID, error) {
	if cursor == "" {
		return nil, nil
	}
	id, err := decodeCursor(cursor)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func validLinkMode(m string) bool {
	switch sqlcgen.WorldLinkMode(m) {
	case sqlcgen.WorldLinkModeRoad, sqlcgen.WorldLinkModeRail, sqlcgen.WorldLinkModeSea:
		return true
	}
	return false
}

func validVehicleStatus(s string) bool {
	switch sqlcgen.WorldVehicleStatus(s) {
	case sqlcgen.WorldVehicleStatusIdle, sqlcgen.WorldVehicleStatusLoading, sqlcgen.WorldVehicleStatusInTransit,
		sqlcgen.WorldVehicleStatusUnloading, sqlcgen.WorldVehicleStatusBroken,
		sqlcgen.WorldVehicleStatusInMaintenance, sqlcgen.WorldVehicleStatusSealed:
		return true
	}
	return false
}

func validShipmentStatus(s string) bool {
	switch sqlcgen.WorldShipmentStatus(s) {
	case sqlcgen.WorldShipmentStatusInWarehouse, sqlcgen.WorldShipmentStatusInTransit,
		sqlcgen.WorldShipmentStatusAtTerminal, sqlcgen.WorldShipmentStatusDelivered,
		sqlcgen.WorldShipmentStatusReleasedInSitu:
		return true
	}
	return false
}

func minInt32(a, b int32) int32 {
	if a < b {
		return a
	}
	return b
}

func uuidOrEmpty(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

// mapLedgerError traduce violaciones de invariantes de la BD (carreras resueltas
// por constraint) a errores tipados del subpaquete.
func mapLedgerError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == sqlstateCheckViolation && pgErr.ConstraintName == constraintNonNegative:
			return fmt.Errorf("%w: %s", ErrInsufficientFunds, pgErr.Message)
		case pgErr.Code == sqlstateFKViolation:
			return fmt.Errorf("%w: referencia inexistente (%s)", ErrValidation, pgErr.ConstraintName)
		}
	}
	return err
}
