<template>
  <div class="overflow-hidden rounded-xl border border-gray-200 bg-white dark:border-dark-600 dark:bg-dark-800">
    <div class="flex flex-wrap items-center justify-between gap-2 border-b border-gray-100 px-4 py-3 dark:border-dark-700">
      <span class="text-sm font-medium text-gray-700 dark:text-dark-200">{{ labels.selected }} <strong class="text-primary-600 dark:text-primary-400">{{ modelValue.length }}</strong> {{ labels.count }}</span>
      <button type="button" class="text-xs font-medium text-primary-600 hover:underline disabled:opacity-40 dark:text-primary-400" :disabled="disabled || !modelValue.length" @click="emit('update:modelValue', [])">{{ labels.clear }}</button>
    </div>
    <div v-if="modelValue.length" class="px-4 pt-3">
      <div ref="chipsElement" class="flex flex-wrap gap-2" :class="expanded ? '' : 'max-h-16 overflow-hidden'">
        <span v-for="(model, modelIndex) in modelValue" :key="model" class="max-w-full items-center gap-1.5 rounded-md bg-primary-50 px-2 py-1 font-mono text-xs text-primary-700 dark:bg-primary-900/30 dark:text-primary-300" :class="!expanded && modelIndex >= visibleChipCount ? 'hidden' : 'inline-flex'">
          <span class="truncate" :title="model">{{ model }}</span>
          <button type="button" class="shrink-0 rounded p-0.5 hover:bg-primary-100 focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500 disabled:opacity-50 dark:hover:bg-primary-900" :disabled="disabled" :aria-label="labels.remove + ' ' + model" @click="toggle(model)">×</button>
        </span>
      </div>
      <button v-if="expanded || hiddenChipCount" type="button" class="mt-2 text-xs font-medium text-primary-600 hover:underline disabled:opacity-50 dark:text-primary-400" :aria-expanded="expanded" :disabled="disabled" @click="expanded = !expanded">{{ expanded ? labels.collapse : labels.more + ' ' + hiddenChipCount + ' ' + labels.expand }}</button>
    </div>
    <div class="p-3">
      <div class="relative">
        <Icon name="search" size="sm" class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-gray-400" />
        <input v-model="query" type="search" class="input w-full !pl-9" :placeholder="labels.search" :aria-label="labels.search" :disabled="disabled" />
      </div>
      <div class="mt-3 max-h-56 space-y-1 overflow-y-auto pr-1" :aria-label="labels.models">
        <template v-for="group in groups" :key="group.label">
          <template v-if="group.models.length">
            <div class="px-2 pb-1 pt-2 text-[11px] font-semibold uppercase tracking-wide text-gray-500 dark:text-dark-400">{{ group.label }}</div>
            <button
              v-for="model in group.models"
              :key="model"
              type="button"
              class="flex w-full items-center gap-3 rounded-lg px-3 py-2 text-left transition hover:bg-gray-50 focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500 disabled:opacity-50 dark:hover:bg-dark-700"
              :class="modelValue.includes(model) ? 'bg-primary-50/70 dark:bg-primary-900/20' : ''"
              :aria-pressed="modelValue.includes(model)"
              :disabled="disabled"
              @click="toggle(model)"
            >
              <span class="flex h-5 w-5 shrink-0 items-center justify-center rounded-md border text-xs font-bold" :class="modelValue.includes(model) ? 'border-primary-500 bg-primary-500 text-white' : 'border-gray-300 dark:border-dark-500'">{{ modelValue.includes(model) ? '✓' : '' }}</span>
              <span class="min-w-0 flex-1 truncate font-mono text-xs font-medium text-gray-800 dark:text-dark-100">{{ model }}</span>
              <span class="shrink-0 text-[11px] text-gray-500 dark:text-dark-400">{{ group.hint }}</span>
            </button>
          </template>
        </template>
        <p v-if="!groups.some(group => group.models.length)" class="px-3 py-6 text-center text-sm text-gray-500">{{ labels.empty }}</p>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'

const props = defineProps<{
  models: string[]
  ticketModels: string[]
  modelValue: string[]
  disabled?: boolean
}>()
const emit = defineEmits<{ 'update:modelValue': [models: string[]] }>()
const { locale } = useI18n()
const query = ref('')
const chipsElement = ref<HTMLElement | null>(null)
const expanded = ref(false)
const visibleChipCount = ref(props.modelValue.length)
const hiddenChipCount = computed(() => Math.max(0, props.modelValue.length - visibleChipCount.value))
let measurementSerial = 0
let resizeObserver: ResizeObserver | null = null
async function measureChips() {
  const serial = ++measurementSerial
  visibleChipCount.value = props.modelValue.length
  await nextTick()
  if (serial !== measurementSerial || !chipsElement.value || !chipsElement.value.clientWidth) return
  const chips = Array.from(chipsElement.value.children) as HTMLElement[]
  const rows = [...new Set(chips.map(chip => chip.offsetTop))].sort((first, second) => first - second)
  if (rows.length > 2) visibleChipCount.value = chips.filter(chip => chip.offsetTop <= rows[1]).length
}
watch(chipsElement, element => {
  resizeObserver?.disconnect()
  if (!element) return
  let previousWidth = element.clientWidth
  if (typeof ResizeObserver !== 'undefined') {
    resizeObserver = new ResizeObserver(entries => {
      const width = entries[0]?.contentRect.width ?? 0
      if (width !== previousWidth) { previousWidth = width; void measureChips() }
    })
    resizeObserver.observe(element)
  }
  void measureChips()
}, { flush: 'post' })
watch(() => props.modelValue.slice(), () => {
  if (!props.modelValue.length) expanded.value = false
  void measureChips()
}, { flush: 'post' })
onUnmounted(() => { ++measurementSerial; resizeObserver?.disconnect() })
const labels = computed(() => locale.value.startsWith('zh') ? {
  more: '还有', expand: '个，展开', collapse: '收起已选模型',
  selected: '已选择', count: '个模型', clear: '清空选择', remove: '移除', search: '搜索模型名称', models: 'GPT 模型',
  ticket: '需要打票', ticketHint: '按指纹库打票', direct: '无需打票', directHint: '直接检测', empty: '没有匹配的模型'
} : {
  more: 'Show', expand: 'more', collapse: 'Collapse selected models',
  selected: 'Selected', count: 'models', clear: 'Clear selection', remove: 'Remove', search: 'Search models', models: 'GPT models',
  ticket: 'Ticket required', ticketHint: 'Fingerprint ticket', direct: 'No ticket', directHint: 'Direct check', empty: 'No matching models'
})
const groups = computed(() => {
  const search = query.value.trim().toLowerCase()
  const matches = props.models.filter(model => model.toLowerCase().includes(search))
  const ticketSet = new Set(props.ticketModels)
  return [
    { label: labels.value.ticket, hint: labels.value.ticketHint, models: matches.filter(model => ticketSet.has(model)) },
    { label: labels.value.direct, hint: labels.value.directHint, models: matches.filter(model => !ticketSet.has(model)) }
  ]
})
function toggle(model: string) {
  if (props.disabled) return
  emit('update:modelValue', props.modelValue.includes(model)
    ? props.modelValue.filter(selected => selected !== model)
    : [...props.modelValue, model])
}
</script>
