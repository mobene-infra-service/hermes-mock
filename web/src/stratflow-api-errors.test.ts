import assert from 'node:assert/strict'
import test from 'node:test'
import { ApiRequestError, sfImport } from './api'

test('sfImport preserves upstreamCode and upstreamData from the mock proxy', async () => {
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: { localStorage: { getItem: () => null } },
  })
  const originalFetch = globalThis.fetch
  globalThis.fetch = async () => new Response(JSON.stringify({
    error: 'No valid rows to import',
    upstreamCode: 42011,
    upstreamData: { total: 1, success: 0, fail: 1, errors: [], errorsTruncated: true },
  }), { status: 400, headers: { 'Content-Type': 'application/json' } })

  try {
    await assert.rejects(
      sfImport('COL', { rows: [] }),
      (error: unknown) => {
        assert.ok(error instanceof ApiRequestError)
        assert.equal(error.upstreamCode, 42011)
        assert.deepEqual(error.upstreamData, {
          total: 1, success: 0, fail: 1, errors: [], errorsTruncated: true,
        })
        return true
      },
    )
  } finally {
    globalThis.fetch = originalFetch
  }
})
