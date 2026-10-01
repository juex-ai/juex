import { useEffect, useState, type FormEvent } from 'react'
import { Link, useParams } from 'react-router-dom'
import { Laptop, Link2, ShieldX } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { api, errorText } from './api'
import { Empty, Failure, Field, Loading, Notice, PageHeading } from './components'
import { useResource } from './use-resource'
import type { Agent, Device, FleetOverview, Pairing, TenantAccess, User } from './schema'

type Grants = Record<string, string[]>
const capabilityName: Record<string, string> = { files: '文件访问', shell: '命令与进程', mcp: 'MCP 服务' }

function GrantFields({ agents, ceiling, value, onChange }: { agents: Agent[]; ceiling: Grants; value: Grants; onChange: (value: Grants) => void }) {
  function toggle(agent: string, capability: string, checked: boolean) {
    const next = { ...value, [agent]: checked ? [...(value[agent] ?? []), capability] : (value[agent] ?? []).filter(item => item !== capability) }
    if (!next[agent].length) delete next[agent]
    onChange(next)
  }
  return <div className="management-device-grants">{agents.filter(agent => ceiling[agent.id]?.length).map(agent => <fieldset key={agent.id}><legend>{agent.name}{agent.status !== 'active' ? ' · 已归档' : ''}</legend>{ceiling[agent.id].map(capability => <label className="management-checkbox" key={capability}><input type="checkbox" checked={value[agent.id]?.includes(capability) ?? false} onChange={event => toggle(agent.id, capability, event.target.checked)} />{capabilityName[capability] ?? capability}</label>)}</fieldset>)}</div>
}

export function DevicePairPage({ tenants, user }: { tenants: TenantAccess[]; user: User }) {
  const { pairId } = useParams()
  const [tenantId, setTenantId] = useState(tenants[0].id)
  return <main className="management-start management-pair-page"><PageHeading title="连接你的设备" description={`当前账号：${user.email}`} /><Field label="设备所属租户"><select value={tenantId} onChange={event => setTenantId(event.target.value)}>{tenants.map(tenant => <option key={tenant.id} value={tenant.id}>{tenant.name}</option>)}</select></Field><PairApproval key={`${tenantId}:${pairId}`} tenantId={tenantId} pairId={pairId!} /></main>
}

function PairApproval({ tenantId, pairId }: { tenantId: string; pairId: string }) {
  const [revision, setRevision] = useState(0)
  const pair = useResource<Pairing>(`/tenants/${tenantId}/device-pairings/${pairId}`, revision)
  const fleet = useResource<FleetOverview>(`/tenants/${tenantId}/fleet`, revision)
  const [grants, setGrants] = useState<Grants>({})
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  useEffect(() => { const timer = window.setInterval(() => setRevision(value => value + 1), 3000); return () => window.clearInterval(timer) }, [])
  async function approve(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError('')
    try { await api(`/tenants/${tenantId}/device-pairings/${pairId}`, { grants }); setRevision(value => value + 1) } catch (error) { setError(errorText(error)) } finally { setBusy(false) }
  }
  if (pair.error || fleet.error) return <Failure message={pair.error || fleet.error!} retry={() => setRevision(value => value + 1)} />
  if (!pair.data || !fleet.data) return <Loading />
  const value = pair.data
  const agents = fleet.data.agents.filter(agent => agent.status === 'active')
  const ceiling = Object.fromEntries(agents.map(agent => [agent.id, value.capabilities]))
  return <section className="management-panel management-pair-card"><Laptop size={28} /><h2>{value.name}</h2><p>{value.os === 'darwin' ? 'macOS' : 'Linux'} · {value.working_directory}</p><Notice>仅连接你认识的设备。授权后，所选 Agent 可以使用该设备当前系统用户拥有的权限；默认目录不限制文件访问范围。</Notice>{error && <Notice error>{error}</Notice>}
    {value.state === 'pending' ? agents.length ? <form onSubmit={approve}><h3>选择可使用此设备的 Agents</h3><GrantFields agents={agents} ceiling={ceiling} value={grants} onChange={setGrants} /><Button type="submit" disabled={busy || !Object.keys(grants).length}>{busy ? '正在提交…' : '确认授权，继续本机确认'}</Button></form> : <Empty title="先创建一个 Agent">创建后再打开此配对链接。</Empty> : <Notice>{value.state === 'confirmed' ? '设备配对完成。在本机运行 executor 后，设备会出现在 Fleet 中。' : 'Web 授权已完成。请返回发起配对的终端，核对租户、账号和能力，并输入 yes 完成本机确认。'}</Notice>}
    <Button variant="link" asChild><Link to={`/t/${tenantId}/fleet`}>返回我的 Fleet</Link></Button>
  </section>
}

export function DevicesPanel({ tenantId, fleet, user }: { tenantId: string; fleet: FleetOverview; user: User }) {
  const [revision, setRevision] = useState(0)
  const devices = useResource<Device[]>(`/tenants/${tenantId}/users/${fleet.owner.id}/devices`, revision)
  const [editor, setEditor] = useState<{ device: Device; grants: Grants } | null>(null)
  const [revoke, setRevoke] = useState<Device | null>(null)
  const [connect, setConnect] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const own = fleet.owner.id === user.id
  const refresh = () => setRevision(value => value + 1)
  useEffect(() => { const timer = window.setInterval(refresh, 5000); return () => window.clearInterval(timer) }, [])
  async function save(event: FormEvent) {
    event.preventDefault(); if (!editor) return
    setBusy(true); setError('')
    try { await api(`/tenants/${tenantId}/devices/${editor.device.id}/grants`, { version: editor.device.version, grants: editor.grants }, 'PUT'); setEditor(null); refresh() } catch (error) { setError(errorText(error)) } finally { setBusy(false) }
  }
  async function revokeDevice() {
    if (!revoke) return
    setBusy(true); setError('')
    try { await api(`/tenants/${tenantId}/devices/${revoke.id}/revoke`, {}); setRevoke(null); refresh() } catch (error) { setError(errorText(error)) } finally { setBusy(false) }
  }
  const command = `juex-executor --state "$HOME/.local/share/juex-executor/work" pair --server ${window.location.origin}${window.location.protocol === 'http:' ? ' --insecure-http' : ''}`
  return <section className="management-panel management-devices"><div className="management-panel-heading"><h2>附加执行设备</h2>{own && <Button variant="outline" disabled={fleet.membership.status !== 'active'} onClick={() => setConnect(true)}><Link2 />连接设备</Button>}</div>{devices.error ? <Failure message={devices.error} retry={refresh} /> : !devices.data ? <Loading /> : !devices.data.length ? <Empty title="尚未连接电脑">连接 Linux 或 macOS 电脑，让指定 Agents 使用本机文件、命令和 MCP 服务。</Empty> : <div className="management-agents">{devices.data.map(device => <article key={device.id} className="management-agent-row"><div className="management-agent-mark"><Laptop /></div><div className="management-agent-info"><h3>{device.name}</h3><p>{device.os === 'darwin' ? 'macOS' : 'Linux'} · {device.status === 'revoked' ? '已撤销' : device.status === 'journal_changed' ? '执行日志已变化，需要重新配对' : device.online ? '在线' : '离线'}</p><small>{Object.keys(device.grants).map(id => fleet.agents.find(agent => agent.id === id)?.name ?? '已删除 Agent').join('、') || '未授权 Agent'}</small></div>{device.status === 'active' && <div className="management-row-actions">{own && <Button variant="outline" disabled={fleet.membership.status !== 'active'} onClick={() => { setError(''); setEditor({ device, grants: device.grants }) }}>管理授权</Button>}<Button variant="ghost" onClick={() => { setError(''); setRevoke(device) }}><ShieldX />撤销</Button></div>}</article>)}</div>}
    <Dialog open={connect} onOpenChange={setConnect}><DialogContent><DialogHeader><DialogTitle>连接 Linux 或 macOS 电脑</DialogTitle><DialogDescription>在需要连接的电脑安装 juex-executor，然后在终端执行：</DialogDescription></DialogHeader><pre className="management-command">{command}</pre><p>打开 CLI 给出的链接，选择 Agent 和能力，再返回该终端确认。不同租户的配对使用不同的状态目录。</p><Button variant="outline" onClick={() => { void navigator.clipboard.writeText(command).catch(error => setError(errorText(error))) }}>复制命令</Button>{error && <Notice error>{error}</Notice>}</DialogContent></Dialog>
    <Dialog open={editor !== null} onOpenChange={open => { if (!open) setEditor(null) }}><DialogContent><DialogHeader><DialogTitle>{editor?.device.name} 的授权</DialogTitle><DialogDescription>可以调整本机确认过的能力。新增 Agent 或能力需要在本机发起新的配对。</DialogDescription></DialogHeader>{editor && <form onSubmit={save}><GrantFields agents={fleet.agents} ceiling={editor.device.ceiling} value={editor.grants} onChange={grants => setEditor({ ...editor, grants })} />{error && <Notice error>{error}</Notice>}<DialogFooter><Button variant="outline" type="button" onClick={() => setEditor(null)}>取消</Button><Button disabled={busy}>保存授权</Button></DialogFooter></form>}</DialogContent></Dialog>
    <Dialog open={revoke !== null} onOpenChange={open => { if (!open) setRevoke(null) }}><DialogContent><DialogHeader><DialogTitle>撤销 {revoke?.name}</DialogTitle><DialogDescription>立即停止下发新操作，并请求取消进行中的操作。设备离线时，取消请求会保留到再次连接；远程文件不会被删除。</DialogDescription></DialogHeader>{error && <Notice error>{error}</Notice>}<DialogFooter><Button variant="outline" onClick={() => setRevoke(null)}>保留连接</Button><Button disabled={busy} onClick={() => void revokeDevice()}>确认撤销</Button></DialogFooter></DialogContent></Dialog>
  </section>
}
