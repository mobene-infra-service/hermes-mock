import assert from 'node:assert/strict'
import test from 'node:test'
import {
  composeStratflowImportRows,
  generateSequentialPhones,
  MAX_STRATFLOW_IMPORT_ROWS,
  parseStratflowImportCsv,
} from '../src/pages/stratflow-import.ts'

const fields = [
  { key: 'customer_name', displayName: '客户姓名', dataType: 'string', required: true, sort: 1, maxLen: 8 },
  { key: 'age', displayName: '年龄', dataType: 'int', required: false, sort: 2 },
  { key: 'score', displayName: '评分', dataType: 'float', required: false, sort: 3, scale: 2 },
  { key: 'vip', displayName: 'VIP', dataType: 'bool', required: false, sort: 4 },
  { key: 'birthday', displayName: '生日', dataType: 'date', required: false, sort: 5 },
  { key: 'level', displayName: '等级', dataType: 'enum', required: false, sort: 6, options: ['A', 'B'] },
  { key: 'tags', displayName: '标签', dataType: 'array', itemType: 'string', required: false, sort: 7 },
]

test('批量生成保留加号和前导零', () => {
  assert.deepEqual(generateSequentialPhones('+0008', 3), ['+0008', '+0009', '+0010'])
  assert.throws(() => generateSequentialPhones('13x', 1), /只能包含/)
  assert.throws(() => generateSequentialPhones('138', MAX_STRATFLOW_IMPORT_ROWS + 1), /生成数量/)
})

test('公共对象按显示名映射字段 Key 并应用到全部号码', () => {
  const result = composeStratflowImportRows(
    '13800138000\n13800138001',
    JSON.stringify({ 客户姓名: '张三', age: '20', score: '0.00', tags: ['vip'] }),
    fields,
  )

  assert.equal(result.errorCount, 0)
  assert.equal(result.mode, 'COMMON')
  assert.deepEqual(result.rows, [
    { phone: '13800138000', bizFields: { customer_name: '张三', age: '20', score: '0.00', tags: ['vip'] } },
    { phone: '13800138001', bizFields: { customer_name: '张三', age: '20', score: '0.00', tags: ['vip'] } },
  ])
})

test('逐行 JSON 同时支持嵌套和平铺业务字段', () => {
  const result = composeStratflowImportRows('', JSON.stringify([
    { phone: '13800138000', bizFields: { customer_name: '张三', level: 'A' } },
    { number: '13800138001', 客户姓名: '李四', vip: true },
  ]), fields)

  assert.equal(result.errorCount, 0)
  assert.equal(result.mode, 'ROWS')
  assert.deepEqual(result.rows, [
    { phone: '13800138000', bizFields: { customer_name: '张三', level: 'A' } },
    { phone: '13800138001', bizFields: { customer_name: '李四', vip: true } },
  ])
})

test('不带 phone 的逐行 JSON 必须与号码逐行对齐', () => {
  const ok = composeStratflowImportRows('13800138000\n13800138001', JSON.stringify({ rows: [
    { customer_name: '张三' },
    { customer_name: '李四' },
  ] }), fields)
  assert.equal(ok.errorCount, 0)
  assert.deepEqual(ok.rows.map((row) => row.phone), ['13800138000', '13800138001'])

  const mismatch = composeStratflowImportRows('13800138000', JSON.stringify([
    { customer_name: '张三' },
    { customer_name: '李四' },
  ]), fields)
  assert.ok(mismatch.errors.some((error) => error.includes('数量必须一致')))
  assert.ok(mismatch.errors.some((error) => error.includes('第 2 行：手机号为空')))
})

test('导入前报告未知字段、必填缺失及类型错误', () => {
  const result = composeStratflowImportRows('', JSON.stringify([
    { phone: '13800138000', unknown: 1, age: '1.5', score: '1.234', birthday: '2026-02-29', level: 1, tags: 'vip' },
  ]), fields)

  for (const expected of ['未定义字段 unknown', '必填字段', '应为整数', '小数位超限', '应为日期', '应为枚举文本', '应为数组']) {
    assert.ok(result.errors.some((error) => error.includes(expected)), `缺少错误：${expected}`)
  }
})

test('嵌套 bizFields 与平铺字段混用时拒绝，避免静默丢字段', () => {
  const result = composeStratflowImportRows('', JSON.stringify([
    { phone: '13800138000', bizFields: { customer_name: '张三' }, age: 20 },
  ]), fields)

  assert.ok(result.errors.some((error) => error.includes('不能再平铺业务字段 age')))
})

test('{rows:[...]} 包装不能混入其它顶层字段', () => {
  const result = composeStratflowImportRows('13800138000', JSON.stringify({
    rows: [{ customer_name: '张三' }],
    ignored: true,
  }), fields)

  assert.equal(result.mode, 'COMMON')
  assert.ok(result.errors.some((error) => error.includes('未定义字段 rows')))
  assert.ok(result.errors.some((error) => error.includes('未定义字段 ignored')))
})

test('可解析带 UTF-8 BOM 的 JSON 文件内容', () => {
  const result = composeStratflowImportRows('13800138000', '\uFEFF{"customer_name":"张三"}', fields)
  assert.equal(result.errorCount, 0)
  assert.equal(result.rows[0].bizFields?.customer_name, '张三')
})

test('按实际 Org 模板解析 phone 与字段 Key', () => {
  const templateFields = [
    { key: 'smoke_seg_1784142605', displayName: 'Smoke Segment', dataType: 'string', required: false, sort: 1 },
  ]
  const result = parseStratflowImportCsv(
    'phone,smoke_seg_1784142605\n10000000000,\n10000000001,A\n',
    templateFields,
  )

  assert.equal(result.errorCount, 0)
  assert.equal(result.mode, 'CSV')
  assert.deepEqual(result.fieldKeys, ['smoke_seg_1784142605'])
  assert.deepEqual(result.rows, [
    { phone: '10000000000', bizFields: {} },
    { phone: '10000000001', bizFields: { smoke_seg_1784142605: 'A' } },
  ])
})

test('CSV 支持显示名、双引号逗号与数组竖线分隔', () => {
  const result = parseStratflowImportCsv(
    '\uFEFFphone,客户姓名,标签,年龄\r\n13800138000,"张,三",vip|overdue,20\r\n',
    fields,
  )

  assert.equal(result.errorCount, 0)
  assert.deepEqual(result.rows[0], {
    phone: '13800138000',
    bizFields: { customer_name: '张,三', tags: ['vip', 'overdue'], age: '20' },
  })
})

test('CSV 将未知表头原样交给 Hermes，仅拦截结构错误', () => {
  const unknown = parseStratflowImportCsv('phone,customer_name,test1\n13800138000,张三,x\n', fields)
  assert.equal(unknown.errorCount, 0)
  assert.deepEqual(unknown.fieldKeys, ['customer_name', 'test1'])
  assert.deepEqual(unknown.rows[0], {
    phone: '13800138000',
    bizFields: { customer_name: '张三', test1: 'x' },
  })

  const deferredValidation = parseStratflowImportCsv('phone,age,level\n13800138001,not-an-int,C\n', fields)
  assert.equal(deferredValidation.errorCount, 0)
  assert.deepEqual(deferredValidation.rows[0], {
    phone: '13800138001',
    bizFields: { age: 'not-an-int', level: 'C' },
  })

  const unclosed = parseStratflowImportCsv('phone,customer_name\n13800138000,"张三\n', fields)
  assert.ok(unclosed.errors.some((error) => error.includes('未闭合')))
})
