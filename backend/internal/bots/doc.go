// Package bots implementa la población de bots (ADR-024, GDD §13/§15.4): los
// ARQUETIPOS de reglas fijas auditables que validan el loop económico y el
// ORQUESTADOR del Bot Orchestration Service (cmd/bots).
//
// Arquetipos (GDD §13.2 completo):
//
//   - coal_producer / iron_producer — productores primarios: extraen y venden;
//     el carbonero además atiende solicitudes de compra y despacha camiones, y
//     el minero compra su combustible por el tablón.
//   - trader — comerciante/arbitrajista: compra barato y re-lista con margen.
//   - industrial_transformer — transformador industrial (bot INTERMEDIO del
//     GDD §13.3, reglas + optimización simple): compra insumos, funde acero y
//     lo vende con margen sobre el coste estimado; con el margen esperado en
//     negativo para la cola y no publica.
//   - freighter — transportista: no toca mercancía, vende capacidad de
//     transporte. Valora las solicitudes kind=freight del tablón (ingreso de
//     la tarifa contra combustible + opex + riesgo de la garantía), acepta las
//     rentables, despacha su vehículo y cobra al entregar (CCRI-Flete,
//     GDD §5.3.2).
//
// Modos de operación del GDD §13.4 cubiertos aquí:
//
//   - "Mundo vivo" (modo 1): el orquestador aprovisiona y ejecuta la población
//     permanente contra la API pública.
//   - "Densidad dinámica" (modo 2): DensityController ajusta continuamente
//     cuántos bots de cada arquetipo están ACTIVOS —los pausa y los reanuda en
//     caliente, sin retirarlos— según la actividad humana, la saturación del
//     sistema y la cobertura del tablón. Es la válvula de carga principal del
//     techo de capacidad (GDD §19, ADR-009): ante saturación se reduce la
//     población de bots ANTES que degradar la experiencia humana. Ver la nota
//     de cabecera de density.go para las señales y la fórmula.
//
// La frontera del ADR-024 es nítida y este paquete la respeta:
//
//   - Lifecycle (interno): el aprovisionamiento — cuenta kind=bot, credencial
//     argon2id derivada de una semilla (reproducible sin almacenar el secreto
//     en claro), bot_profiles y la CAPITALIZACIÓN única (bot_capitalization:
//     +cash/−emission) — usa internal/auth e internal/ledger, porque es una
//     operación del banco central, no un comando de juego.
//   - Gameplay (API pública): TODO lo que los bots juegan pasa por pkg/botsdk
//     (REST + WS), con los mismos endpoints y rate limits que cualquier
//     jugador (igualdad de API literal, ADR-010). Los arquetipos NO importan
//     ningún otro paquete interno de dominio.
//
// Cada arquetipo implementa la interfaz Behavior: Decide es UNA pasada de
// decisión idempotente que se invoca periódicamente (tick jitterizado, o
// despertada por eventos WS) y tolera re-ejecuciones y estados a medias — el
// estado observable de la API manda, el State local solo cachea IDs ya
// descubiertos. Toda decisión relevante emite un log slog INFO estructurado
// (bot, arquetipo, decisión, motivo, ids) y la métrica
// ii_bot_decisions_total{bot,decision}: las heurísticas son auditables por
// diseño (GDD §13). El corolario operativo es que NINGUNA pasada es muda:
// cuando un barrido termina sin actuar emite su decisión terminal wait con el
// motivo y los umbrales evaluados (no_bargain_on_board, no_freight_on_board,
// cash_at_cushion…), de modo que un bot ocioso se distingue en la métrica de
// uno colgado o muerto.
//
// Ese latido no es una convención que cada arquetipo deba recordar: los
// arquetipos con varias etapas (productores primarios y transformador) envuelven
// su Decide en base.pass, que ANOTA el motivo del no-op de cada etapa
// (base.idle: awaiting_inputs, awaiting_fuel, sell_already_active, queue_full,
// no_buy_on_board…) y, si la pasada termina sin ninguna decisión, emite UNA
// wait con el motivo más aguas arriba y el detalle de todos. Así el latido de
// CADA bot está en ii_bot_decisions_total y un bot atascado se detecta con una
// alerta de tasa, sin volcar goroutines.
package bots
