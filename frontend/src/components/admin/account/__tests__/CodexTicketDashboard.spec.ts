import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick, ref } from 'vue'
import CodexTicketDashboard from '../CodexTicketDashboard.vue'
import type { Account } from '@/types'

const { events, fingerprint, ownKeys, invalidation, diagnose, getById, harvest } = vi.hoisted(() => ({
  events: vi.fn(), fingerprint: vi.fn(), ownKeys: vi.fn(), invalidation: vi.fn(), diagnose: vi.fn(), getById: vi.fn(), harvest: vi.fn()
}))
vi.mock('@/api/admin/codexTickets', () => ({
  events, fingerprint, ownKeys, invalidation, diagnose, harvest, refreshFingerprint: vi.fn()
}))
vi.mock('@/api/admin', () => ({ adminAPI: { accounts: { getById } } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ locale: ref('zh-CN'), t: (key: string) => key }) }))

const account = {
  id: 19, name: 'GPT Account', platform: 'openai', type: 'oauth',
  codex_turn_tickets: [{ model: 'gpt-5.6-sol', ready: true, length: 292, turn_state_present: true, cookie_present: false, harvest_enabled: true }]
} as Account
const attempt = { id: 1, model: 'gpt-5.6-sol', occurred_at: '2026-09-22T08:00:00Z', kind: 'miss', reason_code: 'fingerprint_mismatch' }
const expired = { id: 2, model: 'gpt-5.6-sol', occurred_at: '2026-09-22T09:00:00Z', kind: 'invalidation', reason_code: 'upstream_new_turn_state' }

function mountDashboard(initialDiagnostic = false, value: Account = account) {
  return mount(CodexTicketDashboard, {
    props: { show: true, account: value, initialDiagnostic },
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
}
function configure(wrapper: ReturnType<typeof mountDashboard>, models = ['gpt-5.6-sol', 'gpt-5.5']) {
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

describe('Codex ticket dashboard', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    diagnose.mockReset()
    fingerprint.mockResolvedValue({ commit: 'abc123', models: ['gpt-5.6-sol', 'gpt-5.5'] })
    getById.mockResolvedValue(account)
    ownKeys.mockResolvedValue([{ id: 4, name: 'Billing Key', status: 'active', quota: 0, quota_used: 0 }])
    events.mockImplementation((_id: number, params: { filter: string }) => Promise.resolve({
      items: params.filter === 'invalidation' ? [expired] : [attempt], total: 1
    }))
    invalidation.mockResolvedValue({ ...expired, original_ticket: 'old-secret', returned_ticket: 'new-secret' })
  })

  it('separates current tickets, failed attempts and invalidation history', async () => {
    const wrapper = mountDashboard()
    await flushPromises()
    expect(wrapper.text()).toContain('1 / 1')
    expect(wrapper.text()).not.toContain('fingerprint_mismatch')
    expect(events).not.toHaveBeenCalled()

    const tab = (label: string) => wrapper.findAll('nav button').find(button => button.text() === label)!
    await tab('打票流水').trigger('click')
    await flushPromises()
    expect(events).toHaveBeenLastCalledWith(19, expect.objectContaining({ filter: 'attempts' }))
    expect(wrapper.text()).toContain('fingerprint_mismatch')
    expect(wrapper.text()).not.toContain('old-secret')

    await tab('票据过期历史').trigger('click')
    await flushPromises()
    expect(events).toHaveBeenLastCalledWith(19, expect.objectContaining({ filter: 'invalidation' }))
    expect(invalidation).not.toHaveBeenCalled()
    expect(wrapper.text()).not.toContain('old-secret')
    await wrapper.find('section[aria-label="票据过期历史"] button').trigger('click')
    await flushPromises()
    expect(invalidation).toHaveBeenCalledWith(19, 2)
    expect(wrapper.text()).toContain('old-secret')
    wrapper.unmount()
  })

  it('allows checking a missing ticket with account and model harvesting disabled', async () => {
    const noTicket = {
      ...account,
      extra: { codex_ticket_harvest_enabled: false, codex_ticket_harvest_models: { 'gpt-5.6-sol': false } },
      codex_turn_tickets: [{ model: 'gpt-5.6-sol', ready: false, harvest_enabled: false, turn_state_present: false, cookie_present: false }]
    } as Account
    getById.mockResolvedValue(noTicket)
    diagnose.mockResolvedValue({ items: [{ model: 'gpt-5.6-sol', status: 'normal' }] })
    const wrapper = mountDashboard(true, noTicket)
    await flushPromises()
    configure(wrapper, ['gpt-5.6-sol'])
    await nextTick()
    const start = wrapper.findAll('button').find(button => button.text().startsWith('开始检测'))!
    expect(start.attributes('disabled')).toBeUndefined()
    await start.trigger('click')
    await flushPromises()
    expect(diagnose).toHaveBeenCalledWith(19, 4, ['gpt-5.6-sol'], expect.any(AbortSignal))
    expect(harvest).not.toHaveBeenCalled()
    expect(wrapper.find('[data-model="gpt-5.6-sol"]').attributes('data-status')).toBe('normal')
    wrapper.unmount()
  })

  it('sends one model per diagnostic request and retains completed results', async () => {
    const wrapper = mountDashboard(true)
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
    const wrapper = mountDashboard(true)
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
    expect(document.activeElement?.textContent).toBe('检测结果')
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
    const wrapper = mountDashboard(true)
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
    const wrapper = mountDashboard(true)
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
    const wrapper = mountDashboard(true)
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
    const wrapper = mountDashboard(true)
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

  it('keeps the latest results across tabs and setup edits without starting another billable request', async () => {
    diagnose.mockImplementation((_accountID: number, _keyID: number, requested: string[]) => Promise.resolve({ items: [{ model: requested[0], status: 'normal' }] }))
    const wrapper = mountDashboard(true)
    await flushPromises()
    const view = configure(wrapper, ['gpt-5.6-sol'])
    await view.runDiagnostic()
    await flushPromises()
    const tab = (label: string) => wrapper.findAll('nav button').find(button => button.text() === label)!
    await tab('打票流水').trigger('click')
    await tab('降智检测').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-model="gpt-5.6-sol"]').attributes('data-status')).toBe('normal')
    await view.showDiagnosticStage('setup')
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
})
