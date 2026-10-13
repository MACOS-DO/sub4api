<template>
  <BaseDialog :show="show" :title="zh ? 'Codex 打票代理池' : 'Codex ticket proxy pool'" width="normal" @close="emit('close')">
    <p class="text-sm text-gray-500 dark:text-dark-400">{{ zh ? '打票请求按池轮换代理；无可用代理时不直连。' : 'Ticket requests rotate through the pool; never connect directly without a proxy.' }}</p>
    <div v-if="loading" role="status" class="flex items-center justify-center gap-2 py-8 text-sm text-gray-500 dark:text-dark-400"><span class="spinner !h-4 !w-4" />{{ zh ? '正在加载…' : 'Loading…' }}</div>
    <template v-else>
      <div class="mt-4 grid grid-cols-2 gap-2 text-sm">
        <label v-for="option in modeOptions" :key="option.value" class="flex cursor-pointer items-center gap-2 rounded-xl border px-3 py-2.5 transition-colors" :class="mode === option.value ? 'border-primary-500 bg-primary-50 text-primary-700 dark:bg-primary-900/20 dark:text-primary-300' : 'border-gray-200 text-gray-700 hover:border-gray-300 dark:border-dark-600 dark:text-dark-200 dark:hover:border-dark-500'"><input v-model="mode" type="radio" :value="option.value" class="text-primary-600 focus:ring-primary-500" />{{ option.label }}</label>
      </div>
      <div v-if="mode === 'custom'" class="mt-4">
        <div class="flex items-center gap-2">
          <input v-model="keyword" type="search" class="input flex-1 !py-2" :placeholder="zh ? '按名称搜索代理' : 'Search proxies by name'" />
          <button type="button" class="btn btn-secondary btn-sm" :disabled="!filtered.length" @click="selectFiltered">{{ zh ? '全选' : 'Select all' }}</button>
          <button type="button" class="btn btn-secondary btn-sm" :disabled="!selected.length" @click="selected = []">{{ zh ? '清空' : 'Clear' }}</button>
        </div>
        <p class="mt-2 text-xs tabular-nums text-gray-500 dark:text-dark-400">{{ zh ? `已选 ${selected.length} / ${proxies.length}` : `${selected.length} of ${proxies.length} selected` }}</p>
        <div class="mt-2 max-h-72 space-y-1 overflow-y-auto">
          <label v-for="proxy in filtered" :key="proxy.id" class="flex min-w-0 cursor-pointer items-center gap-2 rounded-xl border px-3 py-2 text-sm transition-colors" :class="selected.includes(proxy.id) ? 'border-primary-500 bg-primary-50 text-primary-700 dark:border-primary-500 dark:bg-primary-900/20 dark:text-primary-300' : 'border-gray-200 text-gray-700 hover:border-gray-300 dark:border-dark-600 dark:text-dark-200 dark:hover:border-dark-500'"><input v-model="selected" type="checkbox" :value="proxy.id" class="h-4 w-4 shrink-0 rounded border-gray-300 text-primary-600 focus:ring-primary-500 dark:border-dark-500" /><span class="truncate" :title="proxy.name">{{ proxy.name }}</span><span class="ml-auto shrink-0 font-mono text-xs text-gray-400 dark:text-dark-500">{{ proxy.protocol }}://{{ proxy.host }}:{{ proxy.port }}</span></label>
          <p v-if="!proxies.length" class="text-xs text-amber-700 dark:text-amber-400">{{ zh ? '无可用代理，请先添加代理。' : 'No available proxies; add one first.' }}</p>
          <p v-else-if="!filtered.length" class="py-4 text-center text-xs text-gray-500 dark:text-dark-400">{{ zh ? '没有匹配的代理' : 'No matching proxies' }}</p>
        </div>
      </div>
    </template>
    <p v-if="error" role="alert" class="mt-3 text-xs text-red-600 dark:text-red-400">{{ error }}</p>
    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" @click="emit('close')">{{ zh ? '取消' : 'Cancel' }}</button>
        <button type="button" class="btn btn-primary" :disabled="saving || loading || (mode === 'custom' && !selected.length)" @click="save"><span v-if="saving" class="spinner !h-4 !w-4" />{{ zh ? '保存' : 'Save' }}</button>
      </div>
    </template>
  </BaseDialog>
</template>
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import { apiClient } from '@/api/client'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import BaseDialog from '@/components/common/BaseDialog.vue'
import type { Proxy } from '@/types'
const props = defineProps<{ show: boolean }>()
const emit = defineEmits<{ close: [] }>()
const { locale } = useI18n()
const appStore = useAppStore()
const zh = computed(() => locale.value.startsWith('zh'))
const modeOptions = computed(() => [{ value: 'all', label: zh.value ? '全部可用代理' : 'All available proxies' }, { value: 'custom', label: zh.value ? '自定义选择' : 'Custom selection' }])
const mode = ref('all')
const selected = ref<number[]>([])
const proxies = ref<Proxy[]>([])
const keyword = ref('')
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const filtered = computed(() => { const q = keyword.value.trim().toLowerCase(); return q ? proxies.value.filter(proxy => proxy.name.toLowerCase().includes(q)) : proxies.value })
function selectFiltered() { selected.value = Array.from(new Set([...selected.value, ...filtered.value.map(proxy => proxy.id)])) }
async function load() { loading.value = true; error.value = ''; keyword.value = ''; try { const [pool, available] = await Promise.all([apiClient.get<{ mode: string; proxy_ids: number[] }>('/admin/proxies/codex-ticket-pool'), adminAPI.proxies.getAll()]); mode.value = pool.data.mode; selected.value = pool.data.proxy_ids ?? []; proxies.value = available } catch (cause) { error.value = extractApiErrorMessage(cause, 'Unable to load proxy pool') } finally { loading.value = false } }
async function save() { saving.value = true; error.value = ''; try { await apiClient.put('/admin/proxies/codex-ticket-pool', { mode: mode.value, proxy_ids: mode.value === 'all' ? [] : selected.value }); appStore.showSuccess(zh.value ? '代理池已保存' : 'Proxy pool saved'); emit('close') } catch (cause) { error.value = extractApiErrorMessage(cause, 'Unable to save proxy pool') } finally { saving.value = false } }
watch(() => props.show, open => { if (open) void load() }, { immediate: true })
</script>
