<template>
  <section class="space-y-4 rounded-lg border border-gray-200 p-4 dark:border-dark-600">
    <h3 class="font-medium">OpenAI BPS · Access Token</h3>
    <div>
      <label for="bps-access-token" class="input-label">Access Token</label>
      <input id="bps-access-token" v-model="draft.token" type="password" autocomplete="new-password" class="input" :required="!editing" :placeholder="editing ? t('admin.accounts.bps.keepToken') : 'access_token'" />
      <p class="input-hint">{{ t('admin.accounts.bps.tokenHint') }}</p>
    </div>
    <div>
      <label for="bps-account-id" class="input-label">ChatGPT Account ID</label>
      <input id="bps-account-id" v-model="draft.accountId" class="input" :placeholder="t('admin.accounts.bps.accountHint')" />
      <p class="input-hint">{{ t('admin.accounts.bps.accountHint') }}</p>
    </div>
    <p v-if="expiresAt" class="input-hint">{{ t('admin.accounts.bps.expiresAt') }}: {{ expiresAt }}</p>
    <div>
      <label class="input-label">{{ t('admin.accounts.modelMapping') }}</label>
      <p class="input-hint">{{ t('admin.accounts.bps.modelsHint') }}</p>
      <div v-for="(row, index) in draft.models" :key="index" class="mt-2 flex items-center gap-2">
        <input v-model="row.from" class="input min-w-0" :aria-label="t('admin.accounts.bps.clientModel')" :placeholder="t('admin.accounts.bps.clientModel')" required />
        <span aria-hidden="true">→</span>
        <input v-model="row.to" class="input min-w-0" :aria-label="t('admin.accounts.bps.upstreamModel')" :placeholder="row.from" />
        <button type="button" class="btn btn-secondary" :aria-label="t('common.delete')" @click="draft.models.splice(index, 1)">×</button>
      </div>
      <button type="button" class="btn btn-secondary mt-2" @click="draft.models.push({ from: '', to: '' })">{{ t('common.add') }}</button>
    </div>
  </section>
</template>
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { BPSAccountDraft } from '@/utils/openaiBps'
defineProps<{ editing?: boolean; expiresAt?: string }>()
const draft = defineModel<BPSAccountDraft>({ required: true })
const { t } = useI18n()
</script>
