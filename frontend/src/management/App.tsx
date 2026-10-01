import { useEffect, useState } from 'react'
import { BrowserRouter, Link, Navigate, NavLink, Route, Routes, useLocation, useNavigate, useParams } from 'react-router-dom'
import { Layers3, LogOut, Users, ShieldCheck, BookOpen, CalendarDays, ChartColumn } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { APIError, api, errorText } from './api'
import { AuthPage } from './auth'
import { Empty, Failure, Loading, Notice } from './components'
import { FleetPage } from './fleet'
import { MemoryPage } from './memory'
import { CalendarPage } from './calendar'
import { NotificationNav, NotificationsPage } from './notifications'
import { ConversationPage } from './conversation'
import { DevicePairPage } from './devices'
import { UsersPage } from './users'
import { UsagePage } from './usage'
import { useResource } from './use-resource'
import type { TenantAccess, User } from './schema'
import './management.css'

export default function ManagementApp() {
  return <BrowserRouter><SessionRoot /></BrowserRouter>
}

function SessionRoot() {
	const location = useLocation()
  const [user, setUser] = useState<User | null | undefined>(undefined)
  const [mailEnabled, setMailEnabled] = useState(false)
  const [error, setError] = useState('')
  const [revision, setRevision] = useState(0)
  const navigate = useNavigate()
  useEffect(() => {
    const controller = new AbortController()
    Promise.all([
      api<User>('/auth/session', undefined, undefined, controller.signal).catch(error => { if (error instanceof APIError && error.status === 401) return null; throw error }),
      api<{ email_enabled: boolean }>('/config', undefined, undefined, controller.signal),
    ]).then(([session, config]) => { setUser(session); setMailEnabled(config.email_enabled); setError('') })
      .catch(error => { if (!controller.signal.aborted) setError(errorText(error)) })
    return () => controller.abort()
  }, [revision])
  const signedIn = (value: User | null) => { setUser(value); setRevision(value => value + 1) }
  async function logout() {
    try { await api('/auth/logout', {}) } catch (err) { if (!(err instanceof APIError && err.status === 401)) { setError(errorText(err)); return } }
    setUser(null); navigate('/login')
  }
  if (error) return <main className="management-start"><Failure message={error} retry={() => setRevision(value => value + 1)} /></main>
  if (user === undefined) return <main className="management-start"><Loading /></main>
  const auth = <AuthPage user={user} mailEnabled={mailEnabled} onSession={signedIn} />
  return <Routes>
    <Route path="/login" element={user ? <Navigate to="/" replace /> : auth} />
    <Route path="/join" element={auth} /><Route path="/set-password" element={auth} /><Route path="/verify-email" element={auth} /><Route path="/recover" element={auth} />
    <Route path="*" element={user ? <Workspace key={`${user.id}:${revision}`} user={user} mailEnabled={mailEnabled} logout={logout} /> : location.pathname.startsWith('/pair/') ? auth : <Navigate to="/login" replace />} />
  </Routes>
}

function Workspace({ user, mailEnabled, logout }: { user: User; mailEnabled: boolean; logout: () => Promise<void> }) {
  const [revision, setRevision] = useState(0)
  const tenants = useResource<TenantAccess[]>('/tenants', revision)
  const refresh = () => setRevision(value => value + 1)
  useEffect(() => { const timer = window.setInterval(refresh, 60_000); return () => window.clearInterval(timer) }, [])
  if (tenants.error) return <main className="management-start"><Failure message={tenants.error} retry={refresh} /><Button variant="outline" onClick={() => void logout()}>退出登录</Button></main>
  if (!tenants.data) return <main className="management-start"><Loading /></main>
  if (!tenants.data.length) return <main className="management-start"><Empty title="暂时没有可访问的租户">请联系租户管理员确认成员状态，或打开新的邀请链接。</Empty><Button onClick={() => void logout()}>退出登录</Button></main>
  return <Routes><Route path="/pair/:pairId" element={<DevicePairPage tenants={tenants.data} user={user} />} /><Route path="/t/:tenantId/*" element={<TenantShell user={user} tenants={tenants.data} mailEnabled={mailEnabled} logout={logout} refresh={refresh} />} /><Route path="*" element={<Navigate to={`/t/${tenants.data[0].id}/fleet`} replace />} /></Routes>
}

function TenantShell({ user, tenants, mailEnabled, logout, refresh }: { user: User; tenants: TenantAccess[]; mailEnabled: boolean; logout: () => Promise<void>; refresh: () => void }) {
  const { tenantId } = useParams()
  const tenant = tenants.find(value => value.id === tenantId)
  const navigate = useNavigate()
  const [notice, setNotice] = useState('')
  if (!tenant) return <Navigate to="/" replace />
  return <div className="management-shell">
    <aside className="management-sidebar"><Link to={`/t/${tenant.id}/fleet`} className="management-wordmark">JueX<span>Managed Agents</span></Link>
      {tenants.length > 1 ? <label className="management-tenant"><span>当前租户</span><select aria-label="切换租户" value={tenant.id} onChange={event => navigate(`/t/${event.target.value}/fleet`)}>{tenants.map(value => <option key={value.id} value={value.id}>{value.name}</option>)}</select></label> : <div className="management-tenant"><span>工作空间</span><strong>{tenant.name}</strong></div>}
      <nav aria-label="管理导航"><NavLink to={`/t/${tenant.id}/fleet`}><Layers3 size={18} />我的 Fleet</NavLink><NavLink to={`/t/${tenant.id}/memory`}><BookOpen size={18} />Memory</NavLink><NavLink to={`/t/${tenant.id}/calendar`}><CalendarDays size={18} />Calendar</NavLink><NavLink to={`/t/${tenant.id}/usage`}><ChartColumn size={18} />Token 用量</NavLink><NotificationNav key={tenant.id} tenant={tenant} />{tenant.role === 'admin' && <NavLink to={`/t/${tenant.id}/users`}><Users size={18} />用户管理</NavLink>}</nav>
      <div className="management-profile"><span className="management-profile-role"><ShieldCheck size={14} />{tenant.role === 'admin' ? '租户管理员' : '普通用户'}</span><strong title={user.email}>{user.email}</strong><Button variant="ghost" onClick={() => void logout()}><LogOut size={15} />退出登录</Button></div>
    </aside>
    <main className="management-main"><div className="management-topline"><span>{tenant.name}</span><span>Management Dashboard</span></div><div className="management-content">
      {!user.email_verified && mailEnabled && <Notice>邮箱尚未验证。<Button variant="link" onClick={() => { void api('/auth/request-verification', {}).then(() => setNotice('验证邮件已进入发送队列。')).catch(error => setNotice(errorText(error))) }}>发送验证邮件</Button></Notice>}{notice && <Notice>{notice}</Notice>}
      <Routes><Route path="usage" element={<UsagePage key={tenant.id} tenant={tenant} user={user} />} /><Route path="users/:ownerId/usage" element={tenant.role === 'admin' ? <UsagePage key={tenant.id} tenant={tenant} user={user} delegated /> : <Navigate to={`/t/${tenant.id}/fleet`} replace />} /><Route path="calendar" element={<CalendarPage key={`${tenant.id}:${user.id}`} tenant={tenant} user={user} />} /><Route path="users/:ownerId/calendar" element={tenant.role === 'admin' ? <CalendarPage key={tenant.id} tenant={tenant} user={user} delegated /> : <Navigate to={`/t/${tenant.id}/fleet`} replace />} /><Route path="notifications" element={<NotificationsPage key={tenant.id} tenant={tenant} user={user} mailEnabled={mailEnabled} />} /><Route path="memory" element={<MemoryPage key={`${tenant.id}:${user.id}`} tenant={tenant} user={user} />} /><Route path="users/:ownerId/memory" element={tenant.role === 'admin' ? <MemoryPage key={tenant.id} tenant={tenant} user={user} delegated /> : <Navigate to={`/t/${tenant.id}/fleet`} replace />} /><Route path="fleet" element={<FleetPage key={tenant.id} tenant={tenant} user={user} />} /><Route path="agents/:agentId" element={<ConversationPage key={tenant.id} tenant={tenant} user={user} />} /><Route path="users" element={tenant.role === 'admin' ? <UsersPage key={tenant.id} tenant={tenant} onAccessChanged={refresh} /> : <Navigate to={`/t/${tenant.id}/fleet`} replace />} /><Route path="users/:ownerId/*" element={tenant.role === 'admin' ? <FleetPage key={tenant.id} tenant={tenant} user={user} delegated /> : <Navigate to={`/t/${tenant.id}/fleet`} replace />} /><Route path="*" element={<Navigate to={`/t/${tenant.id}/fleet`} replace />} /></Routes>
    </div></main>
  </div>
}
