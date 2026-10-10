import { createContext, useContext, useEffect, useState, type ReactNode } from 'react'
import { APIError, api, errorText } from './api'
import type { Event, InputCheck, InputCheckPage, Message } from './schema'

type Checks = { items: Map<string, InputCheck>; scope: string; enabled: boolean; error?: string }
const empty: Checks = { items: new Map(), scope: '', enabled: true }
const Context = createContext<Checks>(empty)

export function InputChecks({ base, thread, events, children }: { base: string; thread: string; events: Event[]; children: ReactNode }) {
  const [state, setState] = useState<Checks>(empty)
  const ids = [...new Set(events.flatMap(event => {
    const message = event.kind === 'message.appended' ? event.data as Message : undefined
    const accepted = event.kind === 'input.accepted' ? event.data as { receipt?: { id: string }; source?: { kind?: string } } : undefined
    const id = message?.role === 'user' && (!message.kind || message.kind === 'direct') ? message.id : accepted && !accepted.source?.kind ? accepted.receipt?.id : undefined
    return id ? [id] : []
  }))].sort().join(',')
  useEffect(() => {
    const controller = new AbortController()
    let timer: number | undefined
    setState(empty)
    if (!ids) return () => controller.abort()
    const refresh = async () => {
      try {
        const keys = ids.split(',')
        let page: InputCheckPage | undefined
        const items = new Map<string, InputCheck>()
        for (let offset = 0; offset < keys.length; offset += 500) {
          const result = await api<InputCheckPage>(`${base}/threads/${thread}/input-checks`, { message_ids: keys.slice(offset, offset + 500) }, undefined, controller.signal)
          if (page && (page.scope_id !== result.scope_id || page.enabled !== result.enabled)) throw new Error('输入清单正在变化，稍后重新读取')
          page = result
          for (const item of result.items) items.set(item.message_id || item.input_id, item)
        }
        if (!controller.signal.aborted) setState({ items, scope: page!.scope_id, enabled: page!.enabled })
      } catch (error) {
        if (controller.signal.aborted) return
        setState({ ...empty, error: errorText(error) })
        if (error instanceof APIError && error.status >= 400 && error.status < 500 && error.status !== 429) return
      }
      if (!controller.signal.aborted) timer = window.setTimeout(() => void refresh(), 3000)
    }
    void refresh()
    return () => { controller.abort(); window.clearTimeout(timer) }
  }, [base, thread, ids])
  return <Context.Provider value={state}>{state.error && <p className="management-turn-notice">输入处理标记暂不可用：{state.error}</p>}{children}</Context.Provider>
}

export function InputCheckMarker({ message }: { message: string | undefined }) {
  const value = useContext(Context)
  const item = message ? value.items.get(message) : undefined
  if (!item) return null
  const checked = !!item.checked_at
  const text = checked ? '已处理 · Agent 确认' : item.scope_id !== value.scope ? '范围已结束 · 未确认处理' : item.delivery === 'blocked' ? '未交付模型' : item.delivery === 'registered' ? '等待交付模型' : '待处理'
  const reason = checked ? `Agent 于 ${new Date(item.checked_at!).toLocaleString()} 确认已处理，或已完整记入 Tasks；这是模型判断，不代表执行成功。` : '回复结束不会自动确认输入；只有 Agent 明确确认才会标记已处理。'
  return <small className="management-input-check" data-input-check={checked ? 'checked' : item.scope_id !== value.scope ? 'scope-ended' : item.delivery} title={`${reason}${value.enabled ? '' : ' 输入清单模块当前已停用，历史保留。'}`}>{text}{!value.enabled && ' · 模块已停用'}</small>
}
