import {useState} from 'react'
import {nanoid} from 'nanoid'
import {Button} from '@/components/ui/button'
import {APIError,api,errorText} from './api'
import {Empty,Failure,Loading,Notice} from './components'
import {useResource} from './use-resource'
import type {MCPPage,MCPRefresh,MCPStatus,MCPToolList,OperationOutput} from './schema'

const names:Record<string,string>={waiting:'等待环境',dispatched:'等待接收',accepted:'已接收',connecting:'正在握手',running:'已连接',unconfirmed:'连接待确认',completed:'已结束',failed:'连接失败',cancelled:'已断开',unknown:'结果未知'}
const terminal=(state:string)=>['completed','failed','cancelled','unknown'].includes(state)

export function MCPConnections({base,actor}:{base:string;actor:string}) {
  const [after,setAfter]=useState('')
  const [revision,setRevision]=useState(0)
  const page=useResource<MCPPage>(`${base}/mcp-connections?after=${encodeURIComponent(after)}`,revision,5000)
  const refresh=()=>setRevision(value=>value+1)
  return <section className="management-panel management-runtime-resources" aria-label="MCP 实际连接"><h2>MCP 实际连接</h2><p>下面是已发起的连接。状态每 5 秒只读刷新；“刷新工具列表”才会向 MCP 服务发起请求，操作在连接所属的执行环境运行。</p>
    {page.error?<Failure message={page.error} retry={refresh}/>:!page.data?<Loading/>:<>
      {!page.data.items.length?<Empty title="尚无连接记录">启用 MCP 配置后，在对话中连接所需资源；启用配置本身不会启动连接。</Empty>:page.data.items.map(connection=><Connection key={`${actor}:${base}:${connection.environment_id}:${connection.id}`} base={base} actor={actor} value={connection} refresh={refresh}/>)}
      <small>读取于 {new Date(page.data.observed_at).toLocaleString()}。历史握手只说明曾连接成功；离线、撤权或停止中的连接会标为待确认。</small>
      <div className="management-row-actions">{after&&<Button variant="outline" onClick={()=>setAfter('')}>最新连接</Button>}{page.data.next&&<Button variant="outline" onClick={()=>setAfter(page.data!.next)}>更早连接</Button>}</div>
    </>}
  </section>
}

function readPending(key:string):MCPRefresh|null {
  try {const value=JSON.parse(sessionStorage.getItem(key)??'null');return value&&typeof value.id==='string'&&typeof value.cursor==='string'?value:null}catch{return null}
}

function Connection({base,actor,value,refresh}:{base:string;actor:string;value:MCPStatus;refresh:()=>void}) {
  const key=`juex.mcp-refresh:${actor}:${base}:${value.environment_id}:${value.id}`
  const [pending,setPending]=useState<MCPRefresh|null>(()=>readPending(key))
  const [busy,setBusy]=useState(false)
  const [error,setError]=useState('')
  const [notice,setNotice]=useState('')
  const [checked,setChecked]=useState<string|null>(null)
  const list=value.latest_tools
  const confirmed=!!pending&&(checked===pending.id||list?.id===pending.id&&terminal(list.state))
  const unresolved=pending&&!confirmed?pending:null
  function retain(change:MCPRefresh|null) {
    // A lost POST response must retain the exact identity across reloads.
    if(change)sessionStorage.setItem(key,JSON.stringify(change));else sessionStorage.removeItem(key)
    setPending(change)
  }
  async function submit(cursor='',retry=false) {
    if(busy||!value.can_refresh||unresolved&&!retry)return
    const change=retry?unresolved!:{id:nanoid(),cursor}
    setBusy(true);setError('');setNotice('')
    try {
      retain(change)
      await api<MCPToolList>(`${base}/environments/${encodeURIComponent(value.environment_id)}/mcp-connections/${encodeURIComponent(value.id)}/tools`,change)
      setNotice('刷新请求已接收。正在读取原请求结果。');refresh()
    }catch(error){
      setError(errorText(error))
      // A rejected retry says nothing about whether the first request was accepted.
      if(!retry&&error instanceof APIError&&error.status<500){try{retain(null)}catch{/* Keep the original identity if local storage fails. */}}
    }finally{setBusy(false)}
  }
  async function check() {
    if(!unresolved||busy)return
    setBusy(true);setError('')
    try{
      const output=await api<OperationOutput>(`${base}/environments/${encodeURIComponent(value.environment_id)}/operations/${encodeURIComponent(unresolved.id)}/output?after=0`)
      setNotice(terminal(output.state)?`原刷新请求：${names[output.state]??output.state}${output.output_expired?'，输出已过保留期':''}`:'原刷新请求仍在执行，请等待。')
      if(terminal(output.state)){setChecked(unresolved.id);retain(null)}
      refresh()
    }catch(error){setError(errorText(error))}finally{setBusy(false)}
  }
  return <article className="management-runtime-environment" aria-label={`MCP ${value.handshake?.server_name||value.id}`}>
    <h3>{value.handshake?.server_name||'MCP 连接'}<span>{names[value.state]??value.state}</span></h3>
    <p>{value.environment} · {value.handshake?.transport||'握手未完成'}{value.handshake&&` · 服务版本 ${value.handshake.server_version||'未提供'} · 协议 ${value.handshake.protocol_version}`}</p>
    {value.directory&&<p>运行目录 <code>{value.directory}</code></p>}
    {value.state==='unconfirmed'&&<Notice>无法确认连接当前可用。历史执行状态：{names[value.observed_state]??value.observed_state}，请检查原环境。</Notice>}
    {value.handshake&&<small>连接建立于 {new Date(value.handshake.connected_at).toLocaleString()}</small>}
    {error&&<Notice error>{error}</Notice>}{notice&&<Notice>{confirmed?'原刷新请求已完成。':notice}</Notice>}
    {unresolved&&<Notice>保留了一次尚未确认的刷新请求。<Button variant="outline" disabled={busy} onClick={()=>void check()}>检查原刷新结果</Button><Button variant="outline" disabled={busy||!value.can_refresh} onClick={()=>void submit('',true)}>重试原刷新请求</Button></Notice>}
    <div className="management-row-actions"><Button variant="outline" disabled={busy||!!unresolved||!value.can_refresh} onClick={()=>void submit()}>刷新工具列表</Button>{list?.next_cursor&&<Button variant="outline" disabled={busy||!!unresolved||!value.can_refresh} onClick={()=>void submit(list.next_cursor)}>读取下一页工具</Button>}</div>
    {!list?<p>尚未读取此连接的工具列表。</p>:<section aria-label="最近工具列表"><p>最近请求：{new Date(list.requested_at).toLocaleString()} · {list.state==='completed'?'已读取':list.state==='running'?'读取中':names[list.state]??list.state}{list.cursor?' · 后续页':''}</p>
      {list.output_expired?<Notice>工具列表已过保留期。刷新需要连接当前可用。</Notice>:list.incomplete?<Notice error>结果超出显示上限或格式不完整，无法展示完整工具列表。</Notice>:list.state==='completed'?list.tools.length?list.tools.map(tool=><details key={tool.name}><summary>{tool.name}{tool.description&&` · ${tool.description}`}</summary><pre>{JSON.stringify(tool.input_schema,null,2)}</pre></details>):<p>服务返回的这一页没有工具。</p>:<p>尚无已完成的工具列表。</p>}
    </section>}
    <details><summary>连接标识</summary><p>环境 <code>{value.environment_id}</code></p><p>连接 <code>{value.id}</code></p>{value.binding_id&&<p>扩展绑定 <code>{value.binding_id}</code></p>}</details>
  </article>
}
