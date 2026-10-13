import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import CodexTicketDashboard from '../CodexTicketDashboard.vue'
import type { Account } from '@/types'

const { events, fingerprint, ownKeys, invalidation, diagnose, getById, harvest, language } = vi.hoisted(() => ({
  events: vi.fn(), fingerprint: vi.fn(), ownKeys: vi.fn(), invalidation: vi.fn(), diagnose: vi.fn(), getById: vi.fn(), harvest: vi.fn(), language: { value: 'zh-CN' }
}))
vi.mock('@/api/admin/codexTickets', () => ({
  events, fingerprint, ownKeys, invalidation, diagnose, harvest, refreshFingerprint: vi.fn()
}))
vi.mock('@/api/admin', () => ({ adminAPI: { accounts: { getById } } }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ locale: ref(language.value), t: (key: string) => key }) }))

const account = {
  id: 19, name: 'GPT Account', platform: 'openai', type: 'oauth',
  codex_ticket: { model: 'gpt-6-sol', ready: true, remaining_seconds: 0, blocked: false, source_account_id: 7, length: 292, turn_state_present: true, cookie_present: true, harvest_enabled: true }
} as Account
const attempt = { id: 1, model: 'gpt-5.6-sol', occurred_at: '2026-09-22T08:00:00Z', kind: 'miss', reason_code: 'fingerprint_mismatch' }
const expired = { id: 2, model: 'gpt-5.6-sol', occurred_at: '2026-09-22T09:00:00Z', kind: 'invalidation', reason_code: 'upstream_new_turn_state' }

function mountDashboard(value: Account = account) {
  return mount(CodexTicketDashboard, {
    props: { show: true, account: value },
    attachTo: document.body,
    global: {
      stubs: {
        Teleport: true, Select: true, DateRangePicker: true, Icon: true
      }
    }
  })
}

describe('Codex ticket dashboard', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    language.value = 'zh-CN'
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
    expect(wrapper.text()).toContain('全站共享票')
    expect(wrapper.text()).toContain('gpt-6-sol')
    expect(wrapper.text()).toContain('#7')
    expect(wrapper.text()).not.toContain('fingerprint_mismatch')
    expect(events).not.toHaveBeenCalled()
    expect(ownKeys).not.toHaveBeenCalled()
    expect(wrapper.findAll('nav button').map(button => button.text())).toEqual(['当前票据', '打票流水', '票据过期历史'])

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

  it('labels superseded successful harvests that have no ticket generation', async () => {
    const superseded = { id: 3, model: 'gpt-5.6-sol', occurred_at: '2026-09-22T10:00:00Z', kind: 'success', reason_code: 'superseded', verification_method: 'fingerprint' }
    events.mockResolvedValue({ items: [superseded], total: 1 })
    const wrapper = mountDashboard()
    await flushPromises()
    await wrapper.findAll('nav button').find(button => button.text() === '打票流水')!.trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('打票成功，本轮已有票未采用')
    expect(wrapper.text()).not.toContain('superseded')
    expect(wrapper.text()).not.toContain('undefined')

    await wrapper.find('section[aria-label="打票流水"] button').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('打票成功，本轮已有票未采用 · —')
    expect(wrapper.text()).not.toContain('undefined')
    wrapper.unmount()
  })

  it.each([
    ['zh-CN', 'upstream_turn_state_and_routing_cookie_changed', 'turn-state 变化且 __oailb 或 __cflb 变化'],
    ['zh-CN', 'upstream_turn_state_and_oailb_changed', '旧规则：turn-state 与 __oailb 同时变化'],
    ['zh-CN', 'upstream_new_turn_state', '旧规则：turn-state 变化'],
    ['en', 'upstream_turn_state_and_routing_cookie_changed', 'Turn-state and either __oailb or __cflb changed'],
    ['en', 'upstream_turn_state_and_oailb_changed', 'Legacy rule: both turn-state and __oailb changed'],
    ['en', 'upstream_new_turn_state', 'Legacy rule: turn-state changed']
  ])('displays the original reason for %s / %s', async (locale, reason, label) => {
    language.value = locale
    events.mockResolvedValue({ items: [{ ...expired, reason_code: reason }], total: 1 })
    const wrapper = mountDashboard()
    await flushPromises()
    expect(wrapper.text()).toContain('__oailb + __cflb')
    await wrapper.findAll('nav button')[2].trigger('click')
    await flushPromises()
    expect(wrapper.find('section button').text()).toContain(label)
    expect(wrapper.find('section button').text()).not.toContain(reason)
    wrapper.unmount()
  })

})
