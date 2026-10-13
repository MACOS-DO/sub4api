<template>
  <div>
    <h3 class="text-base font-semibold text-gray-900 dark:text-white">{{ zh ? '打票间隔' : 'Ticket cadence' }}</h3>
    <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ zh ? '失败后随机重试；主动刷新默认 30 分钟，填 0 关闭。需要新票时按批并发打票，拿到票即停。使用页面底部的「保存设置」保存。' : 'Random retry after failure; proactive refresh defaults to 30 minutes (0 disables it). New tickets are harvested in concurrent batches that stop once one succeeds. Use Save settings below.' }}</p>
    <div class="mt-3 grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
      <div v-for="field in fields" :key="field.key">
        <label :for="`codex-ticket-cadence-${field.key}`" class="input-label flex items-center gap-2">
          {{ field.label }}
          <span v-if="field.key === 'refresh_seconds' && value.refresh_seconds === 0 && !loading" class="badge badge-gray">{{ zh ? '已关闭' : 'Off' }}</span>
        </label>
        <div class="relative">
          <input :id="`codex-ticket-cadence-${field.key}`" v-model.number="value[field.key]" type="number" :min="field.min" :max="field.max" class="input pr-10 tabular-nums" :disabled="loading || saving" />
          <span class="pointer-events-none absolute inset-y-0 right-3 flex items-center text-xs text-gray-400 dark:text-dark-400">{{ field.unit }}</span>
        </div>
        <p class="input-hint">{{ field.min }}–{{ field.max }}</p>
      </div>
    </div>
    <p v-if="loadError" class="input-error-text">{{ loadError }}</p>
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
  { key: 'retry_min_seconds' as const, label: zh.value ? '最小重试' : 'Minimum retry', unit: zh.value ? '秒' : 's', min: 1, max: 3600 },
  { key: 'retry_max_seconds' as const, label: zh.value ? '最大重试' : 'Maximum retry', unit: zh.value ? '秒' : 's', min: 1, max: 3600 },
  { key: 'refresh_seconds' as const, label: zh.value ? '主动刷新（0=关闭）' : 'Proactive refresh (0=off)', unit: zh.value ? '秒' : 's', min: 0, max: 86400 },
  { key: 'batch_size' as const, label: zh.value ? '每批打票账号数' : 'Accounts per batch', unit: zh.value ? '个' : '', min: 1, max: 200 }
])
const value = reactive<tickets.TicketCadence>({ retry_min_seconds: 10, retry_max_seconds: 30, refresh_seconds: 1800, batch_size: 10 })
const saved = ref({ ...value })
const loading = ref(true)
const loadError = ref('')

onMounted(async () => {
  try {
    Object.assign(value, await tickets.cadence())
    saved.value = { ...value }
  } catch (cause) {
    loadError.value = extractApiErrorMessage(cause, 'Unable to load cadence')
  } finally {
    loading.value = false
  }
})

function isDirty() {
  return !loading.value && !loadError.value && fields.value.some(field => value[field.key] !== saved.value[field.key])
}

function validate() {
  if (loading.value || loadError.value) throw new Error(loadError.value || (zh.value ? '打票间隔仍在加载' : 'Ticket cadence is still loading'))
  if (!Number.isInteger(value.retry_min_seconds) || !Number.isInteger(value.retry_max_seconds) || !Number.isInteger(value.refresh_seconds) || !Number.isInteger(value.batch_size) || value.retry_min_seconds < 1 || value.retry_max_seconds < value.retry_min_seconds || value.retry_max_seconds > 3600 || value.refresh_seconds < 0 || value.refresh_seconds > 86400 || value.batch_size < 1 || value.batch_size > 200) {
    throw new Error(zh.value ? '请检查打票间隔范围' : 'Check ticket cadence intervals')
  }
}

async function save() {
  if (!isDirty()) return
  validate()
  await tickets.saveCadence({ ...value })
  saved.value = { ...value }
}

defineExpose({ isDirty, validate, save })
</script>
