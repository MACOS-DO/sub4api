<template>
  <section class="space-y-2" data-testid="upstream-response-headers">
    <div class="flex items-center justify-between">
      <h3 class="text-sm font-medium">{{ t('admin.accounts.upstreamHeaders') }}</h3>
      <button v-if="responses.length" type="button" class="btn btn-secondary btn-sm" @click="copyToClipboard(allHeaders)">{{ t('common.copy') }}</button>
    </div>
    <p v-if="!responses.length" class="text-xs text-gray-500">{{ t('admin.accounts.upstreamHeadersEmpty') }}</p>
    <div v-else class="max-h-80 space-y-3 overflow-auto">
      <pre v-for="response in responses" :key="response.sequence" class="whitespace-pre-wrap break-all rounded-lg bg-gray-50 p-3 font-mono text-xs dark:bg-dark-800">{{ formatUpstreamResponse(response) }}</pre>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useClipboard } from '@/composables/useClipboard'
import { formatUpstreamResponse, type AccountTestUpstreamResponse } from './accountTestResponse'

const props = defineProps<{ responses: AccountTestUpstreamResponse[] }>()
const { t } = useI18n()
const { copyToClipboard } = useClipboard()
const allHeaders = computed(() => props.responses.map(formatUpstreamResponse).join('\n\n'))
</script>
