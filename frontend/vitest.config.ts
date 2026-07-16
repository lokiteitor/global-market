import { fileURLToPath } from 'node:url'
import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vitest/config'

// Tests sin Nuxt:
//  - kernel (framework-agnostic): entorno node por defecto;
//  - componentes UI (@vue/test-utils): declaran `@vitest-environment happy-dom`
//    en su docblock y compilan .vue vía @vitejs/plugin-vue.
export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '~': fileURLToPath(new URL('./app', import.meta.url))
    }
  },
  test: {
    environment: 'node',
    include: ['tests/**/*.spec.ts']
  }
})
