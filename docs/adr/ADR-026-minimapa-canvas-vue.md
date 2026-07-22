# ADR-026 — Minimapa como Canvas 2D en la capa Vue

- **Estado:** Aceptada (2026-07-21)
- **Ámbito:** frontend (cliente jugable /play)
- **Relacionadas:** ADR-019 (vista top-down), FAD §11.4/§11.5/§15.11/§16.9

## Contexto

El FAD v1.1 (§15.11, §16.9, §11.4) describe el minimapa como **marco Vue +
contenido Phaser**: una `MinimapScene` paralela con cámara propia renderizando
a `RenderTexture` embebida en el componente. A la vez, §16.9 fija el requisito
de fondo: el minimapa es una **vista agregada por región** (coropleta de biomas
+ puntos de interés propios + rect del viewport) construida con datos de bajo
detalle — *"ver el minimapa no debe implicar cargar el mundo"* (ni sus chunks).

Al implementarlo (Incremento 15, mundo multi-región) se constató que **todo el
contenido exigido ya vive en las stores de la capa Vue** (`Region.boundsM` y
biomas, ciudades, edificios propios) y que lo único que aporta el motor es la
posición de la cámara. Nada del contenido requiere el pipeline GL.

## Opciones

**(A) MinimapScene + RenderTexture (FAD literal).**
- (+) Cumple el FAD al pie de la letra; un solo pipeline de render.
- (−) Alineación frágil marco-DOM ↔ rect del canvas bajo `Phaser.Scale.RESIZE`.
- (−) Enrutado de input: los clics del área del minimapa no deben llegar al mundo.
- (−) El motor necesitaría un camino nuevo de derivación SIN culling (mundo completo).
- (−) Intesteable en vitest (exige GPU/canvas GL).

**(B) Canvas 2D propio en `MinimapPanel.vue` (elegida).**
- (+) El contenido es exactamente el modelo agregado que ya está en las stores;
  cero acople nuevo con el motor salvo **un evento** (`camera`, throttled ~5 Hz,
  `game/camera-throttle.ts` puro) y el comando inverso ya existente
  (`mapui.requestCenterOn` → `WorldLive.centerOnM`).
- (+) Testeable en vitest (transformación pura en `minimap-math.ts` + test de
  componente); ~40 rects y puntos repintados a ≤5 Hz con coalescing rAF: coste
  despreciable.
- (−) Desviación del mecanismo prescrito por el FAD (RenderTexture).
- (−) Duplicación puntual de la paleta de biomas en Vue (`MINIMAP_BIOME_COLORS`
  espejo de `BIOME_COLORS`), con comentario cruzado en ambos ficheros — mismo
  precedente aceptado que la duplicación de `BiomeName` en `game/textures.ts`.

## Decisión

Minimapa como **componente Vue con Canvas 2D propio**
(`app/components/play/minimap/MinimapPanel.vue` + `minimap-math.ts`), alimentado
por stores y por el evento `camera` del mundo vivo. El FAD §15.11/§16.9 queda
anotado con la referencia a este ADR.

## Criterio de reversión

Si el minimapa llega a necesitar **contenido de render real** (rutas activas
dibujadas, congestión pintada por segmento, terreno por tile), se migra a la
`MinimapScene`/`RenderTexture` original **sin cambiar el contrato Vue**: el
marco, el toggle, `mapui.cameraViewM` y los comandos de cámara se conservan; lo
único que cambia es la fuente del bitmap.
