<!--
  ToastHost — host de toasts efímeros.
  Observa notifications.store (única fuente: alerts: del WS + 'ui:notify' del
  event bus) y presenta las entradas NUEVAS durante unos segundos. No marca
  leído ni borra nada del feed: solo presentación efímera.
-->
<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { useNotificationsStore, type NotificationItem } from '~/stores/notifications.store'
import BaseToast from './BaseToast.vue'

const AUTO_DISMISS_MS = 6000
const MAX_VISIBLE = 5

const store = useNotificationsStore()

const visible = ref<NotificationItem[]>([])
const timers = new Map<number, ReturnType<typeof setTimeout>>()

function dismiss(id: number): void {
  visible.value = visible.value.filter((t) => t.id !== id)
  const timer = timers.get(id)
  if (timer !== undefined) {
    clearTimeout(timer)
    timers.delete(id)
  }
}

watch(
  () => store.items,
  (items, previous) => {
    const known = new Set((previous ?? []).map((n) => n.id))
    const shown = new Set(visible.value.map((n) => n.id))
    for (const item of items) {
      if (known.has(item.id) || shown.has(item.id)) continue
      visible.value = [item, ...visible.value].slice(0, MAX_VISIBLE)
      timers.set(
        item.id,
        setTimeout(() => dismiss(item.id), AUTO_DISMISS_MS)
      )
    }
  },
  { deep: false }
)

onBeforeUnmount(() => {
  for (const timer of timers.values()) clearTimeout(timer)
  timers.clear()
})
</script>

<template>
  <div class="b-toast-host" aria-live="polite" aria-label="Notificaciones">
    <BaseToast v-for="item in visible" :key="item.id" :item="item" @dismiss="dismiss" />
  </div>
</template>

<style lang="scss" scoped>
.b-toast-host {
  position: fixed;
  right: 1rem;
  bottom: 3.5rem;
  z-index: 200; // $z-toast
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
  pointer-events: none;

  > * {
    pointer-events: auto;
  }
}
</style>
