-- =============================================================================
-- Imperio Industrial — queries del pathfinding del Logistics Service (GDD 7.4).
--
-- El route-plan (POST /logistics/route-plans) es una operación de SOLO cálculo
-- (no persiste nada): carga el grafo de enlaces ponderado por la congestión
-- suavizada (EMA) que publican los shards y corre Dijkstra sobre nodos. A la
-- escala de la Fase 1 (una región, pocos nodos) cargar el grafo entero de la
-- región es correcto y barato; la jerarquía HPA* del GDD 7.4 queda para cuando
-- la medición del grafo mundial lo exija (la interface Planner ya lo enmarca).
--
-- El coste monetario aproximado (optimize=cost) combina combustible (∝ km) y
-- aduanas/peajes según customs_rate_bp de la región de cada segmento: por eso
-- se agrega la congestión y las aduanas por enlace desde sus segmentos.
-- =============================================================================

-- LoadGraphEdges devuelve TODOS los enlaces (opcionalmente filtrados por modos)
-- con la congestión EMA media y la tasa de aduanas media de sus segmentos. Un
-- enlace sin segmentos (no debería ocurrir en un mundo bien sembrado) cae a
-- congestión fluida (1.0) y aduanas 0 — defensivo. Los pesos exactos (ETA en
-- sim-segundos y coste) los deriva el planner en Go a partir de estos campos.
-- name: LoadGraphEdges :many
SELECT l.id, l.mode, l.from_node_id, l.to_node_id,
       l.length_m, l.base_speed_kmh, l.capacity_per_hour,
       COALESCE(AVG(s.congestion_ema), 1.0)::float8 AS congestion_ema,
       COALESCE(AVG(r.customs_rate_bp), 0)::float8   AS customs_rate_bp
FROM world.network_links l
LEFT JOIN world.link_segments s ON s.link_id = l.id
LEFT JOIN world.regions r       ON r.id = s.region_id
WHERE (sqlc.narg(modes)::text[] IS NULL
       OR l.mode::text = ANY(sqlc.narg(modes)::text[]))
GROUP BY l.id, l.mode, l.from_node_id, l.to_node_id,
         l.length_m, l.base_speed_kmh, l.capacity_per_hour;

-- TerminalsAtNodes devuelve las terminales intermodales presentes en un conjunto
-- de nodos (transbordo en un cambio de modo, GDD 7.3). Lo usan tanto el
-- route-plan (anotar transshipment_terminal_id en el tramo donde cambia el modo)
-- como la creación de rutas (validar que un salto multimodal ocurre en un nodo
-- con terminal).
-- name: TerminalsAtNodes :many
SELECT id, node_id
FROM world.terminals
WHERE node_id = ANY(sqlc.arg(node_ids)::uuid[]);

-- LoadTerminalNodes devuelve TODAS las terminales intermodales del mundo (node_id
-- → id, capacidad de transbordo por hora). El pathfinding las carga una vez por
-- consulta para (a) permitir un cambio de modo SOLO en un nodo con terminal (GDD
-- 7.3: sin terminal, el transbordo no es transitable) y (b) sumar el tiempo de
-- transbordo a la ETA del tramo donde cambia el modo. Las terminales son escasas
-- (una por junction intermodal), así que cargarlas enteras es barato.
-- name: LoadTerminalNodes :many
SELECT id, node_id, transshipment_per_hour
FROM world.terminals;

-- LinksByIDs devuelve la topología (modo, extremos) de un conjunto de enlaces
-- para validar la contigüidad y el multimodalismo de una ruta. NO preserva el
-- orden de entrada: la capa de servicio reordena por la secuencia pedida y
-- detecta los ids inexistentes (una ruta con un enlace que no existe es 422).
-- name: LinksByIDs :many
SELECT id, mode, from_node_id, to_node_id
FROM world.network_links
WHERE id = ANY(sqlc.arg(link_ids)::uuid[]);
