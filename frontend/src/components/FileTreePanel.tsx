import { useEffect, useState } from "react";
import { getFileTree, getFileContent } from "@/api";
import type { FileContentResponse, FileNode } from "@/types";
import { Folder, FolderOpen, File as FileIcon, ChevronRight, ChevronDown } from "lucide-react";
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

  useEffect(() => {
    getFileTree()
      .then((next) => {
        setTree(next);
        setError(null);
      })
      .catch((e) => {
        console.error(e);
        setError("Failed to load directory.");
      })
      .finally(() => setLoading(false));
  }, []);

  const handleFileClick = async (path: string) => {
    try {
      setPreviewFile(await getFileContent(path));
    } catch (e) {
      console.error(e);
      const message = e instanceof Error ? e.message : "Failed to load file content.";
      setPreviewFile({ path, content: message, size: 0, truncated: false });
    }
  };

  return (
    <div className="flex h-full flex-col bg-sidebar text-sidebar-foreground">
      <div className="flex h-12 shrink-0 items-center border-b p-3 text-sm font-medium">
        Workspace
      </div>
      <ScrollArea className="flex-1 p-2">
        {loading ? (
          <div className="animate-pulse p-2 text-sm text-muted-foreground">Loading...</div>
        ) : tree ? (
          <TreeNode node={tree} depth={0} onFileClick={handleFileClick} />
        ) : (
          <div className="p-2 text-sm text-muted-foreground">{error}</div>
        )}
      </ScrollArea>

      <Sheet open={!!previewFile} onOpenChange={(open) => !open && setPreviewFile(null)}>
        <SheetContent className="flex w-full flex-col gap-0 border-l p-0 sm:max-w-xl" side="right">
          <SheetHeader className="border-b p-4">
            <SheetTitle className="break-all pr-8 font-mono text-sm">
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
          <div className="flex-1 overflow-auto bg-muted/30 p-4">
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
        className="flex w-full min-w-0 items-center gap-1.5 rounded-md px-2 py-1 text-left text-sm hover:bg-sidebar-accent hover:text-sidebar-accent-foreground"
        onClick={() => onFileClick(node.path)}
      >
        <FileIcon className="size-4 shrink-0 text-muted-foreground" />
        <span className="truncate">{node.name}</span>
      </button>
    );
  }

  return (
    <div className="flex flex-col">
      <button
        type="button"
        className={cn(
          "flex w-full min-w-0 items-center gap-1.5 rounded-md px-2 py-1 text-left text-sm hover:bg-sidebar-accent hover:text-sidebar-accent-foreground",
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
          <FolderOpen className="size-4 shrink-0 text-amber-500" />
        ) : (
          <Folder className="size-4 shrink-0 text-amber-500" />
        )}
        <span className="truncate">{node.name}</span>
      </button>
      {expanded && node.children && (
        <div className="ml-3 flex flex-col border-l border-sidebar-border pl-2">
          {node.children.map((child) => (
            <TreeNode key={child.path} node={child} depth={depth + 1} onFileClick={onFileClick} />
          ))}
        </div>
      )}
    </div>
  );
}
