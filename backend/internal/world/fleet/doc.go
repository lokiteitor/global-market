// Package fleet es el subpaquete de FLOTA y TRÁNSITO FÍSICO del bounded context
// World Simulation (internal/world), Incremento 3 (LOGÍSTICA FÍSICA, Fase 1
// terrestre). Materializa el pilar del diseño: NINGÚN bien se mueve sin
// transporte físico; nada se teletransporta, tampoco en los fallos (GDD
// 7.1/5.3).
//
// Superficie (contrato OpenAPI v1.3.0, secciones world fleet/shipments):
//
//   - Catálogo de vehículos y flota propia (GET /world/vehicle-types,
//     GET/POST /world/vehicles, GET/PATCH /world/vehicles/{id}). La POSICIÓN de
//     un vehículo en tránsito es ANALÍTICA (GDD 1.1): se persiste (segmento,
//     t_entrada, función de avance) y se DERIVA bajo demanda al observarla; solo
//     los HITOS (salida, llegada, avería, cambio de segmento) escriben.
//   - Cargamentos propios y su despacho (GET /world/shipments,
//     GET /world/shipments/{id}, POST /world/shipments/{id}/dispatch): la
//     ejecución logística del CCRI (GDD 5.3 paso 4).
//
// Procesos en segundo plano (los arranca el engine):
//
//   - TransitWorker: el MOTOR DE TRÁNSITO event-driven. Barre los segmentos
//     vencidos (combustible, desgaste, avería probabilística, avance
//     segmento/leg, llegada y entrega física), reanuda averías/mantenimiento y
//     recalcula la congestión por segmento (EMA). Cada vehículo se procesa en su
//     propia transacción SERIALIZABLE, bloqueado con FOR UPDATE SKIP LOCKED.
//   - shipment_creator: consumidor del outbox de contract.confirmed que, para
//     los contratos de compra cross-node, crea el cargamento en el origen y mueve
//     el stock físico fuera del almacén (GDD 5.3 paso 4).
//
// Fronteras (SAD §7 / ADR-006): fleet NUNCA importa internal/logistics ni
// internal/contracts. La integración con el Contract Service es SOLO por el
// outbox (contract.confirmed → shipment_creator; el motor emite shipment.arrived,
// que el Contract Service consume para liquidar). Las rutas (world.routes) las
// escribe el Logistics Service; aquí solo se leen para despachar y simular.
//
// Reglas duras: dinero/stock son int64 (string en JSON, jamás float; math/big
// para overflow); toda mutación de valor va en db.RunSerializable con
// outbox.Emit en la MISMA transacción; autorización por propiedad (403); errores
// tipados; slog + prometheus.
package fleet
