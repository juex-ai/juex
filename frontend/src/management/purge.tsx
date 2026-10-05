import { useEffect, useState, type FormEvent } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { api, errorText } from './api'
import { Empty, Failure, Field, Loading, Notice } from './components'
import { useResource } from './use-resource'
import type { PurgeJob, PurgeRequest } from './schema'

export type PurgeSelection = { id: string; name: string; version: number; requestId: string }

export function PurgeDialog({ tenantId, owner, selection, close, changed }: { tenantId: string; owner: string; selection: PurgeSelection | null; close: () => void; changed: () => void }) {
  const [confirmation, setConfirmation] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  async function submit(event: FormEvent) {
    event.preventDefault()
    if (!selection || confirmation !== selection.name) return
    setBusy(true); setError('')
    const request: PurgeRequest = { id: selection.requestId, agent_id: selection.id, version: selection.version }
    try { await api(`/tenants/${tenantId}/users/${owner}/purges`, request); close(); changed() } catch (err) { setError(errorText(err)) } finally { setBusy(false) }
  }
  return <Dialog open={selection !== null} onOpenChange={open => { if (!open && !busy) close() }}><DialogContent onOpenAutoFocus={event => { event.preventDefault(); document.getElementById('purge-cancel')?.focus() }}><DialogHeader><DialogTitle>永久删除 {selection?.name}</DialogTitle><DialogDescription>{selection?.id ? '将删除此 Agent 的配置、私有对话和托管文件。Fleet 共享 Memory、Calendar 和远程设备文件保留。' : '将删除此 Fleet 的全部 Agents、对话、Memory、Calendar 和托管文件。全局账号和其他租户的数据保留。'}此操作无法撤销。</DialogDescription></DialogHeader><form className="management-form" onSubmit={submit}>
    <p className="text-sm text-muted-foreground">正在执行的操作会请求取消；离线设备可能暂时无法确认停止。历史用量保留，历史备份按部署方的保留周期过期。</p>
    {error && <Notice error>{error}</Notice>}
    <Field label={`输入“${selection?.name ?? ''}”确认`}><Input autoComplete="off" value={confirmation} onChange={event => setConfirmation(event.target.value)} /></Field>
    <DialogFooter><Button id="purge-cancel" type="button" variant="outline" disabled={busy} onClick={close}>取消</Button><Button type="submit" variant="destructive" disabled={busy || !selection || confirmation !== selection.name}>{busy ? '正在提交…' : '永久删除'}</Button></DialogFooter>
  </form></DialogContent></Dialog>
}

const serviceLabels: Record<string, string> = { runtime: '对话与运行记录', memory: 'Memory', calendar: 'Calendar', execution: '执行环境与文件' }

const stageLabels = ['停止新执行', '停止 Memory 工作', '停止 Calendar 工作', '取消环境中的操作', '清理托管文件', '清理 Memory 数据', '清理 Calendar 数据', '清理私有对话', '完成资源清理']

export function PurgePanel({ tenantId, owner, revision, refresh }: { tenantId: string; owner: string; revision: number; refresh: () => void }) {
  const [cursor, setCursor] = useState('')
  const resource = useResource<PurgeJob[]>(`/tenants/${tenantId}/users/${owner}/purges?after=${cursor}`, revision)
  const pending = resource.data?.some(job => job.state !== 'completed' || job.receipts.execution?.unconfirmed > 0)
  useEffect(() => {
    if (!pending) return
    const timer = window.setInterval(refresh, 5000)
    return () => window.clearInterval(timer)
  }, [pending, refresh])
  return <section className="management-panel"><div className="management-panel-heading"><h2>永久清理记录</h2><Button variant="ghost" size="sm" onClick={refresh}>刷新清理状态</Button></div>
    {resource.error ? <Failure message={resource.error} retry={refresh} /> : !resource.data ? <Loading /> : resource.data.length === 0 ? <Empty title="暂无清理记录">归档 Agent 和已移除成员的 Fleet 可以单独永久删除。</Empty> : <div className="management-purge-list">{resource.data.map(job => <article key={job.id} className="management-purge-row"><div><strong>{job.whole_fleet ? 'Fleet 整体清理' : 'Agent 永久删除'}</strong><p>{job.state === 'completed' ? '平台数据已清理' : job.state === 'failed' ? '清理暂未完成，正在自动重试' : stageLabels[job.step] ?? '清理中'}</p><small>{new Date(job.created_at).toLocaleString()} · {job.whole_fleet ? job.fleet_id : job.agent_ids[0]}</small></div><div className="management-purge-status">{job.receipts.execution ? <span>{job.receipts.execution.unconfirmed > 0 ? `${job.receipts.execution.unconfirmed} 个远端操作停止未确认` : '远端无待核验操作'}</span> : <span>等待执行状态核验</span>}{job.receipts.execution?.environments_pending > 0 && <small>托管文件清理中</small>}<details><summary>查看服务进度</summary>{Object.entries(job.receipts).map(([service, receipt]) => <p key={service}>{serviceLabels[service] ?? service}: {receipt.data_removed ? '清理完成' : receipt.fenced ? '已停止接纳新工作' : '待确认'}</p>)}<small>任务 {job.id}</small></details></div></article>)}</div>}
    {(cursor || resource.data?.length === 100) && <div className="management-panel-heading"><Button variant="outline" onClick={() => setCursor('')} disabled={!cursor}>返回首批</Button><Button variant="outline" disabled={resource.data?.length !== 100} onClick={() => setCursor(resource.data![99].id)}>下一批记录</Button></div>}
  </section>
}
