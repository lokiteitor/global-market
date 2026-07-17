<script setup lang="ts">
import { t } from '~shared/i18n'
import { useTheme } from './composables/useTheme'

// El tema (oscuro por defecto, FAD §15.14) se proyecta como `data-theme` en
// <html>; los temas SCSS (themes/) reaccionan a ese atributo. La preferencia
// persistida la rehidrata el plugin theme.client antes del primer render.
const { theme } = useTheme()

useHead({
  htmlAttrs: { lang: 'es', 'data-theme': () => theme.value },
  title: t('app.title'),
})
</script>

<template>
  <NuxtLayout>
    <NuxtPage />
  </NuxtLayout>
</template>
