import { computed, toValue, type MaybeRefOrGetter } from 'vue'
import { createSharedComposable, useNow } from '@vueuse/core'
import { bpsCredentialState, isBPSCredentialFailure, type BPSCredentialAccount } from '@/utils/openaiBps'

// One timer for every visible account, stopped when the last consumer leaves.
const useCredentialClock = createSharedComposable(() => useNow({ interval: 30_000 }))
export function useBPSCredentialState(account: MaybeRefOrGetter<BPSCredentialAccount | null | undefined>) {
  const now = useCredentialClock()
  const state = computed(() => {
    const current = toValue(account)
    return current?.platform === 'openai_bps' ? bpsCredentialState(current, now.value.getTime()) : null
  })
  const failed = computed(() => isBPSCredentialFailure(state.value))
  return { state, failed }
}
