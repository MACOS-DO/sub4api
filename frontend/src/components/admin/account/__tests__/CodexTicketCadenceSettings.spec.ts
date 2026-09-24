import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import CodexTicketCadenceSettings from '../CodexTicketCadenceSettings.vue'

const { cadence, saveCadence } = vi.hoisted(() => ({ cadence: vi.fn(), saveCadence: vi.fn() }))

vi.mock('@/api/admin/codexTickets', () => ({ cadence, saveCadence }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ locale: ref('zh-CN') }) }))

describe('ticket cadence unified save', () => {
  beforeEach(() => {
    cadence.mockResolvedValue({ retry_min_seconds: 10, retry_max_seconds: 30, refresh_seconds: 1800 })
    saveCadence.mockResolvedValue(undefined)
  })
  afterEach(() => vi.clearAllMocks())

  it('loads the 30-minute default and has no standalone save button', async () => {
    const wrapper = mount(CodexTicketCadenceSettings, { props: { saving: false } })
    await flushPromises()
    const inputs = wrapper.findAll('input')
    expect((inputs[2].element as HTMLInputElement).value).toBe('1800')
    expect(wrapper.find('button').exists()).toBe(false)
    expect(wrapper.vm.isDirty()).toBe(false)
    wrapper.unmount()
  })

  it('preserves an explicitly saved zero and saves only an edited cadence', async () => {
    cadence.mockResolvedValue({ retry_min_seconds: 10, retry_max_seconds: 30, refresh_seconds: 0 })
    const wrapper = mount(CodexTicketCadenceSettings, { props: { saving: false } })
    await flushPromises()
    expect((wrapper.findAll('input')[2].element as HTMLInputElement).value).toBe('0')
    await wrapper.vm.save()
    expect(saveCadence).not.toHaveBeenCalled()
    await wrapper.findAll('input')[2].setValue('1800')
    expect(wrapper.vm.isDirty()).toBe(true)
    wrapper.vm.validate()
    await wrapper.vm.save()
    expect(saveCadence).toHaveBeenCalledWith({ retry_min_seconds: 10, retry_max_seconds: 30, refresh_seconds: 1800 })
    expect(wrapper.vm.isDirty()).toBe(false)
    wrapper.unmount()
  })
})
