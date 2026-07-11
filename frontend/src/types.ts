// Mirror of Go API/session DTOs and the internal/web browser event contract.
// When the Go side changes, update this file in the same PR.

export type Role = "user" | "assistant" | "system";

export type BlockType = "text" | "image" | "reasoning" | "tool_use" | "tool_result";

export interface BlockBase {
  type: BlockType;
  artifact?: ContextArtifactProjection;
  // UI-local key for provisional provider stream blocks. The final
  // llm.responded payload replaces these blocks with canonical history data.
  stream_index?: number;
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

export interface MediaRef {
  artifact_path?: string;
  media_type?: string;
  sha256?: string;
  original_bytes?: number;
  width?: number;
  height?: number;
}

export interface ImageBlock extends BlockBase {
  type: "image";
  media?: MediaRef;
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
  media?: MediaRef;
  is_error?: boolean;
  // UI-local live projection marker. Persisted history omits this field; it
  // lets the session transcript keep streamed tool output in a running state
  // until the final tool.completed/tool.errored event arrives.
  streaming?: boolean;
}

export type Block = TextBlock | ImageBlock | ReasoningBlock | ToolUseBlock | ToolResultBlock;

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
  turn?: SessionTurnStatus;
  goal?: GoalStatusSnapshot;
  working_state?: WorkingStateStatusSnapshot;
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

export interface SessionTurnStatus extends TurnStatusResponse {
  turn_id: string;
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

export const BROWSER_EVENT_TYPES = [
  "turn.started",
  "turn.completed",
  "turn.errored",
  "llm.requested",
  "llm.responded",
  "llm.output_delta",
  "llm.retry",
  "tool.requested",
  "tool.completed",
  "tool.output_delta",
  "tool.errored",
  "hook.started",
  "hook.completed",
  "hook.errored",
  "hook.trace",
  "pending_input.queued",
  "pending_input.drained",
  "pending_input.dropped",
  "pending_input.rejected",
  "goal.updated",
  "observable.started",
  "observable.stopped",
  "observable.exited",
  "observable.errored",
  "observation.recorded",
  "observation.queued",
  "observation.delivered",
  "observation.dropped",
  "context.compact.skipped",
  "context.compact.started",
  "context.compact.completed",
  "context.compact.errored",
  "context.compact.summary_model_fallback",
  "context.projection.applied",
] as const;

export type BrowserEventType = (typeof BROWSER_EVENT_TYPES)[number];

interface BrowserEventBase<TType extends BrowserEventType> {
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
  error_kind?: string;
  timed_out?: boolean;
  raw_cause?: string;
}

export interface LLMRequestedPayload {
  iter: number;
  history_len: number;
  tool_count: number;
}

export interface LLMOutputDeltaPayload {
  iter: number;
  model?: string;
  kind: string;
  index: number;
  text: string;
}

export interface LLMRetryPayload {
  purpose?: string;
  iter?: number;
  provider: string;
  model: string;
  protocol?: string;
  transport?: string;
  operation: string;
  attempt: number;
  max_attempts: number;
  delay_ms?: number;
  retry_reason: string;
  raw_error?: string;
  will_retry: boolean;
  exhausted?: boolean;
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
  result?: Record<string, unknown>;
  media?: MediaRef;
}

export interface ToolOutputDeltaPayload {
  name: string;
  tool_use_id: string;
  session_id: string;
  chunk_id: number;
  stream: string;
  text: string;
  truncated?: boolean;
  binary_omitted?: boolean;
  binary_bytes?: number;
  binary_sha256?: string;
  first_bytes_hex?: string;
}

export interface ToolErroredPayload {
  name: string;
  tool_use_id: string;
  error: string;
  error_kind?: string;
  raw_cause?: string;
  timeout_seconds: number;
  len?: number;
  preview?: string;
  timed_out?: boolean;
  exit_code?: number;
  result?: Record<string, unknown>;
  media?: MediaRef;
}

export interface HookStartedPayload {
  name: string;
  source?: string;
  event_name: string;
  tool_name?: string;
}

export interface HookCompletedPayload extends HookStartedPayload {
  duration_ms: number;
  decision?: string;
  additional_context_len?: number;
  block_stop?: boolean;
  continue_prompt_len?: number;
  stdout_len?: number;
  stderr_len?: number;
  stdout_preview?: string;
  stderr_preview?: string;
}

export interface HookErroredPayload extends HookStartedPayload {
  duration_ms: number;
  error: string;
  stdout_len?: number;
  stderr_len?: number;
  stdout_preview?: string;
  stderr_preview?: string;
}

export interface HookTracePayload {
  text: string;
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

export type ObservableRunState =
  | "starting"
  | "running"
  | "stopped"
  | "exited"
  | "errored";

export type ObservationState =
  | "recorded"
  | "queued"
  | "delivered"
  | "dropped";

export interface ObservableBatchSpec {
  interval_seconds: number;
  max_chars: number;
}

export interface ObservableScheduleStatus {
  summary?: string;
  timezone?: string;
  catch_up_mode?: string;
  next_occurrence?: string;
  last_evaluated_at?: string;
  last_emitted_scheduled_at?: string;
}

export interface ObservableStatus {
  id: string;
  name?: string;
  source_type?: "command" | "schedule" | string;
  command: string;
  args?: string[];
  streams?: string[];
  batch: ObservableBatchSpec;
  schedule?: ObservableScheduleStatus;
  state: ObservableRunState | string;
  run_id?: string;
  pid?: number;
  started_at?: string;
  exited_at?: string;
  exit_code?: number;
  last_error?: string;
  last_observation?: ObservationRecord;
}

export interface ObservationRecord {
  id: string;
  observable_id: string;
  run_id?: string;
  source_event_id?: string;
  kind: string;
  severity: string;
  stream?: string;
  window_start: number;
  window_end: number;
  content: string;
  original_chars: number;
  truncated?: boolean;
  artifact_path?: string;
  state: ObservationState | string;
  target_session?: string;
  pending_input_id?: string;
  created_at: number;
  delivered_at?: number;
  error?: string;
}

export interface ObservableEventPayload {
  id: string;
  name?: string;
  state: ObservableRunState | string;
  run_id?: string;
  pid?: number;
  exit_code?: number;
  error?: string;
}

export interface ObservationEventPayload {
  observation: ObservationRecord;
  error?: string;
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

export interface ContextCompactSummaryFallbackPayload {
  configured_model?: string;
  fallback_model?: string;
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

export type GoalUpdatedPayload = GoalStatusSnapshot;

export type BrowserEvent =
  | (BrowserEventBase<"turn.started"> & { payload: TurnStartedPayload })
  | (BrowserEventBase<"turn.completed"> & { payload: TurnCompletedPayload })
  | (BrowserEventBase<"turn.errored"> & { payload: TurnErroredPayload })
  | (BrowserEventBase<"llm.requested"> & { payload: LLMRequestedPayload })
  | (BrowserEventBase<"llm.responded"> & { payload: LLMRespondedPayload })
  | (BrowserEventBase<"llm.output_delta"> & { payload: LLMOutputDeltaPayload })
  | (BrowserEventBase<"llm.retry"> & { payload: LLMRetryPayload })
  | (BrowserEventBase<"tool.requested"> & { payload: ToolRequestedPayload })
  | (BrowserEventBase<"tool.completed"> & { payload: ToolCompletedPayload })
  | (BrowserEventBase<"tool.output_delta"> & { payload: ToolOutputDeltaPayload })
  | (BrowserEventBase<"tool.errored"> & { payload: ToolErroredPayload })
  | (BrowserEventBase<"hook.started"> & { payload: HookStartedPayload })
  | (BrowserEventBase<"hook.completed"> & { payload: HookCompletedPayload })
  | (BrowserEventBase<"hook.errored"> & { payload: HookErroredPayload })
  | (BrowserEventBase<"hook.trace"> & { payload: HookTracePayload })
  | (BrowserEventBase<"pending_input.queued"> & { payload: PendingInputQueuedPayload })
  | (BrowserEventBase<"pending_input.drained"> & { payload: PendingInputDrainedPayload })
  | (BrowserEventBase<"pending_input.dropped"> & { payload: PendingInputDroppedPayload })
  | (BrowserEventBase<"pending_input.rejected"> & { payload: PendingInputRejectedPayload })
  | (BrowserEventBase<"goal.updated"> & { payload: GoalUpdatedPayload })
  | (BrowserEventBase<"observable.started"> & { payload: ObservableEventPayload })
  | (BrowserEventBase<"observable.stopped"> & { payload: ObservableEventPayload })
  | (BrowserEventBase<"observable.exited"> & { payload: ObservableEventPayload })
  | (BrowserEventBase<"observable.errored"> & { payload: ObservableEventPayload })
  | (BrowserEventBase<"observation.recorded"> & { payload: ObservationEventPayload })
  | (BrowserEventBase<"observation.queued"> & { payload: ObservationEventPayload })
  | (BrowserEventBase<"observation.delivered"> & { payload: ObservationEventPayload })
  | (BrowserEventBase<"observation.dropped"> & { payload: ObservationEventPayload })
  | (BrowserEventBase<"context.compact.skipped"> & { payload: ContextCompactSkippedPayload })
  | (BrowserEventBase<"context.compact.started"> & { payload: ContextCompactStartedPayload })
  | (BrowserEventBase<"context.compact.completed"> & { payload: ContextCompactCompletedPayload })
  | (BrowserEventBase<"context.compact.errored"> & { payload: ContextCompactErroredPayload })
  | (BrowserEventBase<"context.compact.summary_model_fallback"> & { payload: ContextCompactSummaryFallbackPayload })
  | (BrowserEventBase<"context.projection.applied"> & { payload: ContextProjectionAppliedPayload });

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
  sandbox: {
    enabled: boolean;
    file_system: {
      outside_workspace: "read_write" | "read_only" | "denied" | string;
    };
    network: {
      enabled: boolean;
    };
  };
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
  hooks: RuntimeHooksStatus;
  skills: {
    count: number;
    items: SkillInfo[];
    filtered?: SkillFilteredInfo[];
    prompt: SkillPromptStatus;
  };
}

export interface ObservablesListResponse {
  observables: ObservableStatus[];
}

export interface ObservableDetailResponse {
  observable: ObservableStatus;
  observations: ObservationRecord[];
}

export interface ObservableObservationsResponse {
  observations: ObservationRecord[];
}

export interface ObservableCreateRequest {
  id: string;
  name?: string;
  command: string;
  args?: string[];
  cwd?: string;
  env?: Record<string, string>;
  streams?: string[];
  defaults?: {
    kind?: string;
    severity?: string;
  };
  parser?: {
    type: "text" | "jsonl" | string;
    content_field?: string;
    kind_field?: string;
    severity_field?: string;
    time_field?: string;
  };
  filters?: Array<{
    contains?: string;
    regex?: string;
    kind?: string;
    severity?: string;
  }>;
  batch: ObservableBatchSpec;
  on_exit?: {
    notify?: "never" | "always" | "nonzero" | string;
  };
}

export interface GoalStatusSnapshot {
  description?: string;
  acceptance_criteria?: string[];
  required_artifacts?: string[];
  artifact_requirements?: string[];
  validation_requirements?: string[];
  verification_method?: string;
  continuation_count?: number;
  status?: string;
  status_reason?: string;
  updated_at?: string;
}

export interface RuntimeHooksStatus {
  configured: number;
  commands: RuntimeHookInfo[];
}

export interface RuntimeHookInfo {
  name: string;
  source?: string;
  events: string[];
  tools?: string[];
  command: string[];
  timeout_seconds: number;
  max_output_bytes: number;
}

export interface WorkingStateStatusSnapshot {
  path?: string;
  disabled?: boolean;
  present: boolean;
  state: WorkingState;
}

export interface WorkingState {
  version: number;
  updated_at?: string;
  goal?: WorkingStateRecord;
  hard_constraints?: WorkingStateRecord[];
  artifacts?: WorkingStateRecord[];
  checks?: WorkingStateRecord[];
  open_issues?: WorkingStateRecord[];
  tool_failures?: WorkingStateRecord[];
  last_successful_checks?: WorkingStateRecord[];
  stale_checks?: WorkingStateRecord[];
  active_processes?: WorkingStateRecord[];
  runtime_budget?: WorkingStateRecord[];
}

export interface WorkingStateRecord {
  id?: string;
  text?: string;
  source?: string;
  confidence?: number;
  severity?: string;
  related_paths?: string[];
  created_at?: string;
  resolved_at?: string;
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

export interface SkillFilteredInfo {
  name: string;
  source: string;
  reason: string;
}

export interface SkillPromptStatus {
  budget_chars: number;
  used_chars: number;
  compacted: boolean;
  omitted?: SkillFilteredInfo[];
}
