-- =============================================================================
-- Imperio Industrial — 0012_system_liquidation (down)
-- Revierte 0012: elimina la tabla de idempotencia de la subasta del sistema. Es
-- un registro propio del liquidador sin dependencias externas (sin FK): su
-- retirada es limpia. Las publicaciones sell del sistema ya creadas quedan en
-- ledger.publications (su ciclo lo cierra el worker del CCRI como cualquier
-- otra), pero sin esta tabla ya no habrá liquidador que registre nuevas.
-- =============================================================================

DROP TABLE IF EXISTS ledger.system_liquidations;
