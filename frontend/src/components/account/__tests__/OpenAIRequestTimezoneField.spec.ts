import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import OpenAIRequestTimezoneField from '../OpenAIRequestTimezoneField.vue'

const { getOpenAIRequestTimezones, detectOpenAIRequestTimezone } = vi.hoisted(() => ({
  getOpenAIRequestTimezones: vi.fn(),
  detectOpenAIRequestTimezone: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: { accounts: { getOpenAIRequestTimezones, detectOpenAIRequestTimezone } }
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => (params ? `${key}:${JSON.stringify(params)}` : key) }) }
})

const selectStub = {
  props: ['modelValue', 'options', 'loading'],
  template: '<div data-test="timezone-select">{{ modelValue }} / {{ options.length }}</div>'
}

const catalog = { default: 'Asia/Singapore', timezones: ['Asia/Singapore', 'Asia/Tokyo', 'Europe/London'] }

describe('OpenAIRequestTimezoneField', () => {
  it('uses the backend catalog and resets a retired timezone to Singapore', async () => {
    getOpenAIRequestTimezones.mockResolvedValue(catalog)
    const wrapper = mount(OpenAIRequestTimezoneField, {
      props: { modelValue: 'Europe/Oslo', mode: 'fixed' as const },
      global: { stubs: { Select: selectStub } }
    })
    await flushPromises()
    expect(wrapper.get('[data-test="timezone-select"]').text()).toBe('Europe/Oslo / 3')
    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual(['Asia/Singapore'])
    wrapper.unmount()
  })

  it('keeps a saved timezone when it remains in the catalog', async () => {
    getOpenAIRequestTimezones.mockResolvedValue(catalog)
    const wrapper = mount(OpenAIRequestTimezoneField, {
      props: { modelValue: 'Asia/Tokyo', mode: 'fixed' as const },
      global: { stubs: { Select: selectStub } }
    })
    await flushPromises()
    expect(wrapper.get('[data-test="timezone-select"]').text()).toBe('Asia/Tokyo / 3')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    wrapper.unmount()
  })

  it('hides the fixed selector when following the exit IP and never resets the detected timezone', async () => {
    getOpenAIRequestTimezones.mockResolvedValue(catalog)
    const wrapper = mount(OpenAIRequestTimezoneField, {
      props: { modelValue: 'Europe/Oslo', mode: 'follow_proxy' as const },
      global: { stubs: { Select: selectStub } }
    })
    await flushPromises()
    expect(wrapper.find('[data-test="timezone-select"]').exists()).toBe(false)
    expect((wrapper.get('[data-testid="openai-request-timezone-mode-follow_proxy"]').element as HTMLInputElement).checked).toBe(true)
    expect(wrapper.text()).toContain('admin.accounts.openai.requestTimezoneDetectAfterSave')
    expect(wrapper.find('[data-testid="openai-request-timezone-detect"]').exists()).toBe(false)
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    await wrapper.get('[data-testid="openai-request-timezone-mode-fixed"]').trigger('change')
    expect(wrapper.emitted('update:mode')?.[0]).toEqual(['fixed'])
    wrapper.unmount()
  })

  it('shows the last detection and re-detects on demand', async () => {
    getOpenAIRequestTimezones.mockResolvedValue(catalog)
    detectOpenAIRequestTimezone.mockResolvedValue({
      id: 7,
      extra: {
        openai_request_timezone: 'Asia/Tokyo',
        openai_request_timezone_detected: { ip: '203.0.113.7', country: '日本', city: '东京', timezone: 'Asia/Tokyo', detected_at: '2026-10-15T00:00:00Z' }
      }
    })
    const wrapper = mount(OpenAIRequestTimezoneField, {
      props: {
        modelValue: 'Europe/Berlin',
        mode: 'follow_proxy' as const,
        accountId: 7,
        detection: { error: 'dial timeout', detected_at: '2026-10-14T00:00:00Z' }
      },
      global: { stubs: { Select: selectStub } }
    })
    await flushPromises()
    expect(wrapper.get('[data-testid="openai-request-timezone-detect-error"]').text()).toContain('Europe/Berlin')
    await wrapper.get('[data-testid="openai-request-timezone-detect"]').trigger('click')
    await flushPromises()
    expect(detectOpenAIRequestTimezone).toHaveBeenCalledWith(7)
    expect(wrapper.get('[data-testid="openai-request-timezone-detected"]').text()).toContain('203.0.113.7')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual(['Asia/Tokyo'])
    wrapper.unmount()
  })
})
