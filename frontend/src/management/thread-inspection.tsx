import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { MessageResponse } from '@/components/ai-elements/message'
import { Empty, Failure, Loading, Notice } from './components'
import { useResource } from './use-resource'
import { ThreadDiagnostics } from './thread-diagnostics'
import type { ThreadInspection } from './schema'

const statuses: Record<string, string> = { todo: '待开始', doing: '进行中', done: '已完成', pending: '等待条件', failed: '失败' }

export function ThreadInspectionDialog({ base, thread, initialTab = 'notes', close }: { base: string; thread: string; initialTab?: string; close: () => void }) {
  const [revision, setRevision] = useState(0)
  const refresh = () => setRevision(value => value + 1)
  const resource = useResource<ThreadInspection>(`${base}/threads/${thread}/inspection`, revision, 5000)
  const value = resource.data
  const request = value?.latest_request
  return <Sheet open onOpenChange={open => { if (!open) close() }}><SheetContent className="management-inspection-dialog"><SheetHeader><SheetTitle>状态与上下文{value ? ` · ${value.thread.name}` : ''}</SheetTitle><SheetDescription>查看当前输入清单、Notes、Tasks，以及本代最近一次模型请求的上下文。数据每 5 秒更新。</SheetDescription></SheetHeader>
    {resource.error ? <Failure message={resource.error} retry={refresh} /> : !value ? <Loading /> : <Tabs defaultValue={initialTab}>
      <TabsList><TabsTrigger value="notes">Notes</TabsTrigger><TabsTrigger value="tasks">Tasks · {value.state.tasks.length}</TabsTrigger><TabsTrigger value="inputs">输入清单</TabsTrigger><TabsTrigger value="context">上下文</TabsTrigger><TabsTrigger value="usage">用量</TabsTrigger><TabsTrigger value="diagnostics">诊断记录</TabsTrigger></TabsList>
      <TabsContent value="diagnostics"><ThreadDiagnostics key={thread} base={base} thread={thread} /></TabsContent>
      <TabsContent value="notes">{value.capabilities.disabled.includes('notes') && <Notice>Notes 已停用；内容保留，但不会进入后续模型请求。</Notice>}{value.state.notes.content ? <div className="management-inspection-content"><MessageResponse>{value.state.notes.content}</MessageResponse></div> : <Empty title="暂无 Notes">Agent 会在工作中记录需要持续保留的上下文。</Empty>}</TabsContent>
      <TabsContent value="tasks">{value.capabilities.disabled.includes('tasks') && <Notice>Tasks 已停用；记录保留，当前不参与完成门禁。</Notice>}<p className="management-help">待开始和进行中的 Tasks 会阻止正常结束；等待条件或失败的 Tasks 会保留，但不阻止结束。</p>{value.state.tasks.length ? <ol className="management-task-state">{value.state.tasks.map(task => <li key={task.id}><div><strong>{task.title}</strong><span>{statuses[task.status] ?? task.status}</span></div>{task.description && <p>{task.description}</p>}{task.acceptance && <p><b>完成条件：</b>{task.acceptance}</p>}{task.status_reason && <p><b>状态原因：</b>{task.status_reason}</p>}<small>优先级：{task.priority} · 持续推进 {task.continuation_count} 次</small></li>)}</ol> : <Empty title="暂无 Tasks">此对话当前没有工作任务。</Empty>}</TabsContent>
      <TabsContent value="inputs"><p className="management-help">只显示当前工作范围内尚未确认的直接输入。回复结束不会自动勾选；Agent 确认是模型判断，不等于执行成功。新上下文结束原范围，历史标记保留。</p>{!value.input_checklist?.enabled ? <Notice>输入清单已停用；已有记录保留，历史消息仍可查看处理标记。</Notice> : value.input_checklist.items.length ? <ol className="management-task-state">{value.input_checklist.items.map(item => <li key={item.input_id}><div><code>{item.input_id}</code><span>{item.delivery === 'delivered' ? '待处理' : item.delivery === 'blocked' ? '未交付模型' : '等待交付模型'}</span></div></li>)}</ol> : <Empty title="当前没有待确认输入">新输入交付给 Agent 后，会进入处理清单。</Empty>}</TabsContent><TabsContent value="context">{request ? <>
        <p>{request.provider} / {request.model} · 第 {request.generation} 代 · {request.purpose === 'compaction' ? '压缩请求' : '模型请求'}</p><small>请求记录：{new Date(request.recorded_at).toLocaleString()} · 后续 Notes 或配置变化可能尚未进入模型请求。</small>
        <div className="management-context-budget"><strong>输入估算 {request.estimated_tokens.toLocaleString()} / {request.context_window.toLocaleString()} tokens</strong><progress aria-label="估算上下文占用" value={request.estimated_tokens} max={request.context_window || 1} /><span>另预留输出 {request.output_reserve.toLocaleString()} tokens；估算不是模型供应方账单用量。</span><dl>{request.breakdown.map(part => <div key={part.key}><dt>{part.label}</dt><dd>{part.tokens.toLocaleString()}</dd></div>)}</dl></div>
        <details className="management-tool-row"><summary>System 指令</summary><pre>{request.system}</pre></details><details className="management-tool-row"><summary>工具定义 · {request.tools.length}</summary>{request.tools.map(tool => <details key={tool.name}><summary>{tool.name}</summary><p>{tool.description}</p><pre>{JSON.stringify(tool.schema, null, 2)}</pre></details>)}</details><p>本次请求包含 {request.message_count} 条上下文消息。</p>
      </> : <Empty title="本代尚无模型请求">发送消息后，将显示实际冻结的请求内容；旧代记录不会冒充当前上下文。</Empty>}</TabsContent>
      <TabsContent value="usage"><p>此 Thread 的模型调用累计，包括压缩。缓存输入属于输入 token 的一部分。</p>{value.imported_history && <Notice>包含迁入历史；历史消息没有可核验的模型调用账单，下面只统计迁入后的请求。</Notice>}<dl className="management-usage-counts"><div><dt>输入 tokens（已报告）</dt><dd>{value.usage.input_tokens.toLocaleString()}</dd></div><div><dt>输出 tokens（已报告）</dt><dd>{value.usage.output_tokens.toLocaleString()}</dd></div><div><dt>缓存输入 tokens</dt><dd>{value.usage.cached_input_tokens.toLocaleString()}</dd></div><div><dt>调用次数</dt><dd>{value.usage.attempts}</dd></div></dl><p>完整报告 {value.usage.reported} 次 · 部分报告 {value.usage.partial} 次 · 未知 {value.usage.unknown} 次</p>{value.usage.partial + value.usage.unknown > 0 && <Notice>部分调用没有完整用量；已知总量不代表全部消耗。</Notice>}</TabsContent>
    </Tabs>}
    <div className="management-inspection-footer"><small>{value ? `第 ${value.thread.generation} 代 · 状态版本 ${value.state.revision}` : ''}</small><Button variant="outline" onClick={refresh}>刷新</Button></div>
  </SheetContent></Sheet>
}
