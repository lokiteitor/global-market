package balancer

import (
	"context"
	"fmt"
	"log/slog"
	"math/big"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lokiteitor/global-market/backend/internal/platform/db"
)

// publishCityBuys mantiene UNA solicitud de compra viva por (ciudad, producto)
// en el tablón: para cada objetivo de demanda comprueba que no haya ya una buy
// viva (dedup), PRE-FONDEA la caja de la ciudad por emisión si no cubre el escrow
// (faucet, GDD 5.5) y publica la buy por el PORT (camino estándar del Contract
// Service). Best-effort por producto: un fallo se registra y no bloquea al resto.
func (w *DemandWorker) publishCityBuys(ctx context.Context, res recalcResult) {
	if len(res.Buys) == 0 {
		return
	}
	if !res.HasDistribution {
		w.logger.Warn("balancer: la ciudad no tiene centro de distribución: no se publican buys",
			slog.String("city_id", res.CityID.String()), slog.String("city", res.Name))
		return
	}
	for _, b := range res.Buys {
		if err := w.publishOneBuy(ctx, res, b); err != nil {
			w.logger.Warn("balancer: no se pudo publicar la buy de ciudad",
				slog.String("city_id", res.CityID.String()),
				slog.String("product_id", b.ProductID.String()),
				slog.Any("error", err))
		}
	}
}

// publishOneBuy publica (si procede) una solicitud de compra de la ciudad para un
// producto: dedup, pre-fondeo y llamada al PORT.
func (w *DemandWorker) publishOneBuy(ctx context.Context, res recalcResult, b cityBuyTarget) error {
	live, err := w.repo.CountLiveCityBuys(ctx, res.CityAccountID, b.ProductID)
	if err != nil {
		return err
	}
	if live > 0 {
		return nil // ya hay una buy viva para (ciudad, producto): no duplicar
	}

	value, err := mulOverflow(b.Quantity, b.UnitPrice)
	if err != nil {
		return fmt.Errorf("valor de la buy (qty %d × precio %d): %w", b.Quantity, b.UnitPrice, err)
	}

	if err := w.prefundCity(ctx, res.CityAccountID, value); err != nil {
		return fmt.Errorf("pre-fondeo de la ciudad: %w", err)
	}

	if err := w.port.CreateCityBuy(ctx, CityBuy{
		CityAccountID:      res.CityAccountID,
		ProductID:          b.ProductID,
		Quantity:           b.Quantity,
		UnitPrice:          b.UnitPrice,
		DestinationNodeID:  res.DistributionNodeID,
		DeliverySimSeconds: w.opts.CityBuyDeadlineSim,
	}); err != nil {
		return fmt.Errorf("publicando por el PORT: %w", err)
	}
	w.metrics.incBuyPublished(b.ProductID.String())
	w.logger.Debug("balancer: buy de ciudad publicada",
		slog.String("city_id", res.CityID.String()),
		slog.String("product_id", b.ProductID.String()),
		slog.Int64("quantity", b.Quantity),
		slog.Int64("unit_price", b.UnitPrice))
	return nil
}

// prefundCity garantiza que la caja de la ciudad cubre `needed` (el escrow de la
// buy). Si no llega, emite el déficit (+caja ciudad / −emisión del banco central,
// transacción seed_capital reutilizada como faucet de fondeo de ciudad, GDD 5.5:
// una ciudad nunca incumple el pago). El asiento es balanceado por dinero (la
// emisión es la única cuenta monetaria que puede quedar negativa). Todo en UNA
// transacción SERIALIZABLE. La métrica de emisión se cuenta tras el COMMIT.
func (w *DemandWorker) prefundCity(ctx context.Context, cityAccount uuid.UUID, needed int64) error {
	simNow := w.sim.Now(ctx)
	var emitted int64

	err := db.RunSerializable(ctx, w.pool, func(tx pgx.Tx) error {
		emitted = 0
		r := w.repo.WithTx(tx)
		cash, err := r.EnsureCashAccount(ctx, cityAccount)
		if err != nil {
			return err
		}
		if cash.Balance >= needed {
			return nil // ya cubre el escrow: sin emisión
		}
		deficit := needed - cash.Balance
		emission, err := r.GetEmissionAccount(ctx)
		if err != nil {
			return fmt.Errorf("cuenta de emisión del banco central: %w", err)
		}
		ref := cityAccount
		if _, err := r.PostLedgerTransaction(ctx, txKindCityFunding, simNow, ref,
			fmt.Sprintf("Fondeo de ciudad (faucet): +%d a la caja por emisión", deficit),
			[]entryAmount{
				{AccountID: cash.ID, Amount: deficit},
				{AccountID: emission.ID, Amount: -deficit},
			}); err != nil {
			return err
		}
		emitted = deficit
		return nil
	})
	if err != nil {
		return err
	}
	if emitted > 0 {
		w.metrics.addEmission(emitted)
		w.logger.Debug("balancer: emisión de fondeo de ciudad (faucet)",
			slog.String("city_account_id", cityAccount.String()),
			slog.Int64("emitted", emitted),
			slog.Int64("sim_time_at", int64(simNow)))
	}
	return nil
}

// mulOverflow multiplica a*b con guarda de desbordamiento de int64 (math/big),
// coherente con lockAmounts del Contract Service (el escrow es qty*precio).
func mulOverflow(a, b int64) (int64, error) {
	v := new(big.Int).Mul(big.NewInt(a), big.NewInt(b))
	if !v.IsInt64() {
		return 0, fmt.Errorf("desbordamiento de int64")
	}
	return v.Int64(), nil
}
