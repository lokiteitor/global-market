<!--
  SimTimeText — un instante de sim-time mostrado en dual: cuánto falta en
  tiempo de PARED ('vence en 3h 12m') y la fecha de juego ('día A-DDD-HH:MM').
  La conversión sim→wall es la del kernel (ratio 24×, único punto: simtime.ts);
  el "ahora" lo aporta el SimClock (sim.store). El vencimiento real lo decide
  el servidor (P1) — esto es presentación.
-->
<script setup lang="ts">
import { computed } from 'vue'
import { formatSimTime, plazoRestante, simToWallMs } from '~/lib/kernel/simtime'
import { formatWallDuration } from '~/composables/useWallClock'
import { useSimClock } from '~/composables/useSimClock'

const props = withDefaults(
  defineProps<{
    /** Instante en segundos de sim-time desde el génesis. */
    simSeconds: number
    /** Muestra el plazo relativo ('vence en …' / 'hace …') junto a la fecha. */
    relative?: boolean
    /** Verbo del plazo relativo. */
    verb?: string
  }>(),
  { relative: true, verb: 'vence' }
)

const clock = useSimClock()

const simDate = computed(() => formatSimTime(props.simSeconds))

const relativeText = computed(() => {
  const now = clock.nowSim.value
  if (props.simSeconds >= now) {
    const remainingSim = plazoRestante(props.simSeconds, now)
    return `${props.verb} en ${formatWallDuration(simToWallMs(remainingSim))}`
  }
  const elapsedWallMs = simToWallMs(now - props.simSeconds)
  return `hace ${formatWallDuration(elapsedWallMs)}`
})
</script>

<template>
  <span class="b-simtime">
    <template v-if="relative">
      <span class="b-simtime__relative">{{ relativeText }}</span>
      <span class="b-simtime__sep" aria-hidden="true"> · </span>
    </template>
    <span class="b-simtime__date e-num">día {{ simDate }}</span>
  </span>
</template>

<style lang="scss" scoped>
.b-simtime {
  white-space: nowrap;

  &__relative {
    color: var(--ii-text);
  }

  &__sep,
  &__date {
    color: var(--ii-text-muted);
    font-size: 0.9em;
  }
}
</style>
