# 上下文压缩调研

> [English](research.md) | 中文

日期：2026-06-04

本文比较 Juex 当前压缩行为与 Codex、Claude Code 及其他上下文工程项目中最强的模式。它是下一轮压缩迭代的调研，不是 changelog。已实现的 V2 详情见 `docs/compaction/design.zh.md`。

## 当前 Juex 基线

Juex 在所有权分层之间保持本地状态可恢复：普通 transcript row 位于 `$JUEX_HOME/agents/<agent-id>/sessions/<session-id>/conversation.jsonl`，过大的用户输入和 Tool result 则在进入 Provider context 前实体化到 Agent 的 Artifact root 下。Compaction 会追加带类型化 metadata 的 `MessageKindCompact` marker，随后活跃 Provider context 按以下方式组装：

```text
latest compact summary
+ retained recent tail before that compact marker
+ messages after the compact marker
+ incoming message
```

该活跃上下文会在 Provider call 前投影。过大的用户输入和 Tool result block 会被替换为稳定的 Provider 可见 preview，其中包含路径、字节数、SHA-256 和 head/tail 摘要。

实现集中在：

- `internal/runtime/compact.go`
- `internal/runtime/compaction_policy.go`
- `internal/runtime/compaction_select.go`
- `internal/runtime/compaction_summary.go`
- `internal/runtime/active_context.go`
- `internal/runtime/context_projection.go`
- `internal/runtime/context_usage.go`

当前优势：

- 完整可恢复的 Session 内容保存在 transcript row 或 artifact 中。
- 显式 compact metadata 记录边界和 token 估算。
- 保留近期 tail，避免只依赖自然语言 summary。
- 选择切点时保护 Tool use/Tool result 配对。
- 过大的用户输入和成功 Tool output 会在 Provider call 前外置，且稳定 artifact preview 会持久化到 transcript 中。
- Overflow error 会压缩并重试一次。
- Compaction summary request 序列化为一条有界 user message，并限制为最多 16k 估算 request token，避免恢复请求在超大 history 上超时。
- Adapter 支持时，OpenAI-compatible Provider 会收到稳定 prompt-cache key；Anthropic 会在稳定 prompt/tool section 上收到 ephemeral cache-control breakpoint。
- 可用时会在 usage/context event 中记录 Provider 报告的 cached input token。
- 自动压缩有连续失败 circuit breaker；主动压缩失败后，MCP notification Turn 仍可继续。

当前缺口：

- 尚未实现 Provider-native Responses compaction。
- Auto-compaction 使用活跃上下文总大小，而不是稳定 prefix baseline 之后的增长量，因此大的 system/tool prefix 可能使后续压缩过于频繁。
- Assistant text/reasoning block projection 和延迟加载 MCP Tool definition 仍是未来工作。
- 一些 Adapter 支持 prompt-cache retention policy，但它还不是 Runtime 级调优项。
- 每次重大 context-management 变化后都需要刷新 live model scorecard，以便看见 cache ratio 和质量回归。

## OpenAI / Codex 模式

OpenAI prompt caching 文档说明 cache hit 依赖完全一致的 prompt prefix 复用；稳定 instruction 与 example 应放在前面，动态内容放在最后。同一文档还暴露 `prompt_cache_key`、`prompt_cache_retention` 和 `cached_tokens` 记账。这意味着 Runtime 应把 prompt 布局视为架构边界，而不只是字符串。

Responses API 提供原生 `responses.compact` endpoint，返回带 `encrypted_content` 的 compaction item。Provider 支持时，Codex 使用这类不透明原生状态；本地 fallback 则使用模型编写的 handoff summary。

Codex 开源 client 展示了几个值得借鉴的实用模式：

- 在 prompt assembly 前清理上下文。History manager 会在 item 发送前强制保持 Provider 有效的 function-call/output 配对。
- Compact retry 在 `ContextWindowExceeded` 时裁剪最旧本地 history，优先保持近期任务连续性，并让 retry 行为确定。
- Exec output 使用 head/tail buffer 策略，使 command output 仍有用，又不让超大日志中段主导上下文。
- Remote compaction 有 Provider gate path，而不是假装每个模型都支持相同机制。

Codex 给 Juex 的关键启示是：Provider 原生压缩可用时使用它，同时保留确定、有界、可观察的本地 fallback。

## Claude Code / Anthropic 模式

Claude Code 公开文档说明 context window 包含 conversation history、file content、command output、CLAUDE.md、auto memory、已加载 Skill 和 system instruction。文档还说明项目根目录的 `CLAUDE.md` 能在压缩后保留，因为它会从磁盘重新读取并注入。这是正确的心智划分：规则与环境应从来源重建，不能只由 compact summary 携带。

Anthropic prompt-caching 文档在 `tools -> system -> messages` 层级上暴露显式 cache breakpoint。它支持不同 TTL，但约束是更长寿命的 cache entry 必须出现在更短寿命者之前。这自然形成分层 prompt 形态：

```text
tools and stable tool schemas
project/user/system instructions
stable memory and workspace facts
conversation prefix
volatile latest turn
```

关于 Claude Code 的内部本地调研还强调两点：

- Tool result 可以外置到 sidecar 文件，同时模型看到包含 preview、字节数、checksum 和路径的稳定 replacement。对每个 Tool result，replacement decision 一旦做出就冻结，后续 Turn 不会改写旧 prompt prefix 并破坏 cache locality。
- 完整压缩只是一层。更早的层可以为 Tool result 分配预算、在 cache TTL 过期后微压缩 stale Tool output，并维护一个轻量 Session memory 文档，在无需额外 LLM call 的情况下充当 summary。

Claude Code 给 Juex 的关键启示是：在完整压缩前控制增长，并且旧 prefix rewrite decision 一旦出现在 Provider context 就要冻结。

## DeepSeek-Reasonix 模式

DeepSeek-Reasonix 明确围绕 DeepSeek prefix-cache 稳定性设计。它的公开 README 描述了一个由配置和 plugin 驱动的 coding Agent，其 long-session 成本模型依赖 cache-stable Session。产品启示并非 DeepSeek 特有：如果 Provider 的 prefix cache 是 loop 的经济中心，Runtime 就必须把逐字节稳定的 prompt prefix 作为设计目标。

对 Juex 而言，这意味着一个 Provider-neutral cache contract：

- 稳定 prefix identity 是 Runtime 概念。
- Provider Adapter 把它翻译成 `prompt_cache_key`、Anthropic `cache_control` 或 no-op fallback。
- 旧的投影内容保持不可变，直到 compact boundary 开始新的 prefix。

## Juex 设计原则

1. Transcript 保持 append-only，但允许活跃 Provider context 是一个 projection。
2. 外置大型证据，不要通过总结丢掉它。
3. 冻结旧 message 的投影决策。
4. 根据稳定 baseline 之后的增长触发压缩，而不只看总 token。
5. 将任务状态与系统脚手架分离；从文件重建规则。
6. 对 OpenAI/Codex 类 Provider 优先使用原生压缩，对通用 Provider 使用本地 summary fallback。
7. 记录 cache 指标和压缩质量，使变更可以评估，而不是靠猜测。

## 来源

- OpenAI API 文档，prompt caching：
  <https://developers.openai.com/api/docs/guides/prompt-caching>
- OpenAI API reference，Responses compaction：
  <https://developers.openai.com/api/reference/resources/responses/methods/compact>
- OpenAI Codex source，本地压缩：
  <https://github.com/openai/codex/blob/main/codex-rs/core/src/compact.rs>
- OpenAI Codex source，context-manager history：
  <https://github.com/openai/codex/blob/main/codex-rs/core/src/context_manager/history.rs>
- OpenAI Codex source，exec head/tail buffer：
  <https://github.com/openai/codex/blob/main/codex-rs/core/src/unified_exec/head_tail_buffer.rs>
- Claude Code 文档，memory 与 compaction survival：
  <https://code.claude.com/docs/en/memory>
- Claude Code 文档，how Claude Code works：
  <https://code.claude.com/docs/en/how-claude-code-works>
- Anthropic API 文档，prompt caching：
  <https://platform.claude.com/docs/en/build-with-claude/prompt-caching>
- DeepSeek-Reasonix README：
  <https://github.com/esengine/DeepSeek-Reasonix>
