import { useState, type FormEvent } from 'react'
import { Link, useParams } from 'react-router-dom'
import { BookOpen, RefreshCw, Search, Settings2, Trash2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { APIError, api, errorText } from './api'
import { Empty, Failure, Field, Loading, Notice, PageHeading } from './components'
import { useResource } from './use-resource'
import { correctMemoryEntry } from './memory-correction'
import type { FleetOverview, MemoryAdminRequest, MemoryConfiguration, MemoryEntry, MemoryPage as EntryPage, MemoryReviewPage, MemoryStatus, MemoryStorageRules, TenantAccess, User } from './schema'

const states: Record<string, string> = { pending: '等待审核', applied: '已记住', no_change: '无需变更', rejected: '未采纳', failed: '审核未完成' }
const entryName = (entry: MemoryEntry) => entry.name === 'Structured knowledge' ? '结构化知识' : entry.name
const factStates: Record<string, string> = { valid: '有效断言', superseded: '已被后续事实取代', corrected: '已更正', retracted: '已撤回', disputed: '存在分歧' }

export function MemoryPage({ tenant, user, delegated = false }: { tenant: TenantAccess; user: User; delegated?: boolean }) {
  const { ownerId } = useParams()
  const owner = delegated ? ownerId! : user.id
  const base = `/tenants/${tenant.id}/users/${owner}/memory`
  const [revision, setRevision] = useState(0)
  const refresh = () => setRevision(value => value + 1)
  const fleet = useResource<FleetOverview>(`/tenants/${tenant.id}/users/${owner}/fleet`, revision)
  const status = useResource<MemoryStatus>(base, revision)
  const [tab, setTab] = useState<'knowledge' | 'reviews' | 'rules'>('knowledge')
  const [search, setSearch] = useState('')
  const [query, setQuery] = useState('')
  const [offset, setOffset] = useState(0)
  const [entry, setEntry] = useState<MemoryEntry | null>(null)
  const [originalEntry, setOriginalEntry] = useState<MemoryEntry | null>(null)
  const [editing, setEditing] = useState(false)
  const [remove, setRemove] = useState(false)
  const [settings, setSettings] = useState<MemoryConfiguration | null>(null)
  const [pending, setPending] = useState<MemoryAdminRequest | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const writable = fleet.data?.membership.status === 'active' && status.data?.enabled === true
  const canConfigure = fleet.data?.membership.status === 'active'
  const agentName = (id: string) => fleet.data?.agents.find(agent => agent.id === id)?.name ?? '已移除的 Agent'
  const list = useResource<EntryPage | MemoryReviewPage | MemoryStorageRules>(`${base}/${tab === 'knowledge' ? `entries?text=${encodeURIComponent(query)}&` : tab === 'reviews' ? 'reviews?' : 'storage-rules?'}offset=${offset}&limit=20`, revision)
  const entries = tab === 'knowledge' ? list.data as EntryPage | undefined : undefined
  const reviews = tab === 'reviews' ? list.data as MemoryReviewPage | undefined : undefined
  const rules = tab === 'rules' ? list.data as MemoryStorageRules | undefined : undefined
  const threadLink = (agent: string, thread: string) => `/t/${tenant.id}/agents/${agent}?thread=${encodeURIComponent(thread)}`

  async function read(id: string) {
    setBusy(true); setError('')
    try { setEntry(await api<MemoryEntry>(`${base}/entries/${encodeURIComponent(id)}?view=history`)); setEditing(false); setRemove(false) }
    catch (err) { setError(errorText(err)) } finally { setBusy(false) }
  }
  async function administer(request: MemoryAdminRequest) {
    setPending(request); setBusy(true); setError('')
    try { await api(`${base}/administer`, request); setPending(null); setEntry(null); setRemove(false); setOffset(0); refresh() }
    catch (err) { setError(errorText(err)); if (err instanceof APIError && err.status >= 400 && err.status < 500) setPending(null) }
    finally { setBusy(false) }
  }
  async function editEntry() {
    if (!entry) return
    setBusy(true); setError('')
    try { const stored = await api<MemoryEntry>(`${base}/entries/${encodeURIComponent(entry.id)}?view=stored`); setEntry(stored); setOriginalEntry(stored); setEditing(true) }
    catch (err) { setError(errorText(err)) } finally { setBusy(false) }
  }
  async function saveSettings(event: FormEvent) {
    event.preventDefault(); if (!settings) return
    setBusy(true); setError('')
    try { await api(base, settings, 'PUT'); setSettings(null); refresh() }
    catch (err) { setError(errorText(err)) } finally { setBusy(false) }
  }
  function searchEntries(event: FormEvent) { event.preventDefault(); setQuery(search); setOffset(0) }
  function changeTab(next: typeof tab) { setTab(next); setOffset(0) }

  return <>
    <PageHeading title={delegated ? `${fleet.data?.owner.email ?? ''} 的 Memory` : 'Memory'} description="同一 Fleet 的 Agents 共享持久知识，原始对话仍各自独立。" actions={<><Button variant="outline" onClick={refresh}><RefreshCw />刷新</Button><Button variant="outline" disabled={!canConfigure || !status.data} onClick={() => { setError(''); setSettings({ version: status.data!.version, enabled: status.data!.enabled, strategy: status.data!.strategy }) }}><Settings2 />应用设置</Button></>} />
    {delegated && <Notice>正在代管 {fleet.data?.owner.email} 的知识 · 操作者：{user.email}</Notice>}
    {status.error ? <Failure message={status.error} retry={refresh} /> : !status.data ? <Loading /> : <>
      <div className="management-fleet-summary"><div><span>应用状态</span><strong>{status.data.enabled ? '已启用' : '已停用 · 只读'}</strong></div><div><span>共享知识</span><strong>{status.data.entries} 条</strong></div><div><span>待审核</span><strong>{status.data.pending} 项</strong></div></div>
      {!status.data.enabled && <Notice>保留的知识仍可查看。Agent 查询、写入和后台审核已停止；重新启用后不会恢复旧审核。</Notice>}
      {fleet.data && !canConfigure && <Notice>成员已停用或移除，当前只能查看保留的知识。</Notice>}
      <section className="management-panel"><div className="management-panel-heading"><div className="management-tabs" role="group" aria-label="Memory 内容"><Button variant={tab === 'knowledge' ? 'secondary' : 'ghost'} onClick={() => changeTab('knowledge')}>知识</Button><Button variant={tab === 'reviews' ? 'secondary' : 'ghost'} onClick={() => changeTab('reviews')}>审核记录</Button><Button variant={tab === 'rules' ? 'secondary' : 'ghost'} onClick={() => changeTab('rules')}>禁止重新学习</Button></div></div>
        {tab === 'knowledge' && <form className="management-memory-search" onSubmit={searchEntries}><Input aria-label="搜索知识" placeholder="搜索偏好、项目和参考知识" value={search} onChange={event => setSearch(event.target.value)} /><Button type="submit" variant="outline"><Search />搜索</Button></form>}
        {list.error ? <Failure message={list.error} retry={refresh} /> : !list.data ? <Loading /> : <>
          {entries && (entries.entries.length ? <div className="management-agents">{entries.entries.map(value => <article className="management-agent-row" key={value.id}><div className="management-agent-mark"><BookOpen size={22} /></div><div className="management-agent-info"><h2>{entryName(value)}</h2><p>{value.summary}</p><small>{value.scope.project || value.scope.workspace || 'Fleet 知识'}</small></div><Button variant="outline" disabled={busy} onClick={() => void read(value.id)}>查看</Button></article>)}</div> : <Empty title="暂无匹配的知识">在对话中明确告诉 Agent 需要记住的内容，审核完成后会出现在这里。</Empty>)}
          {reviews && (reviews.reviews.length ? <div className="management-agents">{reviews.reviews.map(value => {
            const human = !value.agent_id && !value.thread_id
            const label = human && value.state === 'applied' ? '管理操作已完成' : human && value.state === 'failed' ? '管理操作未完成' : states[value.state] ?? value.state
            return <article className="management-agent-row" key={value.id}><div className="management-agent-info"><h2>{label}</h2><p>{value.reason}</p><small>{human ? '人类管理' : agentName(value.agent_id)} · {new Date(value.updated_at).toLocaleString()}</small></div><div className="management-row-actions">{value.agent_id && value.thread_id && <Button variant="ghost" asChild><Link to={threadLink(value.agent_id, value.thread_id)}>来源对话</Link></Button>}{value.agent_id && value.worker_id && <Button variant="outline" asChild><Link to={threadLink(value.agent_id, value.worker_id)}>查看审核</Link></Button>}</div></article>
          })}</div> : <Empty title="暂无审核记录">提交成功表示已经进入审核，只有“已记住”表示知识完成变更。</Empty>)}
          {rules && <><Notice>遗忘后，相关来源不会再次被自动写入知识。明确允许重新学习后，仍需发起新的记忆请求。</Notice>{rules.entries.length + rules.sources.length === 0 ? <Empty title="暂无限制">在知识详情中遗忘条目时，会保留禁止重新学习的约束。</Empty> : <div className="management-memory-rules">{rules.entries.map(id => <div key={id}><span>已遗忘条目 · {id}</span></div>)}{rules.sources.map((source, index) => <div key={`${source.agent_id}:${source.thread_id}:${source.from}:${index}`}><Link to={threadLink(source.agent_id, source.thread_id)}>{agentName(source.agent_id)} 的来源对话</Link></div>)}<Button variant="outline" disabled={!writable || busy || pending !== null} onClick={() => void administer({ key: crypto.randomUUID(), action: 'allow_store', entry_ids: rules.entries, sources: rules.sources })}>允许本页来源重新学习</Button></div>}</>}
          <div className="management-memory-pagination"><Button variant="ghost" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - 20))}>上一页</Button><span>第 {Math.floor(offset / 20) + 1} 页</span><Button variant="ghost" disabled={list.data.next <= 0} onClick={() => setOffset(list.data!.next)}>下一页</Button></div>
        </>}
      </section>
    </>}
    {error && <Notice error>{error}{pending && <Button variant="link" disabled={busy} onClick={() => void administer(pending)}>恢复这次请求的结果</Button>}</Notice>}
    <Dialog open={settings !== null} onOpenChange={open => { if (!open && !busy) setSettings(null) }}><DialogContent><DialogHeader><DialogTitle>Memory 设置</DialogTitle><DialogDescription>停用会停止 Agent 访问和旧审核，保留知识供查看。</DialogDescription></DialogHeader>{settings && <form className="management-form" onSubmit={saveSettings}><label className="management-checkbox"><input type="checkbox" checked={settings.enabled} onChange={event => setSettings({ ...settings, enabled: event.target.checked })} />启用 Memory</label><Field label="知识维护策略"><select value={settings.strategy} onChange={event => setSettings({ ...settings, strategy: event.target.value })}><option value="basic">Basic · 明确要求才记住</option><option value="advanced">Advanced · 空闲时维护长期知识</option></select></Field>{error && <Notice error>{error}</Notice>}<DialogFooter><Button type="button" variant="outline" disabled={busy} onClick={() => setSettings(null)}>取消</Button><Button disabled={busy}>保存</Button></DialogFooter></form>}</DialogContent></Dialog>
    <Dialog open={entry !== null} onOpenChange={open => { if (!open && !busy && !pending) setEntry(null) }}><DialogContent className="management-memory-dialog"><DialogHeader><DialogTitle>{remove ? '遗忘这条知识' : entry ? entryName(entry) : ''}</DialogTitle><DialogDescription>{remove ? '删除共享知识、清理 Memory 中的相关证据，并阻止它从同一来源再次被学到。原始对话保留。' : '来源说明知识从哪里来，适用范围说明什么时候使用。'}</DialogDescription></DialogHeader>{entry && <>
      {!remove && <div className="management-form management-memory-detail">{editing ? <><Field label="名称"><Input disabled={busy || pending !== null} value={entry.name} onChange={event => setEntry({ ...entry, name: event.target.value })} /></Field><Field label="摘要"><Textarea disabled={busy || pending !== null} value={entry.summary} onChange={event => setEntry({ ...entry, summary: event.target.value })} /></Field><Field label="内容"><Textarea rows={8} disabled={busy || pending !== null} value={entry.body} onChange={event => setEntry({ ...entry, body: event.target.value })} /></Field></> : <><p>{entry.facts?.length ? `这条知识包含 ${entry.facts.length} 项事实，保留历史状态和来源。` : entry.summary}</p>{!entry.facts?.length && <div className="management-memory-body">{entry.body}</div>}</>}
        {(entry.facts ?? []).map((fact, index) => <div className="management-memory-fact" key={fact.id}><strong>{entry.entities?.find(entity => entity.id === fact.subject)?.name ?? fact.subject} · {fact.predicate}</strong>{editing && !fact.object && (fact.status === 'valid' || fact.status === 'disputed') ? <Input aria-label={`${fact.predicate} 的值`} disabled={busy || pending !== null} value={fact.value ?? ''} onChange={event => setEntry({ ...entry, facts: entry.facts!.map((value, i) => i === index ? { ...value, value: event.target.value } : value) })} /> : <p>{fact.value || entry.entities?.find(entity => entity.id === fact.object)?.name || fact.object}</p>}<small>{fact.domain} · {factStates[fact.status] ?? fact.status}{fact.valid_from && ` · 自 ${new Date(fact.valid_from).toLocaleDateString()}`}{fact.valid_until && ` · 至 ${new Date(fact.valid_until).toLocaleDateString()}`}{fact.time_note && ` · ${fact.time_note}`}</small></div>)}
        <div className="management-memory-sources"><strong>来源</strong>{entry.sources.map((source, index) => <Link key={index} to={threadLink(source.agent_id, source.thread_id)}>{agentName(source.agent_id)} 的对话</Link>)}</div>
      </div>}
      {error && <Notice error>{error}{pending && <Button variant="link" disabled={busy} onClick={() => void administer(pending)}>恢复这次请求的结果</Button>}</Notice>}
      <DialogFooter><Button variant="outline" disabled={busy || pending !== null} onClick={() => remove ? setRemove(false) : setEntry(null)}>{remove ? '取消' : '关闭'}</Button>{remove ? <Button variant="destructive" disabled={!writable || busy || pending !== null} onClick={() => void administer({ key: crypto.randomUUID(), action: 'delete', entry_ids: [entry.id] })}>确认遗忘</Button> : editing ? <Button disabled={!writable || busy || pending !== null} onClick={() => void administer({ key: crypto.randomUUID(), action: 'correct', changes: [{ entry: correctMemoryEntry(originalEntry!, entry, new Date().toISOString(), () => crypto.randomUUID()), expected_revision: entry.revision }] })}>提交更正</Button> : <><Button variant="ghost" disabled={!writable || busy} onClick={() => setRemove(true)}><Trash2 />遗忘</Button><Button disabled={!writable || busy} onClick={() => void editEntry()}>更正知识</Button></>}</DialogFooter>
    </>}</DialogContent></Dialog>
  </>
}
