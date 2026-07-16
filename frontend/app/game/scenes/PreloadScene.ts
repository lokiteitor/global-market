/**
 * game/scenes/PreloadScene.ts — carga de assets de juego (FAD §11.4, §14).
 *
 * Única escena que toca el Loader de Phaser: carga el atlas pak128 (frames
 * recortados de los tilesheets de Simutrans por `npm run build:assets`) y su
 * manifiesto de anclajes, y arranca la WorldScene. Los assets de juego viven
 * en public/ (URL estable en runtime), NUNCA en el build de Vite (FAD §14.1).
 *
 * Fail-visible: si un asset falla se loguea y la WorldScene arranca igual —
 * los sprites sin textura se ven como el placeholder de Phaser, no rompen.
 */
import Phaser from 'phaser'
import { WORLD_SCENE_KEY } from './WorldScene'

export const PRELOAD_SCENE_KEY = 'preload'

/** Clave del atlas pak128 en el Texture Manager / JSON cache. */
export const PAK_ATLAS_KEY = 'pak128'
export const PAK_META_KEY = 'pak128-meta'

const BASE_URL = '/game/pak128'

export class PreloadScene extends Phaser.Scene {
  constructor() {
    super(PRELOAD_SCENE_KEY)
  }

  preload(): void {
    this.load.on('loaderror', (file: Phaser.Loader.File) => {
      // eslint-disable-next-line no-console
      console.error(`[game] error cargando asset '${file.key}' (${file.url}); ¿falta npm run build:assets?`)
    })
    this.load.atlas(PAK_ATLAS_KEY, `${BASE_URL}/atlas.png`, `${BASE_URL}/atlas.json`)
    this.load.json(PAK_META_KEY, `${BASE_URL}/meta.json`)
  }

  create(): void {
    this.scene.start(WORLD_SCENE_KEY)
  }
}
