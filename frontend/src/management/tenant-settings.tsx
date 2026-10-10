import { useState, type FormEvent } from 'react'
import { Button } from '@/components/ui/button'
import { APIError, api, errorText } from './api'
import { Failure, Loading, Notice, PageHeading } from './components'
import { ConfigurationEditor } from './configuration'
import { useResource } from './use-resource'
import type { ConfigurationLayer, Model, TenantAccess } from './schema'

export function TenantSettingsPage({tenant}:{tenant:TenantAccess}) {
  const [revision,setRevision]=useState(0)
  const settings=useResource<ConfigurationLayer>(`/tenants/${tenant.id}/settings`,revision)
  const models=useResource<Model[]>(`/tenants/${tenant.id}/models`,revision)
  const [draft,setDraft]=useState<ConfigurationLayer|null>(null)
  const [busy,setBusy]=useState(false)
  const [error,setError]=useState('')
  const [notice,setNotice]=useState('')
  const current=draft??settings.data
  const stale=draft&&settings.data&&draft.version!==settings.data.version
  async function save(event:FormEvent) {
    event.preventDefault();if(!current||busy)return
    setBusy(true);setError('');setNotice('')
    try { await api(`/tenants/${tenant.id}/settings`,current,'PUT');setDraft(null);setRevision(value=>value+1);setNotice('Tenant 默认配置已保存。') }
    catch(error){setError(errorText(error));if(error instanceof APIError&&error.status===409)setRevision(value=>value+1)}finally{setBusy(false)}
  }
  return <><PageHeading title="Tenant 默认配置" description="所有 Fleet 的基础模型顺序和模块开关。Fleet、Workspace 快照和 Agent 可依次覆盖；模型目录准入仍由部署管理员管理。"/>
    {settings.error||models.error?<Failure message={settings.error??models.error!} retry={()=>setRevision(value=>value+1)}/>:!current||!models.data?<Loading/>:<section className="management-panel management-settings-form">
      {tenant.role!=='admin'&&<Notice>当前为只读视图。租户管理员可以修改这些默认值。</Notice>}
      {error&&<Notice error>{error}</Notice>}{notice&&<Notice>{notice}</Notice>}
      {stale&&<Notice>服务器配置已改变，本地编辑仍保留。<Button variant="outline" onClick={()=>setDraft(null)}>加载服务器配置</Button></Notice>}
      <form className="management-form" onSubmit={save}><fieldset disabled={busy||tenant.role!=='admin'} className="management-settings-fields"><ConfigurationEditor bottom value={current.declaration} models={models.data} onChange={declaration=>setDraft({...current,declaration})}/></fieldset><Button disabled={busy||tenant.role!=='admin'||!draft} type="submit">{busy?'正在保存…':'保存 Tenant 配置'}</Button></form>
    </section>}</>
}
