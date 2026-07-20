import { useCallback, useEffect, useState } from 'react'
import { AutoComplete, Input } from 'antd'
import { DownOutlined } from '@ant-design/icons'
import { listHTTPMocks, type HTTPMockEndpoint } from '../api'

// HttpMockURLInput 既可从已启用的通用 Endpoint 选择，也允许手工输入任意 confirmUrlBeforeDial。
// 下拉选择时直接回填完整 invokeUrl；手工输入 /mock/{token} 时后端仍会兜底补全域名。
export function HttpMockURLInput({ value, onChange, placeholder }: {
  value?: string
  onChange?: (value: string) => void
  placeholder?: string
}) {
  const [endpoints, setEndpoints] = useState<HTTPMockEndpoint[]>([])
  const [open, setOpen] = useState(false)

  const load = useCallback(() => {
    listHTTPMocks()
      .then((result) => setEndpoints((result.endpoints || []).filter((endpoint) => endpoint.enabled && !!(endpoint.invokePath || endpoint.invokeUrl))))
      .catch(() => {})
  }, [])

  useEffect(() => { load() }, [load])

  const options = endpoints.flatMap((endpoint) => {
    const target = endpoint.invokeUrl
      || (endpoint.invokePath ? new URL(endpoint.invokePath, window.location.origin).toString() : '')
    const strategy = endpoint.config.defaultWeightedCases?.length ? '概率/参数规则' : '默认/参数规则'
    const base = [{ value: target, label: endpoint.name + ' · ' + strategy }]
    const cases = Object.keys(endpoint.config.cases || {}).sort().map((caseName) => ({
      value: target + (target.includes('?') ? '&' : '?') + '__mock_case=' + encodeURIComponent(caseName),
      label: endpoint.name + ' · case=' + caseName,
    }))
    return [...base, ...cases]
  })

  return (
    <AutoComplete
      value={value}
      open={open}
      onOpenChange={setOpen}
      onChange={(next) => { onChange?.(next); setOpen(true) }}
      onSelect={() => setOpen(false)}
      onFocus={() => { load(); setOpen(true) }}
      options={options}
      filterOption={(input, option) => {
        const keyword = input.toLowerCase()
        return String(option?.label || '').toLowerCase().includes(keyword)
          || String(option?.value || '').toLowerCase().includes(keyword)
      }}
      style={{ width: '100%' }}
    >
      <Input
        allowClear
        placeholder={placeholder || '可空；选择通用 HTTP Mock 或手工输入 URL / 相对路径'}
        suffix={<DownOutlined onMouseDown={(event) => event.preventDefault()} onClick={() => setOpen((current) => !current)} style={{ color: '#94a3b8', cursor: 'pointer' }} />}
      />
    </AutoComplete>
  )
}
