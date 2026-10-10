import { useCallback, useMemo, useState } from 'react'
import { nanoid } from 'nanoid'
import { defaultRehypePlugins } from 'streamdown'
import { workspaceMarkdownLinks, workspacePathFromNode } from './markdown-path'
import { MessageResponse, type MessageResponseProps } from '@/components/ai-elements/message'
import { Button } from '@/components/ui/button'
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { Empty, Failure, Field, Loading, Notice } from './components'
import { useResource } from './use-resource'
import { WorkspacePreview } from './workspace-files'
import type { Environment, WorkspaceReadRequest } from './schema'

const rehypePlugins = [defaultRehypePlugins.raw, defaultRehypePlugins.sanitize, workspaceMarkdownLinks, defaultRehypePlugins.harden]

export function MessageMarkdown({ base, children, animating = false }: { base: string; children: string; animating?: boolean }) {
  const [path, setPath] = useState<string | null>(null)
  const components = useMemo<NonNullable<MessageResponseProps['components']>>(() => ({
    a: ({ href, children, node }) => {
      const target = workspacePathFromNode(node)
      return target ? <button type="button" className="management-markdown-file" title={`查看工作文件：${target}`} onClick={() => setPath(target)}>{children}</button> : <a href={href} target="_blank" rel="noreferrer">{children}</a>
    },
    img: ({ src, alt, node }) => {
      const target = workspacePathFromNode(node)
      return target ? <button type="button" className="management-markdown-file" aria-label={`查看工作图片 ${alt || target}`} onClick={() => setPath(target)}>{alt || target} · 查看工作图片</button> : <img src={src} alt={alt ?? ''} loading="lazy" />
    },
  }), [])
  return <><MessageResponse rehypePlugins={rehypePlugins} components={components} isAnimating={animating}>{children}</MessageResponse>{path && <MarkdownFile key={`${base}:${path}`} base={base} path={path} close={() => setPath(null)} />}</>
}

function MarkdownFile({ base, path, close }: { base: string; path: string; close: () => void }) {
  const [revision, setRevision] = useState(0)
  const refreshEnvironment = useCallback(() => setRevision(value => value + 1), [])
  const environments = useResource<Environment[]>(`${base}/environments`, revision)
  const [selected, setSelected] = useState('')
  const [read, setRead] = useState<{ environment: Environment; request: WorkspaceReadRequest } | null>(null)
  const available = environments.data?.filter(environment => environment.capabilities.includes('files'))
  const environment = available?.find(item => item.id === selected)
  return <Sheet open onOpenChange={open => { if (!open) close() }}><SheetContent className="management-artifact-dialog"><SheetHeader><SheetTitle>对话中的工作文件</SheetTitle><SheetDescription>消息没有记录此相对路径所在的环境，请明确选择。读取当前文件；已保存的附件保留在原始文件产物中。</SheetDescription></SheetHeader>
    <code>{path}</code>
    {environments.error ? <Failure message={environments.error} retry={() => setRevision(value => value + 1)} /> : !available ? <Loading /> : available.length === 0 ? <Empty title="没有可访问的文件环境">在 Agent 配置中检查执行环境授权。</Empty> : <>
      <Field label="消息文件所在环境"><select value={selected} onChange={event => { setSelected(event.target.value); setRead(null) }}><option value="">请选择，不会自动使用默认环境</option>{available.map(item => <option key={item.id} value={item.id}>{item.name} · {item.online ? '在线' : '离线'} · {item.working_directory}</option>)}</select></Field>
      {environment && <><p>读取目录：<code>{environment.working_directory}</code></p>{!environment.online && <Notice>环境离线，读取将等待该环境；不会切换到其它环境。</Notice>}<Button onClick={() => setRead({ environment, request: { request_id: nanoid(), environment_id: environment.id, directory: environment.working_directory, query: { path, read: true, hidden: true } } })}>读取文件</Button></>}
    </>}
    {read && <WorkspacePreview key={read.request.request_id} base={base} request={read.request} environment={read.environment} close={() => setRead(null)} onRead={refreshEnvironment} />}
  </SheetContent></Sheet>
}
