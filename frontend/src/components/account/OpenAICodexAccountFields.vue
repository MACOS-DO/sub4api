<template>
  <section class="space-y-4 rounded-lg border border-gray-200 p-4 dark:border-dark-600" data-testid="codex-gateway-account-fields">
    <div class="flex items-center justify-between gap-3">
      <h3 class="font-medium">OpenAI Codex</h3>
      <span class="text-xs text-gray-500">{{ t('admin.accounts.codexGateway.managed') }}</span>
    </div>
    <div v-if="gateway" class="rounded-lg bg-gray-50 p-3 text-sm dark:bg-dark-700" role="status">
      <strong>{{ statusLabel }}</strong>
      <p v-if="gateway.error" class="mt-1 text-xs text-amber-700 dark:text-amber-300">{{ gateway.error.message }}</p>
      <p v-if="gateway.authentication" class="mt-1 text-xs text-gray-500">{{ gateway.authentication }}</p>
      <p v-if="gateway.profile" class="mt-1 text-xs text-gray-500">{{ gateway.profile.email }} {{ gateway.profile.plan_type }}</p>
      <details class="mt-2 break-all text-xs text-gray-500">
        <summary>{{ t('admin.accounts.codexGateway.details') }}</summary>
        <p>{{ t('admin.accounts.codexGateway.binding') }}: {{ gateway.binding_id }}</p>
        <p>Revision: {{ gateway.revision }}</p>
        <p v-if="gateway.operation_id">{{ t('admin.accounts.codexGateway.operation') }}: {{ gateway.operation_id }}</p>
      </details>
    </div>
    <label v-if="editing && !shadow" class="flex items-center gap-2 text-sm">
      <input v-model="draft.replace" type="checkbox" class="checkbox">
      {{ t('admin.accounts.codexGateway.replace') }}
    </label>
    <template v-if="!shadow && (!editing || draft.replace)">
      <div v-if="standaloneAuth" class="rounded-lg border border-blue-200 bg-blue-50 p-3 text-sm dark:border-blue-800 dark:bg-blue-950/30">
        <div v-if="draft.type !== 'oauth_code'" class="flex items-center justify-between gap-3">
          <div>
            <strong>ChatGPT OAuth</strong>
            <p class="mt-1 text-xs text-gray-600 dark:text-gray-300">Authorize in the browser, then paste the callback URL.</p>
          </div>
          <button type="button" class="btn btn-secondary btn-sm" :disabled="sessionLoading || !supports('oauth_code')" @click="beginOAuth">
            {{ sessionLoading ? 'Opening…' : 'Authorize' }}
          </button>
        </div>
        <OAuthAuthorizationFlow
          v-else
          ref="oauthFlowRef"
          add-method="oauth"
          platform="openai_codex"
          :auth-url="authURL"
          :session-id="draft.oauthSessionID"
          :loading="sessionLoading"
          :error="authError"
          :show-help="false"
          :show-proxy-warning="false"
          :show-cookie-option="false"
          :show-manual-option="true"
          :show-refresh-token-option="false"
          :strict-callback="true"
          initial-input-method="manual"
          @generate-url="beginOAuth"
          @callback-invalid="authError = $event"
        />
        <button
          v-if="draft.oauthSessionID"
          type="button"
          class="btn btn-primary mt-3 w-full"
          :disabled="sessionLoading || !oauthFlowRef?.authCode?.trim() || !oauthFlowRef?.oauthState?.trim()"
          @click="acceptOAuthCallback"
        >
          Use authorization
        </button>
        <p v-if="draft.oauthSessionID" class="mt-2 text-xs text-gray-500">Session: {{ draft.oauthSessionID }}</p>
      </div>
      <div>
        <label class="input-label">{{ t('admin.accounts.codexGateway.method') }}</label>
        <p v-if="capabilitiesLoaded && supportedCredentialTypes.length === 0" class="mb-2 text-xs text-amber-700 dark:text-amber-300">Gateway reports no credential import methods.</p>
        <select v-model="draft.type" class="input" @change="handleMethodChange">
          <option v-if="draft.type === 'oauth_code'" value="oauth_code">Browser OAuth callback</option>
          <option value="auth_json" :disabled="!supports('auth_json')">auth.json</option><option value="tokens" :disabled="!supports('tokens')">AT / RT</option>
          <option value="refresh_token" :disabled="!supports('refresh_token')">RT-only</option><option value="mobile_refresh_token" :disabled="!supports('refresh_token')">Mobile RT</option><option value="personal_access_token" :disabled="!supports('personal_access_token')">PAT</option>
          <option value="setup_token" :disabled="!supports('setup_token')">Setup Token</option><option value="agent_identity" :disabled="!supports('agent_identity')">Agent Identity</option>
        </select>
      </div>
      <div v-if="draft.type === 'auth_json'">
        <label class="input-label">auth.json</label>
        <textarea v-model="draft.authJSON" class="input font-mono text-xs" rows="5" autocomplete="off" spellcheck="false" :required="standaloneAuth" />
        <input type="file" accept="application/json,.json" class="mt-2 block w-full text-xs" @change="importJSON">
      </div>
      <div v-if="['tokens', 'personal_access_token', 'setup_token'].includes(draft.type)">
        <label class="input-label">{{ draft.type === 'personal_access_token' ? 'Personal Access Token' : draft.type === 'setup_token' ? 'Setup Token' : 'Access Token' }}</label>
        <input v-model="draft.accessToken" type="password" autocomplete="new-password" class="input" :required="standaloneAuth">
      </div>
      <div v-if="draft.type === 'tokens' || draft.type === 'refresh_token' || draft.type === 'mobile_refresh_token'">
        <label class="input-label">Refresh Token {{ draft.type === 'tokens' ? t('admin.accounts.codexGateway.optional') : '' }}</label>
        <input v-model="draft.refreshToken" type="password" autocomplete="new-password" class="input" :required="standaloneAuth && (draft.type === 'refresh_token' || draft.type === 'mobile_refresh_token')">
        <p v-if="draft.type === 'mobile_refresh_token'" class="input-hint">Uses the ChatGPT mobile OAuth client automatically.</p>
      </div>
      <label v-if="editing && draft.type === 'tokens' && gateway?.authentication === 'at_rt'" class="flex items-center gap-2 text-sm">
        <input v-model="draft.preserveRefreshToken" type="checkbox" class="checkbox">
        Preserve the existing refresh token when updating with a new access token
      </label>
      <template v-if="draft.type === 'agent_identity'">
        <div><label class="input-label">Agent Runtime ID</label><input v-model="draft.runtimeID" class="input" :required="standaloneAuth"></div>
        <div><label class="input-label">Agent Private Key</label><textarea v-model="draft.privateKey" class="input font-mono text-xs" autocomplete="off" spellcheck="false" rows="3" :required="standaloneAuth" /></div>
        <div><label class="input-label">Task ID {{ t('admin.accounts.codexGateway.optional') }}</label><input v-model="draft.taskID" class="input"></div>
      </template>
      <div v-if="['tokens', 'refresh_token', 'setup_token', 'agent_identity'].includes(draft.type)">
        <label class="input-label">ChatGPT Account ID {{ draft.type !== 'tokens' ? t('admin.accounts.codexGateway.optional') : '' }}</label>
        <input v-model="draft.accountID" class="input" :required="draft.type === 'tokens'">
      </div>
      <p class="input-hint">{{ t('admin.accounts.codexGateway.credentialHint') }}</p>
    </template>
    <div>
      <label class="input-label">{{ t('admin.accounts.codexGateway.deviceID') }}</label>
      <input v-model="draft.deviceID" class="input" :disabled="shadow" :placeholder="t('admin.accounts.codexGateway.deviceHint')">
    </div>
    <details v-if="!shadow" class="rounded-lg border border-gray-200 p-3 text-sm dark:border-dark-600">
      <summary class="cursor-pointer font-medium">Batch import</summary>
      <div class="mt-3 grid gap-2">
        <select v-model="batchMethod" class="input">
          <option value="codex_session" :disabled="!supports('auth_json') && !supports('tokens')">auth.json / session credentials</option>
          <option value="agent_identity" :disabled="!supports('agent_identity')">Agent Identity</option>
          <option value="refresh_token" :disabled="!supports('refresh_token')">Refresh Token</option>
          <option value="mobile_refresh_token" :disabled="!supports('refresh_token')">Mobile Refresh Token</option>
          <option value="personal_access_token" :disabled="!supports('personal_access_token')">Personal Access Token</option>
          <option value="setup_token" :disabled="!supports('setup_token')">Setup Token</option>
        </select>
        <textarea v-model="batchContent" class="input font-mono text-xs" rows="4" autocomplete="off" spellcheck="false" placeholder="One credential per line, or a JSON/session export." />
        <button type="button" class="btn btn-secondary btn-sm justify-self-start" :disabled="batchLoading || !batchContent.trim()" @click="submitBatchImport">
          {{ batchLoading ? 'Importing…' : 'Import credentials' }}
        </button>
        <p v-if="batchStatus" class="text-xs text-gray-600 dark:text-gray-300" role="status">{{ batchStatus }}</p>
      </div>
    </details>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { CodexGatewayState } from '@/types'
import { CODEX_MOBILE_OAUTH_CLIENT_ID, codexGatewayStatusKey, type CodexGatewayDraft } from '@/composables/useCodexGatewayAccount'
import { cancelCodexOAuthSession, createCodexOAuthSession, getCodexGatewayCapabilities } from '@/api/admin/accounts'
import OAuthAuthorizationFlow from './OAuthAuthorizationFlow.vue'

const props = withDefaults(defineProps<{ editing?: boolean; shadow?: boolean; gateway?: CodexGatewayState; proxyId?: number | null; accountId?: number; batchLoading?: boolean; batchStatus?: string; standaloneAuth?: boolean }>(), { editing: false, shadow: false, batchLoading: false, batchStatus: '', standaloneAuth: true })
const emit = defineEmits<{ 'batch-import': [payload: { method: 'codex_session' | 'agent_identity' | 'refresh_token' | 'mobile_refresh_token' | 'personal_access_token' | 'setup_token'; content: string }] }>()
const draft = defineModel<CodexGatewayDraft>({ required: true })
const { t } = useI18n()
const statusLabel = computed(() => t(`admin.accounts.codexGateway.${codexGatewayStatusKey(props.gateway)}`))
const authURL = ref('')
const authError = ref('')
const sessionLoading = ref(false)
const sessionProxyID = ref<number | null>(null)
const sessionClientID = ref('')
const oauthFlowRef = ref<{ authCode?: string; oauthState?: string } | null>(null)
const batchMethod = ref<'codex_session' | 'agent_identity' | 'refresh_token' | 'mobile_refresh_token' | 'personal_access_token' | 'setup_token'>('codex_session')
const batchContent = ref('')
const capabilitiesLoaded = ref(false)
const supportedCredentialTypes = ref<string[]>(['auth_json', 'tokens', 'refresh_token', 'personal_access_token', 'setup_token', 'agent_identity', 'oauth_code'])
const supports = (type: string) => !capabilitiesLoaded.value || supportedCredentialTypes.value.includes(type)

onMounted(async () => {
  try {
    const result = await getCodexGatewayCapabilities()
    const supported = result.supported_credential_types
    supportedCredentialTypes.value = supported == null ? supportedCredentialTypes.value : supported
    capabilitiesLoaded.value = true
    if (supportedCredentialTypes.value.length > 0 && !supports(draft.value.type)) {
      draft.value.type = supportedCredentialTypes.value[0] as CodexGatewayDraft['type']
      handleMethodChange()
    }
  } catch {
    // Older Gateways have no capability endpoint; retain the legacy mode fallback.
  }
})
async function beginOAuth() {
  if (!supports('oauth_code')) {
    authError.value = 'Gateway does not support browser OAuth'
    return
  }
  sessionLoading.value = true
  authError.value = ''
  try {
    const session = await createCodexOAuthSession({ purpose: props.editing ? 'reauthorize' : 'create', ...(props.accountId ? { account_id: props.accountId } : {}), proxy_id: props.proxyId ?? undefined, oauth_client_id: draft.value.oauthClientID || undefined })
    draft.value.type = 'oauth_code'
    draft.value.oauthSessionID = session.session_id
    authURL.value = session.auth_url
    sessionProxyID.value = props.proxyId ?? null
    sessionClientID.value = draft.value.oauthClientID.trim()
  } catch (error: any) {
    authError.value = error?.response?.data?.message || error?.message || 'Gateway authorization is unavailable'
  } finally { sessionLoading.value = false }
}

function acceptOAuthCallback() {
  const code = oauthFlowRef.value?.authCode?.trim() || ''
  const state = oauthFlowRef.value?.oauthState?.trim() || ''
  if (!code || !state) {
    authError.value = 'Callback must contain exactly one non-empty code and state'
    return
  }
  draft.value.type = 'oauth_code'
  draft.value.oauthCode = code
  draft.value.oauthState = state
  authError.value = ''
}

async function invalidateOAuthSession() {
  const sessionID = draft.value.oauthSessionID.trim()
  if (!sessionID) return
  draft.value.oauthSessionID = ''
  draft.value.oauthCode = ''
  draft.value.oauthState = ''
  draft.value.oauthCallbackURL = ''
  authURL.value = ''
  try { await cancelCodexOAuthSession(sessionID) } catch { /* the next authorization request will create a fresh session */ }
}

watch(() => [props.proxyId ?? null, draft.value.oauthClientID.trim()] as const, ([proxyID, clientID]) => {
  if (!draft.value.oauthSessionID) return
  if (proxyID !== sessionProxyID.value || clientID !== sessionClientID.value) {
    void invalidateOAuthSession()
  }
})

watch(() => draft.value.type, (type) => {
  if (type !== 'oauth_code' && draft.value.oauthSessionID) void invalidateOAuthSession()
})

function handleMethodChange() {
  if (draft.value.type === 'mobile_refresh_token') {
    draft.value.oauthClientID = CODEX_MOBILE_OAUTH_CLIENT_ID
  } else if (draft.value.type === 'refresh_token' && draft.value.oauthClientID === CODEX_MOBILE_OAUTH_CLIENT_ID) {
    draft.value.oauthClientID = ''
  }
}

function submitBatchImport() {
  const content = batchContent.value.trim()
  if (!content) return
  emit('batch-import', { method: batchMethod.value, content })
}

async function importJSON(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file) return
  try { draft.value.authJSON = await file.text() } finally { input.value = '' }
}
</script>
