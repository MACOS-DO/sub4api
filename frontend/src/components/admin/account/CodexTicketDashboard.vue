<template>
  <Teleport to="body">
    <div v-if="show && account" class="fixed inset-0 z-[10000] flex items-center justify-center bg-slate-950/65 p-2 backdrop-blur-sm sm:p-6" @click.self="emit('close')">
      <section role="dialog" aria-modal="true" :aria-label="text.title" class="flex max-h-[94vh] w-full max-w-6xl flex-col overflow-hidden rounded-2xl border border-slate-200 bg-slate-50 shadow-2xl dark:border-slate-700 dark:bg-slate-950">
        <header class="flex flex-wrap items-center justify-between gap-3 border-b border-slate-200 bg-white px-5 py-4 dark:border-slate-800 dark:bg-slate-900 sm:px-7">
          <div class="min-w-0">
            <div class="flex items-center gap-2 text-[11px] font-semibold uppercase tracking-[0.18em] text-indigo-600 dark:text-indigo-400">{{ text.eyebrow }} <span class="h-1 w-1 rounded-full bg-slate-400" /> {{ account.name }}</div>
            <h2 class="mt-1 text-xl font-bold text-slate-900 dark:text-white sm:text-2xl">{{ diagnosticMode ? text.diagnostic : text.title }}</h2>
            <p class="mt-1 text-xs text-slate-500 dark:text-slate-400">{{ text.retention }}</p>
          </div>
          <div class="flex items-center gap-2">
            <button class="rounded-lg border border-slate-200 px-3 py-2 text-xs font-semibold text-slate-600 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800" @click="reload">{{ text.refresh }}</button>
            <button class="rounded-lg p-2 text-slate-500 hover:bg-slate-100 dark:hover:bg-slate-800" :aria-label="text.close" @click="emit('close')">✕</button>
          </div>
        </header>
        <div class="overflow-y-auto p-4 sm:p-7">
          <div v-if="error" class="mb-4 rounded-lg border border-rose-200 bg-rose-50 p-3 text-sm text-rose-700 dark:border-rose-900 dark:bg-rose-950 dark:text-rose-300">{{ error }}</div>
          <div class="grid grid-cols-2 gap-2 sm:grid-cols-4 sm:gap-3">
            <div v-for="metric in metrics" :key="metric.label" class="rounded-xl border border-slate-200 bg-white p-3 dark:border-slate-800 dark:bg-slate-900 sm:p-4">
              <div class="text-[11px] font-semibold text-slate-500 dark:text-slate-400">{{ metric.label }}</div>
              <div class="mt-2 truncate text-lg font-bold text-slate-900 dark:text-white" :title="metric.value">{{ metric.value }}</div>
            </div>
          </div>
          <div class="mt-5 flex flex-wrap items-center justify-between gap-3">
            <div class="flex flex-wrap gap-2">
              <button :class="tabClass(!diagnosticMode)" @click="diagnosticMode = false">{{ text.timeline }}</button>
              <button :class="tabClass(diagnosticMode)" @click="openDiagnostic">{{ text.diagnostic }}</button>
            </div>
            <div class="flex max-w-full items-center gap-2"><span class="truncate font-mono text-[11px] text-slate-500 dark:text-slate-400" :title="fingerprintCommit">{{ text.fingerprint }} {{ fingerprintCommit ? fingerprintCommit.slice(0, 12) : '—' }}</span><button class="text-xs font-semibold text-indigo-600 disabled:opacity-50" :disabled="refreshingBank" @click="updateFingerprint">{{ text.updateBank }}</button></div>
          </div>
          <template v-if="!diagnosticMode">
            <div class="mt-4 grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
              <div v-for="model in models" :key="model" class="rounded-xl border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900">
                <div class="flex items-start justify-between gap-2"><span class="break-all font-mono text-xs font-bold text-slate-900 dark:text-white">{{ model }}</span><span :class="statusClass(statusFor(model))" class="shrink-0 rounded-md px-2 py-0.5 text-[11px] font-semibold">{{ statusLabel(statusFor(model)) }}</span></div>
                <p class="mt-3 text-xs text-slate-500 dark:text-slate-400">{{ text.length }} {{ statusFor(model)?.length ?? '—' }} · {{ text.acquired }} {{ formatTime(statusFor(model)?.captured_at) }}</p>
                <p class="mt-2 text-[11px] text-slate-500 dark:text-slate-400">turn-state {{ statusFor(model)?.turn_state_present ? '✓' : '—' }} · Cookie {{ statusFor(model)?.cookie_present ? '✓' : '—' }}</p>
                <p v-if="recentFailureByModel[model]" class="mt-2 truncate text-[11px] text-rose-600" :title="recentFailureByModel[model].reason_code">{{ text.cardFailure }} · {{ recentFailureByModel[model].reason_code || recentFailureByModel[model].kind }}</p>
                <button class="mt-3 rounded-lg border border-indigo-200 px-3 py-1.5 text-xs font-semibold text-indigo-700 hover:bg-indigo-50 disabled:opacity-50 dark:border-indigo-800 dark:text-indigo-300 dark:hover:bg-indigo-950" :disabled="busyModel !== '' || !statusFor(model)?.harvest_enabled" @click="harvestModel(model)">{{ busyModel === model ? text.running : text.manual }}</button>
              </div>
            </div>
            <section class="mt-5 overflow-hidden rounded-xl border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
              <div class="flex flex-wrap items-center justify-between gap-3 border-b border-slate-200 p-4 dark:border-slate-800 sm:px-5">
                <div><h3 class="font-semibold text-slate-900 dark:text-white">{{ text.timeline }}</h3><p class="text-xs text-slate-500">{{ text.retention }}</p></div>
                <div class="flex flex-wrap gap-2">
                  <select v-model="filter" :aria-label="text.filter" class="input !w-auto !py-1.5 text-xs"><option value="all">{{ text.all }}</option><option value="success">{{ text.success }}</option><option value="failure">{{ text.failure }}</option><option value="invalidation">{{ text.invalidation }}</option></select>
                  <select v-model="modelFilter" :aria-label="text.model" class="input !w-auto !py-1.5 text-xs"><option value="">{{ text.allModels }}</option><option v-for="model in models" :key="model" :value="model">{{ model }}</option></select>
                  <input v-model="startDay" type="date" :min="retentionDay" :max="todayDay" :aria-label="text.from" class="input !w-auto !py-1.5 text-xs" />
                  <input v-model="endDay" type="date" :min="retentionDay" :max="todayDay" :aria-label="text.to" class="input !w-auto !py-1.5 text-xs" />
                </div>
              </div>
              <div v-if="loading" class="p-10 text-center text-sm text-slate-500">{{ text.loading }}</div>
              <div v-else-if="!events.length" class="p-10 text-center text-sm text-slate-500">{{ text.empty }}</div>
              <div v-else class="divide-y divide-slate-100 dark:divide-slate-800">
                <template v-for="event in events" :key="`${event.kind}-${event.id}`">
                  <div v-if="isNewDay(event)" class="bg-slate-50 px-5 py-2 text-xs font-semibold text-slate-500 dark:bg-slate-950">{{ formatDay(event.occurred_at) }}</div>
                  <button class="flex w-full flex-wrap items-start gap-3 px-4 py-3 text-left hover:bg-slate-50 dark:hover:bg-slate-800/60 sm:flex-nowrap sm:px-5" @click="openDetail(event)">
                    <span :class="eventBadge(event.kind)" class="rounded-md px-2 py-1 text-[11px] font-semibold">{{ kindLabel(event.kind) }}</span>
                    <div class="min-w-0 flex-1"><div class="flex flex-wrap items-center gap-2"><span class="font-mono text-xs font-semibold text-slate-900 dark:text-white">{{ event.model }}</span><span v-if="event.kind !== 'invalidation' && !event.verification_method" class="text-[11px] text-amber-600">{{ text.legacy }}</span></div><p class="mt-1 break-words text-xs text-slate-500 dark:text-slate-400">{{ eventSummary(event) }}</p></div>
                    <span class="shrink-0 text-xs tabular-nums text-slate-500">{{ formatTime(event.occurred_at, true) }}</span>
                  </button>
                </template>
              </div>
              <div class="flex items-center justify-between gap-3 border-t border-slate-200 px-5 py-3 text-xs text-slate-500 dark:border-slate-800"><span>{{ total }} {{ text.records }}</span><div class="flex items-center gap-2"><button class="rounded border px-2 py-1 disabled:opacity-40 dark:border-slate-700" :disabled="page <= 1 || loading" @click="page--">←</button><span>{{ page }} / {{ Math.max(1, Math.ceil(total / pageSize)) }}</span><button class="rounded border px-2 py-1 disabled:opacity-40 dark:border-slate-700" :disabled="page * pageSize >= total || loading" @click="page++">→</button></div></div>
            </section>
          </template>
          <section v-else class="mt-4 rounded-xl border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900 sm:p-6">
            <p class="text-sm text-slate-600 dark:text-slate-300">{{ text.diagnosticDesc }}</p>
            <label class="mt-4 block text-xs font-semibold text-slate-500">{{ text.key }}</label>
            <select v-model.number="selectedKey" class="input mt-1 w-full max-w-md" :disabled="diagnosing"><option :value="0">{{ text.chooseKey }}</option><option v-for="key in keys" :key="key.id" :value="key.id">{{ key.name }} · #{{ key.id }}</option></select>
            <p v-if="!keys.length" class="mt-2 text-xs text-amber-600">{{ text.noKey }}</p>
            <div class="mt-4 grid gap-2 sm:grid-cols-2 lg:grid-cols-4"><label v-for="model in models" :key="model" class="flex cursor-pointer items-center gap-2 rounded-lg border border-slate-200 p-3 text-xs dark:border-slate-700"><input v-model="selectedModels" type="checkbox" :value="model" :disabled="diagnosing" /><span class="break-all font-mono">{{ model }}</span></label></div>
            <div class="mt-4 flex items-center gap-3"><button class="btn btn-primary" :disabled="diagnosing || !selectedKey || !selectedModels.length" @click="runDiagnostic">{{ diagnosing ? text.running : text.start }}</button><button v-if="diagnosing" class="btn btn-secondary" @click="controller?.abort()">{{ text.cancel }}</button><span class="text-xs text-slate-500">{{ text.paid }}</span></div>
            <div v-if="diagnosing" role="status" class="mt-4 text-sm text-indigo-600">{{ text.serialRunning }}</div>
            <div v-if="results.length" class="mt-5 space-y-2"><div v-for="result in results" :key="result.model" class="flex flex-wrap items-center justify-between gap-2 rounded-lg bg-slate-50 p-3 text-sm dark:bg-slate-800"><span class="font-mono">{{ result.model }}</span><span :class="result.status === 'normal' ? 'text-emerald-600' : result.status === 'degraded' ? 'text-rose-600' : 'text-amber-600'">{{ resultLabel(result.status) }} · {{ result.predicted_model || result.reason || '—' }} <span v-if="result.probability">({{ (result.probability * 100).toFixed(1) }}%)</span></span></div></div>
          </section>
        </div>
      </section>
      <div v-if="detail" class="fixed inset-0 z-[10001] flex items-center justify-center bg-slate-950/70 p-3" @click.self="closeDetail">
        <section role="dialog" aria-modal="true" :aria-label="text.details" class="max-h-[90vh] w-full max-w-4xl overflow-y-auto rounded-2xl bg-white p-5 shadow-2xl dark:bg-slate-900 sm:p-7">
          <div class="flex items-center justify-between gap-3"><h3 class="text-lg font-bold text-slate-900 dark:text-white">{{ text.details }} · {{ detail.model }}</h3><button class="p-2 text-slate-500" @click="closeDetail">✕</button></div>
          <p class="mt-1 text-xs text-slate-500">{{ formatTime(detail.occurred_at) }} · {{ kindLabel(detail.kind) }}</p>
          <div v-if="detail.kind === 'invalidation'" class="mt-5">
            <p class="mb-3 text-xs text-slate-500">{{ detail.request_kind }} · {{ detail.request_route }} · HTTP {{ detail.response_http_status ?? '—' }}</p>
            <div v-if="detailLoading" class="text-sm text-slate-500">{{ text.loading }}</div>
            <div v-else-if="detailError" class="text-sm text-rose-600">{{ detailError }}</div>
            <div v-else-if="rawDetail" class="grid gap-4 md:grid-cols-2"><div v-for="field in credentialFields" :key="field.label" class="min-w-0 rounded-xl border border-slate-200 p-4 dark:border-slate-700"><div class="flex items-center justify-between gap-2"><span class="text-xs font-semibold text-slate-500">{{ field.label }}</span><button v-if="field.value" class="text-xs font-semibold text-indigo-600" @click="copy(field.value)">{{ text.copy }}</button></div><pre class="mt-2 max-h-40 overflow-auto whitespace-pre-wrap break-all font-mono text-xs text-slate-800 dark:text-slate-200">{{ field.value || (field.original ? text.notCarried : text.notReturned) }}</pre></div></div>
            <div v-if="rawDetail?.returned_set_cookies?.length" class="mt-4 rounded-xl border border-slate-200 p-4 dark:border-slate-700"><h4 class="text-xs font-semibold text-slate-500">Set-Cookie</h4><pre v-for="(cookie, index) in rawDetail.returned_set_cookies" :key="index" class="mt-2 overflow-auto whitespace-pre-wrap break-all font-mono text-xs">{{ cookie }}</pre></div>
          </div>
          <dl v-else class="mt-5 grid gap-3 text-sm sm:grid-cols-2"><div v-for="field in attemptFields" :key="field.label" class="rounded-lg bg-slate-50 p-3 dark:bg-slate-800"><dt class="text-xs text-slate-500">{{ field.label }}</dt><dd class="mt-1 break-all font-medium text-slate-900 dark:text-white">{{ field.value }}</dd></div></dl>
        </section>
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { computed, ref, watch, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import * as tickets from '@/api/admin/codexTickets'
import { extractApiErrorMessage } from '@/utils/apiError'
import type { Account } from '@/types'

const props = defineProps<{ show: boolean; account: Account | null; initialDiagnostic?: boolean }>()
const emit = defineEmits<{ close: []; updated: [account: Account] }>()
const { locale } = useI18n()
const text = computed(() => locale.value.startsWith('zh') ? {
  eyebrow: 'CODEX · 票据中心', title: '票据与流水', diagnostic: '降智检测', retention: '流水仅保留最近 90 天 · 失效凭据仅管理员可查看', refresh: '刷新', close: '关闭',
  available: '可用票 / 模型', lastSuccess: '最近成功', lastFailure: '最近失败', cardFailure: '近期失败流水', lastInvalidation: '最近失效', timeline: '事件流水', fingerprint: '指纹版本', updateBank: '校验并更新', length: '长度', acquired: '获取于', ready: '可用', missing: '无票', paused: '暂停', manual: '手动打票', running: '运行中…', filter: '事件类型', model: '模型', from: '开始日期', to: '结束日期', all: '全部', success: '打票成功', failure: '失败与错误', invalidation: '票据失效', allModels: '全部模型', loading: '正在加载…', empty: '没有符合条件的记录（最多保留 90 天）', records: '条记录', legacy: '旧版长度校验', details: '流水详情', copy: '复制', notCarried: '未携带', notReturned: '上游未返回', diagnosticDesc: '选择自己的可计费 API Key 与 GPT 模型，按顺序通过正式 /v1/responses 网关检测。缺票会先打票；失效时不自动重放付费请求。', key: '用于计费的 API Key', chooseKey: '选择 API Key', noKey: '没有可用的本人 API Key', start: '开始串行检测', cancel: '取消后续检测', paid: '检测将正常计费', serialRunning: '正在串行检测，请勿关闭窗口…', normal: '正常', degraded: '疑似降智', uncertain: '不确定', failed: '失败', error: '操作失败'
} : {
  eyebrow: 'CODEX · TICKET CENTER', title: 'Tickets & activity', diagnostic: 'Model degradation check', retention: 'Records are retained for 90 days · credentials visible to admins only', refresh: 'Refresh', close: 'Close',
  available: 'Ready / models', lastSuccess: 'Last success', lastFailure: 'Last failure', cardFailure: 'Recent failed attempt', lastInvalidation: 'Last invalidation', timeline: 'Activity', fingerprint: 'Fingerprint version', updateBank: 'Verify & update', length: 'Length', acquired: 'Acquired', ready: 'Ready', missing: 'Missing', paused: 'Paused', manual: 'Harvest', running: 'Running…', filter: 'Event type', model: 'Model', from: 'From', to: 'To', all: 'All', success: 'Success', failure: 'Failures', invalidation: 'Invalidation', allModels: 'All models', loading: 'Loading…', empty: 'No matching records (90-day retention)', records: 'records', legacy: 'Legacy length check', details: 'Event details', copy: 'Copy', notCarried: 'Not carried', notReturned: 'Not returned by upstream', diagnosticDesc: 'Select your billable API key and GPT models. Tests run serially through the real /v1/responses gateway. Missing tickets are harvested first; a paid request is not replayed after invalidation.', key: 'Billing API key', chooseKey: 'Select API key', noKey: 'No eligible own API key', start: 'Start serial check', cancel: 'Cancel remaining tests', paid: 'Normal usage billing applies', serialRunning: 'Testing serially; keep this window open…', normal: 'Normal', degraded: 'Possible degradation', uncertain: 'Uncertain', failed: 'Failed', error: 'Operation failed'
})
const pageSize = 20
const page = ref(1)
const filter = ref('all')
const modelFilter = ref('')
const startDay = ref('')
const endDay = ref('')
const todayDay = new Date().toISOString().slice(0, 10)
const retentionDay = new Date(Date.now() - 90 * 86400000).toISOString().slice(0, 10)
const events = ref<tickets.TicketEvent[]>([])
const total = ref(0)
const loading = ref(false)
const error = ref('')
const fingerprintCommit = ref('')
const refreshingBank = ref(false)
const recentEvents = ref<tickets.TicketEvent[]>([])
const latestByKind = ref<Record<string, string>>({})
const recentFailureByModel = computed(() => Object.fromEntries(recentEvents.value.filter(event => event.kind === 'miss' || event.kind === 'error').map(event => [event.model, event])))
const models = ref<string[]>([])
const accountDetail = ref<Account | null>(null)
const busyModel = ref('')
const diagnosticMode = ref(false)
const keys = ref<Awaited<ReturnType<typeof tickets.ownKeys>>>([])
const selectedKey = ref(0)
const selectedModels = ref<string[]>([])
const results = ref<tickets.TicketDiagnostic[]>([])
const diagnosing = ref(false)
const controller = ref<AbortController | null>(null)
const detail = ref<tickets.TicketEvent | null>(null)
const rawDetail = ref<tickets.TicketInvalidation | null>(null)
const detailLoading = ref(false)
const detailError = ref('')
let loadSerial = 0
const statuses = computed(() => (accountDetail.value ?? props.account)?.codex_turn_tickets as tickets.TicketStatus[] | undefined ?? [])
const statusFor = (model: string) => statuses.value.find(status => status.model === model)
const metrics = computed(() => [
  { label: text.value.available, value: `${statuses.value.filter(status => status.ready).length} / ${models.value.length}` },
  { label: text.value.lastSuccess, value: formatTime(latestByKind.value.success) },
  { label: text.value.lastFailure, value: formatTime(latestByKind.value.failure) },
  { label: text.value.lastInvalidation, value: formatTime(latestByKind.value.invalidation) }
])
function formatTime(value?: string, clockOnly = false) { if (!value) return '—'; const date = new Date(value); return Number.isNaN(date.getTime()) ? '—' : new Intl.DateTimeFormat(locale.value, { dateStyle: clockOnly ? undefined : 'short', timeStyle: 'medium' }).format(date) }
function formatDay(value: string) { return new Intl.DateTimeFormat(locale.value, { dateStyle: 'full' }).format(new Date(value)) }
function isNewDay(event: tickets.TicketEvent) { const index = events.value.indexOf(event); return index === 0 || new Date(events.value[index - 1].occurred_at).toDateString() !== new Date(event.occurred_at).toDateString() }
function statusClass(status?: tickets.TicketStatus) { return status?.ready ? 'bg-emerald-50 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-300' : status?.blocked || status?.harvest_paused ? 'bg-amber-50 text-amber-700 dark:bg-amber-950 dark:text-amber-300' : 'bg-slate-100 text-slate-600 dark:bg-slate-800 dark:text-slate-400' }
function statusLabel(status?: tickets.TicketStatus) { return status?.ready ? text.value.ready : status?.blocked || status?.harvest_paused ? text.value.paused : text.value.missing }
function tabClass(active: boolean) { return active ? 'rounded-lg bg-indigo-600 px-3 py-2 text-xs font-semibold text-white' : 'rounded-lg border border-slate-200 px-3 py-2 text-xs font-semibold text-slate-600 dark:border-slate-700 dark:text-slate-300' }
function eventBadge(kind: string) { return kind === 'success' ? 'bg-emerald-50 text-emerald-700 dark:bg-emerald-950 dark:text-emerald-300' : kind === 'invalidation' ? 'bg-violet-50 text-violet-700 dark:bg-violet-950 dark:text-violet-300' : 'bg-rose-50 text-rose-700 dark:bg-rose-950 dark:text-rose-300' }
function kindLabel(kind: string) { return kind === 'success' ? text.value.success : kind === 'invalidation' ? text.value.invalidation : text.value.failure }
function resultLabel(status: tickets.TicketDiagnostic['status']) { return text.value[status] }
function eventSummary(event: tickets.TicketEvent) { return [event.reason_code, event.ticket_length != null ? `${text.value.length} ${event.ticket_length}` : '', event.http_status ?? event.response_http_status ? `HTTP ${event.http_status ?? event.response_http_status}` : '', event.duration_ms != null ? `${event.duration_ms}ms` : '', event.proxy_name, event.fingerprint_predicted_model ? `top-1 ${event.fingerprint_predicted_model}` : ''].filter(Boolean).join(' · ') || '—' }
const credentialFields = computed(() => rawDetail.value ? [
  { label: 'Original turn-state', value: rawDetail.value.original_ticket, original: true },
  { label: 'Original Cookie', value: rawDetail.value.original_cookie, original: true },
  { label: 'Returned turn-state', value: rawDetail.value.returned_ticket, original: false },
  { label: 'Returned Cookie', value: rawDetail.value.returned_cookie, original: false }
] : [])
const attemptFields = computed(() => { const event = detail.value; if (!event) return []; return [
  { label: 'Outcome / source', value: `${event.kind} / ${event.trigger || '—'}` }, { label: 'Turn-state length', value: String(event.ticket_length ?? '—') },
  { label: 'Credentials', value: `turn-state ${event.turn_state_present == null ? '—' : event.turn_state_present ? '✓' : '×'} · Cookie ${event.cookie_present == null ? '—' : event.cookie_present ? '✓' : '×'}` },
  { label: 'Challenge / parsed', value: `${event.challenge_expected_count ?? '—'} / ${event.parsed_number_count ?? '—'}` },
  { label: 'Top-1 / probability', value: `${event.fingerprint_predicted_model || '—'} / ${event.fingerprint_probability == null ? '—' : `${(event.fingerprint_probability * 100).toFixed(2)}%`}` },
  { label: 'Fingerprint / version', value: `${event.verification_method || text.value.legacy} · ${event.fingerprint_commit || '—'}` },
  { label: 'Reason / HTTP', value: `${event.reason_code || '—'} · ${event.http_status ?? '—'}` },
  { label: 'Duration / proxy', value: `${event.duration_ms ?? '—'} ms · ${event.proxy_name || '—'}` }
] })
async function loadEvents() { if (!props.account) return; const serial = ++loadSerial; loading.value = true; error.value = ''; try { const data = await tickets.events(props.account.id, { model: modelFilter.value || undefined, filter: filter.value, page: page.value, page_size: pageSize, start_time: startDay.value ? new Date(`${startDay.value}T00:00:00`).toISOString() : undefined, end_time: endDay.value ? new Date(new Date(`${endDay.value}T00:00:00`).getTime() + 86400000).toISOString() : undefined }); if (serial === loadSerial) { events.value = data.items ?? []; total.value = data.total } } catch (cause) { if (serial === loadSerial) error.value = extractApiErrorMessage(cause, text.value.error) } finally { if (serial === loadSerial) loading.value = false } }
async function updateFingerprint() { refreshingBank.value = true; error.value = ''; try { const bank = await tickets.refreshFingerprint(); fingerprintCommit.value = bank.commit; models.value = bank.models } catch (cause) { error.value = extractApiErrorMessage(cause, text.value.error) } finally { refreshingBank.value = false } }
async function loadRecent(id: number) {
  try {
    const filters = ['all', 'success', 'failure', 'invalidation'] as const
    const results = await Promise.all(filters.map(filter => tickets.events(id, { page: 1, page_size: filter === 'all' ? 100 : 1, filter })))
    if (props.account?.id !== id) return
    recentEvents.value = results[0].items || []
    latestByKind.value = Object.fromEntries(filters.slice(1).map((filter, index) => [filter, results[index + 1].items?.[0]?.occurred_at || '']))
  } catch { latestByKind.value = {} }
}
async function reload() { if (!props.account) return; const id = props.account.id; try { const [bank, updated] = await Promise.all([tickets.fingerprint(), adminAPI.accounts.getById(id)]); if (props.account?.id !== id) return; fingerprintCommit.value = bank.commit; models.value = bank.models; accountDetail.value = updated; emit('updated', updated) } catch (cause) { error.value = extractApiErrorMessage(cause, text.value.error) } await Promise.all([loadEvents(), loadRecent(id)]) }
async function harvestModel(model: string) { if (!props.account) return; busyModel.value = model; try { const attempt = await tickets.harvest(props.account.id, model); await reload(); if (attempt.outcome !== 'success') error.value = attempt.reason_code || attempt.outcome } catch (cause) { error.value = extractApiErrorMessage(cause, text.value.error) } finally { busyModel.value = '' } }
async function openDiagnostic() { diagnosticMode.value = true; if (keys.value.length) return; try { keys.value = (await tickets.ownKeys()).filter(key => key.status === 'active' && (key.quota === 0 || key.quota_used < key.quota) && (!key.expires_at || new Date(key.expires_at) > new Date())); } catch (cause) { error.value = extractApiErrorMessage(cause, text.value.error) } }
async function runDiagnostic() { if (!props.account || !selectedKey.value || !selectedModels.value.length) return; diagnosing.value = true; results.value = []; error.value = ''; controller.value = new AbortController(); try { const data = await tickets.diagnose(props.account.id, selectedKey.value, selectedModels.value, controller.value.signal); results.value = data.items; await reload() } catch (cause) { if (!controller.value.signal.aborted) error.value = extractApiErrorMessage(cause, text.value.error) } finally { diagnosing.value = false; controller.value = null } }
async function openDetail(event: tickets.TicketEvent) { detail.value = event; rawDetail.value = null; detailError.value = ''; if (event.kind !== 'invalidation' || !props.account) return; detailLoading.value = true; try { const data = await tickets.invalidation(props.account.id, event.id); if (detail.value === event) rawDetail.value = data } catch (cause) { detailError.value = extractApiErrorMessage(cause, text.value.error) } finally { detailLoading.value = false } }
function closeDetail() { detail.value = null; rawDetail.value = null }
async function copy(value?: string | null) { if (value) await navigator.clipboard.writeText(value) }
watch(() => [props.show, props.account?.id], () => { if (!props.show || !props.account) { controller.value?.abort(); closeDetail(); return } diagnosticMode.value = !!props.initialDiagnostic; selectedModels.value = []; results.value = []; page.value = 1; filter.value = 'all'; modelFilter.value = ''; startDay.value = ''; endDay.value = ''; accountDetail.value = props.account; reload(); if (diagnosticMode.value) openDiagnostic() }, { immediate: true })
watch([filter, modelFilter, startDay, endDay], () => { page.value = 1; loadEvents() })
watch(page, loadEvents)
onUnmounted(() => controller.value?.abort())
</script>
