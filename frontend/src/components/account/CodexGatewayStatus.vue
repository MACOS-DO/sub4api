<template>
  <span class="inline-flex rounded px-1.5 py-0.5 text-[11px] font-medium" :class="tone" :title="gateway?.error?.message">
    {{ t(`admin.accounts.codexGateway.${status}`) }}
  </span>
</template>
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { CodexGatewayState } from '@/types'
import { codexGatewayStatusKey } from '@/composables/useCodexGatewayAccount'
const props = defineProps<{ gateway?: CodexGatewayState }>()
const { t } = useI18n()
const status = computed(() => codexGatewayStatusKey(props.gateway))
const tone = computed(() => status.value === 'ready'
  ? 'bg-emerald-50 text-emerald-700 dark:bg-emerald-900/20 dark:text-emerald-300'
  : ['reauthorize', 'unavailable', 'missing'].includes(status.value)
    ? 'bg-red-50 text-red-700 dark:bg-red-900/20 dark:text-red-300'
    : 'bg-amber-50 text-amber-700 dark:bg-amber-900/20 dark:text-amber-300')
</script>
