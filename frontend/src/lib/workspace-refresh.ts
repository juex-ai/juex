import type { FileContentResponse, FileNode } from "../types";

export type LoadFileTree = (signal?: AbortSignal) => Promise<FileNode>;
export type LoadFileContent = (
  path: string,
  signal?: AbortSignal,
) => Promise<FileContentResponse>;

export type WorkspaceSnapshot = {
  tree: FileNode;
  previewFile?: FileContentResponse;
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

  const previewPromise = loadContent(previewPath, signal).catch((error) =>
    fileContentError(previewPath, error),
  );
  const [tree, previewFile] = await Promise.all([treePromise, previewPromise]);
  return { tree, previewFile };
}

function fileContentError(path: string, error: unknown): FileContentResponse {
  const content = error instanceof Error ? error.message : "Failed to load file content.";
  return { path, content, size: 0, truncated: false };
}
