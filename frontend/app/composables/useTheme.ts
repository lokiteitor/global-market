/**
 * app/composables/useTheme — preferencia de tema claro/oscuro (FAD §15.14, §20.8).
 *
 * ÚNICO estado que persiste en localStorage en el Incremento 0: la preferencia
 * de tema, que es UI-state puro (permitido por FAD §20.8 — jamás dominio ni
 * sesión). El atributo `data-theme` de <html> lo aplica app.vue vía useHead a
 * partir de este estado; los temas SCSS (themes/) reaccionan a ese atributo.
 *
 * SSR-safe: el estado vive en useState (por petición); localStorage solo se
 * toca en cliente y dentro de try/catch (modos de privacidad que lo bloquean).
 */

import { readonly } from 'vue'
import type { Ref } from 'vue'

export const THEME_STORAGE_KEY = 'imperio-industrial.theme'

export const THEME_PREFERENCES = ['dark', 'light'] as const
export type ThemePreference = (typeof THEME_PREFERENCES)[number]

/** Oscuro por defecto: sesiones largas de gestión (FAD §15.14). */
export const DEFAULT_THEME: ThemePreference = 'dark'

export function isThemePreference(value: string): value is ThemePreference {
  return (THEME_PREFERENCES as readonly string[]).includes(value)
}

export function useTheme(): {
  theme: Readonly<Ref<ThemePreference>>
  setTheme: (next: ThemePreference) => void
} {
  const theme = useState<ThemePreference>('ui-theme', () => DEFAULT_THEME)

  function setTheme(next: ThemePreference): void {
    theme.value = next
    if (import.meta.client) {
      try {
        window.localStorage.setItem(THEME_STORAGE_KEY, next)
      } catch {
        // Storage bloqueado (privacidad/cuota): el tema aplica solo en sesión.
      }
    }
  }

  return { theme: readonly(theme), setTheme }
}
