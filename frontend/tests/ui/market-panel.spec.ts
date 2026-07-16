// @vitest-environment happy-dom
/**
 * MarketPanel — al confirmar la aceptación emite el intent con la cantidad
 * validada (forma) y envía la INTENCIÓN al servidor (stores stub + API stub).
 */
import { flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia, type Pinia } from 'pinia'
import { beforeEach, describe, expect, it } from 'vitest'
import MarketPanel from '~/components/panels/MarketPanel.vue'
import { API_CLIENT_KEY, type ApiClient, type RequestOptions } from '~/composables/useApiClient'
import type { Publication, SessionCreated } from '~/lib/api/types'
import { useMarketStore } from '~/stores/market.store'
import { useSessionStore } from '~/stores/session.store'

const MY_ID = '00000000-0000-7000-8000-0000000000aa'
const OTHER_ID = '00000000-0000-7000-8000-0000000000bb'
const PUB_ID = '00000000-0000-7000-8000-0000000000cc'
const PRODUCT_ID = '00000000-0000-7000-8000-0000000000dd'

interface RecordedCall {
  method: string
  path: string
  body: unknown
}

function createStubClient(calls: RecordedCall[]): ApiClient {
  return {
    // eslint-disable-next-line @typescript-eslint/require-await
    async request<T>(method: string, path: string, opts: RequestOptions = {}) {
      calls.push({ method, path, body: opts.body })
      const meta = { sim_time: '1-001-00:00', server_time: '2026-07-15T10:00:00Z' }
      if (method === 'POST' && path.endsWith('/acceptances')) {
        const acceptance = {
          id: '00000000-0000-7000-8000-0000000000ee',
          publication_id: PUB_ID,
          acceptor_account_id: MY_ID,
          quantity: (opts.body as { quantity: string }).quantity,
          quantity_served: '0',
          status: 'pending_draw',
          accepted_at: '2026-07-15T10:00:01Z'
        }
        return { ok: true as const, value: { data: acceptance as T, meta } }
      }
      // Catálogos y listados: vacío.
      return { ok: true as const, value: { data: [] as T, meta } }
    }
  } as ApiClient
}

const publication = {
  id: PUB_ID,
  kind: 'sell',
  publisher_account_id: OTHER_ID,
  channel: 'board',
  product_id: PRODUCT_ID,
  quantity_total: '500',
  quantity_remaining: '500',
  unit_price: '120',
  min_lot: '50',
  origin_node_id: '00000000-0000-7000-8000-0000000000ff',
  delivery_sim_seconds: 172800,
  status: 'draw_window',
  window_closes_at: new Date(Date.now() + 40_000).toISOString(),
  published_at_sim: 0
} as unknown as Publication

const session = {
  session_id: '00000000-0000-7000-8000-000000000001',
  token: 'tok',
  expires_at: '2026-07-16T10:00:00Z',
  account: { id: MY_ID, kind: 'human', name: 'Aurora Corp', status: 'active', created_at: '2026-01-01T00:00:00Z' }
} as unknown as SessionCreated

describe('MarketPanel', () => {
  let pinia: Pinia

  beforeEach(() => {
    pinia = createPinia()
    setActivePinia(pinia)
    useSessionStore().setSession(session)
    useMarketStore().setBoardResults({}, [publication])
  })

  it('emite el intent de aceptar con la cantidad confirmada y envía la intención', async () => {
    const calls: RecordedCall[] = []
    const wrapper = mount(MarketPanel, {
      global: {
        plugins: [pinia],
        provide: { [API_CLIENT_KEY as symbol]: createStubClient(calls) }
      }
    })
    await flushPromises()

    // La publicación ajena aparece en el tablón con su botón de aceptar.
    const acceptButton = wrapper.find('[data-test="accept-btn"]')
    expect(acceptButton.exists()).toBe(true)
    expect(acceptButton.attributes('disabled')).toBeUndefined()
    await acceptButton.trigger('click')

    // El modal precarga min_lot; el jugador ajusta la cantidad.
    const qtyInput = wrapper.find('[data-test="accept-qty"] input')
    expect((qtyInput.element as HTMLInputElement).value).toBe('50')
    await qtyInput.setValue('100')

    await wrapper.find('[data-test="accept-confirm"]').trigger('click')
    await flushPromises()

    // Intent emitido con la cantidad exacta (string de punto fijo).
    const emitted = wrapper.emitted('intent:accept')
    expect(emitted).toHaveLength(1)
    expect(emitted?.[0]?.[0]).toEqual({ publicationId: PUB_ID, quantity: '100' })

    // Y la intención viajó al servidor (POST …/acceptances con { quantity }).
    const post = calls.find((c) => c.method === 'POST' && c.path === `/contracts/publications/${PUB_ID}/acceptances`)
    expect(post).toBeDefined()
    expect(post?.body).toEqual({ quantity: '100' })
  })

  it('rechaza en forma una cantidad menor que min_lot sin emitir intent', async () => {
    const calls: RecordedCall[] = []
    const wrapper = mount(MarketPanel, {
      global: {
        plugins: [pinia],
        provide: { [API_CLIENT_KEY as symbol]: createStubClient(calls) }
      }
    })
    await flushPromises()

    await wrapper.find('[data-test="accept-btn"]').trigger('click')
    await wrapper.find('[data-test="accept-qty"] input').setValue('10')
    await wrapper.find('[data-test="accept-confirm"]').trigger('click')
    await flushPromises()

    expect(wrapper.emitted('intent:accept')).toBeUndefined()
    expect(calls.some((c) => c.path.endsWith('/acceptances'))).toBe(false)
    expect(wrapper.text()).toContain('Mínimo de aceptación')
  })
})
