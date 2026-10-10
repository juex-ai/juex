import { useState, type FormEvent } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { Search } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Empty, Failure, Field, Loading, Notice } from './components'
import { useResource } from './use-resource'
import type { MemoryDomain, MemoryFactPage, MemoryFactView } from './schema'

const domainNames: Record<string,string> = {identity:'身份与自我',interpersonal:'人际关系',knowledge:'知识与兴趣',health:'健康',projects:'项目与职业',hobbies:'爱好',preferences:'偏好与习惯',finance:'财务与物质',obligations:'承诺与义务',temporary:'临时情境',other:'其他'}
const lifecycleNames: Record<string,string> = {current:'当前有效',overdue:'已到期未结束',future:'尚未生效',expired:'已过有效期',superseded:'已被取代',corrected:'已更正',retracted:'已撤回',disputed:'存在分歧',uncertain:'时间不确定'}
type Filters = {domain:string;entity:string;subject:string;predicate:string;view:string;status:string;at:string;text:string}
const dateLabel = (value?:string|null) => value?new Date(value).toLocaleString():'未注明'

export function MemoryKnowledge({base,tenant,revision,read,busy}:{base:string;tenant:string;revision:number;read:(id:string)=>void;busy:boolean}) {
  const [params,setParams] = useSearchParams()
  const [retry,setRetry] = useState(0)
  const filters:Filters = {domain:params.get('domain')??'',entity:params.get('entity')??'',subject:params.get('subject')??'',predicate:params.get('predicate')??'',view:params.get('view')??'current',status:params.get('status')??'',at:params.get('at')??'',text:params.get('text')??''}
  const offset = Math.max(0,Math.min(1<<30,Number(params.get('offset'))||0))
  const query = new URLSearchParams({...filters,offset:String(offset),limit:'20'})
  const domains = useResource<MemoryDomain[]>(`${base}/domains`,revision+retry)
  const domain = useResource<MemoryDomain[]>(filters.domain?`${base}/domains?id=${encodeURIComponent(filters.domain)}`:null,revision+retry)
  const facts = useResource<MemoryFactPage>(`${base}/facts?${query}`,revision+retry)
  function change(next:Filters, nextOffset=0) {
    const search = new URLSearchParams({tab:'facts'})
    for(const [key,value] of Object.entries(next))if(value)search.set(key,value)
    if(nextOffset)search.set('offset',String(nextOffset))
    setParams(search)
  }
  const filterEntity = (item:MemoryFactView,id:string)=>change({...filters,domain:item.fact.domain,entity:id,subject:''})
  return <div className="management-memory-knowledge">
    <nav aria-label="知识领域" className="management-memory-domains"><Button variant={!filters.domain?'secondary':'ghost'} onClick={()=>change({...filters,domain:'',predicate:'',entity:'',subject:''})}>全部领域</Button>{domains.data?.map(value=><Button key={value.id} variant={filters.domain===value.id?'secondary':'ghost'} onClick={()=>change({...filters,domain:value.id,predicate:'',entity:'',subject:''})}>{domainNames[value.id]??value.name}</Button>)}</nav>
    {domains.error&&<Failure message={domains.error} retry={()=>setRetry(value=>value+1)}/>}
    <KnowledgeFilters key={JSON.stringify(filters)} initial={filters} relations={domain.data?.[0]?.relations??[]} apply={change}/>
    {domain.error&&<Failure message={domain.error} retry={()=>setRetry(value=>value+1)}/>}
    {filters.view==='as_of'&&<Notice>按所选时刻的有效期查看。已更正或撤回的断言不会重新成为事实；缺少明确日期的内容不会被当作当时有效。</Notice>}
    {facts.error?<Failure message={facts.error} retry={()=>setRetry(value=>value+1)}/>:!facts.data?<Loading/>:<>
      <p className="text-sm text-muted-foreground">匹配 {facts.data.total} 项 · 此领域保留 {facts.data.domain_total} 项断言</p>
      {facts.data.facts.length?<div className="management-memory-facts">{facts.data.facts.map(item=><article className="management-memory-fact" key={`${item.entry_id}:${item.fact.id}`}>
        <div className="management-memory-fact-heading"><span>{domainNames[item.fact.domain]??item.fact.domain} · {lifecycleNames[item.lifecycle]??item.lifecycle}</span><Button variant="outline" disabled={busy} onClick={()=>read(item.entry_id)}>查看完整知识</Button></div>
        <div className="management-memory-statement"><Button variant="link" title={item.subject.id} onClick={()=>filterEntity(item,item.subject.id)}>{item.subject.name} <small>({item.subject.id})</small></Button><strong>{item.fact.predicate}</strong>{item.object?<Button variant="link" title={item.object.id} onClick={()=>filterEntity(item,item.object!.id)}>{item.object.name} <small>({item.object.id})</small></Button>:<p>{item.fact.value}</p>}</div>
        {Object.keys(item.fact.qualifiers??{}).length>0&&<dl className="management-memory-qualifiers">{Object.entries(item.fact.qualifiers??{}).map(([key,value])=><div key={key}><dt>{key}</dt><dd>{value}</dd></div>)}</dl>}
        <p>{item.fact.reason}</p><small>有效期：{dateLabel(item.fact.valid_from)} — {dateLabel(item.fact.valid_until)}{item.fact.due_at&&` · 到期 ${dateLabel(item.fact.due_at)}`}</small>
        {item.fact.time_note&&<p>{item.fact.time_note}</p>}<small>记录于 {dateLabel(item.fact.recorded_at)} · {item.scope.project||item.scope.workspace||'Fleet 知识'}</small>
        <div className="management-memory-sources">{item.fact.sources.filter(source=>source.agent_id&&source.thread_id).map((source,index)=><Link key={index} to={`/t/${tenant}/agents/${source.agent_id}?thread=${encodeURIComponent(source.thread_id)}`}>来源对话 {index+1}</Link>)}</div>
      </article>)}</div>:<Empty title="暂无匹配的事实">可调整领域、实体、关系或时间视图。非结构化知识仍在“知识”中查看。</Empty>}
      <div className="management-memory-pagination"><Button variant="ghost" disabled={offset===0} onClick={()=>change(filters,Math.max(0,offset-20))}>上一页</Button><span>第 {Math.floor(offset/20)+1} 页</span><Button variant="ghost" disabled={facts.data.next<=0} onClick={()=>change(filters,facts.data!.next)}>下一页</Button></div>
    </>}
  </div>
}

function KnowledgeFilters({initial,relations,apply}:{initial:Filters;relations:NonNullable<MemoryDomain['relations']>;apply:(value:Filters)=>void}) {
  const [value,setValue]=useState(initial)
  const [error,setError]=useState('')
  function submit(event:FormEvent) {
    event.preventDefault();setError('')
    const at=value.view==='as_of'?new Date(value.at):null
    if(at&&!Number.isFinite(at.getTime())){setError('请选择有效的时间点。');return}
    apply({...value,at:at?.toISOString()??''})
  }
  const localTime=value.at&&Number.isFinite(new Date(value.at).getTime())?new Date(new Date(value.at).getTime()-new Date(value.at).getTimezoneOffset()*60000).toISOString().slice(0,16):''
  return <form className="management-memory-filter-form" onSubmit={submit}>
    <Field label="搜索事实"><Input value={value.text} maxLength={4096} onChange={event=>setValue({...value,text:event.target.value})}/></Field>
    <Field label="实体 ID" hint="匹配关系两端的实体；点击事实中的实体可直接筛选。"><Input value={value.entity} onChange={event=>setValue({...value,entity:event.target.value})}/></Field>
    <Field label="关系"><select className="management-select" value={value.predicate} onChange={event=>setValue({...value,predicate:event.target.value})}><option value="">全部关系</option>{value.predicate&&!relations.some(item=>item.predicate===value.predicate)&&<option value={value.predicate}>{value.predicate}</option>}{relations.map(item=><option key={item.predicate} value={item.predicate}>{item.predicate} · {item.description}</option>)}</select></Field>
    <Field label="时间视图"><select className="management-select" value={value.view} onChange={event=>setValue({...value,view:event.target.value,status:'',at:event.target.value==='as_of'?value.at:''})}><option value="current">当前有效</option><option value="history">全部历史</option><option value="as_of">指定时间点</option></select></Field>
    <Field label="生命周期"><select className="management-select" value={value.status} onChange={event=>setValue({...value,status:event.target.value})}><option value="">全部状态</option>{Object.entries(lifecycleNames).filter(([key])=>key!=='uncertain'&&(value.view==='history'||key==='current'||key==='overdue')).map(([key,label])=><option key={key} value={key}>{label}</option>)}</select></Field>
    {value.view==='as_of'&&<Field label="有效时间点（本地时间）"><Input type="datetime-local" required value={localTime} onChange={event=>setValue({...value,at:event.target.value})}/></Field>}
    {error&&<Notice error>{error}</Notice>}<div className="management-row-actions"><Button type="submit" variant="outline"><Search/>应用筛选</Button><Button type="button" variant="ghost" onClick={()=>apply({domain:'',entity:'',subject:'',predicate:'',view:'current',status:'',at:'',text:''})}>清除筛选</Button></div>
  </form>
}
