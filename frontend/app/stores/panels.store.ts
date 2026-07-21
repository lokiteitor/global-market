/**
 * app/stores/panels.store — estado de UI de paneles y flujos espaciales de /play.
 *
 * Slice de presentación (como mapui.store, NO replicado): qué panel flotante
 * está abierto (v1: uno a la vez, FAD §15.5 simplificado), el tipo de edificio
 * elegido para el modo construir y los INTENTS espaciales pendientes de
 * confirmación (build/parcel) que el mundo vivo emite y los diálogos consumen.
 * Solo tipos del motor (import type, erasados): Phaser sigue fuera del portal.
 */

import { defineStore } from 'pinia'
import { ref } from 'vue'
import type { BuildIntent, ParcelIntent } from '~~/game'
import type { BuildingTypeId } from '~domain/world'

/** Paneles flotantes del HUD (v1: uno visible a la vez). */
export const GAME_PANELS = [
  'market',
  'industry',
  'fleet',
  'finance',
  'concessions',
  'build',
] as const
export type GamePanelName = (typeof GAME_PANELS)[number]

export const usePanelsStore = defineStore('panels', () => {
  const activePanel = ref<GamePanelName | null>(null)
  /** Tipo elegido en el panel Construir (requisito del modo build del mapa). */
  const buildTypeId = ref<BuildingTypeId | null>(null)
  /** Intent de emplazamiento pendiente de confirmación (diálogo). */
  const pendingBuild = ref<BuildIntent | null>(null)
  /** Intent de parcela pendiente de confirmación (diálogo). */
  const pendingParcel = ref<ParcelIntent | null>(null)

  function open(panel: GamePanelName): void {
    activePanel.value = panel
  }

  function close(): void {
    activePanel.value = null
  }

  function toggle(panel: GamePanelName): void {
    activePanel.value = activePanel.value === panel ? null : panel
  }

  function setBuildType(id: BuildingTypeId | null): void {
    buildTypeId.value = id
  }

  function setPendingBuild(intent: BuildIntent | null): void {
    pendingBuild.value = intent
  }

  function setPendingParcel(intent: ParcelIntent | null): void {
    pendingParcel.value = intent
  }

  /** Vuelta al estado inicial (salir de /play, logout). */
  function reset(): void {
    activePanel.value = null
    buildTypeId.value = null
    pendingBuild.value = null
    pendingParcel.value = null
  }

  return {
    activePanel,
    buildTypeId,
    pendingBuild,
    pendingParcel,
    open,
    close,
    toggle,
    setBuildType,
    setPendingBuild,
    setPendingParcel,
    reset,
  }
})
