import { useState, type FormEvent } from 'react'
import { Link, useParams } from 'react-router-dom'
import { Settings2, Monitor, Puzzle, ArrowLeft } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { APIError, api, errorText } from './api'
import { Failure, Field, Loading, Notice, PageHeading } from './components'
import { useResource } from './use-resource'
import { ConfigurationEditor, configurationSource } from './configuration'
import { InstructionsEditor } from './instructions'
import { HooksEditor } from './hooks'
import { DefaultEnvironmentDialog } from './default-environment'
import { ExtensionsDialog } from './extensions'
import { WorkspaceConfigurationDialog } from './workspace-configuration'
import { ProcessEnvironmentDialog } from './process-environment'
import type { Agent, AgentConfig, AgentDetail, Model, TenantAccess, User } from './schema'

export function AgentSettingsDialog({ tenant, owner, agent, close, changed }: { tenant: string; owner: string; agent?: Agent; close: () => void; changed: () => void }) {
  return <Dialog open onOpenChange={open => { if (!open) close() }}><DialogContent className="sm:max-w-2xl max-h-[90dvh] overflow-y-auto"><DialogHeader><DialogTitle>{agent ? 'Agent 设置' : '创建 Agent'}</DialogTitle><DialogDescription>独立配置此 Agent 的能力、指令和模型。共享记忆和日程在 Fleet 中管理。</DialogDescription></DialogHeader><AgentConfigForm tenant={tenant} owner={owner} agent={agent} close={close} saved={() => { changed(); close() }} /></DialogContent></Dialog>
}

function agentConfig(agent?: Agent): AgentConfig {
  return { name: agent?.name ?? '', instructions: agent?.instructions ?? '', configuration: agent?.configuration ?? {}, worker_depth: agent?.worker_depth ?? 1, hooks: agent?.hooks ?? [], dynamic_instructions: agent?.dynamic_instructions ?? { enabled: false, global_path: '' } }
}

function AgentManagementGrant({ tenant, agent, writable, changed }: {tenant:string;agent:Agent;writable:boolean;changed:()=>void}) {
  const [busy,setBusy]=useState(false)
  const [error,setError]=useState('')
  async function toggle(){
    if(!writable||busy)return
    setBusy(true);setError('')
    try {await api(`/tenants/${tenant}/agents/${agent.id}/agent-management`,{version:agent.version,enabled:!agent.agent_management},'PUT')}
    catch(err){setError(errorText(err))}
    finally {setBusy(false);changed()}
  }
  return <section className="management-panel"><h2>管理其他 Agent</h2><p>允许此 Agent 的主对话创建、配置和暂停同一 Fleet 的其他 Agent。不能修改自身，也不能读取其他 Agent 的对话或密钥。</p><p>仅人工授予，默认关闭；不随模块预设或 Workspace 继承。启用后从新一轮对话生效，撤销后旧授权不能恢复。</p>{error&&<Notice error>{error}</Notice>}<Button variant="outline" disabled={!writable||busy} onClick={()=>void toggle()}>{busy?'正在更新…':agent.agent_management?'撤销 Agent 管理权限':'授予 Agent 管理权限'}</Button></section>
}

function AgentConfigForm({ tenant, owner, agent, writable = true, close, saved }: { tenant: string; owner: string; agent?: Agent; writable?: boolean; close?: () => void; saved: () => void }) {
  const [baseline, setBaseline] = useState(agent)
  const [config, setConfig] = useState<AgentConfig>(() => agentConfig(agent))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [revision, setRevision] = useState(0)
  const current = useResource<AgentDetail>(agent ? `/tenants/${tenant}/agents/${agent.id}` : null, revision + (agent?.version ?? 0))
  const catalog = useResource<Model[]>(`/tenants/${tenant}/models`, revision)
  const serverAgent = current.data && (!agent || current.data.agent.version >= agent.version) ? current.data.agent : agent
  const stale = !!serverAgent && !!baseline && serverAgent.version > baseline.version
  const sameFields = JSON.stringify(agentConfig(serverAgent)) === JSON.stringify(agentConfig(baseline))
  const dirty = JSON.stringify(config) !== JSON.stringify(agentConfig(baseline))
  // Extension changes share the Agent version but do not replace this draft.
  // Concurrent edits to these fields retain the old version so CAS still protects them.
  if (stale && (sameFields || !dirty)) {
    setBaseline(serverAgent)
    if (!dirty) setConfig(agentConfig(serverAgent))
  }
  async function save(event: FormEvent) {
    event.preventDefault(); if (!writable || busy) return
    setBusy(true); setError('')
    try {
      if (agent) { const updated = await api<Agent>(`/tenants/${tenant}/agents/${agent.id}`, { ...config, version: baseline?.version }, 'PUT'); setBaseline(updated); setConfig(agentConfig(updated)) }
      else await api(`/tenants/${tenant}/users/${owner}/agents`, config)
      setRevision(value=>value+1); saved()
    } catch (err) { setError(errorText(err)); if(err instanceof APIError&&err.status===409)setRevision(value=>value+1) } finally { setBusy(false) }
  }
  return <form className="management-form" onSubmit={save}>
    {stale && !sameFields && dirty && <Notice>服务器上的 Agent 设置已改变，你的编辑仍然保留。<Button type="button" variant="outline" onClick={() => { setConfig(agentConfig(serverAgent)); setBaseline(serverAgent) }}>丢弃本地编辑并加载服务器设置</Button></Notice>}
    {error && <Notice error>{error}</Notice>}{catalog.error && <Failure message={catalog.error} retry={() => setRevision(value => value + 1)} />}
    <fieldset disabled={!writable || busy} className="management-settings-fields">
      <Field label="名称"><Input autoComplete="off" required maxLength={100} value={config.name} onChange={event => setConfig({ ...config, name: event.target.value })} /></Field>
      <ConfigurationEditor value={config.configuration??{}} models={catalog.data??[]} effective={current.data?.effective} layers={current.data?.layers} onChange={configuration=>setConfig({...config,configuration})}/>
      <Field label="Worker 最大层级"><select className="management-select" value={config.worker_depth} onChange={event => setConfig({ ...config, worker_depth: Number(event.target.value) })}><option value={1}>1 层 · Main 可创建 Workers</option><option value={2}>2 层 · Worker 可继续委派一层</option></select></Field>
      <Field label="专属指令"><Textarea rows={5} maxLength={16000} value={config.instructions} onChange={event => setConfig({ ...config, instructions: event.target.value })} placeholder="这个 Agent 负责什么？有哪些需要遵守的要求？" /></Field>
      <InstructionsEditor value={config.dynamic_instructions!} onChange={dynamic_instructions => setConfig({ ...config, dynamic_instructions })} />
      <HooksEditor tenant={tenant} owner={owner} agent={agent?.id} value={config.hooks ?? []} onChange={hooks => setConfig({ ...config, hooks })} />
    </fieldset>
    <DialogFooter>{close && <Button type="button" variant="outline" onClick={close}>取消</Button>}<Button type="submit" disabled={!writable || busy}>{busy ? '正在保存…' : '保存'}</Button></DialogFooter>
  </form>
}

export function AgentSettingsPage({ tenant, user }: { tenant: TenantAccess; user: User }) {
  const { agentId } = useParams()
  const [revision, setRevision] = useState(0)
  const [panel, setPanel] = useState<'environment' | 'extensions' | 'workspace' | 'process-environment' | null>(null)
  const [notice, setNotice] = useState('')
  const refresh = () => setRevision(value => value + 1)
  const detail = useResource<AgentDetail>(`/tenants/${tenant.id}/agents/${agentId}`, revision)
  if (detail.error) return <Failure message={detail.error} retry={refresh} />
  if (!detail.data) return <Loading />
  const value = detail.data
  const fleet = `/t/${tenant.id}/${value.owner_id === user.id ? 'fleet' : `users/${value.owner_id}`}`
  return <>
    <PageHeading title={`${value.agent.name} · 配置`} description="设置当前 Agent 的模型、能力和指令；执行环境与扩展决定用户代码在哪里运行。" actions={<><Button variant="outline" asChild><Link to={`/t/${tenant.id}/agents/${agentId}/runtime`}>运行状态</Link></Button><Button variant="outline" asChild><Link to={`/t/${tenant.id}/agents/${agentId}`}><ArrowLeft />返回对话</Link></Button></>} />
    {value.owner_id !== user.id && <Notice>正在代管此 Agent · 操作者：{user.email}</Notice>}
    {!value.can_execute && <Notice>此 Agent 或所属成员已停用，配置仅供查看。</Notice>}
    {notice && <Notice>{notice}</Notice>}
    <AgentManagementGrant tenant={tenant.id} agent={value.agent} writable={value.can_execute} changed={refresh}/>
    <div className="management-settings-layout"><section className="management-panel management-settings-form"><h2><Settings2 size={17} />Agent 设置</h2><AgentConfigForm key={value.agent.id} tenant={tenant.id} owner={value.owner_id} agent={value.agent} writable={value.can_execute} saved={() => { setNotice('Agent 配置已保存。'); refresh() }} /></section>
<aside className="management-settings-context"><section className="management-panel"><h2><Monitor size={17} />执行环境</h2><p>文件、Shell、MCP 和 Observable 在授权的执行环境运行。已有操作保持原来的环境。</p><Button variant="outline" disabled={!value.can_execute} onClick={() => setPanel('environment')}>选择环境与工作目录</Button><Button variant="outline" disabled={!value.can_execute} onClick={() => setPanel('process-environment')}>配置执行环境变量</Button></section><section className="management-panel"><h2><Puzzle size={17} />扩展</h2><p>选择扩展所在环境与目录，检查后启用 Skills、MCP、Observable 和 Hook 资源。</p><Button variant="outline" disabled={!value.can_execute} onClick={() => setPanel('extensions')}>管理扩展资源</Button></section><section className="management-panel"><h2>配置优先级</h2><p>Agent → Workspace 快照 → Fleet → Tenant。高层覆盖低层；没有覆盖的模块逐项继承。</p><p>当前模型来源：{configurationSource(value.effective?.model_source)}。</p><Button variant="outline" disabled={!value.can_execute} onClick={() => setPanel('workspace')}>导入 Workspace 配置</Button><Button variant="link" asChild><Link to={fleet}>打开 Fleet 设置</Link></Button><Button variant="link" asChild><Link to={`/t/${tenant.id}/settings`}>查看 Tenant 默认配置</Link></Button></section></aside>
    </div>
    {panel === 'workspace' && <WorkspaceConfigurationDialog tenant={tenant.id} agent={value.agent} close={() => setPanel(null)} changed={refresh} />}
    {panel === 'process-environment' && <ProcessEnvironmentDialog tenant={tenant.id} agent={value.agent.id} close={() => setPanel(null)} />}
    {panel === 'environment' && <DefaultEnvironmentDialog tenant={tenant.id} agent={value.agent} close={() => setPanel(null)} changed={refresh} />}
    {panel === 'extensions' && <ExtensionsDialog tenant={tenant.id} agent={value.agent} close={() => setPanel(null)} changed={refresh} />}
  </>
}
