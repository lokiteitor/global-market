// Configuración Nuxt del cliente web de Imperio Industrial.
// Stack fijado por el FAD (C1..C6): Nuxt 4 + Vue 3 + TS estricto + Pinia + Phaser 3 + Sass.
export default defineNuxtConfig({
  compatibilityDate: '2026-07-15',

  // SSR por defecto para el portal (login/lobby); el mundo de juego es client-only.
  ssr: true,

  modules: ['@pinia/nuxt'],

  css: ['~/assets/styles/index.scss'],

  routeRules: {
    // Portal estático: prerender opcional (FAD §10.6).
    '/': { prerender: true },
    // El mundo de juego se monta client-only: estado en vivo por WS, Phaser sin SSR (FAD §11.2).
    '/play': { ssr: false }
  },

  runtimeConfig: {
    public: {
      apiBase: '/api/v1',
      wsPath: '/ws',
      // Override absoluto opcional (NUXT_PUBLIC_WS_URL) para dev directo en :3000,
      // donde el devProxy de Nitro no hace upgrade de WebSocket. Vacío = mismo origen.
      wsUrl: ''
    }
  },

  nitro: {
    // Proxy SOLO de desarrollo hacia el gateway (:8080). En producción el edge Caddy
    // enruta /api/* y /ws (docs/desarrollo.md §2).
    devProxy: {
      '/api': { target: 'http://localhost:8080/api', changeOrigin: true },
      '/ws': { target: 'ws://localhost:8080/ws', ws: true }
    }
  },

  // P9 — tipos como contrato: TypeScript estricto en todo el frontend.
  typescript: {
    strict: true,
    tsConfig: {
      // El pakset fuente de Simutrans (PNG + .dat + scripts propios) no es
      // código del frontend: fuera del typecheck (vue-tsc tropezaba con sus JS).
      exclude: ['../app/assets/pak128']
    }
  }
})
