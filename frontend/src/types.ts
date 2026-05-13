// Mirror of Go types from internal/llm, internal/session, internal/events.
// When the Go side changes, update this file in the same PR.

export type Role = "user" | "assistant" | "system";

export type BlockType = "text" | "reasoning" | "tool_use" | "tool_result";

export interface BlockBase {
  type: BlockType;
}

export interface TextBlock extends BlockBase {
  type: "text";
  text: string;
}

export interface ReasoningBlock extends BlockBase {
  type: "reasoning";
  text?: string;
  content?: string;
  signature?: string;
  redacted?: boolean;
}

export interface ToolUseBlock extends BlockBase {
  type: "tool_use";
  tool_use_id: string;
  tool_name: string;
  input?: Record<string, unknown>;
}

export interface ToolResultBlock extends BlockBase {
  type: "tool_result";
  tool_use_id?: string;
  content: string;
  is_error?: boolean;
}

export type Block = TextBlock | ReasoningBlock | ToolUseBlock | ToolResultBlock;

export interface Message {
  role: Role;
  blocks?: Block[] | null;
  pending?: boolean;
  turn_id?: string;
  // Model that produced this assistant message. Stamped by the provider at
  // generation time so resumed sessions retain attribution even if the
  // current config has been swapped to a different model.
  model?: string;
}

export interface SessionInfo {
  id: string;
  dir: string;
  started_at: string;        // RFC3339
  last_active_at: string;    // RFC3339
  turns: number;
  preview: string;
}

export interface SessionShowResponse extends SessionInfo {
  messages: Message[];
  model?: string;
}

export interface SessionsListResponse {
  sessions: SessionInfo[];
}

export interface CreateSessionResponse {
  id: string;
  dir: string;
  started_at: string;
}

export interface DeleteSessionResponse {
  deleted: boolean;
  id: string;
}

export interface StartTurnResponse {
  turn_id: string;
}

export type TurnState = "running" | "done" | "errored";

export interface TurnStatusResponse {
  state: TurnState;
  error?: string;
}

export interface InterruptResponse {
  cancelled: boolean;
}

export interface BusEvent {
  id: string;
  type: string;             // e.g. "turn.started", "tool.requested"
  timestamp: string;
  turn_id?: string;
  payload?: unknown;
}

export interface FileNode {
  name: string;
  path: string;
  is_dir: boolean;
  children?: FileNode[];
}

export interface FileContentResponse {
  path: string;
  content: string;
  size: number;
  truncated: boolean;
}
