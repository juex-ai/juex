import { useEffect, useState, type FormEvent } from 'react'
import { useParams } from 'react-router-dom'
import { RefreshCw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { api, errorText } from './api'
import { Empty, Failure, Field, Loading, Notice, PageHeading } from './components'
import { useResource } from './use-resource'
import type { MemberView, TenantAccess, UsageCounts, UsageReport, User } from './schema'

const number = (value: number) => value.toLocaleString()
const token = (value: number, counts: UsageCounts) => counts.reported ? number(value) : '—'
const kindName: Record<string, string> = { main: 'Main', worker: 'Worker', compaction: '上下文压缩', memory: 'Memory', calendar: 'Calendar' }
function shiftDate(value: string, days: number) {
  if (!value) return ''
  const date = new Date(`${value}T00:00:00Z`)
  date.setUTCDate(date.getUTCDate() + days)
  return date.toISOString().slice(0, 10)
}

export function UsagePage({ tenant, user, delegated = false }: { tenant: TenantAccess; user: User; delegated?: boolean }) {
  const { ownerId } = useParams()
  const initialOwner = delegated ? ownerId! : tenant.role === 'admin' ? '' : user.id
  const [filters, setFilters] = useState(() => ({ from: '', end: '', group: 'day', owner: initialOwner }))
  const [applied, setApplied] = useState(filters)
  const [offset, setOffset] = useState(0)
  const [revision, setRevision] = useState(0)
  const [members, setMembers] = useState<MemberView[]>([])
  const [memberError, setMemberError] = useState('')
  useEffect(() => {
    if (tenant.role !== 'admin') return
    const controller = new AbortController()
    api<MemberView[]>(`/tenants/${tenant.id}/members`, undefined, undefined, controller.signal).then(setMembers).catch(error => { if (!controller.signal.aborted) setMemberError(errorText(error)) })
    return () => controller.abort()
  }, [tenant.id, tenant.role])
  const query = new URLSearchParams({ from: applied.from, until: shiftDate(applied.end, 1), group: applied.group, user_id: delegated ? ownerId! : applied.owner, offset: String(offset) })
  const resource = useResource<UsageReport>(`/tenants/${tenant.id}/usage?${query}`, revision)
  const refresh = () => setRevision(value => value + 1)
  const report = resource.data
  const from = filters.from || report?.query.from || ''
  const end = filters.end || shiftDate(report?.query.until || '', -1)
  function apply(event: FormEvent) { event.preventDefault(); if (from && end) { setApplied({ ...filters, from, end }); setOffset(0) } }
  const ownerName = (id: string) => id === user.id ? user.email : members.find(item => item.id === id)?.email ?? id
  return <>
    <PageHeading title="Token 用量" description="查看用户与模型的已报告消耗。数据按资源所有者归属，包含 Main、Worker、应用和上下文压缩。" actions={<Button variant="outline" onClick={refresh}><RefreshCw size={15} />刷新</Button>} />
    <form className="management-panel management-usage-filters" onSubmit={apply}>
      <Field label="开始日期"><Input type="date" required value={from} max={end} onChange={event => setFilters({ ...filters, from: event.target.value })} /></Field>
      <Field label="结束日期"><Input type="date" required value={end} min={from} onChange={event => setFilters({ ...filters, end: event.target.value })} /></Field>
      <Field label="汇总方式"><select value={filters.group} onChange={event => setFilters({ ...filters, group: event.target.value })}><option value="day">按天</option><option value="month">按月</option></select></Field>
      {tenant.role === 'admin' && !delegated && <Field label="用户"><select value={filters.owner} onChange={event => setFilters({ ...filters, owner: event.target.value })}><option value="">全部用户</option>{members.map(member => <option key={member.id} value={member.id}>{member.email}</option>)}</select></Field>}
      <Button type="submit">查询</Button>
    </form>
    {memberError && <Notice error>{memberError}</Notice>}
    {resource.error ? <Failure message={resource.error} retry={refresh} /> : !report ? <Loading /> : <>
      <section className="management-usage-totals" aria-label="用量汇总">
        <article className="management-panel"><span>已报告 Token 总量</span><strong>{token(report.totals.total_tokens, report.totals)}</strong><small>输入加输出，缓存不重复相加</small></article>
        <article className="management-panel"><span>输入 / 输出</span><strong>{token(report.totals.input_tokens, report.totals)} / {token(report.totals.output_tokens, report.totals)}</strong><small>其中缓存命中 {token(report.totals.cached_input_tokens, report.totals)}</small></article>
        <article className="management-panel"><span>模型调用</span><strong>{number(report.totals.attempts)}</strong><small>{number(report.totals.unknown)} 次用量未知 · {number(report.totals.partial)} 次仅部分报告</small></article>
      </section>
      {(report.totals.unknown > 0 || report.totals.partial > 0) && <Notice>部分调用未取得完整用量，当前已报告总量可能低于实际消耗。未知不代表零消耗。</Notice>}
      <section className="management-panel"><div className="management-panel-heading"><h2>用户与模型明细</h2><span>{applied.group === 'day' ? '按天' : '按月'} · {report.query.from} 至 {shiftDate(report.query.until, -1)}</span></div>
        {!report.rows.length ? <Empty title="此范围没有用量记录" /> : <div className="management-table-wrap"><table className="management-table"><thead><tr><th>日期 / 用户</th><th>模型 / 用途</th><th>输入</th><th>输出</th><th>缓存命中</th><th>合计</th><th>调用</th></tr></thead><tbody>{report.rows.map(row => <tr key={`${row.period_id}:${row.bucket}:${row.tenant_id}:${row.user_id}:${row.model_id}:${row.provider}:${row.model}:${row.kind}`}>
          <td>{applied.group === 'month' ? row.bucket.slice(0, 7) : row.bucket}<small>{ownerName(row.user_id)}</small><small>{report.periods.find(period => period.id === row.period_id)?.timezone}</small></td>
          <td>{row.provider} / {row.model}<small>{kindName[row.kind] ?? row.kind}</small></td><td>{token(row.input_tokens, row)}</td><td>{token(row.output_tokens, row)}</td><td>{token(row.cached_input_tokens, row)}</td><td>{token(row.total_tokens, row)}</td><td>{number(row.attempts)}<small>未知 {number(row.unknown)} · 部分 {number(row.partial)}</small></td>
        </tr>)}</tbody></table></div>}
        <div className="management-usage-pages"><Button variant="outline" disabled={!offset} onClick={() => setOffset(value => Math.max(0, value - 100))}>上一页</Button><span>第 {offset / 100 + 1} 页</span><Button variant="outline" disabled={!report.has_more} onClick={() => setOffset(value => value + 100)}>下一页</Button></div>
      </section>
      <details className="management-panel management-usage-policy"><summary>统计时区和口径</summary><p>日期按调用发生时的统计时区归集。时区调整后，已有统计保持原口径；月汇总由这些日统计计算。统计仅包含供应商已报告的用量，不作为供应商账单。</p>{report.periods.map(period => <p key={period.id}>{period.timezone} · {period.id === 1 ? '初始口径' : `自 ${new Date(period.effective_from).toLocaleString()}`} · {period.effective_until ? `截至 ${new Date(period.effective_until).toLocaleString()}` : '当前使用'}</p>)}<p>已结算调用明细保留 {report.detail_days} 天，日／月汇总长期保留。归档或清理 Agent 不会减少历史消耗。</p></details>
    </>}
  </>
}
