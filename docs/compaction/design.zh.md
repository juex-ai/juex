# 上下文压缩 V2 设计

> [English](design.md) | 中文

日期：2026-06-04

## 目标

Juex 应支持很长的本地 Agent Session，同时不让上下文无界增长、不浪费 prompt-cache locality，并且在重复压缩后不丢失重要任务状态。

V2 保留 V1 append-only transcript model，但在 Provider Call 前增加 cache-aware projection layer。

## 当前实现状态

已实现：

- 过大的用户输入和 Tool Result 会实体化到 Agent Artifact root，并在 Provider request 前替换为稳定的 Provider-visible preview。
- 恢复的 legacy history 会在 Provider Call 前投影，即使原始 `conversation.jsonl` row 早于 artifact metadata。
- `/compact [instructions]`、`juex sessions compact --instructions` 与 Web compact API 可以向 summary prompt 传递 focus instruction。
- Adapter 支持时，OpenAI-compatible Provider 发送稳定的 per-Session prompt cache key；Anthropic Provider 在稳定 prompt section 设置 ephemeral `cache_control` breakpoint。Provider 报告的 cached input token 记录到 usage/context Event。
- 自动压缩具有连续失败 circuit breaker。
- Summary generation 遍历可选专用 summary model、effective primary 与有序 fallback model。它去重 ref、遵守共享 Model-health cooldown 与 half-open reservation，并针对每个选中 candidate 的 context window 重新适配 request。Candidate-specific fitting 只删除最旧的完整 Tool Call/Tool Result batch，并保留所有 user-authored message；无法进一步缩减且过大的 request 会在 Provider dispatch 前跳过该 candidate。

未来工作：

- Provider-native Responses compaction item。
- 延迟加载 MCP Tool definition。
- 每次重大 context-management 变更后，针对完整 Provider matrix 刷新 live scorecard。

## 非目标

- 不删除或重写原始 transcript row。
- 不要求所有 Provider 都支持同一项 Provider-specific feature。
- 不把 compaction state 隐藏在不透明本地数据库中。
- 不把大型 multi-agent memory system 作为本次变更的一部分。

Compaction marker 仍是普通 canonical transcript row。Session metadata 可以缓存最新 marker 及其显式 retained message 的有界 byte location，但该派生 checkpoint 必须带 fingerprint、可丢弃，并可从 `conversation.jsonl` 重建。Checkpoint 中 repair-safe marker 只对匹配 transcript fingerprint 有效；存在 unresolved Tool Call 或未验证 hidden prefix 时，必须 canonical repair scan。

## 架构

V2 是四阶段上下文 pipeline：

```text
raw transcript
  -> entry budgeter
  -> stable active-context projection
  -> provider-specific cache/compact adapter
  -> provider request
```

Raw transcript 保持事实来源。Active context 可以用稳定 reference 替换旧大型 block、保留近期 raw tail message，并插入 compact marker。

## 阶段 1：Entry Budgeter

大型用户输入与 Tool Result 应在成为每个未来 Provider request 的一部分之前得到控制。V1 可在 Turn 前压缩旧 history，但不能缩小 incoming user message 本身。因此即使 compaction 成功，一段粘贴日志或生成 prompt 仍可能让 Provider request 远大于配置的 Juex context window。

Runtime-owned materialization layer 直接在受影响 block 上记录 artifact metadata：

```go
type ContextArtifactProjection struct {
    SourceKind    string // "user_input", "tool_result"
    MessageID     string
    ToolUseID     string
    ToolName      string
    OriginalBytes int
    StoredPath    string // Agent Artifact root-relative reference
    SHA256        string
    HeadBytes     int
    TailBytes     int
    Truncated     bool
}
```

Tool output 超过 `tool_output.inline_max_bytes` 时，无论是否启用 compaction，Juex 都把完整输出写入：

```text
sessions/<session-id>/tool-results/<tool-use-id>-<block-index>.txt
```

用户输入超过 `compaction.user_input_inline_max_bytes` 时，Juex 把完整输入写入：

```text
sessions/<session-id>/user-inputs/<message-id>-<block-index>.txt
```

Provider-visible Tool Result 变为稳定 text block：

```text
Tool output stored outside context.
tool_use_id: <id>
tool_name: <name>
bytes: <n>
sha256: <hash>
path: <Agent Artifact root-relative path>

Preview:
<head>
...
<tail>
```

Replacement decision 由原始 `tool_use_id` 冻结。如果同一 historical result 在之后 Turn 再次投影，文本必须逐字节一致，从而保护 prefix-cache hit。

默认策略：

```yaml
compaction:
  user_input_inline_max_bytes: 65536
  user_input_preview_head_bytes: 8192
  user_input_preview_tail_bytes: 8192
```

由 context window 派生的默认值为：70% 自动压缩触发点、80% 完整 summary request envelope；initial summary output、summary Tool Result serialization 与普通 Tool Result projection 各占 0.5%；retained recent tail 占 5/64。正的绝对值是更严格 ceiling，`reserve_tokens` 只能让触发提前。单次 incomplete-summary retry 请求至少 2,048 output token，或 initial budget 的两倍，取较大者。显式正 `summary_max_tokens` 的两倍仍是 ceiling，完整 request 还必须适配 80% envelope。

理由：

- 完整证据仍可通过路径恢复。
- 模型保留足够 head/tail 信号来判断是否读取文件。
- 上下文窗口填充时，旧 prefix text 不会持续变化。

## 阶段 2：稳定 Active-Context Projection

V1 Active context 是：

```text
latest compact summary + retained tail + messages after compact + incoming
```

V2 在此基础上增加 projection pass：

```go
// internal/runtime/compaction_policy.go and tool_output_policy.go
type compactionPolicy struct {
    Enabled                   bool
    ContextWindow             int
    ReserveTokens             int
    KeepRecentTokens          int
    SummaryRequestTokens      int
    SummaryMaxTokens          int
    ToolResultMaxChars        int
    UserInputInlineMaxBytes   int
    UserInputPreviewHeadBytes int
    UserInputPreviewTailBytes int
    MaxAutoFailures           int
    TriggerTokens             int
}

type ToolOutputPolicy struct {
    InlineMaxBytes   int
    PreviewHeadBytes int
    PreviewTailBytes int
}
```

Projection 规则：

1. 除 compact boundary 外，绝不改变旧 projected text。
2. 始终保持 Provider protocol 有效：Tool output 必须与 Tool Call 匹配。
3. 只有近期 input 能装入配置 token budget 时才逐字保留；在 compact boundary，若某个 input 自身大于该 budget，则将它外置，并与 summary 一起保留有界 head/tail artifact reference。同一 input 中所有 text block 共用该 reference preview budget。仅图片 input 会在同一 retained-reference section 中保留 durable media path、type、digest、byte size 与 dimension。
4. 在 compaction metadata 中持久化 retained input reference，并在后续 compaction 中确定性继承；Model summary 不是 artifact path 或 digest 的 authority。只保留能装入共享 retention budget 的最新完整 reference suffix，使 metadata 与 compact text 不无界增长。后续 summarization 只反馈之前模型生成的 summary；deterministic reference section 通过 metadata 传递，不再被总结。
5. Compact summary 保持简短且结构化；不要要求它携带 system instruction、AGENTS.md、Tool schema 或 cwd，这些会重建。
6. Assistant text/reasoning projection 是未来工作。目前 reasoning replay 由 Provider capability 与已有 block metadata 控制。

这仍是 Runtime 职责，不是 Provider 职责。

## 阶段 3：Cache-Aware Prompt 布局

Juex 应让 prompt stability 显式。Prompt section 在 `internal/prompt` 已有 key；Provider Adapter 应接收从这些 key 派生的 cache plan。

当前 request option：

```go
type CachePolicy struct {
    StablePrefixKey string
    Retention       string
}

type CompleteOptions struct {
    Purpose         string
    MaxOutputTokens int
    CachePolicy     CachePolicy
}
```

Provider 映射：

- `openai/chat`、`openai/responses` 与 `openai-codex/responses`：支持时设置 `prompt_cache_key`，并记录 Provider cached-token detail。
- `anthropic/messages`：在稳定 section boundary 放置 `cache_control` breakpoint。存在 cache policy 时，当前 Adapter 标记 system prompt 与最后一个 Tool definition，并记录 `usage.cache_read_input_tokens`。
- 未知 compatible Provider：在 Provider 暴露等价字段前 no-op，但保持相同 Runtime metrics shape。

推荐 prompt 顺序：

```text
tool schemas
global and project instructions
stable workspace context
selected Extension guidance
latest compact summary
retained recent tail
volatile incoming message
```

Volatile tail 有意放在最后。

## 阶段 4：Compaction Strategy Interface

Compaction 的 Provider 特异性足够高，应成为可选 Provider capability。

```go
type CompactionRequest struct {
    SystemPrompt string
    History      []Message
    Tools        []ToolSpec
    Policy       compactionPolicy
    Reason       string
}

type CompactionArtifact struct {
    Message          Message
    Opaque           bool
    Replacement      []Message
    InputTokens      int
    CachedInputTokens int
    OutputTokens     int
}

type ProviderCompactor interface {
    CompactContext(ctx context.Context, req CompactionRequest) (CompactionArtifact, error)
}
```

策略顺序：

1. Provider 可生成 Provider-native compact item 或 replacement history 时使用原生 compaction。
2. 使用当前 Juex summary prompt 与有界 serialized transcript 的本地 structured summary。
3. 只有 summary request 无法适配时才做最后手段的 deterministic trim。

OpenAI/Codex Provider 最终应优先原生 Responses compaction。通用 `openai/chat`、Ark、DeepSeek 和本地 proxy 应从本地 structured summary 开始，除非显式声明原生支持。

## 触发策略

V1 根据估算总 context 触发。V2 应同时支持总量与 baseline 后增长：

```go
type CompactWindow struct {
    BaselineInputTokens int
    BaselineMessageID   string
    LastCompactID       string
}
```

触发点：

- Pre-turn：projected Active context 加 incoming message 将超过选中 candidate 配置 context window 的 70%，或显式 `reserve_tokens` 隐含的更早阈值。
- Mid-turn：每次 Provider Call 前，在 drain pending input 和 Tool Result 后，如果 baseline 后增长超过 trigger。
- Overflow retry：Provider 返回 context overflow error 时。
- Manual：`/compact` 与 `juex sessions compact`。

失败处理：

- 每次 Provider Call 最多 compact retry 一次。
- 一个 Session 内自动 compact 连续失败三次后触发 circuit breaker。
- 手动 compact 始终报告底层 error。
- 主动 compact 失败后 MCP notification Turn 仍可继续，与当前 external notification 行为一致。

## Summary 契约

本地 summary 继续使用固定 heading：

- Goal
- Constraints & Preferences
- Progress
- Key Decisions
- Next Steps
- Critical Context
- Relevant Files
- Tool Failures

V2 增加两条规则：

- Tool Result 被外置时包含 `Evidence References`。
- 早期 transcript 为适配 compaction request 被省略时包含 `Confidence / Missing Context`。
- 复制带 label fact、Task ID、path、command、error string、constraint 与 safety guard 的具体值。不要用“facts were stored”或“available in context”等模糊 placeholder 替代。

Summary 不应重述 AGENTS.md、Tool schema、Provider setting 或当前 cwd，除非它们直接属于任务决策；这些会从来源重建。

## 可观察性

当前发出的 Event：

- `context.projection.applied`
  - `user_inputs_externalized`
  - `tool_results_externalized`
  - `bytes_externalized`
- `context.compact.started`
  - `reason`、`auto`、`estimated_tokens`、`tokens_before`
  - `context_window`、`reserve_tokens`、`keep_recent_tokens`
- `context.compact.completed`
  - `message_id`、`reason`、`auto`
  - `estimated_tokens`、`tokens_before`、`tokens_after`、`summary_chars`、`summary_model`
  - `tail_start_message_id`、`context_window`、`reserve_tokens`、`keep_recent_tokens`
- `context.compact.summary_retry`
  - Summary chain 中首个 incomplete candidate 获得一次有界 semantic retry；Event 记录 empty-summary reason、stop reason、reasoning-only classification、之前与 retry output-token budget，以及失败 attempt 的 Request Epoch link
- `context.compact.summary_model_fallback`
  - 每次 attempted-candidate transition 一个 Event，记录失败 Model、下一个选中 Model（耗尽时为空）、candidate error 与失败 attempt 的 Request Epoch link
- `llm.fallback`
  - 选择 compaction summary candidate 时遇到的 shared-health cooldown 与 `probe_in_flight` skip；这些 diagnostic 不会创建 conversation `model_change` message
- `context.compact.errored` 与 `context.compact.skipped`
  - Compact error 与自动 failure-circuit-breaker state
- `llm.responded`
  - Response usage、累计 token usage、Model、block 与可选 `context_usage`

Provider 暴露时，cached-token metric 进入 `Usage.CachedInputTokens`、`ContextUsage.CachedInputTokens` 与 `llm.responded` usage payload。

计划的 Event 扩展：

- `context.projection.applied` 上的 `projected_tokens`
- `context.compact.started` 上的 `trigger_scope` 与 `growth_tokens`
- `context.compact.completed` 上的 `strategy` 与直接 `cached_input_tokens`

Session `ContextUsage` 记录：

- `system_prompt`
- `system_tools`
- `mcp_tools`
- `skills`
- `compact_summary`
- `context_artifacts`
- `messages`
- `response`
- `cached_input_tokens`

## 评估要求

每次 compaction 变更都应评估：

- 对旧 conversation head、middle 与 tail 事实的 recall。
- Compact 后继续 implementation plan 的能力。
- File path、command、error 与 Task ID 的保留。
- Prompt-cache 行为：第二个 post-compact Turn 后的 cached-token ratio。
- 重复 Tool output 下的 context growth slope。
- Compaction request 本身过大时的 recovery behavior。

快速自动测试应使用正常 256k 窗口十分之一到四分之一之间的 context window。Live evaluation 从已解析 Provider config 派生 eligible Model ref，并记录 selection seed。

## Rollout 计划

1. 先合并文档与可重复 evaluation asset。
2. 先实现 input 与 Tool-result externalization。它能在没有 Provider-specific 风险的情况下实现最大增长削减。
3. 增加 cache-policy field 与 Provider metrics plumbing。
4. 增加 growth-after-baseline trigger scope。
5. 在 capability gate 后增加 Provider-native compaction。
6. 扩展 live eval，只在 scorecard 改善后提升默认值。
