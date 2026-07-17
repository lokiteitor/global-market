<script setup lang="ts">
/**
 * Login (FAD Incremento 0): nombre de corporación + credencial.
 *
 * La validación local es SOLO de forma (campos requeridos, C7): la validación
 * real es del servidor — el resultado llega como AppError tipado y se muestra
 * con useAppError. Éxito → /lobby (o el destino de `?redirect=` si es una
 * ruta interna). El estado loading viene de la store (`authenticating`).
 */

import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { t } from '~shared/i18n'
import { AppError } from '~network/rest'
import BaseBanner from '~/components/base/BaseBanner.vue'
import BaseButton from '~/components/base/BaseButton.vue'
import BaseFormField from '~/components/base/BaseFormField.vue'
import BaseInput from '~/components/base/BaseInput.vue'
import { useAppError } from '~/composables/useAppError'
import { useSessionStore } from '~/stores/session.store'

definePageMeta({ layout: 'auth' })

const session = useSessionStore()
const route = useRoute()
const router = useRouter()
const { messageFor, isMaintenance } = useAppError()

const accountName = ref('')
const secret = ref('')
/** La validación de forma solo se muestra tras el primer intento de envío. */
const submitted = ref(false)
const submitError = ref<unknown>(null)

const accountNameError = computed(() =>
  submitted.value && accountName.value.trim() === '' ? t('validation.required') : null,
)
const secretError = computed(() =>
  submitted.value && secret.value === '' ? t('validation.required') : null,
)

const isSubmitting = computed(() => session.status === 'authenticating')

/** Solo rutas internas: un `?redirect=` externo o protocol-relative se ignora. */
const redirectTarget = computed<string>(() => {
  const raw = route.query['redirect']
  const value = Array.isArray(raw) ? raw[0] : raw
  if (typeof value === 'string' && value.startsWith('/') && !value.startsWith('//')) {
    return value
  }
  return '/lobby'
})

const banner = computed<{ variant: 'warn' | 'error'; message: string } | null>(() => {
  const error = submitError.value
  if (error === null) {
    return null
  }
  if (isMaintenance(error)) {
    return { variant: 'warn', message: t('error.MAINTENANCE_WINDOW') }
  }
  if (error instanceof AppError && error.status === 401) {
    return { variant: 'error', message: t('login.error.invalidCredentials') }
  }
  return { variant: 'error', message: messageFor(error) }
})

async function onSubmit(): Promise<void> {
  submitted.value = true
  submitError.value = null
  if (accountName.value.trim() === '' || secret.value === '') {
    return
  }
  try {
    await session.login(accountName.value.trim(), secret.value)
    await router.push(redirectTarget.value)
  } catch (error) {
    submitError.value = error
  }
}
</script>

<template>
  <div class="login o-stack">
    <h1 class="login__title">{{ t('login.title') }}</h1>

    <BaseBanner v-if="banner !== null" :variant="banner.variant">{{ banner.message }}</BaseBanner>

    <form class="o-stack" novalidate @submit.prevent="onSubmit">
      <BaseFormField :label="t('login.accountName.label')" :error="accountNameError" required>
        <template #default="{ id, describedBy, invalid }">
          <BaseInput
            :id="id"
            v-model="accountName"
            :aria-describedby="describedBy"
            :invalid="invalid"
            :placeholder="t('login.accountName.placeholder')"
            name="account_name"
            autocomplete="username"
            required
          />
        </template>
      </BaseFormField>

      <BaseFormField :label="t('login.secret.label')" :error="secretError" required>
        <template #default="{ id, describedBy, invalid }">
          <BaseInput
            :id="id"
            v-model="secret"
            type="password"
            :aria-describedby="describedBy"
            :invalid="invalid"
            name="secret"
            autocomplete="current-password"
            required
          />
        </template>
      </BaseFormField>

      <BaseButton type="submit" :loading="isSubmitting">
        {{ isSubmitting ? t('login.submitting') : t('login.submit') }}
      </BaseButton>
    </form>
  </div>
</template>

<style scoped lang="scss">
@use 'settings' as s;

.login__title {
  font-size: s.$font-size-600;
}
</style>
