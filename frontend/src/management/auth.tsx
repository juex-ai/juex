import { useEffect, useState, type FormEvent } from 'react'
import { Link, useLocation, useNavigate } from 'react-router-dom'
import { ArrowRight } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { api, errorText } from './api'
import { Field, Loading, Notice } from './components'
import type { InvitationPreview, Session, User } from './schema'

export function AuthPage({ user, mailEnabled, onSession }: { user: User | null; mailEnabled: boolean; onSession: (user: User | null) => void }) {
  const location = useLocation()
  const navigate = useNavigate()
  const token = new URLSearchParams(location.hash.slice(1)).get('token') ?? ''
  const mode = location.pathname === '/join' ? 'join' : location.pathname === '/set-password' ? 'password' : location.pathname === '/verify-email' ? 'verify' : location.pathname === '/recover' ? 'recover' : 'login'
  const [preview, setPreview] = useState<InvitationPreview | null>(null)
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  useEffect(() => {
    setError(''); setNotice((location.state as { notice?: string } | null)?.notice ?? ''); setPassword('')
    if (mode !== 'join') return
    const controller = new AbortController()
    setPreview(null)
    api<InvitationPreview>('/auth/invitation', { token }, undefined, controller.signal)
      .then(value => { setPreview(value); setEmail(value.email) })
      .catch(err => { if (!controller.signal.aborted) setError(errorText(err)) })
    return () => controller.abort()
  }, [mode, token, location.state])

  async function submit(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError(''); setNotice('')
    try {
      if (mode === 'password') {
        await api('/auth/set-password', { token, password })
        setPassword(''); onSession(null)
        navigate('/login', { replace: true, state: { notice: '密码已设置，请使用邮箱和新密码登录。' } })
        return
      }
      if (mode === 'recover') {
        await api('/auth/recovery', { email })
        setNotice('如果此邮箱已通过验证，我们会发送密码恢复邮件。')
        return
      }
      if (mode === 'verify') {
        await api('/auth/verify-email', { token })
        setNotice('邮箱已验证。')
        navigate(user ? '/' : '/login', { replace: true })
        if (user) onSession(await api<User>('/auth/session'))
        return
      }
      if (mode === 'join' && user) {
        await api('/auth/accept-invitation', { token })
        onSession(user); navigate('/', { replace: true }); return
      }
      const session = await api<Session>(mode === 'join' && !preview?.requires_login ? '/auth/register-invitation' : '/auth/login', mode === 'join' && !preview?.requires_login ? { token, password } : { email, password })
      onSession(session.user)
      if (mode === 'join') {
        if (preview?.requires_login) await api('/auth/accept-invitation', { token })
        navigate('/', { replace: true })
      } else navigate(location.pathname.startsWith('/pair/') ? location.pathname : '/', { replace: true })
    } catch (err) { setError(errorText(err)) } finally { setBusy(false) }
  }

  const titles = { login: '欢迎回来', join: '加入你的团队', password: '设置账号密码', recover: '找回密码', verify: '验证邮箱' }
  const submitText = mode === 'join' ? user ? '接受邀请' : preview?.requires_login ? '登录并接受邀请' : '创建账号并加入' : mode === 'password' ? '保存新密码' : mode === 'verify' ? '确认验证邮箱' : mode === 'recover' ? '发送恢复邮件' : '登录'
  const validUser = !preview || !user || preview.email === user.email
  return <main className="management-auth">
    <section className="management-auth-story"><Link to="/" className="management-wordmark">JueX<span>Managed Agents</span></Link><div><div className="management-eyebrow">一个 Fleet，无限可能</div><h1>让你的 Agents<br />持续为你工作。</h1><p>对话、记忆、日程和执行环境，在一个清晰的工作空间中连接。</p></div><small>你的工作，持续前行。</small></section>
    <section className="management-auth-form"><div className="management-auth-card"><div className="management-eyebrow">Management</div><h1>{titles[mode]}</h1>
      <p>{mode === 'login' ? '使用受邀邮箱登录你的工作空间。' : mode === 'join' && preview ? `${preview.tenant_name} 邀请 ${preview.email} 以${preview.role === 'admin' ? '管理员' : '普通用户'}身份加入。` : mode === 'password' ? '设置至少 12 个字符的密码。' : mode === 'verify' ? '确认你拥有此邮箱，以接收通知并使用邮件恢复账号。' : '使用已经验证的邮箱恢复账号。'}</p>
      {error && <Notice error>{error}</Notice>}{notice && <Notice>{notice}</Notice>}
      {mode === 'join' && !preview ? !error && <Loading /> : <form onSubmit={submit}>
        {(mode === 'login' || mode === 'recover' || (mode === 'join' && !user)) && <Field label="邮箱"><Input type="email" required autoComplete="username" value={email} readOnly={mode === 'join'} onChange={e => setEmail(e.target.value)} /></Field>}
        {(mode === 'login' || mode === 'password' || (mode === 'join' && !user)) && <Field label="密码" hint={mode === 'password' || (mode === 'join' && !preview?.requires_login) ? '至少 12 个字符，建议使用密码管理器生成。' : undefined}><Input type="password" required minLength={mode === 'login' || preview?.requires_login ? undefined : 12} maxLength={1024} autoComplete={mode === 'login' || preview?.requires_login ? 'current-password' : 'new-password'} value={password} onChange={e => setPassword(e.target.value)} /></Field>}
        {mode === 'join' && user && <Notice>当前登录：{user.email}{!validUser && '。请退出当前账号，使用邀请中的邮箱登录。'}</Notice>}
        {mode === 'recover' && !mailEnabled ? <Notice>此部署未配置邮件服务，请联系部署管理员获取一次性账号恢复链接。</Notice> : <Button type="submit" size="lg" disabled={busy || !validUser || (mode !== 'login' && mode !== 'recover' && !token)} className="management-primary-action">{busy ? '正在处理…' : submitText}<ArrowRight aria-hidden="true" size={16} /></Button>}
      </form>}
      <div className="management-auth-links">{mode === 'login' ? <Link to="/recover">忘记密码</Link> : <Link to={user ? '/' : '/login'}>{user ? '返回工作空间' : '返回登录'}</Link>}{mode === 'join' && user && !validUser && <Button variant="link" onClick={() => { void api('/auth/logout', {}).then(() => onSession(null)).catch(err => setError(errorText(err))) }}>退出账号</Button>}</div>
      {mode === 'login' && <small className="management-muted">仅限受邀用户。请联系租户管理员获取邀请链接。</small>}
    </div></section>
  </main>
}
