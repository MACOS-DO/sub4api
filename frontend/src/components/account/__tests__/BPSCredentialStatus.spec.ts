import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, type VueWrapper } from '@vue/test-utils'
import { nextTick } from 'vue'
import AccountStatusIndicator from '../AccountStatusIndicator.vue'
import BPSCredentialStatus from '../BPSCredentialStatus.vue'
import { bpsCredentialState, bpsExpiryMilliseconds } from '@/utils/openaiBps'
import type { Account } from '@/types'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})
const wrappers: VueWrapper[] = []
function account(extra: Partial<Account> = {}): Account {
  return { id: 1, platform: 'openai_bps', type: 'oauth', status: 'active', schedulable: true, credentials: {}, ...extra } as Account
}
function render(value: Account) {
  const wrapper = mount(AccountStatusIndicator, { props: { account: value } })
  wrappers.push(wrapper)
  return wrapper
}
describe('BPS credential visibility', () => {
  beforeEach(() => { vi.useFakeTimers(); vi.setSystemTime(new Date('2026-09-25T08:00:00Z')) })
  afterEach(() => { wrappers.splice(0).forEach(w => w.unmount()); vi.useRealTimers() })
  it('shows an expired token even while the account is active and the usage column is absent', () => {
    const wrapper = render(account({ credentials: { expires_at: '2026-09-25T07:00:00Z' } }))
    expect(wrapper.text()).toContain('credentialStatus.expired')
    expect(wrapper.text()).toContain('admin.accounts.bps.replaceToken')
    expect(wrapper.find('.badge-danger').exists()).toBe(true)
    expect(wrapper.find('.badge-success').exists()).toBe(false)
  })
  it('updates across the deadline without account reloads', async () => {
    const wrapper = render(account({ credentials: { expires_at: '2026-09-25T08:00:10Z' } }))
    expect(wrapper.text()).not.toContain('credentialStatus.expired')
    vi.advanceTimersByTime(30_000); await nextTick()
    expect(wrapper.text()).toContain('credentialStatus.expired')
  })
  it('keeps revoked distinct from a future expiry and retains administrative disablement', () => {
    const wrapper = render(account({ status: 'inactive', schedulable: false, bps_credential_state: { status: 'revoked', expires_at: '2026-10-05T08:00:00Z', error_code: 'token_revoked' } }))
    expect(wrapper.text()).toContain('credentialStatus.revoked')
    expect(wrapper.text()).not.toContain('credentialStatus.expired')
    expect(wrapper.text()).toContain('token_revoked')
    expect(wrapper.text()).toContain('admin.accounts.status.inactive')
  })
  it('uses the same normalized dates for numeric and string credentials', () => {
    const epoch = Date.parse('2026-09-25T07:00:00Z')
    for (const value of [epoch / 1000, String(epoch / 1000), epoch, new Date(epoch).toISOString()]) {
      expect(bpsExpiryMilliseconds(value)).toBe(epoch)
      expect(bpsCredentialState(account({ credentials: { expires_at: value } }), Date.now())?.status).toBe('expired')
    }
    expect(bpsCredentialState(account({ credentials: { expires_at: 'invalid' } }), Date.now())?.status).toBe('unknown')
    expect(bpsCredentialState(account({ platform: 'openai' }), Date.now())).toBeNull()
  })
  it('clears the visible error after a replacement, retaining manual resume guidance', async () => {
    const wrapper = render(account({ bps_credential_state: { status: 'revoked' } }))
    await wrapper.setProps({ account: account({ schedulable: false, bps_credential_state: { status: 'not_expired', expires_at: '2026-10-05T08:00:00Z', requires_manual_resume: true } }) })
    expect(wrapper.text()).not.toContain('credentialStatus.revoked')
    expect(wrapper.text()).toContain('admin.accounts.status.paused')
    const notice = mount(BPSCredentialStatus, { props: { state: { status: 'not_expired', requires_manual_resume: true } } })
    wrappers.push(notice)
    expect(notice.text()).toContain('admin.accounts.bps.manualResume')
  })
})
