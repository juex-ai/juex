import { randomUUID } from '@/lib/uuid'
import { useCallback, useState, type FormEvent } from 'react'
import { Link, useParams } from 'react-router-dom'
import { Bot, Plus, Settings2, Archive, RotateCcw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { api, errorText } from './api'
import { AgentSettingsDialog } from './agent-settings'
import { Empty, Failure, Loading, Notice, PageHeading } from './components'
import { useResource } from './use-resource'
import { ConfigurationEditor, modelName } from './configuration'
import { DevicesPanel } from './devices'
import { DefaultEnvironmentDialog } from './default-environment'
import { ExtensionsDialog } from './extensions'
import { PurgeDialog, PurgePanel, type PurgeSelection } from './purge'
import type { Agent, FleetOverview, FleetSettings, Model, TenantAccess, User } from './schema'
export function FleetPage({ tenant, user, delegated = false }: { tenant: TenantAccess; user: User; delegated?: boolean }) {
  const { ownerId } = useParams()
  const owner = delegated ? ownerId! : user.id
  const [revision, setRevision] = useState(0)
  const refresh = useCallback(() => setRevision(value => value + 1), [])
  const resource = useResource<FleetOverview>(`/tenants/${tenant.id}/users/${owner}/fleet`, revision)
  const catalog = useResource<Model[]>(`/tenants/${tenant.id}/models`, revision)
  const [editor, setEditor] = useState<{ agent?: Agent } | null>(null)
  const [settings, setSettings] = useState<FleetSettings | null>(null)
  const [archive, setArchive] = useState<Agent | null>(null)
  const [environmentAgent, setEnvironmentAgent] = useState<Agent | null>(null)
  const [extensions, setExtensions] = useState<Agent | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [showArchived, setShowArchived] = useState(false)
  const [purge, setPurge] = useState<PurgeSelection | null>(null)
  const fleet = resource.data
  const writable = fleet?.membership.status === 'active' && !fleet.purged && !fleet.purging
  const models = catalog.data ?? []
  const fleetModels = fleet?.settings.configuration?.models ?? fleet?.tenant_settings?.declaration.models ?? []
  const modelsLabel = (ids: string[]) => ids.length ? ids.map(id=>modelName(models,id)).join(' → ') : '尚未选择模型'

  async function saveSettings(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError('')
    try { await api(`/tenants/${tenant.id}/users/${owner}/fleet/settings`, settings, 'PUT'); setSettings(null); refresh() } catch (err) { setError(errorText(err)) } finally { setBusy(false) }
  }
  async function archiveAgent(agent: Agent, archived: boolean) {
    setBusy(true); setError('')
    try { await api(`/tenants/${tenant.id}/agents/${agent.id}/archive`, { version: agent.version, archived }); setArchive(null); refresh() } catch (err) { setError(errorText(err)) } finally { setBusy(false) }
  }

  const visible = fleet?.agents.filter(agent => showArchived ? agent.status === 'archived' : agent.status === 'active') ?? []
  return <>
    <PageHeading title={delegated && fleet ? `${fleet.owner.email} 的 Fleet` : '我的 Fleet'} description="用 Agents 组织不同的工作，共享记忆和日程。" actions={<>{delegated && <Button variant="outline" asChild><Link to={`/t/${tenant.id}/users`}>返回用户列表</Link></Button>}<Button variant="outline" disabled={!writable} onClick={() => { setError(''); setSettings(fleet!.settings) }}><Settings2 />Fleet 设置</Button><Button disabled={!writable} onClick={() => { setError(''); setEditor({}) }}><Plus />创建 Agent</Button></>} />
    {delegated && fleet && <Notice>正在代管 {fleet.owner.email} 的 Fleet · 操作者：{user.email}</Notice>}
    {fleet && !fleet.purged && !fleet.purging && !writable && <Notice>此成员已{fleet.membership.status === 'removed' ? '移除' : '停用'}，保留数据可供查看，配置修改和执行已停止。</Notice>}
    {error && !editor && !settings && !archive && <Notice error>{error}</Notice>}
    {resource.error ? <Failure message={resource.error} retry={refresh} /> : !fleet ? <Loading /> : fleet.purged ? <Empty title="原 Fleet 已永久清理">成员重新接受邀请后，将创建全新的 Fleet。历史用量和清理记录仍可查看。</Empty> : <>
      <div className="management-fleet-summary"><div><span>模型优先级</span><strong>{modelsLabel(fleetModels)}</strong></div><div><span>共享知识</span><Button variant="link" asChild><Link to={`/t/${tenant.id}/${delegated ? `users/${owner}/memory` : 'memory'}`}>打开 Memory</Link></Button></div><div><span>共享日程</span><Button variant="link" asChild><Link to={`/t/${tenant.id}/${delegated ? `users/${owner}/calendar` : 'calendar'}`}>打开 Calendar</Link></Button></div></div>
      {catalog.error && <Failure message={catalog.error} retry={refresh} />}
      {catalog.data?.length === 0 && <Notice>部署方尚未提供可用模型。配置完成后，可以在 Fleet 或 Agent 设置中选择。</Notice>}
      <section className="management-panel"><div className="management-panel-heading"><div className="management-tabs" role="group" aria-label="Agent 状态筛选"><Button variant={!showArchived ? 'secondary' : 'ghost'} onClick={() => setShowArchived(false)}>Agents · {fleet.agents.filter(agent => agent.status === 'active').length}</Button><Button variant={showArchived ? 'secondary' : 'ghost'} onClick={() => setShowArchived(true)}>已归档</Button></div></div>
        {visible.length === 0 ? <Empty title={showArchived ? '暂无归档 Agent' : '创建你的第一个 Agent'}>{showArchived ? '归档后保留历史、配置与托管文件，可以随时恢复。' : '为一项工作命名，写下要求，然后开始对话。'}</Empty> : <div className="management-agents">{visible.map(agent => <article className="management-agent-row" key={agent.id}><div className="management-agent-mark"><Bot size={22} /></div><div className="management-agent-info"><h2>{agent.purging ? agent.name : <Link to={`/t/${tenant.id}/agents/${agent.id}`}>{agent.name}</Link>}</h2><p>{agent.instructions || '尚未填写专属指令'}</p><small>{modelsLabel(agent.configuration?.models??agent.workspace_configuration?.declaration.models??fleetModels)}</small></div><div className="management-row-actions">{!agent.purging && <Button asChild><Link to={`/t/${tenant.id}/agents/${agent.id}`}>{agent.status === 'active' && writable ? '打开对话' : '查看历史'}</Link></Button>}{agent.purging ? <span>永久清理中</span> : agent.status === 'active' ? <><Button variant="outline" disabled={!writable} onClick={() => { setError(''); setEditor({ agent }) }}>设置</Button><Button variant="outline" disabled={!writable} onClick={() => setEnvironmentAgent(agent)}>执行环境</Button><Button variant="outline" disabled={!writable} onClick={() => setExtensions(agent)}>扩展</Button><Button variant="ghost" disabled={!writable} onClick={() => { setError(''); setArchive(agent) }}><Archive />归档</Button></> : <><Button variant="outline" disabled={!writable || busy} onClick={() => void archiveAgent(agent, false)}><RotateCcw />恢复</Button><Button variant="destructive" disabled={busy || fleet.purging} onClick={() => setPurge({ id: agent.id, name: agent.name, version: agent.version, requestId: randomUUID() })}>永久删除</Button></>}</div></article>)}</div>}
      </section>
      <DevicesPanel tenantId={tenant.id} fleet={fleet} user={user} />
    </>}
    {fleet && !fleet.purged && fleet.membership.status === 'removed' && tenant.role === 'admin' && <section className="management-panel"><div className="management-panel-heading"><div><h2>永久清理此 Fleet</h2><p className="text-sm text-muted-foreground">删除全部 Agents、私有历史和共享应用数据。</p></div><Button variant="destructive" disabled={fleet.purging} onClick={() => setPurge({ id: '', name: fleet.owner.email, version: fleet.membership.version, requestId: randomUUID() })}>{fleet.purging ? '清理中' : '永久清理 Fleet'}</Button></div></section>}
    <PurgePanel key={`${tenant.id}:${owner}`} tenantId={tenant.id} owner={owner} revision={revision} refresh={refresh} />
    <PurgeDialog key={purge?.requestId ?? 'closed'} tenantId={tenant.id} owner={owner} selection={purge} close={() => setPurge(null)} changed={refresh} />
    {environmentAgent && <DefaultEnvironmentDialog tenant={tenant.id} agent={environmentAgent} close={() => setEnvironmentAgent(null)} changed={refresh} />}
    {extensions && <ExtensionsDialog tenant={tenant.id} agent={extensions} close={() => setExtensions(null)} changed={refresh} />}
    {editor && <AgentSettingsDialog tenant={tenant.id} owner={owner} agent={editor.agent} close={() => setEditor(null)} changed={refresh} />}
    <Dialog open={settings !== null} onOpenChange={open => { if (!open) setSettings(null) }}><DialogContent className="sm:max-w-2xl max-h-[90dvh] overflow-y-auto"><DialogHeader><DialogTitle>Fleet 设置</DialogTitle><DialogDescription>此 Fleet 的 Agents 继承这里的模型顺序和模块开关，Agent 与 Workspace 可覆盖。未设置时继续继承 Tenant。Memory 和 Calendar 在各自应用中管理。</DialogDescription></DialogHeader>{settings && <form onSubmit={saveSettings}>{error && <Notice error>{error}</Notice>}<ConfigurationEditor value={settings.configuration??{}} models={models} onChange={configuration=>setSettings({...settings,configuration})}/><DialogFooter><Button type="button" variant="outline" onClick={() => setSettings(null)}>取消</Button><Button type="submit" disabled={busy}>{busy ? '正在保存…' : '保存设置'}</Button></DialogFooter></form>}</DialogContent></Dialog>
    <Dialog open={archive !== null} onOpenChange={open => { if (!open) setArchive(null) }}><DialogContent onOpenAutoFocus={event => { event.preventDefault(); document.getElementById('archive-agent-cancel')?.focus() }}><DialogHeader><DialogTitle>归档 {archive?.name}</DialogTitle><DialogDescription>停止新执行，并请求停止进行中的工作；历史和托管文件保留。关联日程将暂停，恢复 Agent 后需要显式恢复日程。</DialogDescription></DialogHeader>{error && <Notice error>{error}</Notice>}<DialogFooter><Button id="archive-agent-cancel" variant="outline" onClick={() => setArchive(null)}>取消</Button><Button disabled={busy} onClick={() => archive && void archiveAgent(archive, true)}>确认归档</Button></DialogFooter></DialogContent></Dialog>
  </>
}
