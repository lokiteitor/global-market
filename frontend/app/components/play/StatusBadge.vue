<script setup lang="ts">
/**
 * StatusBadge — estado de dominio como chip visual (FAD §15.8, §20.9).
 *
 * Recibe la presentación ESPEJO de domain/status (clave i18n + severidad):
 * este componente no conoce máquinas de estado, solo pinta.
 */

import { t } from '~shared/i18n'
import type { StatusPresentation } from '~domain/status'

interface Props {
  presentation: StatusPresentation
}

const props = defineProps<Props>()
</script>

<template>
  <span class="badge" :class="`badge--${props.presentation.severity}`" data-testid="status-badge">
    {{ t(props.presentation.labelKey) }}
  </span>
</template>

<style scoped lang="scss">
@use 'settings' as s;

.badge {
  display: inline-block;
  padding: s.$space-1 s.$space-3;
  border-radius: 999px;
  font-size: s.$font-size-100;
  font-weight: s.$font-weight-medium;
  line-height: s.$line-height-tight;
  white-space: nowrap;
  border: 1px solid var(--color-border);
  color: var(--color-text);
}

.badge--ok {
  color: var(--color-success);
  border-color: var(--color-success);
}

.badge--busy {
  color: var(--color-info);
  border-color: var(--color-info);
}

.badge--warn {
  color: var(--color-warning);
  border-color: var(--color-warning);
}

.badge--danger {
  color: var(--color-danger);
  border-color: var(--color-danger);
}
</style>
