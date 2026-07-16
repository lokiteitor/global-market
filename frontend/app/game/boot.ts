/**
 * game/boot.ts — arranque del juego Phaser (FAD §11.2, O7).
 *
 * IMPORTANTE Nuxt/SSR: Phaser se importa SOLO client-side y de forma
 * PEREZOSA — `import('phaser')` / `import('./scenes/…')` dinámicos dentro de
 * createGame(). Este módulo, a su vez, solo se alcanza vía import() dinámico
 * desde GameCanvasHost en onMounted: el bundle de game/ jamás se evalúa en
 * SSR ni encarece el arranque del portal (login/lobby).
 */
import { DEFAULT_PALETTE } from './palette'
import type { GameDeps, WorldBbox, WorldRenderer } from './types'
import type { WorldScene } from './scenes/WorldScene'

export interface CreatedGame {
  /** Instancia Phaser.Game (destroy(true) al desmontar el host). */
  game: import('phaser').Game
  /** La WorldScene como puerto WorldRenderer + viewport para el bridge/host. */
  renderer: WorldRenderer & { getViewportBbox(): WorldBbox }
}

export async function createGame(canvasParent: HTMLElement, deps: GameDeps): Promise<CreatedGame> {
  // Carga perezosa client-only: nada de esto se evalúa en el servidor.
  const [{ default: Phaser }, { WorldScene }, { OverlayScene }] = await Promise.all([
    import('phaser'),
    import('./scenes/WorldScene'),
    import('./scenes/OverlayScene')
  ])

  const palette = deps.palette ?? DEFAULT_PALETTE
  const world = new WorldScene(deps)
  const overlay = new OverlayScene()
  overlay.setPalette(palette)

  // El renderer está listo cuando la escena de mundo ha hecho create().
  // Se engancha ANTES de construir el juego: Phaser arranca las escenas de
  // forma asíncrona tras `new Phaser.Game()`, y hasta entonces `world.events`
  // aún es undefined (se crea en sys.init). Por eso la escena avisa vía
  // onReady en lugar de escuchar world.events.once(CREATE) —esto último
  // reventaba con "can't access property once, events is undefined".
  const ready = new Promise<void>((resolve) => {
    world.onReady = resolve
  })

  const game = new Phaser.Game({
    type: Phaser.AUTO,
    parent: canvasParent,
    // Fondo = token oscuro ($color-bg-deep) pasado como config, no Sass.
    backgroundColor: palette.background,
    render: { roundPixels: true, antialias: true },
    scale: {
      mode: Phaser.Scale.RESIZE,
      width: '100%',
      height: '100%'
    },
    // WorldScene arranca sola (primera); ella lanza la OverlayScene.
    scene: [world, overlay]
  })

  await ready

  return { game, renderer: world as WorldScene }
}
