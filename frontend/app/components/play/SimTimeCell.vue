<script setup lang="ts">
/**
 * SimTimeCell — instante de sim-time con cuenta atrás viva (FAD §15.8).
 *
 * Formatea con el ÚNICO formateador del kernel (formatSimTime) y deriva el
 * restante contra el simNow reactivo del SimClock (ticker ~1 s). Con el mundo
 * frozen el restante no avanza — comportamiento correcto por construcción.
 */

import { computed } from 'vue'
import { t } from '~shared/i18n'
import type { SimTime } from '~shared/simtime'
import { SIM_SECONDS_PER_DAY, SIM_SECONDS_PER_HOUR, formatSimTime } from '~shared/simtime'
import { useSimNow } from '~/composables/useSimNow'

interface Props {
  at: SimTime
  /** Mostrar cuenta atrás además del instante (deadlines). */
  countdown?: boolean
}

const props = withDefaults(defineProps<Props>(), { countdown: false })

const simNow = useSimNow()

const formatted = computed(() => formatSimTime(props.at))

const remainingText = computed<string | null>(() => {
  if (!props.countdown) {
    return null
  }
  const now = simNow.value
  if (now === null) {
    return null
  }
  const remaining = props.at - now
  if (remaining <= 0) {
    return t('simtime.overdue')
  }
  const days = Math.floor(remaining / SIM_SECONDS_PER_DAY)
  const hours = Math.floor((remaining % SIM_SECONDS_PER_DAY) / SIM_SECONDS_PER_HOUR)
  return t('simtime.remaining', { days, hours })
})
</script>

<template>
  <span class="simtime u-numeric">
    <span>{{ formatted }}</span>
    <span v-if="remainingText !== null" class="simtime__remaining">{{ remainingText }}</span>
  </span>
</template>

<style scoped lang="scss">
@use 'settings' as s;

.simtime {
  display: inline-flex;
  gap: s.$space-3;
  align-items: baseline;
  white-space: nowrap;
}

.simtime__remaining {
  color: var(--color-text-muted);
  font-size: s.$font-size-100;
}
</style>
