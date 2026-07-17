import { fileURLToPath } from 'node:url'

/**
 * Configuración Nuxt del cliente de Imperio Industrial (FAD v1.1).
 *
 * - SSR por defecto: el shell (portal/login/lobby) se sirve renderizado;
 *   la ruta de juego `/play` (client-only) llega en el Incremento 5 y NO se configura aquí.
 * - `runtimeConfig.public.apiBase` se valida con zod al arrancar (config/env.ts + plugin).
 * - En dev, Nitro proxya `/api` hacia el gateway Go local (mismo origen, sin CORS);
 *   en producción Caddy cumple ese mismo papel (FAD C16).
 */
export default defineNuxtConfig({
  compatibilityDate: '2026-07-16',

  modules: ['@pinia/nuxt', '@nuxt/eslint'],

  // Aliases hacia el código framework-agnostic que vive FUERA de app/ (FAD §10.3):
  // kernel puro (shared/), dominio (domain/) y capa de red (network/).
  // Nuxt los propaga a Vite y a los tsconfig generados.
  alias: {
    '~shared': fileURLToPath(new URL('./shared', import.meta.url)),
    '~domain': fileURLToPath(new URL('./domain', import.meta.url)),
    '~network': fileURLToPath(new URL('./network', import.meta.url)),
  },

  css: ['~/assets/styles/index.scss'],

  vite: {
    css: {
      preprocessorOptions: {
        scss: {
          // Permite `@use "settings" as s;` / `@use "tools" as t;` desde cualquier
          // fichero SCSS (componentes incluidos) sin rutas relativas frágiles (FAD §10.4).
          loadPaths: [fileURLToPath(new URL('./app/assets/styles', import.meta.url))],
        },
      },
    },
  },

  runtimeConfig: {
    public: {
      // Prefijo del contrato REST v1.1.0. Sobreescribible por entorno con
      // NUXT_PUBLIC_API_BASE. Validado con zod en config/env.ts (FAD §23.7).
      apiBase: '/api/v1',
    },
  },

  nitro: {
    // Dev: mismo origen que el gateway Go (http://localhost:8080) para evitar CORS.
    // En producción, Caddy enruta /api hacia el gateway con la misma forma.
    devProxy: {
      '/api': {
        target: 'http://localhost:8080/api',
        changeOrigin: true,
      },
    },
  },

  typescript: {
    strict: true,
    // Typecheck como parte del build (gate de FAD §23.2); en dev no bloquea el HMR.
    typeCheck: 'build',
    tsConfig: {
      compilerOptions: {
        noUncheckedIndexedAccess: true,
        exactOptionalPropertyTypes: true,
        noImplicitOverride: true,
        noFallthroughCasesInSwitch: true,
      },
    },
  },

  eslint: {
    config: {
      // Prettier es el dueño del formato (FAD §23.4); ESLint no aporta reglas estilísticas.
      stylistic: false,
    },
  },
})
