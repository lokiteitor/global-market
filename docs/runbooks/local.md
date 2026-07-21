# Runbook — entorno local

## Arranque desde cero

```bash
make dev        # postgres18+postgis (healthy) + prometheus + grafana + migraciones + seed
make worldgen   # (opcional) mundo multi-región procedural Fase 2 — aditivo sobre el seed
make backend    # terminal 2: gateway :8080 + engine :8081
make frontend   # terminal 3: Nuxt dev en :3000 (proxy /api → :8080)
```

Login de desarrollo: corporación `Demo` / secret `demo-secret-dev` (configurable con `II_SEED_DEMO_NAME` / `II_SEED_DEMO_SECRET`; el seed es idempotente y no re-emite capital).

### Mundo multi-región (`make worldgen`, opcional — Incremento 7)

`make worldgen` genera proceduralmente el **mundo Fase 2** (multi-región + ferroviario/marítimo) **encima** del seed: es **aditivo** (conserva intacta la región raíz Askadia (0,0)), **determinista** (misma `II_WORLD_SEED` ⇒ mismo mundo) e **idempotente** (re-ejecutarlo no duplica). **Requiere el seed corrido antes** (banco central, reloj, catálogo mínimo `iron_ore`/`coal`); si falta, aborta con un mensaje claro. Sin él, el entorno arranca con el mundo mínimo de una región (Fase 1) — `make worldgen` es opcional para desarrollo. Vuelca a stdout un resumen (grilla, biomas por celda, ciudades/yacimientos/enlaces/terminales creados). Configuración (defaults del mundo canónico):

| Variable | Default | Efecto |
|---|---|---|
| `II_WORLD_SEED` | `42` | Semilla del mundo (misma semilla ⇒ mismo mundo). |
| `II_WORLD_GRID` | `3` | Lado de la grilla de macro-regiones, **impar**, centrada en (0,0) (3 = las 8 regiones que rodean a Askadia). |
| `II_WORLD_REGION_SIZE_M` | `50000` | Lado en metros de cada región cuadrada (SRID 0 planar). |

### Corrida de stress (`make stress`, opcional — Incremento 9)

Carga masiva contra el stack local por la **misma API pública** (GDD §13.4 modo 3). Requiere la BD y el backend vivos (`make dev` + `make backend`).

```bash
II_STRESS_API_URL=http://localhost:8080/api/v1 II_STRESS_BOTS=200 make stress
# en contenedor (perfil compose `stress`): II_STRESS_API_URL=http://host.docker.internal:8080/api/v1 make stress-docker
```

- **Salvaguarda**: sin `II_STRESS_API_URL` no arranca (no hay default), y rehúsa si `II_ENV` es de producción o si el host de la API **o el de la BD** no casan la allowlist (`II_STRESS_ALLOW_HOSTS`; por defecto `localhost`/`127.0.0.1`/`::1`/`host.docker.internal`/`*.stress.*`/`staging.*`). Nunca se lanza contra el mundo real.
- **Informe**: `backend/stress-report.json` (la ruta de `II_STRESS_REPORT` es relativa al directorio de trabajo, y `make stress` corre desde `/backend`) + resumen por consola. Salida `0` sana · `1` configuración/ejecución · `2` veredicto negativo (5xx o errores inesperados). Cómo leerlo y qué umbrales vigilar: `docs/guias/desarrollo.md` § *Stress test*.
- **Observabilidad**: `:8083/metrics` (`ii_stress_*`) mientras dura la corrida; al terminar, el proceso se apaga.
- **Limpieza**: automática — las cuentas del run (`stress-<run_id>-…`) se marcan retiradas al terminar. Con `II_STRESS_CLEANUP=false` quedan activas a propósito (para inspeccionar); bórralas del entorno con `make reset-db` + `make seed`, o identifícalas por su prefijo (`SELECT ... FROM auth.accounts WHERE name LIKE 'stress-%'`). El ledger es append-only: nada se borra de la contabilidad, y las publicaciones vivas que el cooldown impidió cancelar expiran solas por su TTL.

## Endpoints y puertos

| Qué | Dónde |
|---|---|
| API pública | `http://localhost:8080/api/v1/...` (en dev también vía `:3000/api/v1` por el proxy) |
| Salud | `:8080/healthz`, `:8080/readyz`, `:8081/healthz` (fuera de `/api/v1`) |
| Métricas | `:8080/metrics` (gateway: HTTP síncrono, incl. Logistics Service `ii_route_plans_total`/`ii_routes_created_total`/`ii_route_plan_duration_seconds`), `:8081/metrics` (engine: workers de tránsito/producción/CCRI, reconciliación, congestión). Prometheus scrapea **ambos**. |
| Bots / stress | `:8082` (orquestador `make bots`: `ii_bot_*`, `ii_bots_density_*`, `ii_outbox_lag_observed`) · `:8083` (harness `make stress`, solo mientras dura la corrida: `ii_stress_*`) |
| PostgreSQL | `localhost:5432`, BD `imperio`, usuario/clave `imperio`/`imperio` |
| Prometheus / Grafana | `:9090` / `:3001` (admin/admin) |

## Diagnóstico rápido

- **`readyz` devuelve 503** → la BD no responde: `docker ps` (contenedor `imperio-postgres` debe estar `healthy`), `make infra-core`, revisa `II_DATABASE_URL`.
- **`migrate-status` falla por checksum** → una migración aplicada fue editada. Nunca se edita una migración aplicada: crea una nueva (`make migrate-create name=fix_x`). En dev puedes `make reset-db`.
- **401 UNAUTHORIZED en todo** → token expirado (24 h) o sesión cerrada: re-login. **429 RATE_LIMITED** → respeta `Retry-After` (límites idénticos para humanos y bots por diseño).
- **El sim-time no avanza** → el engine no corre o el reloj está `frozen`: `curl :8081/metrics | grep ii_sim_clock_frozen`; el ancla vive en `world.sim_clock` (fila única id=1).
- **El frontend no ve la API** → el gateway no está en :8080 o el proxy dev no arrancó: reinicia `make frontend` con el gateway ya levantado.
- **El HUD queda en «Reconectando…»** → el WS no conecta. En dev el cliente conecta el WS DIRECTO a `ws://localhost:8080/api/v1/ws` (el devProxy de Nitro no proxya upgrades WebSocket) y el gateway debe permitir el origen del dev server: `II_WS_ALLOWED_ORIGINS=localhost:3000` (lo exporta `scripts/run-backend.sh`; si arrancas `go run ./cmd/gateway` a mano, expórtalo tú).
- **`make stress` rehúsa arrancar** → es la salvaguarda, no un fallo: falta `II_STRESS_API_URL` (obligatoria, sin default), `II_ENV` declara producción, o el host de la API/BD no está en la allowlist. El mensaje dice cuál de los tres y cita la regla del GDD §13.4.
- **La población de bots baja sola** → es la densidad dinámica recortando por carga (`II_BOTS_DENSITY_*`): mira el log `bots: densidad ajustada` (trae señales y factores) y `ii_outbox_lag_observed`. Un consumidor de outbox parado se observa como saturación creciente. Con `II_BOTS_DENSITY_ENABLED=false` la población queda fija.
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
