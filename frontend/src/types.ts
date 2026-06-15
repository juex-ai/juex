// Mirror of Go types from internal/llm, internal/session, internal/events.
// When the Go side changes, update this file in the same PR.

export type Role = "user" | "assistant" | "system";

export type BlockType = "text" | "reasoning" | "tool_use" | "tool_result";

export interface BlockBase {
  type: BlockType;
  artifact?: ContextArtifactProjection;
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
  timeout_seconds?: number;
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
  cached_input_tokens?: number;
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
  cached_input_tokens?: number;
  total_tokens: number;
  breakdown?: ContextUsagePart[];
}

export interface ContextArtifactProjection {
  source_kind: "user_input" | "tool_result" | string;
  message_id?: string;
  tool_use_id?: string;
  tool_name?: string;
  original_bytes: number;
  stored_path: string;
  sha256: string;
  head_bytes: number;
  tail_bytes: number;
  truncated: boolean;
}

export interface SessionInfo {
  id: string;
  dir: string;
  kind: "primary" | "side";
  active: boolean;
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
  has_more_before?: boolean;
  oldest_message_id?: string;
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
  turn_id?: string;
  queued?: boolean;
  pending_count?: number;
  max_pending_inputs?: number;
  command?: SlashCommandResponse;
}

export interface SlashCommandResponse {
  name: string;
  text: string;
  compact?: CompactSessionResponse;
  status?: SlashStatusResponse;
}

export interface SlashStatusResponse {
  session_id?: string;
  session_dir?: string;
  session_kind?: "primary" | "side";
  active?: boolean;
  work_dir?: string;
  turns?: number;
  last_active_at?: string;
  provider?: Record<string, unknown>;
  mcp?: Record<string, unknown>;
  skill_count?: number;
  token_usage?: TokenUsage;
  token_total?: number;
  context_usage?: ContextUsage;
  pending_input?: Record<string, unknown>;
}

export type TurnState = "running" | "done" | "errored";

export interface TurnStatusResponse {
  state: TurnState;
  error?: string;
  pending_count?: number;
  max_pending_inputs?: number;
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

interface BusEventBase<TType extends string> {
  id: string;
  type: TType;
  ts: string;
  turn_id?: string;
}

export interface TurnStartedPayload {
  input: string;
  kind?: string;
}

export interface TurnCompletedPayload {
  duration_ms: number;
  output_len: number;
  token_usage: TokenUsage;
}

export interface TurnErroredPayload {
  error: string;
}

export interface LLMRequestedPayload {
  iter: number;
  history_len: number;
  tool_count: number;
}

export interface ToolCallPayload {
  tool_use_id: string;
  name: string;
  input?: Record<string, unknown> | null;
  timeout_seconds: number;
}

export interface LLMRespondedPayload {
  stop_reason: string;
  usage: TokenUsage;
  token_usage: TokenUsage;
  blocks: Block[] | null;
  text: string;
  thinking: string;
  tool_calls: ToolCallPayload[] | null;
  model: string;
  context_usage?: ContextUsage;
}

export interface ToolRequestedPayload {
  name: string;
  input?: Record<string, unknown> | null;
  tool_use_id: string;
  timeout_seconds: number;
}

export interface ToolCompletedPayload {
  name: string;
  tool_use_id: string;
  timeout_seconds: number;
  len: number;
  preview: string;
}

export interface ToolOutputDeltaPayload {
  name: string;
  tool_use_id: string;
  session_id: string;
  chunk_id: number;
  stream: string;
  text: string;
  truncated?: boolean;
}

export interface ToolErroredPayload {
  name: string;
  tool_use_id: string;
  error: string;
  timeout_seconds: number;
  len?: number;
  preview?: string;
  timed_out?: boolean;
  exit_code?: number;
}

export interface PendingInputQueuedPayload {
  input: string;
  kind: string;
  pending_count: number;
  max_pending_inputs: number;
}

export interface PendingInputDrainedPayload {
  count: number;
  pending_count: number;
  max_pending_inputs: number;
}

export type PendingInputDroppedPayload = PendingInputDrainedPayload;

export interface PendingInputRejectedPayload {
  input: string;
  kind: string;
  pending_count: number;
  max_pending_inputs: number;
  reason: string;
}

export interface ContextCompactSkippedPayload {
  reason: string;
  auto: boolean;
  consecutive_failures: number;
  max_auto_failures: number;
  error: string;
}

export interface ContextCompactStartedPayload {
  reason: string;
  auto: boolean;
  estimated_tokens: number;
  tokens_before: number;
  context_window: number;
  reserve_tokens: number;
  keep_recent_tokens: number;
  tail_turns: number;
}

export interface ContextCompactErroredPayload {
  reason: string;
  auto: boolean;
  error: string;
}

export interface ContextCompactCompletedPayload {
  message_id: string;
  reason: string;
  auto: boolean;
  estimated_tokens: number;
  tokens_before: number;
  tokens_after: number;
  summary_chars: number;
  summary_model: string;
  tail_start_message_id: string;
  context_window: number;
  reserve_tokens: number;
  keep_recent_tokens: number;
}

export interface ContextProjectionAppliedPayload {
  user_inputs_externalized: number;
  tool_results_externalized: number;
  bytes_externalized: number;
  reasoning_contents_stripped?: number;
  reasoning_content_bytes_stripped?: number;
}

export type BusEvent =
  | (BusEventBase<"turn.started"> & { payload: TurnStartedPayload })
  | (BusEventBase<"turn.completed"> & { payload: TurnCompletedPayload })
  | (BusEventBase<"turn.errored"> & { payload: TurnErroredPayload })
  | (BusEventBase<"llm.requested"> & { payload: LLMRequestedPayload })
  | (BusEventBase<"llm.responded"> & { payload: LLMRespondedPayload })
  | (BusEventBase<"tool.requested"> & { payload: ToolRequestedPayload })
  | (BusEventBase<"tool.completed"> & { payload: ToolCompletedPayload })
  | (BusEventBase<"tool.output_delta"> & { payload: ToolOutputDeltaPayload })
  | (BusEventBase<"tool.errored"> & { payload: ToolErroredPayload })
  | (BusEventBase<"pending_input.queued"> & { payload: PendingInputQueuedPayload })
  | (BusEventBase<"pending_input.drained"> & { payload: PendingInputDrainedPayload })
  | (BusEventBase<"pending_input.dropped"> & { payload: PendingInputDroppedPayload })
  | (BusEventBase<"pending_input.rejected"> & { payload: PendingInputRejectedPayload })
  | (BusEventBase<"context.compact.skipped"> & { payload: ContextCompactSkippedPayload })
  | (BusEventBase<"context.compact.started"> & { payload: ContextCompactStartedPayload })
  | (BusEventBase<"context.compact.completed"> & { payload: ContextCompactCompletedPayload })
  | (BusEventBase<"context.compact.errored"> & { payload: ContextCompactErroredPayload })
  | (BusEventBase<"context.projection.applied"> & { payload: ContextProjectionAppliedPayload });

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
  kind?: "text" | "image";
  media_type?: string;
  size: number;
  truncated: boolean;
}

export interface ShellProfile {
  profile: string;
  family: string;
  binary: string;
  args?: string[];
  path_style: string;
  host_path_style?: string;
  source: string;
  runtime_os: string;
  runtime_arch: string;
  environment?: string;
}

export interface RuntimeStatusResponse {
  work_dir: string;
  provider: {
    id?: string;
    protocol?: string;
    model?: string;
    base_url?: string;
    capabilities: {
      tools: boolean;
      streaming: boolean;
      reasoning_effort: boolean;
      reasoning_replay: boolean;
      max_output_tokens: boolean;
    };
  };
  shell: ShellProfile;
  system_prompt?: {
    count: number;
    items: SystemPromptEntry[];
  };
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
  goal?: GoalStatusSnapshot;
}

export interface GoalStatusSnapshot {
  objective?: string;
  status?: string;
  evidence?: GoalEvidence[];
  budget?: GoalBudget;
  blocked_reason?: string;
  next_user_input?: string;
  last_progress?: string;
  last_check?: CompletionCheck;
  updated_at?: string;
}

export interface GoalEvidence {
  id?: string;
  kind?: string;
  text?: string;
  source?: string;
  related_paths?: string[];
  created_at?: string;
}

export interface GoalBudget {
  max_continuations?: number;
  continuations_used?: number;
}

export interface CompletionCheck {
  status?: string;
  summary?: string;
  continue_prompt?: string;
  source?: string;
  checked_at?: string;
}

export interface SystemPromptEntry {
  key: string;
  label: string;
  source: string;
  path?: string;
  tokens: number;
  text: string;
}

export interface MCPServerInfo {
  name: string;
  source: string;
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
