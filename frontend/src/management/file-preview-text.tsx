import { useEffect, useMemo, useState, type CSSProperties } from 'react'
import type { ThemedToken } from 'shiki'

const languages: Record<string, string> = {
  js: 'javascript', mjs: 'javascript', cjs: 'javascript', jsx: 'jsx', ts: 'typescript', tsx: 'tsx',
  json: 'json', jsonl: 'json', html: 'html', htm: 'html', css: 'css', py: 'python',
  go: 'go', md: 'markdown', markdown: 'markdown', yaml: 'yaml', yml: 'yaml', sh: 'shellscript', bash: 'shellscript',
}

export function FilePreviewText({ code, path, wrap }: { code: string; path: string; wrap: boolean }) {
  const language = languages[path.split('.').at(-1)?.toLowerCase() ?? ''] ?? 'text'
  const lines = useMemo(() => code.split('\n'), [code])
  // Byte limits alone do not bound tokenization cost or the number of DOM nodes.
  const plain = code.length > 100_000 || lines.length > 2000
  const [highlighted, setHighlighted] = useState<{ code: string; language: string; tokens: ThemedToken[][] }>()
  useEffect(() => {
    if (plain || language === 'text') return
    let cancelled = false
    import('@/lib/file-highlight').then(module => module.highlightFile(code, language)).then(tokens => {
      if (!cancelled) setHighlighted({ code, language, tokens })
    }).catch(() => { /* Source remains readable when the optional highlighter cannot load. */ })
    return () => { cancelled = true }
  }, [code, language, plain])
  const tokens = highlighted?.code === code && highlighted.language === language ? highlighted.tokens : undefined
  return <>
    {plain && <p className="management-help">此预览较大，使用纯文本显示。</p>}
    <pre className="management-file-code" data-wrap={wrap} data-language={language} tabIndex={0} aria-label="文件内容"><code>{plain ? code : lines.map((line, index) => <span key={index} className="management-file-code-line" data-line={index + 1}>
      <span aria-hidden="true" className="management-file-line-number">{index + 1}</span>
      <span className="management-file-line-text">{tokens?.[index]?.map((token, tokenIndex) => <span key={tokenIndex} className="management-file-token" style={{ '--file-token-light': token.htmlStyle?.color ?? token.color, '--file-token-dark': token.htmlStyle?.['--shiki-dark'] ?? token.color } as CSSProperties}>{token.content}</span>) ?? line}{index < lines.length - 1 ? '\n' : ''}</span>
    </span>)}</code></pre>
  </>
}
