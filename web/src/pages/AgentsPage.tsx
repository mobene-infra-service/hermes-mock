import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import {
  Alert, Button, Card, Form, Input, InputNumber, Modal, Popconfirm, Select, Space, Table, Tag, Typography, message,
} from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import {
  listManagedAgents, addManagedAgent, updateManagedAgent, deleteManagedAgent, setManagedAgentEnabled, listOrgs,
  type ManagedAgent, type AddAgentReq, type UpdateAgentReq, type OrgConfig,
} from '../api'

const { Text } = Typography

// 账号启用状态（StatusEnum）：1=启用 / 0=停用 / 2=已删。与工作态（在线/小休/忙）无关——
// 工作态/在线/外呼属于「坐席外呼」页（浏览器 jssip 软电话），本页只做坐席台账管理（CRUD）。
const ACCOUNT_STATUS: Record<string, { text: string; color: string }> = {
  '1': { text: '启用', color: 'green' }, '0': { text: '停用', color: 'red' }, '2': { text: '已删', color: 'default' },
}

export default function AgentsPage() {
  const [agents, setAgents] = useState<ManagedAgent[]>([])
  const [total, setTotal] = useState(0)
  const [loading, setLoading] = useState(false)
  const [loadErr, setLoadErr] = useState('') // 加载失败原因（如未配机构），页内引导而非弹错
  const [q, setQ] = useState<{ number?: string; agentName?: string; agentGroupCode?: string }>({})
  const [open, setOpen] = useState(false)
  const [editing, setEditing] = useState<ManagedAgent | null>(null)
  const [org, setOrg] = useState<OrgConfig | null>(null) // 当前机构配置（表单默认值/占位联动）
  const [form] = Form.useForm()

  const loadAgents = async () => {
    setLoading(true)
    try {
      const r = await listManagedAgents({ pageNum: 1, pageSize: 200, ...q })
      setAgents(r.agents || []); setTotal(r.total || 0); setLoadErr('')
    } catch (e) { setLoadErr(e instanceof Error ? e.message : String(e)) } finally { setLoading(false) }
  }
  useEffect(() => {
    loadAgents()
    listOrgs().then((r) => setOrg(r.orgs.find((o) => o.orgCode === r.current) || null)).catch(() => {})
  }, []) // eslint-disable-line

  const onNew = () => {
    setEditing(null); form.resetFields()
    // 默认值联动当前机构配置（机构页的默认技能组/部门/角色/口令）
    form.setFieldsValue({
      status: 1,
      password: org?.defaultAgentPassword || undefined,
      agentGroupCode: org?.defaultAgentGroupCode || undefined,
      depCode: org?.defaultDepCode || undefined,
      agentRoleCode: org?.defaultAgentRoleCode || undefined,
    })
    setOpen(true)
  }
  const onEdit = (r: ManagedAgent) => {
    setEditing(r); form.resetFields()
    form.setFieldsValue({ agentName: r.agentName, agentGroupCode: r.agentGroupCode, depCode: r.depCode })
    setOpen(true)
  }
  const onSave = async () => {
    let v
    try { v = await form.validateFields() } catch { return }
    try {
      if (editing) await updateManagedAgent({ agentNumber: editing.number || '', ...v } as UpdateAgentReq)
      else await addManagedAgent(v as AddAgentReq)
      message.success('已保存'); setOpen(false); loadAgents()
    } catch (e) { message.error(String(e)) }
  }
  const onDelete = async (r: ManagedAgent) => {
    try { await deleteManagedAgent(r.number || ''); message.success('已删除'); loadAgents() } catch (e) { message.error(String(e)) }
  }
  const onToggleEnabled = async (r: ManagedAgent, enabled: boolean) => {
    try { await setManagedAgentEnabled([r.agentCode || ''], enabled); message.success(enabled ? '账号已启用' : '账号已停用'); loadAgents() } catch (e) { message.error(String(e)) }
  }

  return (
    <div className="page-container">
      <Alert type="info" showIcon style={{ marginBottom: 16 }}
        message="坐席管理（真实 Hermes 坐席台账，经 OpenAPI 增删改）"
        description="本页只管坐席台账：查询 / 新增 / 编辑 / 删除 / 启停账号（写 t_agent 由 Hermes 完成，mock 只调 OpenAPI、不直连库）。坐席的上线 / 工作态 / 通话外呼在「重点通话场景 → 坐席外呼」页（浏览器 jssip 软电话）。删除启用中的坐席前需先「停用」。" />

      {loadErr && (
        <Alert type="warning" showIcon style={{ marginBottom: 16 }}
          message="坐席列表加载失败" description={<span>{loadErr}{loadErr.includes('机构') && <>　<Link to="/orgs">去「机构」页配置 →</Link></>}</span>} />
      )}

      <Card size="small" style={{ marginBottom: 12 }}>
        <Space wrap>
          <Input placeholder="坐席号" style={{ width: 120 }} allowClear value={q.number} onChange={(e) => setQ({ ...q, number: e.target.value })} />
          <Input placeholder="坐席名" style={{ width: 120 }} allowClear value={q.agentName} onChange={(e) => setQ({ ...q, agentName: e.target.value })} />
          <Input placeholder="技能组" style={{ width: 120 }} allowClear value={q.agentGroupCode} onChange={(e) => setQ({ ...q, agentGroupCode: e.target.value })} />
          <Button type="primary" onClick={loadAgents}>查询</Button>
          <Button onClick={onNew}>新增坐席</Button>
          <Button icon={<ReloadOutlined />} onClick={loadAgents}>刷新</Button>
          <Text type="secondary">共 {total}</Text>
        </Space>
      </Card>

      <Table<ManagedAgent>
        rowKey={(r) => r.number || r.agentCode || ''}
        size="small" loading={loading} dataSource={agents} pagination={{ pageSize: 20, showSizeChanger: true }} scroll={{ x: 800 }}
        columns={[
          { title: '坐席号', dataIndex: 'number', width: 100, fixed: 'left' },
          { title: '坐席名', dataIndex: 'agentName', width: 140 },
          { title: '坐席码', dataIndex: 'agentCode', width: 140, ellipsis: true },
          { title: '技能组', dataIndex: 'agentGroupCode', width: 120 },
          { title: '部门', dataIndex: 'depCode', width: 100 },
          {
            title: '账号状态', dataIndex: 'status', width: 90,
            render: (s: unknown) => { const k = String(s ?? ''); const m = ACCOUNT_STATUS[k]; return m ? <Tag color={m.color}>{m.text}</Tag> : <Tag>{k || '-'}</Tag> },
          },
          {
            title: '操作', width: 220, fixed: 'right', render: (_: unknown, r: ManagedAgent) => (
              <Space size={8} wrap>
                <a onClick={() => onEdit(r)}>编辑</a>
                <a onClick={() => onToggleEnabled(r, true)}>启用</a>
                <a onClick={() => onToggleEnabled(r, false)}>停用</a>
                <Popconfirm title={`删除坐席 ${r.number}？（启用中需先停用）`} okText="删除" okButtonProps={{ danger: true }} onConfirm={() => onDelete(r)}>
                  <a style={{ color: '#cf1322' }}>删除</a>
                </Popconfirm>
              </Space>
            ),
          },
        ]} />

      <Modal title={editing ? `编辑坐席 ${editing.number}` : '新增坐席'} open={open} onOk={onSave} onCancel={() => setOpen(false)} destroyOnHidden forceRender>
        <Form form={form} layout="vertical">
          {!editing && (
            <Alert type="info" showIcon style={{ marginBottom: 16 }}
              message="坐席号由 Hermes 自动生成，无需填写。"
              description="账号状态(启用/停用)是 StatusEnum 数字；与工作态(在线/小休)无关，工作态在「坐席外呼」页切换。" />
          )}
          <Form.Item name="agentName" label="坐席名"><Input /></Form.Item>
          {!editing && <Form.Item name="password" label="登录口令" rules={[{ required: true }]}><Input.Password placeholder="新坐席登录口令" /></Form.Item>}
          <Form.Item name="agentGroupCode" label="技能组 code"><Input placeholder={org?.defaultAgentGroupCode || '技能组 code'} /></Form.Item>
          <Form.Item name="depCode" label="部门 code" tooltip="留空则由 Hermes 落到机构根部门"><Input placeholder={org?.defaultDepCode || '留空走根部门'} /></Form.Item>
          <Form.Item name="agentRoleCode" label="角色 code" tooltip="留空则不绑定角色"><Input placeholder="留空不绑角色" /></Form.Item>
          {!editing && <Form.Item name="phoneCode" label="外显号 phoneCode"><Input /></Form.Item>}
          <Form.Item name="callProcessTime" label="话后处理时长(秒)"><InputNumber min={0} /></Form.Item>
          {!editing && (
            <Form.Item name="status" label="账号状态（StatusEnum）">
              <Select options={[{ value: 1, label: '启用 (ENABLED=1)' }, { value: 0, label: '停用 (DISABLED=0)' }]} />
            </Form.Item>
          )}
        </Form>
      </Modal>
    </div>
  )
}
