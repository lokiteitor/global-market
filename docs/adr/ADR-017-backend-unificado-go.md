# ADR-017 — Backend unificado en Go (gateway incluido)

| Campo | Valor |
|---|---|
| **ID** | ADR-017 |
| **Fecha** | 2026-07-16 |
| **Estado** | Aceptado |
| **Deroga** | GDD Anexo B decisión #19 (dual stack Go + TypeScript, ratificada en v1.2) y ADR-005 en su parte de "dos stacks backend"; la regla de oro del ledger de ADR-005 **permanece intacta** |

## Contexto

El SAD v1.0 asignaba el gateway (API pública REST, auth, Notification/Event Gateway WebSocket) a TypeScript + Fastify + Drizzle, y el motor + Contract Service a Go. El mandato de proyecto establece que **todo el backend se implementa en Go**. Mantener dos stacks duplica toolchains, pipelines, convenciones y superficie de dependencias sin beneficio equivalente una vez que el equipo opera un solo lenguaje de servidor.

## Decisión

1. **Todo el código de servidor es Go**: gateway (REST + WebSocket + auth/sesiones), motor de simulación (shards, producción, tránsito), Contract Service, Logistics Service, Economy Balancer, jobs de plataforma, SDK de bots y bots.
2. Se conservan las **fronteras de despliegue** del monolito modular (ADR-008/ADR-013): binarios separados `cmd/gateway` y `cmd/engine` (más `cmd/migrate`, y `cmd/bots` cuando llegue su fase), módulos internos tras interfaces sin imports cruzados entre bounded contexts.
3. **Fastify y Drizzle ORM desaparecen del proyecto.** El esquema `auth` pasa a ser propiedad del módulo Go del gateway; sus migraciones siguen el flujo único de ADR-020.
4. Stack HTTP: **`net/http` de la librería estándar** (ServeMux con métodos y wildcards, Go ≥1.22). Middleware propio y pequeño (envelope, errores, rate limit, métricas, logging). No se introduce framework web.
5. Dependencias backend permitidas inicialmente (cada una justificada por requisito):
   - `github.com/jackc/pgx/v5` — driver PostgreSQL (pooling, tipos nativos uuid).
   - `github.com/google/uuid` — generación UUIDv7 en aplicación (ADR-018).
   - `github.com/prometheus/client_golang` — métricas (Observability by Default).
   - `golang.org/x/crypto` — argon2id para credenciales (Security by Default).
   - `sqlc` (build-time, no runtime) — codegen tipado de queries SQL (ADR-020).
   - Librería WebSocket: **se decidirá en el ADR del protocolo del Notification Gateway** (Incremento 4), no antes (YAGNI).
6. Logging estructurado con `log/slog` (JSON); ningún logger de terceros.

## Consecuencias

- (+) Un toolchain, un modelo de concurrencia, un pipeline de CI para todo el servidor; menos dependencias.
- (+) La igualdad de API literal humanos/bots se refuerza: el SDK de bots y el gateway comparten tipos generados del mismo contrato.
- (−) Se pierde la afinidad natural TS↔Fastify para el fan-out WebSocket; Go la cubre con goroutines/canales sin coste conceptual adicional.
- (−) El FAD pierde la opción de `packages/` compartidos con el gateway TS (§10.7); los tipos del cliente se generan del contrato OpenAPI (ADR-021).
- La decisión #19 del GDD queda derogada y registrada en el Anexo B v1.3.
