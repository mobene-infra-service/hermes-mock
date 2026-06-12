import { useEffect, useState } from 'react'
import {
  Card, Table, Tag, Button, message, Alert, Typography, Space, Modal, Form, Input, Select, Popconfirm,
} from 'antd'
import { CheckCircleTwoTone, PlusOutlined, ReloadOutlined, ApiOutlined } from '@ant-design/icons'
import { listOrgs, upsertOrg, deleteOrg, pingOrg, setCurrentOrg, type OrgConfig } from '../api'
import { resetSipWebrtcAddrCache } from '../sip/request'

const { Text, Paragraph } = Typography

// 机构配置：维护每个机构的 OpenAPI 接入凭据（网关 X-OpenApi-Key 或直连各服务地址）。
// mock 与 Hermes 的所有交互都走 OpenAPI、用这套凭据；一键切换当前测试机构。可新增/编辑/删除/连通测试。
export default function OrgsPage() {
  const [form] = Form.useForm()
  const [orgs, setOrgs] = useState<OrgConfig[]>([])
  const [current, setCurrent] = useState('')
  const [open, setOpen] = useState(false)
  const [mode, setMode] = useState<'direct' | 'gateway'>('direct')

  const load = async () => {
    try {
      const r = await listOrgs()
      setOrgs(r.orgs || [])
      setCurrent(r.current)
    } catch (e) { message.error(String(e)) }
  }
  useEffect(() => { load() }, [])

  const onNew = () => {
    form.resetFields()
    form.setFieldsValue({ mode: 'direct' })
    setMode('direct')
    setOpen(true)
  }
  const onEdit = (o: OrgConfig) => {
    form.setFieldsValue(o)
    setMode(o.mode)
    setOpen(true)
  }
  const onSave = async () => {
    let v
    try { v = await form.validateFields() } catch { return }
    try {
      await upsertOrg(v)
      message.success('机构配置已保存')
      setOpen(false)
      load()
    } catch (e) { message.error(String(e)) }
  }
  const onSwitch = async (code: string) => {
    try { await setCurrentOrg(code); resetSipWebrtcAddrCache(); message.success(`当前机构 → ${code}`); load() }
    catch (e) { message.error(String(e)) }
  }
  const onPing = async (code: string) => {
    const r = await pingOrg(code)
    if (r.ok) message.success(`机构 ${code} OpenAPI 连通：${r.msg || ''}`)
    else message.error(`机构 ${code} 连通失败：${r.error}`)
  }
  const onDelete = async (code: string) => {
    await deleteOrg(code); message.success('已删除'); load()
  }

  return (
    <div className="page-container">
      <Alert
        type="info"
        style={{ marginBottom: 16 }}
        message={<span>当前测试机构：<Tag color="blue" style={{ fontSize: 14 }}>{current || '未选择'}</Tag></span>}
        description="mock 与 Hermes 的所有交互只走 OpenAPI（绝不直连 Hermes 数据库）。这里配置每个机构的接入凭据：①网关模式=网关地址+X-OpenApi-Key；②直连模式=各服务地址+机构码（无网关环境用）。切换当前机构后，坐席/任务/对话等都用该机构凭据。"
      />
      <Card title="机构 OpenAPI 接入配置" size="small"
        extra={<Space>
          <Button size="small" icon={<ReloadOutlined />} onClick={load}>刷新</Button>
          <Button type="primary" size="small" icon={<PlusOutlined />} onClick={onNew}>新增机构</Button>
        </Space>}>
        <Table rowKey="orgCode" size="small" dataSource={orgs} pagination={false}
          columns={[
            {
              title: '机构码', dataIndex: 'orgCode',
              render: (v: string) => <Space><Text code>{v}</Text>{v === current && <CheckCircleTwoTone twoToneColor="#52c41a" />}</Space>,
            },
            { title: '名称', dataIndex: 'orgName' },
            { title: '模式', dataIndex: 'mode', width: 90, render: (m: string) => <Tag color={m === 'gateway' ? 'purple' : 'blue'}>{m === 'gateway' ? '网关' : '直连'}</Tag> },
            {
              title: '接入地址', render: (_: unknown, o: OrgConfig) => (
                <Text style={{ fontSize: 12 }} type="secondary">
                  {o.mode === 'gateway' ? (o.gatewayUrl || '-') : (o.basicUrl || o.callCenterUrl || '-')}
                </Text>
              ),
            },
            {
              title: '默认坐席参数', render: (_: unknown, o: OrgConfig) => (
                <Space size={4} wrap>
                  {o.defaultAgentGroupCode && <Tag>{o.defaultAgentGroupCode}</Tag>}
                  {o.defaultDepCode && <Tag>{o.defaultDepCode}</Tag>}
                  {o.defaultAgentRoleCode && <Tag>{o.defaultAgentRoleCode}</Tag>}
                </Space>
              ),
            },
            {
              title: '操作', width: 280, render: (_: unknown, o: OrgConfig) => (
                <Space size={4}>
                  {o.orgCode !== current && <Button size="small" type="primary" ghost onClick={() => onSwitch(o.orgCode)}>切到此</Button>}
                  <Button size="small" icon={<ApiOutlined />} onClick={() => onPing(o.orgCode)}>连通测试</Button>
                  <a onClick={() => onEdit(o)}>编辑</a>
                  <Popconfirm title="删除该机构配置？" onConfirm={() => onDelete(o.orgCode)}><a style={{ color: '#ff4d4f' }}>删除</a></Popconfirm>
                </Space>
              ),
            },
          ]} />
      </Card>

      <Modal title="机构 OpenAPI 配置" open={open} onOk={onSave} onCancel={() => setOpen(false)} destroyOnHidden forceRender width={560}>
        <Form form={form} layout="vertical">
          <Space>
            <Form.Item name="orgCode" label="机构码" rules={[{ required: true }]}><Input style={{ width: 200 }} placeholder="org001" /></Form.Item>
            <Form.Item name="orgName" label="机构名"><Input style={{ width: 200 }} /></Form.Item>
          </Space>
          <Form.Item name="mode" label="接入模式" rules={[{ required: true }]}>
            <Select onChange={(v) => setMode(v)} options={[
              { value: 'direct', label: '直连服务（注入 ORG 头）' },
              { value: 'gateway', label: '网关（X-OpenApi-Key，生产）' },
            ]} />
          </Form.Item>
          {mode === 'gateway' ? (
            <>
              <Form.Item name="gatewayUrl" label="网关地址" rules={[{ required: true }]}><Input placeholder="https://gateway.example.com" /></Form.Item>
              <Form.Item name="apiKey" label="X-OpenApi-Key（机构密钥）" rules={[{ required: true }]}><Input.Password placeholder="机构申请的 OpenAPI 密钥" /></Form.Item>
            </>
          ) : (
            <>
              <Form.Item name="basicUrl" label="basic 服务地址"><Input placeholder="http://hermes-basic:8080" /></Form.Item>
              <Form.Item name="callCenterUrl" label="call-center 服务地址"><Input placeholder="http://hermes-call-center:8080" /></Form.Item>
              <Form.Item name="callBotUrl" label="call-bot 服务地址"><Input placeholder="http://hermes-call-bot:8080（可空）" /></Form.Item>
              <Form.Item name="otpUrl" label="otp 服务地址"><Input placeholder="http://hermes-otp:8080（可空）" /></Form.Item>
              <Form.Item name="userCode" label="操作人(审计头)"><Input placeholder="操作人标识，随 OpenAPI 请求记审计" /></Form.Item>
            </>
          )}
          <Form.Item name="agentWsUrl" label="hermes-ws 工作台地址" tooltip="hermes-ws 服务的 host:port；留空时按 call-center/basic 服务地址自动推导">
            <Input placeholder="hermes-ws:8081（可空，按服务地址推导）" />
          </Form.Item>
          <Space>
            <Form.Item name="defaultAgentGroupCode" label="默认技能组"><Input style={{ width: 150 }} placeholder="新建坐席默认值" /></Form.Item>
            <Form.Item name="defaultDepCode" label="默认部门"><Input style={{ width: 130 }} placeholder="可空" /></Form.Item>
            <Form.Item name="defaultAgentRoleCode" label="默认角色"><Input style={{ width: 130 }} placeholder="可空" /></Form.Item>
          </Space>
          <Form.Item name="defaultAgentPassword" label="新坐席默认口令"><Input style={{ width: 180 }} placeholder="新建坐席的初始口令" /></Form.Item>
          <Paragraph type="secondary" style={{ fontSize: 12, marginBottom: 0 }}>
            保存后点「连通测试」会用坐席分页 OpenAPI 验证凭据是否可用。
          </Paragraph>
        </Form>
      </Modal>
    </div>
  )
}
