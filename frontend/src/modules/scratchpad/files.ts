import { getModuleFileContent, getModuleFileRawURL, getModuleFileTree, subscribeModuleResource } from "@/api";
import type { FileContribution } from "../types";

export const scratchpadFiles: FileContribution = {
  id: "scratchpad.files", moduleID: "scratchpad", version: 1, order: 30, label: "Scratchpad", slot: "file.root",
  resource: "files", emptyLabel: "No scratchpad files yet.",
  loadTree: (scope, signal) => getModuleFileTree(scope, "scratchpad", "files", signal),
  loadContent: (scope, path, signal) => getModuleFileContent(scope, "scratchpad", "files", path, signal),
  rawURL: (scope, path) => getModuleFileRawURL(scope, "scratchpad", "files", path),
  subscribe: (scope, receive) => subscribeModuleResource(scope, "scratchpad", "files", receive),
};
