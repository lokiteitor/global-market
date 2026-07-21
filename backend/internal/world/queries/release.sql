-- =============================================================================
-- Imperio Industrial — queries sqlc de la LIBERACIÓN IN SITU de cargamentos
-- (Incremento 3, Fase 1 terrestre). El consumidor world "shipment_releaser"
-- consume contract.expired_undelivered (emitido por el Contract Service al vencer
-- un contrato con cantidad SIN entregar) y DETIENE los cargamentos aún vivos de
-- ese contrato, liberando su stock físico in situ (GDD 7.1/5.3: nada se
-- teletransporta, tampoco en los fallos).
--
-- Coherencia físico↔contable: al vencer, el Contract Service liquida pro-rata y
-- libera en el LEDGER el stock reservado NO entregado como stock_free del
-- vendedor EN EL ALMACÉN DE ORIGEN del contrato (ledger.settle_contract_prorata
-- con p_seller_stock_release = stock_free(vendedor, producto, almacén de origen)).
-- El lado FÍSICO debe casar EXACTAMENTE con ese asiento: el cargamento se marca
-- released_in_situ y su stock vuelve a world.building_inventories del MISMO
-- almacén de origen. Así la reconciliación (que atribuye los cargamentos en
-- vuelo al almacén de origen vía la cuenta reservada, y EXCLUYE los
-- released_in_situ por estar ya en building_inventories de origen) sigue cuadrando
-- a cero. En Fase 1 la ruta sólo tiene nodos con almacén en sus extremos (el
-- junction intermedio no tiene almacén), de modo que "la ubicación física actual"
-- se resuelve al almacén de origen; las terminales intermedias son fases
-- posteriores.
-- =============================================================================

-- ListContractShipmentsToRelease lista los cargamentos AÚN VIVOS de un contrato
-- (in_warehouse/in_transit/at_terminal; los delivered ya llegaron y los
-- released_in_situ ya se liberaron) con el nodo y el almacén de ORIGEN del
-- contrato (donde el ledger liberó el stock no entregado). FOR UPDATE OF sh
-- serializa con el motor de tránsito (que los tomaría para entregar).
-- name: ListContractShipmentsToRelease :many
SELECT sh.id, sh.owner_account_id, sh.product_id, sh.quantity,
       c.origin_node_id AS origin_node_id,
       n.building_id     AS origin_building_id
FROM world.shipments sh
JOIN ledger.contracts c ON c.id = sh.contract_id
JOIN world.network_nodes n ON n.id = c.origin_node_id
WHERE sh.contract_id = sqlc.arg(contract_id)
  AND sh.status IN ('in_warehouse', 'in_transit', 'at_terminal')
FOR UPDATE OF sh;

-- ReleaseShipmentInSitu detiene un cargamento y lo libera in situ: released_in_situ
-- en el nodo de origen, ya no a bordo de un vehículo (at_node_id NOT NULL /
-- vehicle_id NULL respeta el CHECK de ubicación física coherente).
-- name: ReleaseShipmentInSitu :exec
UPDATE world.shipments
   SET status = 'released_in_situ', at_node_id = sqlc.arg(at_node_id), vehicle_id = NULL,
       updated_at_sim = sqlc.arg(sim_now)
 WHERE id = sqlc.arg(id);

-- ListFreightShipmentsToRelease lista los cargamentos AÚN VIVOS de un flete vencido
-- (in_warehouse/in_transit/at_terminal) con el nodo y el almacén de ORIGEN del
-- flete (donde el Contract Service liberó la custodia in situ en el ledger). El
-- lado físico debe casar: el cargamento se marca released_in_situ y su stock vuelve
-- a world.building_inventories del mismo almacén de origen. FOR UPDATE OF sh
-- serializa con el motor de tránsito.
-- name: ListFreightShipmentsToRelease :many
SELECT sh.id, sh.owner_account_id, sh.product_id, sh.quantity,
       fc.origin_node_id AS origin_node_id,
       n.building_id      AS origin_building_id
FROM world.shipments sh
JOIN ledger.freight_contracts fc ON fc.id = sh.freight_contract_id
JOIN world.network_nodes n ON n.id = fc.origin_node_id
WHERE sh.freight_contract_id = sqlc.arg(freight_contract_id)
  AND sh.status IN ('in_warehouse', 'in_transit', 'at_terminal')
FOR UPDATE OF sh;
