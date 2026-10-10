import {useState} from 'react'
import {nanoid} from 'nanoid'
import {Button} from '@/components/ui/button'
import {api,APIError,errorText} from './api'
import {Empty,Failure,Loading,Notice} from './components'
import {useResource} from './use-resource'
import {LiveOperationOutput} from './operation-output'
import type {AgentDetail,ObservationPage,ObservationStatus,ObservedEvents,ObserverControl,ObserverStart,ObserverTarget} from './schema'

const states:Record<string,string>={pending:'等待启动',waiting:'等待环境',dispatched:'等待接收',accepted:'已接收',running:'运行中',completed:'已结束',failed:'失败',cancelled:'已停止',unknown:'结果未知'}
function savedStart(key:string):ObserverStart|null{try{return JSON.parse(sessionStorage.getItem(key)??'null')}catch{return null}}

export function Observers({base,actor,detail}:{base:string;actor:string;detail:AgentDetail}){
 const[after,setAfter]=useState(''),[revision,setRevision]=useState(0)
 const page=useResource<ObservationPage>(`${base}/observation-sources?after=${encodeURIComponent(after)}`,revision,5000)
 const refresh=()=>setRevision(n=>n+1)
 return <section className="management-panel management-runtime-resources" aria-label="事件源与订阅"><h2>事件源与订阅</h2>
 <p>从扩展资源启动 Observable 或 MCP。执行发生在资源绑定的环境和目录；Runtime 保存事件并向订阅的 Thread 投递，关闭页面不会停止它。</p>
 {page.error?<Failure message={page.error} retry={refresh}/>:!page.data?<Loading/>:<>
 <ObserverForm base={base} actor={actor} detail={detail} page={page.data} refresh={refresh}/>
 {page.data.targets_truncated&&<Notice>目标列表显示最早的 100 个活跃普通 Thread。</Notice>}
 {!page.data.sources.length?<Empty title="尚无事件源">选择已配置的资源后启动；查看页面不会自动执行命令。</Empty>:page.data.sources.map(source=><Source key={source.id} base={base} source={source} control={page.data!.controls.find(c=>c.id===source.control_id)} targets={page.data!.targets} refresh={refresh}/>)}
 <div className="management-row-actions">{after&&<Button variant="outline" onClick={()=>setAfter('')}>返回首批事件源</Button>}{page.data.next&&<Button variant="outline" onClick={()=>setAfter(page.data!.next)}>更多事件源</Button>}</div>
 </>}
 </section>
}
function ObserverForm({base,actor,detail,page,refresh}:{base:string;actor:string;detail:AgentDetail;page:ObservationPage;refresh:()=>void}){
 const key=`juex.observer-start:${actor}:${base}`
 const[pending,setPending]=useState<ObserverStart|null>(()=>savedStart(key))
 const[resource,setResource]=useState(''),[thread,setThread]=useState(''),[mode,setMode]=useState('once'),[subscribe,setSubscribe]=useState(true)
 const[busy,setBusy]=useState(false),[error,setError]=useState(''),[notice,setNotice]=useState('')
 const unresolved=pending
 const resources=(detail.agent.extensions??[]).filter(binding=>binding.enabled).flatMap(binding=>binding.resources.filter(id=>id.startsWith('observable/')||id.startsWith('mcp/')).map(id=>({key:`${binding.id}:${id}`,binding,kind:id.split('/')[0],id:id.slice(id.indexOf('/')+1)})))
 const selected=resources.find(value=>value.key===resource)||resources[0]
 const target=thread||page.targets[0]?.id||''
 const disabled=!detail.can_execute||['extensions','observations',selected?.kind==='mcp'?'mcp':'shell'].some(name=>detail.effective?.modules[name]?.enabled===false)
 async function start(retry=false){
  if(busy)return
  if(!retry&&(!selected||!target||disabled||unresolved))return
  const request=retry?unresolved!:{request_id:nanoid(),thread_id:target,binding_id:selected!.binding.id,kind:selected!.kind,resource_id:selected!.id,agent_version:detail.agent.version,revision:selected!.binding.catalog.revision,mode,subscribe}
  setBusy(true);setError('');setNotice('')
  try{
   sessionStorage.setItem(key,JSON.stringify(request));setPending(request)
   const result=await api<ObserverControl>(`${base}/observers`,request)
   if(result.request_id!==request.request_id)throw new Error('启动回执与原请求不一致，请核对原请求。')
   try{sessionStorage.removeItem(key)}catch{/* A later retry still uses the original accepted identity. */}setPending(null)
   setNotice(`启动请求已保存，${states[result.state]??result.state}。`);refresh()
  }catch(error){setError(errorText(error));if(!retry&&error instanceof APIError&&error.status<500){try{sessionStorage.removeItem(key);setPending(null)}catch{/* Retain the original request. */}}}finally{setBusy(false)}
 }
 return <div className="management-observer-start">
 {error&&<Notice error>{error}</Notice>}{notice&&<Notice>{notice}</Notice>}
 {unresolved?<Notice>{busy?'正在保存原启动请求…':'保留了尚未确认的启动请求。重试会使用同一个请求和原配置，不会新建一份进程。'}<Button variant="outline" disabled={busy} onClick={()=>void start(true)}>核对并重试原启动</Button></Notice>:<>
 {!selected?<p>尚未选择可运行的 MCP 或 Observable 扩展资源。请先在“配置 → 执行环境与扩展”中配置。</p>:<>
 <div className="management-observer-fields"><label>扩展资源<select aria-label="扩展资源" value={selected.key} disabled={busy} onChange={e=>setResource(e.target.value)}>{resources.map(value=><option key={value.key} value={value.key}>{value.binding.catalog.manifest.name} · {value.kind==='mcp'?'MCP':'Observable'} · {value.id}</option>)}</select></label>
 <label>所属 Thread<select aria-label="所属 Thread" value={target} disabled={busy} onChange={e=>setThread(e.target.value)}>{page.targets.map(value=><option value={value.id} key={value.id}>{value.name}</option>)}</select></label>
 <label>运行方式<select aria-label="运行方式" value={mode} disabled={busy} onChange={e=>setMode(e.target.value)}><option value="once">按需运行一次</option><option value="continuous">持续运行</option></select></label></div>
 <p>执行目录 <code>{selected.binding.directory}</code></p>
 <label><input type="checkbox" checked={subscribe} disabled={busy} onChange={e=>setSubscribe(e.target.checked)}/> 启动时让所属 Thread 订阅事件</label>
 <p>{mode==='continuous'?'确认运行结束并保存输出后，等待至少 5 秒启动下一次。结果未知、撤权或手动停止时不再重启。':'本次执行结束后保留记录，不自动重新启动。'} 订阅事件可能唤醒 Thread 并调用模型；持续运行会保留你修改后的订阅设置。</p>
 {!target&&<Notice>尚无活跃普通 Thread，请先进入对话创建 Main。</Notice>}{disabled&&<Notice>Agent 或所需模块已停用。请在配置中检查 Extensions、Observations 和对应执行能力。</Notice>}
 <Button disabled={busy||!target||disabled} onClick={()=>void start()}>启动资源</Button>
 </>}
 </>}
 </div>
}
function Source({base,source,control,targets,refresh}:{base:string;source:ObservationStatus;control?:ObserverControl;targets:ObserverTarget[];refresh:()=>void}){
 const[expanded,setExpanded]=useState(false),[target,setTarget]=useState(''),[busy,setBusy]=useState(false),[error,setError]=useState(''),[notice,setNotice]=useState('')
 const historical=!!control&&control.source_id!==source.id
 const thread=target||source.thread_id
 const subscriptionKind=source.kind==='mcp_connect'?'mcp.notification':source.kind==='observe_command'?'command.observation':'operation.terminal'
 const existing=source.subscriptions.find(sub=>sub.thread_id===thread&&!sub.method&&sub.kind===subscriptionKind)
 async function change(path:string,body:unknown,method?:string){setBusy(true);setError('');try{await api(path,body,method);setNotice(method==='PUT'?'订阅设置已保存。':'停止请求已保存，等待原环境确认。');refresh()}catch(error){setError(errorText(error))}finally{setBusy(false)}}
 return <article className="management-runtime-environment" aria-label={`事件源 ${source.id}`}>
 <h3>{control?`${control.kind==='mcp'?'MCP':'Observable'} · ${control.resource_id}`:source.kind==='mcp_connect'?'MCP 通知':source.kind==='observe_command'?'Observable':'命令状态'}<span>{source.origin==='manual'?'人工启动':'模型启动'}</span></h3>
 <p>{control?`${control.mode==='continuous'?'持续运行':'按需运行'} · 第 ${control.attempt} 次 · 最近确认：${states[control.state]??control.state}`:'由模型创建的原执行实例'}{source.stop_requested?' · 已请求停止':source.closed?' · 事件采集已结束':' · 正在采集事件'}</p>
 {control&&control.source_id!==source.id&&<Notice>这是较早实例，当前实例标识：<code>{control.source_id}</code>。停止会关闭此资源的持续运行及当前实例。订阅设置请在当前实例操作。</Notice>}
 <p>环境 <code>{source.environment_id}</code> · 目录 <code>{source.directory||'环境默认目录'}</code></p>
 {error&&<Notice error>{error}</Notice>}{notice&&<Notice>{notice}</Notice>}
 <div className="management-row-actions"><Button variant="outline" disabled={busy||source.stop_requested} onClick={()=>void change(`${base}/observation-sources/${source.id}/stop`,{})}>停止资源及其订阅</Button><Button variant="outline" onClick={()=>setExpanded(!expanded)}>{expanded?'收起事件与原操作':'查看事件与原操作'}</Button></div>
 {source.subscriptions_truncated&&<Notice>仅显示前 100 个订阅。</Notice>}
 <p>已采集 {source.cursor} 字节 · {source.subscriptions.filter(sub=>sub.enabled).length} 个启用订阅。采集结束不代表已确认进程停止，执行结果以原操作为准。</p>
 {!historical&&<div className="management-row-actions"><label>订阅目标<select aria-label="订阅目标" value={thread} disabled={busy} onChange={e=>setTarget(e.target.value)}>{targets.map(value=><option key={value.id} value={value.id}>{value.name}</option>)}</select></label><Button variant="outline" disabled={busy||!thread||!existing?.enabled&&(source.stop_requested||source.closed)} onClick={()=>void change(`${base}/observation-sources/${source.id}/subscriptions/${thread}`,{enabled:!existing?.enabled},'PUT')}>{existing?.enabled?'取消此 Thread 订阅':'订阅后续事件'}</Button></div>}
 {expanded&&<><SourceEvents base={base} source={source.id}/><LiveOperationOutput base={base} handle={{environment_id:source.environment_id,operation_id:source.operation_id}}/><details><summary>标识与订阅</summary><code>{source.id}</code>{source.subscriptions.map(sub=><p key={sub.id}>{targets.find(target=>target.id===sub.thread_id)?.name||sub.thread_id} · {sub.kind} · {sub.enabled?'已启用':'已停用'}{sub.method&&` · ${sub.method}`}</p>)}</details></>}
 </article>
}
function SourceEvents({base,source}:{base:string;source:string}){
 const[after,setAfter]=useState(''),[revision,setRevision]=useState(0)
 const events=useResource<ObservedEvents>(`${base}/observation-sources/${source}/events?after=${encodeURIComponent(after)}`,revision,5000)
 return <section aria-label="已保存事件"><h4>已保存事件</h4><p>“已投递”表示已加入 Thread 输入队列，不表示模型已经处理完成。未订阅的事件仍可在这里查看。</p>{events.error?<Failure message={events.error} retry={()=>setRevision(n=>n+1)}/>:!events.data?<Loading/>:<>
 {events.data.items.length?events.data.items.map(event=><details key={event.id}><summary>{new Date(event.created_at).toLocaleString()} · {event.kind} · 待投递 {event.pending} / 已投递 {event.delivered} / 已跳过 {event.skipped}</summary>{event.data_truncated&&<Notice>事件较大，仅显示预览。</Notice>}<pre>{JSON.stringify(event.data,null,2)}</pre><small>事件 ID {event.id}</small></details>):<p>暂无已保存事件。</p>}
 <div className="management-row-actions">{after&&<Button variant="outline" onClick={()=>setAfter('')}>最新事件</Button>}{events.data.next&&<Button variant="outline" onClick={()=>setAfter(events.data!.next)}>更早事件</Button>}</div></>}
 </section>
}
