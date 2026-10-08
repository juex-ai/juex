import { Input } from '@/components/ui/input'
import { Field } from './components'
import type { DynamicInstructions } from './schema'

export function InstructionsEditor({ value, onChange }: { value: DynamicInstructions; onChange: (value: DynamicInstructions) => void }) {
  return <fieldset className="management-form">
    <legend>工作环境中的指令</legend>
    <label className="management-checkbox"><input type="checkbox" checked={value.enabled} onChange={event => onChange({ ...value, enabled: event.target.checked })} />读取 AGENTS.md</label>
    <p className="text-sm text-muted-foreground">每次新的模型请求依次读取全局指令文件、工作目录的 AGENTS.md 和 .agents/AGENTS.md，接在专属指令之后。所有路径均属于选定的执行环境，需要文件访问权限。</p>
    <Field label="全局指令文件（可选）"><Input disabled={!value.enabled} value={value.global_path} maxLength={4096} placeholder="执行环境中的绝对路径，留空则只读取工作目录" onChange={event => onChange({ ...value, global_path: event.target.value })} /></Field>
    <p className="text-sm text-muted-foreground">文件内容不会授予额外权限。关闭或更换已启用的来源会停止旧授权下尚未完成的工作。</p>
  </fieldset>
}
