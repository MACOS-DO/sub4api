import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick, ref } from 'vue'
import CodexDiagnosticModal from '../CodexDiagnosticModal.vue'
import Select from '@/components/common/Select.vue'
import zhAccounts from '@/i18n/locales/zh/admin/accounts'
import type { Account } from '@/types'

const { events, fingerprint, ownKeys, invalidation, diagnose, getById, harvest } = vi.hoisted(() => ({
  events: vi.fn(), fingerprint: vi.fn(), ownKeys: vi.fn(), invalidation: vi.fn(), diagnose: vi.fn(), getById: vi.fn(), harvest: vi.fn()
}))
vi.mock('@/api/admin/codexTickets', () => ({
  events, fingerprint, ownKeys, invalidation, diagnose, harvest, refreshFingerprint: vi.fn()
}))
vi.mock('@/api/admin', () => ({ adminAPI: { accounts: { getById } } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ locale: ref('zh-CN'), t: (key: string, values: Record<string, string | number> = {}) => {
  const message = key.split('.').reduce<unknown>((value, part) => value && typeof value === 'object' ? (value as Record<string, unknown>)[part] : undefined, { admin: zhAccounts })
  return typeof message === 'string' ? message.replace(/\{(\w+)\}/g, (_, name) => String(values[name] ?? name)) : key
} }) }))

const account = {
  id: 19, name: 'GPT Account', platform: 'openai', type: 'oauth',
  codex_turn_tickets: [{ model: 'gpt-5.6-sol', ready: true, length: 292, turn_state_present: true, cookie_present: false, harvest_enabled: true }]
} as Account

function mountModal(value: Account = account) {
  return mount(CodexDiagnosticModal, {
    props: { show: true, account: value },
    attachTo: document.body,
    global: {
      stubs: {
        Teleport: true, Select: true, DateRangePicker: true, CodexDiagnosticModelPicker: true, Icon: true
      }
    }
  })
}

type DiagnosticView = {
  selectedKey: number | null
  selectedModels: string[]
  runDiagnostic: () => Promise<void>
  cancelDiagnostic: () => void
  showDiagnosticStage: (stage: 'setup' | 'results') => Promise<void>
  loadKeys: () => Promise<void>
  loadModels: (refresh?: boolean) => Promise<void>
}
function configure(wrapper: ReturnType<typeof mountModal>, models = ['gpt-5.6-sol', 'gpt-5.5']) {
  const view = wrapper.vm as unknown as DiagnosticView
  view.selectedKey = 4
  view.selectedModels = models
  return view
}
function deferred() {
  let resolve!: (value: unknown) => void
  let reject!: (cause: Error) => void
  const promise = new Promise((onResolve, onReject) => { resolve = onResolve; reject = onReject })
  return { promise, resolve, reject }
}

describe('Codex diagnostic modal', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    fingerprint.mockResolvedValue({ commit: 'abc123', models: ['gpt-5.6-sol', 'gpt-5.5', 'gpt-5.4', 'gpt-4o'] })
    getById.mockResolvedValue(account)
    ownKeys.mockResolvedValue([{ id: 4, name: 'Billing Key', status: 'active', quota: 0, quota_used: 0 }, { id: 9, name: 'Other Key', status: 'active', quota: 0, quota_used: 0 }])

  })

  it('allows checking a missing ticket with account and model harvesting disabled', async () => {
    const noTicket = {
      ...account,
      extra: { codex_ticket_harvest_enabled: false, codex_ticket_harvest_models: { 'gpt-5.6-sol': false } },
      codex_turn_tickets: [{ model: 'gpt-5.6-sol', ready: false, harvest_enabled: false, turn_state_present: false, cookie_present: false }]
    } as Account
    getById.mockResolvedValue(noTicket)
    diagnose.mockResolvedValue({ items: [{ model: 'gpt-5.6-sol', status: 'normal' }] })
    const wrapper = mountModal(noTicket)
    await flushPromises()
    configure(wrapper, ['gpt-5.6-sol'])
    await nextTick()
    const start = wrapper.findAll('button').find(button => button.text().startsWith('开始检测'))!
    expect(start.attributes('disabled')).toBeUndefined()
    await start.trigger('click')
    await flushPromises()
    expect(diagnose).toHaveBeenCalledWith(19, 4, ['gpt-5.6-sol'], expect.any(AbortSignal))
    expect(harvest).not.toHaveBeenCalled()
    expect(events).not.toHaveBeenCalled()
    expect(getById).not.toHaveBeenCalled()
    expect(wrapper.find('[data-model="gpt-5.6-sol"]').attributes('data-status')).toBe('normal')
    wrapper.unmount()
  })

  it('sends one model per diagnostic request and retains completed results', async () => {
    const wrapper = mountModal()
    await flushPromises()
    let finishFirst!: (value: unknown) => void
    diagnose.mockImplementationOnce(() => new Promise(resolve => { finishFirst = resolve }))
      .mockResolvedValueOnce({ items: [{ model: 'gpt-5.5', status: 'normal' }] })
    const view = wrapper.vm as unknown as {
      selectedKey: number | null; selectedModels: string[]; runDiagnostic: () => Promise<void>
    }
    view.selectedKey = 4
    view.selectedModels = ['gpt-5.6-sol', 'gpt-5.5']
    const running = view.runDiagnostic()
    expect(diagnose).toHaveBeenCalledTimes(1)
    expect(diagnose).toHaveBeenCalledWith(19, 4, ['gpt-5.6-sol'], expect.any(AbortSignal))
    finishFirst({ items: [{ model: 'gpt-5.6-sol', status: 'normal' }] })
    await running
    await flushPromises()
    expect(diagnose).toHaveBeenCalledTimes(2)
    expect(diagnose).toHaveBeenLastCalledWith(19, 4, ['gpt-5.5'], expect.any(AbortSignal))
    expect(wrapper.text()).toContain('2 / 2')
    wrapper.unmount()
  })

  it('shows the whole queue before the first response, focuses results and freezes the billing key', async () => {
    const first = deferred()
    diagnose.mockImplementationOnce(() => first.promise)
      .mockResolvedValueOnce({ items: [{ model: 'gpt-5.5', status: 'normal' }] })
    const wrapper = mountModal()
    await flushPromises()
    expect(wrapper.text()).not.toContain('流水保留 90 天')
    expect(wrapper.find('[data-test="diagnostic-progress"]').exists()).toBe(false)
    const view = configure(wrapper)
    const running = view.runDiagnostic()
    await nextTick()
    expect(wrapper.find('[data-test="diagnostic-progress"]').exists()).toBe(true)
    expect(wrapper.find('[data-model="gpt-5.6-sol"]').attributes('data-status')).toBe('running')
    expect(wrapper.find('[data-model="gpt-5.5"]').attributes('data-status')).toBe('pending')
    expect(wrapper.find('[role="progressbar"]').attributes('aria-valuenow')).toBe('0')
    expect(document.activeElement?.textContent).toBe('检测进行中')
    expect(diagnose).toHaveBeenCalledTimes(1)
    view.selectedKey = 9
    view.selectedModels = ['gpt-4o']
    first.resolve({ items: [{ model: 'gpt-5.6-sol', status: 'normal' }] })
    await running
    await flushPromises()
    expect(diagnose).toHaveBeenLastCalledWith(19, 4, ['gpt-5.5'], expect.any(AbortSignal))
    expect(wrapper.get('[data-test="diagnostic-key"]').text()).toBe('Billing Key · #4')
    expect(wrapper.findAll('article[data-model]')).toHaveLength(2)
    wrapper.unmount()
  })

  it('keeps request failures in their row, renders details safely and continues the queue', async () => {
    diagnose.mockRejectedValueOnce(new Error('<img src=x onerror=alert(1)> request failed'))
      .mockResolvedValueOnce({ items: [{ model: 'gpt-5.5', status: 'normal' }] })
    const wrapper = mountModal()
    await flushPromises()
    await configure(wrapper).runDiagnostic()
    await flushPromises()
    expect(diagnose).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    const failed = wrapper.get('[data-model="gpt-5.6-sol"]')
    expect(failed.attributes('data-status')).toBe('failed')
    expect(failed.text()).not.toContain('request failed')
    await failed.get('button[aria-expanded]').trigger('click')
    const expanded = wrapper.get('[data-model="gpt-5.6-sol"]')
    expect(expanded.get('button[aria-expanded]').attributes('aria-expanded')).toBe('true')
    expect(expanded.text()).toContain('<img src=x onerror=alert(1)> request failed')
    expect(expanded.find('img').exists()).toBe(false)
    expect(wrapper.get('[data-model="gpt-5.5"]').attributes('data-status')).toBe('normal')
    wrapper.unmount()
  })

  it('explains the shared template and shows an actionable template failure', async () => {
    diagnose.mockResolvedValueOnce({ items: [{ model: 'gpt-5.6-sol', status: 'failed', reason: 'template_invalid' }] })
    const wrapper = mountModal()
    await flushPromises()
    expect(wrapper.text()).toContain('Codex 打票与降智检测模板')
    await configure(wrapper, ['gpt-5.6-sol']).runDiagnostic()
    await flushPromises()
    const failed = wrapper.get('[data-model="gpt-5.6-sol"]')
    expect(failed.attributes('data-status')).toBe('failed')
    await failed.get('button[aria-expanded]').trigger('click')
    expect(wrapper.text()).toContain('共享模板配置无效，请在系统设置中修正后重试。')
    wrapper.unmount()
  })

  it('retains completed results when cancelled and marks unfinished and unstarted models separately', async () => {
    const second = deferred()
    diagnose.mockResolvedValueOnce({ items: [{ model: 'gpt-5.6-sol', status: 'normal' }] })
      .mockImplementationOnce(() => second.promise)
    const wrapper = mountModal()
    await flushPromises()
    const view = configure(wrapper, ['gpt-5.6-sol', 'gpt-5.5', 'gpt-5.4'])
    const running = view.runDiagnostic()
    await flushPromises()
    expect(diagnose).toHaveBeenCalledTimes(2)
    view.cancelDiagnostic()
    await nextTick()
    expect(diagnose.mock.calls[1][3].aborted).toBe(true)
    expect(wrapper.get('[data-model="gpt-5.6-sol"]').attributes('data-status')).toBe('normal')
    expect(wrapper.get('[data-model="gpt-5.5"]').attributes('data-status')).toBe('stopped')
    expect(wrapper.get('[data-model="gpt-5.4"]').attributes('data-status')).toBe('not_run')
    expect(wrapper.get('[role="progressbar"]').attributes('aria-valuenow')).toBe('1')
    second.resolve({ items: [{ model: 'gpt-5.5', status: 'normal' }] })
    await running
    await flushPromises()
    expect(wrapper.get('[data-model="gpt-5.5"]').attributes('data-status')).toBe('stopped')
    expect(diagnose).toHaveBeenCalledTimes(2)
    await view.showDiagnosticStage('setup')
    expect(view.selectedModels).toEqual(['gpt-5.6-sol', 'gpt-5.5', 'gpt-5.4'])
    await view.showDiagnosticStage('results')
    expect(diagnose).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).toContain('已取消')
    wrapper.unmount()
  })

  it.each(['close', 'account'] as const)('ignores an old request after a %s change even when another check has started', async (change) => {
    const oldRequest = deferred()
    const newRequest = deferred()
    diagnose.mockImplementationOnce(() => oldRequest.promise).mockImplementationOnce(() => newRequest.promise)
    const wrapper = mountModal()
    await flushPromises()
    const view = configure(wrapper, ['gpt-5.6-sol', 'gpt-5.4'])
    const oldRun = view.runDiagnostic()
    if (change === 'close') {
      await wrapper.setProps({ show: false })
      await wrapper.setProps({ show: true })
    } else await wrapper.setProps({ account: { ...account, id: 20 } })
    await flushPromises()
    configure(wrapper, ['gpt-5.5'])
    const newRun = view.runDiagnostic()
    await nextTick()
    expect(diagnose).toHaveBeenCalledTimes(2)
    oldRequest.reject(new Error('stale request failure'))
    await oldRun
    await flushPromises()
    expect(wrapper.text()).not.toContain('stale request failure')
    expect(wrapper.findAll('article[data-model]')).toHaveLength(1)
    expect(wrapper.get('[data-model="gpt-5.5"]').attributes('data-status')).toBe('running')
    newRequest.resolve({ items: [{ model: 'gpt-5.5', status: 'normal' }] })
    await newRun
    expect(diagnose).toHaveBeenCalledTimes(2)
    expect(diagnose).toHaveBeenLastCalledWith(change === 'account' ? 20 : 19, 4, ['gpt-5.5'], expect.any(AbortSignal))
    wrapper.unmount()
  })

  it('returns to retest with previous choices and results without starting another billable request', async () => {
    diagnose.mockImplementation((_accountID: number, _keyID: number, requested: string[]) => Promise.resolve({ items: [{ model: requested[0], status: 'normal' }] }))
    const wrapper = mountModal()
    await flushPromises()
    const view = configure(wrapper, ['gpt-5.6-sol'])
    await view.runDiagnostic()
    await flushPromises()
    expect(wrapper.get('[data-model="gpt-5.6-sol"]').attributes('data-status')).toBe('normal')
    const retest = wrapper.get('[data-action="retest"]')
    expect(retest.text()).toBe('返回重测')
    await retest.trigger('click')
    expect(view.selectedKey).toBe(4)
    expect(view.selectedModels).toEqual(['gpt-5.6-sol'])
    expect(diagnose).toHaveBeenCalledTimes(1)
    view.selectedKey = 9
    view.selectedModels = ['gpt-5.5']
    await view.showDiagnosticStage('results')
    expect(wrapper.get('[data-test="diagnostic-key"]').text()).toBe('Billing Key · #4')
    expect(diagnose).toHaveBeenCalledTimes(1)
    await view.showDiagnosticStage('setup')
    await view.runDiagnostic()
    await flushPromises()
    expect(diagnose).toHaveBeenCalledTimes(2)
    expect(diagnose).toHaveBeenLastCalledWith(19, 9, ['gpt-5.5'], expect.any(AbortSignal))
    expect(wrapper.find('[data-model="gpt-5.6-sol"]').exists()).toBe(false)
    wrapper.unmount()
  })
  it('uses the shared searchable Select and excludes ticket and request-policy UI', async () => {
    const wrapper = mountModal()
    await flushPromises()
    expect(wrapper.findComponent(Select).props('searchable')).toBe(true)
    expect(wrapper.findComponent(Select).props('options')).toEqual(expect.arrayContaining([{ value: 4, label: 'Billing Key · #4' }]))
    expect(wrapper.find('select').exists()).toBe(false)
    expect(wrapper.find('nav').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('请求策略')
    expect(wrapper.text()).not.toContain('票据中心')
    expect(wrapper.text()).not.toContain('当前票据')
    expect(getById).not.toHaveBeenCalled()
    expect(events).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('retries a failed model load independently of successful API keys', async () => {
    fingerprint.mockRejectedValueOnce(new Error('model list unavailable'))
    const wrapper = mountModal()
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('model list unavailable')
    expect(wrapper.get('[data-action="start"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[role="alert"] button').trigger('click')
    await flushPromises()
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(fingerprint).toHaveBeenCalledTimes(2)
    expect(ownKeys).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  it('retries a failed key load and disables submission without an eligible key', async () => {
    ownKeys.mockRejectedValueOnce(new Error('keys unavailable')).mockResolvedValueOnce([])
    const wrapper = mountModal()
    await flushPromises()
    expect(wrapper.get('[role="alert"]').text()).toContain('keys unavailable')
    await wrapper.get('[role="alert"] button').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('没有可用的本人 API Key')
    expect(wrapper.get('[data-action="start"]').attributes('disabled')).toBeDefined()
    expect(fingerprint).toHaveBeenCalledTimes(1)
    expect(diagnose).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('ignores old key and model loads after changing accounts', async () => {
    const oldKeys = deferred()
    const oldModels = deferred()
    ownKeys.mockImplementationOnce(() => oldKeys.promise)
    fingerprint.mockImplementationOnce(() => oldModels.promise)
    const wrapper = mountModal()
    await wrapper.setProps({ account: { ...account, id: 20 } })
    await flushPromises()
    oldKeys.reject(new Error('stale key failure'))
    oldModels.resolve({ commit: 'stale', models: ['stale-model'] })
    await flushPromises()
    expect(wrapper.text()).not.toContain('stale')
    expect(wrapper.findComponent(Select).props('options')).toEqual(expect.arrayContaining([{ value: 4, label: 'Billing Key · #4' }]))
    expect(wrapper.get('[data-action="start"]').attributes('disabled')).toBeDefined()
    const view = configure(wrapper, ['gpt-5.5'])
    diagnose.mockResolvedValue({ items: [{ model: 'gpt-5.5', status: 'normal' }] })
    await view.runDiagnostic()
    expect(diagnose).toHaveBeenCalledWith(20, 4, ['gpt-5.5'], expect.any(AbortSignal))
    wrapper.unmount()
  })

  it('prevents repeated submissions and cancels immediately on close', async () => {
    const pending = deferred()
    diagnose.mockImplementationOnce(() => pending.promise)
    const wrapper = mountModal()
    await flushPromises()
    const view = configure(wrapper)
    const running = view.runDiagnostic()
    await view.runDiagnostic()
    expect(diagnose).toHaveBeenCalledTimes(1)
    await wrapper.get('button[aria-label="关闭降智检测"]').trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(1)
    expect(diagnose.mock.calls[0][3].aborted).toBe(true)
    pending.resolve({ items: [{ model: 'gpt-5.6-sol', status: 'normal' }] })
    await running
    expect(diagnose).toHaveBeenCalledTimes(1)
    expect(wrapper.emitted('completed')).toEqual([[19]])
    wrapper.unmount()
  })

  it('counts each conclusion separately and handles a missing model result', async () => {
    diagnose.mockResolvedValueOnce({ items: [{ model: 'gpt-5.6-sol', status: 'normal' }] })
      .mockResolvedValueOnce({ items: [{ model: 'gpt-5.5', status: 'degraded' }] })
      .mockResolvedValueOnce({ items: [{ model: 'gpt-5.4', status: 'uncertain' }] })
      .mockResolvedValueOnce({ items: [] })
    const wrapper = mountModal()
    await flushPromises()
    await configure(wrapper, ['gpt-5.6-sol', 'gpt-5.5', 'gpt-5.4', 'gpt-4o']).runDiagnostic()
    await flushPromises()
    expect(wrapper.get('[data-test="diagnostic-summary"]').findAll('strong').map(item => item.text())).toEqual(['1', '1', '1', '1'])
    const failed = wrapper.get('[data-model="gpt-4o"]')
    expect(failed.text()).toContain('请求未完成，不代表降智')
    await failed.get('button[aria-expanded]').trigger('click')
    expect(failed.text()).toContain('未返回此模型的检测结果')
    expect(wrapper.emitted('completed')).toEqual([[19]])
    wrapper.unmount()
  })

})
