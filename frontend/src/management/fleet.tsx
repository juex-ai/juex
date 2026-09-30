import { useState, type FormEvent } from 'react'
import { Link, useParams } from 'react-router-dom'
import { Bot, Plus, Settings2, Archive, RotateCcw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { api, errorText } from './api'
import { Empty, Failure, Field, Loading, Notice, PageHeading } from './components'
import { useResource } from './use-resource'
import type { Agent, AgentConfig, FleetOverview, FleetSettings, Model, TenantAccess, User } from './schema'

export function FleetPage({ tenant, user, delegated = false }: { tenant: TenantAccess; user: User; delegated?: boolean }) {
  const { ownerId } = useParams()
  const owner = delegated ? ownerId! : user.id
  const [revision, setRevision] = useState(0)
  const refresh = () => setRevision(value => value + 1)
  const resource = useResource<FleetOverview>(`/tenants/${tenant.id}/users/${owner}/fleet`, revision)
  const catalog = useResource<Model[]>(`/tenants/${tenant.id}/models`, revision)
  const [editor, setEditor] = useState<{ agent?: Agent; config: AgentConfig } | null>(null)
  const [settings, setSettings] = useState<FleetSettings | null>(null)
  const [archive, setArchive] = useState<Agent | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [showArchived, setShowArchived] = useState(false)
  const fleet = resource.data
  const writable = fleet?.membership.status === 'active'
  const models = catalog.data ?? []
  const modelLabel = (id: string) => { const model = models.find(model => model.id === id); return model ? `${model.provider} / ${model.name}` : id ? '模型已不可用' : '尚未选择模型' }

  async function saveAgent(event: FormEvent) {
    event.preventDefault()
    if (!editor) return
    setBusy(true); setError('')
    try {
      if (editor.agent) await api(`/tenants/${tenant.id}/agents/${editor.agent.id}`, { ...editor.config, version: editor.agent.version }, 'PUT')
      else await api(`/tenants/${tenant.id}/users/${owner}/agents`, editor.config)
      setEditor(null); refresh()
    } catch (err) { setError(errorText(err)) } finally { setBusy(false) }
  }
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
    <PageHeading title={delegated && fleet ? `${fleet.owner.email} 的 Fleet` : '我的 Fleet'} description="用 Agents 组织不同的工作，共享记忆和日程。" actions={<>{delegated && <Button variant="outline" asChild><Link to={`/t/${tenant.id}/users`}>返回用户列表</Link></Button>}<Button variant="outline" disabled={!writable} onClick={() => { setError(''); setSettings(fleet!.settings) }}><Settings2 />Fleet 设置</Button><Button disabled={!writable} onClick={() => { setError(''); setEditor({ config: { name: '', instructions: '', model_id: '' } }) }}><Plus />创建 Agent</Button></>} />
    {delegated && fleet && <Notice>正在代管 {fleet.owner.email} 的 Fleet · 操作者：{user.email}</Notice>}
    {fleet && !writable && <Notice>此成员已{fleet.membership.status === 'removed' ? '移除' : '停用'}，保留数据可供查看，配置修改和执行已停止。</Notice>}
    {error && !editor && !settings && !archive && <Notice error>{error}</Notice>}
    {resource.error ? <Failure message={resource.error} retry={refresh} /> : !fleet ? <Loading /> : <>
      <div className="management-fleet-summary"><div><span>默认模型</span><strong>{modelLabel(fleet.settings.default_model_id)}</strong></div><div><span>Memory</span><strong>{fleet.settings.memory_enabled ? '已启用' : '只读'}</strong></div><div><span>Calendar</span><strong>{fleet.settings.calendar_enabled ? '已启用' : '只读'}</strong></div></div>
      {catalog.error && <Failure message={catalog.error} retry={refresh} />}
      {catalog.data?.length === 0 && <Notice>部署方尚未提供可用模型。配置完成后，可以在 Fleet 或 Agent 设置中选择。</Notice>}
      <section className="management-panel"><div className="management-panel-heading"><div className="management-tabs" role="group" aria-label="Agent 状态筛选"><Button variant={!showArchived ? 'secondary' : 'ghost'} onClick={() => setShowArchived(false)}>Agents · {fleet.agents.filter(agent => agent.status === 'active').length}</Button><Button variant={showArchived ? 'secondary' : 'ghost'} onClick={() => setShowArchived(true)}>已归档</Button></div></div>
        {visible.length === 0 ? <Empty title={showArchived ? '暂无归档 Agent' : '创建你的第一个 Agent'}>{showArchived ? '归档后保留历史、配置与托管文件，可以随时恢复。' : '为一项工作命名，写下要求，然后开始对话。'}</Empty> : <div className="management-agents">{visible.map(agent => <article className="management-agent-row" key={agent.id}><div className="management-agent-mark"><Bot size={22} /></div><div className="management-agent-info"><h2><Link to={`/t/${tenant.id}/agents/${agent.id}`}>{agent.name}</Link></h2><p>{agent.instructions || '尚未填写专属指令'}</p><small>{agent.model_id ? modelLabel(agent.model_id) : `继承 Fleet · ${modelLabel(fleet.settings.default_model_id)}`}</small></div><div className="management-row-actions"><Button asChild><Link to={`/t/${tenant.id}/agents/${agent.id}`}>{agent.status === 'active' && writable ? '打开对话' : '查看历史'}</Link></Button>{agent.status === 'active' ? <><Button variant="outline" disabled={!writable} onClick={() => { setError(''); setEditor({ agent, config: { name: agent.name, instructions: agent.instructions, model_id: agent.model_id } }) }}>设置</Button><Button variant="ghost" disabled={!writable} onClick={() => { setError(''); setArchive(agent) }}><Archive />归档</Button></> : <Button variant="outline" disabled={!writable || busy} onClick={() => void archiveAgent(agent, false)}><RotateCcw />恢复</Button>}</div></article>)}</div>}
      </section>
    </>}
    <Dialog open={editor !== null} onOpenChange={open => { if (!open) setEditor(null) }}><DialogContent className="sm:max-w-lg"><DialogHeader><DialogTitle>{editor?.agent ? 'Agent 设置' : '创建 Agent'}</DialogTitle><DialogDescription>每个 Agent 拥有独立的对话和托管工作目录，同一 Fleet 共享 Memory 和 Calendar。</DialogDescription></DialogHeader>{editor && <form className="management-form" onSubmit={saveAgent}>{error && <Notice error>{error}</Notice>}<Field label="名称"><Input autoComplete="off" required maxLength={100} value={editor.config.name} onChange={event => setEditor({ ...editor, config: { ...editor.config, name: event.target.value } })} /></Field><Field label="专属指令"><Textarea rows={5} maxLength={16000} value={editor.config.instructions} onChange={event => setEditor({ ...editor, config: { ...editor.config, instructions: event.target.value } })} placeholder="这个 Agent 负责什么？有哪些需要遵守的要求？" /></Field><Field label="模型"><ModelSelect models={models} value={editor.config.model_id} emptyLabel="继承 Fleet 默认模型" onChange={value => setEditor({ ...editor, config: { ...editor.config, model_id: value } })} /></Field><DialogFooter><Button type="button" variant="outline" onClick={() => setEditor(null)}>取消</Button><Button type="submit" disabled={busy}>{busy ? '正在保存…' : '保存'}</Button></DialogFooter></form>}</DialogContent></Dialog>
    <Dialog open={settings !== null} onOpenChange={open => { if (!open) setSettings(null) }}><DialogContent><DialogHeader><DialogTitle>Fleet 设置</DialogTitle><DialogDescription>模型选择在新 Turn 生效；停用应用会保留数据，并停止新的应用工作。</DialogDescription></DialogHeader>{settings && <form onSubmit={saveSettings}>{error && <Notice error>{error}</Notice>}<Field label="默认模型"><ModelSelect models={models} value={settings.default_model_id} emptyLabel="暂不选择" onChange={value => setSettings({ ...settings, default_model_id: value })} /></Field><label className="management-checkbox"><input type="checkbox" checked={settings.memory_enabled} onChange={event => setSettings({ ...settings, memory_enabled: event.target.checked })} />启用 Fleet Memory</label><label className="management-checkbox"><input type="checkbox" checked={settings.calendar_enabled} onChange={event => setSettings({ ...settings, calendar_enabled: event.target.checked })} />启用 Fleet Calendar</label><DialogFooter><Button type="button" variant="outline" onClick={() => setSettings(null)}>取消</Button><Button type="submit" disabled={busy}>{busy ? '正在保存…' : '保存设置'}</Button></DialogFooter></form>}</DialogContent></Dialog>
    <Dialog open={archive !== null} onOpenChange={open => { if (!open) setArchive(null) }}><DialogContent onOpenAutoFocus={event => { event.preventDefault(); document.getElementById('archive-agent-cancel')?.focus() }}><DialogHeader><DialogTitle>归档 {archive?.name}</DialogTitle><DialogDescription>停止新执行，并请求停止进行中的工作；历史和托管文件保留。关联日程将暂停，恢复 Agent 后需要显式恢复日程。</DialogDescription></DialogHeader>{error && <Notice error>{error}</Notice>}<DialogFooter><Button id="archive-agent-cancel" variant="outline" onClick={() => setArchive(null)}>取消</Button><Button disabled={busy} onClick={() => archive && void archiveAgent(archive, true)}>确认归档</Button></DialogFooter></DialogContent></Dialog>
  </>
}

function ModelSelect({ models, value, onChange, emptyLabel }: { models: Model[]; value: string; onChange: (value: string) => void; emptyLabel: string }) {
  return <select value={value} onChange={event => onChange(event.target.value)}><option value="">{emptyLabel}</option>{value && !models.some(model => model.id === value) && <option value={value} disabled>当前模型已不可用</option>}{models.map(model => <option key={model.id} value={model.id}>{model.provider} / {model.name}</option>)}</select>
}
