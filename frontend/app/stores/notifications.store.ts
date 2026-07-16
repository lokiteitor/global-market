/**
 * stores/notifications.store.ts — bounded context: notificaciones.
 *
 * Feed de `message` de la room `alerts:` (resultado de sorteo, liquidaciones,
 * mantenimiento) y de toasts locales de UI ('ui:notify' del event bus).
 * Tope de 200 entradas: descarta las más antiguas.
 */
import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

export type NotificationLevel = 'info' | 'success' | 'warning' | 'error'

export interface NotificationItem {
  id: number
  level: NotificationLevel
  text: string
  /** Evento del gateway que la originó (p. ej. 'acceptance.resolved'), si aplica. */
  event?: string
  /** Sim-time del evento, si vino del servidor. */
  simSeconds?: number
  /** Wall-clock local de recepción. */
  receivedAtMs: number
  read: boolean
}

const MAX_ITEMS = 200

export const useNotificationsStore = defineStore('notifications', () => {
  // ── Estado ──
  const items = ref<NotificationItem[]>([])
  const nextId = ref(1)

  // ── Getters ──
  const unreadCount = computed(() => items.value.filter((n) => !n.read).length)
  const latest = computed(() => items.value.slice(0, 20))

  // ── Acciones ──
  function push(input: { level: NotificationLevel; text: string; event?: string; simSeconds?: number }): NotificationItem {
    const item: NotificationItem = {
      id: nextId.value++,
      level: input.level,
      text: input.text,
      receivedAtMs: Date.now(),
      read: false,
      ...(input.event !== undefined ? { event: input.event } : {}),
      ...(input.simSeconds !== undefined ? { simSeconds: input.simSeconds } : {})
    }
    items.value = [item, ...items.value].slice(0, MAX_ITEMS)
    return item
  }

  function markRead(id: number): void {
    const item = items.value.find((n) => n.id === id)
    if (item) item.read = true
  }

  function markAllRead(): void {
    for (const item of items.value) item.read = true
  }

  function clear(): void {
    items.value = []
  }

  return { items, unreadCount, latest, push, markRead, markAllRead, clear }
})
