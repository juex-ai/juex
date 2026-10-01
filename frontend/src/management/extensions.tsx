import { useEffect, useState, type FormEvent } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { api, errorText } from './api'
import { Empty, Field, Notice } from './components'
import { useResource } from './use-resource'
import type { Agent, Binding, Environment, ExtensionChange, ExtensionInspection, Manifest } from './schema'

export function extensionResources(manifest: Manifest) {
  return [
    ...(manifest.skills ?? []).map(item => ({ id: `skill/${item.id}`, label: `Skill · ${item.id}`, description: item.description })),
    ...(manifest.hooks ?? []).map(item => ({ id: `hook/${item.id}`, label: `Hook · ${item.id}`, description: item.events.join(', ') })),
    ...(manifest.mcp ?? []).map(item => ({ id: `mcp/${item.id}`, label: `MCP · ${item.id}`, description: item.description })),
    ...(manifest.observables ?? []).map(item => ({ id: `observable/${item.id}`, label: `Observable · ${item.id}`, description: item.description })),
  ]
}

export function ExtensionsDialog({ tenant, agent, close, changed }: { tenant: string; agent: Agent; close: () => void; changed: () => void }) {
  const [current, setCurrent] = useState(agent)
  const [environment, setEnvironment] = useState('')
  const [directory, setDirectory] = useState('')
  const [inspection, setInspection] = useState<ExtensionInspection | null>(null)
  const [request, setRequest] = useState<{ id: string; binding: string; environment: string; directory: string } | null>(null)
  const [selected, setSelected] = useState<string[]>([])
  const [refreshBinding, setRefreshBinding] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState('')
  const environments = useResource<Environment[]>(`/tenants/${tenant}/agents/${agent.id}/environments`)
  const root = `/tenants/${tenant}/agents/${agent.id}`
  const available = (environments.data ?? []).filter(item => item.capabilities.includes('files'))
  const pending = inspection && ['queued', 'waiting', 'dispatched', 'accepted', 'running'].includes(inspection.state)
  useEffect(() => {
    if (!pending) return
    let stopped = false
    const timer = window.setInterval(() => {
      void api<ExtensionInspection>(`${root}/extension-inspections/${inspection.environment_id}/${inspection.operation_id}`).then(value => {
        if (!stopped) { setInspection(value); if (value.catalog) setSelected(extensionResources(value.catalog.manifest).map(item => item.id)) }
      }).catch(error => { if (!stopped) setError(errorText(error)) })
    }, 1500)
    return () => { stopped = true; window.clearInterval(timer) }
  }, [pending, inspection, root])
  async function inspect(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError(''); setSaved('')
    const target = environment || available[0]?.id || ''
    const same = request?.environment === target && request?.directory === directory && (!inspection || pending)
    const next = same ? request! : { id: crypto.randomUUID(), binding: refreshBinding || crypto.randomUUID(), environment: target, directory }
    setRequest(next)
    try {
      const result = await api<ExtensionInspection>(`${root}/extension-inspections`, { request_id: next.id, environment_id: target, directory })
      setInspection(result); setSelected(result.catalog ? extensionResources(result.catalog.manifest).map(item => item.id) : [])
    } catch (error) { setError(errorText(error)) } finally { setBusy(false) }
  }
  async function configure(id: string, change: Omit<ExtensionChange, 'version'>) {
    setBusy(true); setError(''); setSaved('')
    try {
      const updated = await api<Agent>(`${root}/extensions/${id}`, { ...change, version: current.version }, 'PUT')
      setCurrent(updated); changed(); setSaved('扩展配置已保存'); setInspection(null); setRequest(null); setRefreshBinding('')
    } catch (error) { setError(errorText(error)) } finally { setBusy(false) }
  }
  function refresh(binding: Binding) {
    setRefreshBinding(binding.id); setEnvironment(binding.environment_id); setDirectory(binding.directory); setInspection(null); setRequest(null); setSaved('')
  }
  return <Dialog open onOpenChange={open => { if (!open) close() }}><DialogContent className="sm:max-w-2xl max-h-[90dvh] overflow-y-auto"><DialogHeader><DialogTitle>{current.name} 的扩展</DialogTitle><DialogDescription>从执行环境中的目录读取扩展，选择这个 Agent 可用的资源。</DialogDescription></DialogHeader>
    {error && <Notice error>{error}</Notice>}{saved && <Notice>{saved}</Notice>}
    <div className="management-extension-list">{(current.extensions ?? []).length === 0 ? <Empty title="尚未配置扩展">把扩展文件放到托管环境或已授权设备，再读取目录。</Empty> : current.extensions.map(binding => <fieldset className="management-hook-settings" key={binding.id}><legend>{binding.catalog.manifest.name} · {binding.catalog.manifest.version}</legend><small>{environments.data?.find(item => item.id === binding.environment_id)?.name ?? '已保存的执行环境'} · {binding.directory}</small>
      <ResourceSelection manifest={binding.catalog.manifest} value={binding.resources} disabled={busy} onChange={resources => { void configure(binding.id, { enabled: binding.enabled, resources }) }} />
      <div className="management-row-actions"><Button type="button" variant="outline" disabled={busy} onClick={() => void configure(binding.id, { enabled: !binding.enabled, resources: binding.resources })}>{binding.enabled ? '停用扩展' : '启用扩展'}</Button><Button type="button" variant="outline" disabled={busy} onClick={() => refresh(binding)}>重新读取</Button><Button type="button" variant="ghost" disabled={busy} onClick={() => void configure(binding.id, { enabled: false, resources: [], remove: true })}>移除配置</Button></div>
    </fieldset>)}</div>
    <form className="management-form" onSubmit={inspect}><h3>{refreshBinding ? '更新已配置的扩展' : '添加扩展'}</h3>{environments.error && <Notice error>{environments.error}</Notice>}<Field label="扩展所在环境"><select className="management-select" required value={environment || available[0]?.id || ''} disabled={busy || !!pending} onChange={event => { setEnvironment(event.target.value); setInspection(null); setRequest(null); setRefreshBinding('') }}>{available.map(item => <option key={item.id} value={item.id}>{item.name} · {item.online ? '在线' : '离线'}</option>)}{!available.length && <option value="">暂无已授权的文件执行环境</option>}</select></Field><Field label="扩展目录"><Input required value={directory} disabled={busy || !!pending} onChange={event => { setDirectory(event.target.value); setInspection(null); setRequest(null) }} placeholder="例如 /workspace/my-extension" /><small>目录中需要有 juex.extension.json。读取操作不会启动扩展中的命令。</small></Field><Button type="submit" disabled={busy || !!pending || !available.length}>{pending ? '正在等待执行环境…' : busy ? '正在读取…' : '读取扩展目录'}</Button></form>
    {inspection && <div aria-live="polite">{inspection.catalog ? <><h3>{inspection.catalog.manifest.name} · {inspection.catalog.manifest.version}</h3><p>{inspection.catalog.manifest.description}</p><ResourceSelection manifest={inspection.catalog.manifest} value={selected} disabled={busy} onChange={setSelected} /><Button type="button" disabled={busy || !request} onClick={() => void configure(request!.binding, { enabled: true, resources: selected, inspection_id: inspection.operation_id, environment_id: inspection.environment_id })}>保存所选资源</Button></> : <Notice error={['failed', 'unknown', 'cancelled', 'expired'].includes(inspection.state)}>{inspection.error || (pending ? '保留此次读取操作，设备可用后继续。' : `读取状态：${inspection.state}`)}</Notice>}</div>}
    <p className="text-sm text-muted-foreground">保存后的技能与配置从下一轮生效。停用或移除资源会撤销旧配置下未完成的工作；设备上的扩展文件会保留。Observable 与 MCP 由 Agent 按需启动。</p>
  </DialogContent></Dialog>
}
function ResourceSelection({ manifest, value, onChange, disabled }: { manifest: Manifest; value: string[]; onChange: (value: string[]) => void; disabled: boolean }) {
  return <div className="management-hook-events">{extensionResources(manifest).map(item => <label className="management-checkbox" key={item.id}><input type="checkbox" disabled={disabled} checked={value.includes(item.id)} onChange={event => onChange(event.target.checked ? [...value, item.id] : value.filter(id => id !== item.id))} /><span>{item.label}{item.description && <small className="block">{item.description}</small>}</span></label>)}</div>
}
