package outbox

// Constructores de DTOs REST (specs/openapi.yaml) para los payloads de eventos.
// Convenciones transversales: dinero/stock como strings decimales, sim-time
// como enteros, timestamps RFC3339, geometrías GeoJSON.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"imperio/engine/internal/core"
)

func fmtTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func put(m map[string]any, key string, v any) {
	// Añade solo valores no nulos (los DTO omiten los opcionales ausentes).
	switch x := v.(type) {
	case *uuid.UUID:
		if x != nil {
			m[key] = x.String()
		}
	case *int64:
		if x != nil {
			m[key] = *x
		}
	case *int32:
		if x != nil {
			m[key] = *x
		}
	case *string:
		if x != nil {
			m[key] = *x
		}
	case *time.Time:
		if x != nil {
			m[key] = fmtTime(*x)
		}
	default:
		if v != nil {
			m[key] = v
		}
	}
}

// PublicationEntity — DTO Publication.
func PublicationEntity(ctx context.Context, q core.Querier, id uuid.UUID) (map[string]any, error) {
	var (
		kind, channel, status                     string
		publisher                                 uuid.UUID
		counterparty, productID, originID, destID *uuid.UUID
		qtyTotal, qtyRemaining, unitPrice, minLot int64
		deliverySimSeconds, publishedAtSim        int64
		windowClosesAt, cancelCooldownUntil       *time.Time
		createdAt                                 time.Time
	)
	err := q.QueryRow(ctx, `
		SELECT kind, publisher_account_id, channel, counterparty_account_id, product_id,
		       quantity_total, quantity_remaining, unit_price, min_lot,
		       origin_node_id, destination_node_id, delivery_sim_seconds, status,
		       window_closes_at, cancel_cooldown_until, published_at_sim, created_at
		  FROM ledger.publications WHERE id = $1`, id).Scan(
		&kind, &publisher, &channel, &counterparty, &productID,
		&qtyTotal, &qtyRemaining, &unitPrice, &minLot,
		&originID, &destID, &deliverySimSeconds, &status,
		&windowClosesAt, &cancelCooldownUntil, &publishedAtSim, &createdAt)
	if err != nil {
		return nil, fmt.Errorf("dto publication %s: %w", id, err)
	}
	m := map[string]any{
		"id":                   id.String(),
		"kind":                 kind,
		"publisher_account_id": publisher.String(),
		"channel":              channel,
		"quantity_total":       core.Money(qtyTotal),
		"quantity_remaining":   core.Money(qtyRemaining),
		"unit_price":           core.Money(unitPrice),
		"min_lot":              core.Money(minLot),
		"delivery_sim_seconds": deliverySimSeconds,
		"status":               status,
		"published_at_sim":     publishedAtSim,
		"created_at":           fmtTime(createdAt),
	}
	put(m, "counterparty_account_id", counterparty)
	put(m, "product_id", productID)
	put(m, "origin_node_id", originID)
	put(m, "destination_node_id", destID)
	put(m, "window_closes_at", windowClosesAt)
	put(m, "cancel_cooldown_until", cancelCooldownUntil)
	return m, nil
}

// ContractEntity — DTO Contract.
func ContractEntity(ctx context.Context, q core.Querier, id uuid.UUID) (map[string]any, error) {
	var (
		publicationID                      *uuid.UUID
		channel, status                    string
		buyer, seller, product             uuid.UUID
		origin, dest                       uuid.UUID
		qtyAgreed, qtyDelivered, unitPrice int64
		deadlineSim, confirmedAtSim        int64
		fillBp                             *int32
		settledAtSim                       *int64
		stockAcc, guarAcc, escrowAcc       uuid.UUID
		createdAt                          time.Time
	)
	err := q.QueryRow(ctx, `
		SELECT publication_id, channel, buyer_account_id, seller_account_id, product_id,
		       quantity_agreed, quantity_delivered, unit_price,
		       origin_node_id, destination_node_id, deadline_sim, status, fill_bp,
		       stock_reserve_account_id, seller_guarantee_account_id, escrow_account_id,
		       confirmed_at_sim, settled_at_sim, created_at
		  FROM ledger.contracts WHERE id = $1`, id).Scan(
		&publicationID, &channel, &buyer, &seller, &product,
		&qtyAgreed, &qtyDelivered, &unitPrice,
		&origin, &dest, &deadlineSim, &status, &fillBp,
		&stockAcc, &guarAcc, &escrowAcc,
		&confirmedAtSim, &settledAtSim, &createdAt)
	if err != nil {
		return nil, fmt.Errorf("dto contract %s: %w", id, err)
	}
	m := map[string]any{
		"id":                          id.String(),
		"channel":                     channel,
		"buyer_account_id":            buyer.String(),
		"seller_account_id":           seller.String(),
		"product_id":                  product.String(),
		"quantity_agreed":             core.Money(qtyAgreed),
		"quantity_delivered":          core.Money(qtyDelivered),
		"unit_price":                  core.Money(unitPrice),
		"origin_node_id":              origin.String(),
		"destination_node_id":         dest.String(),
		"deadline_sim":                deadlineSim,
		"status":                      status,
		"stock_reserve_account_id":    stockAcc.String(),
		"seller_guarantee_account_id": guarAcc.String(),
		"escrow_account_id":           escrowAcc.String(),
		"confirmed_at_sim":            confirmedAtSim,
		"created_at":                  fmtTime(createdAt),
	}
	put(m, "publication_id", publicationID)
	put(m, "fill_bp", fillBp)
	put(m, "settled_at_sim", settledAtSim)
	return m, nil
}

// AcceptanceEntity — DTO Acceptance (contract_id se pasa aparte porque la
// tabla no lo persiste; lo conoce el sorteo que acaba de crear el contrato).
func AcceptanceEntity(ctx context.Context, q core.Querier, id uuid.UUID, contractID *uuid.UUID) (map[string]any, error) {
	var (
		publicationID, acceptor uuid.UUID
		quantity, served        int64
		status                  string
		drawOrder               *int32
		acceptedAt              time.Time
		resolvedAt              *time.Time
	)
	err := q.QueryRow(ctx, `
		SELECT publication_id, acceptor_account_id, quantity, quantity_served, status,
		       draw_order, accepted_at, resolved_at
		  FROM ledger.publication_acceptances WHERE id = $1`, id).Scan(
		&publicationID, &acceptor, &quantity, &served, &status, &drawOrder, &acceptedAt, &resolvedAt)
	if err != nil {
		return nil, fmt.Errorf("dto acceptance %s: %w", id, err)
	}
	m := map[string]any{
		"id":                  id.String(),
		"publication_id":      publicationID.String(),
		"acceptor_account_id": acceptor.String(),
		"quantity":            core.Money(quantity),
		"quantity_served":     core.Money(served),
		"status":              status,
		"accepted_at":         fmtTime(acceptedAt),
	}
	put(m, "draw_order", drawOrder)
	put(m, "resolved_at", resolvedAt)
	put(m, "contract_id", contractID)
	return m, nil
}

// BatchEntity — DTO ProductionBatch.
func BatchEntity(ctx context.Context, q core.Querier, id uuid.UUID) (map[string]any, error) {
	var (
		buildingID, recipeID uuid.UUID
		queued, done, pos    int32
		status               string
		startedAtSim         *int64
	)
	err := q.QueryRow(ctx, `
		SELECT building_id, recipe_id, batches_queued, batches_done, status, queue_position, started_at_sim
		  FROM world.production_batches WHERE id = $1`, id).Scan(
		&buildingID, &recipeID, &queued, &done, &status, &pos, &startedAtSim)
	if err != nil {
		return nil, fmt.Errorf("dto batch %s: %w", id, err)
	}
	m := map[string]any{
		"id":             id.String(),
		"building_id":    buildingID.String(),
		"recipe_id":      recipeID.String(),
		"batches_queued": queued,
		"batches_done":   done,
		"status":         status,
		"queue_position": pos,
	}
	put(m, "started_at_sim", startedAtSim)
	return m, nil
}

// BuildingEntity — DTO Building. Devuelve además el centroide (para la
// location de los eventos espaciales building.status_changed).
func BuildingEntity(ctx context.Context, q core.Querier, id uuid.UUID) (map[string]any, *Location, error) {
	var (
		owner, region, concession, btype uuid.UUID
		footprintJSON                    string
		level, conditionPct              int32
		status                           string
		activeRecipe                     *uuid.UUID
		fuelStock, updatedAtSim          int64
		createdAt                        time.Time
		lon, lat                         float64
	)
	err := q.QueryRow(ctx, `
		SELECT owner_account_id, region_id, concession_id, building_type_id,
		       ST_AsGeoJSON(footprint), level, status, active_recipe_id, condition_pct,
		       fuel_stock, updated_at_sim, created_at,
		       ST_X(ST_Centroid(footprint)), ST_Y(ST_Centroid(footprint))
		  FROM world.buildings WHERE id = $1`, id).Scan(
		&owner, &region, &concession, &btype,
		&footprintJSON, &level, &status, &activeRecipe, &conditionPct,
		&fuelStock, &updatedAtSim, &createdAt, &lon, &lat)
	if err != nil {
		return nil, nil, fmt.Errorf("dto building %s: %w", id, err)
	}
	m := map[string]any{
		"id":               id.String(),
		"owner_account_id": owner.String(),
		"region_id":        region.String(),
		"concession_id":    concession.String(),
		"building_type_id": btype.String(),
		"footprint":        json.RawMessage(footprintJSON),
		"level":            level,
		"status":           status,
		"condition_pct":    conditionPct,
		"fuel_stock":       core.Money(fuelStock),
		"updated_at_sim":   updatedAtSim,
		"created_at":       fmtTime(createdAt),
	}
	put(m, "active_recipe_id", activeRecipe)
	return m, &Location{Lon: lon, Lat: lat}, nil
}

// VehicleEntity — DTO Vehicle con posición derivada. simNow permite derivar
// el progreso del segmento (analítico, GDD 1.1). Devuelve la location.
func VehicleEntity(ctx context.Context, q core.Querier, id uuid.UUID, simNow int64) (map[string]any, *Location, error) {
	var (
		vtype, owner       uuid.UUID
		status             string
		wearPct            int32
		fuel, updatedAtSim int64
		routeID            *uuid.UUID
		routeLegIndex      *int32
		atNode, onSegment  *uuid.UUID
		segmentEnteredSim  *int64
		advanceFnRaw       []byte
		repairUntilSim     *int64
	)
	err := q.QueryRow(ctx, `
		SELECT vehicle_type_id, owner_account_id, status, wear_pct, fuel, route_id,
		       route_leg_index, at_node_id, on_segment_id, segment_entered_sim,
		       advance_fn, repair_until_sim, updated_at_sim
		  FROM world.vehicles WHERE id = $1`, id).Scan(
		&vtype, &owner, &status, &wearPct, &fuel, &routeID,
		&routeLegIndex, &atNode, &onSegment, &segmentEnteredSim,
		&advanceFnRaw, &repairUntilSim, &updatedAtSim)
	if err != nil {
		return nil, nil, fmt.Errorf("dto vehicle %s: %w", id, err)
	}

	position := map[string]any{}
	var loc *Location
	if atNode != nil {
		position["at_node_id"] = atNode.String()
		if l, err := NodeLocation(ctx, q, *atNode); err == nil {
			loc = l
			position["location"] = map[string]any{"type": "Point", "coordinates": []float64{l.Lon, l.Lat}}
		}
	} else if onSegment != nil {
		position["on_segment_id"] = onSegment.String()
		// Progreso analítico del segmento: (sim_now − t_entrada) / duración.
		progress := 0.0
		if segmentEnteredSim != nil && len(advanceFnRaw) > 0 {
			var fn core.AdvanceFn
			if json.Unmarshal(advanceFnRaw, &fn) == nil && fn.DurationSimSeconds > 0 {
				progress = float64(simNow-*segmentEnteredSim) / float64(fn.DurationSimSeconds)
			}
		}
		if progress < 0 {
			progress = 0
		}
		if progress > 1 {
			progress = 1
		}
		position["segment_progress_pct"] = progress * 100
		var lon, lat float64
		if err := q.QueryRow(ctx,
			`SELECT ST_X(ST_LineInterpolatePoint(portion, $2)), ST_Y(ST_LineInterpolatePoint(portion, $2))
			   FROM world.link_segments WHERE id = $1`, *onSegment, progress).Scan(&lon, &lat); err == nil {
			loc = &Location{Lon: lon, Lat: lat}
			position["location"] = map[string]any{"type": "Point", "coordinates": []float64{lon, lat}}
		}
	}

	m := map[string]any{
		"id":               id.String(),
		"vehicle_type_id":  vtype.String(),
		"owner_account_id": owner.String(),
		"status":           status,
		"wear_pct":         wearPct,
		"fuel":             core.Money(fuel),
		"position":         position,
		"updated_at_sim":   updatedAtSim,
	}
	put(m, "route_id", routeID)
	put(m, "route_leg_index", routeLegIndex)
	put(m, "repair_until_sim", repairUntilSim)
	return m, loc, nil
}

// CityEntity — DTO City (+ location para eventos espaciales city.*).
func CityEntity(ctx context.Context, q core.Querier, id uuid.UUID) (map[string]any, *Location, error) {
	var (
		region, account  uuid.UUID
		name             string
		level            int32
		population       int64
		supplyIndex      float64
		influenceRadiusM int32
		baseSalary       int64
		lon, lat         float64
	)
	err := q.QueryRow(ctx, `
		SELECT region_id, account_id, name, level, population, supply_index,
		       influence_radius_m, base_salary, ST_X(location), ST_Y(location)
		  FROM world.cities WHERE id = $1`, id).Scan(
		&region, &account, &name, &level, &population, &supplyIndex,
		&influenceRadiusM, &baseSalary, &lon, &lat)
	if err != nil {
		return nil, nil, fmt.Errorf("dto city %s: %w", id, err)
	}
	m := map[string]any{
		"id":                 id.String(),
		"region_id":          region.String(),
		"account_id":         account.String(),
		"name":               name,
		"location":           map[string]any{"type": "Point", "coordinates": []float64{lon, lat}},
		"level":              level,
		"population":         population,
		"supply_index":       supplyIndex,
		"influence_radius_m": influenceRadiusM,
		"base_salary":        core.Money(baseSalary),
	}
	return m, &Location{Lon: lon, Lat: lat}, nil
}

// CityDemandEntity — DTO CityDemand.
func CityDemandEntity(ctx context.Context, q core.Querier, cityID, productID uuid.UUID) (map[string]any, error) {
	var (
		d0           int64
		saturation   float64
		currentPrice int64
		unlockedAt   int32
		updatedAtSim int64
	)
	err := q.QueryRow(ctx, `
		SELECT d0_per_sim_day, saturation_factor, current_price, unlocked_at_level, updated_at_sim
		  FROM world.city_demand WHERE city_id = $1 AND product_id = $2`, cityID, productID).Scan(
		&d0, &saturation, &currentPrice, &unlockedAt, &updatedAtSim)
	if err != nil {
		return nil, fmt.Errorf("dto city_demand %s/%s: %w", cityID, productID, err)
	}
	return map[string]any{
		"city_id":           cityID.String(),
		"product_id":        productID.String(),
		"d0_per_sim_day":    core.Money(d0),
		"saturation_factor": saturation,
		"current_price":     core.Money(currentPrice),
		"unlocked_at_level": unlockedAt,
		"updated_at_sim":    updatedAtSim,
	}, nil
}
