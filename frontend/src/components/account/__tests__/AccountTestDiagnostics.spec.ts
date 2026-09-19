import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import AdminAccountTestModal from '@/components/admin/account/AccountTestModal.vue'
import AccountTestModal from '../AccountTestModal.vue'
import CodexTicketPolicyField from '../CodexTicketPolicyField.vue'

const { getSettings, copyToClipboard } = vi.hoisted(() => ({
  getSettings: vi.fn(),
  copyToClipboard: vi.fn()
}))
vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: { getAvailableModels: vi.fn().mockResolvedValue([{ id: 'gpt-5.5', display_name: 'GPT' }]) },
    settings: { getSettings }
  }
}))
vi.mock('@/composables/useClipboard', () => ({ useClipboard: () => ({ copyToClipboard }) }))
vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => params?.policy ? `${key} ${params.policy}` : key })
}))

const stubs = {
  BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
  Select: {
    props: ['modelValue', 'options'],
    emits: ['update:modelValue'],
    template: '<select :value="modelValue" @change="$emit(\'update:modelValue\', $event.target.value)"><option v-for="option in options" :value="option.value">{{ option.label }}</option></select>'
  },
  TextArea: {
    props: ['modelValue'], emits: ['update:modelValue'],
    template: '<textarea :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" />'
  },
  Icon: true
}

function response(events: unknown[]) {
  const bytes = new TextEncoder().encode(events.map(event => `data: ${JSON.stringify(event)}\n\n`).join(''))
  return {
    ok: true,
    body: new ReadableStream({
      start(controller) {
        // Split in the middle of an event to exercise stream buffering.
        controller.enqueue(bytes.slice(0, 45))
        controller.enqueue(bytes.slice(45))
        controller.close()
      }
    })
  }
}

beforeEach(() => {
  copyToClipboard.mockReset()
  getSettings.mockResolvedValue({ openai_codex_ticket_enabled: true, openai_codex_ticket_allow_without_ticket: false })
  localStorage.setItem('auth_token', 'test')
})

describe.each([
  ['admin modal', AdminAccountTestModal],
  ['account modal', AccountTestModal]
] as const)('%s diagnostics', (_name, component) => {
  it('sends text prompts, preserves all responses on failure, copies full values and resets on retry', async () => {
    const long = 'x'.repeat(10000)
    vi.stubGlobal('fetch', vi.fn()
      .mockResolvedValueOnce(response([
        { type: 'test_start', model: 'gpt-5.5' },
        { type: 'upstream_response', data: { sequence: 1, method: 'POST', stage: '/responses', status_code: 429, headers: { 'Set-Cookie': ['a=1', 'b=2'], 'X-Long': [long] } } },
        { type: 'upstream_response', data: { sequence: 2, method: 'POST', stage: '/responses', status_code: 502, headers: { 'X-Request-Id': ['second'] } } },
        { type: 'error', error: 'upstream failed' }
      ]))
      .mockResolvedValueOnce(response([{ type: 'error', error: 'network failed' }])))
    const wrapper = mount(component, {
      props: { show: false, account: { id: 1, name: 'OpenAI', platform: 'openai', type: 'oauth', status: 'active' } as any },
      global: { stubs }
    })
    await wrapper.setProps({ show: true })
    await flushPromises()
    await wrapper.get('textarea').setValue('自定义测试\nsecond line')
    await wrapper.findAll('button').find(button => button.text().includes('admin.accounts.startTest'))!.trigger('click')
    await flushPromises()
    expect(JSON.parse(vi.mocked(fetch).mock.calls[0][1]!.body as string).prompt).toBe('自定义测试\nsecond line')
    const panel = wrapper.get('[data-testid="upstream-response-headers"]')
    expect(panel.findAll('pre')).toHaveLength(2)
    expect(panel.text()).toContain('Set-Cookie: a=1')
    expect(panel.text()).toContain('Set-Cookie: b=2')
    expect(panel.text()).toContain(long)
    expect(panel.findAll('pre')[0].text()).toContain('429')
    expect(panel.findAll('pre')[1].text()).toContain('502')
    await panel.get('button').trigger('click')
    expect(copyToClipboard).toHaveBeenCalledWith(expect.stringContaining(long))
    await wrapper.findAll('button').find(button => button.text().includes('admin.accounts.retry'))!.trigger('click')
    await flushPromises()
    expect(panel.findAll('pre')).toHaveLength(0)
    expect(panel.text()).toContain('admin.accounts.upstreamHeadersEmpty')
    wrapper.unmount()
    vi.unstubAllGlobals()
  })
})

describe('ticket policy field', () => {
  it.each([false, true])('shows inherited global policy %s and explicit overrides', async allow => {
    getSettings.mockResolvedValue({ openai_codex_ticket_enabled: true, openai_codex_ticket_allow_without_ticket: allow })
    const wrapper = mount(CodexTicketPolicyField, { props: { modelValue: 'inherit' }, global: { stubs } })
    await flushPromises()
    expect(wrapper.get('[role="status"]').text()).toContain(allow ? 'ticketAllow' : 'ticketDeny')
    await wrapper.setProps({ modelValue: allow ? 'deny' : 'allow' })
    expect(wrapper.get('[role="status"]').text()).toContain(allow ? 'ticketDeny' : 'ticketAllow')
    await wrapper.get('select').setValue('inherit')
    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual(['inherit'])
  })

  it('shows that the global master switch disables enforcement', async () => {
    getSettings.mockResolvedValue({ openai_codex_ticket_enabled: false, openai_codex_ticket_allow_without_ticket: false })
    const wrapper = mount(CodexTicketPolicyField, { props: { modelValue: 'deny' }, global: { stubs } })
    await flushPromises()
    expect(wrapper.get('[role="status"]').text()).toContain('ticketDisabled')
  })
})
