import { useEffect, useState, type FormEvent } from 'react'
import { ArrowRight, RefreshCw } from 'lucide-react'
import { nanoid } from 'nanoid'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { api, APIError, errorText } from './api'
import { Empty, Failure, Field, Loading, Notice } from './components'
import { useResource } from './use-resource'
import type { Environment, FileLocation, Transfer, TransferRequest } from './schema'

type LocationInput = { environment: string; path: string; directory: string }
const emptyLocation = (): LocationInput => ({ environment: '', path: '', directory: '' })
const stateName: Record<string, string> = { accepted: '进行中 / 等待设备', completed: '已完成', cancelled: '已取消', failed: '失败', unknown: '结果未知，请核对原设备' }

function LocationFields({ label, value, change, environments, disabled }: { label: string; value: LocationInput; change: (value: LocationInput) => void; environments: Environment[]; disabled: boolean }) {
  return <fieldset className="management-transfer-location" disabled={disabled}><legend>{label}</legend>
    <Field label={`${label}环境`}><select required value={value.environment} onChange={event => change({ ...value, environment: event.target.value })}><option value="">请选择环境</option>{environments.map(environment => <option key={environment.id} value={environment.id}>{environment.name} · {environment.kind === 'hosted' ? '托管工作区' : environment.online ? '在线' : '离线'}</option>)}</select></Field>
    <Field label={`${label}路径`}><Input required value={value.path} onChange={event => change({ ...value, path: event.target.value })} placeholder={label === '目标' ? '新文件的路径' : '文件路径'} /></Field>
    <Field label={`${label}工作目录（可选）`}><Input value={value.directory} onChange={event => change({ ...value, directory: event.target.value })} placeholder="留空使用环境默认目录" /></Field>
  </fieldset>
}

function TransferForm({ base, complete }: { base: string; complete: () => void }) {
  const [revision, setRevision] = useState(0)
  const environments = useResource<Environment[]>(`${base}/environments`, revision)
  useEffect(() => { const timer = window.setInterval(() => setRevision(value => value + 1), 5000); return () => window.clearInterval(timer) }, [])
  const [mode, setMode] = useState('copy')
  const [source, setSource] = useState(emptyLocation)
  const [target, setTarget] = useState(emptyLocation)
  const [artifact, setArtifact] = useState('')
  const [visibility, setVisibility] = useState('agent')
  const [request, setRequest] = useState<TransferRequest | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const files = (environments.data ?? []).filter(item => item.capabilities.includes('files'))
  function location(value: LocationInput): FileLocation {
    const environment = files.find(item => item.id === value.environment)
    if (!environment) throw new Error('请选择已授权文件访问的环境。')
    return { environment_id: environment.id, authorization_version: environment.authorization_version, path: value.path, working_directory: value.directory || undefined }
  }
  async function submit(event: FormEvent) {
    event.preventDefault(); setBusy(true); setError(''); setNotice('')
    try {
      const submission = request ?? {
        request_id: nanoid(),
        source: mode !== 'import' ? location(source) : undefined,
        target: mode !== 'publish' ? location(target) : undefined,
        artifact_id: mode === 'import' ? artifact.trim().replace(/^artifact:/, '') : undefined,
        name: mode !== 'import' ? source.path.split('/').pop() : undefined,
        media_type: mode !== 'import' ? 'application/octet-stream' : undefined,
        visibility: mode !== 'import' ? visibility : undefined,
      }
      setRequest(submission)
      await api<Transfer>(`${base}/transfers`, submission)
      setRequest(null); setNotice('传输已创建，可在下方查看进度。设备离线时会保留等待。'); complete()
    } catch (err) {
      setError(errorText(err))
      if (err instanceof APIError && [400, 401, 403, 404].includes(err.status)) setRequest(null)
    } finally { setBusy(false) }
  }
  if (environments.error) return <Failure message={environments.error} retry={() => setRevision(value => value + 1)} />
  if (!environments.data) return <Loading />
  if (!files.length) return <Empty title="暂无可用的文件环境">连接设备并授权此 Agent 的文件访问，或启用托管工作区后即可传输。</Empty>
  return <form onSubmit={submit} className="management-file-upload">
    <Field label="传输方式"><select value={mode} disabled={busy || request !== null} onChange={event => setMode(event.target.value)}><option value="copy">环境之间复制</option><option value="publish">从环境发布文件</option><option value="import">导入已有文件产物</option></select></Field>
    {request && <Notice>正在确认原传输请求。重试会沿用同一请求，不会再次执行。</Notice>}
    <div className="management-transfer-locations">
      {mode !== 'import' ? <LocationFields label="来源" value={source} change={setSource} environments={files} disabled={busy || request !== null} /> : <Field label="文件产物引用"><Input required value={artifact} disabled={busy || request !== null} onChange={event => setArtifact(event.target.value)} placeholder="artifact:…" /></Field>}
      {mode !== 'publish' && <LocationFields label="目标" value={target} change={setTarget} environments={files} disabled={busy || request !== null} />}
    </div>
    {mode !== 'import' && <Field label="文件产物可见范围"><select value={visibility} disabled={busy || request !== null} onChange={event => setVisibility(event.target.value)}><option value="agent">此 Agent 私有</option><option value="fleet">Fleet 共享</option></select></Field>}
    <p className="management-file-progress">只传输指定文件。目标路径必须尚不存在；离线设备默认等待 24 小时，可在列表中延长。</p>
    {error && <Notice error>{error}</Notice>}{notice && <Notice>{notice}</Notice>}
    <Button disabled={busy}><ArrowRight />{busy ? '正在提交…' : request ? '重试原请求' : '开始传输'}</Button>
  </form>
}

export function TransfersPanel({ base, writable }: { base: string; writable: boolean }) {
  const [revision, setRevision] = useState(0)
  const [after, setAfter] = useState('')
  const transfers = useResource<Transfer[]>(`${base}/transfers?after=${after}`, revision)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const refresh = () => setRevision(value => value + 1)
  useEffect(() => { const timer = window.setInterval(() => setRevision(value => value + 1), 3000); return () => window.clearInterval(timer) }, [])
  async function action(id: string, command: string) {
    setBusy(id); setError('')
    try { await api(`${base}/transfers/${id}/${command}`, command === 'extend' ? { wait_hours: 24 } : {}); refresh() } catch (err) { setError(errorText(err)) } finally { setBusy('') }
  }
  return <section aria-label="文件传输">
    {writable && <TransferForm base={base} complete={refresh} />}{error && <Notice error>{error}</Notice>}
    <div className="management-panel-heading"><h3>传输记录</h3><Button variant="ghost" size="sm" onClick={refresh}><RefreshCw />刷新</Button></div>
    {transfers.error ? <Failure message={transfers.error} retry={refresh} /> : !transfers.data ? <Loading /> : transfers.data.length === 0 ? <Empty title="暂无传输记录">在这里或通过 Agent 发起的文件传输会显示在此处。</Empty> : transfers.data.map(transfer => <article key={transfer.id} className="management-artifact-row">
      <strong>{transfer.request.name || transfer.request.target?.path || '导入文件'}</strong><small>{stateName[transfer.state] ?? transfer.state}{transfer.cancel_requested ? ' · 已请求取消' : ''}</small>
      <code>来源：{transfer.request.source ? `${transfer.request.source.environment_id} · ${transfer.request.source.path}` : `artifact:${transfer.request.artifact_id}`}</code>
      <code>目标：{transfer.request.target ? `${transfer.request.target.environment_id} · ${transfer.request.target.path}` : '平台文件产物'}</code>
      {transfer.artifact_id && <code>artifact:{transfer.artifact_id}</code>}
      {transfer.error && <small>{transfer.error === 'waiting_for_file_storage' ? '等待平台文件存储空间' : transfer.error}</small>}
      {transfer.state === 'accepted' && <><small>等待截止：{new Date(transfer.wait_until).toLocaleString()}</small>{writable && !transfer.cancel_requested && <div className="management-row-actions"><Button size="sm" variant="outline" disabled={busy === transfer.id} onClick={() => void action(transfer.id, 'extend')}>再等待 24 小时</Button><Button size="sm" variant="ghost" disabled={busy === transfer.id} onClick={() => void action(transfer.id, 'cancel')}>取消传输</Button></div>}</>}
    </article>)}
    <div className="management-row-actions">{after && <Button variant="ghost" onClick={() => setAfter('')}>回到第一页</Button>}{transfers.data?.length === 100 && <Button variant="outline" onClick={() => setAfter(transfers.data![transfers.data!.length - 1].id)}>下一页</Button>}</div>
  </section>
}
