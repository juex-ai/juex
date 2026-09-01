# Juex 前端

> [English](README.md) | 中文

此目录包含由 `juex fleet serve` 提供的 React + Vite Fleet Web UI。Fleet Server 负责 Fleet JSON API，把选中 Agent 的 JSON/SSE request 代理到已验证的 Resident Agent endpoint，并嵌入 `internal/web/dist` 中的 production bundle。Resident Agent Server 只暴露 API；它们不提供 SPA。

## 技术栈

- React + TypeScript
- Vite
- React Router
- Tailwind CSS v4
- shadcn/ui primitives（通过 shadcn CLI 复制）
- AI Elements primitives（通过 `pnpm dlx ai-elements@latest add` 复制）
- streamdown，用于 AI Elements 内的 Markdown / KaTeX / mermaid 渲染
- shiki，用于独立 `CodeBlock` 内的代码高亮
- lucide-react icons

## 开发

先至少构建一次嵌入 bundle，再在一个 shell 中运行 Fleet Server：

```bash
make web
go run ./cmd/juex fleet serve
```

在另一个 shell 中运行 Vite：

```bash
pnpm --dir frontend dev
```

Vite 把 Fleet `/api` request 和选中 Agent 的 `/agents/:agentId/api` request 代理到默认 Fleet Server `127.0.0.1:5839`。

## 构建

从仓库根目录运行：

```bash
make web
make build
```

`make web` 运行 `pnpm install && pnpm build`，再把 `frontend/dist/` 复制到 `internal/web/dist/` 供 Go 嵌入。

## 源码地图

| 路径 | 用途 |
| --- | --- |
| `src/api.ts` | 类型化 Fleet 与选中 Agent fetch helper，包括 lifecycle/config/log 操作、轻量 Active Session lookup、Schedule 手动 Run、Session message pagination、Composer image upload、Workspace/media preview URL，以及 transcript/Fleet/resource SSE subscription |
| `src/types.ts` | Fleet、Agent、Session 与 Message API shape 的 TypeScript 镜像，包括带 tag 的 Command Observable/Schedule create union、transcript paging/replay-cursor metadata，以及来自 `internal/web` 的 Browser Event contract |
| `src/lib/agent-config.ts` | 区分持久化 update 与 restart failure 的纯 config-save reconciliation |
| `src/lib/fleet-directories.ts` | 纯 Add Agent directory validation、stale-request isolation、listing merge、keyboard 与 path-tail 行为 |
| `src/lib/fleet-routes.ts` | Fleet 与选中 Agent navigation 的纯 route helper |
| `src/lib/clipboard.ts` | Copy control 使用的 clipboard writer 与本地 HTTP fallback |
| `src/lib/conversation-scroll.ts` | 纯 Session conversation scroll 行为选项和 Composer clearance sizing |
| `src/lib/assistant-blocks.ts` | 把 live `llm.responded` Event payload 转为有序 Assistant block |
| `src/lib/composer-submit.ts` | 纯 Composer submit-state transition |
| `src/lib/code-theme.ts` | Markdown 与 reasoning code block 共用的明暗 syntax theme |
| `src/lib/compact-ui.ts` | 乐观 `/compact` UI label 与本地 Message helper |
| `src/lib/display-units.ts` | 把 `Block[]` 折叠为用于 Tool 配对的 `DisplayUnit[]` |
| `src/lib/fleet-shell.ts` | 纯 Fleet selection、visual state、lifecycle 与 stage-route helper |
| `src/lib/history-sessions.ts` | 纯 History-list title、badge 与规范 Session route helper |
| `src/lib/home-route.ts` | 选择 Web root redirect target 的纯 helper |
| `src/lib/light-code-highlight.ts` | Tool payload 使用的轻量同步 JSON/log 高亮 |
| `src/lib/live-session-projection.ts` | SSE BrowserEvent、optimistic message、provisional Assistant delta、pending-input presentation、compact marker 与 final-response assembly 的纯 transcript read model；Runtime status 来自每个 Event snapshot |
| `src/lib/live-tool-events.ts` | Tool requested/output-delta Event 的纯 live transcript update |
| `src/lib/loading-state.ts` | 纯 loading-state display text helper |
| `src/lib/mcp-events.ts` | MCP Event label 与折叠 preview 的纯 helper |
| `src/lib/media-reference.ts` | Transcript 与 Tool-result media reference 的稳定文本格式 |
| `src/lib/message-copy.ts` | Compact-summary 与 Message copy text 的纯 helper |
| `src/lib/message-rendering.ts` | 纯 Message chrome、disclosure 与 display-policy helper |
| `src/lib/observation-time.ts` | 本地 Observation timestamp 与窗口显示的纯 helper |
| `src/lib/queued-inputs.ts` | 纯 queued-input stack state transition |
| `src/lib/route-state.ts` | Shell state 的纯 route matching helper |
| `src/lib/runtime-display.ts` | 纯 Runtime 与 Session-state display formatting helper |
| `src/lib/runtime-navigation.ts` | 纯 Runtime subsection parsing、label 与规范 nested path |
| `src/lib/runtime-tool-catalog.ts` | 纯 Runtime Tool group label、timeout label、parameter projection 与防御性 schema formatting |
| `src/lib/session-messages.ts` | 规范 Message-ID creation-time decoding 与合并分页 transcript window 的纯 helper |
| `src/lib/session-read-controller.ts` | Session-detail effect interpreter，处理 route guard、fetch/context refresh、transcript SSE dispatch、reconnect-safe status calibration/application/cleanup 与 navigation effect |
| `src/lib/session-read-state.ts` | 纯 Session read-model transition、effect descriptor、route-stable live-subscription cursor capture 与 replay overlap suppression |
| `src/lib/session-transcript-renderers.ts` | 类型化 transcript registry 使用的纯 message-group renderer-key contract |
| `src/lib/session-title.ts` | Session preview display-title fallback 的纯 helper |
| `src/lib/system-notice.ts` | 纯自动 notice normalization 与 restart-title formatting |
| `src/lib/shell-header.ts` | Runtime badge 与 Session timestamp 的纯 shell header helper |
| `src/lib/tool-display.ts` | 纯 Tool title、lifecycle label 与 timeout display helper |
| `src/lib/tool-payload.ts` | 结构化 Tool input/output payload 的防御性格式化 |
| `src/lib/tool-result-output.ts` | 可见 Tool-result text 的有界多行格式 |
| `src/lib/session-access.ts` | 基于 kind 与 Active state 判断可写或 read-only Session view 的纯规则 |
| `src/lib/utils.ts` | UI primitive 共用的 Tailwind class merge helper |
| `src/lib/workspace-refresh.ts` | 刷新 Workspace tree 与打开文件 preview data 的纯 helper |
| `src/lib/fleet-roster.ts` | 纯 Fleet roster reconciliation，只为仍健康的 Agent 保留 current activity |
| `src/pages/` | Route 级 view |
| `src/components/` | App component |
| `src/components/FileTreePanel.tsx` | 可折叠 workdir tree 和 file preview sheet，由 Agent resource notification 或显式用户操作刷新 |
| `src/components/fleet/` | 持久 Agent rail、tabbed stage header、Runtime state bar 与选中 Agent context |
| `src/components/session/SessionComposer.tsx` | Session Composer、attachment workflow、queued-input/read-only presentation 与 overlay measurement |
| `src/components/session/SessionStatusPanel.tsx` | Composer 中显示的 context、goal、notes 与 Runtime-state control |
| `src/components/session/SessionTranscript.tsx` | 类型化 transcript renderer registry 和所有 Message/process row renderer |
| `src/components/LoadingState.tsx` | 全页等待时居中的 Juex logo loading state |
| `src/components/QueuedInputStack.tsx` | Composer 上方显示的 pending-input stack |
| `src/components/AssistantMarkdown.tsx` | 带 backend 验证的 inline local image link 的 Assistant Markdown 渲染 |
| `src/components/ImageBlock.tsx` | 共用的 80px transcript image thumbnail、failure metadata、download 与 full-size lightbox |
| `src/components/RuntimeToolCatalog.tsx` | 可复用的分组 Builtin/MCP Tool disclosure，包含 parameter 和 raw-schema detail |
| `src/pages/History.tsx` | Session history list，row 打开规范 `/sessions/:id` URL |
| `src/pages/Fleet.tsx` | Fleet setting stage，包含 service summary、registration、inline Workspace directory creation、condensed operational state、lifecycle、enablement 与 removal control |
| `src/pages/AgentConfig.tsx` | 带 validation 与保存后 restart reconciliation 的 Workspace config editor |
| `src/pages/AgentLogs.tsx` | 带显式 refresh 和行数 control 的有界 Resident Agent log tail |
| `src/pages/Extensions.tsx` | Read-only 的选中 Extension manifest、install scope、path、effective resource count 和不含值的 Agent environment declaration status |
| `src/pages/Observables.tsx` | 紧凑 Workspace Observable list，带完整内容 tooltip、sticky action、Schedule Run 与 lifecycle control |
| `src/pages/ObservableDetail.tsx` | Observable source detail、近期 Observation history、Schedule Run 与 lifecycle control |
| `src/pages/RuntimeLayout.tsx` | 共用 Runtime title、subsection selector 与 nested route outlet |
| `src/pages/Runtime.tsx` | Overview，包含 Provider、shell、sandbox、分组 Builtin/MCP Tool catalog、hook、system prompt 与 Skill detail |
| `src/components/ui/` | shadcn primitive |
| `src/components/ai-elements/` | AI Elements primitive（Conversation、Message、Reasoning、Tool、CodeBlock、PromptInput） |

Go API response shape 变化时，在同一个 PR 中更新 `src/types.ts` 和匹配的 client helper。
