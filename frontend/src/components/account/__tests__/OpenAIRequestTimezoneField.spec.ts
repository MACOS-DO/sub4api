import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import OpenAIRequestTimezoneField from '../OpenAIRequestTimezoneField.vue'

const { getOpenAIRequestTimezones } = vi.hoisted(() => ({
  getOpenAIRequestTimezones: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: { accounts: { getOpenAIRequestTimezones } }
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
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
      props: { modelValue: 'Europe/Oslo' },
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
      props: { modelValue: 'Asia/Tokyo' },
      global: { stubs: { Select: selectStub } }
    })
    await flushPromises()
    expect(wrapper.get('[data-test="timezone-select"]').text()).toBe('Asia/Tokyo / 3')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
    wrapper.unmount()
  })
})
