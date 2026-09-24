import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import CodexTicketDashboard from '../CodexTicketDashboard.vue'
import type { Account } from '@/types'
import type { TicketParticipation } from '@/api/admin/codexTickets'

const { getById, saveParticipation, fingerprint, events, harvest } = vi.hoisted(() => ({
  getById: vi.fn(), saveParticipation: vi.fn(), fingerprint: vi.fn(), events: vi.fn(), harvest: vi.fn()
}))
vi.mock('@/api/admin', () => ({ adminAPI: { accounts: { getById } } }))
vi.mock('@/api/admin/codexTickets', () => ({
  saveParticipation, fingerprint, events, harvest, refreshFingerprint: vi.fn(), ownKeys: vi.fn(), invalidation: vi.fn(), diagnose: vi.fn()
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ locale: ref('zh-CN'), t: (key: string) => key }) }))

const models = ['gpt-6-astra', 'gpt-5.6-sol']
function account(id = 19, extra: Record<string, unknown> = {}, runtime = false): Account {
  return { id, name: `Account ${id}`, platform: 'openai', type: 'oauth', extra,
    codex_turn_tickets: models.map(model => ({ model, ready: false, harvest_enabled: runtime, harvest_paused: false }))
  } as Account
}
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: Error) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
const mounted: ReturnType<typeof mountDashboard>[] = []
function mountDashboard(value: Account) {
  const wrapper = mount(CodexTicketDashboard, {
    props: { show: true, account: value },
    global: { stubs: { Teleport: true, Select: true, DateRangePicker: true, CodexDiagnosticModelPicker: true } }
  })
  mounted.push(wrapper)
  return wrapper
}
function switches(wrapper: ReturnType<typeof mountDashboard>) { return wrapper.findAll('button[role="switch"]') }
function values(wrapper: ReturnType<typeof mountDashboard>) { return switches(wrapper).map(item => item.attributes('aria-checked')) }
function refresh(wrapper: ReturnType<typeof mountDashboard>) { return wrapper.findAll('button').find(button => button.text() === '刷新')! }
function harvesting(wrapper: ReturnType<typeof mountDashboard>) { return wrapper.findAll('button').filter(button => button.text() === '手动打票') }

describe('Ticket participation', () => {
  let stored: Account
  beforeEach(() => {
    vi.resetAllMocks()
    stored = account()
    getById.mockImplementation(async () => structuredClone(stored))
    fingerprint.mockResolvedValue({ commit: 'abc123', models })
    events.mockResolvedValue({ items: [], total: 0 })
    saveParticipation.mockImplementation(async (_id: number, value: TicketParticipation) => {
      stored = { ...stored, extra: { ...stored.extra, codex_ticket_harvest_enabled: value.enabled, codex_ticket_harvest_models: { ...value.models } },
        codex_turn_tickets: stored.codex_turn_tickets?.map(status => ({ ...status, harvest_enabled: value.enabled && value.models[status.model] !== false })) }
      return value
    })
  })
  afterEach(() => { for (const wrapper of mounted.splice(0)) wrapper.unmount() })

  it('defaults to enabled from stored policy even when runtime harvesting is disabled', async () => {
    const wrapper = mountDashboard(stored)
    expect(switches(wrapper).every(item => item.attributes('disabled') !== undefined)).toBe(true)
    await flushPromises()
    expect(values(wrapper)).toEqual(['true', 'true', 'true'])
    expect(switches(wrapper).every(item => item.attributes('disabled') === undefined)).toBe(true)
    expect(harvesting(wrapper).every(item => item.attributes('disabled') !== undefined)).toBe(true)
    await switches(wrapper)[1].trigger('click')
    await flushPromises()
    expect(saveParticipation).toHaveBeenCalledWith(19, { enabled: true, models: { 'gpt-6-astra': false } })
  })

  it('restores all disabled choices, edits models while the account is off, and preserves unrelated settings', async () => {
    stored = account(19, { codex_ticket_harvest_enabled: false, codex_ticket_harvest_models: { 'gpt-6-astra': false, 'gpt-5.6-sol': false, 'gpt-5.6-terra': true }, codex_allow_without_ticket: false })
    const wrapper = mountDashboard(stored)
    await flushPromises()
    expect(values(wrapper)).toEqual(['false', 'false', 'false'])
    expect(wrapper.findAll('article')).toHaveLength(2)
    await switches(wrapper)[1].trigger('click')
    await flushPromises()
    expect(saveParticipation).toHaveBeenLastCalledWith(19, { enabled: false, models: { 'gpt-6-astra': true, 'gpt-5.6-sol': false, 'gpt-5.6-terra': true } })
    await switches(wrapper)[0].trigger('click')
    await flushPromises()
    expect(values(wrapper)).toEqual(['true', 'true', 'false'])
    expect(harvesting(wrapper)[0].attributes('disabled')).toBeUndefined()
    expect(harvesting(wrapper)[1].attributes('disabled')).toBeDefined()
    const updated = wrapper.emitted('updated')!.at(-1)![0] as Account
    expect(updated.extra?.codex_allow_without_ticket).toBe(false)
    expect(updated.codex_turn_tickets?.[0].harvest_enabled).toBe(true)
    await switches(wrapper)[0].trigger('click')
    await flushPromises()
    await switches(wrapper)[0].trigger('click')
    await flushPromises()
    expect(values(wrapper)).toEqual(['true', 'true', 'false'])
  })

  it('only explicit false disables a saved choice', async () => {
    stored = account(19, { codex_ticket_harvest_enabled: 0, codex_ticket_harvest_models: { 'gpt-6-astra': 'false', 'gpt-5.6-sol': false } })
    const wrapper = mountDashboard(stored)
    await flushPromises()
    expect(values(wrapper)).toEqual(['true', 'true', 'false'])
  })

  it('rolls back a failed save and prevents duplicate submissions', async () => {
    const saving = deferred<TicketParticipation>()
    saveParticipation.mockReturnValueOnce(saving.promise)
    const wrapper = mountDashboard(stored)
    await flushPromises()
    await switches(wrapper)[0].trigger('click')
    expect(values(wrapper)[0]).toBe('false')
    expect(switches(wrapper).every(item => item.attributes('disabled') !== undefined)).toBe(true)
    await switches(wrapper)[0].trigger('click')
    await switches(wrapper)[1].trigger('click')
    expect(saveParticipation).toHaveBeenCalledTimes(1)
    saving.reject(new Error('save rejected'))
    await flushPromises()
    expect(values(wrapper)).toEqual(['true', 'true', 'true'])
    expect(wrapper.find('[role="alert"]').exists()).toBe(true)
    expect(switches(wrapper)[0].attributes('disabled')).toBeUndefined()
  })

  it('retains a successful save if details refresh fails and allows retrying refresh', async () => {
    const wrapper = mountDashboard(stored)
    await flushPromises()
    getById.mockRejectedValueOnce(new Error('refresh unavailable'))
    await switches(wrapper)[0].trigger('click')
    await flushPromises()
    expect(values(wrapper)).toEqual(['false', 'true', 'true'])
    expect(wrapper.text()).toContain('参与设置已保存，但账号详情刷新失败')
    const updated = wrapper.emitted('updated')!.at(-1)![0] as Account
    expect(updated.extra?.codex_ticket_harvest_enabled).toBe(false)
    await refresh(wrapper).trigger('click')
    await flushPromises()
    expect(values(wrapper)[0]).toBe('false')
    expect(wrapper.text()).not.toContain('账号详情刷新失败')
  })

  it('requires a successful first detail load, independently of fingerprints and history', async () => {
    getById.mockRejectedValueOnce(new Error('account unavailable'))
    fingerprint.mockRejectedValue(new Error('fingerprints unavailable'))
    events.mockRejectedValue(new Error('history unavailable'))
    const wrapper = mountDashboard(stored)
    await flushPromises()
    expect(switches(wrapper).every(item => item.attributes('disabled') !== undefined)).toBe(true)
    await refresh(wrapper).trigger('click')
    await flushPromises()
    expect(switches(wrapper).every(item => item.attributes('disabled') === undefined)).toBe(true)
    const attemptsTab = wrapper.findAll('nav button').find(item => item.text() === '打票流水')!
    await attemptsTab.trigger('click')
    await flushPromises()
    await wrapper.findAll('nav button')[0].trigger('click')
    await switches(wrapper)[0].trigger('click')
    await flushPromises()
    expect(saveParticipation).toHaveBeenCalledTimes(1)
    expect(values(wrapper)[0]).toBe('false')
  })

  it('ignores a previous account detail response', async () => {
    const old = deferred<Account>()
    getById.mockReturnValueOnce(old.promise)
    const wrapper = mountDashboard(stored)
    stored = account(20, { codex_ticket_harvest_enabled: false })
    await wrapper.setProps({ account: stored })
    await flushPromises()
    old.resolve(account())
    await flushPromises()
    expect(values(wrapper)[0]).toBe('false')
    expect((wrapper.emitted('updated') ?? []).every(([item]) => (item as Account).id === 20)).toBe(true)
  })

  it.each(['switch', 'close'] as const)('ignores an old save after %s', async action => {
    const old = deferred<TicketParticipation>()
    saveParticipation.mockReturnValueOnce(old.promise)
    const wrapper = mountDashboard(stored)
    await flushPromises()
    await switches(wrapper)[0].trigger('click')
    if (action === 'switch') {
      stored = account(20)
      await wrapper.setProps({ account: stored })
    } else {
      await wrapper.setProps({ show: false })
      await wrapper.setProps({ show: true })
    }
    await flushPromises()
    const updates = wrapper.emitted('updated')!.length
    const reads = getById.mock.calls.length
    old.resolve({ enabled: false, models: {} })
    await flushPromises()
    expect(values(wrapper)[0]).toBe('true')
    expect(wrapper.emitted('updated')).toHaveLength(updates)
    expect(getById).toHaveBeenCalledTimes(reads)
  })
})
