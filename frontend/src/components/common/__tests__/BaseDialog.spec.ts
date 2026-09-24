import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { h, nextTick } from 'vue'
import BaseDialog from '../BaseDialog.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

describe('BaseDialog', () => {
  afterEach(() => {
    document.body.innerHTML = ''
    document.body.classList.remove('modal-open')
  })

  it('resets body scroll position when reopened', async () => {
    const wrapper = mount(BaseDialog, {
      attachTo: document.body,
      props: { show: false, title: 'Details' },
      slots: { default: '<div style="height: 2000px">content</div>' },
      global: { stubs: { Icon: true } }
    })

    await wrapper.setProps({ show: true })
    await nextTick()
    const body = document.body.querySelector<HTMLElement>('.modal-body')
    expect(body).not.toBeNull()
    body!.scrollTop = 480

    await wrapper.setProps({ show: false })
    await wrapper.setProps({ show: true })
    await nextTick()

    expect(document.body.querySelector<HTMLElement>('.modal-body')?.scrollTop).toBe(0)
    wrapper.unmount()
  })
  it('keeps custom headers labelled and supports an independently scrolling body', () => {
    const wrapper = mount(BaseDialog, {
      props: { show: true, title: 'Diagnostic', contentClass: 'diagnostic-panel', bodyClass: 'diagnostic-body' },
      slots: { header: ({ titleId }: { titleId: string }) => h('h2', { id: titleId }, 'Custom diagnostic header') },
      global: { stubs: { Teleport: true, Icon: true } }
    })
    const labelledBy = wrapper.get('[role="dialog"]').attributes('aria-labelledby')
    expect(wrapper.get(`[id="${labelledBy}"]`).text()).toBe('Custom diagnostic header')
    expect(wrapper.get('.modal-content').classes()).toContain('diagnostic-panel')
    expect(wrapper.get('.modal-body').classes()).toContain('diagnostic-body')
    wrapper.unmount()
  })

})
