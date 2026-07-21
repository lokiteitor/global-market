package fleet

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lokiteitor/global-market/backend/internal/outbox"
	"github.com/lokiteitor/global-market/backend/internal/platform/db"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
	"github.com/lokiteitor/global-market/backend/internal/world/sqlcgen"
)

// ─── Tipos de dominio (schemas Terminal / TerminalSlot del contrato) ──────────

// Terminal es una terminal intermodal con dueño (schema Terminal, GDD 7.3).
type Terminal struct {
	ID                   uuid.UUID
	NodeID               uuid.UUID
	OwnerAccountID       uuid.UUID
	TransshipmentPerHour int32
	QueueLength          int32
	UpdatedAtSim         int64
}

// TerminalSlot es un slot de prioridad de atraque/transbordo (schema TerminalSlot).
type TerminalSlot struct {
	ID              uuid.UUID
	TerminalID      uuid.UUID
	PriorityTier    int32
	Price           int64
	HolderAccountID *uuid.UUID
	ValidUntilSim   *int64
}

// ─── Servicio: lectura y compra de slots ──────────────────────────────────────

// GetTerminal devuelve el detalle de una terminal. ErrNotFound si no existe.
func (s *Service) GetTerminal(ctx context.Context, id uuid.UUID) (Terminal, error) {
	ctx, cancel := context.WithTimeout(ctx, s.opts.QueryTimeout)
	defer cancel()
	t, err := s.repo.GetTerminal(ctx, id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Terminal{}, fmt.Errorf("%w (%s)", ErrNotFound, id)
	case err != nil:
		return Terminal{}, fmt.Errorf("world/fleet: consultando la terminal %s: %w", id, err)
	}
	return t, nil
}

// ListTerminalSlots devuelve los slots de una terminal (only_available filtra los
// que están en venta). ErrNotFound si la terminal no existe.
func (s *Service) ListTerminalSlots(ctx context.Context, terminalID uuid.UUID, onlyAvailable bool) ([]TerminalSlot, error) {
	ctx, cancel := context.WithTimeout(ctx, s.opts.QueryTimeout)
	defer cancel()
	if _, err := s.repo.GetTerminal(ctx, terminalID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w (%s)", ErrNotFound, terminalID)
		}
		return nil, fmt.Errorf("world/fleet: consultando la terminal %s: %w", terminalID, err)
	}
	return s.repo.ListTerminalSlots(ctx, terminalID, onlyAvailable, s.sim.Now(ctx))
}

// PurchaseSlot adquiere un slot de prioridad: el comprador paga el precio al DUEÑO
// de la terminal (cash→cash) y toma el slot con vigencia II_SLOT_VALIDITY_SIM. 409
// si el slot tiene titular vigente; 422 si la caja no cubre el precio. Todo en una
// transacción SERIALIZABLE con slot.purchased en la misma tx.
func (s *Service) PurchaseSlot(ctx context.Context, buyer, slotID uuid.UUID) (TerminalSlot, error) {
	if buyer == uuid.Nil {
		return TerminalSlot{}, fmt.Errorf("%w: comprador vacío", ErrValidation)
	}
	simNow := s.sim.Now(ctx)

	var out TerminalSlot
	err := db.RunSerializable(ctx, s.pool, func(tx pgx.Tx) error {
		r := s.repo.WithTx(tx)

		slot, err := r.GetSlotForPurchase(ctx, slotID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("%w (%s)", ErrNotFound, slotID)
		case err != nil:
			return fmt.Errorf("world/fleet: bloqueando el slot %s: %w", slotID, err)
		}
		// Titular vigente: no está en venta (409).
		if slot.HolderAccountID != nil && (slot.ValidUntilSim == nil || *slot.ValidUntilSim >= int64(simNow)) {
			return fmt.Errorf("%w (%s)", ErrSlotHeld, slotID)
		}
		// El dueño de la terminal no compra su propio slot.
		if slot.TerminalOwner == buyer {
			return fmt.Errorf("%w: el dueño de la terminal no puede comprar su propio slot", ErrValidation)
		}
		// Pago: caja del comprador → caja del dueño de la terminal (cash→cash). Si el
		// precio es 0 (slot promocional), no genera asiento.
		if slot.Price > 0 {
			if err := s.payToOwner(ctx, r, buyer, slot.TerminalOwner, slotID, slot.Price, simNow); err != nil {
				return err
			}
		}
		validUntil := int64(simNow) + s.opts.SlotValiditySim
		updated, err := r.SetSlotHolder(ctx, slotID, buyer, validUntil)
		if err != nil {
			return err
		}
		out = updated
		return outbox.Emit(ctx, tx, int64(simNow), AggregateTerminalSlot, slotID, EventSlotPurchased, SlotPurchasedPayload{
			SlotID: slotID.String(), TerminalID: slot.TerminalID.String(), HolderAccountID: buyer.String(),
			Price: fixed(slot.Price), PriorityTier: int64(slot.PriorityTier), ValidUntilSim: validUntil, PurchasedAtSim: int64(simNow),
		})
	})
	if err != nil {
		return TerminalSlot{}, mapLedgerError(err)
	}
	s.slotPurchases.Inc()
	s.logger.Info("slot de prioridad comprado",
		slog.String("slot_id", slotID.String()), slog.String("holder", buyer.String()),
		slog.Int64("price", out.Price), slog.Int("priority_tier", int(out.PriorityTier)))
	return out, nil
}

// payToOwner cobra amount de la caja del comprador a la caja del dueño de la
// terminal con un asiento 'transfer'. FundsError (422) si la caja no cubre.
func (s *Service) payToOwner(ctx context.Context, r *Repo, buyer, owner, reference uuid.UUID, amount int64, simNow simtime.SimTime) error {
	buyerCash, err := r.GetCashAccount(ctx, buyer)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return &FundsError{Required: amount, Available: 0}
	case err != nil:
		return fmt.Errorf("world/fleet: consultando la caja de %s: %w", buyer, err)
	case buyerCash.Balance < amount:
		return &FundsError{Required: amount, Available: buyerCash.Balance}
	}
	ownerCash, err := r.EnsureCashAccount(ctx, owner)
	if err != nil {
		return fmt.Errorf("world/fleet: localizando la caja del dueño de la terminal %s: %w", owner, err)
	}
	return r.PostLedgerTransaction(ctx, sqlcgen.LedgerTransactionKindTransfer, simNow, reference,
		fmt.Sprintf("Compra de slot de prioridad %s", reference), []entryAmount{
			{AccountID: buyerCash.ID, Amount: -amount},
			{AccountID: ownerCash.ID, Amount: amount},
		})
}

// ─── Repo: terminales y slots ──────────────────────────────────────────────────

// GetTerminal devuelve una terminal por id; pgx.ErrNoRows si no existe.
func (r *Repo) GetTerminal(ctx context.Context, id uuid.UUID) (Terminal, error) {
	row, err := r.q.GetTerminal(ctx, id)
	if err != nil {
		return Terminal{}, err
	}
	return Terminal{
		ID: row.ID, NodeID: row.NodeID, OwnerAccountID: row.OwnerAccountID,
		TransshipmentPerHour: row.TransshipmentPerHour, QueueLength: row.QueueLength, UpdatedAtSim: row.UpdatedAtSim,
	}, nil
}

// ListTerminalSlots lista los slots de una terminal (only_available opcional).
func (r *Repo) ListTerminalSlots(ctx context.Context, terminalID uuid.UUID, onlyAvailable bool, simNow simtime.SimTime) ([]TerminalSlot, error) {
	rows, err := r.q.ListTerminalSlots(ctx, sqlcgen.ListTerminalSlotsParams{
		TerminalID: terminalID, OnlyAvailable: onlyAvailable, SimNow: int64(simNow),
	})
	if err != nil {
		return nil, fmt.Errorf("world/fleet: listando slots de la terminal %s: %w", terminalID, err)
	}
	out := make([]TerminalSlot, len(rows))
	for i, row := range rows {
		out[i] = TerminalSlot{
			ID: row.ID, TerminalID: row.TerminalID, PriorityTier: row.PriorityTier,
			Price: row.Price, HolderAccountID: row.HolderAccountID, ValidUntilSim: row.ValidUntilSim,
		}
	}
	return out, nil
}

// slotForPurchase es la vista bloqueada de un slot con el dueño de su terminal.
type slotForPurchase struct {
	ID              uuid.UUID
	TerminalID      uuid.UUID
	PriorityTier    int32
	Price           int64
	HolderAccountID *uuid.UUID
	ValidUntilSim   *int64
	TerminalOwner   uuid.UUID
}

// GetSlotForPurchase bloquea un slot con el dueño de su terminal; pgx.ErrNoRows si
// no existe.
func (r *Repo) GetSlotForPurchase(ctx context.Context, id uuid.UUID) (slotForPurchase, error) {
	row, err := r.q.GetSlotForPurchase(ctx, id)
	if err != nil {
		return slotForPurchase{}, err
	}
	return slotForPurchase{
		ID: row.ID, TerminalID: row.TerminalID, PriorityTier: row.PriorityTier, Price: row.Price,
		HolderAccountID: row.HolderAccountID, ValidUntilSim: row.ValidUntilSim, TerminalOwner: row.TerminalOwnerAccountID,
	}, nil
}

// SetSlotHolder asigna el titular y la vigencia de un slot.
func (r *Repo) SetSlotHolder(ctx context.Context, id, holder uuid.UUID, validUntil int64) (TerminalSlot, error) {
	h, vu := holder, validUntil
	row, err := r.q.SetSlotHolder(ctx, sqlcgen.SetSlotHolderParams{ID: id, HolderAccountID: &h, ValidUntilSim: &vu})
	if err != nil {
		return TerminalSlot{}, fmt.Errorf("world/fleet: asignando el titular del slot %s: %w", id, err)
	}
	return TerminalSlot{
		ID: row.ID, TerminalID: row.TerminalID, PriorityTier: row.PriorityTier,
		Price: row.Price, HolderAccountID: row.HolderAccountID, ValidUntilSim: row.ValidUntilSim,
	}, nil
}

// noSlotTier es el centinela de "sin slot" (menor prioridad posible): coincide con
// el COALESCE de la prioridad de la cola de transbordo (int32 máximo). Un
// slot_tier < noSlotTier ⇒ el dueño posee un slot vigente (servicio con prioridad).
const noSlotTier int32 = 2147483647

// EnsureCashAccount localiza (o crea, on-demand) la caja de una corporación (el
// dueño de la terminal puede no tener caja aún: recibe el pago del slot).
func (r *Repo) EnsureCashAccount(ctx context.Context, owner uuid.UUID) (ledgerAccount, error) {
	acc, err := r.GetCashAccount(ctx, owner)
	switch {
	case err == nil:
		return acc, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return ledgerAccount{}, fmt.Errorf("world/fleet: consultando la caja de %s: %w", owner, err)
	}
	id, err := newUUIDv7()
	if err != nil {
		return ledgerAccount{}, err
	}
	o := owner
	row, err := r.q.CreateCashAccount(ctx, sqlcgen.CreateCashAccountParams{ID: id, OwnerAccountID: &o})
	if err != nil {
		return ledgerAccount{}, fmt.Errorf("world/fleet: creando la caja de %s: %w", owner, err)
	}
	return ledgerAccount{ID: row.ID, Balance: row.Balance}, nil
}
