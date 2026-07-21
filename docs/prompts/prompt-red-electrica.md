# Prompt — Red eléctrica regional (GDD §5.8, Fase 3)

> Copiar íntegro como instrucción inicial del agente.

---

Trabajas en el monorepo **Imperio Industrial** (`/home/ddelgado/git/lab/global-market`), un MMO de simulación económica con backend Go, PostgreSQL 18 y cliente Nuxt. Vas a implementar **el último elemento diferido del diseño**: la red eléctrica regional. El GDD la aparcó explícitamente para la Fase 3 **conservando su especificación íntegra** (§5.8); tu trabajo es materializarla sin reinterpretarla.

## Antes de escribir una línea, lee

- `docs/gdd.md` **§5.8** (la especificación normativa: léela entera y respétala al pie de la letra), más §5.5 (sinks y faucets), §6.2 (consumo energético por edificio), §5.9 (insolvencia: parada progresiva, nunca deuda), §11 (infraestructura, huella y mantenimiento).
- `docs/arquitectura_imperio_industrial.md` (SAD v1.1) — fronteras de módulo, monolito modular, outbox.
- `docs/documentacion_base_de_datos.md` — modelo de datos y, sobre todo, **la regla de oro del ledger**.
- `docs/adr/` completo, en especial **ADR-020** (migraciones manuales), **ADR-018** (UUIDv7), **ADR-022** (`world_source` como contrapartida física del stock) y **ADR-019** (geometría planar en metros).
- Código que es tu plantilla: `internal/balancer/` (**el modelo más cercano a lo que vas a construir**: recalcula curvas, corre workers periódicos en el engine y publica en el mercado por un port), `internal/world/production/` (consumo de combustible in situ, pausas), `internal/contracts/` (patrón sqlc + `RunSerializable` + `outbox.Emit` en la misma transacción), `internal/platform/`, `backend/db/migrations/` (16 migraciones; la tuya será la 0017).

## Qué construir (especificación del GDD §5.8, no la inventes)

**Centrales eléctricas construibles**
- **Térmicas a combustible físico**: consumen carbón/fuel entregado por logística (el combustible ya es un bien de mercado con almacén local por edificio). Sin combustible → no despachan.
- **Hidroeléctricas**: emplazamiento restringido (ríos/agua), sin combustible.
- Son `world.building_types` con su propia lógica de generación; respetan las reglas de emplazamiento server-side que ya existen (`placement_rules`, validación PostGIS).

**Líneas de transmisión**
- Con **huella espacial** y **coste de mantenimiento** (sink periódico, como el resto de infraestructura).
- Conectan generadores y consumidores dentro de una **región**. Explícitamente **fuera de alcance**: flujos de potencia realistas, pérdidas por distancia, almacenamiento (baterías) e **interconexiones interregionales** — son expansión futura (§22). No las implementes.

**Mercado spot regional por orden de mérito** (el corazón)
- Cada región tiene un **tick de mercado spot** (el GDD lo asigna al Economy Balancer): los generadores ofertan capacidad a un precio; se ordenan por mérito (precio ascendente) y se despachan hasta cubrir la demanda agregada de la región.
- **El precio de cierre lo pagan todos los despachados** (precio marginal uniforme, no pay-as-bid). Esta es la regla explícita del GDD: respétala.
- La demanda agregada sale del consumo eléctrico de los edificios conectados de esa región.

**Déficit: recorte rotatorio por prioridad inversa de precio**
- Si la oferta no cubre la demanda, se recorta a los consumidores **empezando por los que menos pagan**, y el recorte **rota** entre ciclos para no castigar siempre a los mismos.
- Efecto visible para el jugador: sus edificios sin electricidad **pausan producción**, exactamente igual que hoy pausan sin combustible (`paused_no_fuel`). Reutiliza esa mecánica; si necesitas un estado nuevo, **exige migración del enum** y justifícalo.

**Convivencia con el modelo actual (decisión que debes tomar y documentar)**
Hoy la energía es **combustible físico consumido in situ** (decisión v1.2 #29) y la producción funciona así en todo el juego. La red eléctrica **no debe romper** ese modelo el día que se active. Analiza y propón: ¿qué edificios pasan a consumir electricidad y cuáles siguen quemando combustible? ¿Convive un edificio con ambas fuentes? ¿La activación es por región o global? **Escribe un ADR con tu decisión antes de implementar** y actualiza el GDD si el diseño se afina (el GDD es normativo: cualquier desviación se registra, no se improvisa).

## Reglas duras de este proyecto

- **Todo el valor económico pasa por el ledger de doble entrada.** Los pagos del mercado spot son asientos reales (consumidores → generadores), y las invariantes viven en SQL: no-negatividad, doble entrada balanceada por activo, append-only. Nada de dinero fuera del libro. El combustible que quema una térmica se asienta como `consumption` contra `world_source` (ADR-022).
- **Migraciones SQL a mano** en `backend/db/migrations` (`NNNN_nombre.up.sql` + `.down.sql`, reversible). **NUNCA edites una migración ya aplicada** (rompe los checksums del runner): si necesitas aclarar algo, va al documento de datos. sqlc **solo** genera código de queries.
- **UUIDv7** para todos los identificadores; **geometrías planas SRID 0** en metros de mundo.
- **Dinero y stock**: `int64` en Go, `BIGINT` en SQL, **string** en JSON. Jamás floats en magnitudes económicas.
- **Motor event-driven**: el coste es proporcional a los eventos, no a las entidades. El tick del spot es un **evento recurrente de baja frecuencia**, no un bucle por entidad. Magnitudes continuas derivadas analíticamente.
- **Fronteras de bounded context**: sin imports cruzados entre módulos de dominio. Si la red eléctrica necesita hablar con otro contexto, hazlo por **outbox** (eventos) o por un **port que define el consumidor** y cablea el composition root (`cmd/engine`). El Balancer publicando en el mercado por un port es tu ejemplo a copiar.
- **Toda mutación de valor** dentro de `db.RunSerializable` con su `outbox.Emit` en la **misma transacción**.
- **API First**: si expones endpoints (catálogo de centrales, estado de la red, precio spot, mi consumo), primero el contrato `docs/api/openapi.yaml` con **bump de versión y changelog**, luego la implementación, y `npm run gen:api` en el frontend.
- **Observabilidad**: logging estructurado con `slog` y métricas Prometheus (precio spot por región, capacidad despachada, déficit, recortes, combustible quemado).

## Definition of Done

Desde `backend/`: `go build ./...` · `go vet ./...` · `gofmt -l .` vacío · `make backend-generate` **sin drift** · `go test ./...` **verde** con `II_TEST_DATABASE_URL` apuntando al PostgreSQL de desarrollo (los tests de integración crean bases efímeras). Además: `make contract-lint` y el typecheck del frontend si tocaste el contrato; tu migración **down + up** probada; y una **prueba E2E de proceso** que demuestre el ciclo completo: construir una térmica → abastecerla de carbón por logística → conectar consumidores → el tick del spot despacha por mérito y cobra el precio de cierre → provocar un déficit y observar el **recorte rotatorio** pausando producción → verificar que **el ledger cuadra por activo** y que ningún saldo quedó negativo.

Documentación sincronizada en el mismo cambio: ADR de tus decisiones, `docs/documentacion_base_de_datos.md` (nueva versión con las tablas y los asientos), SAD (el servicio materializado) y GDD si afinaste el diseño.

## Cómo levantar el entorno

```bash
make dev        # PostgreSQL 18 + PostGIS + observabilidad + migraciones + seed
make worldgen   # mundo multi-región (opcional pero útil: 9 regiones)
make backend    # gateway :8080 + engine :8081
make bots       # economía viva con los 5 arquetipos
```

`make help` lista todo. Migraciones: `make migrate-create name=electric_grid`, `make migrate-up`, `make migrate-status` (verifica checksums), `make reset-db`.

## Advertencia final

Esta es la última pieza grande del diseño y toca el corazón económico del juego. **Si algo del GDD §5.8 te parece inconsistente con lo ya construido, detente, explica el problema, propón alternativas y espera confirmación** antes de cambiar arquitectura. No improvises sobre el diseño normativo: regístralo.
