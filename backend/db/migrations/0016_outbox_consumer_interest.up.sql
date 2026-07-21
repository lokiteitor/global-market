-- =============================================================================
-- Imperio Industrial — 0016_outbox_consumer_interest (up)
-- Incremento 9: cada cursor de outbox.consumer_cursors DECLARA los tipos de
-- evento a los que su consumidor lógico está suscrito (el interest management
-- que hasta ahora solo vivía en el proceso).
--
-- MOTIVO — el retraso de un consumidor NO es max(seq) del outbox menos su
-- cursor: un consumidor solo avanza el cursor cuando procesa eventos de SUS
-- tipos (internal/outbox/consumer.go), así que uno perfectamente sano suscrito
-- a un evento raro (system_liquidator ← building.seized) se queda en last_seq=0
-- para siempre y esa resta devuelve el número TOTAL de eventos que el mundo ha
-- emitido en su historia: monótonamente creciente, jamás baja. La válvula de
-- carga del GDD §19 (bots.DensityController) se disparaba contra ese fantasma y
-- clavaba la población de bots en el suelo de forma permanente.
--
-- Con la suscripción EN LA FILA, el retraso se mide contra los eventos que ese
-- consumidor realmente debe procesar (eventos de sus tipos por encima de su
-- cursor) y un consumidor al día vale 0 aunque el mundo siga emitiendo.
--
-- El array lo registra y refresca el PROPIO consumidor en cada polling
-- (INSERT ... ON CONFLICT DO UPDATE, y solo si cambió: sin escrituras en
-- régimen estacionario). No hay alta manual. Un cursor sin suscripción
-- declarada ('{}': nadie lo ha arrancado todavía con este código) no aporta
-- retraso — no se mide lo que no se sabe.
-- =============================================================================

ALTER TABLE outbox.consumer_cursors
    ADD COLUMN event_types TEXT[] NOT NULL DEFAULT '{}';
