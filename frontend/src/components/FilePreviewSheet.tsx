import { useEffect, useMemo, useRef, useState, type CSSProperties } from 'react';
import type { ThemedToken } from 'shiki';
import type { FileContentResponse } from '@/types';
import { Copy, Download, WrapText } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet';
import { fileLanguage } from '@/lib/file-browser';
import { writeClipboardText } from '@/lib/clipboard';
import { cn } from '@/lib/utils';

export type FilePreview = { path: string; content?: FileContentResponse; error?: string };

export function FilePreviewSheet({ preview, rawURL, onClose, onRetry, restoreFocus }: {
  preview: FilePreview | null;
  rawURL: (path: string) => string;
  onClose: () => void;
  onRetry: () => void;
  restoreFocus: () => void;
}) {
  return <Sheet open={!!preview} onOpenChange={open => { if (!open) onClose(); }}>
    <SheetContent side="right" className="flex !w-full !max-w-none flex-col gap-0 bg-card p-0 sm:!w-[min(90vw,64rem)]"
      onCloseAutoFocus={event => { event.preventDefault(); restoreFocus(); }}>
      {preview && <FilePreviewBody key={preview.path} preview={preview} rawURL={rawURL} onRetry={onRetry} />}
    </SheetContent>
  </Sheet>;
}

function FilePreviewBody({ preview, rawURL, onRetry }: {
  preview: FilePreview; rawURL: (path: string) => string; onRetry: () => void;
}) {
  const [wrap, setWrap] = useState(false);
  const [copyStatus, setCopyStatus] = useState('');
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);
  const file = preview.content;
  const text = file && file.kind !== 'image';
  const downloadURL = `${rawURL(preview.path)}&download=1`;
  async function copy() {
    if (!file) return;
    try {
      await writeClipboardText(file.content);
      setCopyStatus(file.truncated ? 'Preview copied' : 'Content copied');
    } catch { setCopyStatus('Copy failed. Select the text to copy it manually.'); }
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setCopyStatus(''), 3000);
  }
  return <>
    <SheetHeader className="shrink-0 border-b p-4 pr-12">
      <SheetTitle className="break-all font-mono text-sm">{preview.path}</SheetTitle>
      <SheetDescription>Read-only file preview{file ? ` · ${new Intl.NumberFormat().format(file.size)} bytes` : ''}</SheetDescription>
    </SheetHeader>
    <div className="flex shrink-0 flex-wrap items-center gap-1 border-b px-3 py-2" aria-label="File actions" role="group">
      {text && <>
        <Button variant="ghost" className="min-h-10" onClick={copy}><Copy className="size-4" />{file.truncated ? 'Copy preview' : 'Copy content'}</Button>
        <Button variant="ghost" className="min-h-10" aria-pressed={wrap} onClick={() => setWrap(!wrap)}><WrapText className="size-4" />Wrap lines</Button>
      </>}
      <Button variant="ghost" className="min-h-10" asChild><a href={downloadURL} download={preview.path.split('/').at(-1)}><Download className="size-4" />Download original</a></Button>
    </div>
    {copyStatus && <p role="status" className="shrink-0 px-4 py-2 text-xs">{copyStatus}</p>}
    {file?.truncated && <p role="status" className="shrink-0 border-b px-4 py-2 text-xs text-muted-foreground">Showing the first 256 KB. Download original for the complete file.</p>}
    <div className="min-h-0 flex-1 overflow-auto bg-muted/30 pb-[env(safe-area-inset-bottom)]" tabIndex={0} role="region" aria-label="File content">
      {preview.error ? <div role="alert" className="space-y-3 p-4"><p>Preview unavailable: {preview.error}</p><Button variant="outline" onClick={onRetry}>Retry preview</Button></div>
        : !file ? <p role="status" className="p-4 text-muted-foreground">Loading file…</p>
        : file.kind === 'image' ? <img src={rawURL(preview.path)} alt={`Preview of ${preview.path}`} className="mx-auto max-w-full p-4" />
        : <FilePreviewText code={file.content} path={preview.path} wrap={wrap} />}
    </div>
  </>;
}

function FilePreviewText({ code, path, wrap }: { code: string; path: string; wrap: boolean }) {
  const language = fileLanguage(path);
  const lines = useMemo(() => code.split('\n'), [code]);
  // A byte-bounded preview may still contain hundreds of thousands of empty lines.
  const plain = code.length > 100_000 || lines.length > 2000;
  const [highlighted, setHighlighted] = useState<{ code: string; tokens: ThemedToken[][] }>();
  useEffect(() => {
    if (plain || language === 'text') return;
    let cancelled = false;
    import('@/lib/file-highlight').then(module => module.highlightFile(code, language)).then(tokens => {
      if (!cancelled) setHighlighted({ code, tokens });
    }).catch(() => { /* Keep source text readable if highlighting cannot load. */ });
    return () => { cancelled = true; };
  }, [code, language, plain]);
  const tokens = highlighted?.code === code ? highlighted.tokens : undefined;
  if (plain) return <>
    <p className="px-4 pt-3 text-xs text-muted-foreground">Plain text for this large preview.</p>
    <pre className={cn('m-0 p-4 font-mono text-sm', wrap ? 'whitespace-pre-wrap break-words' : 'whitespace-pre')}><code>{code}</code></pre>
  </>;
  return <pre data-language={language} className={cn('m-0 min-w-full py-4 font-mono text-sm leading-6', wrap ? 'w-full whitespace-pre-wrap break-words' : 'w-max whitespace-pre')}><code>
    {lines.map((line, index) => <span key={index} className="flex" data-line={index + 1}>
      <span aria-hidden="true" className="sticky left-0 w-12 shrink-0 select-none bg-card px-2 text-right text-muted-foreground/60">{index + 1}</span>
      <span className="min-w-0 flex-1 px-3">{tokens?.[index]?.map((token, tokenIndex) => <span key={tokenIndex}
        className="text-[var(--file-token-light)] dark:text-[var(--file-token-dark)]"
        style={{ '--file-token-light': token.htmlStyle?.color ?? token.color, '--file-token-dark': token.htmlStyle?.['--shiki-dark'] ?? token.color } as CSSProperties}>{token.content}</span>) ?? line}{index < lines.length - 1 ? '\n' : ''}</span>
    </span>)}
  </code></pre>;
}
