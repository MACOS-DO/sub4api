import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import CodexTicketProxyPool from '../CodexTicketProxyPool.vue'

const { get, put, getAll } = vi.hoisted(() => ({ get: vi.fn(), put: vi.fn(), getAll: vi.fn() }))

vi.mock('@/api/client', () => ({ apiClient: { get, put } }))
vi.mock('@/api/admin', () => ({ adminAPI: { proxies: { getAll } } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ locale: ref('zh-CN') }) }))
vi.mock('vue-router', () => ({ onBeforeRouteLeave: vi.fn() }))

const proxy = (id: number, name: string, extra: Record<string, unknown> = {}) => ({
  id, name, protocol: 'http', host: `${name.toLowerCase()}.example.com`, port: 8000 + id, status: 'active', expires_at: null, ...extra
})
const proxies = [proxy(1, 'A'), proxy(2, 'B'), proxy(3, 'C')]
const view = (overrides: Record<string, unknown> = {}) => ({
  mode: 'custom', proxy_ids: [3], source: 'pool', effective: [{ id: 3, name: 'C' }], unavailable: [], ...overrides
})

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>(r => { resolve = r })
  return { promise, resolve }
}

describe('CodexTicketProxyPool', () => {
  beforeEach(() => {
    getAll.mockResolvedValue(proxies)
    get.mockResolvedValue({ data: view() })
  })
  afterEach(() => vi.clearAllMocks())

  it('shows which proxies tickets actually rotate through', async () => {
    const wrapper = mount(CodexTicketProxyPool)
    await flushPromises()
    expect(wrapper.get('[data-testid="pool-effective"]').text()).toContain('当前打票轮换：C')
    expect(wrapper.find('[data-testid="pool-dirty"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('flags unsaved edits and only treats the saved server state as current', async () => {
    put.mockResolvedValue({ data: view({ proxy_ids: [1, 2], effective: [{ id: 1, name: 'A' }, { id: 2, name: 'B' }] }) })
    const wrapper = mount(CodexTicketProxyPool)
    await flushPromises()
    await wrapper.get('[data-testid="pool-proxy-3"]').setValue(false)
    await wrapper.get('[data-testid="pool-proxy-1"]').setValue(true)
    await wrapper.get('[data-testid="pool-proxy-2"]').setValue(true)
    expect(wrapper.find('[data-testid="pool-dirty"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="pool-effective"]').text()).toContain('C')

    await wrapper.get('[data-testid="pool-save"]').trigger('click')
    await flushPromises()
    expect(put).toHaveBeenCalledWith('/admin/proxies/codex-ticket-pool', { mode: 'custom', proxy_ids: [1, 2] })
    expect(wrapper.find('[data-testid="pool-dirty"]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="pool-effective"]').text()).toContain('A、B')
    wrapper.unmount()
  })

  it('keeps inputs disabled until the saved pool has loaded', async () => {
    const pending = deferred<{ data: ReturnType<typeof view> }>()
    get.mockReturnValue(pending.promise)
    const wrapper = mount(CodexTicketProxyPool)
    await flushPromises()
    expect((wrapper.get('[data-testid="pool-mode-custom"]').element as HTMLInputElement).disabled).toBe(true)
    pending.resolve({ data: view() })
    await flushPromises()
    expect((wrapper.get('[data-testid="pool-mode-custom"]').element as HTMLInputElement).disabled).toBe(false)
    wrapper.unmount()
  })

  it('lists selections that are no longer available and does not resubmit them', async () => {
    get.mockResolvedValue({ data: view({ proxy_ids: [1, 9], effective: [{ id: 1, name: 'A' }], unavailable: [{ id: 9, name: 'Old', reason: 'deleted' }] }) })
    put.mockResolvedValue({ data: view({ proxy_ids: [1, 2], effective: [{ id: 1, name: 'A' }, { id: 2, name: 'B' }] }) })
    const wrapper = mount(CodexTicketProxyPool)
    await flushPromises()
    expect(wrapper.get('[data-testid="pool-hidden"]').text()).toContain('Old')
    await wrapper.get('[data-testid="pool-proxy-2"]').setValue(true)
    await wrapper.get('[data-testid="pool-save"]').trigger('click')
    await flushPromises()
    expect(put).toHaveBeenCalledWith('/admin/proxies/codex-ticket-pool', { mode: 'custom', proxy_ids: [1, 2] })
    wrapper.unmount()
  })

  it('says the pool is ignored when tickets use each account proxy', async () => {
    get.mockResolvedValue({ data: view({ source: 'account' }) })
    const wrapper = mount(CodexTicketProxyPool)
    await flushPromises()
    expect(wrapper.get('[data-testid="pool-effective"]').text()).toContain('此代理池不参与')
    wrapper.unmount()
  })

  it('reload picks up proxies added elsewhere without discarding unsaved edits', async () => {
    getAll.mockResolvedValueOnce([proxy(3, 'C')])
    const wrapper = mount(CodexTicketProxyPool)
    await flushPromises()
    expect(wrapper.find('[data-testid="pool-proxy-1"]').exists()).toBe(false)
    await wrapper.get('[data-testid="pool-mode-all"]').setValue(true)
    await (wrapper.vm as unknown as { reload: () => Promise<void> }).reload()
    await flushPromises()
    expect((wrapper.get('[data-testid="pool-mode-all"]').element as HTMLInputElement).checked).toBe(true)
    await wrapper.get('[data-testid="pool-mode-custom"]').setValue(true)
    expect(wrapper.find('[data-testid="pool-proxy-1"]').exists()).toBe(true)
    wrapper.unmount()
  })
})
