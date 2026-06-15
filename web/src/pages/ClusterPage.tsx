import { useEffect, useState } from 'react'
import {
  Card, Table, Button, Modal, Form, Input, InputNumber, Select, Tabs, Tag, message, Space, Alert, Tooltip, Typography, Switch, Divider, Popconfirm, Upload,
} from 'antd'
import {
  listProfiles, upsertProfile, deleteProfile, listGroups, upsertGroup, deleteGroup,
  listOverrides, upsertOverride, deleteOverride, listBindings, upsertBinding, deleteBinding,
  clusterResolve, setGroupState, setCustomerState, listAudio, uploadAudio,
  bootstrapDemo,
  type BehaviorProfile, type CustomerGroup, type CustomerOverride, type LineBinding, type IVRStep, type AudioFile,
} from '../api'
import { OUTCOME_OPTIONS, FAULT_OPTIONS } from '../constants/enums'
import { PageHeader } from '../components/layout/PageHeader'
import { InfoBanner } from '../components/layout/InfoBanner'

const { Text } = Typography


// 通用「列表 + 新增/编辑弹窗 + 删除」工厂
function useCrud<T extends object, K extends string | number = string>(load: () => Promise<T[]>, save: (v: T) => Promise<T>, del?: (key: K) => Promise<void>) {
  const [data, setData] = useState<T[]>([])
  const [open, setOpen] = useState(false)
  const [form] = Form.useForm<T>()
  const refresh = async () => {
    try { setData(await load()) } catch (e) { message.error(String(e)) }
  }
  useEffect(() => { refresh() }, []) // eslint-disable-line
  const onNew = (init?: Partial<T>) => { form.resetFields(); if (init) form.setFieldsValue(init as T); setOpen(true) }
  const onEdit = (row: T) => { form.setFieldsValue(row); setOpen(true) }
  const onSave = async () => {
    let v
    try { v = await form.validateFields() } catch { return }
    try { await save(v); message.success('已保存'); setOpen(false); refresh() }
    catch (e) { message.error(String(e)) }
  }
  const onDelete = async (key: K) => {
    if (!del) return
    try { await del(key); message.success('已删除'); refresh() }
    catch (e) { message.error(String(e)) }
  }
  return { data, open, setOpen, form, refresh, onNew, onEdit, onSave, onDelete }
}

// 操作列：编辑 + 删除（带二次确认）
function rowActions<T>(onEdit: (r: T) => void, onDelete: (r: T) => void, confirmTitle: (r: T) => string) {
  return (_: unknown, r: T) => (
    <Space size={8}>
      <a onClick={() => onEdit(r)}>编辑</a>
      <Popconfirm title={confirmTitle(r)} okText="删除" okButtonProps={{ danger: true }} onConfirm={() => onDelete(r)}>
        <a style={{ color: '#cf1322' }}>删除</a>
      </Popconfirm>
    </Space>
  )
}

// IVREditor 脚本化 IVR 编辑器（受控）：value/onChange 为 JSON 字符串（[]IVRStep）。
// 每步：步骤 ID、放音文件、等待按键 ms、进入先发 DTMF、按键→下一步分支、超时去向。
// 特殊跳转目标 "HANGUP" 表示挂断；分支目标填其他步骤的 ID。
function IVREditor({ value, onChange }: { value?: string; onChange?: (v: string) => void }) {
  let steps: IVRStep[] = []
  try { if (value) steps = JSON.parse(value) as IVRStep[] } catch { steps = [] }
  if (!Array.isArray(steps)) steps = []
  const emit = (next: IVRStep[]) => onChange?.(next.length ? JSON.stringify(next) : '')
  const update = (i: number, patch: Partial<IVRStep>) => emit(steps.map((s, idx) => (idx === i ? { ...s, ...patch } : s)))
  const addStep = () => emit([...steps, { id: steps.length ? `s${steps.length + 1}` : 'start', prompt: '', waitMs: 5000, branch: {} }])
  const removeStep = (i: number) => emit(steps.filter((_, idx) => idx !== i))
  const branchPairs = (b?: Record<string, string>) => Object.entries(b || {})
  const setBranch = (i: number, pairs: [string, string][]) => {
    const b: Record<string, string> = {}
    for (const [k, v] of pairs) if (k.trim() !== '') b[k.trim()] = v
    update(i, { branch: b })
  }
  return (
    <div>
      {steps.length === 0 && <Text type="secondary">未配置 IVR 脚本（留空＝按普通放音/DTMF 行为）。点「添加步骤」开始。</Text>}
      {steps.map((s, i) => (
        <Card key={i} size="small" style={{ marginBottom: 8 }} title={`步骤 ${i + 1}${i === 0 ? '（首步）' : ''}`}
          extra={<a onClick={() => removeStep(i)}>删除</a>}>
          <Space wrap>
            <span>ID <Input style={{ width: 110 }} value={s.id} onChange={(e) => update(i, { id: e.target.value })} placeholder="start" /></span>
            <span>放音 <Input style={{ width: 160 }} value={s.prompt} onChange={(e) => update(i, { prompt: e.target.value })} placeholder="menu.wav" /></span>
            <span>等待ms <InputNumber min={0} value={s.waitMs} onChange={(v) => update(i, { waitMs: v ?? undefined })} placeholder="5000" /></span>
            <span>先发DTMF <Input style={{ width: 90 }} value={s.sendDtmf} onChange={(e) => update(i, { sendDtmf: e.target.value })} placeholder="可选" /></span>
            <span>超时去向 <Input style={{ width: 120 }} value={s.onNoKey} onChange={(e) => update(i, { onNoKey: e.target.value })} placeholder="HANGUP/步骤ID" /></span>
          </Space>
          <div style={{ marginTop: 8 }}>
            <Text type="secondary">按键分支（键→下一步 ID；填 HANGUP 挂断）：</Text>
            {branchPairs(s.branch).map(([k, v], bi) => (
              <Space key={bi} style={{ display: 'flex', marginTop: 4 }}>
                <Input style={{ width: 70 }} value={k} placeholder="按键" onChange={(e) => {
                  const pairs = branchPairs(s.branch); pairs[bi] = [e.target.value, v]; setBranch(i, pairs as [string, string][])
                }} />
                <span>→</span>
                <Input style={{ width: 140 }} value={v} placeholder="步骤ID / HANGUP" onChange={(e) => {
                  const pairs = branchPairs(s.branch); pairs[bi] = [k, e.target.value]; setBranch(i, pairs as [string, string][])
                }} />
                <a onClick={() => setBranch(i, branchPairs(s.branch).filter((_, idx) => idx !== bi) as [string, string][])}>移除</a>
              </Space>
            ))}
            <div style={{ marginTop: 4 }}>
              <Button size="small" onClick={() => setBranch(i, [...branchPairs(s.branch), ['', '']] as [string, string][])}>+ 按键分支</Button>
            </div>
          </div>
        </Card>
      ))}
      <Button size="small" type="dashed" onClick={addStep} style={{ marginTop: 4 }}>添加步骤</Button>
    </div>
  )
}

// PlaybackSelect 放音文件下拉 + 上传 .wav（受控，对接 /api/audio）。
function PlaybackSelect({ value, onChange }: { value?: string; onChange?: (v?: string) => void }) {
  const [audios, setAudios] = useState<AudioFile[]>([])
  const refresh = () => listAudio().then(setAudios).catch(() => {})
  useEffect(() => { refresh() }, [])
  return (
    <Space>
      <Select showSearch allowClear style={{ width: 240 }} value={value || undefined} onChange={(v) => onChange?.(v)}
        placeholder="选已有 .wav（或上传）"
        options={audios.map((a) => ({ value: a.name, label: `${a.name}（${Math.round((a.size || 0) / 1024)}KB）` }))} />
      <Upload showUploadList={false} accept=".wav" beforeUpload={(file) => {
        uploadAudio(file as File).then(async (a) => {
          await refresh()
          if (a?.name) onChange?.(a.name)
          message.success(`已上传 ${a?.name || (file as File).name}`)
        }).catch((e) => message.error(String(e)))
        return false
      }}><Button size="small">上传 .wav</Button></Upload>
    </Space>
  )
}

function ProfilesTab() {
  const c = useCrud<BehaviorProfile>(listProfiles, upsertProfile, deleteProfile)
  return (
    <>
      <Space style={{ marginBottom: 12 }}>
        <Button type="primary" onClick={() => c.onNew({ outcome: 'ANSWER', talkMs: 8000, answerRatio: 100 })}>新增行为档</Button>
        <Button onClick={c.refresh}>刷新</Button>
      </Space>
      <Table rowKey="code" size="small" dataSource={c.data} pagination={false}
        columns={[
          { title: 'code', dataIndex: 'code' },
          { title: '名称', dataIndex: 'name' },
          { title: '结果', dataIndex: 'outcome', render: (o: string) => <Tag color={o === 'ANSWER' ? 'green' : o === 'BRIDGE' ? 'blue' : 'orange'}>{o}</Tag> },
          { title: '振铃ms', dataIndex: 'ringMs' },
          { title: '通话ms', dataIndex: 'talkMs' },
          { title: '接通率%', dataIndex: 'answerRatio' },
          { title: '放音', dataIndex: 'playback' },
          { title: 'DTMF', dataIndex: 'dtmf' },
          { title: '故障', dataIndex: 'fault' },
          { title: 'IVR', dataIndex: 'ivrJson', render: (v?: string) => (v ? <Tag color="purple">脚本</Tag> : null) },
          { title: '操作', render: rowActions<BehaviorProfile>(c.onEdit, (r) => c.onDelete(r.code), (r) => `删除行为档 ${r.code}？`) },
        ]} />
      <Modal title="行为档" open={c.open} onOk={c.onSave} onCancel={() => c.setOpen(false)} destroyOnHidden forceRender width={680}>
        <Form form={c.form} layout="vertical">
          <Form.Item name="id" hidden><Input /></Form.Item>
          <Space><Form.Item name="code" label="code" rules={[{ required: true }]}><Input style={{ width: 180 }} /></Form.Item>
            <Form.Item name="name" label="名称"><Input style={{ width: 220 }} /></Form.Item></Space>
          <Form.Item name="outcome" label="通话结果" rules={[{ required: true }]}>
            <Select options={OUTCOME_OPTIONS} />
          </Form.Item>
          <Space wrap>
            <Form.Item name="ringMs" label="振铃ms"><InputNumber min={0} /></Form.Item>
            <Form.Item name="talkMs" label="通话ms"><InputNumber min={0} /></Form.Item>
            <Form.Item name="hangupCode" label="挂断码"><InputNumber min={0} placeholder="486/503/480" /></Form.Item>
            <Form.Item name="answerRatio" label="接通率%"><InputNumber min={0} max={100} /></Form.Item>
          </Space>
          <Form.Item name="playback" label="放音文件"><PlaybackSelect /></Form.Item>
          <Form.Item name="dtmf" label="DTMF 序列"><Input placeholder="159#" /></Form.Item>
          <Space>
            <Form.Item name="fault" label="故障注入"><Select style={{ width: 320 }} allowClear options={FAULT_OPTIONS} /></Form.Item>
            <Form.Item name="bridgeTarget" label="桥接目标"><Input style={{ width: 240 }} placeholder="sip:9999@..." /></Form.Item>
            <Form.Item name="expectDtmf" label="监听对端按键" valuePropName="checked"><Switch /></Form.Item>
          </Space>
          <Divider orientation="left" plain>脚本化 IVR（接听后「放音→收按键→分支」多轮；优先于普通放音/DTMF，且仅在无故障时生效）</Divider>
          <Form.Item name="ivrJson"><IVREditor /></Form.Item>
        </Form>
      </Modal>
    </>
  )
}

function GroupsTab() {
  const c = useCrud<CustomerGroup>(listGroups, upsertGroup, deleteGroup)
  const [profiles, setProfiles] = useState<BehaviorProfile[]>([])
  const refreshProfiles = () => listProfiles().then(setProfiles).catch(() => {})
  useEffect(() => { refreshProfiles() }, [])
  const onNew = async () => {
    await refreshProfiles()
    c.onNew({ numberStart: 0, count: 100, state: 'ENABLED' })
  }
  const onEdit = async (row: CustomerGroup) => {
    await refreshProfiles()
    c.onEdit(row)
  }
  return (
    <>
      <Alert type="info" style={{ marginBottom: 12 }} showIcon
        message="客户组 = 一个号段批量 N 个虚拟客户。改组的行为档/状态 → 整批生效。" />
      <Space style={{ marginBottom: 12 }}>
        <Button type="primary" onClick={onNew}>新增客户组</Button>
        <Button onClick={c.refresh}>刷新</Button>
      </Space>
      <Table rowKey="code" size="small" dataSource={c.data} pagination={false}
        columns={[
          { title: 'code', dataIndex: 'code' },
          { title: '名称', dataIndex: 'name' },
          { title: '号段', render: (_, r) => <Tooltip title={`${r.numberPrefix}${r.numberStart} ~ +${r.count}`}>{r.numberPrefix}{r.numberStart}<Text>…</Text></Tooltip> },
          { title: '数量', dataIndex: 'count' },
          { title: '行为档', dataIndex: 'behaviorCode', render: (v: string) => <Tag>{v}</Tag> },
          { title: '状态', dataIndex: 'state', render: (s: string) => <Tag color={s === 'ENABLED' ? 'green' : 'red'}>{s === 'ENABLED' ? '在线' : '离线'}</Tag> },
          {
            title: '在线控制', render: (_, r) => (
              <Space size={4}>
                <Button size="small" type={r.state === 'ENABLED' ? 'primary' : 'default'}
                  onClick={async () => { await setGroupState(r.code, 'ENABLED'); message.success(`客户组 ${r.code} → 在线`); c.refresh() }}>上线</Button>
                <Button size="small" danger={r.state !== 'DISABLED'}
                  onClick={async () => { await setGroupState(r.code, 'DISABLED'); message.success(`客户组 ${r.code} → 离线(呼叫返回503)`); c.refresh() }}>离线</Button>
              </Space>
            )
          },
          { title: '操作', render: rowActions<CustomerGroup>(onEdit, (r) => c.onDelete(r.code), (r) => `删除客户组 ${r.code}？`) },
        ]} />
      <Modal title="客户组" open={c.open} onOk={c.onSave} onCancel={() => c.setOpen(false)} destroyOnHidden forceRender width={520}>
        <Form form={c.form} layout="vertical">
          <Form.Item name="id" hidden><Input /></Form.Item>
          <Space><Form.Item name="code" label="code" rules={[{ required: true }]}><Input style={{ width: 160 }} /></Form.Item>
            <Form.Item name="name" label="名称"><Input style={{ width: 220 }} /></Form.Item></Space>
          <Space><Form.Item name="numberPrefix" label="号码前缀"><Input style={{ width: 160 }} placeholder="8613800" /></Form.Item>
            <Form.Item name="numberStart" label="号段起始" rules={[{ required: true }]}><InputNumber style={{ width: 160 }} /></Form.Item>
            <Form.Item name="count" label="数量" rules={[{ required: true }]}><InputNumber min={1} /></Form.Item></Space>
          <Form.Item name="behaviorCode" label="行为档" rules={[{ required: true }]}>
            <Select showSearch options={profiles.map((p) => ({ value: p.code, label: `${p.code}（${p.outcome}）` }))} />
          </Form.Item>
          <Form.Item name="state" label="状态"><Select options={[{ value: 'ENABLED', label: 'ENABLED' }, { value: 'DISABLED', label: 'DISABLED' }]} /></Form.Item>
        </Form>
      </Modal>
    </>
  )
}

function BindingsTab() {
  const c = useCrud<LineBinding, number>(listBindings, upsertBinding, deleteBinding)
  const [groups, setGroups] = useState<CustomerGroup[]>([])
  const refreshGroups = () => listGroups().then(setGroups).catch(() => {})
  useEffect(() => { refreshGroups() }, [])
  const onNew = async () => {
    await refreshGroups()
    c.onNew({ enabled: 1 })
  }
  const onEdit = async (row: LineBinding) => {
    await refreshGroups()
    c.onEdit(row)
  }
  return (
    <>
      <Alert type="info" style={{ marginBottom: 12 }} showIcon
        message="入口端口绑定 = 把「mock SIP 监听端口」对应到「客户组」。Hermes 侧线路指向 mockIP:端口；该端口来的呼叫就按对应客户组应答。" />
      <Space style={{ marginBottom: 12 }}>
        <Button type="primary" onClick={onNew}>新增绑定</Button>
        <Button onClick={c.refresh}>刷新</Button>
      </Space>
      <Table rowKey="listenPort" size="small" dataSource={c.data} pagination={false}
        columns={[
          { title: 'SIP 端口', dataIndex: 'listenPort' },
          { title: '线路名', dataIndex: 'lineName', render: (v: string) => v || <Text type="secondary">-</Text> },
          { title: '客户组', dataIndex: 'groupCode', render: (v: string) => <Tag>{v}</Tag> },
          { title: '启用', dataIndex: 'enabled', render: (v: number) => <Tag color={v ? 'green' : 'default'}>{v ? '是' : '否'}</Tag> },
          { title: '操作', render: rowActions<LineBinding>(onEdit, (r) => c.onDelete(r.listenPort), (r) => `删除端口绑定 ${r.listenPort}？`) },
        ]} />
      <Modal title="入口端口绑定" open={c.open} onOk={c.onSave} onCancel={() => c.setOpen(false)} destroyOnHidden forceRender>
        <Form form={c.form} layout="vertical">
          <Form.Item name="id" hidden><Input /></Form.Item>
          <Form.Item name="listenPort" label="SIP 入口端口" rules={[{ required: true }]}>
            <InputNumber min={1} max={65535} precision={0} style={{ width: '100%' }} placeholder="5060" />
          </Form.Item>
          <Form.Item name="lineName" label="线路名（可选）"><Input placeholder="line-base-a" /></Form.Item>
          <Form.Item name="groupCode" label="客户组" rules={[{ required: true }]}>
            <Select showSearch options={groups.map((g) => ({ value: g.code, label: g.code }))} />
          </Form.Item>
          <Form.Item name="enabled" label="启用" initialValue={1}><Select options={[{ value: 1, label: '是' }, { value: 0, label: '否' }]} /></Form.Item>
        </Form>
      </Modal>
    </>
  )
}

function OverridesTab() {
  const c = useCrud<CustomerOverride>(listOverrides, upsertOverride, deleteOverride)
  const [profiles, setProfiles] = useState<BehaviorProfile[]>([])
  const refreshProfiles = () => listProfiles().then(setProfiles).catch(() => {})
  useEffect(() => { refreshProfiles() }, [])
  const onNew = async () => {
    await refreshProfiles()
    c.onNew({ state: 'ENABLED' })
  }
  const onEdit = async (row: CustomerOverride) => {
    await refreshProfiles()
    c.onEdit(row)
  }
  return (
    <>
      <Alert type="info" style={{ marginBottom: 12 }} showIcon message="个例覆盖 = 组内个别号码的例外行为/状态（覆盖组默认）。" />
      <Space style={{ marginBottom: 12 }}><Button type="primary" onClick={onNew}>新增个例</Button><Button onClick={c.refresh}>刷新</Button></Space>
      <Table rowKey="number" size="small" dataSource={c.data} pagination={false}
        columns={[
          { title: '号码', dataIndex: 'number' },
          { title: '所属组', dataIndex: 'groupCode' },
          { title: '覆盖行为档', dataIndex: 'behaviorCode', render: (v: string) => v ? <Tag>{v}</Tag> : <Text type="secondary">（仅改状态）</Text> },
          { title: '状态', dataIndex: 'state', render: (s: string) => <Tag color={s === 'ENABLED' ? 'green' : 'red'}>{s}</Tag> },
          {
            title: '在线控制', render: (_, r) => (
              <Space size={4}>
                <Button size="small" type={r.state === 'ENABLED' ? 'primary' : 'default'}
                  onClick={async () => { await setCustomerState(r.number, r.groupCode || '', 'ENABLED'); message.success(`${r.number} → 在线`); c.refresh() }}>上线</Button>
                <Button size="small" danger={r.state !== 'DISABLED'}
                  onClick={async () => { await setCustomerState(r.number, r.groupCode || '', 'DISABLED'); message.success(`${r.number} → 离线`); c.refresh() }}>离线</Button>
              </Space>
            )
          },
          { title: '操作', render: rowActions<CustomerOverride>(onEdit, (r) => c.onDelete(r.number), (r) => `删除个例 ${r.number}？`) },
        ]} />
      <Modal title="客户个例覆盖" open={c.open} onOk={c.onSave} onCancel={() => c.setOpen(false)} destroyOnHidden forceRender>
        <Form form={c.form} layout="vertical">
          <Form.Item name="id" hidden><Input /></Form.Item>
          <Form.Item name="number" label="客户号码" rules={[{ required: true }]}><Input /></Form.Item>
          <Form.Item name="groupCode" label="所属组 code"><Input /></Form.Item>
          <Form.Item name="behaviorCode" label="覆盖行为档（空=仅改状态）"><Select allowClear options={profiles.map((p) => ({ value: p.code, label: p.code }))} /></Form.Item>
          <Form.Item name="state" label="状态"><Select options={[{ value: 'ENABLED', label: 'ENABLED' }, { value: 'DISABLED', label: 'DISABLED' }]} /></Form.Item>
        </Form>
      </Modal>
    </>
  )
}

function ResolvePreview() {
  const [number, setNumber] = useState('')
  const [listenPort, setListenPort] = useState<number | null>(null)
  const [res, setRes] = useState<string>('')
  const onResolve = async () => {
    try {
      const r = await clusterResolve(number, listenPort || undefined)
      setRes(JSON.stringify(r, null, 2))
    } catch (e) { setRes(String(e)) }
  }
  return (
    <Card size="small" title="解析预览（给被叫号 + 可选入口端口，看命中哪个组/行为）" style={{ marginBottom: 16 }}>
      <Space>
        <Input placeholder="被叫号 如 86138000005" value={number} onChange={(e) => setNumber(e.target.value)} style={{ width: 220 }} />
        <InputNumber min={1} max={65535} precision={0} placeholder="入口端口（可选）" value={listenPort} onChange={(v) => setListenPort(v)} style={{ width: 180 }} />
        <Button type="primary" onClick={onResolve}>解析</Button>
      </Space>
      {res && <pre style={{ marginTop: 12, fontSize: 12, background: '#f6f8fa', padding: 10, borderRadius: 4 }}>{res}</pre>}
    </Card>
  )
}


export default function ClusterPage() {
  const [seeding, setSeeding] = useState(false)
  const [reloadKey, setReloadKey] = useState(0)
  const seed = async () => {
    setSeeding(true)
    try {
      const r = await bootstrapDemo({ provisionLine: true })
      if (r.error) { message.error(`播种失败：${r.error}`); return }
      message.success(`已播种 mock 客户配置：${r.result?.customerGroup}`)
      setReloadKey((k) => k + 1)
    } catch (e) { message.error(String(e)) } finally { setSeeding(false) }
  }
  return (
    <div className="page-container">
      <PageHeader
        title="客户行为配置"
        status={{ tone: 'info', text: '被叫客户行为档' }}
        onReload={() => setReloadKey((k) => k + 1)}
        actions={<Button type="primary" loading={seeding} onClick={seed}>播种 mock 客户配置</Button>}
      />
      <InfoBanner title="mock 被叫客户的「行为档 / 客户组 / 端口绑定 / 个例」配置">
        行为档定义被叫如何应答（接听/拒接/振铃不接/放音/IVR/DTMF/故障）。客户组 = 一个号段批量 N 个虚拟客户。端口绑定 = mock SIP 监听端口对应到客户组。个例 = 组内个别号码的例外。
      </InfoBanner>
      <ResolvePreview />
      <Card size="small">
        <Tabs
          key={reloadKey}
          items={[
            { key: 'groups', label: '客户组', children: <GroupsTab /> },
            { key: 'overrides', label: '客户个例', children: <OverridesTab /> },
            { key: 'bindings', label: '端口绑定', children: <BindingsTab /> },
            { key: 'profiles', label: '行为档', children: <ProfilesTab /> },
          ]}
        />
      </Card>
    </div>
  )
}
