<template>
  <Teleport to="body">
    <div v-if="show && account" class="fixed inset-0 z-[10000] flex items-center justify-center bg-gray-950/60 p-2 backdrop-blur-sm sm:p-6" @click.self="emit('close')">
      <section role="dialog" aria-modal="true" :aria-label="text.title" class="flex max-h-[94vh] w-full max-w-5xl flex-col overflow-hidden rounded-2xl border border-gray-200 bg-gray-50 shadow-2xl dark:border-dark-600 dark:bg-dark-900">
        <header class="flex flex-wrap items-start justify-between gap-3 border-b border-gray-200 bg-white px-5 py-4 dark:border-dark-700 dark:bg-dark-800 sm:px-7 sm:py-5">
          <div class="min-w-0">
            <p class="truncate text-xs font-semibold text-primary-600 dark:text-primary-400">{{ account.name }} · CODEX</p>
            <h2 class="mt-1 text-xl font-semibold text-gray-900 dark:text-white">{{ text.title }}</h2>
            <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ text.retention }}</p>
          </div>
          <div class="flex items-center gap-2">
            <button type="button" class="btn btn-secondary !px-3 !py-1.5 !text-xs" :disabled="loading" @click="reload">{{ text.refresh }}</button>
            <button type="button" class="rounded-lg p-2 text-gray-500 hover:bg-gray-100 dark:hover:bg-dark-700" :aria-label="text.close" @click="emit('close')">✕</button>
          </div>
        </header>
        <div class="min-h-0 flex-1 overflow-y-auto px-4 py-5 sm:px-7">
          <div v-if="error" role="alert" class="mb-4 rounded-lg border border-red-200 bg-red-50 p-3 text-sm text-red-700 dark:border-red-900 dark:bg-red-900/20 dark:text-red-300">{{ error }}</div>
          <div class="mb-5 flex flex-wrap items-center justify-between gap-3 rounded-xl border border-gray-200 bg-white px-4 py-3 dark:border-dark-700 dark:bg-dark-800">
            <div class="flex items-baseline gap-2"><strong class="text-2xl font-semibold tabular-nums text-primary-600 dark:text-primary-400">{{ statuses.filter(status => status.ready).length }} / {{ statuses.length }}</strong><span class="text-sm text-gray-600 dark:text-dark-300">{{ text.available }}</span></div>
            <div class="flex max-w-full items-center gap-2 text-xs text-gray-500 dark:text-dark-400"><span class="truncate" :title="fingerprintCommit">{{ text.fingerprint }} {{ fingerprintCommit ? fingerprintCommit.slice(0, 12) : '—' }}</span><button type="button" class="shrink-0 font-medium text-primary-600 hover:underline disabled:opacity-50 dark:text-primary-400" :disabled="refreshingBank" @click="updateFingerprint">{{ text.updateBank }}</button></div>
          </div>
          <nav class="mb-5 flex gap-1 overflow-x-auto border-b border-gray-200 dark:border-dark-700" :aria-label="text.tabs">
            <button v-for="item in tabs" :key="item.value" type="button" class="shrink-0 border-b-2 px-3 py-3 text-sm font-medium transition sm:px-4" :class="activeTab === item.value ? 'border-primary-500 text-primary-700 dark:text-primary-400' : 'border-transparent text-gray-500 hover:text-gray-800 dark:text-dark-400 dark:hover:text-white'" :aria-current="activeTab === item.value ? 'page' : undefined" @click="switchTab(item.value)">{{ item.label }}</button>
          </nav>

          <section v-if="activeTab === 'tickets'" :aria-label="text.current">
            <div v-if="!statuses.length" class="rounded-xl border border-gray-200 bg-white p-10 text-center text-sm text-gray-500 dark:border-dark-700 dark:bg-dark-800">{{ text.noModels }}</div>
            <div v-else class="grid gap-3 md:grid-cols-2">
              <article v-for="status in statuses" :key="status.model" class="rounded-xl border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-800 sm:p-5">
                <div class="flex flex-wrap items-center justify-between gap-2"><h3 class="min-w-0 break-all font-mono text-sm font-semibold text-gray-900 dark:text-white">{{ status.model }}</h3><span class="rounded-md px-2 py-1 text-xs font-medium" :class="statusClass(status)">{{ statusLabel(status) }}</span></div>
                <dl class="mt-4 grid grid-cols-2 gap-x-3 gap-y-3 text-xs">
                  <div><dt class="text-gray-500 dark:text-dark-400">{{ text.acquired }}</dt><dd class="mt-1 text-gray-800 dark:text-dark-200">{{ formatTime(status.captured_at) }}</dd></div>
                  <div><dt class="text-gray-500 dark:text-dark-400">{{ text.length }}</dt><dd class="mt-1 font-medium tabular-nums text-gray-800 dark:text-dark-200">{{ status.length ?? '—' }}</dd></div>
                  <div><dt class="text-gray-500 dark:text-dark-400">turn-state</dt><dd class="mt-1 text-gray-800 dark:text-dark-200">{{ status.turn_state_present ? text.present : text.absent }}</dd></div>
                  <div><dt class="text-gray-500 dark:text-dark-400">Cookie</dt><dd class="mt-1 text-gray-800 dark:text-dark-200">{{ status.cookie_present ? text.present : text.absent }}</dd></div>
                </dl>
                <button type="button" class="btn btn-secondary mt-4 !px-3 !py-1.5 !text-xs" :disabled="busyModel !== '' || !status.harvest_enabled" @click="harvestModel(status.model)">{{ busyModel === status.model ? text.running : text.manual }}</button>
              </article>
            </div>
          </section>

          <section v-else-if="activeTab === 'attempts' || activeTab === 'invalidations'" :aria-label="activeTab === 'attempts' ? text.attempts : text.invalidations" class="rounded-xl border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800">
            <div class="border-b border-gray-100 p-4 dark:border-dark-700 sm:p-5">
              <h3 class="font-semibold text-gray-900 dark:text-white">{{ activeTab === 'attempts' ? text.attempts : text.invalidations }}</h3>
              <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ activeTab === 'attempts' ? text.attemptsDesc : text.invalidationsDesc }}</p>
              <div class="mt-4 flex flex-wrap items-center gap-2">
                <div v-if="activeTab === 'attempts'" class="w-full sm:w-40"><Select v-model="filter" :options="attemptOptions" :aria-label="text.filter" /></div>
                <div class="w-full sm:w-48"><Select v-model="modelFilter" :options="modelOptions" searchable :aria-label="text.model" /></div>
                <DateRangePicker v-model:start-date="startDay" v-model:end-date="endDay" />
              </div>
            </div>
            <div v-if="loading" role="status" class="p-10 text-center text-sm text-gray-500">{{ text.loading }}</div>
            <div v-else-if="!events.length" class="p-10 text-center text-sm text-gray-500">{{ text.empty }}</div>
            <div v-else class="divide-y divide-gray-100 dark:divide-dark-700">
              <template v-for="event in events" :key="`${event.kind}-${event.id}`">
                <div v-if="isNewDay(event)" class="bg-gray-50 px-5 py-2 text-xs font-semibold text-gray-500 dark:bg-dark-900/50">{{ formatDay(event.occurred_at) }}</div>
                <button type="button" class="flex w-full flex-col gap-2 px-4 py-3 text-left transition hover:bg-gray-50 dark:hover:bg-dark-700/60 sm:flex-row sm:items-center sm:gap-4 sm:px-5" @click="openDetail(event)">
                  <span class="shrink-0 self-start rounded-md px-2 py-1 text-xs font-medium" :class="eventBadge(event.kind)">{{ kindLabel(event.kind) }}</span>
                  <span class="min-w-0 flex-1"><span class="block break-all font-mono text-xs font-semibold text-gray-900 dark:text-white">{{ event.model }} <span v-if="event.kind !== 'invalidation' && !event.verification_method" class="font-sans font-normal text-amber-600">{{ text.legacy }}</span></span><span class="mt-1 block break-words text-xs text-gray-500 dark:text-dark-400">{{ eventSummary(event) }}</span></span>
                  <time class="shrink-0 text-xs tabular-nums text-gray-500 dark:text-dark-400">{{ formatTime(event.occurred_at, true) }}</time>
                </button>
              </template>
            </div>
            <div class="flex flex-wrap items-center justify-between gap-3 border-t border-gray-100 px-5 py-3 text-xs text-gray-500 dark:border-dark-700"><span>{{ total }} {{ text.records }}</span><div class="flex items-center gap-2"><button type="button" class="btn btn-secondary !px-2 !py-1" :disabled="page <= 1 || loading" @click="page--">←</button><span>{{ page }} / {{ Math.max(1, Math.ceil(total / pageSize)) }}</span><button type="button" class="btn btn-secondary !px-2 !py-1" :disabled="page * pageSize >= total || loading" @click="page++">→</button></div></div>
          </section>

          <section v-else class="space-y-4" :aria-label="text.diagnostic">
            <div class="rounded-xl border border-gray-200 bg-white p-5 dark:border-dark-700 dark:bg-dark-800 sm:p-6">
              <h3 class="font-semibold text-gray-900 dark:text-white">{{ text.setup }}</h3><p class="mt-1 text-sm text-gray-500 dark:text-dark-400">{{ text.diagnosticDesc }}</p>
              <div class="mt-5 max-w-md"><label class="mb-2 block text-xs font-semibold text-gray-700 dark:text-dark-200">{{ text.key }}</label><Select v-model="selectedKey" :options="keyOptions" searchable :disabled="diagnosing" :aria-label="text.key" /></div>
              <p v-if="!keys.length" class="mt-2 text-xs text-amber-600">{{ text.noKey }}</p>
              <div class="mt-6"><h4 class="mb-1 text-xs font-semibold text-gray-700 dark:text-dark-200">{{ text.chooseModels }}</h4><p class="mb-3 text-xs text-gray-500 dark:text-dark-400">{{ text.modelHint }}</p><CodexDiagnosticModelPicker v-model="selectedModels" :models="models" :ticket-models="statuses.map(status => status.model)" :disabled="diagnosing" /></div>
              <div class="mt-5 flex flex-wrap items-center gap-3"><button type="button" class="btn btn-primary" :disabled="diagnosing || !selectedKey || !selectedModels.length" @click="runDiagnostic">{{ diagnosing ? text.running : `${text.start} ${selectedModels.length} ${text.count}` }}</button><button v-if="diagnosing" type="button" class="btn btn-secondary" @click="cancelDiagnostic">{{ text.cancel }}</button><span class="text-xs text-gray-500 dark:text-dark-400">{{ text.paid }}</span></div>
            </div>
            <div v-if="diagnosing || results.length" class="rounded-xl border border-gray-200 bg-white p-5 dark:border-dark-700 dark:bg-dark-800 sm:p-6" aria-live="polite">
              <div class="flex flex-wrap items-center justify-between gap-2"><h3 class="font-semibold text-gray-900 dark:text-white">{{ text.results }}</h3><span class="text-xs text-gray-500">{{ results.length }} / {{ diagnosticTotal }} {{ text.completed }}</span></div>
              <p v-if="diagnosing" role="status" class="mt-2 text-sm text-primary-600 dark:text-primary-400">{{ text.testing }} {{ currentDiagnostic }}</p>
              <div v-if="results.length" class="mt-4 divide-y divide-gray-100 dark:divide-dark-700">
                <div v-for="result in results" :key="result.model" class="flex flex-col gap-2 py-3 sm:flex-row sm:items-start sm:justify-between sm:gap-4"><span class="break-all font-mono text-sm font-medium text-gray-900 dark:text-white">{{ result.model }}</span><div class="min-w-0 text-xs sm:text-right"><span class="rounded-md px-2 py-1 font-medium" :class="diagnosticBadge(result.status)">{{ resultLabel(result.status) }}</span><p v-if="result.predicted_model" class="mt-2 break-words text-gray-600 dark:text-dark-300">top-1 {{ result.predicted_model }} · {{ result.probability == null ? '—' : `${(result.probability * 100).toFixed(1)}%` }}</p><p v-if="result.reason || result.gateway_error_code || result.http_status" class="mt-2 break-words text-gray-500 dark:text-dark-400">{{ [result.reason, result.gateway_error_code, result.http_status ? `HTTP ${result.http_status}` : ''].filter(Boolean).join(' · ') }}</p></div></div>
              </div>
            </div>
          </section>
        </div>
      </section>

      <div v-if="detail" class="fixed inset-0 z-[10001] flex items-center justify-center bg-gray-950/70 p-3" @click.self="closeDetail">
        <section role="dialog" aria-modal="true" :aria-label="text.details" class="max-h-[90vh] w-full max-w-4xl overflow-y-auto rounded-2xl bg-white p-5 shadow-2xl dark:bg-dark-800 sm:p-7">
          <div class="flex items-center justify-between gap-3"><h3 class="break-all text-lg font-semibold text-gray-900 dark:text-white">{{ text.details }} · {{ detail.model }}</h3><button type="button" class="p-2 text-gray-500" :aria-label="text.close" @click="closeDetail">✕</button></div>
          <p class="mt-1 text-xs text-gray-500">{{ formatTime(detail.occurred_at) }} · {{ kindLabel(detail.kind) }}</p>
          <div v-if="detail.kind === 'invalidation'" class="mt-5">
            <p class="mb-3 text-xs text-gray-500">{{ detail.request_kind }} · {{ detail.request_route }} · HTTP {{ detail.response_http_status ?? '—' }}</p>
            <div v-if="detailLoading" class="text-sm text-gray-500">{{ text.loading }}</div>
            <div v-else-if="detailError" class="text-sm text-red-600">{{ detailError }}</div>
            <div v-else-if="rawDetail" class="grid gap-4 md:grid-cols-2"><div v-for="field in credentialFields" :key="field.label" class="min-w-0 rounded-xl border border-gray-200 p-4 dark:border-dark-600"><div class="flex items-center justify-between gap-2"><span class="text-xs font-semibold text-gray-500">{{ field.label }}</span><button v-if="field.value" type="button" class="text-xs font-semibold text-primary-600" @click="copy(field.value)">{{ text.copy }}</button></div><pre class="mt-2 max-h-40 overflow-auto whitespace-pre-wrap break-all font-mono text-xs text-gray-800 dark:text-dark-200">{{ field.value || (field.original ? text.notCarried : text.notReturned) }}</pre></div></div>
            <div v-if="rawDetail?.returned_set_cookies?.length" class="mt-4 rounded-xl border border-gray-200 p-4 dark:border-dark-600"><h4 class="text-xs font-semibold text-gray-500">Set-Cookie</h4><pre v-for="(cookie, index) in rawDetail.returned_set_cookies" :key="index" class="mt-2 overflow-auto whitespace-pre-wrap break-all font-mono text-xs">{{ cookie }}</pre></div>
          </div>
          <dl v-else class="mt-5 grid gap-3 text-sm sm:grid-cols-2"><div v-for="field in attemptFields" :key="field.label" class="rounded-lg bg-gray-50 p-3 dark:bg-dark-700"><dt class="text-xs text-gray-500 dark:text-dark-400">{{ field.label }}</dt><dd class="mt-1 break-all font-medium text-gray-900 dark:text-white">{{ field.value }}</dd></div></dl>
        </section>
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import * as tickets from '@/api/admin/codexTickets'
import Select from '@/components/common/Select.vue'
import DateRangePicker from '@/components/common/DateRangePicker.vue'
import CodexDiagnosticModelPicker from './CodexDiagnosticModelPicker.vue'
import { extractApiErrorMessage } from '@/utils/apiError'
import type { Account } from '@/types'

const props = defineProps<{ show: boolean; account: Account | null; initialDiagnostic?: boolean }>()
const emit = defineEmits<{ close: []; updated: [account: Account] }>()
const { locale } = useI18n()
const text = computed(() => locale.value.startsWith('zh') ? {
  title: '票据中心', retention: '流水保留 90 天 · 失效凭据仅管理员可查看', refresh: '刷新', close: '关闭',
  available: '可用票 / 参与模型', fingerprint: '指纹版本', updateBank: '校验并更新', tabs: '票据中心导航',
  current: '当前票据', attempts: '打票流水', invalidations: '票据过期历史', diagnostic: '降智检测',
  noModels: '当前没有参与打票的模型', length: '票据长度', acquired: '获取于', present: '已携带', absent: '未携带',
  ready: '可用', missing: '无票', paused: '暂停', manual: '手动打票', running: '运行中…',
  attemptsDesc: '成功、未命中与执行错误只在这里展示，不代表当前票失效。', invalidationsDesc: '仅记录上游返回新 turn-state 的失效事件，不表示推算的 TTL 到期。',
  filter: '结果', model: '模型', all: '全部结果', success: '打票成功', failure: '失败与错误', invalidation: '票据失效', allModels: '全部模型',
  loading: '正在加载…', empty: '没有符合条件的记录（最多保留 90 天）', records: '条记录', legacy: '旧版长度校验',
  details: '流水详情', copy: '复制', notCarried: '未携带', notReturned: '上游未返回',
  setup: '创建一次检测', diagnosticDesc: '选择自己的可计费 API Key 与 GPT 模型。检测逐项走正式 /v1/responses 请求和计费链路。',
  key: '用于计费的 API Key', chooseKey: '选择 API Key', noKey: '没有可用的本人 API Key', chooseModels: '选择检测模型',
  modelHint: '可多选；无需打票的旧模型直接检测。', start: '开始检测', count: '个模型', cancel: '取消后续检测', paid: '正常计费',
  results: '检测结果', completed: '已完成', testing: '正在检测', normal: '正常', degraded: '疑似降智', uncertain: '不确定', failed: '失败', error: '操作失败'
} : {
  title: 'Ticket center', retention: 'Records retained for 90 days · credentials visible to admins only', refresh: 'Refresh', close: 'Close',
  available: 'Ready / models', fingerprint: 'Fingerprint version', updateBank: 'Verify & update', tabs: 'Ticket center navigation',
  current: 'Current tickets', attempts: 'Ticket attempts', invalidations: 'Ticket expiry history', diagnostic: 'Model degradation check',
  noModels: 'No models currently require tickets', length: 'Ticket length', acquired: 'Acquired', present: 'Present', absent: 'Absent',
  ready: 'Ready', missing: 'Missing', paused: 'Paused', manual: 'Harvest', running: 'Running…',
  attemptsDesc: 'Success, misses and errors are shown here; failed attempts do not mean the current ticket is invalid.', invalidationsDesc: 'Upstream returned a new turn-state; this is not an inferred TTL expiry.',
  filter: 'Result', model: 'Model', all: 'All results', success: 'Success', failure: 'Failures & errors', invalidation: 'Invalidation', allModels: 'All models',
  loading: 'Loading…', empty: 'No matching records (90-day retention)', records: 'records', legacy: 'Legacy length check',
  details: 'Event details', copy: 'Copy', notCarried: 'Not carried', notReturned: 'Not returned by upstream',
  setup: 'Create a check', diagnosticDesc: 'Choose your billable API key and GPT models. Each test runs through the real /v1/responses billing gateway.',
  key: 'Billing API key', chooseKey: 'Select API key', noKey: 'No eligible own API key', chooseModels: 'Select models',
  modelHint: 'Select multiple models; older models without tickets are checked directly.', start: 'Check', count: 'models', cancel: 'Cancel remaining checks', paid: 'Normal billing applies',
  results: 'Results', completed: 'completed', testing: 'Checking', normal: 'Normal', degraded: 'Possible degradation', uncertain: 'Uncertain', failed: 'Failed', error: 'Operation failed'
})
type Tab = 'tickets' | 'attempts' | 'invalidations' | 'diagnostic'
const activeTab = ref<Tab>('tickets')
const tabs = computed(() => [
  { value: 'tickets' as const, label: text.value.current },
  { value: 'attempts' as const, label: text.value.attempts },
  { value: 'invalidations' as const, label: text.value.invalidations },
  { value: 'diagnostic' as const, label: text.value.diagnostic }
])
const pageSize = 20
const page = ref(1)
const filter = ref('all')
const modelFilter = ref('')
const startDay = ref('')
const endDay = ref('')
const events = ref<tickets.TicketEvent[]>([])
const total = ref(0)
const loading = ref(false)
const error = ref('')
const fingerprintCommit = ref('')
const refreshingBank = ref(false)
const models = ref<string[]>([])
const accountDetail = ref<Account | null>(null)
const busyModel = ref('')
const keys = ref<Awaited<ReturnType<typeof tickets.ownKeys>>>([])
const selectedKey = ref<number | null>(null)
const selectedModels = ref<string[]>([])
const results = ref<tickets.TicketDiagnostic[]>([])
const diagnosticTotal = ref(0)
const currentDiagnostic = ref('')
const diagnosing = ref(false)
const cancelRequested = ref(false)
const controller = ref<AbortController | null>(null)
const detail = ref<tickets.TicketEvent | null>(null)
const rawDetail = ref<tickets.TicketInvalidation | null>(null)
const detailLoading = ref(false)
const detailError = ref('')
let loadSerial = 0
const statuses = computed(() => (accountDetail.value ?? props.account)?.codex_turn_tickets as tickets.TicketStatus[] | undefined ?? [])
const attemptOptions = computed(() => [
  { value: 'all', label: text.value.all }, { value: 'success', label: text.value.success }, { value: 'failure', label: text.value.failure }
])
const modelOptions = computed(() => [
  { value: '', label: text.value.allModels }, ...models.value.map(model => ({ value: model, label: model }))
])
const keyOptions = computed(() => [
  { value: null, label: text.value.chooseKey }, ...keys.value.map(key => ({ value: key.id, label: `${key.name} · #${key.id}` }))
])
function formatTime(value?: string, clockOnly = false) {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '—' : new Intl.DateTimeFormat(locale.value, { dateStyle: clockOnly ? undefined : 'short', timeStyle: 'medium' }).format(date)
}
function formatDay(value: string) { return new Intl.DateTimeFormat(locale.value, { dateStyle: 'full' }).format(new Date(value)) }
function isNewDay(event: tickets.TicketEvent) {
  const index = events.value.indexOf(event)
  return index === 0 || new Date(events.value[index - 1].occurred_at).toDateString() !== new Date(event.occurred_at).toDateString()
}
function statusClass(status: tickets.TicketStatus) {
  return status.ready ? 'bg-primary-50 text-primary-700 dark:bg-primary-900/30 dark:text-primary-300'
    : status.blocked || status.harvest_paused ? 'bg-amber-50 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
      : 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-dark-300'
}
function statusLabel(status: tickets.TicketStatus) { return status.ready ? text.value.ready : status.blocked || status.harvest_paused ? text.value.paused : text.value.missing }
function eventBadge(kind: string) {
  return kind === 'success' ? 'bg-primary-50 text-primary-700 dark:bg-primary-900/30 dark:text-primary-300'
    : kind === 'invalidation' ? 'bg-amber-50 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
      : 'bg-red-50 text-red-700 dark:bg-red-900/30 dark:text-red-300'
}
function kindLabel(kind: string) { return kind === 'success' ? text.value.success : kind === 'invalidation' ? text.value.invalidation : text.value.failure }
function resultLabel(status: tickets.TicketDiagnostic['status']) { return text.value[status] }
function diagnosticBadge(status: tickets.TicketDiagnostic['status']) {
  return status === 'normal' ? 'bg-primary-50 text-primary-700 dark:bg-primary-900/30 dark:text-primary-300'
    : status === 'degraded' ? 'bg-red-50 text-red-700 dark:bg-red-900/30 dark:text-red-300'
      : 'bg-amber-50 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
}
function eventSummary(event: tickets.TicketEvent) {
  return [event.reason_code, event.ticket_length != null ? `${text.value.length} ${event.ticket_length}` : '',
    (event.http_status ?? event.response_http_status) != null ? `HTTP ${event.http_status ?? event.response_http_status}` : '',
    event.duration_ms != null ? `${event.duration_ms}ms` : '', event.proxy_name,
    event.fingerprint_predicted_model ? `top-1 ${event.fingerprint_predicted_model}` : ''].filter(Boolean).join(' · ') || '—'
}
const credentialFields = computed(() => rawDetail.value ? [
  { label: 'Original turn-state', value: rawDetail.value.original_ticket, original: true },
  { label: 'Original Cookie', value: rawDetail.value.original_cookie, original: true },
  { label: 'Returned turn-state', value: rawDetail.value.returned_ticket, original: false },
  { label: 'Returned Cookie', value: rawDetail.value.returned_cookie, original: false }
] : [])
const attemptFields = computed(() => {
  const event = detail.value
  if (!event) return []
  return [
    { label: 'Outcome / source', value: `${event.kind} / ${event.trigger || '—'}` },
    { label: 'Turn-state length', value: String(event.ticket_length ?? '—') },
    { label: 'Credentials', value: `turn-state ${event.turn_state_present == null ? '—' : event.turn_state_present ? '✓' : '×'} · Cookie ${event.cookie_present == null ? '—' : event.cookie_present ? '✓' : '×'}` },
    { label: 'Challenge / parsed', value: `${event.challenge_expected_count ?? '—'} / ${event.parsed_number_count ?? '—'}` },
    { label: 'Top-1 / probability', value: `${event.fingerprint_predicted_model || '—'} / ${event.fingerprint_probability == null ? '—' : `${(event.fingerprint_probability * 100).toFixed(2)}%`}` },
    { label: 'Fingerprint / version', value: `${event.verification_method || text.value.legacy} · ${event.fingerprint_commit || '—'}` },
    { label: 'Reason / HTTP', value: `${event.reason_code || '—'} · ${event.http_status ?? '—'}` },
    { label: 'Duration / proxy', value: `${event.duration_ms ?? '—'} ms · ${event.proxy_name || '—'}` }
  ]
})
function switchTab(tab: Tab) {
  if (activeTab.value === tab) return
  activeTab.value = tab
  if (tab === 'diagnostic') openDiagnostic()
  if (tab === 'attempts' || tab === 'invalidations') { page.value = 1; loadEvents() }
}
async function loadEvents() {
  if (!props.account || (activeTab.value !== 'attempts' && activeTab.value !== 'invalidations')) return
  const serial = ++loadSerial
  loading.value = true
  error.value = ''
  try {
    const cutoff = new Date(Date.now() - 90 * 86400000)
    const requestedStart = startDay.value ? new Date(`${startDay.value}T00:00:00`) : undefined
    const start = requestedStart && requestedStart > cutoff ? requestedStart : cutoff
    const end = endDay.value ? new Date(new Date(`${endDay.value}T00:00:00`).getTime() + 86400000) : undefined
    const data = await tickets.events(props.account.id, {
      model: modelFilter.value || undefined,
      filter: activeTab.value === 'invalidations' ? 'invalidation' : filter.value === 'all' ? 'attempts' : filter.value,
      page: page.value, page_size: pageSize, start_time: start.toISOString(), end_time: end?.toISOString()
    })
    if (serial === loadSerial) { events.value = data.items ?? []; total.value = data.total }
  } catch (cause) {
    if (serial === loadSerial) error.value = extractApiErrorMessage(cause, text.value.error)
  } finally { if (serial === loadSerial) loading.value = false }
}
async function updateFingerprint() {
  refreshingBank.value = true
  error.value = ''
  try {
    const bank = await tickets.refreshFingerprint()
    fingerprintCommit.value = bank.commit
    models.value = bank.models
    await reload()
  } catch (cause) { error.value = extractApiErrorMessage(cause, text.value.error) }
  finally { refreshingBank.value = false }
}
async function reload() {
  if (!props.account) return
  const id = props.account.id
  try {
    const [bank, updated] = await Promise.all([tickets.fingerprint(), adminAPI.accounts.getById(id)])
    if (props.account?.id !== id || !props.show) return
    fingerprintCommit.value = bank.commit
    models.value = bank.models
    accountDetail.value = updated
    emit('updated', updated)
  } catch (cause) { error.value = extractApiErrorMessage(cause, text.value.error) }
  await loadEvents()
}
async function harvestModel(model: string) {
  if (!props.account) return
  busyModel.value = model
  error.value = ''
  try {
    const attempt = await tickets.harvest(props.account.id, model)
    await reload()
    if (attempt.outcome !== 'success') error.value = attempt.reason_code || attempt.outcome
  } catch (cause) { error.value = extractApiErrorMessage(cause, text.value.error) }
  finally { busyModel.value = '' }
}
async function openDiagnostic() {
  if (keys.value.length) return
  try {
    keys.value = (await tickets.ownKeys()).filter(key => key.status === 'active' &&
      (key.quota === 0 || key.quota_used < key.quota) && (!key.expires_at || new Date(key.expires_at) > new Date()))
  } catch (cause) { error.value = extractApiErrorMessage(cause, text.value.error) }
}
function cancelDiagnostic() { cancelRequested.value = true; controller.value?.abort() }
async function runDiagnostic() {
  if (!props.account || !selectedKey.value || !selectedModels.value.length) return
  const accountID = props.account.id
  diagnosing.value = true
  cancelRequested.value = false
  results.value = []
  diagnosticTotal.value = selectedModels.value.length
  error.value = ''
  try {
    for (const model of [...selectedModels.value]) {
      if (cancelRequested.value || !props.show || props.account?.id !== accountID) break
      currentDiagnostic.value = model
      controller.value = new AbortController()
      try {
        const data = await tickets.diagnose(accountID, selectedKey.value, [model], controller.value.signal)
        if (!cancelRequested.value && props.show && props.account?.id === accountID) results.value.push(...data.items)
      } catch (cause) {
        if (cancelRequested.value || controller.value.signal.aborted) break
        error.value = extractApiErrorMessage(cause, text.value.error)
        results.value.push({ model, status: 'failed', reason: error.value })
      } finally { controller.value = null }
    }
    if (props.show && props.account?.id === accountID) await reload()
  } finally { diagnosing.value = false; currentDiagnostic.value = '' }
}
async function openDetail(event: tickets.TicketEvent) {
  detail.value = event
  rawDetail.value = null
  detailError.value = ''
  if (event.kind !== 'invalidation' || !props.account) return
  detailLoading.value = true
  try {
    const data = await tickets.invalidation(props.account.id, event.id)
    if (detail.value === event) rawDetail.value = data
  } catch (cause) { detailError.value = extractApiErrorMessage(cause, text.value.error) }
  finally { detailLoading.value = false }
}
function closeDetail() { detail.value = null; rawDetail.value = null }
async function copy(value?: string | null) { if (value) await navigator.clipboard.writeText(value) }
watch(() => [props.show, props.account?.id], () => {
  if (!props.show || !props.account) { cancelDiagnostic(); ++loadSerial; closeDetail(); return }
  activeTab.value = props.initialDiagnostic ? 'diagnostic' : 'tickets'
  selectedKey.value = null
  selectedModels.value = []
  results.value = []
  page.value = 1
  filter.value = 'all'
  modelFilter.value = ''
  startDay.value = ''
  endDay.value = ''
  accountDetail.value = props.account
  keys.value = []
  reload()
  if (activeTab.value === 'diagnostic') openDiagnostic()
}, { immediate: true })
watch([filter, modelFilter, startDay, endDay], () => { if (activeTab.value !== 'attempts' && activeTab.value !== 'invalidations') return; page.value = 1; loadEvents() })
watch(page, loadEvents)
onUnmounted(cancelDiagnostic)
</script>
