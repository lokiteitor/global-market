# Imperio Industrial

MMO de simulación económica, industrial y logística en un mundo único persistente.
Monorepo sin workspaces: cada componente vive en su carpeta y se orquesta con el
`Makefile` raíz.

```
backend/
  engine/        Motor de simulación (Go): sim-time, producción, tránsito,
                 contratos CCRI (sorteo/liquidación), logística, balancer, outbox.
  gateway/       API pública REST + WebSocket (TypeScript + Fastify).
  bots/          Bot Orchestration Service (consume la API pública como un cliente).
  migrations/    Migraciones SQL (PostgreSQL 18, uuidv7 nativo). Aplicación MANUAL.
  seeds/         Seed del mundo inicial.
frontend/        Cliente web (Nuxt 4 + Vue 3 + Phaser 3 + Pinia).
infra/           docker-compose (PostgreSQL 18 + PostGIS, Caddy) y edge.
docs/            Documentación viva (GDD/SAD, arquitectura, BD, FAD, desarrollo).
specs/           Contrato OpenAPI y especificación DDL — fuente normativa.
```

## Arranque rápido

```bash
make up            # PostgreSQL 18 + Caddy
make db-migrate    # aplica migraciones (manual, nunca automático)
make db-seed       # mundo inicial
make engine-run    # motor de simulación
make gateway-dev   # API REST + WS en :8080
make frontend-dev  # cliente en :3000  (edge unificado en http://localhost:8000)
```

Alternativa contenedorizada: `make stack-build && make db-migrate && make db-seed && make stack-up`
(perfil `full` del compose; las migraciones siguen siendo manuales).

`make help` lista todos los comandos. Ver `docs/desarrollo.md` para el detalle.



# Como usar el proyecto

Hay dos formas de levantarla:

1. Todo en contenedores (perfil full)

make up            # infraestructura: PostgreSQL 18 + Caddy (edge)
make db-migrate    # migraciones — SIEMPRE manuales, nunca automáticas
make db-seed       # mundo inicial
make stack-build   # construye las 4 imágenes de aplicación
make stack-up      # engine + gateway + bots + frontend en contenedores

Y juegas en http://localhost:8000 (login: Aurora Corp / aurora). Para parar solo la aplicación (dejando base de datos y edge): make stack-down.

2. Modo desarrollo (híbrido)

Base de datos y Caddy en Docker, y los servicios en el host con recarga para iterar:

make up && make db-migrate && make db-seed
make engine-run      # terminal 1
make gateway-dev     # terminal 2 (tsx watch)
make frontend-dev    # terminal 3 (HMR de Nuxt)
make bots-run        # opcional: economía viva

El punto de entrada es el mismo (:8000 vía Caddy) porque el edge enruta /api/* y /ws al gateway y el resto al frontend, esté en contenedor o en el host — los dos publican los mismos puertos (8080 y 3000).