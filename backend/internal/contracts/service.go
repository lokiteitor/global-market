package contracts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/lokiteitor/global-market/backend/internal/outbox"
	"github.com/lokiteitor/global-market/backend/internal/platform/db"
	"github.com/lokiteitor/global-market/backend/internal/sim/simtime"
)

// Kinds de cuenta y de asiento del ledger que este módulo escribe (enums de
// 0004_ledger; se referencian por literal para no acoplar módulos Go).
const (
	accountKindStockReserved = "stock_reserved"
	accountKindGuarantee     = "guarantee"
	accountKindEscrow        = "escrow"

	txKindPublicationLock    = "publication_lock"
	txKindPublicationRelease = "publication_release"
	txKindAcceptanceLock     = "acceptance_lock"
	txKindAuction            = "auction" // subasta de embargo vía CCRI (GDD 11.2)
)

// SQLSTATE y constraints que este módulo traduce a errores tipados.
const (
	sqlstateCheckViolation = "23514" // check_violation
	sqlstateFKViolation    = "23503" // foreign_key_violation

	constraintNonNegative = "ck_accounts_non_negative"
)

// SimSource entrega el sim-time actual del mundo. La implementación de
// producción es *clock.Reader (internal/sim/clock); los tests inyectan un
// reloj fijo. Los plazos de dominio (published_at_sim, deadline_sim) usan
// SIEMPRE este reloj; las ventanas wall-clock usan now() de la BD.
type SimSource interface {
	Now(ctx context.Context) simtime.SimTime
}

// Service implementa el ciclo de publicación y aceptación del CCRI. Toda
// operación que mueve valor corre en una única transacción SERIALIZABLE con
// reintento (platform/db.RunSerializable) que asienta a la vez el estado, las
// partidas del ledger y el evento del outbox.
type Service struct {
	pool   *pgxpool.Pool
	repo   *Repo
	sim    SimSource
	opts   Options
	logger *slog.Logger

	pubsCreated      *prometheus.CounterVec
	acceptancesTotal prometheus.Counter
	boardOpen        prometheus.Gauge
}

// NewService construye el servicio sobre el pool compartido de la plataforma.
// reg registra las métricas del módulo (nil las deja sin registrar: tests,
// herramientas); logger nil usa slog.Default(). Options inválidas devuelven
// error: la configuración rota debe impedir el arranque.
func NewService(pool *pgxpool.Pool, sim SimSource, opts Options, logger *slog.Logger, reg prometheus.Registerer) (*Service, error) {
	if pool == nil {
		return nil, errors.New("contracts: el pool de BD es obligatorio")
	}
	if sim == nil {
		return nil, errors.New("contracts: el SimSource es obligatorio")
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
		pubsCreated: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "ii_publications_created_total",
			Help: "Total de publicaciones creadas en el tablón, por tipo.",
		}, []string{"kind"}),
		acceptancesTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "ii_acceptances_total",
			Help: "Total de aceptaciones registradas (pending_draw).",
		}),
		boardOpen: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "ii_board_open_publications",
			Help: "Publicaciones visibles del tablón (draw_window, open, micro_window).",
		}),
	}
	if reg != nil {
		reg.MustRegister(s.pubsCreated, s.acceptancesTotal, s.boardOpen)
	}
	return s, nil
}

// ─── (1) Publicación ─────────────────────────────────────────────────────────

// CreatePublication crea una publicación con su garantía propia bloqueada
// íntegramente en el mismo acto (ADR-014, GDD 5.3):
//
//   - sell: mueve la cantidad de stock_free(publicador, producto, almacén de
//     origen) a la cuenta espejo stock_reserved y el 10% del valor de la caja
//     a la cuenta espejo guarantee.
//   - buy: mueve el 100% del valor de la caja a la cuenta espejo escrow.
//
// La ventana de sorteo (now() BD + II_DRAW_WINDOW_SECONDS) y el cooldown
// anti-parpadeo quedan abiertos; published_at_sim es el sim-time actual. Todo
// —cuentas espejo, publicación, asiento publication_lock y evento
// publication.created— se confirma o se revierte como una sola transacción
// SERIALIZABLE.
func (s *Service) CreatePublication(ctx context.Context, publisher uuid.UUID, in PublicationInput) (Publication, error) {
	if publisher == uuid.Nil {
		return Publication{}, fmt.Errorf("%w: publicador vacío", ErrValidation)
	}
	if err := normalizePublicationInput(publisher, &in); err != nil {
		return Publication{}, err
	}
	value, guarantee, err := lockAmounts(in.QuantityTotal, in.UnitPrice)
	if err != nil {
		return Publication{}, err
	}
	simNow := s.sim.Now(ctx)

	var out Publication
	err = db.RunSerializable(ctx, s.pool, func(tx pgx.Tx) error {
		var e error
		out, e = s.createPublicationTx(ctx, s.repo.WithTx(tx), tx, publisher, in, value, guarantee, simNow)
		return e
	})
	if err != nil {
		return Publication{}, mapLedgerError(err)
	}

	s.pubsCreated.WithLabelValues(string(out.Kind)).Inc()
	s.logger.Info("publicación creada",
		slog.String("publication_id", out.ID.String()),
		slog.String("kind", string(out.Kind)),
		slog.String("channel", string(out.Channel)),
		slog.String("publisher", publisher.String()),
		slog.Int64("quantity_total", out.QuantityTotal),
		slog.Int64("unit_price", out.UnitPrice))
	return out, nil
}

// createPublicationTx asienta una publicación (validación de producto, cuentas
// espejo, bloqueo de colateral y evento publication.created) DENTRO de una
// transacción ya abierta (tx) sobre un Repo ya ligado a ella (r). Es el núcleo
// compartido: lo envuelve CreatePublication con su propia transacción
// SERIALIZABLE, y lo reutiliza el consumidor system_liquidator dentro de la
// transacción del lote del outbox (la subasta del stock embargado es una venta
// del sistema por el MISMO camino que cualquier sell). `in` llega ya normalizado
// (normalizePublicationInput) y value/guarantee ya calculados (lockAmounts).
func (s *Service) createPublicationTx(ctx context.Context, r *Repo, tx pgx.Tx, publisher uuid.UUID, in PublicationInput, value, guarantee int64, simNow simtime.SimTime) (Publication, error) {
	ok, err := r.ProductExists(ctx, *in.ProductID)
	if err != nil {
		return Publication{}, err
	}
	if !ok {
		return Publication{}, fmt.Errorf("%w: el producto %s no existe", ErrValidation, *in.ProductID)
	}

	pubID, err := newUUIDv7()
	if err != nil {
		return Publication{}, err
	}
	params := insertPublicationParams{
		ID:                    pubID,
		Kind:                  in.Kind,
		PublisherAccountID:    publisher,
		Channel:               in.Channel,
		CounterpartyAccountID: in.CounterpartyAccountID,
		ProductID:             in.ProductID,
		QuantityTotal:         in.QuantityTotal,
		UnitPrice:             in.UnitPrice,
		MinLot:                in.MinLot,
		OriginNodeID:          in.OriginNodeID,
		DestinationNodeID:     in.DestinationNodeID,
		DeliverySimSeconds:    in.DeliverySimSeconds,
		DrawWindowSeconds:     s.opts.DrawWindowSeconds,
		CancelCooldownSeconds: s.opts.CancelCooldownSeconds,
		PublishedAtSim:        simNow,
	}

	var entries []entryAmount
	var description string
	switch in.Kind {
	case KindSell:
		node, err := s.warehouseNode(ctx, r, *in.OriginNodeID, "origen")
		if err != nil {
			return Publication{}, err
		}
		// Colateral: el stock debe existir YA en el almacén de origen
		// (regla base del CCRI: nada sobre producción futura) y la caja
		// debe cubrir la garantía del 10%.
		stockFree, err := s.stockFreeOrCollateral(ctx, r, publisher, *in.ProductID, *node.BuildingID, in.QuantityTotal)
		if err != nil {
			return Publication{}, err
		}
		cash, err := s.cashOrCollateral(ctx, r, publisher, guarantee)
		if err != nil {
			return Publication{}, err
		}
		reserved, err := r.CreateMirrorAccount(ctx, accountKindStockReserved, publisher, in.ProductID, node.BuildingID, pubID)
		if err != nil {
			return Publication{}, err
		}
		guaranteeAcc, err := r.CreateMirrorAccount(ctx, accountKindGuarantee, publisher, nil, nil, pubID)
		if err != nil {
			return Publication{}, err
		}
		params.StockReserveAccountID = &reserved.ID
		params.GuaranteeAccountID = &guaranteeAcc.ID
		entries = []entryAmount{
			{AccountID: stockFree.ID, Amount: -in.QuantityTotal},
			{AccountID: reserved.ID, Amount: in.QuantityTotal},
			{AccountID: cash.ID, Amount: -guarantee},
			{AccountID: guaranteeAcc.ID, Amount: guarantee},
		}
		description = fmt.Sprintf("Publicación sell: %d de stock congelado + garantía %d (10%%)", in.QuantityTotal, guarantee)

	case KindBuy:
		if _, err := s.warehouseNode(ctx, r, *in.DestinationNodeID, "destino"); err != nil {
			return Publication{}, err
		}
		cash, err := s.cashOrCollateral(ctx, r, publisher, value)
		if err != nil {
			return Publication{}, err
		}
		escrowAcc, err := r.CreateMirrorAccount(ctx, accountKindEscrow, publisher, nil, nil, pubID)
		if err != nil {
			return Publication{}, err
		}
		params.EscrowAccountID = &escrowAcc.ID
		entries = []entryAmount{
			{AccountID: cash.ID, Amount: -value},
			{AccountID: escrowAcc.ID, Amount: value},
		}
		description = fmt.Sprintf("Publicación buy: %d retenido en escrow (100%%)", value)
	}

	out, err := r.InsertPublication(ctx, params)
	if err != nil {
		return Publication{}, err
	}
	if err := r.PostLedgerTransaction(ctx, txKindPublicationLock, simNow, pubID, description, entries); err != nil {
		return Publication{}, err
	}
	if err := outbox.Emit(ctx, tx, int64(simNow), AggregatePublication, pubID, EventPublicationCreated, PublicationCreatedPayload{
		PublicationID:      pubID.String(),
		Kind:               string(out.Kind),
		Channel:            string(out.Channel),
		PublisherAccountID: publisher.String(),
		ProductID:          uuidOrEmpty(out.ProductID),
		QuantityTotal:      fixed(out.QuantityTotal),
		UnitPrice:          fixed(out.UnitPrice),
		MinLot:             fixed(out.MinLot),
		OriginNodeID:       uuidOrEmpty(out.OriginNodeID),
		DestinationNodeID:  uuidOrEmpty(out.DestinationNodeID),
		DeliverySimSeconds: int64(out.DeliverySimSeconds),
		PublishedAtSim:     int64(out.PublishedAtSim),
	}); err != nil {
		return Publication{}, err
	}
	return out, nil
}

// normalizePublicationInput aplica los defaults del contrato y valida la
// forma de la petición (sin tocar la BD).
func normalizePublicationInput(publisher uuid.UUID, in *PublicationInput) error {
	if !in.Kind.Valid() {
		return fmt.Errorf("%w: kind inválido %q", ErrValidation, in.Kind)
	}
	if in.Kind == KindFreight {
		return ErrFreightPhase2
	}
	if in.Channel == "" {
		in.Channel = ChannelBoard
	}
	if !in.Channel.Valid() {
		return fmt.Errorf("%w: channel inválido %q", ErrValidation, in.Channel)
	}
	switch in.Channel {
	case ChannelPrivate:
		if in.CounterpartyAccountID == nil {
			return fmt.Errorf("%w: counterparty_account_id es obligatorio en canal private", ErrValidation)
		}
		if *in.CounterpartyAccountID == publisher {
			return fmt.Errorf("%w: la contraparte de una negociación privada no puede ser el propio publicador", ErrValidation)
		}
	case ChannelBoard:
		if in.CounterpartyAccountID != nil {
			return fmt.Errorf("%w: counterparty_account_id solo aplica al canal private", ErrValidation)
		}
	}
	if in.ProductID == nil {
		return fmt.Errorf("%w: product_id es obligatorio en publicaciones %s", ErrValidation, in.Kind)
	}
	switch in.Kind {
	case KindSell:
		if in.OriginNodeID == nil {
			return fmt.Errorf("%w: origin_node_id es obligatorio en publicaciones sell", ErrValidation)
		}
		if in.DestinationNodeID != nil {
			return fmt.Errorf("%w: destination_node_id no aplica a publicaciones sell (la entrega es in situ)", ErrValidation)
		}
	case KindBuy:
		if in.DestinationNodeID == nil {
			return fmt.Errorf("%w: destination_node_id es obligatorio en publicaciones buy", ErrValidation)
		}
		if in.OriginNodeID != nil {
			return fmt.Errorf("%w: origin_node_id no aplica a publicaciones buy (lo aporta cada aceptante)", ErrValidation)
		}
	}
	if in.QuantityTotal <= 0 {
		return fmt.Errorf("%w: quantity_total debe ser > 0", ErrValidation)
	}
	if in.UnitPrice <= 0 {
		return fmt.Errorf("%w: unit_price debe ser > 0", ErrValidation)
	}
	if in.MinLot == 0 {
		in.MinLot = 1
	}
	if in.MinLot < 0 {
		return fmt.Errorf("%w: min_lot debe ser > 0", ErrValidation)
	}
	if in.DeliverySimSeconds <= 0 {
		return fmt.Errorf("%w: delivery_sim_seconds debe ser > 0", ErrValidation)
	}
	return nil
}

// ─── (2) Consulta del tablón y detalle ───────────────────────────────────────

// QueryBoard consulta el tablón global (GDD 5.3.1: único e interregional,
// pull con filtros — nunca push). Solo devuelve publicaciones del canal board
// en estados visibles. Devuelve la página y el cursor keyset de la siguiente
// ("" si no hay más).
func (s *Service) QueryBoard(ctx context.Context, f BoardFilter) ([]Publication, string, error) {
	sort := f.Sort
	if sort == "" {
		sort = SortUnitPriceAsc
	}
	if !sort.Valid() {
		return nil, "", fmt.Errorf("%w: sort inválido %q", ErrValidation, f.Sort)
	}
	if f.Kind != "" && !f.Kind.Valid() {
		return nil, "", fmt.Errorf("%w: kind inválido %q", ErrValidation, f.Kind)
	}
	limit := normalizeLimit(f.Limit)

	var afterKey *int64
	var afterID *uuid.UUID
	if f.Cursor != "" {
		key, id, err := decodeBoardCursor(f.Cursor, sort)
		if err != nil {
			return nil, "", err
		}
		afterKey, afterID = &key, &id
	}

	pubs, err := s.repo.ListBoardPublications(ctx, f, sort, afterKey, afterID, limit+1)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(pubs) > int(limit) {
		pubs = pubs[:limit]
		next = encodeBoardCursor(sort, pubs[len(pubs)-1])
	}
	return pubs, next, nil
}

// GetPublication devuelve el detalle de una publicación. Las privadas solo
// son visibles para sus partes (publicador y counterparty): ErrNotParty en
// caso contrario.
func (s *Service) GetPublication(ctx context.Context, viewer, id uuid.UUID) (Publication, error) {
	p, err := s.repo.GetPublication(ctx, id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Publication{}, fmt.Errorf("%w (%s)", ErrPublicationNotFound, id)
	case err != nil:
		return Publication{}, fmt.Errorf("contracts: consultando la publicación %s: %w", id, err)
	}
	if p.Channel == ChannelPrivate && viewer != p.PublisherAccountID &&
		(p.CounterpartyAccountID == nil || *p.CounterpartyAccountID != viewer) {
		return Publication{}, fmt.Errorf("%w (%s)", ErrNotParty, id)
	}
	return p, nil
}

// ─── (3) Cancelación ─────────────────────────────────────────────────────────

// CancelPublication cancela la cantidad restante de una publicación propia,
// fuera del cooldown anti-parpadeo, liberando su garantía restante
// proporcional (stock_reserved → stock_free, guarantee/escrow → cash) y las
// garantías de todas las aceptaciones aún pending_draw (que pasan a released:
// todavía no son contratos). Una sola transacción SERIALIZABLE asienta las
// liberaciones, el cambio de estado y los eventos publication.cancelled y
// acceptance.resolved.
func (s *Service) CancelPublication(ctx context.Context, publisher, id uuid.UUID) (Publication, error) {
	simNow := s.sim.Now(ctx)

	var out Publication
	err := db.RunSerializable(ctx, s.pool, func(tx pgx.Tx) error {
		r := s.repo.WithTx(tx)

		p, err := r.GetPublicationForUpdate(ctx, id)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("%w (%s)", ErrPublicationNotFound, id)
		case err != nil:
			return fmt.Errorf("contracts: bloqueando la publicación %s: %w", id, err)
		}
		if p.PublisherAccountID != publisher {
			return fmt.Errorf("%w (%s)", ErrNotPublisher, id)
		}
		if !p.Status.Acceptable() || p.QuantityRemaining <= 0 {
			return fmt.Errorf("%w (%s, estado %s)", ErrPublicationExhausted, id, p.Status)
		}
		if p.CancelCooldownUntil != nil {
			dbNow, err := r.DBNow(ctx)
			if err != nil {
				return err
			}
			if dbNow.Before(*p.CancelCooldownUntil) {
				return &CooldownError{Until: *p.CancelCooldownUntil}
			}
		}

		// Aceptaciones pending_draw: aún no son contratos — se liberan sus
		// garantías y se resuelven como released, con draw_order por orden de
		// llegada (lo exige la BD al salir de pending_draw).
		pending, err := r.ListPendingAcceptancesForUpdate(ctx, p.ID)
		if err != nil {
			return err
		}
		for i, a := range pending {
			if err := s.releaseAcceptanceCollateral(ctx, r, a, simNow); err != nil {
				return err
			}
			released, err := r.ReleaseAcceptance(ctx, a.ID, int32(i+1)) //nolint:gosec // i < len(pending)
			if err != nil {
				return err
			}
			if err := outbox.Emit(ctx, tx, int64(simNow), AggregateAcceptance, a.ID, EventAcceptanceResolved, AcceptanceResolvedPayload{
				AcceptanceID:      a.ID.String(),
				PublicationID:     p.ID.String(),
				AcceptorAccountID: a.AcceptorAccountID.String(),
				Status:            string(released.Status),
				QuantityServed:    fixed(released.QuantityServed),
				ResolvedAtSim:     int64(simNow),
			}); err != nil {
				return err
			}
		}

		if err := s.releasePublicationCollateral(ctx, r, p, simNow, "Cancelación de la publicación: garantía restante liberada"); err != nil {
			return err
		}
		out, err = r.SetPublicationCancelled(ctx, p.ID)
		if err != nil {
			return err
		}
		return outbox.Emit(ctx, tx, int64(simNow), AggregatePublication, p.ID, EventPublicationCancelled, PublicationCancelledPayload{
			PublicationID:       p.ID.String(),
			Kind:                string(p.Kind),
			QuantityRemaining:   fixed(p.QuantityRemaining),
			ReleasedAcceptances: len(pending),
			CancelledAtSim:      int64(simNow),
		})
	})
	if err != nil {
		return Publication{}, mapLedgerError(err)
	}

	s.logger.Info("publicación cancelada",
		slog.String("publication_id", out.ID.String()),
		slog.String("publisher", publisher.String()),
		slog.Int64("quantity_remaining", out.QuantityRemaining))
	return out, nil
}

// releasePublicationCollateral libera el saldo RESTANTE de las cuentas espejo
// de la publicación (proporcional por construcción: lo ya convertido en
// contrato salió de ellas en su confirmación) con un asiento
// publication_release.
func (s *Service) releasePublicationCollateral(ctx context.Context, r *Repo, p Publication, simNow simtime.SimTime, description string) error {
	var entries []entryAmount

	if p.StockReserveAccountID != nil {
		reserved, err := r.GetLedgerAccount(ctx, *p.StockReserveAccountID)
		if err != nil {
			return fmt.Errorf("contracts: leyendo la reserva de stock de %s: %w", p.ID, err)
		}
		if reserved.Balance > 0 {
			stockFree, err := r.GetStockFreeAccount(ctx, p.PublisherAccountID, *reserved.ProductID, *reserved.WarehouseBuildingID)
			if err != nil {
				return fmt.Errorf("contracts: localizando el stock_free del publicador de %s: %w", p.ID, err)
			}
			entries = append(entries,
				entryAmount{AccountID: reserved.ID, Amount: -reserved.Balance},
				entryAmount{AccountID: stockFree.ID, Amount: reserved.Balance})
		}
	}
	for _, mirrorID := range []*uuid.UUID{p.GuaranteeAccountID, p.EscrowAccountID} {
		if mirrorID == nil {
			continue
		}
		mirror, err := r.GetLedgerAccount(ctx, *mirrorID)
		if err != nil {
			return fmt.Errorf("contracts: leyendo la cuenta espejo %s de %s: %w", *mirrorID, p.ID, err)
		}
		if mirror.Balance <= 0 {
			continue
		}
		cash, err := r.GetCashAccount(ctx, p.PublisherAccountID)
		if err != nil {
			return fmt.Errorf("contracts: localizando la caja del publicador de %s: %w", p.ID, err)
		}
		entries = append(entries,
			entryAmount{AccountID: mirror.ID, Amount: -mirror.Balance},
			entryAmount{AccountID: cash.ID, Amount: mirror.Balance})
	}
	if len(entries) == 0 {
		return nil
	}
	return r.PostLedgerTransaction(ctx, txKindPublicationRelease, simNow, p.ID, description, entries)
}

// releaseAcceptanceCollateral devuelve al aceptante el saldo íntegro de las
// cuentas espejo de una aceptación pending_draw (escrow → cash del comprador;
// stock_reserved → stock_free y guarantee → cash del vendedor) con un asiento
// publication_release referenciado a la aceptación.
func (s *Service) releaseAcceptanceCollateral(ctx context.Context, r *Repo, a Acceptance, simNow simtime.SimTime) error {
	var entries []entryAmount

	if a.StockReserveAccountID != nil {
		reserved, err := r.GetLedgerAccount(ctx, *a.StockReserveAccountID)
		if err != nil {
			return fmt.Errorf("contracts: leyendo la reserva de stock de la aceptación %s: %w", a.ID, err)
		}
		if reserved.Balance > 0 {
			stockFree, err := r.GetStockFreeAccount(ctx, a.AcceptorAccountID, *reserved.ProductID, *reserved.WarehouseBuildingID)
			if err != nil {
				return fmt.Errorf("contracts: localizando el stock_free del aceptante %s: %w", a.AcceptorAccountID, err)
			}
			entries = append(entries,
				entryAmount{AccountID: reserved.ID, Amount: -reserved.Balance},
				entryAmount{AccountID: stockFree.ID, Amount: reserved.Balance})
		}
	}
	for _, mirrorID := range []*uuid.UUID{a.GuaranteeAccountID, a.EscrowAccountID} {
		if mirrorID == nil {
			continue
		}
		mirror, err := r.GetLedgerAccount(ctx, *mirrorID)
		if err != nil {
			return fmt.Errorf("contracts: leyendo la cuenta espejo %s de la aceptación %s: %w", *mirrorID, a.ID, err)
		}
		if mirror.Balance <= 0 {
			continue
		}
		cash, err := r.GetCashAccount(ctx, a.AcceptorAccountID)
		if err != nil {
			return fmt.Errorf("contracts: localizando la caja del aceptante %s: %w", a.AcceptorAccountID, err)
		}
		entries = append(entries,
			entryAmount{AccountID: mirror.ID, Amount: -mirror.Balance},
			entryAmount{AccountID: cash.ID, Amount: mirror.Balance})
	}
	if len(entries) == 0 {
		return nil
	}
	return r.PostLedgerTransaction(ctx, txKindPublicationRelease, simNow, a.ID,
		"Cancelación de la publicación: garantía del aceptante liberada", entries)
}

// ─── (4) Aceptación ──────────────────────────────────────────────────────────

// Accept registra una aceptación (total o parcial) dentro de la ventana de
// sorteo (ADR-011: al cierre se sortea el orden — la latencia no otorga
// ventaja). La garantía del aceptante queda bloqueada en el mismo acto:
//
//   - Publicación sell → el aceptante es COMPRADOR: 100% del valor a la
//     cuenta espejo escrow de la aceptación.
//   - Publicación buy → el aceptante es VENDEDOR: aporta origin_node_id (su
//     almacén con el stock), cantidad de stock_free a stock_reserved y 10%
//     del valor de caja a guarantee.
//
// Sobre una publicación madura (open) la aceptación abre la micro-ventana.
// Todo se confirma en una única transacción SERIALIZABLE junto al evento
// acceptance.registered.
func (s *Service) Accept(ctx context.Context, acceptor, publicationID uuid.UUID, in AcceptInput) (Acceptance, error) {
	if acceptor == uuid.Nil {
		return Acceptance{}, fmt.Errorf("%w: aceptante vacío", ErrValidation)
	}
	if in.Quantity <= 0 {
		return Acceptance{}, fmt.Errorf("%w: quantity debe ser > 0", ErrValidation)
	}
	simNow := s.sim.Now(ctx)

	var out Acceptance
	err := db.RunSerializable(ctx, s.pool, func(tx pgx.Tx) error {
		r := s.repo.WithTx(tx)

		p, err := r.GetPublicationForUpdate(ctx, publicationID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("%w (%s)", ErrPublicationNotFound, publicationID)
		case err != nil:
			return fmt.Errorf("contracts: bloqueando la publicación %s: %w", publicationID, err)
		}
		if p.Kind == KindFreight {
			return ErrFreightPhase2
		}
		if p.Channel == ChannelPrivate && (p.CounterpartyAccountID == nil || *p.CounterpartyAccountID != acceptor) {
			return fmt.Errorf("%w (%s)", ErrNotParty, publicationID)
		}
		if acceptor == p.PublisherAccountID {
			return fmt.Errorf("%w: el publicador no puede aceptar su propia publicación", ErrValidation)
		}
		if !p.Status.Acceptable() || p.QuantityRemaining <= 0 {
			return fmt.Errorf("%w (%s, estado %s)", ErrPublicationExhausted, publicationID, p.Status)
		}
		minAccept := min(p.MinLot, p.QuantityRemaining)
		if in.Quantity < minAccept || in.Quantity > p.QuantityRemaining {
			return &MinLotError{MinLot: minAccept, QuantityRemaining: p.QuantityRemaining}
		}
		value, guarantee, err := lockAmounts(in.Quantity, p.UnitPrice)
		if err != nil {
			return err
		}

		accID, err := newUUIDv7()
		if err != nil {
			return err
		}
		params := insertAcceptanceParams{
			ID:                accID,
			PublicationID:     p.ID,
			AcceptorAccountID: acceptor,
			Quantity:          in.Quantity,
		}

		var entries []entryAmount
		var description string
		switch p.Kind {
		case KindSell: // el aceptante compra: escrow del 100%
			cash, err := s.cashOrCollateral(ctx, r, acceptor, value)
			if err != nil {
				return err
			}
			escrowAcc, err := r.CreateMirrorAccount(ctx, accountKindEscrow, acceptor, nil, nil, accID)
			if err != nil {
				return err
			}
			params.EscrowAccountID = &escrowAcc.ID
			entries = []entryAmount{
				{AccountID: cash.ID, Amount: -value},
				{AccountID: escrowAcc.ID, Amount: value},
			}
			description = fmt.Sprintf("Aceptación de venta: %d retenido en escrow (100%%)", value)

		case KindBuy: // el aceptante vende: stock congelado + garantía 10%
			if in.OriginNodeID == nil {
				return fmt.Errorf("%w: origin_node_id es obligatorio al aceptar una publicación buy", ErrValidation)
			}
			node, err := s.warehouseNode(ctx, r, *in.OriginNodeID, "origen")
			if err != nil {
				return err
			}
			if node.BuildingOwner == nil || *node.BuildingOwner != acceptor {
				return fmt.Errorf("%w (%s)", ErrNotNodeOwner, *in.OriginNodeID)
			}
			stockFree, err := s.stockFreeOrCollateral(ctx, r, acceptor, *p.ProductID, *node.BuildingID, in.Quantity)
			if err != nil {
				return err
			}
			cash, err := s.cashOrCollateral(ctx, r, acceptor, guarantee)
			if err != nil {
				return err
			}
			reserved, err := r.CreateMirrorAccount(ctx, accountKindStockReserved, acceptor, p.ProductID, node.BuildingID, accID)
			if err != nil {
				return err
			}
			guaranteeAcc, err := r.CreateMirrorAccount(ctx, accountKindGuarantee, acceptor, nil, nil, accID)
			if err != nil {
				return err
			}
			params.StockReserveAccountID = &reserved.ID
			params.GuaranteeAccountID = &guaranteeAcc.ID
			entries = []entryAmount{
				{AccountID: stockFree.ID, Amount: -in.Quantity},
				{AccountID: reserved.ID, Amount: in.Quantity},
				{AccountID: cash.ID, Amount: -guarantee},
				{AccountID: guaranteeAcc.ID, Amount: guarantee},
			}
			description = fmt.Sprintf("Aceptación de compra: %d de stock congelado + garantía %d (10%%)", in.Quantity, guarantee)
		}

		if err := r.PostLedgerTransaction(ctx, txKindAcceptanceLock, simNow, accID, description, entries); err != nil {
			return err
		}
		out, err = r.InsertAcceptance(ctx, params)
		if err != nil {
			return err
		}
		// Publicación madura: la primera aceptación abre la micro-ventana en
		// la que otras pueden concurrir antes del sorteo (ADR-011).
		if p.Status == StatusOpen {
			if _, err := r.SetPublicationMicroWindow(ctx, p.ID, s.opts.MicroWindowSeconds); err != nil {
				return err
			}
		}
		return outbox.Emit(ctx, tx, int64(simNow), AggregateAcceptance, accID, EventAcceptanceRegistered, AcceptanceRegisteredPayload{
			AcceptanceID:      accID.String(),
			PublicationID:     p.ID.String(),
			AcceptorAccountID: acceptor.String(),
			Quantity:          fixed(in.Quantity),
			RegisteredAtSim:   int64(simNow),
		})
	})
	if err != nil {
		return Acceptance{}, mapLedgerError(err)
	}

	s.acceptancesTotal.Inc()
	s.logger.Info("aceptación registrada",
		slog.String("acceptance_id", out.ID.String()),
		slog.String("publication_id", publicationID.String()),
		slog.String("acceptor", acceptor.String()),
		slog.Int64("quantity", out.Quantity))
	return out, nil
}

// GetAcceptance devuelve el estado de una aceptación; solo visible para el
// aceptante (ErrNotAcceptor en caso contrario).
func (s *Service) GetAcceptance(ctx context.Context, viewer, id uuid.UUID) (Acceptance, error) {
	a, err := s.repo.GetAcceptance(ctx, id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Acceptance{}, fmt.Errorf("%w (%s)", ErrAcceptanceNotFound, id)
	case err != nil:
		return Acceptance{}, fmt.Errorf("contracts: consultando la aceptación %s: %w", id, err)
	}
	if a.AcceptorAccountID != viewer {
		return Acceptance{}, fmt.Errorf("%w (%s)", ErrNotAcceptor, id)
	}
	return a, nil
}

// ─── (5) Contratos y entregas ────────────────────────────────────────────────

// ListContracts devuelve los contratos CCRI en los que la corporación
// autenticada es compradora o vendedora, con los filtros del contrato y el
// cursor keyset de la siguiente página ("" si no hay más).
func (s *Service) ListContracts(ctx context.Context, account uuid.UUID, f ContractFilter) ([]Contract, string, error) {
	if f.Role != "" && !f.Role.Valid() {
		return nil, "", fmt.Errorf("%w: role inválido %q", ErrValidation, f.Role)
	}
	if f.Status != "" && !f.Status.Valid() {
		return nil, "", fmt.Errorf("%w: status inválido %q", ErrValidation, f.Status)
	}
	limit := normalizeLimit(f.Limit)
	var afterID *uuid.UUID
	if f.Cursor != "" {
		id, err := decodeContractCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		afterID = &id
	}
	contracts, err := s.repo.ListContracts(ctx, account, f, afterID, limit+1)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(contracts) > int(limit) {
		contracts = contracts[:limit]
		next = encodeContractCursor(contracts[len(contracts)-1].ID)
	}
	return contracts, next, nil
}

// GetContract devuelve el detalle de un contrato. Solo visible para sus partes
// (comprador y vendedor): ErrNotContractParty en caso contrario.
func (s *Service) GetContract(ctx context.Context, viewer, id uuid.UUID) (Contract, error) {
	c, err := s.repo.GetContract(ctx, id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Contract{}, fmt.Errorf("%w (%s)", ErrContractNotFound, id)
	case err != nil:
		return Contract{}, fmt.Errorf("contracts: consultando el contrato %s: %w", id, err)
	}
	if !c.IsParty(viewer) {
		return Contract{}, fmt.Errorf("%w (%s)", ErrNotContractParty, id)
	}
	return c, nil
}

// ListContractDeliveries devuelve las entregas confirmadas de un contrato,
// visibles solo para sus partes (ErrNotContractParty en caso contrario;
// ErrContractNotFound si el contrato no existe).
func (s *Service) ListContractDeliveries(ctx context.Context, viewer, contractID uuid.UUID) ([]ContractDelivery, error) {
	c, err := s.repo.GetContract(ctx, contractID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf("%w (%s)", ErrContractNotFound, contractID)
	case err != nil:
		return nil, fmt.Errorf("contracts: consultando el contrato %s: %w", contractID, err)
	}
	if !c.IsParty(viewer) {
		return nil, fmt.Errorf("%w (%s)", ErrNotContractParty, contractID)
	}
	return s.repo.ListContractDeliveries(ctx, contractID)
}

// ResolveAcceptanceContract devuelve el contrato resultante de una aceptación
// servida (nil si la aceptación no llegó a contrato). El esquema no liga la
// aceptación al contrato con una FK: el vínculo es publicación + aceptante como
// comprador (venta) o vendedor (compra). Si una misma cuenta encadenó varias
// aceptaciones servidas sobre la misma publicación, se devuelve la más antigua
// (limitación conocida de la ausencia de columna contract_id).
func (s *Service) ResolveAcceptanceContract(ctx context.Context, a Acceptance) (*uuid.UUID, error) {
	if a.Status != AcceptanceServed {
		return nil, nil
	}
	c, err := s.repo.GetContractForAcceptance(ctx, a.PublicationID, a.AcceptorAccountID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("contracts: resolviendo el contrato de la aceptación %s: %w", a.ID, err)
	}
	id := c.ID
	return &id, nil
}

// ─── Liquidación pro-rata (compartida por el worker y el consumidor) ─────────

// settleAndEmit liquida un contrato pro-rata con ledger.settle_contract_prorata
// (lo entregado a tiempo al comprador en el destino; lo faltante reembolsado al
// comprador y la garantía repartida compensación/sink; el stock no entregado
// liberado in situ en el ledger del vendedor en su almacén de origen) y emite
// contract.settled con el estado final (settled|failed). Corre DENTRO de la
// transacción del llamante (tx) sobre un Repo ya ligado a ella (r), con el
// contrato ya bloqueado FOR UPDATE. La comparte el barrido de vencimiento del
// worker (fill pro-rata) y el consumidor de entregas (fill 100% al completar).
// Devuelve el contrato ya liquidado (status/fill_bp los fija la función SQL).
func (s *Service) settleAndEmit(ctx context.Context, r *Repo, tx pgx.Tx, contract Contract, simNow simtime.SimTime) (Contract, error) {
	sellerCash, err := r.GetCashAccount(ctx, contract.SellerAccountID)
	if err != nil {
		return Contract{}, fmt.Errorf("contracts: caja del vendedor %s: %w", contract.SellerAccountID, err)
	}
	buyerCash, err := r.GetCashAccount(ctx, contract.BuyerAccountID)
	if err != nil {
		return Contract{}, fmt.Errorf("contracts: caja del comprador %s: %w", contract.BuyerAccountID, err)
	}
	sink, err := r.GetSinkAccount(ctx)
	if err != nil {
		return Contract{}, fmt.Errorf("contracts: cuenta sink del banco central: %w", err)
	}

	destNode, err := s.warehouseNode(ctx, r, contract.DestinationNodeID, "destino")
	if err != nil {
		return Contract{}, err
	}
	buyerStock, err := r.EnsureStockFreeAccount(ctx, contract.BuyerAccountID, contract.ProductID, *destNode.BuildingID)
	if err != nil {
		return Contract{}, err
	}
	originNode, err := s.warehouseNode(ctx, r, contract.OriginNodeID, "origen")
	if err != nil {
		return Contract{}, err
	}
	sellerStockRelease, err := r.EnsureStockFreeAccount(ctx, contract.SellerAccountID, contract.ProductID, *originNode.BuildingID)
	if err != nil {
		return Contract{}, err
	}

	txID, err := newUUIDv7()
	if err != nil {
		return Contract{}, err
	}
	entryIDs, err := newUUIDv7Batch(maxSettleEntries)
	if err != nil {
		return Contract{}, err
	}
	if err := r.SettleContractProrata(ctx, settleContractArgs{
		TxID:               txID,
		ContractID:         contract.ID,
		SimTime:            simNow,
		SellerCash:         sellerCash.ID,
		BuyerCash:          buyerCash.ID,
		BuyerStock:         buyerStock.ID,
		SinkAccount:        sink.ID,
		SellerStockRelease: sellerStockRelease.ID,
		CompensationBP:     int32(s.opts.CompensationBP), //nolint:gosec // 0..10000 por Validate
		EntryIDs:           entryIDs,
	}); err != nil {
		return Contract{}, mapLedgerError(err)
	}

	// Re-leer el contrato para el estado final (status/fill_bp los fija la
	// función SQL) y emitir contract.settled con el shape exacto del contrato.
	settled, err := r.GetContract(ctx, contract.ID)
	if err != nil {
		return Contract{}, fmt.Errorf("contracts: releyendo el contrato liquidado %s: %w", contract.ID, err)
	}
	fill := 0
	if settled.FillBP != nil {
		fill = int(*settled.FillBP)
	}
	if err := outbox.Emit(ctx, tx, int64(simNow), AggregateContract, settled.ID, EventContractSettled, ContractSettledPayload{
		ContractID:          settled.ID.String(),
		ProductID:           settled.ProductID.String(),
		DestinationRegionID: destNode.RegionID.String(),
		UnitPrice:           fixed(settled.UnitPrice),
		QuantityAgreed:      fixed(settled.QuantityAgreed),
		QuantityDelivered:   fixed(settled.QuantityDelivered),
		FillBP:              fill,
		SettledAtSim:        int64(simNow),
		Status:              string(settled.Status),
	}); err != nil {
		return Contract{}, err
	}
	return settled, nil
}

// ─── Métricas del tablón ─────────────────────────────────────────────────────

// UpdateBoardGauge recuenta las publicaciones visibles del tablón y actualiza
// el gauge ii_board_open_publications. Pensada para el polling de los workers
// del incremento; devuelve el recuento.
func (s *Service) UpdateBoardGauge(ctx context.Context) (int64, error) {
	n, err := s.repo.CountBoardPublications(ctx)
	if err != nil {
		return 0, err
	}
	s.boardOpen.Set(float64(n))
	return n, nil
}

// SetBoardOpenPublications fija el gauge ii_board_open_publications a un
// valor ya conocido por el llamador (evita un recuento redundante).
func (s *Service) SetBoardOpenPublications(n int64) {
	s.boardOpen.Set(float64(n))
}

// ─── Helpers de colateral y de importes ──────────────────────────────────────

// stockFreeOrCollateral localiza la cuenta stock_free (dueño, producto,
// almacén) y comprueba que cubre la cantidad requerida. La ausencia de cuenta
// o el saldo corto devuelven CollateralError (422 INSUFFICIENT_COLLATERAL)
// con los details {required, available}; la verificación definitiva sigue
// siendo el constraint de no-negatividad al asentar las partidas.
func (s *Service) stockFreeOrCollateral(ctx context.Context, r *Repo, owner, product, warehouse uuid.UUID, required int64) (ledgerAccount, error) {
	acc, err := r.GetStockFreeAccount(ctx, owner, product, warehouse)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ledgerAccount{}, &CollateralError{Resource: "stock", Required: required, Available: 0}
	case err != nil:
		return ledgerAccount{}, fmt.Errorf("contracts: consultando el stock_free de %s: %w", owner, err)
	case acc.Balance < required:
		return ledgerAccount{}, &CollateralError{Resource: "stock", Required: required, Available: acc.Balance}
	}
	return acc, nil
}

// cashOrCollateral localiza la caja del titular y comprueba que cubre el
// importe requerido (misma semántica que stockFreeOrCollateral).
func (s *Service) cashOrCollateral(ctx context.Context, r *Repo, owner uuid.UUID, required int64) (ledgerAccount, error) {
	acc, err := r.GetCashAccount(ctx, owner)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ledgerAccount{}, &CollateralError{Resource: "cash", Required: required, Available: 0}
	case err != nil:
		return ledgerAccount{}, fmt.Errorf("contracts: consultando la caja de %s: %w", owner, err)
	case acc.Balance < required:
		return ledgerAccount{}, &CollateralError{Resource: "cash", Required: required, Available: acc.Balance}
	}
	return acc, nil
}

// warehouseNode valida que un nodo del grafo logístico existe y está
// respaldado por un edificio (almacén). side es "origen" o "destino" para el
// mensaje de validación.
func (s *Service) warehouseNode(ctx context.Context, r *Repo, nodeID uuid.UUID, side string) (nodeBuilding, error) {
	node, err := r.GetNodeBuilding(ctx, nodeID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nodeBuilding{}, fmt.Errorf("%w: el nodo de %s %s no existe", ErrValidation, side, nodeID)
	case err != nil:
		return nodeBuilding{}, fmt.Errorf("contracts: consultando el nodo %s: %w", nodeID, err)
	case node.BuildingID == nil:
		return nodeBuilding{}, fmt.Errorf("%w: el nodo de %s %s no tiene almacén", ErrValidation, side, nodeID)
	}
	return node, nil
}

// lockAmounts calcula el valor del bloqueo (qty*price) y la garantía del 10%
// validando con math/big que ni el valor ni el intermedio ×10 de las fórmulas
// SQL de garantía (confirm_contract/settle_contract_prorata) desbordan int64.
func lockAmounts(qty, price int64) (value, guarantee int64, err error) {
	v := new(big.Int).Mul(big.NewInt(qty), big.NewInt(price))
	scaled := new(big.Int).Mul(v, big.NewInt(guaranteePercent))
	if !scaled.IsInt64() {
		return 0, 0, ErrOverflow
	}
	value = v.Int64()
	return value, value * guaranteePercent / 100, nil
}

// normalizeLimit aplica el default y el máximo del contrato (50/200).
func normalizeLimit(limit int) int32 {
	switch {
	case limit <= 0:
		return DefaultPageLimit
	case limit > MaxPageLimit:
		return MaxPageLimit
	default:
		return int32(limit)
	}
}

// mapLedgerError traduce las violaciones de invariantes de la BD que pueden
// emerger pese a las validaciones previas (carreras resueltas por constraint
// en lugar de por serialización) a errores tipados del módulo.
func mapLedgerError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch {
		case pgErr.Code == sqlstateCheckViolation && pgErr.ConstraintName == constraintNonNegative:
			// El asiento dejaría un saldo negativo: garantía insuficiente
			// detectada por el constraint (todo-o-nada: nada quedó asentado).
			return fmt.Errorf("%w: %s", ErrInsufficientCollateral, pgErr.Message)
		case pgErr.Code == sqlstateFKViolation:
			// counterparty/product/node inexistente que escapó a la
			// validación previa.
			return fmt.Errorf("%w: referencia inexistente (%s)", ErrValidation, pgErr.ConstraintName)
		}
	}
	return err
}
