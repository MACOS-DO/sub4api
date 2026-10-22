<template>
  <div class="rounded-xl border border-indigo-100 bg-indigo-50/50 p-4 dark:border-indigo-900 dark:bg-indigo-950/30">
    <h3 class="font-semibold text-slate-900 dark:text-white">{{ zh ? '打票间隔' : 'Ticket cadence' }}</h3>
    <p class="mt-1 text-xs text-slate-500">{{ zh ? '失败后随机重试；主动刷新默认 30 分钟，填 0 关闭。使用页面底部的「保存设置」保存。' : 'Random retry after failure; proactive refresh defaults to 30 minutes (0 disables it). Use Save settings below.' }}</p>
    <div class="mt-3 grid gap-3 sm:grid-cols-3">
      <label v-for="field in fields" :key="field.key" class="text-xs text-slate-600 dark:text-slate-300">
        {{ field.label }}
        <input v-model.number="value[field.key]" type="number" :min="field.min" :max="field.max" class="input mt-1 w-full" :disabled="loading || saving" />
      </label>
    </div>
    <div class="mt-4 border-t border-indigo-100 pt-4 dark:border-indigo-900">
      <h3 class="font-semibold text-slate-900 dark:text-white">{{ zh ? '打票出口 IP' : 'Ticket egress IP' }}</h3>
      <p class="mt-1 text-xs text-slate-500">{{ zh ? '决定打票请求从哪个 IP 发出，只影响打票，不影响业务请求的出口。' : 'Chooses which IP ticket requests leave from. Only affects ticket harvesting, not regular traffic.' }}</p>
      <div class="mt-3 grid gap-2 sm:grid-cols-2">
        <label v-for="option in sourceOptions" :key="option.value" class="flex cursor-pointer items-start gap-2 rounded-lg border bg-white px-3 py-2 dark:bg-slate-900" :class="proxySource === option.value ? 'border-indigo-400 dark:border-indigo-500' : 'border-slate-200 dark:border-slate-700'">
          <input v-model="proxySource" type="radio" name="codex-ticket-proxy-source" class="mt-0.5" :value="option.value" :data-testid="`codex-ticket-proxy-source-${option.value}`" :disabled="loading || saving" />
          <span class="min-w-0">
            <span class="block text-sm font-medium text-slate-900 dark:text-white">{{ option.label }}</span>
            <span class="mt-0.5 block text-xs text-slate-500">{{ option.hint }}</span>
          </span>
        </label>
      </div>
    </div>
    <p v-if="loadError" class="mt-3 text-xs text-rose-600">{{ loadError }}</p>
  </div>
</template>
<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import * as tickets from '@/api/admin/codexTickets'
import { extractApiErrorMessage } from '@/utils/apiError'

defineProps<{ saving: boolean }>()
const { locale } = useI18n()
const zh = computed(() => locale.value.startsWith('zh'))
const fields = computed(() => [
  { key: 'retry_min_seconds' as const, label: zh.value ? '最小重试（秒）' : 'Minimum retry (s)', min: 1, max: 3600 },
  { key: 'retry_max_seconds' as const, label: zh.value ? '最大重试（秒）' : 'Maximum retry (s)', min: 1, max: 3600 },
  { key: 'refresh_seconds' as const, label: zh.value ? '主动刷新（秒，0=关闭）' : 'Proactive refresh (s; 0=off)', min: 0, max: 86400 }
])
const sourceOptions = computed(() => [
  {
    value: 'pool' as const,
    label: zh.value ? '使用打票代理池（默认）' : 'Ticket proxy pool (default)',
    hint: zh.value ? '在「IP管理」页配置的 Codex 打票代理池中轮换；池内无可用代理时不打票，不会直连。' : 'Rotates through the Codex ticket proxy pool configured on the IP page; skips harvesting when the pool is empty and never connects directly.'
  },
  {
    value: 'account' as const,
    label: zh.value ? '使用账号自身的代理' : "Account's own proxy",
    hint: zh.value ? '用账号绑定的代理打票，出口 IP 与该账号的业务请求一致；账号未绑定代理时直连（服务器 IP）。' : "Harvests through the proxy bound to each account, so the ticket IP matches that account's traffic; accounts without a proxy connect directly (server IP)."
  }
])
const value = reactive({ retry_min_seconds: 10, retry_max_seconds: 30, refresh_seconds: 1800 })
const saved = ref({ ...value })
const proxySource = ref<tickets.TicketProxySource>('pool')
const savedProxySource = ref<tickets.TicketProxySource>('pool')
const loading = ref(true)
const loadError = ref('')

onMounted(async () => {
  try {
    const [cadence, source] = await Promise.all([tickets.cadence(), tickets.proxySource()])
    Object.assign(value, cadence)
    saved.value = { ...value }
    proxySource.value = savedProxySource.value = source.source
  } catch (cause) {
    loadError.value = extractApiErrorMessage(cause, 'Unable to load cadence')
  } finally {
    loading.value = false
  }
})

function cadenceDirty() {
  return fields.value.some(field => value[field.key] !== saved.value[field.key])
}

function isDirty() {
  return !loading.value && !loadError.value && (cadenceDirty() || proxySource.value !== savedProxySource.value)
}

function validate() {
  if (loading.value || loadError.value) throw new Error(loadError.value || (zh.value ? '打票间隔仍在加载' : 'Ticket cadence is still loading'))
  if (!Number.isInteger(value.retry_min_seconds) || !Number.isInteger(value.retry_max_seconds) || !Number.isInteger(value.refresh_seconds) || value.retry_min_seconds < 1 || value.retry_max_seconds < value.retry_min_seconds || value.retry_max_seconds > 3600 || value.refresh_seconds < 0 || value.refresh_seconds > 86400) {
    throw new Error(zh.value ? '请检查打票间隔范围' : 'Check ticket cadence intervals')
  }
}

async function save() {
  if (!isDirty()) return
  validate()
  if (cadenceDirty()) {
    await tickets.saveCadence({ ...value })
    saved.value = { ...value }
  }
  if (proxySource.value !== savedProxySource.value) {
    await tickets.saveProxySource(proxySource.value)
    savedProxySource.value = proxySource.value
  }
}

defineExpose({ isDirty, validate, save })
</script>
