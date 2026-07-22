/**
 * app/stores/mapui.store — estado de UI del mapa (FAD §20.2: slice de UI).
 *
 * Modo de interacción, toggles de overlay, selección espacial y vehículo
 * seguido. NO es estado replicado (no hay tríada apply*): es estado de
 * presentación compartido entre el mundo vivo y la UI (HUD/inspector de la
 * fase siguiente). La sincronización bidireccional con `WorldLive` la hace
 * `bindWorldLive` (app/composables/useWorldLive): la store jamás importa el
 * motor en runtime — solo TIPOS del entrypoint (erasados al compilar), para no
 * arrastrar Phaser al bundle del portal (carga perezosa, FAD §21.8).
 */

import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import type { InputMode, OverlayName, SelectionRef, WorldRectM } from '~~/game'

/**
 * Comando de cámara de la UI hacia el motor (salto de región, minimapa). El
 * `seq` monótono distingue dos peticiones idénticas consecutivas (el watcher
 * de `bindWorldLive` reacciona por identidad del objeto).
 */
export type CameraCommand =
  | { readonly seq: number; readonly kind: 'center'; readonly xM: number; readonly yM: number }
  | { readonly seq: number; readonly kind: 'fit'; readonly rectM: WorldRectM }

export const useMapUiStore = defineStore('mapui', () => {
  const mode = ref<InputMode>('select')
  /** Toggles de overlay; vacío hasta que `bindWorldLive` vuelca los defaults del motor. */
  const overlays = ref<Readonly<Partial<Record<OverlayName, boolean>>>>({})
  const selection = ref<SelectionRef | null>(null)
  const followedVehicleId = ref<string | null>(null)
  const cameraCommand = ref<CameraCommand | null>(null)
  let cameraCommandSeq = 0
  /** Vista de cámara reportada por el motor (throttled ~5 Hz; para el minimapa). */
  const cameraViewM = ref<WorldRectM | null>(null)
  const minimapVisible = ref(true)

  const hasSelection = computed(() => selection.value !== null)

  function setMode(next: InputMode): void {
    mode.value = next
  }

  function setOverlay(name: OverlayName, on: boolean): void {
    if (overlays.value[name] === on) {
      return
    }
    overlays.value = { ...overlays.value, [name]: on }
  }

  /** Reemplaza el estado completo de overlays (volcado inicial desde el motor). */
  function applyOverlays(state: Readonly<Partial<Record<OverlayName, boolean>>>): void {
    overlays.value = { ...state }
  }

  function setSelection(next: SelectionRef | null): void {
    selection.value = next
  }

  function setFollow(vehicleId: string | null): void {
    followedVehicleId.value = vehicleId
  }

  /** Pide al motor centrar la cámara en un punto del mundo (metros). */
  function requestCenterOn(xM: number, yM: number): void {
    cameraCommandSeq += 1
    cameraCommand.value = { seq: cameraCommandSeq, kind: 'center', xM, yM }
  }

  /** Pide al motor encuadrar un rectángulo del mundo (salto de región). */
  function requestFitRect(rectM: WorldRectM): void {
    cameraCommandSeq += 1
    cameraCommand.value = { seq: cameraCommandSeq, kind: 'fit', rectM }
  }

  /** Vista de cámara del motor (bindWorldLive la escribe desde el evento camera). */
  function setCameraView(viewM: WorldRectM): void {
    cameraViewM.value = viewM
  }

  function toggleMinimap(): void {
    minimapVisible.value = !minimapVisible.value
  }

  /** Vuelta al estado inicial (salir de /play, logout). */
  function reset(): void {
    mode.value = 'select'
    overlays.value = {}
    selection.value = null
    followedVehicleId.value = null
    cameraCommand.value = null
    cameraViewM.value = null
    minimapVisible.value = true
  }

  return {
    mode,
    overlays,
    selection,
    followedVehicleId,
    cameraCommand,
    cameraViewM,
    minimapVisible,
    hasSelection,
    setMode,
    setOverlay,
    applyOverlays,
    setSelection,
    setFollow,
    requestCenterOn,
    requestFitRect,
    setCameraView,
    toggleMinimap,
    reset,
  }
})
