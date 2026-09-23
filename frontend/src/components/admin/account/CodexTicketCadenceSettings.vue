<template>
  <div class="rounded-xl border border-indigo-100 bg-indigo-50/50 p-4 dark:border-indigo-900 dark:bg-indigo-950/30">
    <h3 class="font-semibold text-slate-900 dark:text-white">{{ zh ? '打票间隔' : 'Ticket cadence' }}</h3>
    <p class="mt-1 text-xs text-slate-500">{{ zh ? '失败后随机重试；主动刷新与重试独立，填 0 关闭。代理池在「IP 设置」中配置。' : 'Random retry after failure; proactive refresh is independent (0 disables it). Configure the proxy pool in IP settings.' }}</p>
    <div class="mt-3 grid gap-3 sm:grid-cols-3"><label v-for="field in fields" :key="field.key" class="text-xs text-slate-600 dark:text-slate-300">{{ field.label }}<input v-model.number="value[field.key]" type="number" :min="field.min" :max="field.max" class="input mt-1 w-full" /></label></div>
    <div class="mt-3 flex items-center gap-3"><button type="button" class="btn btn-secondary !py-1.5 text-xs" :disabled="saving || loading" @click="save">{{ saving ? '…' : zh ? '保存间隔' : 'Save cadence' }}</button><span v-if="message" class="text-xs" :class="failed ? 'text-rose-600' : 'text-emerald-600'">{{ message }}</span></div>
  </div>
</template>
<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import * as tickets from '@/api/admin/codexTickets'
import { extractApiErrorMessage } from '@/utils/apiError'
const { locale } = useI18n()
const zh = computed(() => locale.value.startsWith('zh'))
const fields = computed(() => [
  { key: 'retry_min_seconds' as const, label: zh.value ? '最小重试（秒）' : 'Minimum retry (s)', min: 1, max: 3600 },
  { key: 'retry_max_seconds' as const, label: zh.value ? '最大重试（秒）' : 'Maximum retry (s)', min: 1, max: 3600 },
  { key: 'refresh_seconds' as const, label: zh.value ? '主动刷新（秒，0=关闭）' : 'Proactive refresh (s; 0=off)', min: 0, max: 86400 }
])
const value = reactive({ retry_min_seconds: 10, retry_max_seconds: 30, refresh_seconds: 0 })
const loading = ref(false)
const saving = ref(false)
const failed = ref(false)
const message = ref('')
onMounted(async () => { loading.value = true; try { Object.assign(value, await tickets.cadence()) } catch (cause) { failed.value = true; message.value = extractApiErrorMessage(cause, 'Unable to load cadence') } finally { loading.value = false } })
async function save() { failed.value = false; message.value = ''; if (!Number.isInteger(value.retry_min_seconds) || !Number.isInteger(value.retry_max_seconds) || !Number.isInteger(value.refresh_seconds) || value.retry_min_seconds < 1 || value.retry_max_seconds < value.retry_min_seconds || value.retry_max_seconds > 3600 || value.refresh_seconds < 0 || value.refresh_seconds > 86400) { failed.value = true; message.value = zh.value ? '请检查间隔范围' : 'Check interval range'; return } saving.value = true; try { await tickets.saveCadence(value); message.value = zh.value ? '已保存' : 'Saved' } catch (cause) { failed.value = true; message.value = extractApiErrorMessage(cause, 'Unable to save cadence') } finally { saving.value = false } }
</script>
