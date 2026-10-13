<template>
  <BaseDialog
    :show="show && !!account"
    :title="text.title"
    width="wide"
    content-class="h-[min(720px,94dvh)] overflow-hidden"
    body-class="space-y-5 sm:!py-5"
    :z-index="10000"
    @close="emit('close')"
  >
    <template #header="{ titleId }">
      <div class="flex min-w-0 items-center gap-3">
        <span class="flex h-11 w-11 shrink-0 items-center justify-center rounded-xl bg-primary-50 text-primary-700 dark:bg-primary-900/30 dark:text-primary-300"><Icon name="bolt" size="md" /></span>
        <div class="min-w-0">
          <h2 :id="titleId" class="text-lg font-semibold text-gray-900 dark:text-white">{{ text.title }}</h2>
          <p class="mt-1 truncate text-xs text-gray-500 dark:text-dark-400"><span class="font-medium text-gray-700 dark:text-dark-200">{{ account?.name }}</span> · OpenAI {{ account?.type === 'oauth' ? 'OAuth' : 'Setup Token' }}</p>
        </div>
      </div>
      <div class="ml-3 flex shrink-0 items-center gap-2">
        <button type="button" class="btn btn-secondary btn-sm" :disabled="loading || accountLoading || savingParticipation" @click="reload">
          <Icon name="refresh" size="sm" :class="{ 'animate-spin': loading || accountLoading }" />{{ text.refresh }}
        </button>
        <button type="button" class="flex h-11 w-11 shrink-0 items-center justify-center rounded-xl text-gray-500 hover:bg-gray-100 dark:text-dark-400 dark:hover:bg-dark-700" :aria-label="text.close" @click="emit('close')"><Icon name="x" size="md" /></button>
      </div>
    </template>

    <div v-if="error" role="alert" class="flex items-start gap-2 rounded-xl border border-red-200 bg-red-50 p-3 text-sm text-red-700 dark:border-red-900 dark:bg-red-900/20 dark:text-red-300">
      <Icon name="exclamationCircle" size="sm" class="mt-0.5 shrink-0" /><span class="min-w-0 break-words">{{ error }}</span>
    </div>

    <div class="grid grid-cols-2 gap-3 sm:grid-cols-3">
      <div class="stat-card !p-4">
        <span class="stat-icon hidden !h-10 !w-10 sm:flex" :class="status?.ready ? 'stat-icon-success' : 'stat-icon-warning'"><Icon :name="status?.ready ? 'checkCircle' : 'exclamationCircle'" size="md" /></span>
        <div class="min-w-0 flex-1">
          <p class="truncate text-lg font-bold" :class="status?.ready ? 'text-emerald-600 dark:text-emerald-400' : status ? 'text-amber-600 dark:text-amber-400' : 'text-gray-900 dark:text-white'" :title="status ? statusLabel(status) : undefined">{{ status ? statusLabel(status) : '—' }}</p>
          <p class="stat-label">{{ text.available }}</p>
        </div>
      </div>
      <div class="stat-card !p-4">
        <span class="stat-icon stat-icon-primary hidden !h-10 !w-10 sm:flex"><Icon name="shield" size="md" /></span>
        <div class="min-w-0 flex-1">
          <p class="truncate font-mono text-base font-semibold text-gray-900 dark:text-white" :title="fingerprintCommit">{{ fingerprintCommit ? fingerprintCommit.slice(0, 12) : '—' }}</p>
          <p class="stat-label">{{ text.fingerprint }}</p>
          <button type="button" class="mt-1 inline-flex items-center gap-1 text-xs font-medium text-primary-600 hover:underline disabled:opacity-50 dark:text-primary-400" :disabled="refreshingBank" @click="updateFingerprint">
            <Icon name="refresh" size="xs" :class="{ 'animate-spin': refreshingBank }" />{{ text.updateBank }}
          </button>
        </div>
      </div>
      <div class="stat-card col-span-2 !p-4 sm:col-span-1">
        <span class="stat-icon stat-icon-warning hidden !h-10 !w-10 sm:flex"><Icon name="clock" size="md" /></span>
        <div class="min-w-0 flex-1">
          <p class="text-base font-semibold text-gray-900 dark:text-white">{{ text.retentionTitle }}</p>
          <p class="stat-label !text-xs">{{ text.retentionNote }}</p>
        </div>
      </div>
    </div>

    <nav class="tabs w-full overflow-x-auto sm:w-fit" :aria-label="text.tabs">
      <button v-for="item in tabs" :key="item.value" type="button" class="tab shrink-0 whitespace-nowrap" :class="{ 'tab-active': activeTab === item.value }" :aria-current="activeTab === item.value ? 'page' : undefined" @click="switchTab(item.value)">{{ item.label }}</button>
    </nav>

    <section v-if="activeTab === 'tickets'" :aria-label="text.current" class="space-y-3">
      <div class="card p-4 sm:px-5">
        <div class="flex items-center justify-between gap-4">
          <div class="min-w-0">
            <h3 class="text-sm font-semibold text-gray-900 dark:text-white">{{ text.accountParticipation }}</h3>
            <p v-if="savingParticipation" role="status" class="mt-1 flex items-center gap-1.5 text-xs text-gray-500 dark:text-dark-400"><span class="spinner !h-3 !w-3" />{{ text.savingParticipation }}</p>
            <p v-else-if="participationSaved" role="status" class="mt-1 flex items-center gap-1 text-xs text-primary-600 dark:text-primary-400"><Icon name="check" size="xs" />{{ text.participationSaved }}</p>
            <p v-if="participationError" role="alert" class="mt-1 text-xs text-red-600 dark:text-red-400">{{ participationError }}</p>
          </div>
          <Toggle :model-value="participation.enabled" :disabled="participationDisabled" :aria-label="text.accountParticipation" class="disabled:cursor-not-allowed disabled:opacity-50" @update:model-value="saveParticipation($event)" />
        </div>
        <details class="group mt-2 text-xs text-gray-500 dark:text-dark-400">
          <summary class="inline-flex cursor-pointer list-none items-center gap-1 hover:text-gray-700 dark:hover:text-dark-200"><Icon name="chevronRight" size="xs" class="transition-transform group-open:rotate-90" />{{ text.hint }}</summary>
          <p class="mt-2 leading-5">{{ text.participationHint }}</p>
        </details>
      </div>

      <div v-if="!status" class="card empty-state">
        <Icon name="inbox" size="xl" class="mb-3 text-gray-300 dark:text-dark-600" />
        <p class="empty-state-description">{{ text.noTicket }}</p>
      </div>
      <article v-else class="card p-4 sm:px-5">
        <div class="flex items-start justify-between gap-2">
          <div class="min-w-0">
            <h4 class="text-sm font-semibold text-gray-900 dark:text-white">{{ text.globalTicket }} <span class="font-mono font-normal text-gray-500 dark:text-dark-400">· {{ text.harvestModel }} {{ status.model }}</span></h4>
            <p class="mt-1 text-xs leading-5 text-gray-500 dark:text-dark-400">{{ text.sharedHint }}</p>
          </div>
          <span class="badge shrink-0" :class="statusClass(status)"><span class="h-1.5 w-1.5 rounded-full" :class="statusDot(status)" />{{ statusLabel(status) }}</span>
        </div>
        <dl class="mt-4 grid grid-cols-2 gap-2 text-xs sm:grid-cols-3 lg:grid-cols-5">
          <div class="rounded-lg bg-gray-50 px-3 py-2 dark:bg-dark-900/40"><dt class="text-gray-500 dark:text-dark-400">{{ text.acquired }}</dt><dd class="mt-0.5 tabular-nums text-gray-800 dark:text-dark-200">{{ formatTime(status.captured_at) }}</dd></div>
          <div class="rounded-lg bg-gray-50 px-3 py-2 dark:bg-dark-900/40"><dt class="text-gray-500 dark:text-dark-400">{{ text.sourceAccount }}</dt><dd class="mt-0.5 font-medium tabular-nums text-gray-800 dark:text-dark-200">{{ status.source_account_id ? `#${status.source_account_id}` : '—' }}</dd></div>
          <div class="rounded-lg bg-gray-50 px-3 py-2 dark:bg-dark-900/40"><dt class="text-gray-500 dark:text-dark-400">{{ text.length }}</dt><dd class="mt-0.5 font-medium tabular-nums text-gray-800 dark:text-dark-200">{{ status.length ?? '—' }}</dd></div>
          <div class="rounded-lg bg-gray-50 px-3 py-2 dark:bg-dark-900/40"><dt class="text-gray-500 dark:text-dark-400">turn-state</dt><dd class="mt-0.5 flex items-center gap-1" :class="presenceClass(status.turn_state_present)"><Icon :name="status.turn_state_present ? 'check' : 'x'" size="xs" />{{ status.turn_state_present ? text.present : text.absent }}</dd></div>
          <div class="rounded-lg bg-gray-50 px-3 py-2 dark:bg-dark-900/40"><dt class="text-gray-500 dark:text-dark-400">Cookie</dt><dd class="mt-0.5 flex items-center gap-1" :class="presenceClass(status.cookie_present)"><Icon :name="status.cookie_present ? 'check' : 'x'" size="xs" />{{ status.cookie_present ? text.present : text.absent }}</dd></div>
        </dl>
        <div class="mt-4 flex items-center justify-between gap-3 border-t border-gray-100 pt-3 dark:border-dark-700">
          <span class="flex min-w-0 items-center gap-1.5 text-xs text-gray-500 dark:text-dark-400"><span class="h-1.5 w-1.5 shrink-0 rounded-full" :class="status.harvest_enabled ? 'bg-emerald-500' : 'bg-gray-300 dark:bg-dark-500'" />{{ status.harvest_enabled ? text.harvestActive : text.harvestInactive }}</span>
          <button type="button" class="btn btn-secondary btn-sm shrink-0" :disabled="harvesting || participationDisabled || !participation.enabled || !status.harvest_enabled" @click="harvestTicket">
            <span v-if="harvesting" class="spinner !h-3 !w-3" /><Icon v-else name="play" size="xs" />{{ harvesting ? text.running : text.manual }}
          </button>
        </div>
      </article>
    </section>

    <section v-else-if="activeTab === 'attempts' || activeTab === 'invalidations'" :aria-label="activeTab === 'attempts' ? text.attempts : text.invalidations" class="card overflow-hidden">
      <div class="border-b border-gray-100 px-4 py-4 dark:border-dark-700 sm:px-5">
        <h3 class="text-base font-semibold text-gray-900 dark:text-white">{{ activeTab === 'attempts' ? text.attempts : text.invalidations }}</h3>
        <p class="mt-1 text-xs leading-5 text-gray-500 dark:text-dark-400">{{ activeTab === 'attempts' ? text.attemptsDesc : text.invalidationsDesc }}</p>
        <p class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ text.accountEventsHint }}</p>
        <div class="mt-3 flex flex-wrap items-center gap-2">
          <div v-if="activeTab === 'attempts'" class="w-full sm:w-40"><Select v-model="filter" :options="attemptOptions" :aria-label="text.filter" /></div>
          <div class="w-full sm:w-48"><Select v-model="modelFilter" :options="modelOptions" searchable :aria-label="text.model" /></div>
          <DateRangePicker v-model:start-date="startDay" v-model:end-date="endDay" />
        </div>
      </div>
      <div v-if="loading" role="status" :aria-label="text.loading" class="divide-y divide-gray-100 dark:divide-dark-700">
        <div v-for="index in 4" :key="index" class="flex items-center gap-4 px-5 py-3.5"><div class="skeleton h-5 w-16 rounded-full" /><div class="flex-1 space-y-2"><div class="skeleton h-3 w-40" /><div class="skeleton h-3 w-64 max-w-full" /></div><div class="skeleton h-3 w-14" /></div>
      </div>
      <div v-else-if="!events.length" class="empty-state">
        <Icon name="inbox" size="xl" class="mb-3 text-gray-300 dark:text-dark-600" />
        <p class="empty-state-description">{{ text.empty }}</p>
      </div>
      <div v-else class="divide-y divide-gray-100 dark:divide-dark-700">
        <template v-for="event in events" :key="`${event.kind}-${event.id}`">
          <div v-if="isNewDay(event)" class="bg-gray-50 px-4 py-1.5 text-xs font-medium text-gray-500 dark:bg-dark-900/40 dark:text-dark-400 sm:px-5">{{ formatDay(event.occurred_at) }}</div>
          <button type="button" class="group flex w-full flex-col gap-2 px-4 py-3 text-left transition-colors hover:bg-gray-50 dark:hover:bg-dark-700/40 sm:flex-row sm:items-center sm:gap-4 sm:px-5" @click="openDetail(event)">
            <span class="badge shrink-0 self-start sm:self-center" :class="eventBadge(event.kind)">{{ kindLabel(event.kind) }}</span>
            <span class="min-w-0 flex-1">
              <span class="block break-all font-mono text-xs font-semibold text-gray-900 dark:text-white">{{ event.model }} <span v-if="event.kind !== 'invalidation' && !event.verification_method" class="font-sans font-normal text-amber-600 dark:text-amber-400">{{ text.legacy }}</span></span>
              <span class="mt-1 block break-words text-xs text-gray-700 dark:text-dark-200" data-testid="event-account">{{ text.eventAccount }}: {{ eventAccountLabel(event) }}</span>
              <span class="mt-1 block break-words text-xs text-gray-500 dark:text-dark-400">{{ eventSummary(event) }}</span>
            </span>
            <span class="flex shrink-0 items-center gap-2"><time class="text-xs tabular-nums text-gray-500 dark:text-dark-400">{{ formatTime(event.occurred_at, true) }}</time><Icon name="chevronRight" size="sm" class="hidden text-gray-300 transition-colors group-hover:text-gray-500 dark:text-dark-600 sm:block" /></span>
          </button>
        </template>
      </div>
      <div class="flex flex-wrap items-center justify-between gap-3 border-t border-gray-100 px-4 py-3 text-xs text-gray-500 dark:border-dark-700 dark:text-dark-400 sm:px-5">
        <span>{{ total }} {{ text.records }}</span>
        <div class="flex items-center gap-2">
          <button type="button" class="btn btn-secondary btn-sm !px-2" :aria-label="text.prevPage" :disabled="page <= 1 || loading" @click="page--"><Icon name="chevronLeft" size="sm" /></button>
          <span class="tabular-nums">{{ page }} / {{ Math.max(1, Math.ceil(total / pageSize)) }}</span>
          <button type="button" class="btn btn-secondary btn-sm !px-2" :aria-label="text.nextPage" :disabled="page * pageSize >= total || loading" @click="page++"><Icon name="chevronRight" size="sm" /></button>
        </div>
      </div>
    </section>
  </BaseDialog>

  <BaseDialog :show="!!detail" :title="text.details" width="wide" :z-index="10001" body-class="sm:!py-5" @close="closeDetail">
    <template #header="{ titleId }">
      <div class="min-w-0">
        <h3 :id="titleId" class="break-all text-lg font-semibold text-gray-900 dark:text-white">{{ text.details }} · <span class="font-mono">{{ detail?.model }}</span></h3>
        <p v-if="detail" class="mt-1 break-words text-xs text-gray-700 dark:text-dark-200" data-testid="detail-account">{{ text.eventAccount }}: {{ eventAccountLabel(detail) }}</p>
        <p v-if="detail" class="mt-1 flex flex-wrap items-center gap-2 text-xs text-gray-500 dark:text-dark-400"><span class="badge" :class="eventBadge(detail.kind)">{{ kindLabel(detail.kind) }}</span><span class="tabular-nums">{{ formatTime(detail.occurred_at) }}</span></p>
      </div>
      <button type="button" class="ml-3 flex h-11 w-11 shrink-0 items-center justify-center rounded-xl text-gray-500 hover:bg-gray-100 dark:text-dark-400 dark:hover:bg-dark-700" :aria-label="text.close" @click="closeDetail"><Icon name="x" size="md" /></button>
    </template>
    <template v-if="detail">
      <div v-if="detail.kind === 'invalidation'">
        <div class="mb-4 flex flex-wrap gap-2 text-xs">
          <span v-if="reasonLabel(detail.reason_code)" class="badge badge-warning">{{ reasonLabel(detail.reason_code) }}</span>
          <span v-if="detail.request_kind" class="badge badge-gray">{{ detail.request_kind }}</span>
          <span v-if="detail.request_route" class="badge badge-gray font-mono">{{ detail.request_route }}</span>
          <span class="badge badge-gray">HTTP {{ detail.response_http_status ?? '—' }}</span>
        </div>
        <div v-if="detailLoading" role="status" class="flex items-center gap-2 py-6 text-sm text-gray-500 dark:text-dark-400"><span class="spinner !h-4 !w-4" />{{ text.loading }}</div>
        <div v-else-if="detailError" role="alert" class="rounded-xl border border-red-200 bg-red-50 p-3 text-sm text-red-700 dark:border-red-900 dark:bg-red-900/20 dark:text-red-300">{{ detailError }}</div>
        <div v-else-if="rawDetail" class="grid gap-3 md:grid-cols-2">
          <div v-for="field in credentialFields" :key="field.label" class="card min-w-0 p-4">
            <div class="flex items-center justify-between gap-2">
              <span class="text-xs font-semibold text-gray-500 dark:text-dark-400">{{ field.label }}</span>
              <button v-if="field.value" type="button" class="inline-flex items-center gap-1 text-xs font-medium text-primary-600 hover:underline dark:text-primary-400" @click="copy(field.value, field.label)"><Icon :name="copiedLabel === field.label ? 'check' : 'copy'" size="xs" />{{ copiedLabel === field.label ? text.copied : text.copy }}</button>
            </div>
            <pre class="mt-2 max-h-40 overflow-auto whitespace-pre-wrap break-all rounded-lg bg-gray-50 p-2.5 font-mono text-xs text-gray-800 dark:bg-dark-900/40 dark:text-dark-200">{{ field.value || (field.original ? text.notCarried : text.notReturned) }}</pre>
          </div>
        </div>
        <div v-if="rawDetail?.returned_set_cookies?.length" class="card mt-3 p-4">
          <h4 class="text-xs font-semibold text-gray-500 dark:text-dark-400">Set-Cookie</h4>
          <pre v-for="(cookie, index) in rawDetail.returned_set_cookies" :key="index" class="mt-2 overflow-auto whitespace-pre-wrap break-all rounded-lg bg-gray-50 p-2.5 font-mono text-xs text-gray-800 dark:bg-dark-900/40 dark:text-dark-200">{{ cookie }}</pre>
        </div>
      </div>
      <dl v-else class="grid gap-3 text-sm sm:grid-cols-2">
        <div v-for="field in attemptFields" :key="field.label" class="rounded-xl bg-gray-50 px-4 py-3 dark:bg-dark-900/40"><dt class="text-xs text-gray-500 dark:text-dark-400">{{ field.label }}</dt><dd class="mt-1 break-all font-medium text-gray-900 dark:text-white">{{ field.value }}</dd></div>
      </dl>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import * as tickets from '@/api/admin/codexTickets'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import Toggle from '@/components/common/Toggle.vue'
import DateRangePicker from '@/components/common/DateRangePicker.vue'
import Icon from '@/components/icons/Icon.vue'
import { extractApiErrorMessage } from '@/utils/apiError'
import type { Account } from '@/types'

const props = defineProps<{ show: boolean; account: Account | null }>()
const emit = defineEmits<{ close: []; updated: [account: Account] }>()
const { locale } = useI18n()
// 弹窗关闭时 BaseDialog 仍会读取标题；locale 不可用时回退英文，避免宿主页面渲染失败。
const text = computed(() => String(locale?.value ?? '').startsWith('zh') ? {
  title: '票据中心', retention: '流水保留 90 天 · 失效凭据仅管理员可查看', refresh: '刷新', close: '关闭',
  available: '共享票', fingerprint: '指纹版本', updateBank: '校验并更新', tabs: '票据中心导航',
  current: '当前票据', attempts: '打票流水', invalidations: '票据过期历史',
  noTicket: '此账号不使用共享票', globalTicket: '全站共享票', harvestModel: '打票模型', length: '票据长度', acquired: '获取于', present: '已携带', absent: '未携带', sourceAccount: '共享票来源账号',
  eventAccount: '所属账号', accountEventsHint: '仅显示当前账号的流水；共享票可能由其他账号打出。',
  sharedHint: '全站只有一张票（turn-state + __oailb + __cflb），由参与账号用 gpt-6-sol 打出，所有参与账号在所有门控模型上共用。',
  accountParticipation: '此账号参与共享票',
  participationHint: '切换即保存。关闭后该号既不打票也不使用共享票，请求跳过无票限制；重新开启后恢复原无票策略。实际打票还受全局开关、账号状态和 gpt-6-sol 限流约束。',
  savingParticipation: '正在保存参与设置…', participationSaved: '参与设置已保存', participationRefreshError: '参与设置已保存，但账号详情刷新失败，请点击刷新重试。',
  harvestActive: '已允许打票', harvestInactive: '当前未允许打票',
  superseded: '打票成功，本轮已有票未采用', credentialsChanged: 'turn-state 变化且 __oailb 或 __cflb 变化', legacyOailbChanged: '旧规则：turn-state 与 __oailb 同时变化', legacyStateChanged: '旧规则：turn-state 变化',
  ready: '可用', missing: '无票', paused: '暂停', invalidatedForAccount: '本号已失效，等待新票', manual: '手动打票', running: '运行中…',
  attemptsDesc: '成功、未命中与执行错误只在这里展示，不代表当前票失效。', invalidationsDesc: '此账号携带共享票的请求，上游返回新的 turn-state，且 __oailb 或 __cflb 至少一个值变化时，这一代票仅对本账号失效，其他账号继续使用，并立即重新打票；新票打出后本账号恢复。历史记录按当时的失效规则展示；均不表示推算的 TTL 到期。',
  filter: '结果', model: '模型', all: '全部结果', success: '打票成功', failure: '失败与错误', invalidation: '票据失效', allModels: '全部模型',
  loading: '正在加载…', empty: '没有符合条件的记录（最多保留 90 天）', records: '条记录', legacy: '旧版长度校验',
  details: '流水详情', copy: '复制', copied: '已复制', notCarried: '未携带', notReturned: '上游未返回',
  retentionTitle: '流水保留 90 天', retentionNote: '失效凭据仅管理员可查看', hint: '说明', prevPage: '上一页', nextPage: '下一页',
  error: '操作失败'
} : {
  title: 'Ticket center', retention: 'Records retained for 90 days · credentials visible to admins only', refresh: 'Refresh', close: 'Close',
  available: 'Shared ticket', fingerprint: 'Fingerprint version', updateBank: 'Verify & update', tabs: 'Ticket center navigation',
  current: 'Current tickets', attempts: 'Ticket attempts', invalidations: 'Ticket expiry history',
  noTicket: 'This account does not use the shared ticket', globalTicket: 'Global shared ticket', harvestModel: 'harvested with', length: 'Ticket length', acquired: 'Acquired', present: 'Present', absent: 'Absent', sourceAccount: 'Shared ticket source account',
  eventAccount: 'Account', accountEventsHint: 'Only this account’s events are shown; the shared ticket may have been harvested by another account.',
  sharedHint: 'There is one ticket (turn-state + __oailb + __cflb) for the whole site. Participating accounts harvest it with gpt-6-sol and every participating account uses it on every gated model.',
  accountParticipation: 'Use the shared ticket on this account',
  participationHint: 'Saves immediately. When off, this account neither harvests nor uses the shared ticket and its requests bypass missing-ticket restrictions; turning it back on restores the saved policy. Harvesting also depends on the global switch, account status and gpt-6-sol rate limits.',
  savingParticipation: 'Saving participation…', participationSaved: 'Participation saved', participationRefreshError: 'Participation was saved, but account details could not be refreshed. Select Refresh to retry.',
  harvestActive: 'Harvesting allowed', harvestInactive: 'Harvesting disabled',
  superseded: 'Harvested; another ticket was already adopted this round', credentialsChanged: 'Turn-state and either __oailb or __cflb changed', legacyOailbChanged: 'Legacy rule: both turn-state and __oailb changed', legacyStateChanged: 'Legacy rule: turn-state changed',
  ready: 'Ready', missing: 'Missing', paused: 'Paused', invalidatedForAccount: 'Invalid for this account; awaiting new ticket', manual: 'Harvest', running: 'Running…',
  attemptsDesc: 'Success, misses and errors are shown here; failed attempts do not mean the current ticket is invalid.', invalidationsDesc: 'Recorded when a request from this account carrying the shared ticket receives a new turn-state and a changed value for either __oailb or __cflb. The generation becomes invalid for this account only; other accounts keep using it while a new ticket is harvested at once, and this account recovers when it arrives. Historical events retain their original invalidation rules. These are not inferred TTL expiries.',
  filter: 'Result', model: 'Model', all: 'All results', success: 'Success', failure: 'Failures & errors', invalidation: 'Invalidation', allModels: 'All models',
  loading: 'Loading…', empty: 'No matching records (90-day retention)', records: 'records', legacy: 'Legacy length check',
  details: 'Event details', copy: 'Copy', copied: 'Copied', notCarried: 'Not carried', notReturned: 'Not returned by upstream',
  retentionTitle: '90-day retention', retentionNote: 'Credentials visible to admins only', hint: 'Details', prevPage: 'Previous page', nextPage: 'Next page',
  error: 'Operation failed'
})
type Tab = 'tickets' | 'attempts' | 'invalidations'
const activeTab = ref<Tab>('tickets')
const tabs = computed(() => [
  { value: 'tickets' as const, label: text.value.current },
  { value: 'attempts' as const, label: text.value.attempts },
  { value: 'invalidations' as const, label: text.value.invalidations }
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
const participation = ref<tickets.TicketParticipation>({ enabled: true })
const accountLoaded = ref(false)
const accountLoading = ref(false)
const savingParticipation = ref(false)
const participationSaved = ref(false)
const participationError = ref('')
const participationDisabled = computed(() => !accountLoaded.value || accountLoading.value || savingParticipation.value)
let accountLoadSerial = 0
let bankLoadSerial = 0
const harvesting = ref(false)
let accountSessionSerial = 0
const detail = ref<tickets.TicketEvent | null>(null)
const rawDetail = ref<tickets.TicketInvalidation | null>(null)
const detailLoading = ref(false)
const detailError = ref('')
let loadSerial = 0
const status = computed(() => (accountDetail.value ?? props.account)?.codex_ticket as tickets.TicketStatus | undefined)
const copiedLabel = ref('')
let copiedTimer: ReturnType<typeof setTimeout> | undefined
const attemptOptions = computed(() => [
  { value: 'all', label: text.value.all }, { value: 'success', label: text.value.success }, { value: 'failure', label: text.value.failure }
])
const modelOptions = computed(() => [
  { value: '', label: text.value.allModels }, ...models.value.map(model => ({ value: model, label: model }))
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
  return status.ready ? 'badge-success' : status.invalidated_for_account || status.blocked || status.harvest_paused ? 'badge-warning' : 'badge-gray'
}
function statusDot(status: tickets.TicketStatus) {
  return status.ready ? 'bg-emerald-500' : status.invalidated_for_account || status.blocked || status.harvest_paused ? 'bg-amber-500' : 'bg-gray-400 dark:bg-dark-400'
}
function presenceClass(present?: boolean) { return present ? 'text-emerald-600 dark:text-emerald-400' : 'text-gray-400 dark:text-dark-500' }
function statusLabel(status: tickets.TicketStatus) {
  if (status.ready) return text.value.ready
  if (status.invalidated_for_account) return text.value.invalidatedForAccount
  return status.blocked || status.harvest_paused ? text.value.paused : text.value.missing
}
function eventBadge(kind: string) {
  return kind === 'success' ? 'badge-success' : kind === 'invalidation' ? 'badge-warning' : 'badge-danger'
}
function kindLabel(kind: string) { return kind === 'success' ? text.value.success : kind === 'invalidation' ? text.value.invalidation : text.value.failure }
function reasonLabel(reason?: string) {
  if (reason === 'superseded') return text.value.superseded
  if (reason === 'upstream_turn_state_and_routing_cookie_changed') return text.value.credentialsChanged
  if (reason === 'upstream_turn_state_and_oailb_changed') return text.value.legacyOailbChanged
  if (reason === 'upstream_new_turn_state') return text.value.legacyStateChanged
  return reason || ''
}
function eventAccountLabel(event: tickets.TicketEvent) {
  const account = accountDetail.value ?? props.account
  const name = account?.id === event.account_id ? account.name?.trim() : ''
  return name ? `${name} (#${event.account_id})` : `#${event.account_id}`
}
function eventSummary(event: tickets.TicketEvent) {
  return [reasonLabel(event.reason_code), event.ticket_length != null ? `${text.value.length} ${event.ticket_length}` : '',
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
    { label: 'Reason / HTTP', value: `${reasonLabel(event.reason_code) || '—'} · ${event.http_status ?? '—'}` },
    { label: 'Duration / proxy', value: `${event.duration_ms ?? '—'} ms · ${event.proxy_name || '—'}` }
  ]
})
function switchTab(tab: Tab) {
  if (activeTab.value === tab) return
  activeTab.value = tab
  if (tab === 'attempts' || tab === 'invalidations') { page.value = 1; loadEvents() }
}
async function loadEvents() {
  if (!props.show || !props.account || (activeTab.value !== 'attempts' && activeTab.value !== 'invalidations')) return
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
function savedParticipation(account: Account): tickets.TicketParticipation {
  return { enabled: account.extra?.codex_ticket_harvest_enabled !== false }
}
function isCurrentAccount(session: number, id: number) {
  return session === accountSessionSerial && props.show && props.account?.id === id
}
async function loadAccount(afterSave = false) {
  if (!props.show || !props.account || (savingParticipation.value && !afterSave)) return
  const id = props.account.id
  const session = accountSessionSerial
  const serial = ++accountLoadSerial
  const isCurrent = () => serial === accountLoadSerial && isCurrentAccount(session, id)
  accountLoading.value = true
  participationError.value = ''
  try {
    const updated = await adminAPI.accounts.getById(id)
    if (!isCurrent()) return
    accountDetail.value = updated
    participation.value = savedParticipation(updated)
    accountLoaded.value = true
    emit('updated', updated)
  } catch (cause) {
    if (isCurrent()) participationError.value = afterSave ? text.value.participationRefreshError : extractApiErrorMessage(cause, text.value.error)
  } finally { if (isCurrent()) accountLoading.value = false }
}
async function saveParticipation(enabled: boolean) {
  if (participationDisabled.value || !props.show || !props.account) return
  const id = props.account.id
  const session = accountSessionSerial
  const before = participation.value
  const next = { enabled }
  participation.value = next
  savingParticipation.value = true
  participationSaved.value = false
  participationError.value = ''
  ++accountLoadSerial
  try {
    const saved = await tickets.saveParticipation(id, next)
    if (!isCurrentAccount(session, id)) return
    participation.value = { enabled: saved.enabled }
    participationSaved.value = true
    // Commit the successful PUT locally before refreshing. A failed GET must
    // not roll back settings which are already stored on the server.
    const updated = { ...(accountDetail.value ?? props.account), extra: {
      ...(accountDetail.value ?? props.account).extra,
      codex_ticket_harvest_enabled: saved.enabled
    } } as Account
    accountDetail.value = updated
    emit('updated', updated)
    await loadAccount(true)
  } catch (cause) {
    if (isCurrentAccount(session, id)) {
      participation.value = before
      participationError.value = extractApiErrorMessage(cause, text.value.error)
    }
  } finally { if (isCurrentAccount(session, id)) savingParticipation.value = false }
}
async function loadFingerprint(refresh = false) {
  const session = accountSessionSerial
  const serial = ++bankLoadSerial
  const isCurrent = () => session === accountSessionSerial && serial === bankLoadSerial && props.show
  if (refresh) refreshingBank.value = true
  try {
    const bank = await (refresh ? tickets.refreshFingerprint() : tickets.fingerprint())
    if (!isCurrent()) return
    fingerprintCommit.value = bank.commit
    models.value = bank.models
  } catch (cause) { if (isCurrent()) error.value = extractApiErrorMessage(cause, text.value.error) }
  finally { if (isCurrent()) refreshingBank.value = false }
}
async function updateFingerprint() {
  error.value = ''
  await Promise.all([loadFingerprint(true), loadAccount()])
}
async function reload() {
  if (!props.show || !props.account) return
  error.value = ''
  await Promise.all([loadAccount(), loadFingerprint(), loadEvents()])
}
async function harvestTicket() {
  if (!props.account || harvesting.value || participationDisabled.value) return
  const id = props.account.id
  const session = accountSessionSerial
  harvesting.value = true
  error.value = ''
  try {
    const attempt = await tickets.harvest(id)
    if (!isCurrentAccount(session, id)) return
    await reload()
    if (isCurrentAccount(session, id) && attempt.outcome !== 'success') error.value = attempt.reason_code || attempt.outcome
  } catch (cause) { if (isCurrentAccount(session, id)) error.value = extractApiErrorMessage(cause, text.value.error) }
  finally { if (isCurrentAccount(session, id)) harvesting.value = false }
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
async function copy(value: string | null | undefined, label: string) {
  if (!value) return
  await navigator.clipboard.writeText(value)
  copiedLabel.value = label
  clearTimeout(copiedTimer)
  copiedTimer = setTimeout(() => { copiedLabel.value = '' }, 1500)
}
watch(() => [props.show, props.account?.id], () => {
  ++accountSessionSerial
  ++accountLoadSerial
  ++bankLoadSerial
  accountLoaded.value = false
  accountLoading.value = false
  savingParticipation.value = false
  participationSaved.value = false
  participationError.value = ''
  refreshingBank.value = false
  harvesting.value = false
  fingerprintCommit.value = ''
  models.value = []
  error.value = ''
  loading.value = false
  ++loadSerial
  closeDetail()
  if (!props.show || !props.account) return
  activeTab.value = 'tickets'
  page.value = 1
  filter.value = 'all'
  modelFilter.value = ''
  startDay.value = ''
  endDay.value = ''
  accountDetail.value = props.account
  participation.value = savedParticipation(props.account)
  reload()
}, { immediate: true })
watch([filter, modelFilter, startDay, endDay], () => { if (activeTab.value !== 'attempts' && activeTab.value !== 'invalidations') return; page.value = 1; loadEvents() })
watch(page, loadEvents)
onUnmounted(() => { ++accountSessionSerial; ++loadSerial; clearTimeout(copiedTimer) })
</script>
