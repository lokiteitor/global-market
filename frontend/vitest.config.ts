import { fileURLToPath } from 'node:url'
import vue from '@vitejs/plugin-vue'
import { configDefaults, defineConfig } from 'vitest/config'

/**
 * Vitest — tests unitarios del kernel y de la app (FAD §22.1).
 *
 * - El kernel (shared/, domain/, network/) es framework-agnostic y se testea
 *   sin montar Nuxt. Mismos aliases que nuxt.config.ts / tsconfig.
 * - Los tests de componentes/páginas Vue (tests/nuxt/, FAD §22.3) montan los
 *   SFC con @vue/test-utils sin Nuxt: @vitejs/plugin-vue compila los .vue y
 *   los pocos globals de Nuxt (definePageMeta, useNuxtApp) se doblan en
 *   tests/nuxt/setup.ts. `tests/nuxt/` está incluido en el tsconfig app
 *   generado por Nuxt, así que `nuxt typecheck` también los cubre.
 */
export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: [
      // Build completo de Vue: los tests usan templates en runtime (stubs,
      // hosts de slots con scope); solo afecta a vitest, nunca al build Nuxt.
      { find: /^vue$/, replacement: 'vue/dist/vue.esm-bundler.js' },
      { find: '~shared', replacement: fileURLToPath(new URL('./shared', import.meta.url)) },
      { find: '~domain', replacement: fileURLToPath(new URL('./domain', import.meta.url)) },
      { find: '~network', replacement: fileURLToPath(new URL('./network', import.meta.url)) },
      { find: '~', replacement: fileURLToPath(new URL('./app', import.meta.url)) },
    ],
  },
  css: {
    preprocessorOptions: {
      scss: {
        // Mismos loadPaths que nuxt.config.ts para `@use 'settings' as s;`.
        loadPaths: [fileURLToPath(new URL('./app/assets/styles', import.meta.url))],
      },
    },
  },
  test: {
    environment: 'happy-dom',
    setupFiles: ['./tests/nuxt/setup.ts'],
    css: {
      // CSS Modules doblados con el nombre de clase original: los tests
      // asserten `$style.primary === 'primary'` sin compilar Sass.
      modules: { classNameStrategy: 'non-scoped' },
    },
    include: [
      'shared/**/*.spec.ts',
      'domain/**/*.spec.ts',
      // game/: SOLO lógica pura (grid/chunks/pool/camera-math); Phaser no se
      // monta en vitest (los módulos con Phaser lo importan solo como tipos).
      'game/**/*.spec.ts',
      'network/**/*.spec.ts',
      'app/**/*.spec.ts',
      'config/**/*.spec.ts',
      'tests/**/*.spec.ts',
    ],
    // Los smoke E2E de navegador real son de Playwright (`npm run test:e2e`,
    // playwright.config.ts), no de vitest: fuera del runner unitario.
    exclude: [...configDefaults.exclude, 'tests/e2e-browser/**'],
  },
})
