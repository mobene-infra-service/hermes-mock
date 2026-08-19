import assert from 'node:assert/strict'
import test from 'node:test'
import type { SfField } from '../types'
import { composeStratflowImportRows, parseStratflowImportCsv } from './stratflow-import'

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

test('row JSON keeps business identifiers beside phone and out of bizFields', () => {
  const result = composeStratflowImportRows('', JSON.stringify({ rows: [{
    phone: '4155552671', businessId: '  B123  ', ticketId: '', amount: '12.34',
  }] }), fields)

  assert.equal(result.blockingErrorCount, 0)
  assert.equal(result.rows[0].businessId, '  B123  ')
  assert.equal(result.rows[0].ticketId, '')
  assert.deepEqual(result.rows[0].bizFields, { amount: '12.34' })
})

test('common JSON business identifiers apply to every phone', () => {
  const result = composeStratflowImportRows('4155552671\n4155552672', JSON.stringify({
    businessId: 'B123', userId: null, bizFields: { amount: '12.34' },
  }), fields)

  assert.equal(result.blockingErrorCount, 0)
  assert.deepEqual(result.rows.map((row) => row.businessId), ['B123', 'B123'])
  assert.deepEqual(result.rows.map((row) => row.userId), [null, null])
})

test('CSV preserves business identifier whitespace and explicit empty string', () => {
  const result = parseStratflowImportCsv(
    'phone,businessId,ticketId,amount\n4155552671,"  B123  ",,12.34\n',
    fields,
  )

  assert.equal(result.blockingErrorCount, 0)
  assert.equal(result.rows[0].businessId, '  B123  ')
  assert.equal(result.rows[0].ticketId, '')
  assert.deepEqual(result.rows[0].bizFields, { amount: '12.34' })
})
