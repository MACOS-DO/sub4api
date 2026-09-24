import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import CodexDiagnosticModelPicker from '../CodexDiagnosticModelPicker.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ locale: ref('zh-CN') }) }))

const models = ['gpt-5.6-sol', 'gpt-5.5', 'gpt-5.4']
const mountPicker = (selected: string[] = []) => mount(CodexDiagnosticModelPicker, {
  props: { models, ticketModels: ['gpt-5.6-sol'], modelValue: selected },
  global: { stubs: { Icon: true } }
})

describe('Codex diagnostic model picker', () => {
  it('groups ticket and direct models and selects without native checkboxes', async () => {
    const wrapper = mountPicker()
    expect(wrapper.text()).toContain('支持打票')
    expect(wrapper.text()).toContain('无需打票')
    expect(wrapper.find('input[type="checkbox"]').exists()).toBe(false)

    await wrapper.find('button[aria-pressed="false"]').trigger('click')
    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual([['gpt-5.6-sol']])
    await wrapper.setProps({ modelValue: ['gpt-5.6-sol'] })
    expect(wrapper.find('button[aria-pressed="true"]').exists()).toBe(true)
    await wrapper.find('input[type="search"]').setValue('5.5')
    expect(wrapper.findAll('button[aria-pressed]')).toHaveLength(1)
    expect(wrapper.text()).toContain('gpt-5.5')
    wrapper.unmount()
  })

  it('clears selection and prevents changes while a check runs', async () => {
    const wrapper = mountPicker(['gpt-5.5'])
    const clear = wrapper.findAll('button').find(button => button.text() === '清空选择')
    await clear?.trigger('click')
    expect(wrapper.emitted('update:modelValue')?.[0]).toEqual([[]])
    await wrapper.setProps({ disabled: true })
    await wrapper.find('button[aria-pressed]').trigger('click')
    expect(wrapper.emitted('update:modelValue')).toHaveLength(1)
    wrapper.unmount()
  })
})
