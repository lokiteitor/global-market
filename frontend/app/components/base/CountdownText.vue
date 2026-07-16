<!--
  CountdownText — cuenta atrás hacia un instante de WALL-CLOCK (ISO 8601).
  Uso: ventanas de sorteo / micro-ventanas / cooldown anti-parpadeo, que son
  mecánica en tiempo real del servidor (GDD 5.3.1). Solo presentación: el
  cierre real lo decide el servidor (P1). Emite 'expired' al llegar a cero.
-->
<script setup lang="ts">
import { computed, watch } from 'vue'
import { formatWallDuration, useWallClock } from '~/composables/useWallClock'

const props = withDefaults(
  defineProps<{
    /** Instante objetivo (wall-clock ISO 8601, p. ej. window_closes_at). */
    until: string
    expiredText?: string
  }>(),
  { expiredText: 'cerrada' }
)

const emit = defineEmits<{ expired: [] }>()

const nowMs = useWallClock(1000)

const remainingMs = computed(() => {
  const target = Date.parse(props.until)
  if (Number.isNaN(target)) return 0
  return Math.max(0, target - nowMs.value)
})

const expired = computed(() => remainingMs.value <= 0)
const text = computed(() => (expired.value ? props.expiredText : formatWallDuration(remainingMs.value)))

watch(expired, (isExpired, wasExpired) => {
  if (isExpired && !wasExpired) emit('expired')
})
</script>

<template>
  <span class="b-countdown e-num" :class="{ 'b-countdown--expired': expired }" role="timer" :aria-label="`Tiempo restante: ${text}`">
    {{ text }}
  </span>
</template>

<style lang="scss" scoped>
.b-countdown {
  font-variant-numeric: tabular-nums;
  color: var(--ii-accent-strong);

  &--expired {
    color: var(--ii-text-faint);
  }
}
</style>
