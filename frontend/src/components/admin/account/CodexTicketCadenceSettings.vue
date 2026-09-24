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
const value = reactive({ retry_min_seconds: 10, retry_max_seconds: 30, refresh_seconds: 1800 })
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
  if (!Number.isInteger(value.retry_min_seconds) || !Number.isInteger(value.retry_max_seconds) || !Number.isInteger(value.refresh_seconds) || value.retry_min_seconds < 1 || value.retry_max_seconds < value.retry_min_seconds || value.retry_max_seconds > 3600 || value.refresh_seconds < 0 || value.refresh_seconds > 86400) {
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
