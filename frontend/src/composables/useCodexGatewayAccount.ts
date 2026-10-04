import type { GatewayCredentialInput, CodexGatewayState } from '@/types'

export const CODEX_MOBILE_OAUTH_CLIENT_ID = 'app_LlGpXReQgckcGGUo2JrYvtJK'
export type CodexGatewayCredentialType = GatewayCredentialInput['type'] | 'mobile_refresh_token'

export function codexGatewayStatusKey(state?: CodexGatewayState): string {
  const pending: Record<string, string> = { needs_reauthorization: 'reauthorize', outcome_unknown: 'unknown', pending_delete: 'deleting', remote_missing: 'missing' }
  if (state && pending[state.sync_state]) return pending[state.sync_state]
  if (state?.service_available === false) return 'unavailable'
  if (state?.snapshot?.credential.refresh_blocked) return 'refreshBlocked'
  if (state?.snapshot?.credential.refresh_rejected) return 'reauthorize'
  if (state?.sync_state === 'ready') return state.snapshot?.can_accept_requests ? 'ready' : 'accountUnavailable'
  return 'syncing'
}

export interface CodexGatewayDraft {
  type: CodexGatewayCredentialType
  replace: boolean
  authJSON: string
  accessToken: string
  refreshToken: string
  accountID: string
  runtimeID: string
  privateKey: string
  taskID: string
  deviceID: string
  oauthClientID: string
  preserveRefreshToken: boolean
  oauthSessionID: string
  oauthCallbackURL: string
  oauthCode: string
  oauthState: string
}

export const newCodexGatewayDraft = (): CodexGatewayDraft => ({
  type: 'auth_json', replace: false, authJSON: '', accessToken: '', refreshToken: '',
  accountID: '', runtimeID: '', privateKey: '', taskID: '', deviceID: '', oauthClientID: '', preserveRefreshToken: false, oauthSessionID: '', oauthCallbackURL: '', oauthCode: '', oauthState: ''
})

export function codexGatewayCredentials(draft: CodexGatewayDraft): GatewayCredentialInput {
  const required = (value: string) => {
    if (!value.trim()) throw new Error('Credential fields are incomplete')
    return value.trim()
  }
  switch (draft.type) {
    case 'oauth_code': return { type: 'oauth_code', session_id: required(draft.oauthSessionID), code: required(draft.oauthCode), state: required(draft.oauthState), ...(draft.accountID.trim() ? { chatgpt_account_id: draft.accountID.trim() } : {}) }
    case 'auth_json': {
      const value: unknown = JSON.parse(required(draft.authJSON))
      if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('Invalid auth.json')
      return { type: 'auth_json', auth_json: value as Record<string, unknown> }
    }
    case 'tokens': return {
      type: 'tokens', access_token: required(draft.accessToken), chatgpt_account_id: required(draft.accountID),
      ...(draft.refreshToken.trim() ? { refresh_token: draft.refreshToken.trim() } : {})
    }
    case 'refresh_token':
    case 'mobile_refresh_token': return {
      type: 'refresh_token', refresh_token: required(draft.refreshToken),
      ...(draft.accountID.trim() ? { chatgpt_account_id: draft.accountID.trim() } : {})
    }
    case 'personal_access_token': return { type: 'personal_access_token', access_token: required(draft.accessToken) }
    case 'setup_token': return {
      type: 'setup_token', access_token: required(draft.accessToken),
      ...(draft.accountID.trim() ? { chatgpt_account_id: draft.accountID.trim() } : {})
    }
    case 'agent_identity': return {
      type: 'agent_identity', agent_runtime_id: required(draft.runtimeID), agent_private_key: required(draft.privateKey),
      ...(draft.taskID.trim() ? { task_id: draft.taskID.trim() } : {}),
      ...(draft.accountID.trim() ? { chatgpt_account_id: draft.accountID.trim() } : {})
    }
  }
}

// Gateway endpoints require a UUID key. randomUUID is limited to secure contexts,
// so plain-HTTP admin panels build an RFC 4122 v4 UUID from getRandomValues.
export function newCodexGatewayOperationKey(): string {
  if (typeof globalThis.crypto?.randomUUID === 'function') return globalThis.crypto.randomUUID()
  const bytes = globalThis.crypto.getRandomValues(new Uint8Array(16))
  bytes[6] = (bytes[6] & 0x0f) | 0x40
  bytes[8] = (bytes[8] & 0x3f) | 0x80
  const hex = Array.from(bytes, byte => byte.toString(16).padStart(2, '0')).join('')
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}
