// Package balancer es el agente económico de las ciudades dentro del motor
// (ADR-IMPL-10): recalcula curvas de demanda, publica solicitudes de compra
// por el MISMO camino contable que la API pública, y ejecuta los cargos
// diarios (salarios, mantenimiento, canon) y el nivel urbano.
package balancer

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"imperio/engine/internal/core"
	"imperio/engine/internal/db"
	"imperio/engine/internal/ledger"
	"imperio/engine/internal/outbox"
)

const (
	emaDecay        = 0.98       // decay de supply_ema por pasada (cada ~24 min sim)
	emaFloor        = 0.5        // suelo del EMA (nunca cero)
	cityCashTarget  = 10_000_000 // top-up del pre-fondeo urbano
	cityDeliverySim = 86400      // plazo de las compras de ciudad: 1 día sim
)

type Processor struct {
	Pool       *pgxpool.Pool
	Bank       core.BankRefs
	Log        *slog.Logger
	DrawWindow time.Duration // ventana de sorteo en tiempo real (45 s dev)

	lastSimDay int64 // frontera diaria ya procesada (memoria de proceso)
	daySeeded  bool
}

// InitDay fija la frontera diaria al arrancar para no recobrar el día en curso
// tras un reinicio (los cargos diarios no persisten marcador propio en v1).
func (p *Processor) InitDay(simNow int64) {
	p.lastSimDay = simNow / 86400
	p.daySeeded = true
}

// RunPeriodic ejecuta las pasadas de cada 60 ticks: decay+precio de las curvas
// de demanda y compras de ciudad.
func (p *Processor) RunPeriodic(ctx context.Context, simNow int64) {
	p.updatePrices(ctx, simNow)
	p.cityPurchases(ctx, simNow)
}

// RunDaily ejecuta los cargos al cruzar cada frontera de día sim.
func (p *Processor) RunDaily(ctx context.Context, simNow int64) {
	if !p.daySeeded {
		p.InitDay(simNow)
		return
	}
	day := simNow / 86400
	if day == p.lastSimDay {
		return
	}
	p.lastSimDay = day
	p.wagesAndMaintenance(ctx, simNow)
	p.concessions(ctx, simNow)
	p.cityLevels(ctx, simNow)
}

// --- precios -----------------------------------------------------------------

func (p *Processor) updatePrices(ctx context.Context, simNow int64) {
	err := db.RunSerializable(ctx, p.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT cd.city_id, cd.product_id, cd.d0_per_sim_day, cd.supply_ema,
			       pr.base_price, pr.price_floor, pr.price_ceiling, pr.class
			  FROM world.city_demand cd
			  JOIN world.cities c ON c.id = cd.city_id
			  JOIN world.products pr ON pr.id = cd.product_id
			 WHERE cd.unlocked_at_level <= c.level
			 FOR UPDATE OF cd`)
		if err != nil {
			return err
		}
		type row struct {
			city, product         uuid.UUID
			d0, base, floor, ceil int64
			ema                   float64
			class                 string
		}
		var all []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.city, &r.product, &r.d0, &r.ema, &r.base, &r.floor, &r.ceil, &r.class); err != nil {
				rows.Close()
				return err
			}
			all = append(all, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, r := range all {
			ema := r.ema * emaDecay
			if ema < emaFloor {
				ema = emaFloor
			}
			price, saturation := core.CityPrice(r.base, r.floor, r.ceil, r.d0, ema, r.class)
			if _, err := tx.Exec(ctx, `
				UPDATE world.city_demand
				   SET supply_ema = $3, current_price = $4, saturation_factor = $5, updated_at_sim = $6
				 WHERE city_id = $1 AND product_id = $2`,
				r.city, r.product, ema, price, saturation, simNow); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		p.Log.Error("balancer: precios", "err", err)
	}
}

// --- compras de ciudad ---------------------------------------------------------

func (p *Processor) cityPurchases(ctx context.Context, simNow int64) {
	rows, err := p.Pool.Query(ctx, `
		SELECT c.id, c.account_id, cd.product_id, cd.d0_per_sim_day, cd.current_price
		  FROM world.cities c
		  JOIN world.city_demand cd ON cd.city_id = c.id
		 WHERE cd.unlocked_at_level <= c.level AND cd.d0_per_sim_day > 0`)
	if err != nil {
		p.Log.Error("balancer: query compras", "err", err)
		return
	}
	type demand struct {
		city, account, product uuid.UUID
		d0, price              int64
	}
	var demands []demand
	for rows.Next() {
		var d demand
		if err := rows.Scan(&d.city, &d.account, &d.product, &d.d0, &d.price); err != nil {
			rows.Close()
			p.Log.Error("balancer: scan compras", "err", err)
			return
		}
		demands = append(demands, d)
	}
	rows.Close()
	for _, d := range demands {
		d := d
		err := db.RunSerializable(ctx, p.Pool, func(tx pgx.Tx) error {
			return p.publishCityBuy(ctx, tx, d.city, d.account, d.product, d.d0, d.price, simNow)
		})
		if err != nil {
			p.Log.Error("balancer: compra de ciudad", "city", d.city, "product", d.product, "err", err)
		}
	}
}

func (p *Processor) publishCityBuy(ctx context.Context, tx pgx.Tx, cityID, cityAccount, product uuid.UUID, d0, price, simNow int64) error {
	// Demanda pendiente de cubrir: publicaciones buy vivas + contratos activos.
	var pubOutstanding, contractOutstanding int64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(quantity_remaining), 0) FROM ledger.publications
		 WHERE publisher_account_id = $1 AND kind = 'buy' AND product_id = $2
		   AND status IN ('draw_window','open','micro_window')`, cityAccount, product).Scan(&pubOutstanding); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(quantity_agreed - quantity_delivered), 0) FROM ledger.contracts
		 WHERE buyer_account_id = $1 AND product_id = $2 AND status = 'active'`,
		cityAccount, product).Scan(&contractOutstanding); err != nil {
		return err
	}
	outstanding := pubOutstanding + contractOutstanding
	if outstanding >= d0/2 {
		return nil
	}
	qty := d0 - outstanding
	if qty <= 0 || price <= 0 {
		return nil
	}
	var gateNode uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT id FROM world.network_nodes WHERE city_id = $1 AND kind = 'city_gate' LIMIT 1`,
		cityID).Scan(&gateNode)
	if errors.Is(err, pgx.ErrNoRows) {
		p.Log.Warn("balancer: ciudad sin city_gate", "city", cityID)
		return nil
	}
	if err != nil {
		return err
	}

	pubID, err := uuid.NewV7()
	if err != nil {
		return err
	}
	cost := qty * price
	cash, err := ledger.CashAccount(ctx, tx, cityAccount)
	if err != nil {
		return err
	}
	balance, err := ledger.Balance(ctx, tx, cash)
	if err != nil {
		return err
	}
	if balance < cost {
		// Pre-fondeo por el banco central: top-up hasta 10M (o hasta el coste
		// si excede 10M) con emisión explícita 'seed_capital'.
		target := int64(cityCashTarget)
		if cost > target {
			target = cost
		}
		topUp := target - balance
		if _, err := ledger.PostTx(ctx, tx, "seed_capital", simNow, &cityAccount,
			"Top-up de pre-fondeo urbano", []ledger.Entry{
				{AccountID: p.Bank.EmissionMoneyID, Amount: -topUp},
				{AccountID: cash, Amount: topUp},
			}); err != nil {
			return err
		}
	}
	// Mismo patrón contable que el gateway: escrow espejo de la publicación y
	// asiento 'publication_lock' con el 100% del pago.
	escrow, err := ledger.NewMirrorAccount(ctx, tx, "escrow", cityAccount, nil, nil, pubID)
	if err != nil {
		return err
	}
	if _, err := ledger.PostTx(ctx, tx, "publication_lock", simNow, &pubID,
		"Escrow de solicitud de compra urbana", []ledger.Entry{
			{AccountID: cash, Amount: -cost},
			{AccountID: escrow, Amount: cost},
		}); err != nil {
		return err
	}
	minLot := qty / 10
	if minLot < 1 {
		minLot = 1
	}
	windowCloses := time.Now().Add(p.DrawWindow)
	if _, err := tx.Exec(ctx, `
		INSERT INTO ledger.publications
		    (id, kind, publisher_account_id, channel, product_id, quantity_total,
		     quantity_remaining, unit_price, min_lot, destination_node_id,
		     delivery_sim_seconds, status, window_closes_at, escrow_account_id, published_at_sim)
		VALUES ($1, 'buy', $2, 'board', $3, $4, $4, $5, $6, $7, $8, 'draw_window', $9, $10, $11)`,
		pubID, cityAccount, product, qty, price, minLot, gateNode,
		cityDeliverySim, windowCloses, escrow, simNow); err != nil {
		return err
	}
	entity, err := outbox.PublicationEntity(ctx, tx, pubID)
	if err != nil {
		return err
	}
	return outbox.Insert(ctx, tx, "publication", pubID, "publication.created", simNow,
		outbox.Payload(entity, nil, nil))
}
