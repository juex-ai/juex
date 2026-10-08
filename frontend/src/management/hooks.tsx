import { randomUUID } from '@/lib/uuid'
import { useState } from 'react'
import { Plus, Trash2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Field, Notice } from './components'
import { useResource } from './use-resource'
import type { Declaration, Device } from './schema'

export const hookEvents: Record<string, string> = {
  ThreadStart: '对话首次开始', UserPromptSubmit: '收到用户输入', PreToolUse: '执行工具前',
  PostToolUse: '工具返回后', PreCompact: '压缩上下文前', PostCompact: '压缩上下文后', Stop: '本轮完成前',
}

export function HooksEditor({ tenant, owner, agent, value, onChange }: { tenant: string; owner: string; agent?: string; value: Declaration[]; onChange: (value: Declaration[]) => void }) {
  const devices = useResource<Device[]>(`/tenants/${tenant}/users/${owner}/devices`)
  const update = (index: number, change: Partial<Declaration>) => onChange(value.map((hook, i) => i === index ? { ...hook, ...change } : hook))
  const available = (devices.data ?? []).filter(device => device.kind === 'native' && device.status === 'active' && agent && device.grants[agent]?.includes('shell'))
  return <details className="management-hooks-editor"><summary>Hooks · {value.filter(hook => hook.enabled).length} 个已启用</summary>
    <p>在选定的执行环境中自动运行脚本。配置修改影响下一轮；停用或移除 Hook 会撤销旧配置下仍未完成的工作。</p>
    {devices.error && <Notice error>附加设备列表暂不可用，已保存的环境选择仍会保留。</Notice>}
    {value.map((hook, index) => <fieldset key={hook.id} className="management-hook-settings"><legend>{hook.id}</legend>
      <label className="management-checkbox"><input type="checkbox" checked={hook.enabled} onChange={event => update(index, { enabled: event.target.checked })} />启用</label>
      <div className="management-hook-events">{Object.entries(hookEvents).map(([event, label]) => <label className="management-checkbox" key={event}><input type="checkbox" checked={hook.events.includes(event)} onChange={input => update(index, { events: input.target.checked ? [...hook.events, event] : hook.events.filter(value => value !== event) })} />{label}</label>)}</div>
      <Field label="执行环境"><select className="management-select" value={hook.environment_id ?? ''} onChange={event => update(index, { environment_id: event.target.value })}><option value="">Agent 的默认环境</option>{available.map(device => <option key={device.id} value={device.id}>{device.name} · {device.online ? '在线' : '离线'}</option>)}{hook.environment_id && !available.some(device => device.id === hook.environment_id) && <option value={hook.environment_id}>已保存的设备 · 当前不可用</option>}</select></Field>
      <HookCommand value={hook.command} onChange={command => update(index, { command })} />
      <Field label="工作目录（留空使用环境默认目录）"><Input value={hook.working_directory ?? ''} onChange={event => update(index, { working_directory: event.target.value })} /></Field>
      <Field label="超时（秒）"><Input type="number" min={1} max={300} value={hook.timeout_seconds || 10} onChange={event => update(index, { timeout_seconds: Number(event.target.value) })} /></Field>
      <Field label="限定工具（可选，用逗号分隔）"><Input value={hook.tools?.join(', ') ?? ''} onChange={event => update(index, { tools: event.target.value.split(',').map(value => value.trim()).filter(Boolean) })} placeholder="read, write, exec_command" /></Field>
      <label className="management-checkbox"><input type="checkbox" checked={hook.required} onChange={event => update(index, { required: event.target.checked })} />必须成功</label>
      {hook.source && <small>来源：{hook.source}</small>}
      <Button type="button" variant="ghost" onClick={() => onChange(value.filter((_, i) => i !== index))}><Trash2 size={14} />移除 Hook</Button>
    </fieldset>)}
    <Button type="button" variant="outline" disabled={value.length >= 16} onClick={() => onChange([...value, { id: `hook-${randomUUID().slice(0, 8)}`, enabled: true, required: true, events: ['PreToolUse'], command: ['/bin/sh', '-c', ''], timeout_seconds: 10, max_output_bytes: 8192 }])}><Plus size={14} />添加 Hook</Button>
  </details>
}

function HookCommand({ value, onChange }: { value: string[]; onChange: (value: string[]) => void }) {
  const [draft, setDraft] = useState(JSON.stringify(value))
  const [error, setError] = useState('')
  return <Field label="命令及参数（JSON 数组）"><Textarea required rows={3} value={draft} onChange={event => {
    setDraft(event.target.value)
    try {
      const next: unknown = JSON.parse(event.target.value)
      if (!Array.isArray(next) || next.length === 0 || !next.every(value => typeof value === 'string')) throw new Error()
      event.target.setCustomValidity(''); setError(''); onChange(next)
    } catch { event.target.setCustomValidity('请输入命令和参数组成的 JSON 数组'); setError('请输入命令和参数组成的 JSON 数组') }
  }} />{error && <span role="alert">{error}</span>}<small>脚本从标准输入读取事件 JSON。退出码 0 表示成功；2 可拒绝前置操作，或要求 Stop 继续处理。</small></Field>
}
