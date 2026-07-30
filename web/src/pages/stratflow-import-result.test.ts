import assert from 'node:assert/strict'
import test from 'node:test'
import type { SfImportResult } from '../types'
import { importErrorDetailState, selectImportedRun } from './stratflow-import-result'

const partial: SfImportResult = {
  code: 'B1', batchCode: 'B1', collectionCode: 'C1', status: 3,
  total: 3, success: 2, fail: 1,
  errors: [{ rowNo: 2, errors: [{ fieldKey: 'phone', reason: 'Contains invalid characters' }] }],
  errorsTruncated: false,
  plans: [{ defCode: 'D1', versionCode: 'V1', result: 1, runCode: 'R1' }],
}

test('partial success still selects a successful run for observation', () => {
  assert.equal(selectImportedRun(partial, 'D1')?.runCode, 'R1')
  assert.deepEqual(importErrorDetailState(partial), { kind: 'complete', shown: 1, fail: 1 })
})

test('idempotent replay explains that failed-row details were not retained', () => {
  assert.deepEqual(importErrorDetailState({ ...partial, errors: [], errorsTruncated: true }), {
    kind: 'not-retained', shown: 0, fail: 1,
  })
})

test('truncated first-call details report the full failed-row count', () => {
  assert.deepEqual(importErrorDetailState({ ...partial, fail: 230, errorsTruncated: true }), {
    kind: 'truncated', shown: 1, fail: 230,
  })
})
