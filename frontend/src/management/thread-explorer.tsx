import { useState } from 'react'
import { Archive, GitBranch, RotateCcw, Trash2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { APIError, api, errorText } from './api'
import { Notice } from './components'
import { ThreadInspectionDialog } from './thread-inspection'
import type { Thread, ThreadDeletionReceipt } from './schema'

const states: Record<string, string> = { idle:'就绪', queued:'排队中', running:'处理中', waiting:'等待执行结果', failed:'本轮失败', blocked:'等待处理' }

export function threadDepth(item: Thread, threads: Thread[]): number {
  const seen = new Set<string>()
  let current = item
  while (current.parent_id) {
    if (seen.has(current.id)) return Infinity
    seen.add(current.id)
    const parent = threads.find(value => value.id === current.parent_id)
    if (!parent) return Infinity
    current = parent
  }
  return seen.size
}

type ThreadBatch = {threads: Pick<Thread,'id'|'name'>[]; archived: boolean; delete?: true}

function readBatch(key: string): ThreadBatch|null {
  try {
    const value=JSON.parse(sessionStorage.getItem(key)??'null')
    return value&&typeof value.archived==='boolean'&&(value.delete===undefined||value.delete===true)&&Array.isArray(value.threads)&&value.threads.every((item:unknown)=>item&&typeof item==='object'&&'id' in item&&typeof item.id==='string'&&'name' in item&&typeof item.name==='string')?value:null
  } catch { return null }
}

export function useThreadBatch(actor: string, base: string) {
  const key=`juex.thread-lifecycle:${actor}:${base}`
  const [state,setState]=useState<{key:string;value:ThreadBatch|null}>(()=>({key,value:readBatch(key)}))
  if(state.key!==key)setState({key,value:readBatch(key)})
  function set(value: ThreadBatch|null) {
    const clean=value?{archived:value.archived,...(value.delete?{delete:true as const}:{}),threads:value.threads.map(({id,name})=>({id,name}))}:null
    try { if(clean)sessionStorage.setItem(key,JSON.stringify(clean));else sessionStorage.removeItem(key) } catch { if(clean?.delete)throw new Error('无法保存原删除请求，请允许此页面使用会话存储后重试。') }
    setState({key,value:clean})
  }
  return [state.key===key?state.value:readBatch(key),set] as const
}

export function ThreadExplorer({ base, threads, current, writable, busy, setBusy, canCreate, create, select, refresh, unconfirmed, setUnconfirmed }: { unconfirmed: ThreadBatch|null; setUnconfirmed: (value: ThreadBatch|null)=>void; base: string; threads: Thread[]; current?: string; writable: boolean; busy: boolean; setBusy: (value:boolean) => void; canCreate: boolean; create: () => void; select: (thread:Thread, keepOpen?:boolean) => void; refresh: () => void }) {
  const [archived,setArchived] = useState(false)
  const [query,setQuery] = useState('')
  const [selected,setSelected] = useState(new Set<string>())
  const [inspection,setInspection] = useState<string|null>(null)
  const [notice,setNotice] = useState('')
  const [error,setError] = useState('')
  const [deleting,setDeleting] = useState(false)
  const visible = threads.filter(thread => (thread.kind === 'main' || thread.retention === (archived ? 'archived':'active')) && `${thread.name} ${thread.id}`.toLowerCase().includes(query.toLowerCase()))
  const eligible = visible.filter(thread => thread.kind === 'worker' && !thread.application)
  const chosen = eligible.filter(thread => selected.has(thread.id))
  async function apply() {
    if (busy || unconfirmed || !chosen.length) return
    setBusy(true); setError(''); setNotice('')
    setUnconfirmed({threads:chosen,archived:!archived})
    const failures: string[] = [], succeeded: string[] = [], uncertain: Thread[] = []
    // Archiving children first and restoring parents first satisfies Runtime's
    // graph policy. Each receipt is independent; partial success is explicit.
    const ordered = [...chosen].sort((a,b) => (threadDepth(a,threads)-threadDepth(b,threads)) * (archived ? 1 : -1))
    for (const thread of ordered) {
      try { await api(`${base}/threads/${thread.id}/archive`,{archived:!archived}); succeeded.push(thread.id) }
      catch (error) {
        if (!(error instanceof APIError) || error.status >= 500) uncertain.push(thread)
        else failures.push(`${thread.name}：${error.status === 409 ? archived ? '请先恢复父 Worker。' : '仍有未完成任务、操作、通知或子 Worker，请先处理。' : errorText(error)}`)
      }
    }
    if (uncertain.length) {
      try {
        const latest = await api<Thread[]>(`${base}/threads`)
        for (const thread of uncertain.splice(0)) {
          if (latest.some(value=>value.id===thread.id&&value.retention===(archived?'active':'archived'))) succeeded.push(thread.id)
          else failures.push(`${thread.name}：尚未确认目标状态，请刷新后重试。`)
        }
      } catch { /* Preserve exact targets until a subsequent authoritative read. */ }
    }
    setUnconfirmed(uncertain.length ? {threads:uncertain,archived:!archived} : null)
    setSelected(previous => new Set([...previous].filter(id => !succeeded.includes(id))))
    setNotice(`已${archived?'恢复':'归档'} ${succeeded.length} 个 Worker${failures.length ? `；${failures.length} 个未完成，仍保持选中。`:'。历史记录保留。'}${uncertain.length ? ` ${uncertain.length} 个结果待确认。`:''}`)
    setError(failures.join('\n')); setBusy(false); refresh()
  }
  async function deleteBatch(batch?: ThreadBatch) {
    if (busy || (!batch && unconfirmed)) return
    const targets=batch?.threads??[...chosen].sort((a,b)=>threadDepth(b,threads)-threadDepth(a,threads))
    if (!targets.length) return
    const original:ThreadBatch={threads:targets,archived:true,delete:true}
    try { setUnconfirmed(original) } catch(error) {setDeleting(false);setError(errorText(error));return}
    setDeleting(false);setBusy(true);setError('');setNotice('')
    const done:string[]=[],pending:Pick<Thread,'id'|'name'>[]=[],failures:string[]=[]
    for(const thread of targets) {
      try {
        const receipt=await api<ThreadDeletionReceipt>(`${base}/threads/${thread.id}`,undefined,'DELETE')
        if(!receipt.deleted||receipt.thread_id!==thread.id)throw new Error('删除回执与原对话不符。')
        done.push(thread.id)
      } catch(error) {
        if(batch||!(error instanceof APIError)||error.status>=500)pending.push(thread)
        else failures.push(`${thread.name}：${error.status===409?'请确认已归档、子 Worker 已删除，且原任务、用量和输出均已结算。':errorText(error)}`)
      }
    }
    try {setUnconfirmed(pending.length?{...original,threads:pending}:null)} catch { /* The original durable batch remains safe to retry by identity. */ }
    setSelected(previous=>new Set([...previous].filter(id=>!done.includes(id))))
    setNotice(`已永久删除 ${done.length} 个 Worker。${failures.length?` ${failures.length} 个未完成，仍保持选中。`:''}${pending.length?` ${pending.length} 个结果待确认，请重试原删除请求。`:''}`)
    setError(failures.join('\n'));setBusy(false)
    if(current&&done.includes(current)){const main=threads.find(thread=>thread.kind==='main');if(main)select(main,true)}
    refresh()
  }
  async function reconcile() {
    if (busy || !unconfirmed) return
    if(unconfirmed.delete){await deleteBatch(unconfirmed);return}
    setBusy(true); setError('')
    try {
      const latest = await api<Thread[]>(`${base}/threads`)
      const done = unconfirmed.threads.filter(thread=>latest.some(value=>value.id===thread.id&&value.retention===(unconfirmed.archived?'archived':'active')))
      setSelected(previous=>new Set([...previous].filter(id=>!done.some(thread=>thread.id===id))))
      setNotice(`已确认${unconfirmed.archived?'归档':'恢复'} ${done.length} 个 Worker；${unconfirmed.threads.length-done.length} 个尚未处于目标状态，可刷新后重新选择。`)
      setUnconfirmed(null); refresh()
    } catch (error) { setError(errorText(error)) } finally { setBusy(false) }
  }
  return <div className="management-thread-list management-thread-explorer">
    <div className="management-thread-heading"><strong>对话</strong><Button variant="ghost" size="icon" aria-label="创建 Worker" disabled={busy||!canCreate||!writable} onClick={create}><GitBranch size={16}/></Button></div>
    <Input aria-label="搜索 Thread 名称或标识" value={query} onChange={event=>setQuery(event.target.value)} placeholder="搜索名称或 Thread ID" />
    <label className="management-checkbox"><input type="checkbox" disabled={busy} checked={archived} onChange={event=>{setArchived(event.target.checked);setSelected(new Set());setError('');setNotice('')}}/>查看已归档 Workers</label>
    {writable && <div className="management-row-actions"><Button size="sm" variant="ghost" disabled={busy||!eligible.length} onClick={()=>setSelected(new Set(chosen.length===eligible.length?[]:eligible.map(thread=>thread.id)))}>{chosen.length&&chosen.length===eligible.length?'取消全选':'全选 Workers'}</Button><Button size="sm" variant="outline" disabled={busy||!!unconfirmed||!chosen.length} onClick={()=>void apply()}>{archived?<RotateCcw size={14}/>:<Archive size={14}/>} {busy?'处理中…':`${archived?'恢复':'归档'}所选 (${chosen.length})`}</Button></div>}
    {writable&&archived&&<Button size="sm" variant="destructive" disabled={busy||!!unconfirmed||!chosen.length} onClick={()=>setDeleting(true)}><Trash2 size={14}/>永久删除所选 ({chosen.length})</Button>}
    {notice&&<Notice>{notice}</Notice>}{error&&<Notice error>{error}</Notice>}
    {unconfirmed&&<Notice>{unconfirmed.threads.map(thread=>thread.name).join('、')} 的{unconfirmed.delete?'永久删除':unconfirmed.archived?'归档':'恢复'}请求尚未确认。<Button disabled={busy} variant="outline" onClick={()=>void reconcile()}>{unconfirmed.delete?'重试原删除请求':'检查未确认结果'}</Button></Notice>}
    <nav aria-label="Agent 对话">{visible.map(thread=><article key={thread.id} className="management-thread-entry">
      <div className="management-thread-select">{writable&&thread.kind==='worker'&&!thread.application&&<input type="checkbox" aria-label={`选择 ${thread.name}`} disabled={busy} checked={selected.has(thread.id)} onChange={event=>setSelected(previous=>{const next=new Set(previous);if(event.target.checked)next.add(thread.id);else next.delete(thread.id);return next})}/>}
        <button disabled={busy} className={thread.id===current?'active':''} onClick={()=>select(thread)}><span>{'　'.repeat(Math.min(threadDepth(thread,threads),3))}{thread.name}</span><small>{thread.application?`${thread.application} Worker`:thread.kind==='main'?'Main':'Worker'} · {thread.retention==='archived'?'已归档':states[thread.state]??thread.state}</small></button>
      </div>
      <div className="management-thread-metadata"><code>{thread.id}</code>{thread.parent_id&&<span>父对话：{threads.find(parent=>parent.id===thread.parent_id)?.name??thread.parent_id}</span>}{thread.updated_at&&<time dateTime={thread.updated_at}>最近活动 {new Date(thread.updated_at).toLocaleString()}</time>}<span>第 {thread.generation} 代 · 排队 {thread.pending_inputs} · 待处理 {thread.held_inputs}</span>{thread.application&&<span>由 {thread.application} 管理生命周期</span>}<Button variant="link" size="sm" onClick={()=>setInspection(thread.id)} aria-label={`查看 ${thread.name} 状态与用量`}>状态与用量</Button></div>
    </article>)}</nav>
    <Dialog open={deleting} onOpenChange={setDeleting}><DialogContent onOpenAutoFocus={event=>{event.preventDefault();document.getElementById('delete-worker-cancel')?.focus()}}><DialogHeader><DialogTitle>永久删除 {chosen.length} 个 Worker</DialogTitle><DialogDescription>此操作不可撤销。将删除所选对话的全部消息、未处理输入、上下文、Notes 和 Tasks。独立产物、工作目录文件、已共享 Memory 及用量账本保留。仅能删除已归档且已结算的普通 Worker，子对话先于父对话删除。</DialogDescription></DialogHeader><p>{chosen.map(thread=>thread.name).join('、')}</p><DialogFooter><Button id="delete-worker-cancel" variant="outline" onClick={()=>setDeleting(false)}>取消</Button><Button variant="destructive" disabled={busy||!chosen.length} onClick={()=>void deleteBatch()}>确认永久删除</Button></DialogFooter></DialogContent></Dialog>
    {inspection&&<ThreadInspectionDialog base={base} thread={inspection} initialTab="usage" close={()=>setInspection(null)}/>}
  </div>
}
