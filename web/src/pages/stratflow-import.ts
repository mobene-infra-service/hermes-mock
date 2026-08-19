import type { SfField, SfImportRow } from '../types'

export const MAX_STRATFLOW_IMPORT_ROWS = 10_000
const MAX_ERROR_DETAILS = 200

export type SfImportSourceMode = 'COMMON' | 'ROWS' | 'CSV'

export interface SfImportComposeResult {
  rows: SfImportRow[]
  mode: SfImportSourceMode
  fieldKeys: string[]
  blockingErrors: string[]
  blockingErrorCount: number
  blockingErrorsTruncated: boolean
  semanticWarnings: string[]
  semanticWarningCount: number
  semanticWarningsTruncated: boolean
}

type JsonRecord = Record<string, unknown>

const PHONE_KEYS = new Set(['phone', 'phone_number', 'phonenumber', 'number'])
type BusinessIdentifierKey = keyof Pick<SfImportRow, 'businessId' | 'ticketId' | 'orderId' | 'userId'>

const BUSINESS_IDENTIFIER_KEYS: Map<string, BusinessIdentifierKey> = new Map([
  ['businessid', 'businessId'],
  ['ticketid', 'ticketId'],
  ['orderid', 'orderId'],
  ['userid', 'userId'],
] as const)
const DECIMAL_PATTERN = /^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:e[+-]?\d+)?$/i

function isRecord(value: unknown): value is JsonRecord {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

export function splitStratflowPhones(text: string): string[] {
  return text.split(/\r?\n/).map((phone) => phone.trim()).filter(Boolean)
}

export function generateSequentialPhones(start: string, count: number): string[] {
  const normalized = start.trim()
  const matched = /^(\+?)(\d+)$/.exec(normalized)
  if (!matched) throw new Error('起始手机号只能包含可选的 + 和数字')
  if (!Number.isInteger(count) || count < 1 || count > MAX_STRATFLOW_IMPORT_ROWS) {
    throw new Error(`生成数量须为 1～${MAX_STRATFLOW_IMPORT_ROWS}`)
  }

  const [, prefix, digits] = matched
  const first = BigInt(digits)
  return Array.from({ length: count }, (_, index) =>
    `${prefix}${(first + BigInt(index)).toString().padStart(digits.length, '0')}`,
  )
}

function parseCsvGrid(content: string): { grid: string[][]; error?: string } {
  const grid: string[][] = []
  let row: string[] = []
  let cell = ''
  let quoted = false

  const pushCell = () => {
    row.push(cell)
    cell = ''
  }
  const pushRow = () => {
    pushCell()
    if (row.some((value) => value.trim() !== '')) grid.push(row)
    row = []
  }

  for (let index = 0; index < content.length; index += 1) {
    const char = content[index]
    const next = content[index + 1]
    if (char === '"') {
      if (quoted && next === '"') {
        cell += '"'
        index += 1
      } else {
        quoted = !quoted
      }
    } else if (char === ',' && !quoted) {
      pushCell()
    } else if ((char === '\n' || char === '\r') && !quoted) {
      if (char === '\r' && next === '\n') index += 1
      pushRow()
    } else {
      cell += char
    }
  }
  if (quoted) return { grid: [], error: 'CSV 存在未闭合的双引号' }
  if (cell !== '' || row.length > 0) pushRow()
  return { grid }
}

function importErrorResult(mode: SfImportSourceMode, messages: string[]): SfImportComposeResult {
  return {
    rows: [],
    mode,
    fieldKeys: [],
    blockingErrors: messages.slice(0, MAX_ERROR_DETAILS),
    blockingErrorCount: messages.length,
    blockingErrorsTruncated: messages.length > MAX_ERROR_DETAILS,
    semanticWarnings: [],
    semanticWarningCount: 0,
    semanticWarningsTruncated: false,
  }
}

/** 按 Org 名单模板解析 CSV：phone + 集合字段 Key/显示名；array 单元格用 | 分隔。 */
export function parseStratflowImportCsv(content: string, fields: SfField[]): SfImportComposeResult {
  const parsed = parseCsvGrid(content.replace(/^\uFEFF/, ''))
  if (parsed.error) return importErrorResult('CSV', [parsed.error])
  if (parsed.grid.length < 2) return importErrorResult('CSV', ['CSV 为空或缺少数据行'])

  const headers = parsed.grid[0]
  const normalizedHeaders = headers.map((header) => header.trim().toLowerCase())
  const phoneIndexes = normalizedHeaders
    .map((header, index) => (PHONE_KEYS.has(header) ? index : -1))
    .filter((index) => index >= 0)
  const headerErrors: string[] = []
  if (phoneIndexes.length === 0) headerErrors.push('CSV 表头必须包含 phone 列')
  if (phoneIndexes.length > 1) headerErrors.push('CSV 表头只能包含一个 phone/phone_number/number 列')

  const fieldsByKey = new Map(fields.map((field) => [field.key.trim().toLowerCase(), field]))
  const fieldsByDisplayName = new Map<string, SfField | null>()
  fields.forEach((field) => {
    const displayName = field.displayName.trim().toLowerCase()
    if (!displayName) return
    fieldsByDisplayName.set(displayName, fieldsByDisplayName.has(displayName) ? null : field)
  })

  const fieldColumns = new Map<number, { key: string; field?: SfField }>()
  const identifierColumns = new Map<number, BusinessIdentifierKey>()
  const usedKeys = new Set<string>()
  const usedIdentifiers = new Set<string>()
  normalizedHeaders.forEach((header, index) => {
    if (PHONE_KEYS.has(header)) return
    if (!header) {
      headerErrors.push(`CSV 第 ${index + 1} 列表头为空`)
      return
    }
    const identifier = BUSINESS_IDENTIFIER_KEYS.get(header)
    if (identifier) {
      if (usedIdentifiers.has(identifier)) headerErrors.push(`CSV 业务标识「${identifier}」被重复提供`)
      else {
        usedIdentifiers.add(identifier)
        identifierColumns.set(index, identifier)
      }
      return
    }
    const field = fieldsByKey.get(header) ?? fieldsByDisplayName.get(header)
    if (field === null) {
      headerErrors.push(`CSV 表头「${headers[index]}」对应多个集合字段，请改用字段 Key`)
      return
    }
    const outputKey = field?.key ?? headers[index].trim()
    const normalizedOutputKey = outputKey.toLowerCase()
    if (usedKeys.has(normalizedOutputKey)) {
      headerErrors.push(`CSV 字段「${outputKey}」被重复提供（Key/显示名冲突）`)
      return
    }
    usedKeys.add(normalizedOutputKey)
    fieldColumns.set(index, { key: outputKey, field: field || undefined })
  })
  if (headerErrors.length > 0) return importErrorResult('CSV', headerErrors)

  const dataRows = parsed.grid.slice(1)
  const semanticWarnings: string[] = []
  let semanticWarningCount = 0
  const addSemanticWarning = (warning: string) => {
    semanticWarningCount += 1
    if (semanticWarnings.length < MAX_ERROR_DETAILS) semanticWarnings.push(warning)
  }
  if (dataRows.length > MAX_STRATFLOW_IMPORT_ROWS) {
    addSemanticWarning(`名单共 ${dataRows.length} 条，超过 Hermes 单次上限 ${MAX_STRATFLOW_IMPORT_ROWS} 条`)
  }
  fieldColumns.forEach((column) => {
    if (!column.field) addSemanticWarning(`存在未定义字段 ${column.key}`)
  })

  const rowErrors: string[] = []
  const rows: SfImportRow[] = dataRows.map((cells, index) => {
    const rowNo = index + 1
    if (cells.length > headers.length && cells.slice(headers.length).some((value) => value.trim() !== '')) {
      rowErrors.push(`第 ${rowNo} 行：列数超过表头，请检查逗号或双引号转义`)
    }
    const bizFields: Record<string, unknown> = {}
    fieldColumns.forEach((column, columnIndex) => {
      const raw = cells[columnIndex]?.trim()
      if (!raw) return
      bizFields[column.key] = String(column.field?.dataType || '').toLowerCase() === 'array'
        ? raw.split('|').map((item) => item.trim()).filter(Boolean)
        : raw
    })
    const identifiers: Partial<SfImportRow> = {}
    identifierColumns.forEach((key, columnIndex) => { identifiers[key] = cells[columnIndex] ?? '' })
    return { phone: cells[phoneIndexes[0]]?.trim() || '', ...identifiers, bizFields }
  })

  collectSemanticWarnings(rows, fields, addSemanticWarning)

  return {
    rows,
    mode: 'CSV',
    fieldKeys: [...fieldColumns.values()].map((column) => column.key).sort(),
    blockingErrors: rowErrors.slice(0, MAX_ERROR_DETAILS),
    blockingErrorCount: rowErrors.length,
    blockingErrorsTruncated: rowErrors.length > MAX_ERROR_DETAILS,
    semanticWarnings,
    semanticWarningCount,
    semanticWarningsTruncated: semanticWarningCount > semanticWarnings.length,
  }
}

function decimalText(value: unknown): string | null {
  if (typeof value === 'number') return Number.isFinite(value) ? String(value) : null
  if (typeof value !== 'string') return null
  const normalized = value.trim()
  return DECIMAL_PATTERN.test(normalized) ? normalized : null
}

/** 对齐 Java BigDecimal.stripTrailingZeros().scale() 的常用 JSON 数字语义。 */
function normalizedDecimalScale(value: unknown): number | null {
  const text = decimalText(value)
  if (text === null) return null
  const unsigned = text.replace(/^[+-]/, '')
  const [coefficient, exponentText] = unsigned.toLowerCase().split('e')
  const exponent = exponentText ? Number(exponentText) : 0
  if (!Number.isSafeInteger(exponent)) return null
  const [whole, fraction = ''] = coefficient.split('.')
  const digits = `${whole}${fraction}`.replace(/^0+/, '')
  if (!digits) return 0
  const trailingZeros = digits.match(/0+$/)?.[0].length ?? 0
  return fraction.length - exponent - trailingZeros
}

function isStrictDate(text: string): boolean {
  const matched = /^(\d{4})-(\d{2})-(\d{2})$/.exec(text)
  if (!matched) return false
  const [, yearText, monthText, dayText] = matched
  const year = Number(yearText)
  const month = Number(monthText)
  const day = Number(dayText)
  if (month < 1 || month > 12) return false
  const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0)
  const daysInMonth = [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31]
  return day >= 1 && day <= daysInMonth[month - 1]
}

function isStrictTime(text: string): boolean {
  const matched = /^(\d{2}):(\d{2}):(\d{2})$/.exec(text)
  if (!matched) return false
  const [, hour, minute, second] = matched.map(Number)
  return hour >= 0 && hour <= 23 && minute >= 0 && minute <= 59 && second >= 0 && second <= 59
}

function isStrictDateTime(text: string): boolean {
  const matched = /^(\d{4}-\d{2}-\d{2}) (\d{2}:\d{2}:\d{2})$/.exec(text)
  return !!matched && isStrictDate(matched[1]) && isStrictTime(matched[2])
}

function validateTypedValue(field: SfField, value: unknown): string | null {
  const type = String(field.dataType || '').toLowerCase()
  switch (type) {
    case 'string':
      if (typeof value !== 'string') return `应为文本（实际：${JSON.stringify(value)}）`
      if (field.maxLen != null && value.length > field.maxLen) return `超长（上限 ${field.maxLen}，实际 ${value.length}）`
      return null
    case 'int': {
      const scale = normalizedDecimalScale(value)
      return scale !== null && scale <= 0 ? null : `应为整数（实际：${JSON.stringify(value)}）`
    }
    case 'float': {
      const scale = normalizedDecimalScale(value)
      if (scale === null) return `应为数值（实际：${JSON.stringify(value)}）`
      const actualScale = Math.max(scale, 0)
      if (field.scale != null && actualScale > field.scale) return `小数位超限（上限 ${field.scale}，实际 ${actualScale}）`
      return null
    }
    case 'bool':
      if (typeof value === 'boolean') return null
      if (typeof value === 'number' && (value === 0 || value === 1)) return null
      if (typeof value === 'string' && ['是', '否', 'true', 'false', '1', '0'].includes(value.trim().toLowerCase())) return null
      return `应为布尔值（是/否、true/false、1/0，实际：${JSON.stringify(value)}）`
    case 'date':
      return isStrictDate(String(value).trim()) ? null : `应为日期 yyyy-MM-dd（实际：${JSON.stringify(value)}）`
    case 'datetime':
      return isStrictDateTime(String(value).trim()) ? null : `应为日期时间 yyyy-MM-dd HH:mm:ss（实际：${JSON.stringify(value)}）`
    case 'time':
      return isStrictTime(String(value).trim()) ? null : `应为时间 HH:mm:ss（实际：${JSON.stringify(value)}）`
    case 'enum':
      if (!Array.isArray(field.options) || field.options.length === 0) return '缺少枚举 options 元数据'
      if (typeof value !== 'string') return `应为枚举文本（实际：${JSON.stringify(value)}）`
      return field.options.includes(value) ? null : `不在可选项内（实际：${JSON.stringify(value)}）`
    case 'array': {
      if (!Array.isArray(value)) return `应为数组（实际：${JSON.stringify(value)}）`
      const itemType = field.itemType?.toLowerCase()
      if (!itemType || itemType === 'array') return '缺少或不支持数组元素类型 itemType'
      for (let index = 0; index < value.length; index += 1) {
        if (value[index] == null) return `第 ${index + 1} 项不能为空`
        const itemError = validateTypedValue({ ...field, dataType: itemType, maxLen: null, scale: null }, value[index])
        if (itemError) return `第 ${index + 1} 项${itemError}`
      }
      return null
    }
    default:
      return `字段类型 ${field.dataType} 暂不支持`
  }
}

function collectSemanticWarnings(
  rows: SfImportRow[],
  fields: SfField[],
  addWarning: (warning: string) => void,
) {
  rows.forEach((row, index) => {
    const rowNo = index + 1
    if (!row.phone.trim()) {
      addWarning(`第 ${rowNo} 行：手机号为空`)
    } else if (!/^\+?\d+$/.test(row.phone)) {
      addWarning(`第 ${rowNo} 行：手机号包含非数字字符`)
    }
    fields.forEach((field) => {
      const value = row.bizFields?.[field.key]
      const blank = value == null || (typeof value === 'string' && value.trim() === '')
      if (blank) {
        if (field.required) addWarning(`第 ${rowNo} 行：必填字段「${field.displayName}（${field.key}）」为空`)
        return
      }
      const fieldError = validateTypedValue(field, value)
      if (fieldError) addWarning(`第 ${rowNo} 行：字段「${field.displayName}（${field.key}）」${fieldError}`)
    })
  })
}

function phoneFromJson(value: unknown): string {
  if (typeof value === 'string' || typeof value === 'number') return String(value).trim()
  return ''
}

/**
 * 将页面的号码 + 业务字段 JSON 归一为 Hermes OpenAPI 所需 rows。
 *
 * JSON 支持：
 * - 普通对象：作为公共 bizFields 应用于所有号码；
 * - 数组或 { rows: [...] }：逐行对象，可用 { phone, bizFields } 或 { phone, ...业务字段 }；
 * - 逐行对象不带 phone 时，按下标与号码输入区一一对应。
 */
export function composeStratflowImportRows(
  phonesText: string,
  bizText: string,
  fields: SfField[],
): SfImportComposeResult {
  const phones = splitStratflowPhones(phonesText)
  const blockingErrors: string[] = []
  const semanticWarnings: string[] = []
  let blockingErrorCount = 0
  let semanticWarningCount = 0
  const addBlockingError = (error: string) => {
    blockingErrorCount += 1
    if (blockingErrors.length < MAX_ERROR_DETAILS) blockingErrors.push(error)
  }
  const addSemanticWarning = (warning: string) => {
    semanticWarningCount += 1
    if (semanticWarnings.length < MAX_ERROR_DETAILS) semanticWarnings.push(warning)
  }
  const result = (rows: SfImportRow[], mode: SfImportSourceMode, fieldKeys: string[]): SfImportComposeResult => ({
    rows,
    mode,
    fieldKeys,
    blockingErrors,
    blockingErrorCount,
    blockingErrorsTruncated: blockingErrorCount > blockingErrors.length,
    semanticWarnings,
    semanticWarningCount,
    semanticWarningsTruncated: semanticWarningCount > semanticWarnings.length,
  })

  if (fields.length === 0) addSemanticWarning('当前集合未配置业务字段，Hermes 将拒绝本次导入')

  let parsed: unknown = {}
  if (bizText.trim()) {
    try {
      parsed = JSON.parse(bizText.trim())
    } catch (error) {
      addBlockingError(`业务字段 JSON 格式错误：${error instanceof Error ? error.message : String(error)}`)
      return result([], 'COMMON', [])
    }
  }

  const fieldsByKey = new Map(fields.map((field) => [field.key.trim().toLowerCase(), field]))
  const fieldsByDisplayName = new Map<string, SfField | null>()
  fields.forEach((field) => {
    const name = field.displayName.trim().toLowerCase()
    if (!name) return
    fieldsByDisplayName.set(name, fieldsByDisplayName.has(name) ? null : field)
  })
  const usedFieldKeys = new Set<string>()

  const extractIdentifiers = (source: JsonRecord, rowNo: number) => {
    const identifiers: Partial<SfImportRow> = {}
    const rest: JsonRecord = {}
    const seen = new Set<string>()
    Object.entries(source).forEach(([rawKey, value]) => {
      const key = BUSINESS_IDENTIFIER_KEYS.get(rawKey.trim().toLowerCase())
      if (!key) {
        rest[rawKey] = value
        return
      }
      if (seen.has(key)) {
        addBlockingError(`第 ${rowNo} 行：业务标识「${key}」被重复提供`)
        return
      }
      seen.add(key)
      if (value !== null && typeof value !== 'string') {
        addBlockingError(`第 ${rowNo} 行：业务标识「${key}」必须是字符串或 null`)
        return
      }
      identifiers[key] = value
    })
    return { identifiers, rest }
  }

  const normalizeBizFields = (source: unknown, rowNo: number): Record<string, unknown> => {
    if (!isRecord(source)) {
      addBlockingError(`第 ${rowNo} 行：业务字段应为 JSON 对象`)
      return {}
    }
    const normalized: Record<string, unknown> = {}
    Object.entries(source).forEach(([rawKey, value]) => {
      const lookup = rawKey.trim().toLowerCase()
      if (BUSINESS_IDENTIFIER_KEYS.has(lookup)) {
        addBlockingError(`第 ${rowNo} 行：业务标识「${rawKey}」应与 phone 同级，不能放入 bizFields`)
        return
      }
      const field = fieldsByKey.get(lookup) ?? fieldsByDisplayName.get(lookup)
      if (field === null) {
        addBlockingError(`第 ${rowNo} 行：字段名「${rawKey}」对应多个集合字段，请改用字段 Key`)
        return
      }
      if (!field) {
        addSemanticWarning(`第 ${rowNo} 行：存在未定义字段 ${rawKey}`)
        normalized[rawKey] = value
        usedFieldKeys.add(rawKey)
        return
      }
      if (Object.prototype.hasOwnProperty.call(normalized, field.key)) {
        addBlockingError(`第 ${rowNo} 行：字段「${field.key}」被重复提供（Key/显示名冲突）`)
        return
      }
      normalized[field.key] = value
      usedFieldKeys.add(field.key)
    })
    return normalized
  }

  let mode: SfImportSourceMode = 'COMMON'
  let rows: SfImportRow[] = []
  const wrappedRows = isRecord(parsed) && Object.keys(parsed).length === 1 && Array.isArray(parsed.rows) ? parsed.rows : null
  const jsonRows = Array.isArray(parsed) ? parsed : wrappedRows

  if (jsonRows) {
    mode = 'ROWS'
    if (jsonRows.length > MAX_STRATFLOW_IMPORT_ROWS) {
      addSemanticWarning(`名单共 ${jsonRows.length} 条，超过 Hermes 单次上限 ${MAX_STRATFLOW_IMPORT_ROWS} 条`)
    }
    const rowsToParse = jsonRows

    const explicitPhoneCount = rowsToParse.reduce((count, item) => {
      if (!isRecord(item)) return count
      const phoneKey = Object.keys(item).find((key) => PHONE_KEYS.has(key.trim().toLowerCase()))
      return count + (phoneKey && phoneFromJson(item[phoneKey]) ? 1 : 0)
    }, 0)
    if (explicitPhoneCount === 0 && phones.length !== jsonRows.length) {
      addBlockingError(`逐行 JSON 共 ${jsonRows.length} 条、号码区共 ${phones.length} 条；JSON 不含 phone 时数量必须一致`)
    }

    rows = rowsToParse.map((item, index) => {
      const rowNo = index + 1
      if (!isRecord(item)) {
        addBlockingError(`第 ${rowNo} 行：名单行应为 JSON 对象`)
        return { phone: phones[index] ?? '', bizFields: {} }
      }
      const phoneKey = Object.keys(item).find((key) => PHONE_KEYS.has(key.trim().toLowerCase()))
      const phone = phoneKey ? phoneFromJson(item[phoneKey]) : (phones[index] ?? '')
      const topLevel = Object.fromEntries(
        Object.entries(item).filter(([key]) => key !== 'bizFields' && !PHONE_KEYS.has(key.trim().toLowerCase())),
      )
      const { identifiers, rest } = extractIdentifiers(topLevel, rowNo)

      let source: unknown
      if (Object.prototype.hasOwnProperty.call(item, 'bizFields')) {
        source = item.bizFields
        if (!isRecord(source)) addBlockingError(`第 ${rowNo} 行：bizFields 应为 JSON 对象`)
        const flatKeys = Object.keys(rest)
        if (flatKeys.length > 0) addBlockingError(`第 ${rowNo} 行：已使用 bizFields，不能再平铺业务字段 ${flatKeys.join('、')}`)
      } else {
        source = rest
      }
      return { phone, ...identifiers, bizFields: normalizeBizFields(source, rowNo) }
    })
  } else if (isRecord(parsed)) {
    const topLevel = Object.fromEntries(Object.entries(parsed).filter(([key]) => key !== 'bizFields'))
    const { identifiers, rest } = extractIdentifiers(topLevel, 1)
    const commonSource = Object.prototype.hasOwnProperty.call(parsed, 'bizFields') ? parsed.bizFields : rest
    if (Object.prototype.hasOwnProperty.call(parsed, 'bizFields') && Object.keys(rest).length > 0) {
      addBlockingError(`第 1 行：已使用 bizFields，不能再平铺业务字段 ${Object.keys(rest).join('、')}`)
    }
    const common = normalizeBizFields(commonSource, 1)
    if (phones.length === 0) addSemanticWarning('未提供名单行，Hermes 将返回空名单错误')
    if (phones.length > MAX_STRATFLOW_IMPORT_ROWS) {
      addSemanticWarning(`名单共 ${phones.length} 条，超过 Hermes 单次上限 ${MAX_STRATFLOW_IMPORT_ROWS} 条`)
    }
    rows = phones.map((phone) => ({ phone, ...identifiers, bizFields: { ...common } }))
  } else {
    addBlockingError('业务字段 JSON 顶层须为对象、数组或 {"rows":[...]}')
  }

  collectSemanticWarnings(rows, fields, addSemanticWarning)
  return result(rows, mode, [...usedFieldKeys].sort())
}
