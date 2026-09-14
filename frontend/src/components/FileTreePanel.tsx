import { type ReactNode, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { getFileTree, getFileContent, getFileRawURL } from "@/api";
import type { FileNode } from "@/types";
import { Folder, FolderOpen, File as FileIcon, ChevronRight, ChevronDown, RefreshCw, Search, X } from "lucide-react";
import { cn } from "@/lib/utils";
import { loadWorkspaceSnapshot, type LoadFileTree } from "@/lib/workspace-refresh";
import { filterFileTree, findFiles } from "@/lib/file-browser";
import { FilePreviewSheet, type FilePreview } from "@/components/FilePreviewSheet";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { ScrollArea } from "@/components/ui/scroll-area";

type FileTreePanelProps = {
  active?: boolean;
  emptyLabel?: string;
  headerTitle?: ReactNode;
  loadTree?: LoadFileTree;
  loadContent?: typeof getFileContent;
  rawURL?: typeof getFileRawURL;
  subscribeChanges?: (receive: () => void) => () => void;
  refreshLabel?: string;
  refreshRevision?: number;
  rootKey?: string;
  rootDescription?: string;
  title?: string;
  unavailableReason?: string;
};

export function FileTreePanel({
  active: visible = true, emptyLabel = "This directory is empty.", headerTitle,
  loadTree = getFileTree, loadContent = getFileContent, rawURL = getFileRawURL, subscribeChanges,
  refreshLabel = "Refresh workspace", refreshRevision = 0, rootKey = "workspace", rootDescription,
  title = "Workspace", unavailableReason,
}: FileTreePanelProps) {
  const active = visible && !unavailableReason;
  const [tree, setTree] = useState<FileNode | null>(null);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [preview, setPreview] = useState<FilePreview | null>(null);
  const [query, setQuery] = useState("");
  const [showHidden, setShowHidden] = useState(false);
  const treeRef = useRef<FileNode | null>(null);
  const refreshAbortRef = useRef<AbortController | null>(null);
  const previewAbortRef = useRef<AbortController | null>(null);
  const previewPathRef = useRef<string | undefined>(undefined);
  const previewRevision = useRef(0);
  const previewTrigger = useRef<HTMLButtonElement | null>(null);
  const findInput = useRef<HTMLInputElement>(null);
  const panelRef = useRef<HTMLDivElement>(null);

  const closePreview = useCallback(() => {
    previewAbortRef.current?.abort();
    previewAbortRef.current = null;
    previewPathRef.current = undefined;
    previewRevision.current++;
    setPreview(null);
  }, []);

  useLayoutEffect(() => {
    refreshAbortRef.current?.abort();
    refreshAbortRef.current = null;
    treeRef.current = null;
    setTree(null);
    setLoading(true);
    setRefreshing(false);
    setError(null);
    setQuery("");
    setShowHidden(false);
    closePreview();
  }, [rootKey, closePreview]);

  const refreshWorkspace = useCallback(() => {
    if (!active) return;
    refreshAbortRef.current?.abort();
    const controller = new AbortController();
    refreshAbortRef.current = controller;
    const previewPath = previewPathRef.current;
    const revision = previewRevision.current;
    setRefreshing(true);
    if (!treeRef.current) setLoading(true);
    loadWorkspaceSnapshot({ loadTree, loadContent, previewPath, signal: controller.signal })
      .then((snapshot) => {
        if (refreshAbortRef.current !== controller) return;
        treeRef.current = snapshot.tree;
        setTree(snapshot.tree);
        // A directory refresh must not reopen a closed preview or overwrite a newer selection.
        if (previewPath && revision === previewRevision.current && !previewAbortRef.current) {
          if (snapshot.previewError) setPreview({ path: previewPath, error: snapshot.previewError });
          else if (snapshot.previewFile) setPreview({ path: previewPath, content: snapshot.previewFile });
        }
        setError(null);
      })
      .catch((e) => {
        if (isAbortError(e) || refreshAbortRef.current !== controller) return;
        setError("Failed to load directory.");
      })
      .finally(() => {
        if (refreshAbortRef.current !== controller) return;
        refreshAbortRef.current = null;
        setLoading(false);
        setRefreshing(false);
      });
  }, [active, loadTree, loadContent]);

  useEffect(() => {
    if (!active) return;
    refreshWorkspace();
    return () => { refreshAbortRef.current?.abort(); refreshAbortRef.current = null; };
  }, [active, refreshWorkspace, refreshRevision, rootKey]);

  useEffect(() => () => {
    previewAbortRef.current?.abort();
    previewAbortRef.current = null;
  }, [active, loadContent, rootKey]);

  useEffect(() => {
    if (!active || !subscribeChanges) return;
    let disposed = false;
    const unsubscribe = subscribeChanges(() => { if (!disposed) refreshWorkspace(); });
    return () => { disposed = true; unsubscribe(); };
  }, [active, subscribeChanges, refreshWorkspace]);

  async function loadPreview(path: string) {
    previewAbortRef.current?.abort();
    const controller = new AbortController();
    previewAbortRef.current = controller;
    previewPathRef.current = path;
    previewRevision.current++;
    setPreview({ path });
    try {
      const content = await loadContent(path, controller.signal);
      if (previewAbortRef.current === controller) setPreview({ path, content });
    } catch (e) {
      if (!isAbortError(e) && previewAbortRef.current === controller) {
        setPreview({ path, error: e instanceof Error ? e.message : "Failed to load file content." });
      }
    } finally {
      if (previewAbortRef.current === controller) previewAbortRef.current = null;
    }
  }

  function handleFileClick(path: string, trigger: HTMLButtonElement) {
    previewTrigger.current = trigger;
    void loadPreview(path);
  }
  function restorePreviewFocus() {
    if (!panelRef.current?.isConnected) return;
    const trigger = previewTrigger.current;
    if (trigger?.isConnected) trigger.focus();
    else findInput.current?.focus();
  }
  const filtered = useMemo(() => tree ? filterFileTree(tree, showHidden) : null, [tree, showHidden]);
  const search = useMemo(() => filtered ? findFiles(filtered, query) : null, [filtered, query]);
  const searching = Boolean(query.trim());

  return <div ref={panelRef} className="flex h-full min-w-0 flex-col bg-card text-card-foreground">
    <div className="flex h-[var(--juex-header-height)] shrink-0 items-center justify-between gap-2 border-b px-3">
      {headerTitle ?? <span className="truncate px-2.5 text-sm font-medium">{title}</span>}
      <Button type="button" variant="ghost" size="icon" className="size-11 text-muted-foreground sm:size-9 pointer-coarse:size-11"
        onClick={refreshWorkspace} disabled={refreshing || !active} aria-label={refreshLabel} title={refreshLabel}>
        <RefreshCw className={cn("size-3.5 motion-reduce:animate-none", refreshing && "animate-spin")} />
      </Button>
    </div>
    <div className="shrink-0 space-y-2 border-b px-3 py-3">
      {rootDescription && <p aria-label="File root location" className="break-all font-mono text-xs text-muted-foreground">{rootDescription}</p>}
      <div className="relative">
        <Search aria-hidden="true" className="pointer-events-none absolute left-2.5 top-3 size-4 text-muted-foreground" />
        <Input ref={findInput} aria-label="Find files" placeholder="Find files by name or path…" value={query}
          className="h-10 pl-8 pr-10" disabled={!active || !tree} onChange={event => setQuery(event.target.value)} />
        {query && <Button variant="ghost" size="icon" className="absolute right-0 top-0 size-10" aria-label="Clear file search"
          onClick={() => { setQuery(""); findInput.current?.focus(); }}><X className="size-4" /></Button>}
      </div>
      <label className="flex min-h-8 items-center gap-2 text-xs text-muted-foreground">
        <input type="checkbox" checked={showHidden} disabled={!active} onChange={event => setShowHidden(event.target.checked)} className="size-4 accent-primary" />Show hidden files
      </label>
    </div>
    {error && <div role="alert" className="border-b border-destructive/25 bg-destructive/10 px-4 py-2 text-xs text-destructive">
      {error}{tree ? " Showing the last loaded snapshot." : ""}
    </div>}
    <ScrollArea className="min-h-0 flex-1 overflow-hidden p-3">
      {unavailableReason ? <p role="status" className="p-2 text-sm text-muted-foreground">{unavailableReason}</p>
        : loading ? <p role="status" className="p-2 text-sm text-muted-foreground">Loading…</p>
        : !filtered ? <p className="p-2 text-sm text-muted-foreground">{title} unavailable.</p>
        : <><div hidden={!searching}>
          <p role="status" className="px-2 pb-2 text-xs text-muted-foreground">{search?.files.length ? `${search.files.length} matching ${search.files.length === 1 ? 'file' : 'files'}${search.files.length > 200 ? ' · showing first 200' : ''}` : 'No matching files.'}</p>
          {search?.files.slice(0, 200).map(file => <button type="button" key={file.path} data-file-path={file.path} title={file.path}
            className="flex min-h-11 w-full items-start gap-2 rounded-md px-2 py-2 text-left text-xs outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring/35"
            onClick={event => handleFileClick(file.path, event.currentTarget)}>
            <FileIcon className="mt-0.5 size-4 shrink-0 text-muted-foreground" /><span className="min-w-0 break-all font-mono">{file.path}</span>
          </button>)}
          <p className="px-2 pt-2 text-xs text-muted-foreground">Searching loaded files{showHidden ? '.' : '; hidden files excluded.'}{search?.truncated ? ' Some folders reached the depth limit.' : ''}</p>
        </div>
        <div hidden={searching}>
          {filtered.is_dir && !filtered.children_truncated && !filtered.children?.length
            ? <p role="status" className="p-2 text-sm text-muted-foreground">{tree?.children?.length ? 'Only hidden files. Enable Show hidden files to see them.' : emptyLabel}</p>
            : <TreeNode node={filtered} depth={0} onFileClick={handleFileClick} />}
        </div></>}
    </ScrollArea>
    <FilePreviewSheet preview={preview} rawURL={rawURL} onClose={closePreview}
      onRetry={() => { if (preview) void loadPreview(preview.path); }} restoreFocus={restorePreviewFocus} />
  </div>;
}

function TreeNode({
  node,
  depth,
  onFileClick,
}: {
  node: FileNode;
  depth: number;
  onFileClick: (path: string, trigger: HTMLButtonElement) => void;
}) {
  const [expanded, setExpanded] = useState(depth === 0);

  if (!node.is_dir) {
    return (
      <button
        type="button"
        className="flex min-h-10 w-full min-w-0 items-center gap-1.5 rounded-[6px] px-2 py-1.5 text-left font-mono text-[12.5px] outline-none hover:bg-muted hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/35 sm:min-h-8"
        title={node.path}
        data-file-path={node.path}
        onClick={(event) => onFileClick(node.path, event.currentTarget)}
      >
        <FileIcon className="size-3.5 shrink-0 text-muted-foreground" />
        <span className="truncate">{node.name}</span>
      </button>
    );
  }

  return (
    <div className="flex flex-col">
      <button
        type="button"
        className={cn(
          "flex min-h-10 w-full min-w-0 items-center gap-1.5 rounded-[6px] px-2 py-1.5 text-left font-mono text-[12.5px] outline-none hover:bg-muted hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/35 sm:min-h-8",
          depth === 0 && "font-medium",
        )}
        onClick={() => setExpanded(!expanded)}
        aria-expanded={expanded}
      >
        {expanded ? (
          <ChevronDown className="size-3.5 shrink-0 opacity-50" />
        ) : (
          <ChevronRight className="size-3.5 shrink-0 opacity-50" />
        )}
        {expanded ? (
          <FolderOpen className="size-3.5 shrink-0 text-juex-gold-700 dark:text-juex-gold-400" />
        ) : (
          <Folder className="size-3.5 shrink-0 text-juex-gold-700 dark:text-juex-gold-400" />
        )}
        <span className="truncate">{node.name}</span>
      </button>
      {expanded && (node.children || node.children_truncated) && (
        <div className="ml-3 flex flex-col border-l border-border pl-2">
          {node.children?.map((child) => (
            <TreeNode key={child.path} node={child} depth={depth + 1} onFileClick={onFileClick} />
          ))}
          {node.children_truncated && (
            <div className="px-2 py-1 text-xs text-muted-foreground">Depth limit reached.</div>
          )}
        </div>
      )}
    </div>
  );
}

function isAbortError(err: unknown): boolean {
  return err instanceof DOMException && err.name === "AbortError";
}
