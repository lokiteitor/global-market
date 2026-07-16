/**
 * stores/sim.store.ts — bounded context: reloj de simulación y salud de red.
 *
 * Estado del SimClock (ADR-FE-007 / P5): esta store guarda la última muestra
 * autoritativa de sim-time (de `meta.sim_time_seconds`, snapshots y pongs) y
 * el ancla wall-clock local para DERIVAR now() entre pongs. La aritmética
 * sim↔wall vive SOLO en lib/kernel/simtime.ts.
 */
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { formatSimTime, wallMsToSim } from '~/lib/kernel/simtime'

export type ConnectionState = 'connecting' | 'open' | 'reconnecting' | 'closed' | 'frozen'

export const useSimStore = defineStore('sim', () => {
  // ── Estado ──
  /** Última muestra autoritativa de sim-time (segundos desde el génesis). */
  const simSeconds = ref(0)
  /** Ventana de mantenimiento: el mundo está congelado (C9). */
  const frozen = ref(false)
  /** Ancla wall-clock local (Date.now() en ms) de la última muestra. */
  const syncedAtWallMs = ref<number | null>(null)
  const connectionState = ref<ConnectionState>('closed')
  /** Wall-clock del último contacto con el servidor (pong, meta, snapshot). */
  const lastContactWallMs = ref<number | null>(null)

  // ── Getters ──
  /**
   * Sim-time derivado "ahora": última muestra + deriva local a ratio 24×.
   * Es función (no computed) porque depende del reloj de pared; quien pinte
   * countdowns debe muestrearla en su propio tick.
   */
  function now(wallMs: number = Date.now()): number {
    if (syncedAtWallMs.value === null) return simSeconds.value
    if (frozen.value) return simSeconds.value
    return simSeconds.value + wallMsToSim(wallMs - syncedAtWallMs.value)
  }

  const nowFormatted = computed(() => formatSimTime(now()))

  /** Segundos de pared desde el último contacto con el servidor (staleness, P10). */
  function staleness(wallMs: number = Date.now()): number | null {
    if (lastContactWallMs.value === null) return null
    return (wallMs - lastContactWallMs.value) / 1000
  }

  const isFrozen = computed(() => frozen.value)

  // ── Acciones ──
  /** Re-sincroniza con una muestra autoritativa (pong, meta.sim_time_seconds, snapshot). */
  function syncFromServer(serverSimSeconds: number, serverFrozen: boolean, wallMs: number = Date.now()): void {
    simSeconds.value = serverSimSeconds
    frozen.value = serverFrozen
    syncedAtWallMs.value = wallMs
    lastContactWallMs.value = wallMs
    if (serverFrozen && connectionState.value === 'open') connectionState.value = 'frozen'
    if (!serverFrozen && connectionState.value === 'frozen') connectionState.value = 'open'
  }

  function markContact(wallMs: number = Date.now()): void {
    lastContactWallMs.value = wallMs
  }

  function setConnectionState(state: ConnectionState): void {
    connectionState.value = state
  }

  return {
    simSeconds,
    frozen,
    syncedAtWallMs,
    connectionState,
    lastContactWallMs,
    now,
    nowFormatted,
    staleness,
    isFrozen,
    syncFromServer,
    markContact,
    setConnectionState
  }
})
