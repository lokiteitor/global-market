<!--
  BottomBar — resumen operativo: vehículos por estado, lotes activos y los
  últimos avisos del feed (notifications.store). Solo lectura de stores.
-->
<script setup lang="ts">
import { computed } from 'vue'
import { useBuildingsStore } from '~/stores/buildings.store'
import { useFleetStore } from '~/stores/fleet.store'
import { useNotificationsStore } from '~/stores/notifications.store'

const fleet = useFleetStore()
const buildings = useBuildingsStore()
const notifications = useNotificationsStore()

const vehicleSummary = computed(() => {
  const parts: string[] = []
  for (const [status, list] of Object.entries(fleet.byStatus)) {
    parts.push(`${list.length} ${status.replaceAll('_', ' ')}`)
  }
  return parts.length > 0 ? parts.join(' · ') : 'sin vehículos'
})

const activeBatches = computed(
  () =>
    Object.values(buildings.batchesById).filter(
      (b) => b.status === 'running' || b.status === 'queued' || b.status === 'paused_no_fuel' || b.status === 'paused_no_workers'
    ).length
)

const lastNotifications = computed(() => notifications.latest.slice(0, 3))
</script>

<template>
  <div class="hud-bottom">
    <div class="hud-bottom__cell" aria-label="Flota">
      <span class="hud-bottom__label">Flota</span>
      <span class="hud-bottom__value">{{ vehicleSummary }}</span>
    </div>

    <div class="hud-bottom__cell" aria-label="Producción">
      <span class="hud-bottom__label">Lotes activos</span>
      <span class="hud-bottom__value e-num">{{ activeBatches }}</span>
    </div>

    <div class="hud-bottom__cell hud-bottom__cell--grow" aria-label="Últimos avisos">
      <span class="hud-bottom__label">Avisos</span>
      <span v-if="lastNotifications.length === 0" class="hud-bottom__value hud-bottom__value--faint">sin avisos</span>
      <span
        v-for="n in lastNotifications"
        :key="n.id"
        class="hud-bottom__notice"
        :class="`hud-bottom__notice--${n.level}`"
        :title="n.text"
      >
        {{ n.text }}
      </span>
    </div>
  </div>
</template>

<style lang="scss" scoped>
.hud-bottom {
  display: flex;
  align-items: center;
  gap: 1.5rem;
  font-size: 0.8125rem;
  overflow: hidden;

  &__cell {
    display: inline-flex;
    align-items: center;
    gap: 0.5rem;
    white-space: nowrap;

    &--grow {
      flex: 1;
      min-width: 0;
      overflow: hidden;
    }
  }

  &__label {
    font-size: 0.6875rem;
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--ii-text-faint);
  }

  &__value {
    color: var(--ii-text);

    &--faint {
      color: var(--ii-text-faint);
    }
  }

  &__notice {
    max-width: 18rem;
    overflow: hidden;
    text-overflow: ellipsis;
    padding-left: 0.5rem;
    border-left: 2px solid var(--ii-border);

    &--success {
      border-left-color: var(--ii-success);
    }

    &--warning {
      border-left-color: var(--ii-warning);
    }

    &--error {
      border-left-color: var(--ii-error);
    }

    &--info {
      border-left-color: var(--ii-info);
    }
  }
}
</style>
