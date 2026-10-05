import { useState, type FormEvent } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { api, errorText } from './api'
import { Failure, Field, Loading, Notice } from './components'
import { useResource } from './use-resource'
import type { Agent, DefaultEnvironment, Environment } from './schema'

export function DefaultEnvironmentDialog({ tenant, agent, close, changed }: { tenant: string; agent: Agent; close: () => void; changed: () => void }) {
  const path = `/tenants/${tenant}/agents/${agent.id}`
  const [revision, setRevision] = useState(0)
  const configuration = useResource<DefaultEnvironment>(`${path}/default-environment`, revision)
  const environments = useResource<Environment[]>(`${path}/environments`, revision)
  const error = configuration.error || environments.error
  return <Dialog open onOpenChange={open => { if (!open) close() }}><DialogContent><DialogHeader><DialogTitle>{agent.name} 的执行环境</DialogTitle><DialogDescription>文件、命令和未指定位置的 Hook 使用此默认环境。修改仅影响新操作，已有进程和连接保持原位置。</DialogDescription></DialogHeader>
    {error ? <Failure message={error} retry={() => setRevision(value => value + 1)} /> : !configuration.data || !environments.data ? <Loading /> : <DefaultEnvironmentForm key={`${agent.id}:${configuration.data.version}:${revision}`} initial={configuration.data} environments={environments.data} path={path} close={close} changed={changed} />}
  </DialogContent></Dialog>
}

function DefaultEnvironmentForm({ initial, environments, path, close, changed }: { initial: DefaultEnvironment; environments: Environment[]; path: string; close: () => void; changed: () => void }) {
  const [value, setValue] = useState(initial)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const selected = value.environment_id ? environments.find(environment => environment.id === value.environment_id) : !initial.environment_id ? environments.find(environment => environment.default) : undefined
  async function save(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError('')
    try { await api(`${path}/default-environment`, value, 'PUT'); changed(); close() } catch (err) { setError(errorText(err)) } finally { setBusy(false) }
  }
  return <form className="management-form" onSubmit={save}>
    {error && <Notice error>{error}</Notice>}
    <Field label="默认执行环境"><select className="management-select" value={value.environment_id} onChange={event => setValue({ ...value, environment_id: event.target.value, working_directory: '' })}>
      <option value="">使用部署提供的环境</option>
      {value.environment_id && !selected && <option value={value.environment_id} disabled>已保存的环境 · 当前不可用</option>}
      {environments.map(environment => <option key={environment.id} value={environment.id}>{environment.name} · {environment.kind === 'hosted' ? '托管' : '本机'} · {environment.online ? '在线' : environment.availability === 'sleeping' ? '休眠' : '离线'}</option>)}
    </select></Field>
    {!selected && (value.environment_id || !initial.environment_id) && <Notice>当前默认环境不可用。请确认部署配置或设备授权；新操作不会自动改派到其他环境。</Notice>}
    {!value.environment_id && initial.environment_id && <p className="text-sm text-muted-foreground">保存后使用部署提供的默认环境。</p>}
    {selected && <p className="text-sm text-muted-foreground">{selected.kind === 'hosted' ? '托管环境使用隔离容器，Workspace 与 Home 持久保存。' : '本机环境以设备的 OS 用户权限执行。工作目录只是默认位置，不限制文件访问范围。'}{!selected.online && ' 环境暂未就绪时，操作会保留等待。'}</p>}
    {value.environment_id && <Field label="默认工作目录" hint="使用绝对路径；留空沿用环境目录。"><Input value={value.working_directory} maxLength={4096} pattern="/.*" placeholder={selected?.working_directory || '/absolute/path'} onChange={event => setValue({ ...value, working_directory: event.target.value })} /></Field>}
    <DialogFooter><Button type="button" variant="outline" onClick={close}>取消</Button><Button type="submit" disabled={busy || !!value.environment_id && !selected}>{busy ? '正在保存…' : '保存执行环境'}</Button></DialogFooter>
  </form>
}
