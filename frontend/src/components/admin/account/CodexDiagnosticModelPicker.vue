<template>
  <div>
    <div class="relative">
      <Icon name="search" size="sm" class="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-gray-400" />
      <input v-model="query" type="search" class="input min-h-11 w-full !pl-10" :placeholder="text('search')" :aria-label="text('search')" :disabled="disabled" />
    </div>
    <div class="flex min-h-12 items-center justify-between gap-3 py-1">
      <p class="text-xs text-gray-500 dark:text-dark-400">{{ text(query.trim() ? 'matchedCount' : 'modelCount', { count: matches.length }) }}</p>
      <button type="button" class="min-h-9 py-1 pl-2 text-xs font-medium text-primary-700 hover:underline disabled:opacity-40 dark:text-primary-300" :disabled="disabled || !modelValue.length" @click="emit('update:modelValue', [])">{{ text('clear') }}</button>
    </div>
    <div class="overflow-hidden rounded-xl border border-gray-200 dark:border-dark-700" :aria-label="text('models')">
      <button
        v-for="model in matches"
        :key="model"
        type="button"
        class="flex min-h-14 w-full items-center gap-3 border-t border-gray-200 px-4 py-3 text-left transition first:border-t-0 hover:bg-gray-50 focus-visible:outline focus-visible:-outline-offset-2 focus-visible:outline-primary-500 disabled:opacity-50 dark:border-dark-700 dark:hover:bg-dark-700"
        :class="modelValue.includes(model) ? 'bg-primary-50 dark:bg-primary-900/25' : 'bg-white dark:bg-dark-800'"
        :aria-label="model"
        :aria-pressed="modelValue.includes(model)"
        :disabled="disabled"
        @click="toggle(model)"
      >
        <span aria-hidden="true" class="flex h-[18px] w-[18px] shrink-0 items-center justify-center rounded-[5px] border" :class="modelValue.includes(model) ? 'border-primary-600 bg-primary-600 text-white' : 'border-gray-400 dark:border-dark-400'">
          <Icon v-if="modelValue.includes(model)" name="check" size="sm" :stroke-width="2.5" />
        </span>
        <span class="min-w-0 flex-1 font-mono text-[13px] leading-6 text-gray-900 [overflow-wrap:anywhere] dark:text-dark-100">{{ model }}</span>
        <span v-if="modelValue.includes(model)" class="shrink-0 text-xs tabular-nums text-primary-700 dark:text-primary-300" :aria-label="text('order', { index: modelValue.indexOf(model) + 1 })">#{{ modelValue.indexOf(model) + 1 }}</span>
      </button>
      <p v-if="!matches.length" class="px-4 py-8 text-center text-sm text-gray-500 dark:text-dark-400">{{ text('emptySearch') }}</p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'

const props = defineProps<{ models: string[]; modelValue: string[]; disabled?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [models: string[]] }>()
const { t } = useI18n()
const text = (key: string, values: Record<string, string | number> = {}) => t(`admin.accounts.codexDiagnosticDialog.${key}`, values)
const query = ref('')
const matches = computed(() => props.models.filter(model => model.toLowerCase().includes(query.value.trim().toLowerCase())))
function toggle(model: string) {
  if (props.disabled) return
  emit('update:modelValue', props.modelValue.includes(model)
    ? props.modelValue.filter(selected => selected !== model)
    : [...props.modelValue, model])
}
</script>
