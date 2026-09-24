import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'

import AccountsView from '../AccountsView.vue'

const { listAccounts, listWithEtag, getSettings, getById } = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  listWithEtag: vi.fn(),
  getSettings: vi.fn(),
  getById: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    settings: { getSettings },
    accounts: {
      list: listAccounts,
      listWithEtag,
      getById,
      getBatchTodayStats: vi.fn(async () => ({ stats: {} })),
      getUpstreamBillingProbeSettings: vi.fn(async () => ({ enabled: true, interval_minutes: 30 })),
      delete: vi.fn(),
      batchClearError: vi.fn(),
      batchRefresh: vi.fn(),
      toggleSchedulable: vi.fn()
    },
    proxies: { getAll: vi.fn(async () => []) },
    groups: { getAll: vi.fn(async () => []) }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn(), showInfo: vi.fn() })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ token: 'test-token', isSimpleMode: false })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

const DataTableStub = {
  props: ['columns', 'data'],
  emits: ['sort'],
  template: `
    <div data-test="data-table">
      <span v-for="column in columns" :key="column.key" :data-column="column.key">
        {{ column.sortable ? 'sortable' : 'fixed' }}
      </span>
      <div v-for="row in data" :key="row.id" :data-account-name="row.name">
        <slot v-if="columns.some(column => column.key === 'codex_ticket')" name="cell-codex_ticket" :row="row" />
      </div>
      <button data-test="sort-priority" @click="$emit('sort', 'priority', 'desc')" />
    </div>
  `
}

function mountView() {
  return mount(AccountsView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: {
          template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>'
        },
        DataTable: DataTableStub,
        AccountTableActions: {
          emits: ['refresh'],
          template: `<div><button data-test="refresh" @click="$emit('refresh')" /><slot name="after" /></div>`
        },
        AccountTableFilters: true,
        AccountBulkActionsBar: true,
        Pagination: true,
        ConfirmDialog: true,
        AccountActionMenu: true,
        CodexTicketDashboard: true,
        CodexDiagnosticModal: true,
        ImportDataModal: true,
        ReAuthAccountModal: true,
        AccountTestModal: true,
        AccountStatsModal: true,
        ScheduledTestsPanel: true,
        SyncFromCrsModal: true,
        TempUnschedStatusModal: true,
        ErrorPassthroughRulesModal: true,
        TLSFingerprintProfilesModal: true,
        CreateAccountModal: true,
        EditAccountModal: true,
        BulkEditAccountModal: true,
        PlatformTypeBadge: true,
        AccountCapacityCell: true,
        AccountStatusIndicator: true,
        AccountTodayStatsCell: true,
        AccountGroupsCell: true,
        AccountUsageCell: true,
        HelpTooltip: true,
        Icon: true,
        Teleport: true
      }
    }
  })
}

enableAutoUnmount(afterEach)
afterEach(() => {
  vi.useRealTimers()
  vi.restoreAllMocks()
})

beforeEach(() => {
  localStorage.clear()
  getSettings.mockReset().mockResolvedValue({ openai_codex_ticket_enabled: false })
  listWithEtag.mockReset().mockResolvedValue({ notModified: true, etag: 'accounts-etag', data: null })
  getById.mockReset()
})

describe('admin AccountsView priority column preferences', () => {
  beforeEach(() => {
    localStorage.clear()
    listAccounts.mockReset().mockResolvedValue({
      items: [],
      total: 0,
      page: 1,
      page_size: 20,
      pages: 0
    })
  })

  it('shows priority as a sortable column for fresh preferences', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-column="priority"]').text()).toBe('sortable')

    await wrapper.get('[data-test="sort-priority"]').trigger('click')
    await flushPromises()

    expect(listAccounts).toHaveBeenLastCalledWith(
      1,
      20,
      expect.objectContaining({ sort_by: 'priority', sort_order: 'desc' }),
      expect.objectContaining({ signal: expect.any(AbortSignal) })
    )
  })

  it('preserves an existing preference that explicitly hides priority', async () => {
    localStorage.setItem('account-hidden-columns', JSON.stringify(['priority', 'today_stats']))
    localStorage.setItem('account-hidden-columns-version', 'scheduler-score-hidden-by-default')

    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.find('[data-column="priority"]').exists()).toBe(false)
    expect(JSON.parse(localStorage.getItem('account-hidden-columns') || '[]')).toEqual([
      'priority',
      'today_stats'
    ])
  })

  it('keeps priority visible while migrating older saved preferences', async () => {
    localStorage.setItem('account-hidden-columns', JSON.stringify(['today_stats']))

    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-column="priority"]').text()).toBe('sortable')
    expect(JSON.parse(localStorage.getItem('account-hidden-columns') || '[]')).toEqual(
      expect.arrayContaining(['today_stats', 'scheduler_score'])
    )
    expect(JSON.parse(localStorage.getItem('account-hidden-columns') || '[]')).not.toContain('priority')
  })
})

function deferredSettings() {
  let resolve!: (value: { openai_codex_ticket_enabled: boolean }) => void
  const promise = new Promise<{ openai_codex_ticket_enabled: boolean }>(done => { resolve = done })
  return { promise, resolve }
}

async function refresh(wrapper: ReturnType<typeof mountView>) {
  await wrapper.get('[data-test="refresh"]').trigger('click')
  await flushPromises()
}

async function toggleColumnMenu(wrapper: ReturnType<typeof mountView>) {
  await wrapper.get('button[title="admin.accounts.moreActions"]').trigger('click')
}

function ticketMenuOption(wrapper: ReturnType<typeof mountView>) {
  return wrapper.findAll('button').find(button => button.text() === 'admin.accounts.columns.codexTicket')
}

describe('admin AccountsView ticket column', () => {
  beforeEach(() => {
    getSettings.mockResolvedValue({ openai_codex_ticket_enabled: true })
    listAccounts.mockReset().mockResolvedValue({
      items: [
        {
          id: 1, name: 'Codex', platform: 'openai', type: 'oauth',
          extra: { codex_ticket_harvest_enabled: false },
          codex_turn_tickets: [
            { model: 'gpt-5.6-sol', ready: true, harvest_enabled: false },
            { model: 'gpt-6-astra', ready: false, harvest_enabled: false }
          ]
        },
        { id: 2, name: 'Other', platform: 'anthropic', type: 'api-key' }
      ],
      total: 2, page: 1, page_size: 20, pages: 1
    })
  })

  it('shows counts and the column option when globally enabled, without loading row details', async () => {
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.get('[data-column="codex_ticket"]').text()).toBe('fixed')
    expect(wrapper.get('button[aria-label="admin.accounts.openai.codexTicketSummary"]').text()).toBe('1 / 2')
    expect(wrapper.get('[data-test="data-table"]').text()).toContain('—')
    await toggleColumnMenu(wrapper)
    expect(ticketMenuOption(wrapper)).toBeDefined()
    expect(listAccounts).toHaveBeenCalledTimes(1)
    expect(getSettings).toHaveBeenCalledTimes(1)
    expect(getById).not.toHaveBeenCalled()
  })

  it('hides the header, cells and menu option when globally disabled, even with saved tickets', async () => {
    getSettings.mockResolvedValue({ openai_codex_ticket_enabled: false })
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.find('[data-column="codex_ticket"]').exists()).toBe(false)
    expect(wrapper.find('button[aria-label="admin.accounts.openai.codexTicketSummary"]').exists()).toBe(false)
    expect(wrapper.find('[data-account-name="Codex"]').exists()).toBe(true)
    await toggleColumnMenu(wrapper)
    expect(ticketMenuOption(wrapper)).toBeUndefined()
  })

  it('loads accounts independently while settings are pending and initially hides the column', async () => {
    const pending = deferredSettings()
    getSettings.mockReturnValueOnce(pending.promise)
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.find('[data-account-name="Codex"]').exists()).toBe(true)
    expect(wrapper.find('[data-column="codex_ticket"]').exists()).toBe(false)
    await toggleColumnMenu(wrapper)
    expect(ticketMenuOption(wrapper)).toBeUndefined()

    pending.resolve({ openai_codex_ticket_enabled: true })
    await flushPromises()
    expect(wrapper.find('[data-column="codex_ticket"]').exists()).toBe(true)
    expect(ticketMenuOption(wrapper)).toBeDefined()
  })

  it('stays hidden after the initial settings failure and recovers on manual refresh', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    getSettings.mockRejectedValueOnce(new Error('settings unavailable'))
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.find('[data-account-name="Codex"]').exists()).toBe(true)
    expect(wrapper.find('[data-column="codex_ticket"]').exists()).toBe(false)
    await refresh(wrapper)
    expect(wrapper.find('[data-column="codex_ticket"]').exists()).toBe(true)
    expect(listAccounts).toHaveBeenCalledTimes(2)
  })

  it.each([true, false])('retains the last confirmed setting (%s) when refresh fails', async enabled => {
    vi.spyOn(console, 'error').mockImplementation(() => {})
    getSettings.mockResolvedValueOnce({ openai_codex_ticket_enabled: enabled })
    const wrapper = mountView()
    await flushPromises()
    getSettings.mockRejectedValueOnce(new Error('settings unavailable'))
    await refresh(wrapper)
    expect(wrapper.find('[data-column="codex_ticket"]').exists()).toBe(enabled)
    expect(wrapper.find('[data-account-name="Codex"]').exists()).toBe(true)
    await toggleColumnMenu(wrapper)
    expect(Boolean(ticketMenuOption(wrapper))).toBe(enabled)
  })

  it.each([true, false])('preserves the saved column preference (hidden=%s) across global off/on changes', async hidden => {
    const saved = JSON.stringify(hidden ? ['codex_ticket', 'today_stats'] : ['today_stats'])
    localStorage.setItem('account-hidden-columns', saved)
    localStorage.setItem('account-hidden-columns-version', 'scheduler-score-hidden-by-default')
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.find('[data-column="codex_ticket"]').exists()).toBe(!hidden)
    await toggleColumnMenu(wrapper)
    expect(ticketMenuOption(wrapper)).toBeDefined()

    getSettings.mockResolvedValueOnce({ openai_codex_ticket_enabled: false })
    await refresh(wrapper)
    expect(wrapper.find('[data-column="codex_ticket"]').exists()).toBe(false)
    expect(ticketMenuOption(wrapper)).toBeUndefined()
    await refresh(wrapper)
    expect(wrapper.find('[data-column="codex_ticket"]').exists()).toBe(!hidden)
    expect(ticketMenuOption(wrapper)).toBeDefined()
    expect(localStorage.getItem('account-hidden-columns')).toBe(saved)
  })

  it('keeps a user-hidden column hidden after disabling and enabling globally', async () => {
    const wrapper = mountView()
    await flushPromises()
    await toggleColumnMenu(wrapper)
    await ticketMenuOption(wrapper)!.trigger('click')
    expect(wrapper.find('[data-column="codex_ticket"]').exists()).toBe(false)
    const saved = localStorage.getItem('account-hidden-columns')
    expect(JSON.parse(saved!)).toContain('codex_ticket')

    getSettings.mockResolvedValueOnce({ openai_codex_ticket_enabled: false })
    await refresh(wrapper)
    await refresh(wrapper)
    expect(wrapper.find('[data-column="codex_ticket"]').exists()).toBe(false)
    expect(ticketMenuOption(wrapper)).toBeDefined()
    expect(localStorage.getItem('account-hidden-columns')).toBe(saved)
  })

  it('refreshes settings automatically even when the account list returns 304', async () => {
    vi.useFakeTimers()
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
    localStorage.setItem('account-auto-refresh', JSON.stringify({ enabled: true, interval_seconds: 5 }))
    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.find('[data-column="codex_ticket"]').exists()).toBe(true)

    getSettings.mockResolvedValueOnce({ openai_codex_ticket_enabled: false })
    await vi.advanceTimersByTimeAsync(6000)
    await flushPromises()
    expect(listWithEtag).toHaveBeenCalledTimes(1)
    expect(getSettings).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[data-column="codex_ticket"]').exists()).toBe(false)
    expect(wrapper.find('[data-account-name="Codex"]').exists()).toBe(true)
    await vi.advanceTimersByTimeAsync(6000)
    await flushPromises()
    expect(listWithEtag).toHaveBeenCalledTimes(2)
    expect(getSettings).toHaveBeenCalledTimes(3)
    expect(wrapper.find('[data-column="codex_ticket"]').exists()).toBe(true)
  })

  it.each([true, false])('ignores an older settings response after a newer response (%s)', async enabled => {
    const older = deferredSettings()
    getSettings.mockReturnValueOnce(older.promise)
    const wrapper = mountView()
    await flushPromises()
    getSettings.mockResolvedValueOnce({ openai_codex_ticket_enabled: enabled })
    await refresh(wrapper)
    expect(wrapper.find('[data-column="codex_ticket"]').exists()).toBe(enabled)

    older.resolve({ openai_codex_ticket_enabled: !enabled })
    await flushPromises()
    expect(wrapper.find('[data-column="codex_ticket"]').exists()).toBe(enabled)
  })

  it('isolates a pending response from a closed page', async () => {
    const older = deferredSettings()
    getSettings.mockReturnValueOnce(older.promise)
    const closed = mountView()
    await flushPromises()
    closed.unmount()
    getSettings.mockResolvedValueOnce({ openai_codex_ticket_enabled: false })
    const current = mountView()
    await flushPromises()
    older.resolve({ openai_codex_ticket_enabled: true })
    await flushPromises()
    expect(current.find('[data-column="codex_ticket"]').exists()).toBe(false)
  })
})


describe('independent model diagnostic entry', () => {
  it('opens the diagnostic modal with harvesting disabled and keeps the ticket center separate', async () => {
    listAccounts.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20, pages: 0 })
    const wrapper = mountView()
    await flushPromises()
    const selected = { id: 19, name: 'Diagnostic account', platform: 'openai', type: 'oauth', extra: { codex_ticket_harvest_enabled: false } }
    const diagnostic = wrapper.getComponent({ name: 'CodexDiagnosticModal' })
    const tickets = wrapper.getComponent({ name: 'CodexTicketDashboard' })
    wrapper.getComponent({ name: 'AccountActionMenu' }).vm.$emit('codex-diagnostic', selected)
    await flushPromises()
    expect(diagnostic.props('show')).toBe(true)
    expect(diagnostic.props('account')).toEqual(selected)
    expect(tickets.props('show')).toBe(false)
    diagnostic.vm.$emit('close')
    await flushPromises()
    expect(diagnostic.props('show')).toBe(false)
    expect(tickets.props('show')).toBe(false)
  })
})
