import type { FileContentResponse, FileNode } from "../types";

export type LoadFileTree = (signal?: AbortSignal) => Promise<FileNode>;
export type LoadFileContent = (
  path: string,
  signal?: AbortSignal,
) => Promise<FileContentResponse>;

export type WorkspaceSnapshot = {
  tree: FileNode;
  previewFile?: FileContentResponse;
  previewError?: string;
};

export async function loadWorkspaceSnapshot({
  loadTree,
  loadContent,
  previewPath,
  signal,
}: {
  loadTree: LoadFileTree;
  loadContent: LoadFileContent;
  previewPath?: string | null;
  signal?: AbortSignal;
}): Promise<WorkspaceSnapshot> {
  const treePromise = loadTree(signal);
  if (!previewPath) {
    return { tree: await treePromise };
  }

  const previewPromise = loadContent(previewPath, signal).then(previewFile => ({ previewFile })).catch((error) => {
    if (isAbortError(error)) throw error;
    return { previewError: error instanceof Error ? error.message : "Failed to load file content." };
  });
  const [tree, preview] = await Promise.all([treePromise, previewPromise]);
  return { tree, ...preview };
}

function isAbortError(error: unknown): boolean {
  return error instanceof DOMException && error.name === "AbortError";
}
