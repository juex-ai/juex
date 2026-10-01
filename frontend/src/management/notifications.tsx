import { useEffect, useState } from 'react'
import { Link, NavLink } from 'react-router-dom'
import { Bell, RefreshCw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { api, errorText } from './api'
import { Empty, Failure, Loading, Notice, PageHeading } from './components'
import { useResource } from './use-resource'
import type { NotificationPage as InboxPage, NotificationPreferences, TenantAccess, User } from './schema'

function useNotificationRevision() {
  const [revision, setRevision] = useState(0)
  useEffect(() => {
    const refresh = () => setRevision(value => value + 1)
    const timer = window.setInterval(refresh, 60_000)
    window.addEventListener('juex-notifications-updated', refresh)
    return () => { window.clearInterval(timer); window.removeEventListener('juex-notifications-updated', refresh) }
  }, [])
  return revision
}
function refreshNotifications() { window.dispatchEvent(new Event('juex-notifications-updated')) }

export function NotificationNav({ tenant }: { tenant: TenantAccess }) {
  const revision = useNotificationRevision()
  const inbox = useResource<InboxPage>(`/tenants/${tenant.id}/notifications`, revision)
  return <NavLink to={`/t/${tenant.id}/notifications`}><Bell size={18} />通知{inbox.data && inbox.data.unread > 0 && <span className="management-unread-count" aria-label={`${inbox.data.unread} 条未读通知`}>{inbox.data.unread > 99 ? '99+' : inbox.data.unread}</span>}</NavLink>
}

export function NotificationsPage({ tenant, user, mailEnabled }: { tenant: TenantAccess; user: User; mailEnabled: boolean }) {
  const revision = useNotificationRevision()
  const [cursors, setCursors] = useState([0])
  const [settingsRevision, setSettingsRevision] = useState(0)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const base = `/tenants/${tenant.id}`
  const inbox = useResource<InboxPage>(`${base}/notifications?before=${cursors[cursors.length - 1]}`, revision)
  const preferences = useResource<NotificationPreferences>(`${base}/notification-preferences`, settingsRevision)
  async function configure(patch: Partial<NotificationPreferences>) {
    if (!preferences.data) return
    setBusy(true); setError('')
    try { await api(`${base}/notification-preferences`, { ...preferences.data, ...patch }, 'PUT'); setSettingsRevision(value => value + 1) }
    catch (err) { setError(errorText(err)); setSettingsRevision(value => value + 1) } finally { setBusy(false) }
  }
  async function mark(id: string, read: boolean) {
    setBusy(true); setError('')
    try { await api(`${base}/notifications/${id}`, { read }, 'PUT'); refreshNotifications() }
    catch (err) { setError(errorText(err)) } finally { setBusy(false) }
  }
  return <>
    <PageHeading title="通知" description="提醒、需要处理的等待与失败会保存在这里。" actions={<Button variant="outline" onClick={refreshNotifications}><RefreshCw />刷新</Button>} />
    {error && <Notice error>{error}</Notice>}
    <section className="management-panel management-notification-preferences" aria-label="通知设置">
      <h2>通知设置</h2>
      {preferences.error ? <Failure message={preferences.error} retry={() => setSettingsRevision(value => value + 1)} /> : !preferences.data ? <Loading /> : <>
        <label><input type="checkbox" checked={preferences.data.completions} disabled={busy} onChange={event => void configure({ completions: event.target.checked })} />也接收任务完成通知</label>
        <label><input type="checkbox" checked={preferences.data.email} disabled={busy || (!preferences.data.email && (!mailEnabled || !user.email_verified))} onChange={event => void configure({ email: event.target.checked })} />同时发送邮件提醒</label>
        <p>{!mailEnabled ? '此部署尚未配置邮件，站内通知正常可用。' : !user.email_verified ? '验证邮箱后可开启邮件提醒。' : '邮件只提供站内入口，登录后查看详情。'}设置仅影响之后产生的通知。</p>
      </>}
    </section>
    <section className="management-panel" aria-label="通知列表">
      <div className="management-panel-heading"><h2>{inbox.data ? `${inbox.data.unread} 条未读` : '最近通知'}</h2></div>
      {inbox.error ? <Failure message={inbox.error} retry={refreshNotifications} /> : !inbox.data ? <Loading /> : <>
        {inbox.data.items.length === 0 ? <Empty title="暂无通知">关闭网页后 Agent 仍会运行，之后的提醒会保存在这里。</Empty> : <div className="management-agents">{inbox.data.items.map(item => <article className={`management-agent-row management-notification-row ${item.read ? '' : 'is-unread'}`} key={item.id}>
          <div className="management-agent-info"><h2>{!item.read && <span className="management-notification-dot" aria-label="未读" />}{item.title}</h2><p>{item.summary}</p><small>{item.application} · {new Date(item.created_at).toLocaleString()}</small></div>
          <div className="management-row-actions"><Button variant="ghost" asChild><Link to={item.application === 'memory' ? `/t/${tenant.id}/memory` : item.application === 'calendar' ? `/t/${tenant.id}/calendar` : `/t/${tenant.id}/agents/${item.agent_id}?thread=${encodeURIComponent(item.resource_id)}`}>查看详情</Link></Button><Button variant="outline" disabled={busy} onClick={() => void mark(item.id, !item.read)}>{item.read ? '标为未读' : '标为已读'}</Button></div>
        </article>)}</div>}
        <div className="management-memory-pagination"><Button variant="ghost" disabled={cursors.length === 1} onClick={() => setCursors(value => value.slice(0, -1))}>上一页</Button><span>第 {cursors.length} 页</span><Button variant="ghost" disabled={!inbox.data.next} onClick={() => setCursors(value => [...value, inbox.data!.next])}>下一页</Button></div>
      </>}
    </section>
  </>
}
