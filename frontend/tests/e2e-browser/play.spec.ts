import { expect, test, type Page } from '@playwright/test'

/**
 * Smoke E2E de /play con navegador real (Chromium headless) contra el stack
 * dev REAL — ver prerrequisitos en playwright.config.ts (backend :8080 +
 * `npm run dev` :3000; aquí NO se levanta nada).
 *
 * Flujo: /login (Demo) → /lobby → /play → canvas Phaser + bootstrap completo
 * (hook dev `window.__II_WORLD_READY__`, app/pages/play.vue) → asserts de HUD
 * (saldo, sim-time AAA-DDD-HH:MM, sidebar) → panel Mercado (el tablón
 * renderiza: filas si los bots publican, o el vacío sin error) → screenshots.
 *
 * SOLO LECTURA: login + GETs del bootstrap + toggles de UI (panel/overlay).
 * El test jamás ejecuta comandos que muten estado de juego (no acepta, no
 * publica, no construye, no despacha).
 *
 * Gate suave: si el backend no responde en /healthz, el spec entero se salta
 * con mensaje claro (sin backend, `npm run test:e2e` termina en verde).
 */

/** Sonda de salud del gateway dev (fuera de /api/v1, docs/runbooks/local.md). */
const HEALTHZ_URL = 'http://localhost:8080/healthz'

/** Credenciales dev sembradas por el backend local. */
const ACCOUNT_NAME = 'Demo'
const ACCOUNT_SECRET = 'demo-secret-dev'

/** Formato del contrato para el sim-time (shared/simtime: AAA-DDD-HH:MM). */
const SIM_TIME_RE = /^\d{3}-\d{3}-\d{2}:\d{2}$/

/**
 * Espera a que Vue haya HIDRATADO la página antes de interactuar. Sin esto,
 * en dev (Vite sirve módulos bajo demanda) Playwright puede rellenar y enviar
 * el formulario antes de que `@submit.prevent` esté enganchado, provocando un
 * submit nativo GET (`/login?account_name=...`) y un test colgado en
 * `waitForURL`. `window.useNuxtApp` solo existe en dev — como los demás hooks
 * de este spec (ver playwright.config.ts: el smoke corre contra el stack dev).
 */
async function waitForHydration(page: Page): Promise<void> {
  await page.waitForFunction(() => {
    try {
      const w = window as Window & { useNuxtApp?: () => { isHydrating?: boolean } }
      return w.useNuxtApp !== undefined && w.useNuxtApp().isHydrating === false
    } catch {
      return false
    }
  })
}

let backendUp = false

test.beforeAll(async () => {
  try {
    const response = await fetch(HEALTHZ_URL, { signal: AbortSignal.timeout(2_000) })
    backendUp = response.ok
  } catch {
    backendUp = false
  }
})

test.beforeEach(() => {
  test.skip(
    !backendUp,
    `Backend no disponible (${HEALTHZ_URL} no responde). ` +
      'Smoke E2E omitido: levanta el stack dev (gateway :8080 y `npm run dev` :3000) y reintenta.',
  )
})

test('smoke /play: login → lobby → mundo listo → HUD → tablón → overlay de red', async ({
  page,
}) => {
  // ── Login → /lobby (nombre de la corporación visible) ──────────────────────
  await page.goto('/login')
  await waitForHydration(page)
  await page.locator('input[name="account_name"]').fill(ACCOUNT_NAME)
  await page.locator('input[name="secret"]').fill(ACCOUNT_SECRET)
  await page.getByRole('button', { name: 'Entrar' }).click()

  await page.waitForURL('**/lobby')
  await expect(page.getByText(`Corporación ${ACCOUNT_NAME}`)).toBeVisible()

  // ── /play: canvas de Phaser + bootstrap REST/WS completo ───────────────────
  // Navegación SPA vía el enlace del lobby (nunca `page.goto('/play')`: una
  // navegación dura recarga la app y pierde el token, que vive en memoria por
  // diseño — FAD §seguridad — así que el middleware auth devolvería a /login).
  await page.getByRole('link', { name: 'Entrar al mundo' }).click()
  await page.waitForURL('**/play')
  await expect(page.locator('[data-testid="canvas-host"] canvas')).toBeVisible({
    timeout: 30_000,
  })
  await page.waitForFunction(
    () => (window as Window & { __II_WORLD_READY__?: boolean }).__II_WORLD_READY__ === true,
    undefined,
    { timeout: 60_000 },
  )

  // ── Top bar: saldo del ledger y sim-time con formato del contrato ──────────
  await expect(page.getByTestId('hud-cash')).not.toHaveText('')
  await expect(page.getByTestId('hud-sim-time')).toHaveText(SIM_TIME_RE)

  // ── Sidebar visible (herramientas + lanzador de paneles) ───────────────────
  await expect(page.getByTestId('tool-select')).toBeVisible()
  await expect(page.getByTestId('panel-market')).toBeVisible()

  // ── Panel Mercado: el tablón renderiza sin error ───────────────────────────
  // Los bots publican, así que lo normal es ver filas; si el tablón está vacío
  // en este instante, basta con que renderice el estado vacío (jamás la rama
  // de error: si MarketBoard muestra el banner de error, ninguno de los dos
  // localizadores aparece y el assert falla por timeout).
  await page.getByTestId('panel-market').click()
  await expect(page.getByTestId('market-tab-board')).toBeVisible()
  const boardRows = page.getByTestId('board-row')
  const boardEmpty = page.getByText('El tablón no tiene publicaciones')
  await expect(boardRows.first().or(boardEmpty)).toBeVisible({ timeout: 15_000 })

  await page.screenshot({ path: 'tests/e2e-browser/artifacts/play.png', fullPage: true })

  // ── Overlay de red logística (toggle de UI, sin mutación) ──────────────────
  // Cierra el panel (segundo click = toggle) para que el mapa quede a la vista
  // y activa el overlay de red desde la sidebar.
  await page.getByTestId('panel-market').click()
  await page.getByLabel('Red logística').check()
  // Margen para que el motor pinte la capa del overlay antes de capturar.
  await page.waitForTimeout(1_000)
  await page.screenshot({ path: 'tests/e2e-browser/artifacts/play-network.png', fullPage: true })
})
