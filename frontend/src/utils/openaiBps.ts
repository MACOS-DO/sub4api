export const BPS_DEFAULT_MODELS = ['gpt-6-astra', 'gpt-5.6-sol'] as const
export interface BPSAccountDraft {
  token: string
  accountId: string
  models: { from: string; to: string }[]
}
export function newBPSAccountDraft(credentials?: Record<string, unknown>): BPSAccountDraft {
  const mapping = credentials?.model_mapping
  return {
    token: '',
    accountId: typeof credentials?.chatgpt_account_id === 'string' ? credentials.chatgpt_account_id : '',
    models: mapping && typeof mapping === 'object' && !Array.isArray(mapping)
      ? Object.entries(mapping).map(([from, to]) => ({ from, to: String(to) }))
      : BPS_DEFAULT_MODELS.map(model => ({ from: model, to: model }))
  }
}
export function bpsCredentials(draft: BPSAccountDraft): Record<string, unknown> {
  const credentials: Record<string, unknown> = {
    chatgpt_account_id: draft.accountId.trim(),
    model_mapping: Object.fromEntries(draft.models.filter(row => row.from.trim()).map(row => [row.from.trim(), row.to.trim() || row.from.trim()]))
  }
  if (draft.token.trim()) credentials.access_token = draft.token.trim()
  return credentials
}
