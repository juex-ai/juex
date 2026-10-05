import { useState, type FormEvent } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { DialogFooter } from '@/components/ui/dialog'
import { Field } from './components'
import type { Agent, CalendarDefinition, CalendarRule, CalendarSchedule } from './schema'

const split = (text: string) => text.split(/[，,\s]+/).filter(Boolean)
const localDate = (at?: string) => { const date = at ? new Date(at) : new Date(Date.now() + 3600_000); return new Date(date.getTime() - date.getTimezoneOffset() * 60_000).toISOString().slice(0, 19) }

export function CalendarEditor({ item, agents, busy, submit, close }: { item: CalendarSchedule | null; agents: Agent[]; busy: boolean; submit: (definition: CalendarDefinition) => void; close: () => void }) {
  const rule = item?.rule
  const [name, setName] = useState(item?.name ?? '')
  const [content, setContent] = useState(item?.content ?? '')
  const [mode, setMode] = useState(item?.mode ?? 'reminder')
  const [agent, setAgent] = useState(item?.agent_id ?? agents.find(a => a.status === 'active')?.id ?? '')
  const [frequency, setFrequency] = useState(rule?.frequency ?? 'once')
  const [timezone, setTimezone] = useState(rule?.timezone ?? Intl.DateTimeFormat().resolvedOptions().timeZone)
  const [at, setAt] = useState(localDate(rule?.at))
  const [lunar, setLunar] = useState(!!rule?.lunar)
  const [leap, setLeap] = useState(rule?.lunar?.leap_month ?? 'regular')
  const [year, setYear] = useState(rule?.year ?? new Date().getFullYear())
  const [month, setMonth] = useState(rule?.month ?? 1)
  const [day, setDay] = useState(rule?.day ?? 1)
  const [time, setTime] = useState(rule?.time ?? '09:00')
  const [months, setMonths] = useState(rule?.months?.join(', ') ?? '1')
  const [days, setDays] = useState(rule?.days?.join(', ') ?? '1')
  const [times, setTimes] = useState(rule?.times?.join(', ') ?? '09:00')
  const [weekdays, setWeekdays] = useState(rule?.weekdays ?? [])
  const [minutes, setMinutes] = useState((rule?.every_seconds ?? 3600) / 60)
  const [catchUp, setCatchUp] = useState(item?.catch_up || 'latest')
  const [lateness, setLateness] = useState(item?.max_lateness_minutes ?? 1440)
  const isLunar = lunar && ['once', 'monthly', 'yearly'].includes(frequency)
  function save(event: FormEvent) {
    event.preventDefault()
    const rule: CalendarRule = { frequency }
    if (frequency === 'interval') rule.every_seconds = Math.round(minutes * 60)
    else if (frequency === 'once' && !isLunar) rule.at = item?.rule.at && at === localDate(item.rule.at) ? item.rule.at : new Date(at).toISOString()
    else {
      rule.timezone = timezone
      if (isLunar) rule.lunar = { leap_month: leap }
      if (frequency === 'once') Object.assign(rule, { year, month, day, time })
      else {
        rule.times = split(times)
        if (frequency === 'daily' && weekdays.length) rule.weekdays = weekdays
        if (frequency === 'monthly' || frequency === 'yearly') rule.days = split(days).map(Number)
        if (frequency === 'yearly') rule.months = split(months).map(Number)
      }
    }
    submit({ name, content, mode, agent_id: mode !== 'reminder' ? agent : undefined, rule, catch_up: catchUp, max_lateness_minutes: lateness })
  }
  return <form className="management-form" onSubmit={save}>
    <fieldset className="management-calendar-fields" disabled={busy}>
      <Field label="日程名称"><Input required maxLength={100} value={name} onChange={e => setName(e.target.value)} /></Field>
      <Field label="处理方式"><select className="management-select" value={mode} onChange={e => setMode(e.target.value)}><option value="reminder">提醒我</option><option value="agent">新建 Worker 执行</option><option value="main">唤醒 Main</option></select></Field>
      {mode !== 'reminder' && <Field label="执行 Agent"><select required className="management-select" value={agent} onChange={e => setAgent(e.target.value)}><option value="">选择 Agent</option>{agents.filter(a => a.status === 'active' || a.id === agent).map(a => <option key={a.id} value={a.id} disabled={a.status !== 'active'}>{a.name}{a.status !== 'active' ? '（已归档）' : ''}</option>)}</select></Field>}
      {mode === 'main' && <p>送入所选 Agent 的现有 Main 对话。已接收表示送达；后续取消日程不会撤回已接收的输入。</p>}
      <Field label={mode === 'reminder' ? '提醒内容' : '执行任务'}><Textarea required rows={3} maxLength={mode === 'reminder' ? 2048 : 8192} value={content} onChange={e => setContent(e.target.value)} /></Field>
      <Field label="重复方式"><select className="management-select" value={frequency} onChange={e => setFrequency(e.target.value)}><option value="once">仅一次</option><option value="daily">每天 / 每周</option><option value="monthly">每月</option><option value="yearly">每年</option><option value="interval">固定间隔</option></select></Field>
      {['once', 'monthly', 'yearly'].includes(frequency) && <label className="management-calendar-check"><input type="checkbox" checked={lunar} onChange={e => setLunar(e.target.checked)} />使用农历</label>}
      {frequency === 'interval' ? <Field label="每隔多少分钟"><Input type="number" min={1} max={5256000} step={1} required value={minutes} onChange={e => setMinutes(Number(e.target.value))} /></Field> : frequency === 'once' && !isLunar ? <Field label="提醒时间" hint={`按本机时区 ${Intl.DateTimeFormat().resolvedOptions().timeZone} 输入。`}><Input type="datetime-local" step={1} required value={at} onChange={e => setAt(e.target.value)} /></Field> : <>
        <Field label="时区" hint="例如 Asia/Shanghai、America/New_York。"><Input required value={timezone} onChange={e => setTimezone(e.target.value)} /></Field>
        {frequency === 'once' ? <div className="management-calendar-grid"><Field label="农历年"><Input type="number" required min={1} max={9999} value={year} onChange={e => setYear(Number(e.target.value))} /></Field><Field label="月"><Input type="number" required min={1} max={12} value={month} onChange={e => setMonth(Number(e.target.value))} /></Field><Field label="日"><Input type="number" required min={1} max={30} value={day} onChange={e => setDay(Number(e.target.value))} /></Field><Field label="时间"><Input type="time" required value={time} onChange={e => setTime(e.target.value)} /></Field></div> : <>
          {frequency === 'yearly' && <Field label="月份" hint="多个值用逗号分隔，例如 1, 6。"><Input required value={months} onChange={e => setMonths(e.target.value)} /></Field>}
          {['monthly', 'yearly'].includes(frequency) && <Field label="日期" hint="多个值用逗号分隔；不存在的日期会跳过。"><Input required value={days} onChange={e => setDays(e.target.value)} /></Field>}
          <Field label="时间" hint="多个时间用逗号分隔，例如 09:00, 18:30。"><Input required value={times} onChange={e => setTimes(e.target.value)} /></Field>
          {frequency === 'daily' && <div className="management-calendar-weekdays" role="group" aria-label="星期筛选">{['mon', 'tue', 'wed', 'thu', 'fri', 'sat', 'sun'].map((d, i) => <label key={d}><input type="checkbox" checked={weekdays.includes(d)} onChange={e => setWeekdays(old => e.target.checked ? [...old, d] : old.filter(v => v !== d))} />{'一二三四五六日'[i]}</label>)}<small>不选则每天触发</small></div>}
        </>}
        {isLunar && <Field label="闰月处理"><select className="management-select" value={leap} onChange={e => setLeap(e.target.value)}><option value="regular">仅普通月</option><option value="leap">仅闰月</option>{frequency !== 'once' && <option value="both">普通月和闰月</option>}</select></Field>}
      </>}
      <Field label="恢复后补跑策略" hint="已准备的触发继续投递；人工暂停或停用期间不补跑。"><select className="management-select" value={catchUp} onChange={e => setCatchUp(e.target.value)}><option value="latest">补最近一次</option><option value="none">不补跑</option></select></Field>
      {catchUp === 'latest' && <Field label="故障后允许补跑的分钟数"><Input type="number" required min={1} max={1440} value={lateness} onChange={e => setLateness(Number(e.target.value))} /></Field>}
    </fieldset>
    <DialogFooter><Button type="button" variant="outline" onClick={close}>取消</Button><Button disabled={busy} type="submit">{busy ? '正在保存…' : '保存日程'}</Button></DialogFooter>
  </form>
}
