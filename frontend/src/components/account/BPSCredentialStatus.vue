<template>
  <div v-if="state" class="max-w-[240px] space-y-1 break-words text-xs" data-testid="bps-credential-status" aria-live="polite">
    <span :class="failed ? 'badge badge-danger text-xs' : 'text-gray-500 dark:text-gray-400'">
      {{ t(`admin.accounts.bps.credentialStatus.${state.status}`) }}
    </span>
    <p v-if="showExpiry && state.expires_at" class="text-gray-500 dark:text-gray-400">
      {{ t('admin.accounts.bps.expiresAt') }}: {{ formatDateTime(state.expires_at) }}
    </p>
    <p v-if="failed" class="font-medium text-red-600 dark:text-red-400">{{ t('admin.accounts.bps.replaceToken') }}</p>
    <p v-if="state.error_code" class="text-gray-500 dark:text-gray-400">{{ t('admin.accounts.bps.errorCode') }}: {{ state.error_code }}</p>
    <p v-if="state.requires_manual_resume" class="text-amber-600 dark:text-amber-400">{{ t('admin.accounts.bps.manualResume') }}</p>
  </div>
</template>
<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { formatDateTime } from '@/utils/format'
import { isBPSCredentialFailure, type BPSCredentialState } from '@/utils/openaiBps'
const props = withDefaults(defineProps<{ state?: BPSCredentialState | null; showExpiry?: boolean }>(), { showExpiry: true })
const { t } = useI18n()
const failed = computed(() => isBPSCredentialFailure(props.state))
</script>
