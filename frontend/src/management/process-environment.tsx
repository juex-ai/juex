import { useState, type FormEvent } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { api, errorText } from './api'
import { Failure, Field, Loading, Notice } from './components'
import { useResource } from './use-resource'
import type { EnvironmentInspection, ProcessEnvironmentChange, ProcessEnvironmentView } from './schema'

const labels:Record<string,string>={agent:'Agent',workspace:'Workspace',fleet:'Fleet',tenant:'Tenant'}
export function ProcessEnvironmentDialog({tenant,agent,close}:{tenant:string;agent:string;close:()=>void}) {
 const root=`/tenants/${tenant}/agents/${agent}`
 const resource=useResource<ProcessEnvironmentView>(`${root}/process-environment`)
 const environments=useResource<EnvironmentInspection>(`${root}/environment-status`)
 const [saved,setSaved]=useState<ProcessEnvironmentView|null>(null)
 const [layer,setLayer]=useState('agent')
 const [key,setKey]=useState(''),[value,setValue]=useState('')
 const [environment,setEnvironment]=useState<string|null>(null),[directory,setDirectory]=useState<string|null>(null)
 const [busy,setBusy]=useState(false),[uncertain,setUncertain]=useState(false),[error,setError]=useState(''),[notice,setNotice]=useState('')
 const current=saved??resource.data
 const declaration=current?.layers[layer]
 const writable=layer!=='tenant'||!!current?.tenant_writable
 const available=(environments.data?.environments??[]).filter(item=>item.capabilities.some(cap=>cap==='shell'||cap==='mcp'))
 const target=environment??declaration?.environment_id??available[0]?.id??''
 const cwd=directory??(environment===null?declaration?.working_directory:undefined)??available.find(item=>item.id===target)?.working_directory??''
 function choose(next:string){setLayer(next);setKey('');setValue('');setEnvironment(null);setDirectory(null);setNotice('')}
 async function load(){setBusy(true);setError('');try{setSaved(await api<ProcessEnvironmentView>(`${root}/process-environment`));setUncertain(false);setKey('');setValue('');setEnvironment(null);setDirectory(null)}catch(err){setError(errorText(err))}finally{setBusy(false)}}
 async function update(set:Record<string,string>,remove:string[]){
  if(!declaration||busy||uncertain||!writable)return
  setBusy(true);setError('');setNotice('')
  const change:ProcessEnvironmentChange={version:declaration.version,set,remove,...(layer==='workspace'&&Object.keys(set).length?{environment_id:target,working_directory:cwd}:{})}
  try{setSaved(await api<ProcessEnvironmentView>(`${root}/process-environment/${layer}`,change,'PUT'));setKey('');setValue('');setNotice('环境变量已保存。页面不会回填，系统不会将私有值写入命令参数。')}
  catch(err){setError(errorText(err));setUncertain(true)}finally{setBusy(false)}
 }
 function submit(event:FormEvent){event.preventDefault();void update({[key]:value},[])}
 return <Dialog open onOpenChange={open=>{if(!open)close()}}><DialogContent className="sm:max-w-2xl max-h-[90dvh] overflow-y-auto"><DialogHeader><DialogTitle>执行环境变量</DialogTitle><DialogDescription>为 Shell、Hook、MCP 子进程和 Observable 提供持续配置。值在服务端加密保存，只发送到获准的执行环境，不作为模型指令发送。</DialogDescription></DialogHeader>
 {resource.error&&!current?<Failure message={resource.error} retry={()=>void load()}/>:!current?<Loading/>:<>
 {error&&<Notice error>{error}</Notice>}{notice&&<Notice>{notice}</Notice>}
 {uncertain&&<Notice>提交结果或配置版本需要重新确认。输入仍保留；重新加载会清除未提交的值。<Button variant="outline" disabled={busy} onClick={()=>void load()}>重新加载元数据</Button></Notice>}
 <Field label="配置层级"><select aria-label="配置层级" className="management-select" value={layer} disabled={busy||uncertain} onChange={event=>choose(event.target.value)}>{Object.entries(labels).map(([id,label])=><option key={id} value={id}>{label}{id==='tenant'?' · 全租户共享':id==='fleet'?' · 当前用户的所有 Agents':''}</option>)}</select></Field>
 <p>Agent → Workspace → Fleet → Tenant。删除本层变量后恢复下层值；空字符串是明确的覆盖值。命令单独指定的变量最后生效。改动会撤销受影响 Agent 的旧执行授权，排队工作不会取得新值后继续运行。</p>
 {!writable&&<Notice>Tenant 变量由租户管理员设置。</Notice>}
 {layer==='workspace'&&<>{environments.error&&<Notice error>{environments.error}</Notice>}<Field label="变量所在执行环境"><select aria-label="变量所在执行环境" className="management-select" disabled={busy||uncertain} value={target} onChange={event=>{setEnvironment(event.target.value);setDirectory(null)}}>{target&&!available.some(item=>item.id===target)&&<option value={target}>原绑定环境已不可用 · 仍可删除变量</option>}{available.map(item=><option key={item.id} value={item.id}>{item.name}</option>)}</select></Field><Field label="变量工作目录"><Input aria-label="变量工作目录" value={cwd} disabled={busy||uncertain} onChange={event=>setDirectory(event.target.value)}/><small>仅在这个环境及此目录内的命令使用 Workspace 值。更换设备不会自动带过去。</small></Field></>}
 <section aria-label="本层变量"><h3>{labels[layer]} 已设置 · v{declaration?.version??0}</h3>{declaration?.keys.length?declaration.keys.map(name=><div className="management-row-actions" key={name}><code>{name}</code><span>已设置</span><Button variant="outline" disabled={!writable||busy||uncertain} onClick={()=>setKey(name)}>替换值</Button><Button variant="ghost" disabled={!writable||busy||uncertain} onClick={()=>void update({},[name])}>删除 {name}</Button></div>):<p>本层没有覆盖，继承下层变量。</p>}</section>
 <form className="management-form" onSubmit={submit}><Field label="变量名"><Input required maxLength={128} pattern="[A-Za-z_][A-Za-z0-9_]*" autoComplete="off" disabled={!writable||busy||uncertain} value={key} onChange={event=>setKey(event.target.value)}/></Field><Field label="新值"><Input aria-label="新值" type="password" autoComplete="new-password" maxLength={4096} disabled={!writable||busy||uncertain} value={value} onChange={event=>setValue(event.target.value)}/><small>已保存的值不会填入。留空会保存空字符串。</small></Field><Button type="submit" disabled={!key||!writable||busy||uncertain||layer==='workspace'&&(!target||!cwd||!available.some(item=>item.id===target))}>保存变量</Button></form>
 <details><summary>查看有效变量名与来源</summary><p>Workspace 来源适用于已绑定的环境和目录，其余命令继续使用下层值。这里只列出变量名。</p>{current.effective.map(item=><p key={item.name}><code>{item.name}</code> · {labels[item.source]}</p>)}</details>
 </>}
 </DialogContent></Dialog>
}
