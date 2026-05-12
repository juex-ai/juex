import { useEffect, useState } from "react";
import { getFileTree, getFileContent, type FileNode } from "@/api";
import { Folder, FolderOpen, File as FileIcon, ChevronRight, ChevronDown, X } from "lucide-react";
import { cn } from "@/lib/utils";
import { Sheet, SheetContent, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { ScrollArea } from "@/components/ui/scroll-area";

export function FileTreePanel() {
  const [tree, setTree] = useState<FileNode | null>(null);
  const [loading, setLoading] = useState(true);
  const [previewFile, setPreviewFile] = useState<{ path: string; content: string } | null>(null);

  useEffect(() => {
    getFileTree()
      .then(setTree)
      .catch(console.error)
      .finally(() => setLoading(false));
  }, []);

  const handleFileClick = async (path: string) => {
    try {
      const content = await getFileContent(path);
      setPreviewFile({ path, content });
    } catch (e) {
      console.error(e);
      setPreviewFile({ path, content: "Failed to load file content." });
    }
  };

  return (
    <div className="flex flex-col h-full bg-sidebar text-sidebar-foreground">
      <div className="p-3 border-b font-medium text-sm flex items-center h-12 shrink-0">
        Workspace
      </div>
      <ScrollArea className="flex-1 p-2">
        {loading ? (
          <div className="text-sm text-muted-foreground p-2 animate-pulse">Loading...</div>
        ) : tree ? (
          <TreeNode node={tree} onFileClick={handleFileClick} />
        ) : (
          <div className="text-sm text-muted-foreground p-2">Failed to load directory.</div>
        )}
      </ScrollArea>

      <Sheet open={!!previewFile} onOpenChange={(open) => !open && setPreviewFile(null)}>
        <SheetContent className="w-full sm:max-w-xl p-0 flex flex-col gap-0 border-l" side="right">
          <SheetHeader className="p-4 border-b">
            <SheetTitle className="text-sm font-mono break-all pr-8">
              {previewFile?.path}
            </SheetTitle>
          </SheetHeader>
          <div className="flex-1 overflow-auto p-4 bg-muted/30">
            <pre className="text-xs font-mono whitespace-pre-wrap break-all">
              {previewFile?.content}
            </pre>
          </div>
        </SheetContent>
      </Sheet>
    </div>
  );
}

function TreeNode({ node, onFileClick }: { node: FileNode; onFileClick: (path: string) => void }) {
  const [expanded, setExpanded] = useState(true); // default root expanded

  if (!node.is_dir) {
    return (
      <div
        className="flex items-center gap-1.5 py-1 px-2 hover:bg-sidebar-accent hover:text-sidebar-accent-foreground rounded-md cursor-pointer text-sm"
        onClick={() => onFileClick(node.path)}
      >
        <FileIcon className="w-4 h-4 shrink-0 text-muted-foreground" />
        <span className="truncate">{node.name}</span>
      </div>
    );
  }

  return (
    <div className="flex flex-col">
      <div
        className="flex items-center gap-1.5 py-1 px-2 hover:bg-sidebar-accent hover:text-sidebar-accent-foreground rounded-md cursor-pointer text-sm"
        onClick={() => setExpanded(!expanded)}
      >
        {expanded ? (
          <ChevronDown className="w-3.5 h-3.5 shrink-0 opacity-50" />
        ) : (
          <ChevronRight className="w-3.5 h-3.5 shrink-0 opacity-50" />
        )}
        {expanded ? (
          <FolderOpen className="w-4 h-4 shrink-0 text-blue-500" />
        ) : (
          <Folder className="w-4 h-4 shrink-0 text-blue-500" />
        )}
        <span className="truncate font-medium">{node.name}</span>
      </div>
      {expanded && node.children && (
        <div className="ml-3 pl-2 border-l border-sidebar-border flex flex-col">
          {node.children.map((child, i) => (
            <TreeNode key={i} node={child} onFileClick={onFileClick} />
          ))}
        </div>
      )}
    </div>
  );
}
