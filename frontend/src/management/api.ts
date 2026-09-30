export class APIError extends Error {
  status: number
  code: string
  constructor(status: number, code: string, message: string) {
    super(message)
    this.status = status
    this.code = code
  }
}

export async function api<T>(path: string, body?: unknown, method?: string, signal?: AbortSignal): Promise<T> {
  const response = await fetch(`/api${path}`, {
    method: method ?? (body === undefined ? 'GET' : 'POST'),
    credentials: 'same-origin',
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal,
  })
  const value = await response.json()
  if (!response.ok) throw new APIError(response.status, value.code ?? 'error', value.error ?? '请求失败')
  return value as T
}

export function errorText(error: unknown): string {
  if (error instanceof APIError) {
    if (error.message.includes('retain an active administrator')) return '租户至少需要保留一位有效管理员。'
    if (error.message.includes('account already exists')) return '此邮箱已有账号，请使用原密码登录后接受邀请。'
    if (error.message.includes('invalid email or password')) return '邮箱或密码不正确。'
    const messages: Record<string, string> = {
      authentication_required: '登录已过期，请重新登录。',
      access_denied: '没有权限访问此资源，或成员状态已改变。',
      invitation_unavailable: '链接已失效或已被使用，请申请新链接。',
      conflict: '资源状态已改变或请求已存在，请刷新后重试。',
      model_unavailable: '所选模型不可用，请在 Fleet 或 Agent 设置中选择可用模型。',
      rate_limited: '尝试次数过多，请稍后重试。',
      email_unavailable: '此部署尚未配置邮件服务，请联系部署管理员。',
      invalid_request: '请检查输入内容或请求格式。',
      internal_error: '服务暂时不可用，请稍后重试。',
    }
    return messages[error.code] ?? error.message
  }
  return error instanceof Error ? error.message : '无法完成请求，请重试。'
}
