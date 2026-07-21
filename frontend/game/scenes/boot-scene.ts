/**
 * game/scenes/boot-scene — arranque del render (FAD §11.5, adaptado).
 *
 * Genera las texturas runtime (game/textures.ts) y arranca WorldScene. No hay
 * PreloadScene en v1: sin binarios de arte no hay nada que descargar (ADR-019 +
 * mandato "texturas generadas en runtime"); cuando exista pipeline de assets
 * (FAD §14) se insertará entre Boot y World sin tocar a nadie más.
 */

import Phaser from 'phaser'

import { makeWorldTextures } from '../textures'
import { WORLD_SCENE_KEY } from './world-scene'

export const BOOT_SCENE_KEY = 'Boot'

export class BootScene extends Phaser.Scene {
  constructor() {
    super({ key: BOOT_SCENE_KEY })
  }

  create(): void {
    makeWorldTextures(this)
    this.scene.start(WORLD_SCENE_KEY)
  }
}
