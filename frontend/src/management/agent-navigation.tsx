import { useEffect, useState } from 'react'
import { NavLink } from 'react-router-dom'
import { useResource } from './use-resource'
import type { FleetOverview } from './schema'

export function AgentNavigation({ tenant, owner }: { tenant: string; owner: string }) {
  const [revision, setRevision] = useState(0)
  const fleet = useResource<FleetOverview>(`/tenants/${tenant}/users/${owner}/fleet`, revision)
  useEffect(() => { const timer = window.setInterval(() => setRevision(value => value + 1), 30_000); return () => window.clearInterval(timer) }, [])
  return <section className="management-agent-navigation"><h2>我的 Agents</h2><nav aria-label="我的 Agents">{fleet.data?.agents.filter(agent => agent.status === 'active' && !agent.purging).map(agent => <NavLink key={agent.id} title={agent.name} aria-label={agent.name} to={`/t/${tenant}/agents/${agent.id}`}><span className="management-navigation-avatar" aria-hidden="true">{Array.from(agent.name).slice(0, 2).join('')}</span><span className="management-nav-label">{agent.name}</span></NavLink>)}</nav>{fleet.error ? <p>Agent 列表暂时不可用。<button onClick={() => setRevision(value => value + 1)}>重试</button></p> : !fleet.data ? <p>正在加载 Agents…</p> : fleet.data.agents.length === 0 ? <p>在 Fleet 创建第一个 Agent。</p> : null}</section>
}
