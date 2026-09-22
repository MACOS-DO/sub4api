import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import ProxiesView from '../ProxiesView.vue'

const { list, getAllWithCount, checkProxyQuality } = vi.hoisted(() => ({
  list: vi.fn(),
  getAllWithCount: vi.fn(),
  checkProxyQuality: vi.fn(),
}))
vi.mock('@/api/admin', () => ({ adminAPI: { proxies: { list, getAllWithCount, checkProxyQuality } } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key }),
}))

const mountView = () => shallowMount(ProxiesView, {
  global: { stubs: {
    AppLayout: { template: '<div><slot /></div>' },
    TablePageLayout: { template: '<div><slot name="table" /></div>' },
    DataTable: {
      props: ['data'],
      template: `<div>
        <div v-for="row in data" :key="row.id"><slot name="cell-location" :row="row" /></div>
        <div v-for="row in data" :key="'actions-' + row.id"><slot name="cell-actions" :row="row" /></div>
      </div>`,
    },
    BaseDialog: { props: ['show'], template: '<div v-if="show"><slot /><slot name="footer" /></div>' },
  } },
})

let wrapper: ReturnType<typeof mountView>
beforeEach(() => {
  vi.clearAllMocks()
  list.mockResolvedValue({
    items: [{
      id: 9,
      name: 'proxy',
      protocol: 'http',
      host: 'proxy.example',
      port: 8080,
      status: 'active',
      country: 'Japan',
      city: 'Tokyo',
      timezone: 'Asia/Tokyo',
    }],
    total: 1,
    pages: 1,
  })
  getAllWithCount.mockResolvedValue([])
  checkProxyQuality.mockResolvedValue({
    items: [{ target: 'base_connectivity', status: 'pass' }],
    base_latency_ms: 12,
    summary: 'ok',
    exit_ip: '1.2.3.4',
    country: 'Japan',
    country_code: 'JP',
  })
})
afterEach(() => wrapper?.unmount())

describe('proxy timezone column', () => {
  it('keeps the row timezone after a quality check that omits it', async () => {
    wrapper = mountView()
    await flushPromises()
    expect(wrapper.text()).toContain('Asia/Tokyo')

    const qualityButton = wrapper.findAll('button').find(button => button.text() === 'admin.proxies.qualityCheck')
    expect(qualityButton).toBeTruthy()
    await qualityButton!.trigger('click')
    await flushPromises()

    expect(checkProxyQuality).toHaveBeenCalledWith(9)
    expect(wrapper.text()).toContain('Asia/Tokyo')
  })
})
