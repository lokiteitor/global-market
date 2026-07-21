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
| `full` | gateway, engine, frontend, caddy (+ core) | Stack completo en Docker (`make run`). El flujo de desarrollo habitual sigue siendo `make dev` + `make backend` + `make frontend` en el host. |
| `stress` | stress | Harness de stress test contra un entorno **NO productivo** (`make stress-docker`). Efímero: corre una carga, escribe su informe y termina. Ver [Stress test](#stress-test-perfil-stress-gdd-134-modo-3). |

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
| backend build | `golang:1.25` → runtime `gcr.io/distroless/static-debian12:nonroot` (el tag debe cubrir la directiva `go` de `backend/go.mod`: la imagen oficial fija `GOTOOLCHAIN=local`) |
| frontend build/runtime | `node:22-alpine` |

## Puertos

| Puerto (host) | Servicio | Notas |
|---|---|---|
| 5432 | PostgreSQL | expuesto para `make backend` / `make migrate-*` desde el host |
| 9090 | Prometheus | UI y API de queries |
| 3001 | Grafana | el 3000 lo ocupa el frontend Nuxt en modo dev |
| 80 | Caddy (perfil full) | `/api/*` → gateway:8080, resto → frontend:3000; WebSockets proxyados por defecto |
| 8080 / 8081 | gateway / engine | en perfil full **solo** en la red interna de compose (detrás de Caddy); en dev los procesos corren en el host |
| 8082 | bots (Bot Orchestration Service) | `/healthz`, `/readyz`, `/metrics` del proceso `make bots` en el host (job `bots`) |
| 8083 | stress (harness) | `/healthz`, `/readyz`, `/metrics` de la corrida de stress, en el host (`make stress`) o en el perfil `stress` (job `stress`) |

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
  goroutines, memoria, estado de targets) con las filas **Bots**, **Notification
  Gateway WS**, **Densidad dinámica de bots** (`ii_bots_active`,
  `ii_bots_density_target`, ajustes y señales observadas — la válvula de carga
  del GDD §19) y **Stress test** (throughput, latencia p95, errores por clase,
  población del harness y `ii_outbox_lag_observed`).

## Stress test (perfil `stress`, GDD §13.4 modo 3)

> **AVISO — ENTORNO SEPARADO.** El harness de stress conecta cientos o cientos
> de miles de bots contra las **mismas APIs públicas** que juegan los humanos
> (ADR-010, GDD §15.4) y crea cuentas y capital reales en la base de datos a la
> que apunta. El GDD §13.4 es explícito: este modo corre en un **entorno de
> pruebas independiente y NUNCA toca el mundo de producción**. Es para validar
> escalabilidad y balance **antes** de desplegar.

La salvaguarda está en el binario ([`internal/stress/safety.go`](../backend/internal/stress/safety.go)),
no en la infraestructura, y se aplica **antes** de abrir el pool de BD:

1. `II_STRESS_API_URL` es **obligatoria y no tiene valor por defecto**: elegir
   el target es siempre una decisión consciente del operador.
2. Rehúsa arrancar si `II_ENV` vale `prod`, `production`, `prd` o `live`.
3. El host de la API **y** el de la BD del provisioner deben casar la allowlist
   de entornos no productivos (`II_STRESS_ALLOW_HOSTS`, que **sustituye** a la
   de por defecto: `localhost`, `*.localhost`, `127.0.0.1`, `::1`,
   `host.docker.internal`, `stress.*`, `*.stress.*`, `staging.*`, `*.staging.*`).

Además, toda cuenta que crea lleva el prefijo reconocible `stress-<run_id>-…` y
al terminar se marca retirada (`II_STRESS_CLEANUP=true` por defecto); el ledger
es append-only y no se borra nada.

### Cómo correrlo

```bash
# 1. Entorno de pruebas listo (BD + esquema + seed) y backend en marcha
make dev && make backend          # gateway :8080 + engine :8081 en el host

# 2a. Harness en el host (informe JSON en backend/stress-report.json)
II_STRESS_API_URL=http://localhost:8080/api/v1 II_STRESS_BOTS=500 make stress

# 2b. Harness en Docker (perfil "stress")
#     - stack DENTRO de compose (make run):      http://gateway:8080/api/v1
#     - stack en el HOST (make backend):         http://host.docker.internal:8080/api/v1
II_STRESS_API_URL=http://gateway:8080/api/v1 make stress-docker
docker cp imperio-stress:/tmp/stress-report.json .   # informe de la corrida
```

Sin `II_STRESS_API_URL` el contenedor arranca y **se niega** con el error
documentado: es el comportamiento esperado, no un fallo de configuración.

> **Contenedor → host**: la variante `host.docker.internal` depende de que el
> cortafuegos del host acepte tráfico desde la interfaz `docker0`. Si el harness
> reporta `network`/`i/o timeout` en todas las peticiones pero el gateway
> responde desde el host, es el cortafuegos: abre el puerto para `docker0` o
> apunta el harness al gateway **dentro** de compose (`gateway:8080`), que es lo
> que el perfil espera por defecto. La BD (`postgres:5432`) siempre viaja por la
> red de compose y no se ve afectada.

| Variable | Default | Qué hace |
|---|---|---|
| `II_STRESS_API_URL` | — (**obligatoria**) | Raíz de la API del entorno de pruebas, con prefijo de versión |
| `II_STRESS_BOTS` | 200 | Bots de la corrida (tope 200 000 por instancia) |
| `II_STRESS_RAMP` / `II_STRESS_DURATION` / `II_STRESS_TICK` | 30s / 120s / 1s | Rampa de entrada, duración de la carga y periodo de acción |
| `II_STRESS_MIX` | `producer=50,trader=30,freighter=10,transformer=10` | Mezcla por arquetipo |
| `II_STRESS_WRITE_RATIO` | 0.3 | Fracción de acciones de escritura |
| `II_STRESS_STOCK_ENDOWMENT` / `II_STRESS_SELL_SHARE` | 10000 / 0.5 | Dotación de stock por cuenta y fracción de publicaciones `sell`: habilitan el **lado vendedor** del harness. Sin ellas solo puede publicar `buy` y la operación `accept` depende de ofertas ajenas (se degrada a cero al escalar) |
| `II_STRESS_ADDR` | `:8083` | `/healthz`, `/readyz`, `/metrics` del harness (job `stress`) |
| `II_STRESS_REPORT` | `stress-report.json` | Ruta del informe JSON (en el contenedor, `/tmp/stress-report.json`) |
| `II_STRESS_ALLOW_HOSTS` | ver arriba | Allowlist de hosts NO productivos (sustituye a la default) |
| `II_STRESS_DATABASE_URL` | `II_DATABASE_URL` | BD del entorno de pruebas que usa el provisioner |

**Escala horizontal** (GDD §15.4): el harness es un proceso desacoplado, así que
la forma de generar más carga es levantar más instancias contra las mismas APIs
(`docker compose --profile stress up --scale stress=N`, con un
`II_STRESS_RUN_ID` distinto por instancia para no compartir prefijo de cuentas).

**Códigos de salida**: `0` corrida sana · `1` error de configuración o de
ejecución (incluida la salvaguarda) · `2` veredicto NEGATIVO (hubo 5xx o errores
inesperados). `make stress-docker` propaga el código del contenedor.

**Observabilidad**: Prometheus trae el job `stress` con los dos targets
(`host.docker.internal:8083` y `stress:8083`); fuera de una corrida
`up{job="stress"}=0` es lo normal. La fila *Stress test* del dashboard muestra
throughput, latencia p95, errores por clase, población del harness y el lag del
outbox.

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
