/**
 * game/deps — dependencias que la fase UI inyecta al crear el juego (FAD §11.6).
 *
 * El motor no lee stores ni la red: recibe funciones puras de consulta sobre
 * el estado replicado. Fichero propio (sin Phaser) para que el host/bridge
 * puedan tipar sus deps sin arrastrar el motor.
 */

import type { BiomeLookup } from './map/chunks'

export interface GameDeps {
  /**
   * Bioma de la región que contiene un punto del mundo (metros), o `null`
   * fuera de toda región. La fase UI lo implementa por bounds de región del
   * estado replicado (el backend aún no expone terreno por tile).
   */
  readonly biomeAtM: BiomeLookup
}
