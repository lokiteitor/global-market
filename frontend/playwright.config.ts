import { defineConfig } from '@playwright/test'

/**
 * Playwright — smoke E2E de navegador real (FAD §22: pirámide de tests; este
 * es el vértice, deliberadamente mínimo).
 *
 * PRERREQUISITO: el stack lo levanta quien ejecuta (NO hay `webServer` aquí):
 *   - backend dev en http://localhost:8080 (gateway + engine + bots; ver
 *     docs/runbooks/local.md) — el spec se SALTA limpiamente si
 *     http://localhost:8080/healthz no responde (gate suave en beforeAll);
 *   - frontend dev en http://localhost:3000 (`npm run dev` en /frontend). El
 *     hook `window.__II_WORLD_READY__` que espera el spec solo existe en dev
 *     (`import.meta.dev`, ver app/pages/play.vue), nunca en producción.
 *
 * Ejecución: `npm run test:e2e`. Los screenshots pedidos por el spec quedan en
 * tests/e2e-browser/artifacts/ (ignorado por git); los adjuntos automáticos
 * (`screenshot: 'on'`) en tests/e2e-browser/test-results/.
 */
export default defineConfig({
  testDir: 'tests/e2e-browser',
  outputDir: 'tests/e2e-browser/test-results',
  reporter: 'list',
  // El bootstrap de /play (REST + WS + Phaser) puede tardar en frío.
  timeout: 120_000,
  use: {
    baseURL: 'http://localhost:3000',
    headless: true,
    screenshot: 'on',
    video: 'off',
  },
})
