<!--
  BaseToast — una notificación efímera individual (la orquesta ToastHost).
-->
<script setup lang="ts">
import type { NotificationItem } from '~/stores/notifications.store'

defineProps<{ item: NotificationItem }>()

defineEmits<{ dismiss: [id: number] }>()
</script>

<template>
  <div class="b-toast" :class="`b-toast--${item.level}`" role="status">
    <span class="b-toast__text">{{ item.text }}</span>
    <button class="b-toast__close" type="button" aria-label="Descartar notificación" @click="$emit('dismiss', item.id)">✕</button>
  </div>
</template>

<style lang="scss" scoped>
.b-toast {
  display: flex;
  align-items: flex-start;
  gap: 0.5rem;
  background-color: var(--ii-bg-overlay);
  border: 1px solid var(--ii-border);
  border-left: 3px solid var(--b-toast-color, var(--ii-info));
  border-radius: 3px;
  padding: 0.5rem 0.75rem;
  font-size: 0.875rem;
  max-width: 22rem;
  box-shadow: 0 4px 12px rgb(0 0 0 / 40%);

  &--info {
    --b-toast-color: var(--ii-info);
  }

  &--success {
    --b-toast-color: var(--ii-success);
  }

  &--warning {
    --b-toast-color: var(--ii-warning);
  }

  &--error {
    --b-toast-color: var(--ii-error);
  }

  &__text {
    flex: 1;
    overflow-wrap: anywhere;
  }

  &__close {
    color: var(--ii-text-faint);

    &:hover {
      color: var(--ii-text);
    }
  }
}
</style>
