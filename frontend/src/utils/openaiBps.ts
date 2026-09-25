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

export interface BPSCredentialState {
  status: 'unknown' | 'not_expired' | 'expired' | 'revoked' | 'auth_failed'
  expires_at?: string | null
  error_code?: string
  observed_at?: string | null
  requires_manual_resume?: boolean
}

export interface BPSCredentialAccount {
  platform: string
  status?: string
  schedulable?: boolean
  error_message?: string | null
  credentials?: Record<string, unknown>
  bps_credential_state?: BPSCredentialState | null
}

export function bpsExpiryMilliseconds(value: unknown): number | null {
  if (typeof value !== 'number' && typeof value !== 'string') return null
  if (typeof value === 'string' && !value.trim()) return null
  const numeric = typeof value === 'number' || /^-?\d+(\.\d+)?$/.test(value.trim())
  const timestamp = numeric
    ? (Math.abs(Number(value)) < 1e11 ? Number(value) * 1000 : Number(value))
    : Date.parse(String(value))
  return Number.isFinite(timestamp) && Number.isFinite(new Date(timestamp).getTime()) ? timestamp : null
}

export function bpsCredentialState(account: BPSCredentialAccount, now: number): BPSCredentialState | null {
  if (account.platform !== 'openai_bps') return null
  const supplied = account.bps_credential_state
  const expires = bpsExpiryMilliseconds(supplied?.expires_at ?? account.credentials?.expires_at)
  const state: BPSCredentialState = {
    ...supplied,
    status: supplied?.status ?? (expires === null ? 'unknown' : 'not_expired'),
    expires_at: expires === null ? null : new Date(expires).toISOString(),
    requires_manual_resume: account.status === 'active' && account.schedulable === false
  }
  // A mixed-version backend may still return the earlier generic auth message.
  if (!supplied && account.status === 'error') {
    if (account.error_message === 'BPS authentication failed; replace access_token') state.status = 'auth_failed'
    if (account.error_message === 'BPS access token revoked; replace access_token') state.status = 'revoked'
    if (account.error_message === 'BPS access token expired; replace it manually') state.status = 'expired'
  }
  if (state.status !== 'revoked' && expires !== null && expires <= now) state.status = 'expired'
  return state
}
export function isBPSCredentialFailure(state: BPSCredentialState | null | undefined): boolean {
  return state?.status === 'expired' || state?.status === 'revoked' || state?.status === 'auth_failed'
}
