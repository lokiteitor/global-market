# /infra — Infraestructura local de Imperio Industrial

Todo artefacto de despliegue vive aquí (ADR-016): Docker Compose, Dockerfiles,
Caddy, Prometheus y Grafana (provisioning + dashboards). Nunca código de
negocio. El punto de entrada es siempre el **Makefile raíz**; no invoques
`docker compose` a mano salvo para depurar.

Decisiones vigentes: ver [`/docs/adr`](../docs/adr) (en particular ADR-016,
ADR-017, ADR-018 y ADR-020).

> **Nota operativa**: el nombre del proyecto Compose es `imperio` (clave `name:`
> en `docker-compose.yml`). Si alguna vez se renombra, los contenedores del
> nombre antiguo NO se limpian solos y siguen ocupando puertos del host
> (p. ej. 8080/3000); hay que bajarlos explícitamente:
> `docker compose -p <nombre-antiguo> down --remove-orphans`.

## Perfiles de Compose

| Perfil | Servicios | Uso |
|---|---|---|
| `core` | postgres | Base de datos para desarrollo local (`make dev` / `make infra-core`) |
| `obs`  | prometheus, grafana | Métricas y dashboards en desarrollo (`make infra-core` levanta core+obs) |
| `full` | gateway, engine, frontend, caddy (+ core) | Stack completo en Docker (`make run`). **Aún no operativo**: `/frontend` no existe todavía; los Dockerfiles quedan preparados. |

Flujo habitual de desarrollo: `make dev` (BD + observabilidad + migraciones +
seed) y, en terminales aparte, `make backend` (gateway + engine con `go run`
en el host) y `make frontend`. Prometheus scrapea los procesos del host vía
`host.docker.internal` (mapeado con `host-gateway`); para el perfil full,
descomenta los targets `gateway:8080` / `engine:8081` en
[`prometheus/prometheus.yml`](prometheus/prometheus.yml).

## Imágenes (tags fijados y verificados)

| Servicio | Imagen |
|---|---|
| postgres | `postgis/postgis:18-3.6` (PostgreSQL 18 + PostGIS 3.6, ADR-018) |
| prometheus | `prom/prometheus:v3.13.1` |
| grafana | `grafana/grafana:13.1.0` |
| caddy | `caddy:2` |
| backend build | `golang:1.24` → runtime `gcr.io/distroless/static-debian12:nonroot` |
| frontend build/runtime | `node:22-alpine` |

## Puertos

| Puerto (host) | Servicio | Notas |
|---|---|---|
| 5432 | PostgreSQL | expuesto para `make backend` / `make migrate-*` desde el host |
| 9090 | Prometheus | UI y API de queries |
| 3001 | Grafana | el 3000 lo ocupa el frontend Nuxt en modo dev |
| 80 | Caddy (perfil full) | `/api/*` → gateway:8080, resto → frontend:3000; WebSockets proxyados por defecto |
| 8080 / 8081 | gateway / engine | en perfil full **solo** en la red interna de compose (detrás de Caddy); en dev los procesos corren en el host |

## Credenciales de desarrollo (NUNCA producción)

| Qué | Valor |
|---|---|
| PostgreSQL superusuario | `imperio` / `imperio`, BD `imperio` |
| Usuarios de servicio | `svc_gateway`, `svc_engine`, `svc_analytics` (password = nombre de usuario) |
| Grafana | `admin` / `admin` |

Los usuarios de servicio los crea [`postgres/init/01-roles.sql`](postgres/init/01-roles.sql)
solo en el **primer arranque** del volumen `pgdata`. La membresía a los grupos
de permisos `ii_*` la otorga la **migración 0007** (runner propio, ADR-020);
en producción usuarios y membresías los gestiona operaciones.

## Grafana: provisioning

- Datasource por defecto: Prometheus (`http://prometheus:9090`), provisionado
  desde [`grafana/provisioning/datasources`](grafana/provisioning/datasources).
- Dashboards: provider de ficheros que carga todo JSON de
  [`grafana/dashboards`](grafana/dashboards) en la carpeta "Imperio Industrial".
  Incluido: **Imperio - Backend Overview** (RPS, latencia p95, errores 5xx,
  goroutines, memoria, estado de targets).

## Decisión: Loki y Tempo POSPUESTOS (Fase 0)

La arquitectura (SAD §"Observabilidad") contempla Prometheus/Grafana/Loki/Tempo.
En Fase 0 **solo** se despliegan Prometheus + Grafana, deliberadamente:

- El backend son **dos binarios** (`gateway`, `engine`) con logging estructurado
  `log/slog` en JSON (ADR-017); `docker logs` / la salida directa del proceso
  es suficiente para correlacionar en desarrollo.
- Sin tráfico distribuido real ni múltiples saltos entre servicios, el tracing
  no responde todavía a ninguna pregunta que las métricas y los logs no
  respondan (YAGNI).
- Cada componente de observabilidad añadido antes de tiempo es coste de
  operación y superficie de configuración sin consumidor.

**Disparadores para reevaluar** (vía ADR): la llegada del Notification/Event
Gateway WebSocket (Incremento 4) con su fan-out y necesidad de correlación de
eventos, o superar ~3-4 servicios desplegados de forma independiente. En ese
momento se añadirán Loki (agregación de logs) y/o Tempo (trazas) como
servicios del perfil `obs`.
