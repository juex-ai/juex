import type { ComponentType } from "react";
import type { ModuleState } from "../module-schema";
import type { FileContentResponse, FileNode } from "../types";

export interface ModuleScope { agentID: string; threadID: string }
export interface ModuleStatusProps { state: ModuleState; readOnly: boolean; stale?: string }
interface ContributionBase { id: string; moduleID: string; version: number; order: number; label: string }
export interface StatusContribution extends ContributionBase {
  slot: "thread.status";
  Component: ComponentType<ModuleStatusProps>;
}
export interface FileContribution extends ContributionBase {
  slot: "file.root";
  resource: string;
  emptyLabel: string;
  loadTree: (scope: ModuleScope, signal?: AbortSignal) => Promise<FileNode>;
  loadContent: (scope: ModuleScope, path: string, signal?: AbortSignal) => Promise<FileContentResponse>;
  rawURL: (scope: ModuleScope, path: string) => string;
  subscribe: (scope: ModuleScope, receive: () => void) => () => void;
}
export type ModuleContribution = StatusContribution | FileContribution;
