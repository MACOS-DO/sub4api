<template>
  <BaseDialog
    :show="show && !!account"
    :title="text('title')"
    width="wide"
    content-class="h-[min(800px,92dvh)] overflow-hidden"
    body-class="!p-0 !overflow-hidden"
    :z-index="10000"
    :close-on-escape="false"
    @close="handleClose"
  >
    <template #header="{ titleId }">
      <div class="flex min-w-0 items-center gap-3">
        <span class="flex h-11 w-11 shrink-0 items-center justify-center rounded-xl bg-primary-50 text-primary-700 dark:bg-primary-900/30 dark:text-primary-300"><Icon name="sparkles" size="md" /></span>
        <div class="min-w-0">
          <h2 :id="titleId" class="text-lg font-semibold text-gray-900 dark:text-white">{{ text('title') }}</h2>
          <p class="mt-1 break-words text-xs text-gray-500 dark:text-dark-400"><span class="font-medium text-gray-700 dark:text-dark-200">{{ account?.name }}</span> · OpenAI {{ account?.type === 'oauth' ? 'OAuth' : 'Setup Token' }}</p>
        </div>
      </div>
      <div class="ml-3 flex shrink-0 items-center gap-4">
        <div class="hidden items-center gap-2 text-xs text-gray-500 dark:text-dark-400 lg:flex" :aria-label="text('steps')">
          <span :class="diagnosticStage === 'setup' ? 'font-medium text-primary-700 dark:text-primary-300' : ''">1 · {{ text('setup') }}</span>
          <span class="h-px w-5 bg-gray-200 dark:bg-dark-600"></span>
          <span :class="diagnosticStage === 'results' ? 'font-medium text-primary-700 dark:text-primary-300' : ''">2 · {{ text('viewResultsStep') }}</span>
        </div>
        <button type="button" class="flex h-11 w-11 shrink-0 items-center justify-center rounded-xl text-gray-500 hover:bg-gray-100 dark:text-dark-400 dark:hover:bg-dark-700" :aria-label="text('close')" @click="handleClose"><Icon name="x" size="md" /></button>
      </div>
    </template>

    <div ref="scrollViewport" class="h-full overflow-y-auto overscroll-contain">
      <div v-if="diagnosticStage === 'setup'" class="grid min-h-full md:grid-cols-[250px_minmax(0,1fr)]">
        <aside class="border-b border-gray-200 bg-gray-50 px-5 pb-2 pt-4 dark:border-dark-700 dark:bg-dark-900/40 md:border-b-0 md:border-r md:px-6 md:py-6">
          <h3 class="mb-6 hidden text-sm font-semibold text-gray-900 dark:text-white md:block">{{ text('settings') }}</h3>
          <label for="diagnostic-billing-key" class="mb-2 block text-sm font-medium text-gray-700 dark:text-dark-200">{{ text('key') }}</label>
          <Select
            id="diagnostic-billing-key"
            v-model="selectedKey"
            :options="keyOptions"
            searchable
            :loading="keysLoading"
            :disabled="keysLoading || diagnosing"
            :aria-label="text('key')"
          />
          <p v-if="keysLoading" role="status" class="mt-2 text-xs text-gray-500 dark:text-dark-400">{{ text('loadingKeys') }}</p>
          <div v-else-if="keysError" role="alert" class="mt-3 text-sm text-red-700 dark:text-red-300">
            <p>{{ text('keysError') }}: {{ keysError }}</p>
            <button type="button" class="mt-2 min-h-9 text-sm font-medium underline underline-offset-4" @click="loadKeys">{{ text('retry') }}</button>
          </div>
          <p v-else-if="!keys.length" class="mt-3 text-sm text-amber-700 dark:text-amber-300">{{ text('noKey') }}</p>
          <p class="mt-3 hidden text-xs leading-6 text-gray-500 dark:text-dark-400 md:block">{{ text('keyHint') }}</p>
          <details class="mt-2 text-xs text-gray-500 dark:text-dark-400 md:mt-6 md:border-t md:border-gray-200 md:pt-2 dark:md:border-dark-700">
            <summary class="flex min-h-11 cursor-pointer list-none items-center justify-between gap-2 text-gray-700 dark:text-dark-200">{{ text('info') }}<Icon name="chevronDown" size="sm" /></summary>
            <p class="mb-3 leading-6">{{ text('templateHint') }}</p>
            <div class="mb-3 flex flex-wrap justify-between gap-2"><span>{{ text('fingerprint') }}</span><span class="break-all font-mono" :title="fingerprintCommit">{{ fingerprintCommit ? fingerprintCommit.slice(0, 12) : '—' }}</span></div>
            <button type="button" class="btn btn-secondary min-h-9 !px-3 !py-1.5 !text-xs" :disabled="modelsLoading || diagnosing" @click="loadModels(true)">{{ text('updateBank') }}</button>
          </details>
        </aside>
        <section class="min-w-0 px-5 py-6 sm:px-7" :aria-label="text('chooseModels')">
          <div class="mb-1 flex items-center justify-between gap-3">
            <h3 ref="setupHeading" tabindex="-1" class="text-base font-semibold text-gray-900 outline-none dark:text-white">{{ text('chooseModels') }}</h3>
            <span class="shrink-0 rounded-md bg-primary-50 px-2 py-1 text-xs font-medium text-primary-700 dark:bg-primary-900/30 dark:text-primary-300">{{ text('selected', { count: selectedModels.length }) }}</span>
          </div>
          <p class="mb-5 text-xs leading-5 text-gray-500 dark:text-dark-400">{{ text('modelHint') }}</p>
          <p v-if="modelsLoading" role="status" class="py-8 text-center text-sm text-gray-500 dark:text-dark-400">{{ text('loadingModels') }}</p>
          <div v-else-if="modelsError" role="alert" class="rounded-xl border border-red-200 bg-red-50 p-4 text-sm text-red-700 dark:border-red-900 dark:bg-red-900/20 dark:text-red-300">
            <p>{{ text('modelsError') }}: {{ modelsError }}</p>
            <button type="button" class="mt-2 min-h-11 font-medium underline underline-offset-4" @click="loadModels()">{{ text('retry') }}</button>
          </div>
          <p v-else-if="!models.length" class="rounded-xl border border-gray-200 px-4 py-8 text-center text-sm text-gray-500 dark:border-dark-700 dark:text-dark-400">{{ text('noModels') }}</p>
          <CodexDiagnosticModelPicker v-else v-model="selectedModels" :models="models" :disabled="diagnosing" />
        </section>
      </div>

      <section v-else-if="diagnosticRun" class="px-5 py-6 sm:px-7" :aria-label="text('results')">
        <div data-test="diagnostic-progress">
          <div class="flex items-center justify-between gap-3">
            <h3 ref="resultsHeading" tabindex="-1" class="text-base font-semibold text-gray-900 outline-none dark:text-white">{{ text(diagnosing ? 'runningTitle' : 'results') }}</h3>
            <span class="shrink-0 rounded-md px-2 py-1 text-xs font-medium" :class="diagnosticRun.state === 'cancelled' ? statusTone('stopped') : statusTone('normal')">{{ text(diagnosing ? 'running' : diagnosticRun.state === 'cancelled' ? 'cancelled' : 'finished') }}</span>
          </div>
          <div class="mt-2 flex flex-wrap gap-x-3 gap-y-1 text-xs text-gray-500 dark:text-dark-400">
            <span>{{ text('billingKey') }}: <span data-test="diagnostic-key">{{ diagnosticRun.keyName }} · #{{ diagnosticRun.keyID }}</span></span>
            <span>{{ text('runContext', { count: diagnosticTotal }) }}</span>
          </div>
          <div class="mt-5 flex items-center justify-between gap-3 text-xs">
            <p role="status" aria-live="polite" class="min-w-0 break-words text-gray-800 dark:text-dark-200">{{ currentDiagnostic ? text('checking', { model: currentDiagnostic }) : text(diagnosticRun.state === 'cancelled' ? 'stoppedSummary' : 'finishedSummary') }}</p>
            <span class="shrink-0 tabular-nums text-gray-500 dark:text-dark-400">{{ text('completed', { done: results.length, total: diagnosticTotal }) }}</span>
          </div>
          <div class="mt-2 h-1.5 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700" role="progressbar" :aria-label="text('progress')" :aria-valuemin="0" :aria-valuemax="diagnosticTotal" :aria-valuenow="results.length"><div class="h-full rounded-full bg-primary-600 transition-[width] motion-reduce:transition-none" :style="{ width: diagnosticTotal ? (results.length / diagnosticTotal * 100) + '%' : '0%' }"></div></div>
        </div>
        <div class="my-6 grid grid-cols-2 overflow-hidden rounded-xl border border-gray-200 bg-gray-50 dark:border-dark-700 dark:bg-dark-900/30 sm:grid-cols-4" data-test="diagnostic-summary">
          <div v-for="(status, index) in summaryStatuses" :key="status" class="px-4 py-3" :class="[index % 2 ? 'border-l border-gray-200 dark:border-dark-700' : '', index > 1 ? 'border-t border-gray-200 dark:border-dark-700 sm:border-t-0' : '', index === 2 ? 'sm:border-l' : '']">
            <div class="flex items-center gap-2 text-xs text-gray-500 dark:text-dark-400"><span class="h-1.5 w-1.5 shrink-0 rounded-full" :class="summaryDot(status)"></span>{{ text('status.' + status) }}</div>
            <strong class="mt-1 block text-2xl font-semibold tabular-nums text-gray-900 dark:text-white">{{ diagnosticRun.entries.filter(entry => entry.status === status).length }}</strong>
          </div>
        </div>
        <div class="mb-3 flex flex-wrap justify-between gap-2 text-xs text-gray-500 dark:text-dark-400"><span>{{ text('individualResults') }}</span><span>{{ text('expandHint') }}</span></div>
        <div class="space-y-3">
          <article v-for="(entry, index) in diagnosticRun.entries" :key="entry.model" :data-model="entry.model" :data-status="entry.status" class="overflow-hidden rounded-xl border border-gray-200 dark:border-dark-700">
            <div class="diagnostic-result-row flex flex-wrap items-center gap-x-3 gap-y-1 p-3 sm:flex-nowrap sm:py-4">
              <span aria-hidden="true" class="flex h-8 w-8 shrink-0 items-center justify-center rounded-full" :class="statusTone(entry.status)">
                <span v-if="entry.status === 'running'" class="h-4 w-4 animate-spin rounded-full border-2 border-current border-r-transparent motion-reduce:animate-none"></span>
                <Icon v-else :name="statusIcon(entry.status)" size="sm" />
              </span>
              <div class="diagnostic-result-model min-w-0 flex-1"><h4 class="font-mono text-[13px] font-medium leading-6 text-gray-900 [overflow-wrap:anywhere] dark:text-white">{{ entry.model }}</h4><p class="mt-0.5 text-xs leading-5 text-gray-500 dark:text-dark-400">{{ text('caption.' + entry.status) }}</p></div>
              <span class="diagnostic-result-badge shrink-0 rounded-md px-2 py-1 text-xs font-medium" :class="statusTone(entry.status)">{{ text('status.' + entry.status) }}</span>
              <button v-if="entry.result" type="button" class="ml-auto flex h-11 w-11 shrink-0 items-center justify-center rounded-lg text-gray-500 hover:bg-gray-100 dark:text-dark-400 dark:hover:bg-dark-700" :aria-expanded="entry.expanded" :aria-controls="'codex-diagnostic-detail-' + index" :aria-label="text(entry.expanded ? 'hideDetails' : 'showDetails', { model: entry.model })" @click="entry.expanded = !entry.expanded"><Icon name="chevronDown" size="sm" :class="entry.expanded ? 'rotate-180' : ''" /></button>
              <span v-else class="ml-auto h-3 w-11 shrink-0 sm:h-11"></span>
            </div>
            <dl v-if="entry.result && entry.expanded" :id="'codex-diagnostic-detail-' + index" class="grid gap-4 border-t border-gray-200 bg-gray-50 p-4 text-xs dark:border-dark-700 dark:bg-dark-900/30 sm:grid-cols-2">
              <template v-if="entry.result.predicted_model || entry.result.probability != null">
                <div><dt class="text-gray-500 dark:text-dark-400">{{ text('prediction') }}</dt><dd class="mt-1 break-all font-mono text-gray-800 dark:text-dark-200">{{ entry.result.predicted_model || '—' }}</dd></div>
                <div><dt class="text-gray-500 dark:text-dark-400">{{ text('probability') }}</dt><dd class="mt-1 text-gray-800 dark:text-dark-200">{{ entry.result.probability == null ? '—' : (entry.result.probability * 100).toFixed(1) + '%' }}</dd></div>
              </template>
              <div v-if="entry.result.reason" class="sm:col-span-2"><dt class="text-gray-500 dark:text-dark-400">{{ text('reason') }}</dt><dd class="mt-1 whitespace-pre-wrap break-words text-gray-800 [overflow-wrap:anywhere] dark:text-dark-200">{{ entry.result.reason === 'template_invalid' ? text('templateInvalid') : entry.result.reason }}</dd></div>
              <div v-if="entry.result.gateway_error_code || entry.result.http_status" class="sm:col-span-2"><dt class="text-gray-500 dark:text-dark-400">{{ text('gateway') }}</dt><dd class="mt-1 break-all text-gray-800 dark:text-dark-200">{{ [entry.result.gateway_error_code, entry.result.http_status ? 'HTTP ' + entry.result.http_status : ''].filter(Boolean).join(' · ') }}</dd></div>
              <div class="sm:col-span-2"><dt class="text-gray-500 dark:text-dark-400">{{ text('fingerprint') }}</dt><dd class="mt-1 break-all font-mono text-gray-800 dark:text-dark-200">{{ diagnosticRun.fingerprintCommit || '—' }}</dd></div>
            </dl>
          </article>
        </div>
        <p class="mt-4 text-xs leading-6 text-gray-500 dark:text-dark-400">{{ text('failureHint') }}</p>
      </section>
    </div>

    <template #footer>
      <div class="flex w-full flex-col items-stretch justify-between gap-3 sm:flex-row sm:items-center">
        <p class="flex min-w-0 items-center justify-center gap-2 text-xs leading-5 text-gray-500 dark:text-dark-400 sm:justify-start"><Icon name="infoCircle" size="sm" class="shrink-0" /><span>{{ text(diagnosticStage === 'setup' ? selectedModels.length ? 'paid' : 'selectFirst' : diagnosing ? 'cancelHint' : 'keepResults') }}</span></p>
        <div class="flex w-full flex-wrap items-center gap-2 sm:w-auto sm:shrink-0">
          <template v-if="diagnosticStage === 'setup'">
            <button v-if="diagnosticRun" type="button" class="btn btn-ghost min-h-12 flex-1 sm:flex-none" data-action="view-results" @click="showDiagnosticStage('results')">{{ text('viewResults') }}</button>
            <button type="button" class="btn btn-primary min-h-12 flex-1 sm:flex-none" data-action="start" :disabled="!canStart" @click="runDiagnostic">{{ text('start', { count: selectedModels.length }) }}<Icon name="arrowRight" size="sm" /></button>
          </template>
          <button v-else-if="diagnosing" type="button" class="btn btn-secondary min-h-12 flex-1 sm:flex-none" data-action="cancel" @click="cancelDiagnostic">{{ text('cancel') }}</button>
          <button v-else type="button" class="btn btn-primary min-h-12 flex-1 sm:flex-none" data-action="retest" @click="showDiagnosticStage('setup')"><Icon name="arrowLeft" size="sm" />{{ text('retest') }}</button>
        </div>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import * as tickets from '@/api/admin/codexTickets'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'
import CodexDiagnosticModelPicker from './CodexDiagnosticModelPicker.vue'
import { extractApiErrorMessage } from '@/utils/apiError'
import type { Account } from '@/types'

const props = defineProps<{ show: boolean; account: Account | null }>()
const emit = defineEmits<{ close: []; completed: [accountID: number] }>()
const { t } = useI18n()
const text = (key: string, values: Record<string, string | number> = {}) => t(`admin.accounts.codexDiagnosticDialog.${key}`, values)
type DiagnosticStatus = tickets.TicketDiagnostic['status'] | 'pending' | 'running' | 'stopped' | 'not_run'
type DiagnosticStage = 'setup' | 'results'
interface DiagnosticEntry { model: string; status: DiagnosticStatus; result?: tickets.TicketDiagnostic; expanded: boolean }
interface DiagnosticRun { accountID: number; keyID: number; keyName: string; fingerprintCommit: string; entries: DiagnosticEntry[]; state: 'running' | 'finished' | 'cancelled' }
const summaryStatuses = ['normal', 'degraded', 'uncertain', 'failed'] as const
const keys = ref<Awaited<ReturnType<typeof tickets.ownKeys>>>([])
const keysLoading = ref(false)
const keysError = ref('')
const models = ref<string[]>([])
const modelsLoading = ref(false)
const modelsError = ref('')
const fingerprintCommit = ref('')
const selectedKey = ref<number | null>(null)
const selectedModels = ref<string[]>([])
const diagnosticStage = ref<DiagnosticStage>('setup')
const diagnosticRun = ref<DiagnosticRun | null>(null)
const scrollViewport = ref<HTMLElement | null>(null)
const setupHeading = ref<HTMLElement | null>(null)
const resultsHeading = ref<HTMLElement | null>(null)
const controller = ref<AbortController | null>(null)
let accountSessionSerial = 0
let keysSerial = 0
let modelsSerial = 0
let diagnosticSerial = 0
const closing = ref(false)
const diagnosing = computed(() => diagnosticRun.value?.state === 'running')
const results = computed(() => diagnosticRun.value?.entries.flatMap(entry => entry.result ? [entry.result] : []) ?? [])
const diagnosticTotal = computed(() => diagnosticRun.value?.entries.length ?? 0)
const currentDiagnostic = computed(() => diagnosticRun.value?.entries.find(entry => entry.status === 'running')?.model ?? '')
const keyOptions = computed(() => [
  { value: null, label: text('chooseKey') }, ...keys.value.map(key => ({ value: key.id, label: `${key.name} · #${key.id}` }))
])
const canStart = computed(() => props.show && !!props.account && !closing.value && !diagnosing.value &&
  !keysLoading.value && !modelsLoading.value && !keysError.value && !modelsError.value &&
  keys.value.some(key => key.id === selectedKey.value) && selectedModels.value.length > 0 &&
  selectedModels.value.every(model => models.value.includes(model)))
function isCurrentSession(session: number) { return session === accountSessionSerial && props.show && !!props.account && !closing.value }
async function loadKeys() {
  if (!props.show || !props.account || diagnosing.value || closing.value) return
  const session = accountSessionSerial
  const serial = ++keysSerial
  const isCurrent = () => isCurrentSession(session) && serial === keysSerial
  keysLoading.value = true
  keysError.value = ''
  try {
    const available = await tickets.ownKeys()
    if (!isCurrent()) return
    keys.value = available.filter(key => key.status === 'active' && (key.quota === 0 || key.quota_used < key.quota) && (!key.expires_at || new Date(key.expires_at) > new Date()))
    if (!keys.value.some(key => key.id === selectedKey.value)) selectedKey.value = null
  } catch (cause) { if (isCurrent()) keysError.value = extractApiErrorMessage(cause, text('error')) }
  finally { if (isCurrent()) keysLoading.value = false }
}
async function loadModels(refresh = false) {
  if (!props.show || !props.account || diagnosing.value || closing.value) return
  const session = accountSessionSerial
  const serial = ++modelsSerial
  const isCurrent = () => isCurrentSession(session) && serial === modelsSerial
  modelsLoading.value = true
  modelsError.value = ''
  try {
    const bank = await (refresh ? tickets.refreshFingerprint() : tickets.fingerprint())
    if (!isCurrent()) return
    fingerprintCommit.value = bank.commit
    models.value = [...new Set(bank.models)]
    selectedModels.value = selectedModels.value.filter(model => models.value.includes(model))
  } catch (cause) { if (isCurrent()) modelsError.value = extractApiErrorMessage(cause, text('error')) }
  finally { if (isCurrent()) modelsLoading.value = false }
}
async function showDiagnosticStage(stage: DiagnosticStage) {
  if ((stage === 'setup' && diagnosing.value) || (stage === 'results' && !diagnosticRun.value)) return
  diagnosticStage.value = stage
  const session = accountSessionSerial
  const run = diagnosticRun.value
  await nextTick()
  if (!isCurrentSession(session) || run !== diagnosticRun.value || diagnosticStage.value !== stage) return
  if (scrollViewport.value) scrollViewport.value.scrollTop = 0
  const heading = stage === 'results' ? resultsHeading.value : setupHeading.value
  heading?.focus({ preventScroll: true })
}
function cancelDiagnostic() {
  ++diagnosticSerial
  controller.value?.abort()
  controller.value = null
  const run = diagnosticRun.value
  if (!run || run.state !== 'running') return
  run.state = 'cancelled'
  for (const entry of run.entries) {
    if (entry.status === 'running') entry.status = 'stopped'
    else if (entry.status === 'pending') entry.status = 'not_run'
  }
  emit('completed', run.accountID)
}
async function runDiagnostic() {
  if (!canStart.value || !props.account || selectedKey.value == null) return
  const accountID = props.account.id
  const keyID = selectedKey.value
  const session = accountSessionSerial
  const serial = ++diagnosticSerial
  diagnosticRun.value = {
    accountID, keyID, keyName: keys.value.find(key => key.id === keyID)?.name ?? text('key'), fingerprintCommit: fingerprintCommit.value,
    entries: [...selectedModels.value].map(model => ({ model, status: 'pending', expanded: false })), state: 'running'
  }
  const run = diagnosticRun.value
  const isCurrent = () => serial === diagnosticSerial && isCurrentSession(session) && diagnosticRun.value === run && props.account?.id === accountID
  void showDiagnosticStage('results')
  try {
    for (const entry of run.entries) {
      if (!isCurrent()) break
      entry.status = 'running'
      const requestController = new AbortController()
      controller.value = requestController
      try {
        const data = await tickets.diagnose(accountID, keyID, [entry.model], requestController.signal)
        if (!isCurrent()) break
        const result = data.items?.find(item => item.model === entry.model)
        if (result) { entry.result = result; entry.status = result.status }
        if (data.canceled) { cancelDiagnostic(); break }
        if (!result) { entry.result = { model: entry.model, status: 'failed', reason: text('missingResult') }; entry.status = 'failed' }
      } catch (cause) {
        if (!isCurrent() || requestController.signal.aborted) break
        entry.result = { model: entry.model, status: 'failed', reason: extractApiErrorMessage(cause, text('error')) }
        entry.status = 'failed'
      } finally { if (controller.value === requestController) controller.value = null }
    }
  } finally {
    if (isCurrent()) { run.state = 'finished'; emit('completed', accountID) }
  }
}
function statusTone(status: DiagnosticStatus) {
  if (status === 'normal' || status === 'running') return 'bg-primary-50 text-primary-700 dark:bg-primary-900/30 dark:text-primary-300'
  if (status === 'degraded') return 'bg-red-50 text-red-700 dark:bg-red-900/25 dark:text-red-300'
  if (status === 'uncertain') return 'bg-amber-50 text-amber-700 dark:bg-amber-900/25 dark:text-amber-300'
  return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-dark-300'
}
function summaryDot(status: tickets.TicketDiagnostic['status']) {
  return { normal: 'bg-primary-600 dark:bg-primary-300', degraded: 'bg-red-600 dark:bg-red-300', uncertain: 'bg-amber-600 dark:bg-amber-300', failed: 'bg-gray-500 dark:bg-dark-400' }[status]
}
function statusIcon(status: DiagnosticStatus) {
  return ({ normal: 'check', degraded: 'exclamationTriangle', uncertain: 'infoCircle', failed: 'x', pending: 'clock', running: 'clock', stopped: 'x', not_run: 'clock' } as const)[status]
}
function handleClose() {
  cancelDiagnostic()
  closing.value = true
  ++accountSessionSerial
  emit('close')
}
function handleKeydown(event: KeyboardEvent) {
  if (!props.show || !props.account || closing.value || event.defaultPrevented) return
  if (event.key === 'Escape') { handleClose(); return }
  if (event.key !== 'Tab') return
  const dialog = scrollViewport.value?.closest('[role="dialog"]')
  if (!dialog?.contains(document.activeElement)) return
  const focusable = Array.from(dialog.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled), select:not(:disabled), summary, [tabindex="0"]')).filter(element => element.getClientRects().length)
  if (event.shiftKey && document.activeElement === focusable[0]) { event.preventDefault(); focusable.at(-1)?.focus() }
  else if (!event.shiftKey && document.activeElement === focusable.at(-1)) { event.preventDefault(); focusable[0]?.focus() }
}
watch(() => [props.show, props.account?.id], () => {
  cancelDiagnostic()
  ++accountSessionSerial
  ++keysSerial
  ++modelsSerial
  closing.value = false
  diagnosticRun.value = null
  diagnosticStage.value = 'setup'
  selectedKey.value = null
  selectedModels.value = []
  keys.value = []
  models.value = []
  keysError.value = ''
  modelsError.value = ''
  keysLoading.value = false
  modelsLoading.value = false
  fingerprintCommit.value = ''
  if (props.show && props.account) { void loadKeys(); void loadModels() }
}, { immediate: true })
onMounted(() => document.addEventListener('keydown', handleKeydown))
onUnmounted(() => { cancelDiagnostic(); ++accountSessionSerial; document.removeEventListener('keydown', handleKeydown) })
</script>

<style scoped>
summary::-webkit-details-marker { display: none; }
details[open] summary svg { transform: rotate(180deg); }
@media (max-width: 639px) {
  .diagnostic-result-model { flex-basis: calc(100% - 44px); }
  .diagnostic-result-badge { margin-left: 44px; }
}
</style>
