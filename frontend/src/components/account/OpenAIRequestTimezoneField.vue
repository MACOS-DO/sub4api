<template>
  <div class="border-t border-gray-200 pt-4 dark:border-dark-600">
    <label class="input-label">{{ t('admin.accounts.openai.requestTimezone') }}</label>
    <div class="mt-1 flex flex-wrap gap-4 text-sm" role="radiogroup" :aria-label="t('admin.accounts.openai.requestTimezone')">
      <label v-for="option in modeOptions" :key="option.value" class="flex cursor-pointer items-center gap-2 text-gray-700 dark:text-gray-300">
        <input
          type="radio"
          name="openai-request-timezone-mode"
          :value="option.value"
          :checked="mode === option.value"
          :data-testid="`openai-request-timezone-mode-${option.value}`"
          @change="emit('update:mode', option.value)"
        />
        {{ option.label }}
      </label>
    </div>

    <div v-if="mode === 'follow_proxy'" class="mt-2 space-y-1" data-testid="openai-request-timezone-follow">
      <p class="input-hint">{{ t('admin.accounts.openai.requestTimezoneFollowDesc') }}</p>
      <div v-if="accountId" class="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
        <span v-if="detection?.timezone && !detection.error" class="text-gray-700 dark:text-gray-300" data-testid="openai-request-timezone-detected">
          {{ t('admin.accounts.openai.requestTimezoneDetected', { timezone: detection.timezone, ip: detection.ip || '-', location: location || '-', time: detectedAt }) }}
        </span>
        <span v-else-if="detection?.error" class="text-amber-600 dark:text-amber-400" data-testid="openai-request-timezone-detect-error">
          {{ t('admin.accounts.openai.requestTimezoneDetectFailed', { error: detection.error, timezone: modelValue || defaultTimezone }) }}
        </span>
        <span v-else class="text-gray-500 dark:text-gray-400">{{ t('admin.accounts.openai.requestTimezoneNotDetected', { timezone: modelValue || defaultTimezone }) }}</span>
        <button type="button" class="btn btn-secondary btn-sm" data-testid="openai-request-timezone-detect" :disabled="detecting" @click="detect">
          {{ detecting ? t('admin.accounts.openai.requestTimezoneDetecting') : t('admin.accounts.openai.requestTimezoneRedetect') }}
        </button>
      </div>
      <p v-else class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.openai.requestTimezoneDetectAfterSave') }}</p>
    </div>

    <template v-else>
      <div class="mt-2">
        <Select
          :model-value="modelValue"
          :options="options"
          :loading="loading"
          searchable
          :aria-label="t('admin.accounts.openai.requestTimezone')"
          @update:model-value="selectTimezone"
        />
      </div>
      <p class="input-hint">{{ t('admin.accounts.openai.requestTimezoneDesc') }}</p>
      <p v-if="loadFailed" class="mt-1 text-xs text-red-500">{{ t('admin.accounts.openai.requestTimezoneLoadFailed') }}</p>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import Select from '@/components/common/Select.vue'
import { useAppStore } from '@/stores/app'
import { extractApiErrorMessage } from '@/utils/apiError'
import type { OpenAIRequestTimezoneDetection, OpenAIRequestTimezoneMode } from '@/types'

const defaultTimezone = 'Asia/Singapore'
const props = defineProps<{
  modelValue: string
  mode: OpenAIRequestTimezoneMode
  accountId?: number
  detection?: OpenAIRequestTimezoneDetection | null
}>()
const emit = defineEmits<{
  (event: 'update:modelValue', value: string): void
  (event: 'update:mode', value: OpenAIRequestTimezoneMode): void
}>()
const { t } = useI18n()
const options = ref([{ value: defaultTimezone, label: defaultTimezone }])
const loading = ref(false)
const loadFailed = ref(false)
const loaded = ref(false)
const detecting = ref(false)
const detection = ref<OpenAIRequestTimezoneDetection | null>(props.detection ?? null)

const modeOptions = computed(() => [
  { value: 'follow_proxy' as const, label: t('admin.accounts.openai.requestTimezoneModeFollow') },
  { value: 'fixed' as const, label: t('admin.accounts.openai.requestTimezoneModeFixed') }
])
const location = computed(() => [detection.value?.country, detection.value?.region, detection.value?.city]
  .filter((part, index, parts) => part && parts.indexOf(part) === index).join(' '))
const detectedAt = computed(() => {
  const value = detection.value?.detected_at
  return value ? new Date(value).toLocaleString() : '-'
})

const selectTimezone = (value: string | number | boolean | null) => {
  if (typeof value === 'string') emit('update:modelValue', value)
}

watch(() => props.detection, value => { detection.value = value ?? null })

watch(() => props.modelValue, value => {
  if (props.mode === 'fixed' && loaded.value && !options.value.some(option => option.value === value)) emit('update:modelValue', defaultTimezone)
})

async function detect() {
  if (!props.accountId) return
  detecting.value = true
  try {
    const account = await adminAPI.accounts.detectOpenAIRequestTimezone(props.accountId)
    detection.value = account.extra?.openai_request_timezone_detected ?? null
    const timezone = account.extra?.openai_request_timezone
    if (typeof timezone === 'string' && timezone) emit('update:modelValue', timezone)
  } catch (cause) {
    useAppStore().showError(extractApiErrorMessage(cause, t('admin.accounts.openai.requestTimezoneDetectError')))
  } finally {
    detecting.value = false
  }
}

onMounted(async () => {
  loading.value = true
  try {
    const result = await adminAPI.accounts.getOpenAIRequestTimezones()
    options.value = result.timezones.map(value => ({ value, label: value }))
    loaded.value = true
    if (props.mode === 'fixed' && !result.timezones.includes(props.modelValue)) emit('update:modelValue', result.default)
  } catch {
    loadFailed.value = true
  } finally {
    loading.value = false
  }
})
</script>
