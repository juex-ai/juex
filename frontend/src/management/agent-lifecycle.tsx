import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { randomUUID } from '@/lib/uuid'
import { APIError, api, errorText } from './api'
import { Failure, Loading, Notice } from './components'
import { useResource } from './use-resource'
import type { AgentLifecycleChange, AgentLifecycleReceipt, AgentRunState } from './schema'

const responsibilities: Record<string,string> = { inputs:'未完成输入',turns:'进行中的对话',provider_attempts:'模型请求',tools:'工具操作',hooks:'Hooks',instructions:'指令读取',observers:'观察器',observation_output:'待确认的观察输出',worker_deliveries:'Worker 结果',observation_deliveries:'观察消息' }

export function AgentLifecycle({base,writable,changed}:{base:string;writable:boolean;changed:()=>void}) {
  const [revision,setRevision]=useState(0)
  const state=useResource<AgentRunState>(`${base}/run-state`,revision,10_000)
  const [interrupt,setInterrupt]=useState(false)
  const [busy,setBusy]=useState(false)
  const [pending,setPending]=useState<AgentLifecycleChange|null>(null)
  const [error,setError]=useState('')
  const [notice,setNotice]=useState('')
  const [receipt,setReceipt]=useState<AgentLifecycleReceipt|null>(null)
  const current=receipt&&(!state.data||receipt.state.version>state.data.version)?receipt.state:state.data
  async function send(change:AgentLifecycleChange){
    if(busy||!writable)return
    setBusy(true);setPending(change);setError('');setNotice('')
    try {
      const value=await api<AgentLifecycleReceipt>(`${base}/lifecycle`,change)
      setReceipt(value);setPending(null);setInterrupt(false)
      setNotice(value.outcome==='deferred'?'尚有未完成工作，本次操作未执行。空闲后请重新发起；系统不会自动执行本次请求。':value.action==='pause'?'已暂停模型调度与新工作接纳。原对话与任务保留，已接受的工具继续结算。':value.action==='resume'?'已恢复运行。保留的工作将按原身份继续。':'已重建 Agent 的运行租约。原对话、工具操作与配置快照保留。')
      setRevision(v=>v+1);changed()
    } catch(err){
      setError(errorText(err))
      if(err instanceof APIError&&[400,401,403,409].includes(err.status)){setPending(null);setRevision(v=>v+1)}
    } finally{setBusy(false)}
  }
  function start(action:string){if(!current||pending)return;void send({request_id:randomUUID(),action,version:current.version,interrupt:action==='resume'?false:interrupt})}
  return <section className="management-panel" aria-label="Agent 运行控制"><h2>运行控制</h2>
    {state.error?<Failure message={state.error} retry={()=>setRevision(v=>v+1)}/>:!current?<Loading/>:<><p><strong>{current.mode==='paused'?'已暂停':'运行已启用'}</strong> · {current.busy.length?current.busy.map(reason=>responsibilities[reason]??reason).join('、'):'没有未结算的工作'}</p><p>暂停模型调度和新输入，保留对话、任务与执行回执。此操作不会停止共享服务或执行环境；已接受的工具可以继续完成。</p></>}
    {notice&&<Notice>{notice}</Notice>}{error&&<Notice error>{error}</Notice>}
    {pending&&!busy&&<Notice>操作结果尚未确认。请使用同一请求重试以读取回执。<Button variant="outline" onClick={()=>void send(pending)} disabled={!writable}>重试同一操作</Button></Notice>}
    <div className="management-actions"><Button variant="outline" disabled={!writable||busy||!!pending||!current||!!state.error} onClick={()=>start(current?.mode==='paused'?'resume':'pause')}>{current?.mode==='paused'?'恢复运行':'暂停运行'}</Button><Button variant="outline" disabled={!writable||busy||!!pending||!current||!!state.error||current.mode==='paused'} onClick={()=>start('restart')}>重建运行租约</Button></div>
    {current?.mode==='running'&&<label className="management-check"><input type="checkbox" checked={interrupt} disabled={!writable||busy||!!pending} onChange={event=>setInterrupt(event.target.checked)}/>允许中断当前模型请求，保留未完成工作供恢复</label>}
    <p className="management-help">默认仅在空闲时执行。重建运行租约会让 Runtime 重新接管此 Agent；它不会重新执行已有工具，也不会重新加载进行中对话的配置。</p>
  </section>
}
