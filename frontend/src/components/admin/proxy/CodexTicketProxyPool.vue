<template>
  <section class="w-full rounded-xl border border-indigo-100 bg-indigo-50/60 p-4 dark:border-indigo-900 dark:bg-indigo-950/30 sm:p-5">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div><h3 class="font-semibold text-slate-900 dark:text-white">{{ zh ? 'Codex 打票代理池' : 'Codex ticket proxy pool' }}</h3><p class="mt-1 text-xs text-slate-600 dark:text-slate-400">{{ zh ? '复用 IP 设置中的可用代理，按池轮换；无可用代理时不直连。' : 'Rotate available proxies from IP settings; never connect directly without one.' }}</p></div>
      <button type="button" class="btn btn-primary !py-1.5 text-xs" :disabled="saving || loading || (mode === 'custom' && !selected.length)" @click="save">{{ saving ? '…' : zh ? '保存代理池' : 'Save pool' }}</button>
    </div>
    <div v-if="error" class="mt-3 text-xs text-rose-600">{{ error }}</div>
    <div v-else-if="saved" role="status" class="mt-3 text-xs text-emerald-600">{{ zh ? '代理池已保存' : 'Proxy pool saved' }}</div>
    <div class="mt-4 flex flex-wrap gap-4 text-sm"><label class="flex cursor-pointer items-center gap-2"><input v-model="mode" type="radio" value="all" />{{ zh ? '全部可用代理' : 'All available proxies' }}</label><label class="flex cursor-pointer items-center gap-2"><input v-model="mode" type="radio" value="custom" />{{ zh ? '自定义选择' : 'Custom selection' }}</label></div>
    <div v-if="mode === 'custom'" class="mt-3 grid max-h-40 gap-2 overflow-y-auto sm:grid-cols-2 lg:grid-cols-3"><label v-for="proxy in proxies" :key="proxy.id" class="flex min-w-0 cursor-pointer items-center gap-2 rounded-lg border border-slate-200 bg-white px-3 py-2 text-xs dark:border-slate-700 dark:bg-slate-900"><input v-model="selected" type="checkbox" :value="proxy.id" /><span class="truncate">{{ proxy.name }}</span></label><span v-if="!proxies.length" class="text-xs text-amber-700 dark:text-amber-400">{{ zh ? '无可用代理，请先添加代理。' : 'No available proxies; add one first.' }}</span></div>
  </section>
</template>
<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { apiClient } from '@/api/client'
import { extractApiErrorMessage } from '@/utils/apiError'
import type { Proxy } from '@/types'
const { locale } = useI18n()
const zh = computed(() => locale.value.startsWith('zh'))
const mode = ref('all')
const selected = ref<number[]>([])
const proxies = ref<Proxy[]>([])
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const saved = ref(false)
onMounted(async () => { loading.value = true; try { const [pool, available] = await Promise.all([apiClient.get<{ mode: string; proxy_ids: number[] }>('/admin/proxies/codex-ticket-pool'), adminAPI.proxies.getAll()]); mode.value = pool.data.mode; selected.value = pool.data.proxy_ids ?? []; proxies.value = available } catch (cause) { error.value = extractApiErrorMessage(cause, 'Unable to load proxy pool') } finally { loading.value = false } })
async function save() { saving.value = true; error.value = ''; saved.value = false; try { await apiClient.put('/admin/proxies/codex-ticket-pool', { mode: mode.value, proxy_ids: mode.value === 'all' ? [] : selected.value }); saved.value = true } catch (cause) { error.value = extractApiErrorMessage(cause, 'Unable to save proxy pool') } finally { saving.value = false } }
</script>
