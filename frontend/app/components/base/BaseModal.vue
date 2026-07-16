<!--
  BaseModal — modal del UI kit propio.
  Accesibilidad: role="dialog" + aria-modal, focus trap básico (Tab cicla
  dentro), Esc cierra, y el foco entra al abrir. Sin Teleport: el overlay es
  position:fixed y el HUD vive en un único stacking context (tokens $z-modal).
-->
<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'

const props = withDefaults(
  defineProps<{
    open: boolean
    title: string
  }>(),
  {}
)

const emit = defineEmits<{ close: [] }>()

const dialogEl = ref<HTMLElement | null>(null)

function focusables(): HTMLElement[] {
  if (dialogEl.value === null) return []
  return Array.from(
    dialogEl.value.querySelectorAll<HTMLElement>(
      'button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'
    )
  )
}

async function focusFirst(): Promise<void> {
  await nextTick()
  const list = focusables()
  const target = list[0] ?? dialogEl.value
  target?.focus()
}

function onKeydown(event: KeyboardEvent): void {
  if (!props.open) return
  if (event.key === 'Escape') {
    event.stopPropagation()
    emit('close')
    return
  }
  if (event.key !== 'Tab') return
  const list = focusables()
  if (list.length === 0) return
  const first = list[0] as HTMLElement
  const last = list[list.length - 1] as HTMLElement
  const active = document.activeElement
  if (event.shiftKey && active === first) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && active === last) {
    event.preventDefault()
    first.focus()
  }
}

watch(
  () => props.open,
  (open) => {
    if (open) void focusFirst()
  }
)

onMounted(() => {
  document.addEventListener('keydown', onKeydown)
  if (props.open) void focusFirst()
})

onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKeydown)
})
</script>

<template>
  <div v-if="open" class="b-modal" @click.self="emit('close')">
    <div ref="dialogEl" class="b-modal__dialog" role="dialog" aria-modal="true" :aria-label="title" tabindex="-1">
      <header class="b-modal__header">
        <h2 class="b-modal__title">{{ title }}</h2>
        <button class="b-modal__close" type="button" aria-label="Cerrar" @click="emit('close')">✕</button>
      </header>
      <div class="b-modal__body">
        <slot />
      </div>
      <footer v-if="$slots.footer" class="b-modal__footer">
        <slot name="footer" />
      </footer>
    </div>
  </div>
</template>

<style lang="scss" scoped>
.b-modal {
  position: fixed;
  inset: 0;
  z-index: 100; // $z-modal
  display: grid;
  place-items: center;
  background-color: rgb(0 0 0 / 55%);
  padding: 1rem;

  &__dialog {
    background-color: var(--ii-bg-overlay);
    border: 1px solid var(--ii-border);
    border-radius: 6px;
    width: 100%;
    max-width: 28rem;
    max-height: 85vh;
    display: flex;
    flex-direction: column;
  }

  &__header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0.75rem 1rem;
    border-bottom: 1px solid var(--ii-border-subtle);
  }

  &__title {
    font-size: 1rem;
  }

  &__close {
    color: var(--ii-text-muted);

    &:hover {
      color: var(--ii-text);
    }
  }

  &__body {
    padding: 1rem;
    overflow-y: auto;
  }

  &__footer {
    display: flex;
    justify-content: flex-end;
    gap: 0.5rem;
    padding: 0.75rem 1rem;
    border-top: 1px solid var(--ii-border-subtle);
  }
}
</style>
