/**
 * app/plugins/theme.client — rehidrata la preferencia de tema (FAD §15.14, §20.8).
 *
 * Client-only: lee la preferencia persistida en localStorage (único UI-state
 * persistido en el Incremento 0) y la aplica al estado de useTheme ANTES del
 * primer render de cliente; app.vue proyecta ese estado a `data-theme` en
 * <html> vía useHead. El SSR sirve siempre el tema por defecto (oscuro): si el
 * jugador eligió claro puede haber un parpadeo breve en la hidratación —
 * aceptable en el shell, documentado aquí.
 */

import { isThemePreference, THEME_STORAGE_KEY, useTheme } from '../composables/useTheme'

function readStoredTheme(): string | null {
  try {
    return window.localStorage.getItem(THEME_STORAGE_KEY)
  } catch {
    // Storage bloqueado (privacidad): se queda el tema por defecto.
    return null
  }
}

export default defineNuxtPlugin(() => {
  const { theme, setTheme } = useTheme()

  const stored = readStoredTheme()
  if (stored !== null && isThemePreference(stored) && stored !== theme.value) {
    setTheme(stored)
  }
})
