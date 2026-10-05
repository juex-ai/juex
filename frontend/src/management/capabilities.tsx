import type { Policy } from './schema'

const capabilities = [
  ['files', '文件读写与传输'],
  ['shell', 'Shell 命令'],
  ['workers', 'Worker 委派'],
  ['collaboration', 'Agent 间协作'],
  ['mcp', 'MCP 工具'],
  ['observations', 'Observable 观察与唤醒'],
  ['memory', 'Memory 读写与学习'],
  ['calendar', 'Calendar 日程'],
  ['hooks', 'Hooks 自动脚本'],
  ['extensions', '扩展与 Skills'],
] as const

export function CapabilitiesEditor({ value, onChange }: { value: Policy; onChange: (value: Policy) => void }) {
  return <fieldset className="management-hook-settings"><legend>Agent 能力</legend>
    <p>关闭能力会撤销此 Agent 旧授权下未完成的工作，重新开启不会自动恢复。历史数据保留。</p>
    <div className="management-hook-events">{capabilities.map(([id, label]) => <label key={id} className="management-checkbox">
      <input type="checkbox" checked={!value.disabled.includes(id)} onChange={event => onChange({ disabled: event.target.checked ? value.disabled.filter(capability => capability !== id) : [...value.disabled, id].sort() })} />{label}
    </label>)}</div>
    <small>扩展资源还需开启对应的 Shell、MCP 或 Observable 能力。Host 的 Shell 使用所属系统用户的权限，独立目录不提供系统隔离。</small>
  </fieldset>
}
