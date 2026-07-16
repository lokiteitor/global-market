/**
 * composables/useConnection.ts — salud de la conexión en tiempo real.
 *
 * Lee el estado que el plugin de red espeja en sim.store (P2: la store es la
 * dueña; el transporte solo lo publica) y expone las palancas del transporte.
 */
import { computed } from 'vue'
import type { NetworkTransport } from '~/lib/net/transport'
import { useSimStore } from '~/stores/sim.store'

export function useConnection() {
  const simStore = useSimStore()
  // Capturado en contexto síncrono de setup (tras un await no hay instancia Nuxt).
  const transport = useNuxtApp().$transport as NetworkTransport | undefined

  /** 'connecting' | 'open' | 'reconnecting' | 'closed' | 'frozen' (C9). */
  const state = computed(() => simStore.connectionState)
  /** Conectado (aunque el mundo esté congelado por mantenimiento). */
  const isOnline = computed(() => simStore.connectionState === 'open' || simStore.connectionState === 'frozen')
  const isFrozen = computed(() => simStore.frozen)

  function connect(): void {
    transport?.connect()
  }

  function disconnect(): void {
    transport?.close()
  }

  /** Segundos de pared desde el último contacto con el servidor (staleness, P10). */
  function staleness(): number | null {
    return simStore.staleness()
  }

  return { state, isOnline, isFrozen, connect, disconnect, staleness }
}
