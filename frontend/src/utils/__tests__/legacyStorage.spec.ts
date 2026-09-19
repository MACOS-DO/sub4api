import { describe, expect, it } from 'vitest'
import { migrateLegacyStorage } from '../legacyStorage'

describe('legacy preferences', () => {
  it('copies preferences while retaining explicitly set new values and old data', () => {
    localStorage.clear()
    localStorage.setItem('sub2api_locale', 'zh')
    localStorage.setItem('sub2api_login_agreement_consent', 'old')
    localStorage.setItem('sub4api_login_agreement_consent', 'new')
    migrateLegacyStorage(localStorage)
    expect(localStorage.getItem('sub4api_locale')).toBe('zh')
    expect(localStorage.getItem('sub4api_login_agreement_consent')).toBe('new')
    expect(localStorage.getItem('sub2api_locale')).toBe('zh')
    localStorage.clear()
  })
})
