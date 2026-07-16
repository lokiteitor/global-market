<!--
  /settings — ajustes del cliente.
  v1: tema oscuro fijo (único tema, ADR-FE-006) y volumen como placeholder
  (el audio llega con el mundo Phaser). Logout con cierre de sesión real.
-->
<script setup lang="ts">
import { ref } from 'vue'
import { useSession } from '~/composables/useSession'
import BaseButton from '~/components/base/BaseButton.vue'
import BaseSelect from '~/components/base/BaseSelect.vue'
import { useSessionStore } from '~/stores/session.store'

// Guard de sesión: sin token → /login (client-only; ver middleware/auth.ts).
definePageMeta({ middleware: 'auth' })

const session = useSessionStore()
const { logout, restore } = useSession()

// SIMPLIFICACIÓN v1: restaurar sesión para mostrar el estado real del botón.
if (typeof window !== 'undefined') restore()

const theme = ref('dark')
const volume = ref('80')
const busy = ref(false)

async function onLogout(): Promise<void> {
  busy.value = true
  await logout()
  busy.value = false
  await navigateTo('/login')
}
</script>

<template>
  <section class="o-stack">
    <h1>Ajustes</h1>

    <div class="o-panel o-stack o-stack--tight">
      <h2 class="o-panel__title">Apariencia</h2>
      <BaseSelect
        v-model="theme"
        label="Tema"
        :options="[{ value: 'dark', label: 'Oscuro industrial (v1: único tema)' }]"
        disabled
      />
    </div>

    <div class="o-panel o-stack o-stack--tight">
      <h2 class="o-panel__title">Audio</h2>
      <label class="settings__volume">
        <span class="settings__label">Volumen (placeholder — el audio llega con el mundo)</span>
        <input v-model="volume" type="range" min="0" max="100" step="5" />
        <span class="e-num">{{ volume }} %</span>
      </label>
    </div>

    <div class="o-panel o-stack o-stack--tight">
      <h2 class="o-panel__title">Sesión</h2>
      <p v-if="session.isAuthenticated">Conectado como <strong>{{ session.accountName }}</strong>.</p>
      <p v-else class="settings__faint">Sin sesión activa.</p>
      <div>
        <BaseButton variant="danger" :disabled="busy || !session.isAuthenticated" @click="onLogout">Cerrar sesión</BaseButton>
      </div>
    </div>
  </section>
</template>

<style lang="scss" scoped>
.settings__volume {
  display: flex;
  align-items: center;
  gap: 0.75rem;

  input[type='range'] {
    flex: 1;
    max-width: 16rem;
    accent-color: var(--ii-accent);
  }
}

.settings__label {
  font-size: 0.875rem;
  color: var(--ii-text-muted);
}

.settings__faint {
  color: var(--ii-text-faint);
}
</style>
