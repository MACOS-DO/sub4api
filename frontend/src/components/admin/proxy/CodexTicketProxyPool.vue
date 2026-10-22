<template>
  <section class="w-full rounded-xl border border-indigo-100 bg-indigo-50/60 p-4 dark:border-indigo-900 dark:bg-indigo-950/30 sm:p-5">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div>
        <h3 class="font-semibold text-slate-900 dark:text-white">{{ zh ? 'Codex 打票代理池' : 'Codex ticket proxy pool' }}</h3>
        <p class="mt-1 text-xs text-slate-600 dark:text-slate-400">{{ zh ? '复用 IP 设置中的可用代理，按池轮换；无可用代理时不直连。仅在系统设置「打票出口 IP」选择代理池时生效。修改后需点击「保存代理池」。' : 'Rotate available proxies from IP settings; never connect directly without one. Applies only when Settings → Ticket egress IP uses the pool. Click Save pool to apply changes.' }}</p>
      </div>
      <div class="flex items-center gap-2">
        <span v-if="dirty" class="text-xs font-medium text-amber-600 dark:text-amber-400" data-testid="pool-dirty">{{ zh ? '有未保存的修改' : 'Unsaved changes' }}</span>
        <button type="button" class="btn !py-1.5 text-xs" :class="dirty ? 'btn-primary' : 'btn-secondary'" data-testid="pool-save" :disabled="saving || loading || !dirty || (mode === 'custom' && !selected.length)" @click="save">{{ saving ? '…' : zh ? '保存代理池' : 'Save pool' }}</button>
      </div>
    </div>

    <div v-if="error" class="mt-3 text-xs text-rose-600">{{ error }}</div>
    <div v-else-if="justSaved && !dirty" role="status" class="mt-3 text-xs text-emerald-600">{{ zh ? '代理池已保存' : 'Proxy pool saved' }}</div>

    <div v-if="view" class="mt-3 rounded-lg bg-white/70 px-3 py-2 text-xs dark:bg-slate-900/60" data-testid="pool-effective">
      <template v-if="view.source === 'account'">
        <span class="font-medium text-amber-700 dark:text-amber-400">{{ zh ? '当前未生效：系统设置中「打票出口 IP」为「使用账号自身的代理」，打票走各账号绑定的代理，此代理池不参与。' : "Not in effect: Settings → Ticket egress IP is set to each account's own proxy, so this pool is ignored." }}</span>
      </template>
      <template v-else-if="view.effective.length">
        <span class="text-slate-500 dark:text-slate-400">{{ zh ? '当前打票轮换：' : 'Tickets currently rotate through: ' }}</span>
        <span class="font-medium text-slate-900 dark:text-white">{{ view.effective.map(proxy => proxy.name).join('、') }}</span>
      </template>
      <span v-else class="font-medium text-rose-600">{{ zh ? '当前没有可用代理，不会打票。' : 'No usable proxy; tickets will not be harvested.' }}</span>
    </div>

    <div class="mt-4 flex flex-wrap gap-4 text-sm">
      <label class="flex cursor-pointer items-center gap-2"><input v-model="mode" type="radio" value="all" data-testid="pool-mode-all" :disabled="loading || saving" />{{ zh ? '全部可用代理' : 'All available proxies' }}</label>
      <label class="flex cursor-pointer items-center gap-2"><input v-model="mode" type="radio" value="custom" data-testid="pool-mode-custom" :disabled="loading || saving" />{{ zh ? '自定义选择' : 'Custom selection' }}</label>
    </div>

    <div v-if="mode === 'custom'" class="mt-3 space-y-2">
      <div class="grid max-h-48 gap-2 overflow-y-auto sm:grid-cols-2 lg:grid-cols-3">
        <label v-for="proxy in proxies" :key="proxy.id" class="flex min-w-0 cursor-pointer items-center gap-2 rounded-lg border border-slate-200 bg-white px-3 py-2 text-xs dark:border-slate-700 dark:bg-slate-900">
          <input v-model="selected" type="checkbox" :value="proxy.id" :data-testid="`pool-proxy-${proxy.id}`" :disabled="loading || saving" />
          <span class="min-w-0 truncate">
            <span class="font-medium text-slate-900 dark:text-white">{{ proxy.name }}</span>
            <span class="text-slate-500"> · {{ proxy.host }}:{{ proxy.port }}</span>
          </span>
          <span v-if="isExpired(proxy)" class="ml-auto shrink-0 text-amber-600">{{ zh ? '已过期，不参与打票' : 'Expired, not used' }}</span>
        </label>
        <span v-if="!loading && !proxies.length" class="text-xs text-amber-700 dark:text-amber-400">{{ zh ? '无可用代理，请先添加代理。' : 'No available proxies; add one first.' }}</span>
      </div>
      <div v-if="hiddenSelections.length" class="flex flex-wrap items-center gap-2 text-xs" data-testid="pool-hidden">
        <span class="text-amber-700 dark:text-amber-400">{{ zh ? '已勾选但当前不可用（不会参与打票）：' : 'Selected but unavailable (not used):' }}</span>
        <button v-for="item in hiddenSelections" :key="item.id" type="button" class="rounded-full border border-amber-300 px-2 py-0.5 text-amber-700 hover:bg-amber-50 dark:border-amber-700 dark:text-amber-400" :data-testid="`pool-hidden-${item.id}`" @click="removeSelection(item.id)">
          {{ item.label }} ×
        </button>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { onBeforeRouteLeave } from 'vue-router'
import { adminAPI } from '@/api/admin'
import { apiClient } from '@/api/client'
import { extractApiErrorMessage } from '@/utils/apiError'
import type { Proxy } from '@/types'

type PoolMode = 'all' | 'custom'
interface PoolProxyRef { id: number; name?: string; reason?: 'deleted' | 'disabled' | 'expired' }
interface PoolView {
  mode: PoolMode
  proxy_ids: number[] | null
  source: 'pool' | 'account'
  effective: PoolProxyRef[]
  unavailable: PoolProxyRef[]
}

const { locale } = useI18n()
const zh = computed(() => locale.value.startsWith('zh'))
const mode = ref<PoolMode>('all')
const selected = ref<number[]>([])
const proxies = ref<Proxy[]>([])
const view = ref<PoolView | null>(null)
const loading = ref(true)
const saving = ref(false)
const error = ref('')
const justSaved = ref(false)

const normalizedSelection = (ids: number[]) => [...new Set(ids)].sort((a, b) => a - b)
const dirty = computed(() => {
  if (loading.value || !view.value) return false
  if (mode.value !== view.value.mode) return true
  if (mode.value === 'all') return false
  return normalizedSelection(selected.value).join(',') !== normalizedSelection(view.value.proxy_ids ?? []).join(',')
})

// Selected IDs without a visible checkbox (deleted or disabled proxies) used to be kept silently
// and re-submitted; list them so they can be removed instead.
const hiddenSelections = computed(() => {
  const visible = new Set(proxies.value.map(proxy => proxy.id))
  const known = new Map((view.value?.unavailable ?? []).map(item => [item.id, item]))
  return selected.value.filter(id => !visible.has(id)).map(id => {
    const item = known.get(id)
    const reason = item?.reason === 'disabled' ? (zh.value ? '已停用' : 'disabled') : (zh.value ? '已删除' : 'deleted')
    return { id, label: `${item?.name || `#${id}`}（${reason}）` }
  })
})

function isExpired(proxy: Proxy) {
  return !!proxy.expires_at && new Date(proxy.expires_at).getTime() <= Date.now()
}

function removeSelection(id: number) {
  selected.value = selected.value.filter(value => value !== id)
}

function applyView(next: PoolView) {
  view.value = next
  mode.value = next.mode
  selected.value = [...(next.proxy_ids ?? [])]
}

async function reload() {
  // Read before loading flips: dirty is always false while loading.
  const keepEdits = dirty.value
  loading.value = true
  error.value = ''
  try {
    const [pool, available] = await Promise.all([
      apiClient.get<PoolView>('/admin/proxies/codex-ticket-pool'),
      adminAPI.proxies.getAll()
    ])
    proxies.value = available
    // Keep unsaved edits when only the proxy list is being refreshed.
    if (keepEdits) {
      view.value = pool.data
    } else {
      applyView(pool.data)
    }
  } catch (cause) {
    error.value = extractApiErrorMessage(cause, 'Unable to load proxy pool')
  } finally {
    loading.value = false
  }
}

async function save() {
  saving.value = true
  error.value = ''
  justSaved.value = false
  try {
    const visible = new Set(proxies.value.map(proxy => proxy.id))
    const proxyIDs = mode.value === 'all' ? [] : selected.value.filter(id => visible.has(id))
    const { data } = await apiClient.put<PoolView>('/admin/proxies/codex-ticket-pool', { mode: mode.value, proxy_ids: proxyIDs })
    applyView(data)
    justSaved.value = true
  } catch (cause) {
    error.value = extractApiErrorMessage(cause, 'Unable to save proxy pool')
  } finally {
    saving.value = false
  }
}

const unsavedMessage = computed(() => zh.value ? 'Codex 打票代理池有未保存的修改，确定离开？' : 'The Codex ticket proxy pool has unsaved changes. Leave anyway?')
function beforeUnload(event: BeforeUnloadEvent) {
  if (!dirty.value) return
  event.preventDefault()
  event.returnValue = unsavedMessage.value
}
onBeforeRouteLeave(() => !dirty.value || window.confirm(unsavedMessage.value))
onMounted(() => {
  window.addEventListener('beforeunload', beforeUnload)
  void reload()
})
onBeforeUnmount(() => window.removeEventListener('beforeunload', beforeUnload))

defineExpose({ reload, dirty })
</script>
