import {useEffect,useState} from 'react'
import {nanoid} from 'nanoid'
import {Button} from '@/components/ui/button'
import {Input} from '@/components/ui/input'
import {Dialog,DialogContent,DialogDescription,DialogHeader,DialogTitle} from '@/components/ui/dialog'
import {APIError,api,errorText} from './api'
import {Failure,Field,Loading,Notice} from './components'
import {ConfigurationEditor} from './configuration'
import {useResource} from './use-resource'
import {useWorkspaceRead} from './workspace-files'
import type {Agent,AgentDetail,Environment,Model,WorkspaceConfigurationChange,WorkspaceConfigurationPreview,WorkspaceReadRequest} from './schema'

export function WorkspaceConfigurationDialog({tenant,agent,close,changed}:{tenant:string;agent:Agent;close:()=>void;changed:()=>void}) {
  const base=`/tenants/${tenant}/agents/${agent.id}`
  const [revision,setRevision]=useState(0)
  const environments=useResource<Environment[]>(`${base}/environments`,revision)
  const models=useResource<Model[]>(`/tenants/${tenant}/models`,revision)
  const [selected,setSelected]=useState('')
  const [directory,setDirectory]=useState('')
  const [path,setPath]=useState('.juex/juex.yaml')
  const [request,setRequest]=useState<WorkspaceReadRequest|null>(null)
  const [retry,setRetry]=useState(0)
  const read=useWorkspaceRead(base,request,retry)
  const [previewState,setPreviewState]=useState<{id:string;value?:WorkspaceConfigurationPreview;error?:string}>({id:''})
  const [busy,setBusy]=useState(false)
  const [error,setError]=useState('')
  const [notice,setNotice]=useState('')
  const [unknown,setUnknown]=useState<WorkspaceConfigurationChange|null>(null)
  const available=environments.data?.filter(value=>value.capabilities.includes('files'))??[]
  const environment=available.find(value=>value.id===selected||!selected&&value.default)
  const preview=previewState.id===read.receipt?.operation_id?previewState.value:undefined
  const previewError=previewState.id===read.receipt?.operation_id?previewState.error:undefined
  useEffect(()=>{
    if(!read.receipt||read.receipt.state!=='completed')return
    const receipt=read.receipt,controller=new AbortController()
    void api<WorkspaceConfigurationPreview>(`${base}/workspace-configurations/${receipt.environment_id}/${encodeURIComponent(receipt.operation_id)}`,undefined,undefined,controller.signal).then(value=>setPreviewState({id:receipt.operation_id,value})).catch(error=>{if(!controller.signal.aborted)setPreviewState({id:receipt.operation_id,error:errorText(error)})})
    return ()=>controller.abort()
  },[base,read.receipt,retry])
  async function reconcile(change:WorkspaceConfigurationChange) {
    const current=await api<AgentDetail>(base)
    const snapshot=current.agent.workspace_configuration
    if(change.remove?!snapshot:snapshot?.operation_id===change.operation_id&&snapshot.sha256===change.sha256) {
      setUnknown(null);setNotice(change.remove?'Workspace 快照已移除，恢复下层配置。':'Workspace 配置快照已应用。文件后续修改不会自动生效。');changed();return true
    }
    return false
  }
  async function apply(remove=false) {
    if(busy||unknown||!remove&&!preview?.configuration)return
    const snapshot=preview?.configuration
    const change:WorkspaceConfigurationChange={version:agent.version,remove,environment_id:snapshot?.environment_id??'',operation_id:snapshot?.operation_id??'',sha256:snapshot?.sha256??''}
    setBusy(true);setError('');setNotice('');setUnknown(change)
    try {await api(`${base}/workspace-configuration`,change,'PUT');setUnknown(null);setNotice(remove?'Workspace 快照已移除，恢复下层配置。':'Workspace 配置快照已应用。文件后续修改不会自动生效。');changed()}
    catch(error){
      setError(errorText(error))
      if(error instanceof APIError&&error.status<500){setUnknown(null);changed();return}
      try {if(!await reconcile(change)){setError('服务器尚未确认此变更。可先检查结果；重新编辑前请读取最新 Agent 配置。')}}catch{setError('变更结果未知。请检查结果，避免重复提交。')}
    }finally{setBusy(false)}
  }
  async function check() {if(!unknown)return;setBusy(true);try{if(!await reconcile(unknown))setError('当前保存值尚未匹配本次变更。请重新打开配置查看最新版本。')}catch(error){setError(errorText(error))}finally{setBusy(false)}}
  return <Dialog open onOpenChange={open=>{if(!open&&!busy)close()}}><DialogContent className="sm:max-w-3xl max-h-[90dvh] overflow-y-auto"><DialogHeader><DialogTitle>Workspace 配置快照</DialogTitle><DialogDescription>从授权环境读取 YAML，预览后应用。模型顺序和模块开关位于 Fleet 之上、Agent 之下。扩展继续通过“管理扩展资源”检查和启用。</DialogDescription></DialogHeader>
    {agent.workspace_configuration&&<section className="management-panel"><strong>当前已应用 · v{agent.workspace_configuration.version}</strong><p>{environments.data?.find(value=>value.id===agent.workspace_configuration?.environment_id)?.name??agent.workspace_configuration.environment_id}</p><code>{agent.workspace_configuration.working_directory}/{agent.workspace_configuration.path}</code><p>读取发起于 {new Date(agent.workspace_configuration.read_started_at).toLocaleString()}</p><small>SHA-256 {agent.workspace_configuration.sha256}</small><Button variant="outline" disabled={busy||!!unknown} onClick={()=>void apply(true)}>移除快照并恢复继承</Button></section>}
    {error&&<Notice error>{error}</Notice>}{notice&&<Notice>{notice}</Notice>}{unknown&&<Notice>正在保留本次变更目标。<Button variant="outline" disabled={busy} onClick={()=>void check()}>检查配置结果</Button></Notice>}
    {environments.error||models.error?<Failure message={environments.error??models.error!} retry={()=>setRevision(value=>value+1)}/>:!environments.data||!models.data?<Loading/>:<>
      <fieldset disabled={busy||!!unknown} className="management-settings-fields">
        <Field label="配置文件所在环境"><select className="management-select" value={environment?.id??''} onChange={event=>{setSelected(event.target.value);setDirectory('');setRequest(null)}}><option value="">请选择环境</option>{available.map(value=><option key={value.id} value={value.id}>{value.name} · {value.online?'在线':value.availability==='sleeping'?'休眠 · 读取时唤醒':'离线'}</option>)}</select></Field>
        <Field label="配置 Workspace 目录"><Input value={directory||environment?.working_directory||''} onChange={event=>{setDirectory(event.target.value);setRequest(null)}}/></Field>
        <Field label="配置文件相对路径"><Input value={path} onChange={event=>{setPath(event.target.value);setRequest(null)}}/></Field>
        <details><summary>支持的文件内容</summary><p>最多64 KiB，仅包含模型目录中的名称或 ID，以及模块开关。未列出的项目继承下层，不接受凭据、远程引用或环境变量。</p><pre>{'models: [provider:primary, provider:backup]\nmodules:\n  mcp: true\n  memory: false'}</pre></details>
        <Button variant="outline" disabled={!environment||!path} onClick={()=>{setRequest({request_id:nanoid(),environment_id:environment!.id,directory:directory||environment!.working_directory,query:{path,hidden:true,read:true}});setNotice('');setError('')}}>读取并预览配置</Button>
      </fieldset>
      {request&&(read.error||previewError)?<Failure message={read.error??previewError!} retry={()=>setRetry(value=>value+1)}/>:request&&!preview?<Loading/>:preview?.configuration&&<section aria-label="Workspace 配置预览"><p>来自 {environments.data.find(value=>value.id===preview.configuration?.environment_id)?.name??preview.configuration.environment_id}：{preview.configuration.working_directory}/{preview.configuration.path}</p><small>SHA-256 {preview.configuration.sha256}</small><fieldset disabled><ConfigurationEditor preview value={preview.configuration.declaration} effective={preview.effective??undefined} models={models.data} onChange={()=>{}}/></fieldset><Button disabled={busy||!!unknown} onClick={()=>void apply()}>应用此配置快照</Button></section>}
    </>}
  </DialogContent></Dialog>
}
