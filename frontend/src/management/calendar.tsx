import { useEffect, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { CalendarDays, Plus, RefreshCw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { APIError, api, errorText } from './api'
import { Empty, Failure, Loading, Notice, PageHeading } from './components'
import { CalendarEditor } from './calendar-editor'
import { useResource } from './use-resource'
import type { CalendarChange, CalendarDefinition, CalendarOccurrencePage, CalendarRule, CalendarSchedule, CalendarSchedulePage, CalendarStatus, FleetOverview, TenantAccess, User } from './schema'

const states: Record<string, string> = { active: '已启用', paused: '已暂停', archived: '已归档', completed: '已完成', pending: '等待派发', accepted: 'Main 已接收', skipped: '恢复时已跳过', queued: '排队中', cancelled: '已取消', cancel_requested: '正在确认取消', held: '需要处理', needs_attention: '需要处理', outcome_unknown: '结果未知', missed: '已错过' }
const reasons: Record<string, string> = { manual: '手动暂停', target_unavailable: '执行目标或授权已变化，需重新确认并恢复', no_future_occurrence: '没有可执行的未来日期' }
const formatDate = (value?: string) => !value || value.startsWith('0001-') ? '—' : new Date(value).toLocaleString()
function ruleText(rule: CalendarRule) {
  if (rule.frequency === 'once') return formatDate(rule.at)
  if (rule.frequency === 'interval') return `每 ${Math.round((rule.every_seconds ?? 0) / 60)} 分钟`
  const period = rule.frequency === 'daily' ? '每天 / 每周' : rule.frequency === 'monthly' ? '每月' : `每年 ${rule.months?.join('、')} 月`
  return `${rule.lunar ? '农历 · ' : ''}${period}${rule.days ? ` · ${rule.days.join('、')} 日` : ''} · ${rule.times?.join('、')} · ${rule.timezone}`
}

export function CalendarPage({ tenant, user, delegated = false }: { tenant: TenantAccess; user: User; delegated?: boolean }) {
  const { ownerId } = useParams()
  const owner = delegated ? ownerId! : user.id
  const base = `/tenants/${tenant.id}/users/${owner}/calendar`
  const [revision, setRevision] = useState(0)
  const refresh = () => setRevision(value => value + 1)
  useEffect(() => { const timer = window.setInterval(refresh, 10_000); return () => window.clearInterval(timer) }, [])
  const fleet = useResource<FleetOverview>(`/tenants/${tenant.id}/users/${owner}/fleet`, revision)
  const status = useResource<CalendarStatus>(base, revision)
  const [tab, setTab] = useState<'schedules' | 'occurrences'>('schedules')
  const [offset, setOffset] = useState(0)
  const [filter, setFilter] = useState('')
  const list = useResource<CalendarSchedulePage | CalendarOccurrencePage>(`${base}/${tab}?offset=${offset}&limit=20&schedule_id=${filter}`, revision)
  const schedules = tab === 'schedules' ? list.data as CalendarSchedulePage | undefined : undefined
  const occurrences = tab === 'occurrences' ? list.data as CalendarOccurrencePage | undefined : undefined
  const [editor, setEditor] = useState<{ item: CalendarSchedule | null } | null>(null)
  const [pending, setPending] = useState<CalendarChange | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const canConfigure = fleet.data?.membership.status === 'active'
  const writable = canConfigure && status.data?.enabled && !busy && !pending
  const agentName = (id?: string) => fleet.data?.agents.find(a => a.id === id)?.name ?? '不可用的 Agent'
  async function change(request: CalendarChange) {
    setPending(request); setBusy(true); setError('')
    try { await api(`${base}/changes`, request); setPending(null); setEditor(null); refresh() }
    catch (err) { setError(errorText(err)); if (err instanceof APIError && err.status >= 400 && err.status < 500 && err.status !== 408) { setPending(null); refresh() } } finally { setBusy(false) }
  }
  function action(item: CalendarSchedule, action: string, occurrenceId?: string) {
    void change({ request_id: crypto.randomUUID(), id: item.id, version: item.version, action, occurrence_id: occurrenceId })
  }
  function save(definition: CalendarDefinition) {
    void change({ request_id: crypto.randomUUID(), id: editor?.item?.id ?? crypto.randomUUID(), version: editor?.item?.version ?? 0, action: 'save', definition })
  }
  async function configure() {
    if (!status.data) return
    setBusy(true); setError('')
    try { await api(base, { version: status.data.version, enabled: !status.data.enabled }, 'PUT'); refresh() }
    catch (err) { setError(errorText(err)); refresh() } finally { setBusy(false) }
  }
  async function cancelOccurrence(id: string, scheduleID: string) {
    setBusy(true); setError('')
    try {
      let offset = 0
      while (true) {
        const page = await api<CalendarSchedulePage>(`${base}/schedules?offset=${offset}&limit=50`)
        const item = page.schedules.find(s => s.id === scheduleID)
        if (item) { await change({ request_id: crypto.randomUUID(), id: item.id, version: item.version, action: 'cancel_occurrence', occurrence_id: id }); return }
        if (!page.next) throw new Error('日程已变化，请刷新后重试。')
        offset = page.next
      }
    } catch (err) { setError(errorText(err)) } finally { setBusy(false) }
  }
  function showHistory(id = '') { setFilter(id); setTab('occurrences'); setOffset(0) }
  return <>
    {delegated && <Notice>正在管理 {fleet.data?.owner.email ?? '用户'} 的 Calendar。操作者：{user.email}。<Link to={`/t/${tenant.id}/users/${owner}`}>返回 Fleet</Link></Notice>}
    <PageHeading title="Calendar" description="Fleet 的共享日程。发送提醒、新建 Worker，或唤醒 Agent 的 Main 对话。" actions={<><Button variant="outline" onClick={refresh}><RefreshCw />刷新</Button><Button disabled={!writable} onClick={() => { setError(''); setEditor({ item: null }) }}><Plus />创建日程</Button></>} />
    {error && <Notice error>{error}</Notice>}
    {pending && !busy && <Notice>上次请求的结果尚未确认。<Button variant="outline" onClick={() => void change(pending)}>重试原请求</Button><Button variant="ghost" onClick={() => { setPending(null); setEditor(null); refresh() }}>重新加载核对</Button></Notice>}
    {fleet.error && <Failure message={fleet.error} retry={refresh} />}
    {status.error ? <Failure message={status.error} retry={refresh} /> : !status.data ? <Loading /> : <section className="management-panel management-calendar-control"><div><h2><CalendarDays size={18} />{status.data.enabled ? '日历已启用' : '日历已停用'}</h2><p>{status.data.enabled ? `${status.data.schedules} 条日程 · ${status.data.pending} 次执行处理中` : '定义和历史保留。重新启用只安排未来日程，不补跑停用期间的任务。'}</p></div><Button variant="outline" disabled={!canConfigure || busy || !!pending} onClick={() => void configure()}>{status.data.enabled ? '停用应用' : '启用应用'}</Button></section>}
    <div className="management-memory-tabs" role="tablist" aria-label="日历视图"><Button role="tab" aria-selected={tab === 'schedules'} variant={tab === 'schedules' ? 'default' : 'ghost'} onClick={() => { setTab('schedules'); setOffset(0); setFilter('') }}>日程</Button><Button role="tab" aria-selected={tab === 'occurrences'} variant={tab === 'occurrences' ? 'default' : 'ghost'} onClick={() => showHistory()}>执行记录</Button></div>
    <section className="management-panel" aria-label={tab === 'schedules' ? '日程列表' : '执行记录'}>
      {filter && <div className="management-panel-heading"><span>当前日程的执行记录</span><Button variant="link" onClick={() => showHistory()}>查看全部</Button></div>}
      {list.error ? <Failure message={list.error} retry={refresh} /> : !list.data ? <Loading /> : <>
        {schedules && (schedules.schedules.length === 0 ? <Empty title="还没有日程">创建提醒，或为 Agent 安排定时任务。</Empty> : <div className="management-agents">{schedules.schedules.map(item => <article className="management-agent-row management-calendar-row" key={item.id}><div className="management-agent-info"><h2>{item.name}<span className="management-status">{states[item.status] ?? item.status}</span></h2><p>{ruleText(item.rule)}</p><p>{item.mode === 'reminder' ? '提醒我' : `${item.mode === 'main' ? 'Main' : 'Worker'}：${agentName(item.agent_id)}`} · 下次：{formatDate(item.next_at)} · 恢复后{item.catch_up === 'none' ? '不补跑' : '补最近一次'}</p>{item.pause_reason && <small>{reasons[item.pause_reason] ?? item.pause_reason}</small>}<details><summary>日程内容</summary><p className="management-calendar-content">{item.content}</p></details></div><div className="management-row-actions"><Button variant="ghost" onClick={() => showHistory(item.id)}>执行记录</Button>{item.status !== 'archived' && <Button variant="outline" disabled={!writable} onClick={() => { setError(''); setEditor({ item }) }}>编辑</Button>}{item.status === 'active' && <Button variant="outline" disabled={!writable} onClick={() => action(item, 'pause')}>暂停</Button>}{item.status === 'paused' && <Button disabled={!writable} onClick={() => action(item, 'resume')}>恢复日程</Button>}<Button variant="ghost" disabled={!writable} onClick={() => action(item, item.status === 'archived' ? 'restore' : 'archive')}>{item.status === 'archived' ? '恢复定义' : '归档'}</Button></div></article>)}</div>)}
        {occurrences && (occurrences.occurrences.length === 0 ? <Empty title="暂无执行记录">到达安排时间后，执行状态和原操作会保存在这里。</Empty> : <div className="management-agents">{occurrences.occurrences.map(item => <article className="management-agent-row management-calendar-row" key={item.id}><div className="management-agent-info"><h2>{item.name}<span className="management-status">{states[item.state] ?? item.state}</span></h2><p>计划时间：{formatDate(item.scheduled_at)}</p><p>{item.mode === 'reminder' ? '提醒通知' : `${item.mode === 'main' ? 'Main' : 'Worker'}：${agentName(item.agent_id)}`}</p>{item.state === 'accepted' && <small>已送达 Main，执行结果请查看对话。日程取消不会撤回已接收的输入。</small>}{item.state === 'outcome_unknown' && <Notice error>请核对原操作的结果。此执行不会自动重跑。</Notice>}{item.external_pending && item.state === 'completed' && <small>Worker 已结束，后台操作仍在运行或等待确认。</small>}{item.cancel_requested && <small>{item.mode === 'main' ? '正在确认是否已送达 Main；已接收的输入不会撤回。' : '正在等待执行端确认，尚不能确认外部操作已停止。'}</small>}{!!item.operations?.length && <details><summary>原操作</summary>{item.operations.map(id => <code key={id}>{id}</code>)}</details>}</div><div className="management-row-actions">{item.main_thread_id && <Button variant="outline" asChild><Link to={`/t/${tenant.id}/agents/${item.agent_id}?thread=${item.main_thread_id}`}>查看 Main</Link></Button>}{item.worker_id && <Button variant="outline" asChild><Link to={`/t/${tenant.id}/agents/${item.agent_id}?thread=${item.worker_id}`}>查看 Worker</Link></Button>}{!item.cancel_requested && (item.external_pending || ['pending', 'queued', 'active', 'outcome_unknown'].includes(item.state)) && <Button variant="ghost" disabled={!writable} onClick={() => void cancelOccurrence(item.id, item.schedule_id)}>取消本次执行</Button>}</div></article>)}</div>)}
        <div className="management-memory-pagination"><Button variant="ghost" disabled={offset === 0} onClick={() => setOffset(value => Math.max(0, value - 20))}>上一页</Button><span>第 {Math.floor(offset / 20) + 1} 页</span><Button variant="ghost" disabled={!list.data.next} onClick={() => setOffset(list.data!.next)}>下一页</Button></div>
      </>}
    </section>
    <Dialog open={editor !== null} onOpenChange={open => { if (!open && !busy) setEditor(null) }}><DialogContent className="management-calendar-dialog"><DialogHeader><DialogTitle>{editor?.item ? '编辑日程' : '创建日程'}</DialogTitle><DialogDescription>修改只影响未来执行。暂停后恢复不会补跑错过的日期。</DialogDescription></DialogHeader>{error && <Notice error>{error}</Notice>}{pending && !busy && <Button variant="outline" onClick={() => void change(pending)}>重试原请求</Button>}{editor && <CalendarEditor key={editor.item?.id ?? 'new'} item={editor.item} agents={fleet.data?.agents ?? []} busy={busy || !!pending} submit={save} close={() => setEditor(null)} />}</DialogContent></Dialog>
  </>
}
