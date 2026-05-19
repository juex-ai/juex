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
  id?: string;
  role: Role;
  blocks?: Block[] | null;
  kind?: string;
  compaction?: CompactionMetadata;
  pending?: boolean;
  turn_id?: string;
  // Model that produced this assistant message. Stamped by the provider at
  // generation time so resumed sessions retain attribution even if the
  // current config has been swapped to a different model.
  model?: string;
}

export interface CompactionMetadata {
  auto?: boolean;
  reason?: string;
  previous_summary_id?: string;
  first_kept_message_id?: string;
  tail_start_message_id?: string;
  tokens_before?: number;
  tokens_after?: number;
  summary_chars?: number;
  summary_model?: string;
}

export interface TokenUsage {
  input_tokens: number;
  output_tokens: number;
}

export interface ContextUsagePart {
  key: string;
  label: string;
  tokens: number;
}

export interface ContextUsage {
  model?: string;
  context_window?: number;
  input_tokens: number;
  output_tokens: number;
  total_tokens: number;
  breakdown?: ContextUsagePart[];
}

export interface SessionInfo {
  id: string;
  dir: string;
  started_at: string;        // RFC3339
  last_active_at: string;    // RFC3339
  turns: number;
  preview: string;
  token_usage: TokenUsage;
  context_usage?: ContextUsage;
}

export interface SessionShowResponse extends SessionInfo {
  messages: Message[];
  model?: string;
}

export interface SessionsListResponse {
  sessions: SessionInfo[];
}

export type CreateSessionResponse = SessionInfo;

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

export interface CompactSessionResponse {
  message_id?: string;
  reason?: string;
  auto?: boolean;
  tokens_before?: number;
  tokens_after?: number;
  summary_chars?: number;
  summary_model?: string;
  tail_start_message_id?: string;
  first_kept_message_id?: string;
}

export interface ActiveContextSnapshot {
  messages: Message[];
  estimated_tokens: number;
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
  children_truncated?: boolean;
  children?: FileNode[];
}

export interface FileContentResponse {
  path: string;
  content: string;
  size: number;
  truncated: boolean;
}

export interface RuntimeStatusResponse {
  mcp: {
    configured: number;
    connected: number;
    errors: number;
    servers: MCPServerInfo[];
  };
  skills: {
    count: number;
    items: SkillInfo[];
  };
}

export interface MCPServerInfo {
  name: string;
  command: string;
  args?: string[];
  status: "not_started" | "connected" | "error";
  connected: boolean;
  tool_count: number;
  error?: string;
}

export interface SkillInfo {
  name: string;
  description: string;
  type?: string;
  source: string;
  path: string;
}
