# Runbook — entorno local

## Arranque desde cero

```bash
make dev        # postgres18+postgis (healthy) + prometheus + grafana + migraciones + seed
make backend    # terminal 2: gateway :8080 + engine :8081
make frontend   # terminal 3: Nuxt dev en :3000 (proxy /api → :8080)
```

Login de desarrollo: corporación `Demo` / secret `demo-secret-dev` (configurable con `II_SEED_DEMO_NAME` / `II_SEED_DEMO_SECRET`; el seed es idempotente y no re-emite capital).

## Endpoints y puertos

| Qué | Dónde |
|---|---|
| API pública | `http://localhost:8080/api/v1/...` (en dev también vía `:3000/api/v1` por el proxy) |
| Salud | `:8080/healthz`, `:8080/readyz`, `:8081/healthz` (fuera de `/api/v1`) |
| Métricas | `:8080/metrics` (gateway: HTTP síncrono, incl. Logistics Service `ii_route_plans_total`/`ii_routes_created_total`/`ii_route_plan_duration_seconds`), `:8081/metrics` (engine: workers de tránsito/producción/CCRI, reconciliación, congestión). Prometheus scrapea **ambos**. |
| PostgreSQL | `localhost:5432`, BD `imperio`, usuario/clave `imperio`/`imperio` |
| Prometheus / Grafana | `:9090` / `:3001` (admin/admin) |

## Diagnóstico rápido

- **`readyz` devuelve 503** → la BD no responde: `docker ps` (contenedor `imperio-postgres` debe estar `healthy`), `make infra-core`, revisa `II_DATABASE_URL`.
- **`migrate-status` falla por checksum** → una migración aplicada fue editada. Nunca se edita una migración aplicada: crea una nueva (`make migrate-create name=fix_x`). En dev puedes `make reset-db`.
- **401 UNAUTHORIZED en todo** → token expirado (24 h) o sesión cerrada: re-login. **429 RATE_LIMITED** → respeta `Retry-After` (límites idénticos para humanos y bots por diseño).
- **El sim-time no avanza** → el engine no corre o el reloj está `frozen`: `curl :8081/metrics | grep ii_sim_clock_frozen`; el ancla vive en `world.sim_clock` (fila única id=1).
- **El frontend no ve la API** → el gateway no está en :8080 o el proxy dev no arrancó: reinicia `make frontend` con el gateway ya levantado.
- **El HUD queda en «Reconectando…»** → el WS no conecta. En dev el cliente conecta el WS DIRECTO a `ws://localhost:8080/api/v1/ws` (el devProxy de Nitro no proxya upgrades WebSocket) y el gateway debe permitir el origen del dev server: `II_WS_ALLOWED_ORIGINS=localhost:3000` (lo exporta `scripts/run-backend.sh`; si arrancas `go run ./cmd/gateway` a mano, expórtalo tú).
- **Invariantes del ledger** → `./scripts/db-smoke.sh` (9/9 PASS esperado). Cualquier FAIL es bloqueante: el valor económico está comprometido.

## Reset / limpieza

```bash
make reset-db     # down de TODAS las migraciones + re-up (solo dev) — después: make seed
make infra-down   # detiene contenedores (los datos persisten en el volumen pgdata)
docker volume rm imperio_pgdata   # borrado total de datos (irreversible)
make clean        # artefactos de build
```

## Stack completo en Docker

```bash
make run          # perfil full: caddy :80 → /api/* al gateway, resto al frontend
```

## Notas operativas

- Loki/Tempo pospuestos deliberadamente (ver `infra/README.md`): logs por `docker logs` hasta que llegue el WS gateway.
- Migraciones en producción: solo dentro de la ventana de mantenimiento diaria (ADR-003), nunca en caliente.
