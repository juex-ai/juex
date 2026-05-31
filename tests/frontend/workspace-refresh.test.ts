import assert from "node:assert/strict";
import test from "node:test";
import type { FileContentResponse, FileNode } from "../../frontend/src/types.ts";
import { loadWorkspaceSnapshot } from "../../frontend/src/lib/workspace-refresh.ts";

const root = (children: FileNode[] = []): FileNode => ({
  name: "work",
  path: "/",
  is_dir: true,
  children,
});

test("loadWorkspaceSnapshot refreshes tree and open preview content", async () => {
  const tree = root([
    { name: "notes.txt", path: "notes.txt", is_dir: false },
  ]);
  const preview: FileContentResponse = {
    path: "notes.txt",
    content: "fresh",
    size: 5,
    truncated: false,
  };

  const snapshot = await loadWorkspaceSnapshot({
    previewPath: "notes.txt",
    loadTree: async () => tree,
    loadContent: async (path) => {
      assert.equal(path, "notes.txt");
      return preview;
    },
  });

  assert.equal(snapshot.tree, tree);
  assert.deepEqual(snapshot.previewFile, preview);
});

test("loadWorkspaceSnapshot keeps tree refresh when open preview fails", async () => {
  const tree = root([]);

  const snapshot = await loadWorkspaceSnapshot({
    previewPath: "removed.txt",
    loadTree: async () => tree,
    loadContent: async () => {
      throw new Error("file not found");
    },
  });

  assert.equal(snapshot.tree, tree);
  assert.deepEqual(snapshot.previewFile, {
    path: "removed.txt",
    content: "file not found",
    size: 0,
    truncated: false,
  });
});
