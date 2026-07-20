import {
  type ReactNode,
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { getFileTree, getFileContent, getFileRawURL } from "@/api";
import type { FileContentResponse, FileNode } from "@/types";
import {
  Folder,
  FolderOpen,
  File as FileIcon,
  ChevronRight,
  ChevronDown,
  RefreshCw,
} from "lucide-react";
import { cn } from "@/lib/utils";
import {
  loadWorkspaceSnapshot,
  type LoadFileTree,
} from "@/lib/workspace-refresh";
import { Button } from "@/components/ui/button";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { ScrollArea } from "@/components/ui/scroll-area";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";

const WORKSPACE_REFRESH_INTERVAL_MS = 5_000;

type FileTreePanelProps = {
  active?: boolean;
  emptyLabel?: string;
  headerAction?: ReactNode;
  loadTree?: LoadFileTree;
  refreshLabel?: string;
  rootKey?: string;
  title?: string;
};

export function FileTreePanel({
  active = true,
  emptyLabel = "This directory is empty.",
  headerAction,
  loadTree = getFileTree,
  refreshLabel = "Refresh workspace",
  rootKey = "workspace",
  title = "Workspace",
}: FileTreePanelProps) {
  const [tree, setTree] = useState<FileNode | null>(null);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [previewFile, setPreviewFile] = useState<FileContentResponse | null>(null);
  const treeRef = useRef<FileNode | null>(null);
  const refreshAbortRef = useRef<AbortController | null>(null);
  const previewAbortRef = useRef<AbortController | null>(null);

  useLayoutEffect(() => {
    refreshAbortRef.current?.abort();
    previewAbortRef.current?.abort();
    refreshAbortRef.current = null;
    previewAbortRef.current = null;
    treeRef.current = null;
    setTree(null);
    setLoading(true);
    setRefreshing(false);
    setError(null);
    setPreviewFile(null);
  }, [rootKey]);

  const refreshWorkspace = useCallback(() => {
    if (!active) return;
    refreshAbortRef.current?.abort();
    const controller = new AbortController();
    refreshAbortRef.current = controller;
    setRefreshing(true);
    if (!treeRef.current) setLoading(true);
    loadWorkspaceSnapshot({
      loadTree,
      loadContent: getFileContent,
      previewPath: previewFile?.path,
      signal: controller.signal,
    })
      .then((snapshot) => {
        if (refreshAbortRef.current !== controller) return;
        treeRef.current = snapshot.tree;
        setTree(snapshot.tree);
        if (snapshot.previewFile) setPreviewFile(snapshot.previewFile);
        setError(null);
      })
      .catch((e) => {
        if (isAbortError(e)) return;
        if (refreshAbortRef.current !== controller) return;
        console.error(e);
        setError("Failed to load directory.");
      })
      .finally(() => {
        if (refreshAbortRef.current !== controller) return;
        refreshAbortRef.current = null;
        setLoading(false);
        setRefreshing(false);
      });
  }, [active, loadTree, previewFile?.path]);

  useEffect(() => {
    if (!active) return;
    refreshWorkspace();
    const interval = window.setInterval(refreshWorkspace, WORKSPACE_REFRESH_INTERVAL_MS);
    return () => {
      window.clearInterval(interval);
      refreshAbortRef.current?.abort();
      refreshAbortRef.current = null;
      previewAbortRef.current?.abort();
    };
  }, [active, refreshWorkspace, rootKey]);

  const handleRefreshClick = () => {
    refreshWorkspace();
  };

  const handleFileClick = async (path: string) => {
    previewAbortRef.current?.abort();
    const controller = new AbortController();
    previewAbortRef.current = controller;
    try {
      const content = await getFileContent(path, controller.signal);
      if (previewAbortRef.current === controller) {
        setPreviewFile(content);
      }
    } catch (e) {
      if (isAbortError(e)) return;
      console.error(e);
      const message = e instanceof Error ? e.message : "Failed to load file content.";
      if (previewAbortRef.current === controller) {
        setPreviewFile({ path, content: message, size: 0, truncated: false });
      }
    } finally {
      if (previewAbortRef.current === controller) {
        previewAbortRef.current = null;
      }
    }
  };

  return (
    <div className="flex h-full min-w-0 flex-col bg-card text-card-foreground">
      <div className="flex h-[var(--juex-header-height)] shrink-0 items-center justify-between gap-2 border-b px-4 pr-12 font-sans text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground xl:pr-4">
        <div className="flex min-w-0 items-center gap-1">
          <span className="truncate">{title}</span>
          {headerAction}
        </div>
        <TooltipProvider>
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                type="button"
                variant="ghost"
                size="icon"
                className="size-7 text-muted-foreground hover:text-foreground"
                onClick={handleRefreshClick}
                disabled={refreshing}
                aria-label={refreshLabel}
              >
                <RefreshCw
                  className={cn(
                    "size-3.5 motion-reduce:animate-none",
                    refreshing && "animate-spin",
                  )}
                />
              </Button>
            </TooltipTrigger>
            <TooltipContent>{refreshLabel}</TooltipContent>
          </Tooltip>
        </TooltipProvider>
      </div>
      {error ? (
        <div
          role="alert"
          className="border-b border-destructive/25 bg-destructive/10 px-4 py-2 text-xs text-destructive"
        >
          {error}
          {tree ? " Showing the last loaded snapshot." : ""}
        </div>
      ) : null}
      <ScrollArea className="flex-1 p-3">
        {loading ? (
          <div className="p-2 text-sm text-muted-foreground motion-safe:animate-pulse">
            Loading...
          </div>
        ) : tree && tree.is_dir && !tree.children_truncated && !tree.children?.length ? (
          <div className="p-2 text-sm text-muted-foreground">{emptyLabel}</div>
        ) : tree ? (
          <TreeNode node={tree} depth={0} onFileClick={handleFileClick} />
        ) : (
          <div className="p-2 text-sm text-muted-foreground">
            Workspace unavailable.
          </div>
        )}
      </ScrollArea>

      <Sheet open={!!previewFile} onOpenChange={(open) => !open && setPreviewFile(null)}>
        <SheetContent
          className="flex !w-full !max-w-none flex-col gap-0 border-l bg-card p-0 sm:!max-w-xl"
          side="right"
        >
          <SheetHeader className="border-b p-4">
            <SheetTitle className="break-all pr-8 font-mono text-sm text-foreground">
              {previewFile?.path}
            </SheetTitle>
            <SheetDescription className="sr-only">
              File preview for {previewFile?.path}
            </SheetDescription>
            {previewFile?.truncated && (
              <div className="text-xs text-muted-foreground">
                Preview truncated at 256 KB.
              </div>
            )}
          </SheetHeader>
          <div className="flex-1 overflow-auto bg-muted/40 p-4">
            {previewFile?.kind === "image" ? (
              <div className="flex min-h-full items-center justify-center">
                <img
                  src={getFileRawURL(previewFile.path)}
                  alt={`Preview of ${previewFile.path}`}
                  className="max-h-full max-w-full rounded-md object-contain shadow-[var(--shadow-sm)]"
                />
              </div>
            ) : (
              <pre className="whitespace-pre-wrap break-words font-mono text-xs">
                {previewFile?.content}
              </pre>
            )}
          </div>
        </SheetContent>
      </Sheet>
    </div>
  );
}

function TreeNode({
  node,
  depth,
  onFileClick,
}: {
  node: FileNode;
  depth: number;
  onFileClick: (path: string) => void;
}) {
  const [expanded, setExpanded] = useState(depth === 0);

  if (!node.is_dir) {
    return (
      <button
        type="button"
        className="flex min-h-10 w-full min-w-0 items-center gap-1.5 rounded-[6px] px-2 py-1.5 text-left font-mono text-[12.5px] outline-none hover:bg-muted hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/35 sm:min-h-8"
        onClick={() => onFileClick(node.path)}
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
