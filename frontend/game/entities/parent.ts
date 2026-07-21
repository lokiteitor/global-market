/**
 * game/entities/parent — contenedor de destino de un renderer.
 *
 * Los renderers añaden sus objetos a un padre inyectado (Layer o Container de
 * Phaser: ambos cumplen `add`). El mundo vivo crea un Container POR RENDERER
 * dentro de la capa que le toca: el orden de containers dentro de la capa fija
 * el z-order relativo (enlaces bajo nodos, edificios bajo bordes…), y los
 * pools pueden crear objetos tarde sin romperlo.
 */

import type Phaser from 'phaser'

export interface RenderParent {
  add(child: Phaser.GameObjects.GameObject): unknown
}
