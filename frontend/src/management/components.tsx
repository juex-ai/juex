import type { ReactNode } from 'react'
import { AlertCircle, LoaderCircle } from 'lucide-react'
import { Button } from '@/components/ui/button'

export function PageHeading({ title, description, actions }: { title: string; description?: string; actions?: ReactNode }) {
  return <header className="management-heading"><div><h1>{title}</h1>{description && <p>{description}</p>}</div>{actions && <div className="management-actions">{actions}</div>}</header>
}

export function Notice({ children, error = false }: { children: ReactNode; error?: boolean }) {
  return <div className={`management-notice ${error ? 'is-error' : ''}`} role={error ? 'alert' : 'status'}>{error && <AlertCircle size={16} aria-hidden="true" />}<span>{children}</span></div>
}

export function Loading() {
  return <div className="management-loading" role="status"><LoaderCircle size={18} className="animate-spin motion-reduce:animate-none" aria-hidden="true" />正在加载…</div>
}

export function Empty({ title, children }: { title: string; children?: ReactNode }) {
  return <div className="management-empty"><h2>{title}</h2>{children && <p>{children}</p>}</div>
}

export function Failure({ message, retry }: { message: string; retry: () => void }) {
  return <Notice error>{message} <Button variant="outline" onClick={retry}>重试</Button></Notice>
}

export function Field({ label, children, hint }: { label: string; children: ReactNode; hint?: string }) {
  return <label className="management-field"><span>{label}</span>{children}{hint && <small>{hint}</small>}</label>
}

export function Status({ status }: { status: string }) {
  const labels: Record<string, string> = { active: '正常', suspended: '已停用', removed: '已移除', invited: '邀请中', admin: '管理员', member: '普通用户' }
  return <span className={`management-status status-${status}`}>{labels[status] ?? status}</span>
}
