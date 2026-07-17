<script setup lang="ts">
/**
 * Ajustes (FAD Incremento 0, middleware auth): selección de tema claro/oscuro.
 *
 * SOLO se persiste la preferencia de tema en localStorage (UI-state permitido
 * por FAD §20.8); el atributo `data-theme` de <html> lo proyecta app.vue desde
 * el estado de useTheme.
 */

import type { MessageKey } from '~shared/i18n'
import { t } from '~shared/i18n'
import BasePanel from '~/components/base/BasePanel.vue'
import type { ThemePreference } from '~/composables/useTheme'
import { useTheme } from '~/composables/useTheme'

definePageMeta({ middleware: 'auth' })

const { theme, setTheme } = useTheme()

const THEME_OPTIONS: ReadonlyArray<{ value: ThemePreference; labelKey: MessageKey }> = [
  { value: 'dark', labelKey: 'settings.theme.dark' },
  { value: 'light', labelKey: 'settings.theme.light' },
]
</script>

<template>
  <div class="settings o-stack">
    <header class="settings__header o-cluster">
      <h1 class="settings__title">{{ t('settings.title') }}</h1>
      <NuxtLink class="settings__back" to="/lobby">{{ t('common.back') }}</NuxtLink>
    </header>

    <BasePanel>
      <fieldset class="settings__fieldset">
        <legend class="settings__legend">{{ t('settings.theme.legend') }}</legend>
        <div class="settings__options">
          <label v-for="option in THEME_OPTIONS" :key="option.value" class="settings__option">
            <input
              class="settings__radio"
              type="radio"
              name="theme"
              :value="option.value"
              :checked="theme === option.value"
              @change="setTheme(option.value)"
            />
            <span>{{ t(option.labelKey) }}</span>
          </label>
        </div>
      </fieldset>
      <p class="settings__hint">{{ t('settings.theme.hint') }}</p>
    </BasePanel>
  </div>
</template>

<style scoped lang="scss">
@use 'settings' as s;
@use 'tools' as t;

.settings__header {
  justify-content: space-between;
}

.settings__title {
  font-size: s.$font-size-700;
}

.settings__back {
  color: var(--color-text-muted);

  @include t.focus-ring;

  &:hover {
    color: var(--color-text);
  }
}

.settings__fieldset {
  border: 0;
  padding: 0;
}

.settings__legend {
  padding: 0;
  margin-bottom: s.$space-3;
  color: var(--color-text-strong);
  font-weight: s.$font-weight-medium;
}

.settings__options {
  display: flex;
  flex-wrap: wrap;
  gap: s.$space-5;
}

.settings__option {
  display: inline-flex;
  align-items: center;
  gap: s.$space-3;
  cursor: pointer;
}

.settings__radio {
  accent-color: var(--color-accent);

  @include t.focus-ring;
}

.settings__hint {
  margin-top: s.$space-4;
  color: var(--color-text-muted);
  font-size: s.$font-size-200;
}
</style>
