<template>
  <Teleport to="body">
    <div v-if="show && account" class="fixed inset-0 z-[10000] flex items-center justify-center bg-gray-950/60 p-2 backdrop-blur-sm sm:p-6" @click.self="emit('close')">
      <section role="dialog" aria-modal="true" :aria-label="text.title" :class="activeTab === 'diagnostic' ? 'h-[min(860px,94dvh)]' : ''" class="flex max-h-[94dvh] w-full max-w-5xl flex-col overflow-hidden rounded-2xl border border-gray-200 bg-gray-50 shadow-2xl dark:border-dark-600 dark:bg-dark-900">
        <header class="flex items-start justify-between gap-3 border-b border-gray-200 bg-white px-5 py-4 dark:border-dark-700 dark:bg-dark-800 sm:px-7 sm:py-5">
          <div class="min-w-0 flex-1">
            <p class="truncate text-xs font-semibold text-primary-600 dark:text-primary-400">{{ account.name }} · CODEX</p>
            <h2 class="mt-1 text-xl font-semibold text-gray-900 dark:text-white">{{ text.title }}</h2>
            <p v-if="activeTab !== 'diagnostic'" class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ text.retention }}</p>
          </div>
          <div class="flex shrink-0 items-center gap-2">
            <button type="button" class="btn btn-secondary !px-3 !py-1.5 !text-xs" :disabled="loading" @click="reload">{{ text.refresh }}</button>
            <button type="button" class="rounded-lg p-2 text-gray-500 hover:bg-gray-100 dark:hover:bg-dark-700" :aria-label="text.close" @click="emit('close')">✕</button>
          </div>
        </header>
        <nav class="flex shrink-0 gap-1 overflow-x-auto border-b border-gray-200 bg-white px-3 dark:border-dark-700 dark:bg-dark-800 sm:px-6" :aria-label="text.tabs">
          <button v-for="item in tabs" :key="item.value" type="button" class="shrink-0 border-b-2 px-2.5 py-3 text-xs font-medium transition sm:px-4 sm:text-sm" :class="activeTab === item.value ? 'border-primary-500 text-primary-700 dark:text-primary-400' : 'border-transparent text-gray-500 hover:text-gray-800 dark:text-dark-400 dark:hover:text-white'" :aria-current="activeTab === item.value ? 'page' : undefined" @click="switchTab(item.value)">{{ item.label }}</button>
        </nav>
        <div ref="scrollViewport" class="min-h-0 flex-1 overflow-y-auto" :class="activeTab === 'diagnostic' ? '' : 'px-4 py-5 sm:px-7'">
          <div v-if="error" role="alert" class="mb-4 rounded-lg border border-red-200 bg-red-50 p-3 text-sm text-red-700 dark:border-red-900 dark:bg-red-900/20 dark:text-red-300">{{ error }}</div>
          <div v-if="activeTab !== 'diagnostic'" class="mb-5 flex flex-wrap items-center justify-between gap-3 rounded-xl border border-gray-200 bg-white px-4 py-3 dark:border-dark-700 dark:bg-dark-800">
            <div class="flex items-baseline gap-2"><strong class="text-2xl font-semibold tabular-nums text-primary-600 dark:text-primary-400">{{ statuses.filter(status => status.ready).length }} / {{ statuses.length }}</strong><span class="text-sm text-gray-600 dark:text-dark-300">{{ text.available }}</span></div>
            <div class="flex max-w-full items-center gap-2 text-xs text-gray-500 dark:text-dark-400"><span class="truncate" :title="fingerprintCommit">{{ text.fingerprint }} {{ fingerprintCommit ? fingerprintCommit.slice(0, 12) : '—' }}</span><button type="button" class="shrink-0 font-medium text-primary-600 hover:underline disabled:opacity-50 dark:text-primary-400" :disabled="refreshingBank" @click="updateFingerprint">{{ text.updateBank }}</button></div>
          </div>

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

          <section v-else :aria-label="text.diagnostic" class="min-h-full">
            <div v-if="diagnosticStage === 'setup'" class="mx-auto w-full max-w-3xl px-4 py-5 sm:px-7 sm:py-6">
              <div class="mb-5 flex flex-wrap items-start justify-between gap-2">
                <div><h3 ref="setupHeading" tabindex="-1" class="text-lg font-semibold text-gray-900 outline-none dark:text-white">{{ text.setup }}</h3><p class="mt-1 text-xs leading-5 text-gray-500 dark:text-dark-400">{{ text.diagnosticDesc }}</p></div>
                <button type="button" class="text-[11px] text-gray-500 hover:text-primary-600 disabled:opacity-50 dark:text-dark-400" :title="text.updateBank" :disabled="refreshingBank" @click="updateFingerprint">{{ text.fingerprint }} {{ fingerprintCommit ? fingerprintCommit.slice(0, 12) : '—' }}</button>
              </div>
              <div class="mb-5"><label class="mb-2 block text-xs font-semibold text-gray-700 dark:text-dark-200">{{ text.key }}</label><Select v-model="selectedKey" :options="keyOptions" searchable :loading="keysLoading" :disabled="diagnosing" :aria-label="text.key" /><p v-if="!keysLoading && !keys.length" class="mt-2 text-xs text-amber-600">{{ text.noKey }}</p></div>
              <div class="mb-5"><h4 class="mb-1 text-xs font-semibold text-gray-700 dark:text-dark-200">{{ text.chooseModels }}</h4><p class="mb-3 text-xs text-gray-500 dark:text-dark-400">{{ text.modelHint }}</p><CodexDiagnosticModelPicker v-model="selectedModels" :models="models" :ticket-models="statuses.map(status => status.model)" :disabled="diagnosing" /></div>
              <div class="sticky bottom-0 -mx-4 flex flex-wrap items-center justify-between gap-3 border-t border-gray-200 bg-gray-50 px-4 py-4 pb-[max(1rem,env(safe-area-inset-bottom))] dark:border-dark-700 dark:bg-dark-900 sm:static sm:mx-0 sm:px-0">
                <span class="text-xs text-gray-500 dark:text-dark-400">{{ text.paid }}</span>
                <div class="ml-auto flex flex-wrap items-center gap-3"><button v-if="diagnosticRun" type="button" class="text-xs font-medium text-primary-600 dark:text-primary-400" @click="showDiagnosticStage('results')">{{ text.viewResults }}</button><button type="button" class="btn btn-primary" :disabled="diagnosing || !selectedKey || !selectedModels.length" @click="runDiagnostic">{{ text.start }} {{ selectedModels.length }} {{ text.count }}</button></div>
              </div>
            </div>
            <div v-else-if="diagnosticRun" class="min-h-full bg-white dark:bg-dark-800">
              <div data-test="diagnostic-progress" class="sticky top-0 z-10 border-b border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800">
                <div class="mx-auto max-w-3xl px-4 py-5 sm:px-7">
                  <div class="flex items-center justify-between gap-3"><h3 ref="resultsHeading" tabindex="-1" class="text-lg font-semibold text-gray-900 outline-none dark:text-white">{{ text.results }}</h3><span class="rounded-md px-2 py-1 text-xs" :class="diagnosing ? 'bg-primary-50 text-primary-700 dark:bg-primary-900/30 dark:text-primary-300' : 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-dark-300'">{{ diagnosing ? text.running : diagnosticRun.state === 'cancelled' ? text.cancelled : text.finished }}</span></div>
                  <div class="mt-2 flex flex-wrap gap-x-3 gap-y-1 text-[11px] text-gray-500 dark:text-dark-400"><span data-test="diagnostic-key">{{ diagnosticRun.keyName }} · #{{ diagnosticRun.keyID }}</span><span>{{ diagnosticTotal }} {{ text.count }}</span><span>{{ text.paid }}</span><span :title="diagnosticRun.fingerprintCommit">{{ text.fingerprint }} {{ diagnosticRun.fingerprintCommit ? diagnosticRun.fingerprintCommit.slice(0, 12) : '—' }}</span></div>
                  <div class="mt-5 flex items-center justify-between gap-3"><p role="status" aria-live="polite" class="min-w-0 break-words text-xs text-gray-800 dark:text-dark-200">{{ currentDiagnostic ? text.testing + ' ' + currentDiagnostic : diagnosticRun.state === 'cancelled' ? text.stoppedSummary : text.finishedSummary }}</p><span class="shrink-0 text-[11px] tabular-nums text-gray-500 dark:text-dark-400">{{ results.length }} / {{ diagnosticTotal }} {{ text.completed }}</span></div>
                  <div class="mt-2 h-1.5 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700" role="progressbar" :aria-label="text.progress" :aria-valuemin="0" :aria-valuemax="diagnosticTotal" :aria-valuenow="results.length"><div class="h-full rounded-full bg-primary-500" :style="{ width: diagnosticTotal ? (results.length / diagnosticTotal * 100) + '%' : '0%' }"></div></div>
                  <div class="mt-3 flex items-center justify-between gap-3"><p class="text-[11px] text-gray-500 dark:text-dark-400">{{ diagnosing ? text.serialHint : text.keepResults }}</p><button v-if="diagnosing" type="button" class="btn btn-secondary shrink-0 !px-3 !py-1.5 !text-xs" @click="cancelDiagnostic">{{ text.cancel }}</button><button v-else type="button" class="btn btn-secondary shrink-0 !px-3 !py-1.5 !text-xs" @click="showDiagnosticStage('setup')">{{ text.editSetup }}</button></div>
                </div>
              </div>
              <div class="mx-auto max-w-3xl px-4 pb-5 sm:px-7">
                <article v-for="(entry, entryIndex) in diagnosticRun.entries" :key="entry.model" :data-model="entry.model" :data-status="entry.status" class="border-b border-gray-100 py-4 dark:border-dark-700">
                  <div class="flex items-center gap-3">
                    <span v-if="entry.status === 'running'" aria-hidden="true" class="mx-0.5 h-4 w-4 shrink-0 animate-spin rounded-full border-2 border-gray-200 border-t-primary-500 motion-reduce:animate-none dark:border-dark-600 dark:border-t-primary-400"></span>
                    <span v-else aria-hidden="true" class="w-5 shrink-0 text-center text-sm" :class="entry.status === 'normal' ? 'text-primary-600 dark:text-primary-400' : entry.status === 'failed' || entry.status === 'degraded' ? 'text-red-500' : 'text-gray-400'">{{ diagnosticIcon(entry.status) }}</span>
                    <div class="min-w-0 flex-1"><h4 class="break-all font-mono text-xs font-medium text-gray-900 dark:text-white sm:text-sm">{{ entry.model }}</h4><p class="mt-1 text-[11px] text-gray-500 dark:text-dark-400">{{ diagnosticCaption(entry.status) }}</p></div>
                    <span class="shrink-0 rounded-md px-2 py-1 text-[11px] font-medium" :class="diagnosticBadge(entry.status)">{{ resultLabel(entry.status) }}</span>
                  </div>
                  <template v-if="entry.result">
                    <button type="button" class="ml-8 mt-2 text-[11px] font-medium text-primary-600 hover:underline dark:text-primary-400" :aria-expanded="entry.expanded" :aria-controls="'codex-diagnostic-detail-' + entryIndex" @click="entry.expanded = !entry.expanded">{{ entry.expanded ? text.hideResultDetails : text.resultDetails }}</button>
                    <dl v-if="entry.expanded" :id="'codex-diagnostic-detail-' + entryIndex" class="ml-8 mt-2 space-y-2 rounded-lg border border-gray-200 bg-gray-50 p-3 text-xs dark:border-dark-700 dark:bg-dark-900">
                      <div><dt class="text-gray-500 dark:text-dark-400">{{ text.prediction }}</dt><dd class="mt-1 break-all text-gray-800 dark:text-dark-200">{{ entry.result.predicted_model || '—' }} · {{ entry.result.probability == null ? '—' : (entry.result.probability * 100).toFixed(1) + '%' }}</dd></div>
                      <div v-if="entry.result.reason"><dt class="text-gray-500 dark:text-dark-400">{{ text.reason }}</dt><dd class="mt-1 whitespace-pre-wrap break-words text-gray-800 dark:text-dark-200 [overflow-wrap:anywhere]">{{ entry.result.reason }}</dd></div>
                      <div v-if="entry.result.gateway_error_code || entry.result.http_status"><dt class="text-gray-500 dark:text-dark-400">{{ text.gateway }}</dt><dd class="mt-1 break-all text-gray-800 dark:text-dark-200">{{ [entry.result.gateway_error_code, entry.result.http_status ? 'HTTP ' + entry.result.http_status : ''].filter(Boolean).join(' · ') }}</dd></div>
                    </dl>
                  </template>
                </article>
                <p class="mt-4 text-[11px] text-gray-500 dark:text-dark-400">{{ text.failureHint }}</p>
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
import { computed, nextTick, onUnmounted, ref, watch } from 'vue'
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
  setup: '创建一次检测', diagnosticDesc: '选择计费 Key 和模型，开始后直接查看检测进度与结果。',
  key: '用于计费的 API Key', chooseKey: '选择 API Key', noKey: '没有可用的本人 API Key', chooseModels: '选择检测模型',
  modelHint: '可多选；无需打票的旧模型直接检测。', start: '开始检测', count: '个模型', cancel: '取消后续检测', paid: '正常计费',
  viewResults: '查看本次结果', editSetup: '修改配置', finished: '已完成', cancelled: '已取消', progress: '检测进度',
  pending: '等待中', stopped: '已停止等待', not_run: '未执行', checking: '检测中', waitingHint: '等待前一个模型完成', checkingHint: '正在处理本次检测',
  serialHint: '按所选顺序检测，结果逐项更新', keepResults: '已完成的结果仍然保留', stoppedSummary: '后续检测已停止', finishedSummary: '本次检测已结束',
  resultDetails: '查看详情 +', hideResultDetails: '收起详情 −', prediction: '预测模型 / 匹配概率', reason: '原因', gateway: '网关响应',
  failureHint: '请求失败与疑似降智分别展示。具体原因可展开查看。', failedHint: '请求未完成，不代表降智', stoppedHint: '未收到检测结论', notRunHint: '取消后未发起请求', missingResult: '未返回此模型的检测结果',
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
  setup: 'Create a check', diagnosticDesc: 'Choose a billing key and models, then follow progress and results as each check completes.',
  key: 'Billing API key', chooseKey: 'Select API key', noKey: 'No eligible own API key', chooseModels: 'Select models',
  modelHint: 'Select multiple models; older models without tickets are checked directly.', start: 'Check', count: 'models', cancel: 'Cancel remaining checks', paid: 'Normal billing applies',
  viewResults: 'View current results', editSetup: 'Edit setup', finished: 'Completed', cancelled: 'Cancelled', progress: 'Check progress',
  pending: 'Waiting', stopped: 'Stopped waiting', not_run: 'Not started', checking: 'Checking', waitingHint: 'Waiting for the previous model', checkingHint: 'Processing this check',
  serialHint: 'Checking models in order; results appear as they complete', keepResults: 'Completed results are retained', stoppedSummary: 'Remaining checks stopped', finishedSummary: 'This check has ended',
  resultDetails: 'View details +', hideResultDetails: 'Hide details −', prediction: 'Predicted model / probability', reason: 'Reason', gateway: 'Gateway response',
  failureHint: 'Request failures and possible degradation are shown separately. Expand a result for details.', failedHint: 'Request failed; this does not imply degradation', stoppedHint: 'No conclusion received', notRunHint: 'Request not sent after cancellation', missingResult: 'No result was returned for this model',
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
type DiagnosticStatus = tickets.TicketDiagnostic['status'] | 'pending' | 'running' | 'stopped' | 'not_run'
type DiagnosticStage = 'setup' | 'results'
interface DiagnosticEntry { model: string; status: DiagnosticStatus; result?: tickets.TicketDiagnostic; expanded: boolean }
interface DiagnosticRun { keyID: number; keyName: string; fingerprintCommit: string; entries: DiagnosticEntry[]; state: 'running' | 'finished' | 'cancelled' }
const diagnosticStage = ref<DiagnosticStage>('setup')
const diagnosticRun = ref<DiagnosticRun | null>(null)
const scrollViewport = ref<HTMLElement | null>(null)
const resultsHeading = ref<HTMLElement | null>(null)
const setupHeading = ref<HTMLElement | null>(null)
const keysLoading = ref(false)
const diagnosing = computed(() => diagnosticRun.value?.state === 'running')
const results = computed(() => diagnosticRun.value?.entries.flatMap(entry => entry.result ? [entry.result] : []) ?? [])
const diagnosticTotal = computed(() => diagnosticRun.value?.entries.length ?? 0)
const currentDiagnostic = computed(() => diagnosticRun.value?.entries.find(entry => entry.status === 'running')?.model ?? '')
const controller = ref<AbortController | null>(null)
let diagnosticSerial = 0
let accountSessionSerial = 0
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
function resultLabel(status: DiagnosticStatus) { return status === 'running' ? text.value.checking : text.value[status] }
function diagnosticBadge(status: DiagnosticStatus) {
  if (status === 'normal' || status === 'running') return 'bg-primary-50 text-primary-700 dark:bg-primary-900/30 dark:text-primary-300'
  if (status === 'degraded' || status === 'failed') return 'bg-red-50 text-red-700 dark:bg-red-900/30 dark:text-red-300'
  if (status === 'uncertain') return 'bg-amber-50 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
  return 'bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-dark-400'
}
function diagnosticIcon(status: DiagnosticStatus) {
  return status === 'normal' ? '✓' : status === 'failed' ? '×' : status === 'degraded' ? '!' : status === 'uncertain' ? '?' : '—'
}
function diagnosticCaption(status: DiagnosticStatus) {
  if (status === 'pending') return text.value.waitingHint
  if (status === 'running') return text.value.checkingHint
  if (status === 'failed') return text.value.failedHint
  if (status === 'stopped') return text.value.stoppedHint
  if (status === 'not_run') return text.value.notRunHint
  return text.value.completed
}
async function showDiagnosticStage(stage: DiagnosticStage) {
  if (stage === 'setup' && diagnosing.value) return
  diagnosticStage.value = stage
  const session = accountSessionSerial
  const run = diagnosticRun.value
  await nextTick()
  if (session !== accountSessionSerial || run !== diagnosticRun.value || !props.show || activeTab.value !== 'diagnostic' || diagnosticStage.value !== stage) return
  if (scrollViewport.value) scrollViewport.value.scrollTop = 0
  const heading = stage === 'results' ? resultsHeading.value : setupHeading.value
  heading?.focus({ preventScroll: true })
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
  if (tab === 'diagnostic') { openDiagnostic(); void showDiagnosticStage(diagnosticStage.value) }
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
  const session = accountSessionSerial
  try {
    const [bank, updated] = await Promise.all([tickets.fingerprint(), adminAPI.accounts.getById(id)])
    if (session !== accountSessionSerial || props.account?.id !== id || !props.show) return
    fingerprintCommit.value = bank.commit
    models.value = bank.models
    accountDetail.value = updated
    emit('updated', updated)
  } catch (cause) { if (session === accountSessionSerial) error.value = extractApiErrorMessage(cause, text.value.error) }
  if (session === accountSessionSerial) await loadEvents()
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
  if (keys.value.length || keysLoading.value) return
  const session = accountSessionSerial
  keysLoading.value = true
  try {
    const available = await tickets.ownKeys()
    if (session !== accountSessionSerial || !props.show) return
    keys.value = available.filter(key => key.status === 'active' &&
      (key.quota === 0 || key.quota_used < key.quota) && (!key.expires_at || new Date(key.expires_at) > new Date()))
  } catch (cause) { if (session === accountSessionSerial) error.value = extractApiErrorMessage(cause, text.value.error) }
  finally { if (session === accountSessionSerial) keysLoading.value = false }
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
}
async function runDiagnostic() {
  if (!props.show || !props.account || !selectedKey.value || !selectedModels.value.length || diagnosing.value) return
  const accountID = props.account.id
  const serial = ++diagnosticSerial
  const session = accountSessionSerial
  const keyID = selectedKey.value
  diagnosticRun.value = {
    keyID, keyName: keys.value.find(key => key.id === keyID)?.name ?? text.value.key,
    fingerprintCommit: fingerprintCommit.value,
    entries: [...selectedModels.value].map(model => ({ model, status: 'pending', expanded: false })),
    state: 'running'
  }
  const run = diagnosticRun.value
  const isCurrent = () => serial === diagnosticSerial && session === accountSessionSerial && diagnosticRun.value === run && props.show && props.account?.id === accountID
  error.value = ''
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
        if (!result) { entry.result = { model: entry.model, status: 'failed', reason: text.value.missingResult }; entry.status = 'failed' }
      } catch (cause) {
        if (!isCurrent() || requestController.signal.aborted) break
        entry.result = { model: entry.model, status: 'failed', reason: extractApiErrorMessage(cause, text.value.error) }
        entry.status = 'failed'
      } finally { if (controller.value === requestController) controller.value = null }
    }
  } finally {
    if (isCurrent()) {
      run.state = 'finished'
      await reload()
    }
  }
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
  ++accountSessionSerial
  cancelDiagnostic()
  diagnosticRun.value = null
  diagnosticStage.value = 'setup'
  keysLoading.value = false
  error.value = ''
  loading.value = false
  ++loadSerial
  closeDetail()
  if (!props.show || !props.account) return
  activeTab.value = props.initialDiagnostic ? 'diagnostic' : 'tickets'
  selectedKey.value = null
  selectedModels.value = []
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
onUnmounted(() => { ++accountSessionSerial; ++loadSerial; cancelDiagnostic() })
</script>
