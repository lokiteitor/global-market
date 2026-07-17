/**
 * tests/nuxt/setup — dobles de los globals de Nuxt para tests de UI (FAD §22.3).
 *
 * Los SFC se compilan con @vitejs/plugin-vue SIN Nuxt: los macros/auto-imports
 * de Nuxt que las páginas usan en setup se resuelven contra globalThis. Aquí
 * se doblan los transversales; `useNuxtApp` lo dobla cada spec según lo que
 * necesite inyectar ($simNow, $simFrozen).
 */

import { vi } from 'vitest'

// Macro de Nuxt (layout/middleware por página): compilado fuera de Nuxt queda
// como llamada runtime — noop en tests (los middleware no corren en unitarios).
vi.stubGlobal('definePageMeta', () => {})
