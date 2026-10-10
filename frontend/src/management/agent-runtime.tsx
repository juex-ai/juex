import { useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { Activity, ArrowLeft, Settings2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Empty, Failure, Loading, Notice, PageHeading } from './components'
import { useResource } from './use-resource'
import { extensionResources } from './extensions'
import { ThreadInspectionDialog } from './thread-inspection'
import { AgentLifecycle } from './agent-lifecycle'
import { Observers } from './observers'
import { MCPConnections } from './mcp-connections'
import { hookEvents } from './hooks'
import type { AgentDetail, EnvironmentInspection, RuntimeStatus, TenantAccess } from './schema'

const states: Record<string, string> = { idle: '就绪', queued: '排队中', running: '处理中', unconfirmed: '处理状态待确认', waiting: '等待执行结果', failed: '本轮失败', blocked: '等待处理' }
const availability: Record<string, string> = { ready: '在线', sleeping: '休眠 · 执行时唤醒', starting: '正在启动', offline: '离线', error: '环境错误' }
const kinds: Record<string, string> = { hosted: '独立容器沙箱', native: '原生目录与 Shell' }

export function AgentRuntimePage({ tenant, actor }: { tenant: TenantAccess; actor: string }) {
  const { agentId } = useParams()
  return <RuntimeContent key={`${actor}:${tenant.id}:${agentId}`} tenant={tenant.id} agent={agentId!} actor={actor} />
}

function RuntimeContent({ tenant, agent, actor }: { tenant: string; agent: string; actor: string }) {
  const base = `/tenants/${tenant}/agents/${agent}`
  const root = `/t/${tenant}/agents/${agent}`
  const [revision, setRevision] = useState(0)
  const [inspection, setInspection] = useState<string | null>(null)
  const refresh = () => setRevision(value => value + 1)
  const detail = useResource<AgentDetail>(base, revision, 10_000)
  const runtime = useResource<RuntimeStatus>(`${base}/runtime-status`, revision, 10_000)
  const execution = useResource<EnvironmentInspection>(`${base}/environment-status`, revision, 10_000)
  if (detail.error) return <Failure message={detail.error} retry={refresh} />
  if (!detail.data) return <Loading />
  const value = detail.data
  return <>
    <PageHeading title={`${value.agent.name} · 运行状态`} description="查看工作进度、代码运行位置与已保存的扩展资源。此页每 10 秒只读刷新。" actions={<><Button variant="outline" asChild><Link to={`${root}/settings`}><Settings2 />配置</Link></Button><Button variant="outline" asChild><Link to={root}><ArrowLeft />返回对话</Link></Button></>} />
    {!value.can_execute && <Notice>当前 Agent 或所属成员已停用，保留状态仅供查看。</Notice>}
    <AgentLifecycle base={base} writable={value.can_execute} changed={refresh}/>
    <div className="management-runtime-grid">
      <section className="management-panel" aria-label="Runtime 工作状态"><h2><Activity size={18} />工作状态</h2>{runtime.error ? <Failure message={runtime.error} retry={refresh} /> : !runtime.data ? <Loading /> : <>
        {!runtime.data.initialized ? <Empty title="尚未开始工作">发送消息时将初始化 Main；查看此页不会创建对话。</Empty> : <>
          <dl className="management-usage-counts"><div><dt>活跃 Threads</dt><dd>{runtime.data.active_threads}</dd></div><div><dt>已归档 Threads</dt><dd>{runtime.data.archived_threads}</dd></div><div><dt>待处理输入</dt><dd>{runtime.data.pending_inputs}</dd></div><div><dt>暂存输入</dt><dd>{runtime.data.held_inputs}</dd></div></dl>
          <p>{Object.entries(runtime.data.states).map(([state, count]) => `${states[state] ?? state} ${count}`).join(' · ')}</p>
          <p>最近活动：{runtime.data.last_activity ? new Date(runtime.data.last_activity).toLocaleString() : '尚无记录'}</p>
          {runtime.data.main_thread_id && <><div className="management-row-actions"><Button variant="outline" onClick={() => setInspection('context')}>查看 Main 工具与上下文</Button><Button variant="outline" onClick={() => setInspection('diagnostics')}>查看 Main 诊断记录</Button></div><p className="management-help">上下文显示最近一次实际模型请求。Worker 的诊断记录可在对应对话的「状态与上下文」中查看。</p></>}
        </>}
        <small>状态读取于 {new Date(runtime.data.observed_at).toLocaleString()}。Agent 的工作由共享 Runtime 执行，不对应一个独立常驻进程。</small>
      </>}</section>
      <section className="management-panel" aria-label="代码运行位置"><h2>代码在哪里运行</h2>{execution.error ? <Failure message={execution.error} retry={refresh} /> : !execution.data ? <Loading /> : <>
        {execution.data.default_state === 'unprovisioned' && <Notice>默认托管环境尚未分配，实际执行时按部署配置创建；当前没有可确认的默认运行位置。</Notice>}
        {execution.data.default_state === 'unavailable' && <Notice error>默认环境已不可用或授权已撤销。不会自动切换到另一台设备。<code>{execution.data.binding.environment_id}</code></Notice>}
        {execution.data.environments.length === 0 ? <Empty title="暂无已授权的执行环境">在配置中选择环境，或在 Fleet 配对设备。</Empty> : execution.data.environments.map(environment => <article className="management-runtime-environment" key={environment.id}>
          <h3>{environment.name}{environment.default && <span>默认</span>}</h3><p>{environment.managed ? '平台托管' : '已配对设备'} · {kinds[environment.kind] ?? environment.kind} · {environment.os} · {availability[environment.availability ?? ''] ?? (environment.online ? '在线' : '离线')}</p>
          <dl><div><dt>工作目录</dt><dd><code>{environment.working_directory}</code></dd></div><div><dt>执行权限</dt><dd>{environment.permission_mode === 'gvisor' ? 'gVisor 容器隔离' : '执行器所在操作系统用户的权限'}</dd></div><div><dt>当前能力</dt><dd>{environment.capabilities.join(' / ') || '模块已关闭，暂无可用能力'}</dd></div><div><dt>最近连接</dt><dd>{environment.last_seen ? new Date(environment.last_seen).toLocaleString() : '尚无连接记录'}</dd></div></dl>
          {environment.error && <Notice error>{environment.error}</Notice>}<details><summary>环境标识</summary><code>{environment.id}</code><p>授权版本 {environment.authorization_version}</p></details>
        </article>)}
        <small>读取于 {new Date(execution.data.observed_at).toLocaleString()}。休眠、离线和查询失败不等于可立即执行。</small>
      </>}</section>
    </div>
    <section className="management-panel" aria-label="Agent Hooks"><h2>Agent Hooks</h2><p>Agent 配置中的自动脚本。修改会影响后续执行；启用不代表已经运行。</p>
      {!value.agent.hooks?.length ? <Empty title="尚未配置 Agent Hook">可在 Agent 配置中设置触发时机、命令和执行环境。</Empty> : value.agent.hooks.map(hook => {
        const environment = execution.data?.environments.find(item => hook.environment_id ? item.id === hook.environment_id : item.default)
        const disabled = ['hooks','shell'].some(module => value.effective?.modules[module]?.enabled === false)
        return <article key={hook.id} className="management-runtime-environment"><h3>{hook.id}<span>{disabled ? 'Agent 模块已关闭' : hook.enabled ? '已启用' : '已停用'}</span></h3>
          <p>来源：{hook.source || 'Agent 配置'}</p><p>触发：{hook.events.map(event => hookEvents[event] ?? event).join('、')}</p>
          <p>执行环境：{environment?.name ?? (hook.environment_id ? '当前无法确认所选环境' : 'Agent 默认环境尚未确认')}{hook.environment_id && <> · <code>{hook.environment_id}</code></>}</p>
          <p>工作目录：<code>{hook.working_directory || environment?.working_directory || '使用执行时的环境默认目录'}</code></p>
          <details><summary>命令及执行规则</summary><pre>{JSON.stringify(hook.command, null, 2)}</pre><p>{hook.required ? '必须成功' : '非阻断'} · 超时 {hook.timeout_seconds || 10} 秒</p>{hook.tools?.length ? <p>限定工具：{hook.tools.join('、')}</p> : null}</details>
        </article>
      })}
      <Button variant="outline" asChild><Link to={`${root}/settings`}>配置 Agent Hooks</Link></Button>
    </section>
    <section className="management-panel management-runtime-resources" aria-label="已保存的扩展资源"><h2>扩展资源与运行位置</h2><p>下表是已保存的配置；启用资源不代表它已启动。MCP、Observable 和命令 Hook 在绑定的环境与目录执行；Skill 文本进入 Runtime 的模型上下文。Shell 使用执行环境的 /bin/sh。</p>
      {!value.agent.extensions?.length ? <Empty title="尚未配置扩展">打开配置，读取扩展目录并选择 Skills、MCP、Observable 或 Hooks。</Empty> : value.agent.extensions.map(binding => <article key={binding.id} className="management-runtime-environment"><h3>{binding.catalog.manifest.name} · {binding.catalog.manifest.version}<span>{binding.enabled ? '已启用' : '已停用'}</span></h3><p>{execution.data?.environments.find(environment => environment.id === binding.environment_id)?.name ?? '绑定环境'} · <code>{binding.environment_id}</code></p><p>扩展目录 <code>{binding.directory}</code></p><ul>{extensionResources(binding.catalog.manifest).filter(resource => binding.resources.includes(resource.id)).map(resource => {
        const kind = resource.id.split('/')[0]
        const required = kind === 'observable' ? ['extensions','observations','shell'] : kind === 'skill' ? ['skills'] : kind === 'hook' ? ['extensions','hooks','shell'] : ['extensions','mcp']
        const disabled = required.some(module => value.effective?.modules[module]?.enabled === false)
        return <li key={resource.id}><strong>{resource.label}</strong> · {disabled ? 'Agent 模块已关闭' : binding.enabled ? '已选择' : '扩展已停用'}{resource.description && <p>{resource.description}</p>}</li>
      })}</ul><details><summary>配置标识与版本</summary><code>{binding.id}</code><p>目录版本 <code>{binding.catalog.revision}</code></p></details></article>)}
      <Button variant="outline" asChild><Link to={`${root}/settings`}>配置环境和扩展</Link></Button>
    </section>
    <MCPConnections base={base} actor={actor} />
 <Observers base={base} actor={actor} detail={value}/>
    {inspection && runtime.data?.main_thread_id && <ThreadInspectionDialog base={base} thread={runtime.data.main_thread_id} initialTab={inspection} close={() => setInspection(null)} />}
  </>
}
