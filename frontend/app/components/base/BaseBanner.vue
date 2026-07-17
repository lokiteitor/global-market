<script setup lang="ts">
/**
 * BaseBanner — aviso en bloque del UI kit (FAD §15, §13.7).
 *
 * Uso previsto: errores de API (useAppError → texto) y estado de
 * mantenimiento/mundo pausado (variante `warn`, FAD §12.9). Semántica ARIA
 * honesta: `error` interrumpe (`role="alert"`); `info`/`warn` se anuncian de
 * forma educada (`role="status"`).
 */

import { computed } from 'vue'

export type BannerVariant = 'info' | 'warn' | 'error'

interface Props {
  variant?: BannerVariant
  // `| undefined` explícito: permite el default undefined bajo exactOptionalPropertyTypes.
  title?: string | undefined
}

const props = withDefaults(defineProps<Props>(), {
  variant: 'info',
  title: undefined,
})

const role = computed(() => (props.variant === 'error' ? 'alert' : 'status'))
</script>

<template>
  <div :class="[$style.banner, $style[props.variant]]" :role="role">
    <p v-if="props.title !== undefined" :class="$style.title">{{ props.title }}</p>
    <div :class="$style.body">
      <slot />
    </div>
  </div>
</template>

<style module lang="scss">
@use 'settings' as s;

.banner {
  display: flex;
  flex-direction: column;
  gap: s.$space-2;
  padding: s.$space-4 s.$space-5;
  border: 1px solid var(--banner-color);
  border-left-width: 3px;
  border-radius: 4px;
  background-color: color-mix(in srgb, var(--banner-color) 10%, transparent);
  font-size: s.$font-size-300;
}

.title {
  color: var(--banner-color);
  font-size: s.$font-size-400;
  font-weight: s.$font-weight-semibold;
}

.body {
  min-width: 0;
}

.info {
  --banner-color: var(--color-info);
}

.warn {
  --banner-color: var(--color-warning);
}

.error {
  --banner-color: var(--color-danger);
}
</style>
