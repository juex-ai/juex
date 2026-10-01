import { useState, type FormEvent } from 'react'
import { Link } from 'react-router-dom'
import { Copy, Plus, ArrowUpRight } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { writeClipboardText } from '@/lib/clipboard'
import { api, errorText } from './api'
import { Empty, Failure, Field, Loading, Notice, PageHeading, Status } from './components'
import { useResource } from './use-resource'
import type { InvitationView, MembershipStatus, MemberView, Role, TenantAccess } from './schema'

function deliveryText(status: string): string {
  const labels: Record<string, string> = { manual: '请手动转交链接', queued: '邮件排队中', sent: '邮件已发送', retrying: '邮件发送失败，正在重试', failed: '邮件发送失败，请手动转交链接' }
  return labels[status] ?? '邮件发送状态未知'
}

export function UsersPage({ tenant, onAccessChanged }: { tenant: TenantAccess; onAccessChanged: () => void }) {
  const [revision, setRevision] = useState(0)
  const refresh = () => setRevision(value => value + 1)
  const members = useResource<MemberView[]>(`/tenants/${tenant.id}/members`, revision)
  const invitations = useResource<InvitationView[]>(`/tenants/${tenant.id}/invitations`, revision)
  const [inviteOpen, setInviteOpen] = useState(false)
  const [email, setEmail] = useState('')
  const [role, setRole] = useState<Role>('member')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState(false)
  const [change, setChange] = useState<{ member: MemberView; role: Role; status: MembershipStatus } | null>(null)

  async function invite(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError(''); setNotice('')
    try {
      await api(`/tenants/${tenant.id}/invitations`, { email, role })
      setInviteOpen(false); setEmail(''); refresh(); setNotice('邀请已创建，可以复制链接转交给受邀用户。')
    } catch (err) { setError(errorText(err)) } finally { setBusy(false) }
  }
  async function changeMember() {
    if (!change) return
    setBusy(true); setError(''); setNotice('')
    try {
      await api(`/tenants/${tenant.id}/members/${change.member.user_id}`, { role: change.role, status: change.status }, 'PATCH')
      setChange(null); refresh(); onAccessChanged(); setNotice('成员信息已更新。')
    } catch (err) { setError(errorText(err)) } finally { setBusy(false) }
  }
  async function copy(link: string) {
    try { await writeClipboardText(link); setNotice('邀请链接已复制。') } catch { setError('复制失败，请选择链接后手动复制。') }
  }
  return <>
    <PageHeading title="用户" description="管理本租户的成员，以及他们的 Fleet 和 Agents。" actions={<Button onClick={() => { setError(''); setInviteOpen(true) }}><Plus />邀请用户</Button>} />
    {error && !inviteOpen && !change && <Notice error>{error}</Notice>}{notice && <Notice>{notice}</Notice>}
    {members.error ? <Failure message={members.error} retry={refresh} /> : !members.data ? <Loading /> : <section className="management-panel">
      <div className="management-panel-heading"><h2>租户成员</h2><span>{members.data.length} 位</span></div>
      <div className="management-table-wrap"><table className="management-table"><thead><tr><th>用户</th><th>角色</th><th>状态</th><th>操作</th></tr></thead><tbody>{members.data.map(member => <tr key={member.id}>
        <td><Link className="management-row-link" to={`/t/${tenant.id}/users/${member.user_id}`}>{member.email}<ArrowUpRight size={14} /></Link><small>{member.email_verified ? '邮箱已验证' : '邮箱未验证'}</small></td>
        <td><Status status={member.role} /></td><td><Status status={member.status} /></td>
        <td><div className="management-row-actions"><Button variant="ghost" size="sm" asChild><Link to={`/t/${tenant.id}/users/${member.user_id}/usage`}>查看用量</Link></Button><Button variant="ghost" size="sm" asChild><Link to={`/t/${tenant.id}/users/${member.user_id}`}>{member.fleet_id ? '查看 Fleet' : '查看清理记录'}</Link></Button>{member.status !== 'removed' && <>
          <Button variant="ghost" size="sm" onClick={() => { setError(''); setChange({ member, role: member.role === 'admin' ? 'member' : 'admin', status: member.status }) }}>{member.role === 'admin' ? '设为普通用户' : '设为管理员'}</Button>
          <Button variant="ghost" size="sm" onClick={() => { setError(''); setChange({ member, role: member.role, status: member.status === 'suspended' ? 'active' : 'suspended' }) }}>{member.status === 'suspended' ? '重新启用' : '停用'}</Button>
          <Button variant="destructive" size="sm" onClick={() => { setError(''); setChange({ member, role: member.role, status: 'removed' }) }}>移除</Button>
        </>}</div></td>
      </tr>)}</tbody></table></div>
    </section>}
    <section className="management-panel"><div className="management-panel-heading"><h2>等待加入</h2><span>{invitations.data?.length ?? '—'} 份有效邀请</span></div>
      {invitations.error ? <Failure message={invitations.error} retry={refresh} /> : !invitations.data ? <Loading /> : !invitations.data.length ? <Empty title="暂无待接受的邀请">邀请用户后，链接和有效期会显示在这里。</Empty> : <div className="management-invites">{invitations.data.map(invitation => <article key={invitation.id} className="management-invite"><div className="management-invite-header"><strong>{invitation.email}</strong><Status status="invited" /><Status status={invitation.role} /></div><div className="management-link-copy"><Input aria-label={`${invitation.email} 的邀请链接`} readOnly value={invitation.link} onFocus={event => event.target.select()} /><Button variant="outline" onClick={() => void copy(invitation.link)}><Copy />复制链接</Button></div><small>有效期至 {new Date(invitation.expires_at).toLocaleString()} · {deliveryText(invitation.delivery_status)} · 邀请不代表邮箱已验证</small></article>)}</div>}
    </section>
    <Dialog open={inviteOpen} onOpenChange={setInviteOpen}><DialogContent><DialogHeader><DialogTitle>邀请用户</DialogTitle><DialogDescription>邀请只授予当前租户的成员资格。已有账号继续使用原密码。</DialogDescription></DialogHeader><form onSubmit={invite} className="management-form">{error && <Notice error>{error}</Notice>}<Field label="受邀邮箱"><Input type="email" required value={email} onChange={event => setEmail(event.target.value)} autoComplete="off" /></Field><Field label="租户角色"><select value={role} onChange={event => setRole(event.target.value as Role)}><option value="member">普通用户</option><option value="admin">管理员</option></select></Field><DialogFooter><Button type="button" variant="outline" onClick={() => setInviteOpen(false)}>取消</Button><Button disabled={busy} type="submit">{busy ? '正在创建…' : '创建邀请'}</Button></DialogFooter></form></DialogContent></Dialog>
    <Dialog open={change !== null} onOpenChange={open => { if (!open) setChange(null) }}><DialogContent onOpenAutoFocus={event => { event.preventDefault(); document.getElementById('member-change-cancel')?.focus() }}><DialogHeader><DialogTitle>更新成员权限</DialogTitle><DialogDescription>{change?.member.email} 将设为{change?.role === 'admin' ? '管理员' : '普通用户'}，状态为{change?.status === 'active' ? '正常' : change?.status === 'suspended' ? '已停用' : '已移除'}。{change?.status === 'active' ? '历史积压工作不会自动重新执行。' : '当前租户访问和新执行将停止。数据保留，进行中的工作会请求取消。'}</DialogDescription></DialogHeader>{error && <Notice error>{error}</Notice>}<DialogFooter><Button id="member-change-cancel" variant="outline" onClick={() => setChange(null)}>取消</Button><Button disabled={busy} onClick={() => void changeMember()}>{busy ? '正在更新…' : '确认更新'}</Button></DialogFooter></DialogContent></Dialog>
  </>
}
