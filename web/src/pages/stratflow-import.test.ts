import assert from 'node:assert/strict'
import test from 'node:test'
import type { SfField } from '../types'
import { composeStratflowImportRows } from './stratflow-import'

const fields: SfField[] = [
  {
    key: 'amount', displayName: 'Amount', dataType: 'float', required: true,
    format: null, options: null, itemType: null, maxLen: null, scale: 2, sort: 1,
  },
]

test('semantic row errors warn but preserve valid-invalid-valid rows for Hermes', () => {
  const result = composeStratflowImportRows('', JSON.stringify({ rows: [
    { phone: '4155552671', amount: '12.34' },
    { phone: 'bad-number', amount: 'not-a-number', future_field: 'keep-me' },
    { phone: '4155552671', amount: '56.78' },
  ] }), fields)

  assert.equal(result.blockingErrorCount, 0)
  assert.ok(result.semanticWarningCount >= 2)
  assert.deepEqual(result.rows.map((row) => row.phone), ['4155552671', 'bad-number', '4155552671'])
  assert.equal(result.rows[1].bizFields?.future_field, 'keep-me')
})

test('malformed JSON remains a blocking structure error', () => {
  const result = composeStratflowImportRows('4155552671', '{bad json', fields)

  assert.equal(result.rows.length, 0)
  assert.equal(result.blockingErrorCount, 1)
  assert.equal(result.semanticWarningCount, 0)
})

test('an explicit empty rows request is not blocked locally', () => {
  const result = composeStratflowImportRows('', '{"rows":[]}', fields)

  assert.deepEqual(result.rows, [])
  assert.equal(result.blockingErrorCount, 0)
})
