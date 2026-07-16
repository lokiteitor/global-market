<!--
  BaseBadge — etiqueta de estado con variantes por estado de DOMINIO.
  Pasa `status` (enum del contrato: BuildingStatus, VehicleStatus, …) y el
  badge deriva la variante visual; `variant` explícita tiene prioridad.
-->
<script setup lang="ts">
import { computed } from 'vue'

export type BadgeVariant = 'neutral' | 'success' | 'warning' | 'danger' | 'info' | 'accent'

/** Mapa estado de dominio → variante (presentación pura; sin reglas de negocio). */
const STATUS_VARIANTS: Record<string, BadgeVariant> = {
  // edificios
  operational: 'success',
  under_construction: 'warning',
  damaged: 'danger',
  in_maintenance: 'warning',
  abandoned: 'danger',
  seized: 'danger',
  // lotes
  queued: 'info',
  running: 'success',
  paused_no_fuel: 'warning',
  paused_no_workers: 'warning',
  completed: 'neutral',
  cancelled: 'danger',
  // vehículos
  idle: 'neutral',
  loading: 'info',
  in_transit: 'info',
  unloading: 'info',
  broken: 'danger',
  sealed: 'warning',
  // publicaciones / aceptaciones / contratos
  draw_window: 'accent',
  open: 'success',
  micro_window: 'accent',
  exhausted: 'neutral',
  expired: 'danger',
  pending_draw: 'warning',
  served: 'success',
  released: 'neutral',
  active: 'success',
  settled: 'neutral',
  failed: 'danger',
  // concesiones
  delinquent: 'danger',
  grace: 'warning',
  reverted: 'danger',
  // cargamentos
  in_warehouse: 'neutral',
  at_terminal: 'info',
  delivered: 'success',
  released_in_situ: 'warning',
  // señal temporal (HUD)
  live: 'success',
  stale: 'warning',
  frozen: 'info'
}

const props = withDefaults(
  defineProps<{
    status?: string
    variant?: BadgeVariant
  }>(),
  { status: undefined, variant: undefined }
)

const resolved = computed<BadgeVariant>(() => {
  if (props.variant !== undefined) return props.variant
  if (props.status !== undefined) return STATUS_VARIANTS[props.status] ?? 'neutral'
  return 'neutral'
})
</script>

<template>
  <span class="b-badge" :class="`b-badge--${resolved}`">
    <slot>{{ status }}</slot>
  </span>
</template>

<style lang="scss" scoped>
.b-badge {
  display: inline-block;
  font-size: 0.6875rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.04em;
  padding: 0.0625rem 0.375rem;
  border-radius: 999px;
  border: 1px solid var(--b-badge-color, var(--ii-text-faint));
  color: var(--b-badge-color, var(--ii-text-faint));
  white-space: nowrap;

  &--success {
    --b-badge-color: var(--ii-success);
  }

  &--warning {
    --b-badge-color: var(--ii-warning);
  }

  &--danger {
    --b-badge-color: var(--ii-error);
  }

  &--info {
    --b-badge-color: var(--ii-info);
  }

  &--accent {
    --b-badge-color: var(--ii-accent-strong);
  }

  &--neutral {
    --b-badge-color: var(--ii-text-muted);
  }
}
</style>
