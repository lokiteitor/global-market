<!--
  /login — acceso de corporación contra POST /auth/sessions.
  El servidor decide (P1): el error llega tipado ({ code, message }) y se
  muestra tal cual. SIMPLIFICACIÓN v1: token en memoria + sessionStorage (dev).
-->
<script setup lang="ts">
import { ref } from 'vue'
import { useSession } from '~/composables/useSession'
import BaseButton from '~/components/base/BaseButton.vue'
import BaseInput from '~/components/base/BaseInput.vue'

definePageMeta({ layout: 'auth' })

const { login } = useSession()

const accountName = ref('')
const secret = ref('')
const busy = ref(false)
const errorCode = ref<string | null>(null)
const errorMessage = ref<string | null>(null)

async function onSubmit(): Promise<void> {
  errorCode.value = null
  errorMessage.value = null
  // Validación de FORMA únicamente; las credenciales las valida el servidor.
  if (accountName.value.trim() === '' || secret.value === '') {
    errorCode.value = 'FORM'
    errorMessage.value = 'Nombre de corporación y credencial son obligatorios'
    return
  }
  busy.value = true
  const result = await login(accountName.value, secret.value)
  busy.value = false
  if (result.ok) {
    await navigateTo('/lobby')
  } else {
    errorCode.value = result.error.code
    errorMessage.value = result.error.message
  }
}
</script>

<template>
  <form class="o-stack" novalidate @submit.prevent="onSubmit">
    <h1 class="login__title">Acceso de corporación</h1>

    <BaseInput v-model="accountName" label="Nombre de corporación" name="account_name" autocomplete="username" required />
    <BaseInput v-model="secret" label="Credencial" type="password" name="secret" autocomplete="current-password" required />

    <BaseButton type="submit" variant="primary" :disabled="busy">
      {{ busy ? 'Entrando…' : 'Entrar' }}
    </BaseButton>

    <p v-if="errorMessage !== null" class="login__error" role="alert">
      <strong v-if="errorCode !== null && errorCode !== 'FORM'" class="e-num">{{ errorCode }}</strong>
      {{ errorMessage }}
    </p>
  </form>
</template>

<style lang="scss" scoped>
.login__title {
  font-size: 1.25rem;
}

.login__error {
  color: var(--ii-error);
  font-size: 0.875rem;
  display: flex;
  gap: 0.5rem;
  align-items: baseline;
}
</style>
