// Package bots implementa la población de bots del Incremento 4 (ADR-024,
// GDD §13/§15.4): los ARQUETIPOS v1 de reglas fijas auditables que validan el
// loop económico (productor de carbón, productor de hierro y comerciante) y
// el ORQUESTADOR del Bot Orchestration Service (cmd/bots).
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
// diseño (GDD §13).
package bots
