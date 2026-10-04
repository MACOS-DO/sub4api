import { afterEach, describe, expect, it, vi } from 'vitest'
import { newCodexGatewayOperationKey } from '../useCodexGatewayAccount'

const uuidV4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/

describe('newCodexGatewayOperationKey', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('uses randomUUID in secure contexts', () => {
    expect(newCodexGatewayOperationKey()).toMatch(uuidV4)
  })

  it('still returns a UUID v4 when randomUUID is unavailable over plain HTTP', () => {
    const { getRandomValues } = globalThis.crypto
    vi.stubGlobal('crypto', { getRandomValues: getRandomValues.bind(globalThis.crypto) })
    const first = newCodexGatewayOperationKey()
    expect(first).toMatch(uuidV4)
    expect(newCodexGatewayOperationKey()).not.toBe(first)
  })
})
