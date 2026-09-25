import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { createI18n } from 'vue-i18n'
import OpenAIBPSAccountFields from '../OpenAIBPSAccountFields.vue'
import { bpsCredentials, newBPSAccountDraft } from '@/utils/openaiBps'

function render(editing = false) {
  const draft = newBPSAccountDraft({ access_token: 'must-never-render', chatgpt_account_id: 'workspace-1' })
  return { draft, wrapper: mount(OpenAIBPSAccountFields, {
    props: { modelValue: draft, editing },
    global: { plugins: [createI18n({ legacy: false, locale: 'en', missingWarn: false, fallbackWarn: false, messages: { en: {} } })] }
  }) }
}
describe('OpenAI BPS account fields', () => {
  it('never loads a saved token into the edit form and preserves it on a blank edit', () => {
    const { draft, wrapper } = render(true)
    const token = wrapper.get<HTMLInputElement>('#bps-access-token')
    expect(token.attributes('type')).toBe('password')
    expect(token.element.value).toBe('')
    expect(token.attributes('required')).toBeUndefined()
    expect(wrapper.html()).not.toContain('must-never-render')
    expect(bpsCredentials(draft)).not.toHaveProperty('access_token')
    expect(bpsCredentials(draft).chatgpt_account_id).toBe('workspace-1')
  })
  it('requires a token on creation and submits replacement credentials and model aliases', async () => {
    const { draft, wrapper } = render()
    expect(wrapper.get('#bps-access-token').attributes('required')).toBeDefined()
    await wrapper.get('#bps-access-token').setValue('  replacement  ')
    await wrapper.get('#bps-account-id').setValue(' override ')
    draft.models = [{ from: 'my-astra', to: 'gpt-6-astra' }]
    expect(bpsCredentials(draft)).toEqual({ access_token: 'replacement', chatgpt_account_id: 'override', model_mapping: { 'my-astra': 'gpt-6-astra' } })
  })
  it('starts with the two candidate models and preserves administrator mappings', () => {
    expect(Object.keys(bpsCredentials(newBPSAccountDraft()).model_mapping as object)).toEqual(['gpt-6-astra', 'gpt-5.6-sol'])
    const draft = newBPSAccountDraft({ model_mapping: { alias: 'gpt-5.6-sol' } })
    expect(draft.models).toEqual([{ from: 'alias', to: 'gpt-5.6-sol' }])
    draft.models = []
    expect(bpsCredentials(draft).model_mapping).toEqual({})
  })
})
