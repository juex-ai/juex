import { useEffect, useRef, useState } from "react";
import { getFileTree, getFileContent } from "@/api";
import type { FileContentResponse, FileNode } from "@/types";
import {
  Folder,
  FolderOpen,
  File as FileIcon,
  ChevronRight,
  ChevronDown,
} from "lucide-react";
import { cn } from "@/lib/utils";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { ScrollArea } from "@/components/ui/scroll-area";

export function FileTreePanel() {
  const [tree, setTree] = useState<FileNode | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [previewFile, setPreviewFile] = useState<FileContentResponse | null>(null);
  const previewAbortRef = useRef<AbortController | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    let live = true;
    getFileTree(controller.signal)
      .then((next) => {
        if (!live) return;
        setTree(next);
        setError(null);
      })
      .catch((e) => {
        if (isAbortError(e)) return;
        if (!live) return;
        console.error(e);
        setError("Failed to load directory.");
      })
      .finally(() => {
        if (live) setLoading(false);
      });
    return () => {
      live = false;
      controller.abort();
      previewAbortRef.current?.abort();
    };
  }, []);

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
      <div className="flex h-[var(--juex-header-height)] shrink-0 items-center border-b px-4 pr-12 font-sans text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground xl:pr-4">
        Workspace
      </div>
      <ScrollArea className="flex-1 p-3">
        {loading ? (
          <div className="animate-pulse p-2 text-sm text-muted-foreground">Loading...</div>
        ) : tree ? (
          <TreeNode node={tree} depth={0} onFileClick={handleFileClick} />
        ) : (
          <div className="p-2 text-sm text-muted-foreground">{error}</div>
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
            <pre className="whitespace-pre-wrap break-words font-mono text-xs">
              {previewFile?.content}
            </pre>
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
        className="flex w-full min-w-0 items-center gap-1.5 rounded-[6px] px-2 py-1 text-left font-mono text-[12.5px] hover:bg-muted hover:text-foreground"
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
          "flex w-full min-w-0 items-center gap-1.5 rounded-[6px] px-2 py-1 text-left font-mono text-[12.5px] hover:bg-muted hover:text-foreground",
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
