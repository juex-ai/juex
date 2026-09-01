# Juex Web UI — 设计指南

> [English](DESIGN.md) | 中文

> 目的：定义 `juex fleet serve` Web 查看器的视觉语言、布局和组件词汇。北极星是**直观清晰好用**。任何涉及 Web UI 的改动都遵循本指南；设计变更与代码在同一个 PR 中落地。

---

## 1. 目标与非目标

目标：

- 呈现 Juex 冷静、温暖、感知事件且操作含义清晰的气质。
- 一眼辨明消息结构：谁说了什么、执行了哪些 Tool、Agent 在思考什么。
- 正确渲染 Markdown、语法高亮代码块、表格、列表、图片媒体引用及 Assistant Markdown 引用的本地图片。
- 自动遵循操作系统明暗模式。
- 从桌面适配到平板和手机，页面不产生横向溢出。
- 使用 Juex Design System：森林绿 `#064032`、金色 `#f6d78e`、中性运维表面、系统字体、Lucide 图标与森林绿色调阴影。

v0.1 非目标：非图片文件附件与语音输入、多光标/实时协作、多人协作编辑对话记录。

---

## 2. 技术栈

Web UI 是由 Vite 构建的单页 React 应用。Fleet server 通过 `go:embed` 托管编译产物，提供 Fleet JSON 路由并代理选中 Agent 的 JSON/SSE 路由。Agent server 仅提供 API，不提供 HTML。

| 层 | 选型 | 原因 |
|---|---|---|
| 构建工具 | **Vite**（最新版） | 标准 React 脚手架与快速 HMR |
| 语言 | **TypeScript** | 捕获客户端与服务端 API shape 漂移 |
| UI runtime | **React**（Vite 模板最新版） | shadcn/ui 与生态事实标准 |
| 路由 | **React Router v7** | 小巧、成熟 |
| 样式 | **Tailwind CSS v4** | utility-first，与 shadcn/ui 配合良好，无 runtime CSS-in-JS |
| 基础组件 | **shadcn/ui** | 现代审美、复制源码、无 runtime 锁定 |
| AI Chat 组件 | **AI Elements** | Apache-2.0 的 shadcn 风格组件，提供 Conversation、Message、Reasoning、Tool、CodeBlock、PromptInput |
| Markdown/代码 | **streamdown** + **shiki** | streamdown 渲染 Markdown、GFM、KaTeX、mermaid、CJK；shiki 高亮 Tool JSON |
| 图标 | **lucide-react** | 1.8 stroke，使用 `currentColor` |
| 包管理器 | **pnpm** | 快速且 `node_modules` 布局清晰 |

AI Elements 通过 `pnpm dlx ai-elements@latest add <component>` 逐个复制 TSX 到 `src/components/ai-elements/`，不保留 `@ai-elements/*` runtime 依赖。复制文件中的 `import type { ... } from "ai"` 改为本地 `_local-types.ts`，因而构建和 runtime 均不需要 `ai` 包。`MessageResponse`/`Reasoning` 内部用 streamdown；Tool card 的独立 `CodeBlock` 直接用 shiki。

许可证均兼容：Vite、React、Tailwind、shadcn/ui、shiki 为 MIT，AI Elements 与 streamdown 为 Apache-2.0，lucide-react 为 ISC，`use-stick-to-bottom` 为 MIT。gzip bundle 必须低于 **500 KB**，修改 `frontend/` 的 PR 由 `pnpm build` size reporter 验证。

生产代码把设计系统维护为 `frontend/src/index.css` 中的 CSS variable。禁止引入 Web font；系统字体栈是有意选择。新 UI 优先复用现有 shadcn/AI Elements 组件，再用 Juex token 定制。

---

## 3. 仓库布局

```text
juex/
├── frontend/                       # React + Vite + TS source
│   ├── package.json
│   ├── pnpm-lock.yaml
│   ├── vite.config.ts
│   ├── tailwind.config.ts
│   ├── tsconfig.json
│   ├── index.html
│   ├── components.json             # shadcn config
│   ├── src/
│   │   ├── main.tsx                # React root
│   │   ├── App.tsx                 # routes
│   │   ├── api.ts                  # typed fetch + SSE helpers
│   │   ├── types.ts                # API/session DTO + browser event contract mirror
│   │   ├── lib/
│   │   │   ├── utils.ts            # shadcn cn helper
│   │   │   ├── display-units.ts    # Block[] -> DisplayUnit[]，配对 Tool
│   │   │   ├── assistant-work-groups.ts
│   │   │   ├── session-read-controller.ts
│   │   │   ├── session-transcript-renderers.ts
│   │   │   └── message-rendering.ts
│   │   ├── pages/
│   │   │   ├── Fleet.tsx           # /
│   │   │   ├── Sessions.tsx        # /agents/:agentId
│   │   │   ├── Session.tsx         # /agents/:agentId/sessions/:id
│   │   │   ├── History.tsx         # /agents/:agentId/history
│   │   │   ├── RuntimeLayout.tsx
│   │   │   ├── Runtime.tsx         # runtime overview
│   │   │   ├── Extensions.tsx
│   │   │   ├── Observables.tsx
│   │   │   ├── AgentLogs.tsx
│   │   │   └── AgentConfig.tsx
│   │   └── components/
│   │       ├── AppShell.tsx
│   │       ├── session/
│   │       ├── ai-elements/        # AI Elements primitives
│   │       └── ui/                 # shadcn primitives
│   └── dist/                       # make web 产物；gitignored
├── internal/web/                   # Agent JSON/SSE API 与 embedded SPA handler
└── internal/fleetweb/              # Fleet API、Agent proxy 与 SPA mount
```

旧的 `internal/web/templates/` 和 `internal/web/static/` 已删除。`internal/web/embed.go` 暴露 `//go:embed all:../../frontend/dist` 及 SPA fallback：非 asset 路径返回 `index.html`，交给 React Router；只有 `internal/fleetweb` mount 它。

`frontend/dist/` 不提交。源码构建 Juex 需要 Node + pnpm；运行 `make web` 前 `go build` 会因空 embed 失败。这体现“从源码构建 binary = 运行完整工具链”，不提交预构建 bundle。

---

## 4. 构建与开发流程

首次构建或修改 `frontend/` 后：

```bash
make web          # pnpm install + pnpm build -> frontend/dist/
make build        # 带 ldflags 的 Go build，嵌入 dist
```

`make build` 依赖 `make web`；dist 不存在时 `go:embed` 明确失败。HMR 使用 `make web-dev`（Vite `:5173`），另一终端以默认 `127.0.0.1:5839` 运行 `juex fleet serve`。Vite proxy 转发 Fleet `/api` 与 Agent `/agents/:id/api`，Agent 页面路由留在 Vite 以支持直接导航和刷新。CI 运行 `make web && make test && make build`。

### 4.1 视觉基础

- Radius 固定为 `2px`、`4px`、`6px`、`8px` 四级，base 为 `6px`；共享 control、dialog、card、menu、code surface 不超过 `8px`。消息 bubble 与 prompt surface 可用 `16px`；`rounded-full` 只用于圆形 control 与语义 pill。
- Light mode 采用略带绿色的中性灰 page/sidebar canvas；cream 仅用于 authored content 和 code accent，不作主背景。
- Runtime 状态使用 `status-success`、`status-warning`、`status-working` 的 foreground/background/border token，不直接写 palette utility。
- 交互 control 使用可见 `2px` focus ring + `2px` offset。Prompt 用等价的高对比语义 border，不加 outer ring。用户请求 reduced motion 时停止 spinner、shimmer、pulse 与 disclosure motion。

---

## 5. 页面布局

所有页面置于 Fleet-first shell。持久 Agent sidebar 负责 Fleet 选择与生命周期动作，tabbed stage 显示选中 Agent 的 Chat 或 Runtime route；Runtime 包含 Overview、Extensions、Observables、Logs、Config。Workspace browser 在宽屏 dock，窄屏变成右侧 drawer。History 从 stage header 打开 `/agents/<id>/history`，shell 不重复 Session title。

```text
┌──────────────┬──────────────────────────────────┬──────────────┐
│ Fleet Agent  │ Agent + status + stage tabs      │ Workspace    │
│              ├──────────────────────────────────┤ file tree    │
│ selected     │ message list / selected page     │              │
│ status       │ (scrollable)                     │              │
│ lifecycle    ├──────────────────────────────────┤              │
│              │ composer or runtime state bar    │              │
└──────────────┴──────────────────────────────────┴──────────────┘
```

- Sidebar 宽 268px，折叠为稳定的 64px avatar rail；hover 时 brand mark 变为展开 control，control footprint 不移动。Add agent 区两种模式等高，仅含全宽 outline action，无 summary/separator。低于 760px 变为 stage header 打开的 overlay drawer。
- Agent row 显示 stopped、idle、working、failed。selection/hover/rest 只以背景区分，不给 avatar 冗余 accent rule；avatar 始终用浅金色。展开 row 在 hover 时只显示一个生命周期 toggle 和一个 Runtime shortcut，pending input count 用紧凑金色 badge。
- 优先恢复 local storage 中最后有效 Agent，否则选择第一个 registered Agent；空 Fleet 显示 Add agent 与 CLI 注册提示。
- Stage header 显示 Agent name、紧凑 status pill、Chat/Runtime tabs；Runtime 右上有可访问 subsection selector，行为以 canonical nested route 为准。
- File browser 在宽度至少 1280px 时为右栏，否则由同一 header button 打开右侧 Sheet。具体 Session route 下 panel title 旁可切换 Workspace/Scratchpad，route 变化重置为 Workspace。
- File preview 总在右侧 Sheet 打开；窄屏用 viewport width。文本长 path/content 换行，图片等比完整容纳。
- Header strip 使用 `--juex-header-height` 对齐 app/workspace header。
- History row 打开 canonical `/agents/<id>/sessions/:id`；Session page 根据 kind 与 active state 决定 composer 是否可用。
- Transcript 与 composer 共用 760px content boundary。Transcript container 808px（含桌面 24px gutter），低于 768px gutter 为 16px。
- Active composer 浮在中心列底部，以 Session container 高度为边界。完整遮挡区（bottom safe area + 48px 顶部 fade）至少留 150px transcript scroll clearance，但不缩短全高 scrollport。与 composer 同宽的 occluder 从圆角顶部内 16px 开始，fade 穿过同一边界，不覆盖 scrollbar，也不画 viewport-wide 底色。仅当读者已经位于底部时随 clearance 增长，不抢走手动阅读位置。
- Stopped/failed Agent 仍可读持久化对话；composer 换成带 Start action 的 runtime state bar，failed 额外显示原因与 Logs shortcut。Runtime 不可用时不轮询 Turn/打开 event stream，history 从 Fleet persisted read-only path 加载。
- Desktop 保持高密度：workspace `18rem`，中心 padding `24px`。Header/metadata 对低优先级 label 换行或隐藏，避免页面横向滚动；小屏 Runtime table 在 card 内滚动。

---

## 6. 页面

### 6.1 Fleet settings（`/settings`）

Sidebar footer 进入 Fleet settings。依次显示 Fleet process RSS 与 interval CPU、listener/version、system-service state、model/provider 与 Extension ownership，以及高密度 roster。Roster 用共享 `Agent`、`Workspace`、`State`、`Process`、`Actions` grid；State 组合 stopped/idle/working/failed 与 runtime binding；Process 显示十进制 MB/GB RSS 和 CPU（单核满载为 100%，多核不截断），不可用为 em dash，不显示 PID 或单独 health column。Actions 提供由状态派生的 Start/Stop、Restart、不同 Enable/Disable icon、bounded logs、config 和透明 destructive Remove。Disabled row 变淡，但配置与恢复 action 仍可用。

Add agent dialog 提供可编辑 absolute path、服务端单层 directory browser、compact breadcrumbs、hidden-directory switch、inline mkdir、optional name、autostart、start-now。长 path/breadcrumb 显示尾部而不出现 scrollbar；workspace/name input 用 border-only focus。创建目录期间锁定导航，失败就地展示。Disable 可逆；Remove 使用独立 dialog，只有准确输入 persisted agent name 才启用。错误显示在当前 dialog 或 page alert，不能被 optimistic status 隐藏。

### 6.2 Sessions list（`/agents/:agentId`）

中心列空态为带 logo、`Aware, action` 与正常 prompt input 的暖纸面。提交后创建 active primary Session 并导航至 `/agents/<id>/sessions/<new-id>`。进入页面时用 lightweight active-session lookup 重定向已有 active primary，不加载完整 history。

### 6.3 Session detail（`/agents/:agentId/sessions/:id`）

中心列含 compact header、可滚动消息与 floating composer。只有 active primary 显示 composer；inactive primary 与 side 只读且无 activate control。Footer 显示短暂反馈、最新 request context total、conversation token total。Composer 支持图片粘贴、拖放和 picker，并在发送前显示 bounded thumbnail strip。

Shell file browser 在 title 旁切换 Session Scratchpad 与 Agent Workspace，复用 refresh、empty、text/image preview；Workspace 隐藏 `.juex`。模型无 vision 时，已接收的 image Turn 仍正常开始或排队，通过 composer feedback 非阻塞提示配置建议。

长 transcript 使用 bounded window；顶部 `Load older messages` prepend 前一页。存在 compaction 时，若 compact divider 后的 tail 能放入默认 window，则首次从最新 divider 开始。

MCP/Observation channel event 是居中 external-event text row：radio icon、mono `<event_source>:<event_type>`、muted dot、folded preview、chevron；collapsed 无 bubble、round border 或 card background。完整 JSON-RPC `params` 时 preview 用 `params.content`，expanded 显示含 metadata 的完整 params JSON。Chevron collapsed 向右、expanded 向下；copy icon 仅属于 expanded body，位于右上并在 hover/focus 显示。此类 event 用 gold ramp，不用 blue/teal。

Model fallback 和 automated system notice 用 blue information ramp 的居中 external-style row。Bell icon、mono title、dot、preview、chevron 与金色 MCP/Observation 及内部 process disclosure 区分。Fallback 显示 `Model switched`/`Model recovered`，不带 provider-only `system-reminder` wrapper；restart continuation 显示 `Agent restarted`，其他为 `System notice`。展开显示完整 persisted explanation；不是 user bubble，也无普通 message copy action。

Context compaction 是居中 divider：水平线夹 `Context compacted` button。点击复制 persisted compact summary，tooltip 暂时变为 `Copied to clipboard`，summary 不内联显示。手动 `/compact` bubble 立即显示 captured submission time，并在 marker 替换 pending state 后保持该时间。

User/assistant bubble 在 hover/focus 显示 metadata action row。可复制时复制可见全文并临时显示 `Copied to clipboard`；有效 timestamp 用本地 `MMDD - HH:mm:ss`，attachment-only 也显示。仅时间的 row 可键盘聚焦并有 focus ring。两者同时存在时 copy 在外侧：assistant 为 copy/time，user 为 time/copy。普通 copy 只含 transcript 可见内容，不含 reasoning/process metadata；divider/external event 保留专用 copy。

Tool card lifecycle label：无 result 为 `running`，有正常 result 为 `success`，错误为 `failed`；running header 显示 runtime timeout 秒数。Result 保留换行，不渲染为 Markdown paragraph；长文本限制在可滚动 code surface。Copy 优先 Clipboard API，LAN/NetBird 的非安全 HTTP 下 fallback 为临时 textarea。

### 6.4 History（`/agents/:agentId/history`）

按服务端顺序列出 Session，row 显示 preview、relative last-active、kind、active state、turn count。统一打开 canonical Session URL；active primary 有 composer，其他只读。History page 负责 deletion 与 compact `New chat`。

### 6.5 Runtime（`/agents/:agentId/runtime`）

共享 layout 右上 selector 切换 Overview、Extensions、Observables、Logs、Config；subsection 来自 URL。切换 Agent 保持 subsection，从 Observable detail 切换 Agent 则回到新 Agent 的 list。Layout 只有一个全高 flex scroll boundary，并通过 nested outlet 渲染 child。

Overview 先显示 service runtime metadata，包括稳定的 process start time 与 absolute cwd。Effective system prompt 用语义 table 显示 label、source、path、approx token count，可展开全文。Provider profile 显示 protocol、model、base URL、capability gate。Tools 位于 Provider 后、MCP 前，以固定 `file`、`chunked_write`、`shell`、`search`、`skill`、`session_state`、`observable` group 的两级 table 呈现；group 展示 count/name preview，Tool 展开 description、semantic timeout、top-level parameter table 和单独 raw JSON schema。Bounded timeout 显示秒；Tool-managed lifecycle 明示；空 group 保留并显示 zero count。

MCP table 总是显示 source、canonical transport、connection state、tool count、command 或 display-safe URL、startup error。stdio 显示 command + args；HTTP URL 去掉可能含 credential 的 query。Server row 展开复用 builtin Tool table。失败/未启动解释没有 Tool 的原因，connected zero-tool 明示未 advertised。Project-local source 排在 user-global 前。MCP 在 Agent startup 启动，所以页面报告 process-level live state；snapshot 保持到 Agent restart，编辑 config 不投射未应用 row。

Disclosure body 仅在 open 时 mount；所有 expandable table row 使用最左 chevron（右/下），可见 keyboard focus。窄屏 table 在 section 内滚动，cell 换行或截断但不隐藏 label；长 path/URL/command/error 通过换行或 disclosure 可读，不能仅 hover。Runtime section 使用共享 radius 与单一 visible boundary，保持 operational 而非 conversational 风格。

只读 Extensions `/runtime/extensions` 仅列 selected winner。Card 显示 display/logical name、version、description、scope、absolute path、manifest version，以及有效 Skill/MCP/Hook/Observable count。Requirement 按 manifest 顺序显示 name/description；安全 absolute HTTP(S) 值为可键盘访问的新标签链接，invalid/unsafe/redacted 只显示文本。Agent env var 只显示 name、source、effective/shadowed/deduplicated status，绝不显示 value。Requirement 仅供信息，无 check/install/enable/disable/edit。空态为 `No Extensions are selected for this Agent.`。

### 6.6 Observables（list/detail route）

List 使用适合 `max-w-5xl` 的紧凑五列 grid。Observable、Source、Last Observation 单行 ellipsis；可访问 full-row link 的 hover/focus tooltip 显示完整值并 bounded/wrapping。窄屏 data column 在 card 内滚动，opaque Actions header/cell 固定右侧；过高 tooltip 可用 Arrow、Page、Home、End 滚动。

Schedule row 的 `Run` lightning 触发一次 Observation，Start/Stop 管 timetable，Delete 只删除 project-owned source。Extension row 显示 `ext:<name>` 与 type/config summary，无 Delete，API mutation 仍拒绝。Project row source 为 `project`，Command row 无 Run。Schedule detail 重复带 label 的 Run。Detail 分开显示 resource Source 与 command/schedule Type；窄屏 action group 换行且右对齐。成功后刷新，API error 进入既有 page-level error 区。

### 6.7 Agent logs 与 config

`/runtime/logs` 显示可刷新、有显式 line limit 的 bounded tail。`/runtime/config` 编辑 workspace `juex.yaml`；保存前验证、保存后重启 Agent，验证/重启错误以醒目 alert 显示。Active Turn 遵循 Fleet restart continuation contract；非致命 continuation failure 必须与保存/重启成功信息并列，不能被笼统成功隐藏。

---

## 7. 组件

### 7.1 History list

History 是整页而非 sidebar。每行第一行是单行 ellipsis preview；第二行以 mono 显示 `2m ago`、`yesterday`、`Mar 5` 等 relative time，加 `primary`/`side`、`active`、turn-count badge。Trash icon 删除前需 browser confirmation。

### 7.2 PageHeader

Global shell header 显示 Juex wordmark、当前 page/conversation preview，以及 history、runtime、workspace icon。Session 内部 strip 显示 id、turn count、kind、active、model、last-active；ID、model、数字和单位使用带 tabular number 的 mono。

### 7.3 Conversation

AI Elements `<Conversation>` 包裹 scrollable transcript，使用 `use-stick-to-bottom`：除非用户已向上滚动，否则跟随新内容；偏离底部时显示 `<ConversationScrollButton>`。`<ConversationContent>` 宽 808px、横向 padding 24px，得到与 composer 相同的 760px boundary。

### 7.4 Loading state

整页等待使用 `<LoadingState>`，不散放 `Loading...`。它在可用区域居中，以 Juex logo 为 anchor，加不改变布局尺寸的小圆形 motion cue；文案简短，如 `Loading conversation`、`Loading runtime`。

### 7.5 Message

`<Message from={role}>` 为一个 canonical message 分组可见 unit。User message 是右对齐 card-like bubble，normal card foreground、subtle border、较紧 top-right corner；已发送图片在 bubble 上方呈右对齐 80px 方形 thumbnail，自动换行并打开 full-size lightbox。这只是 view projection，不改 persisted block order。Assistant/system/Markdown-local/tool-result 图片复用相同 thumbnail/lightbox。Assistant text 无 frame、左对齐；同一 model 连续组只在首组显示 model label，user/status 开始新 run。Reasoning/Tool sub-unit 为 assistant text 下方 compact process row。MCP notification、Observation、Policy trace 绕过普通 message chrome；Hook-backed Policy 保留 Hook event/command name，但 renderer 与 persisted kind 仍是 Policy-generic。

普通 Assistant response 以 reasoning + tool call 开始时，连续 process-only message 折叠为一个 assistant work disclosure。运行 title 跟随最新 tool-bearing message；出现非空 text 后显示 persisted response span 与 tool-call 总数，assistant text 仍正常展示。展开复用 Thinking/Tool row；历史/中断 tail 不标 running，加载旧 page 只重算只读 projection。

### 7.6 文本渲染

`<MessageContent>` 包裹 `<MessageResponse>{text}</MessageResponse>`。streamdown 负责 Markdown、GFM table、syntax-highlighted code、KaTeX、CJK、mermaid。Markdown code block 与 Tool JSON block 使用同一 Juex code token 和 light/dark Shiki theme，避免 dark mode 混入异质 editor background。

### 7.7 Reasoning

Reasoning 是低强调 process row，不是 bubble。Collapsed trigger 固定为 muted `Thinking` + 紧邻 chevron，不显示 reasoning preview 或绿色 status dot；chevron 向右/向下。Expanded body 直接用 streamdown 渲染，无 `CONTENT` label；redacted reasoning 保留 provider redaction text。同一 persisted message 的相邻 reasoning block 合为一行，按 provider order 保留可读 summary，不暴露 encrypted replay content；text/image/tool 是 hard boundary，canonical storage 不变。

### 7.8 Tool

Tool process row 是 `tool_use` + `tool_result` pair 的紧凑 collapsible metadata，不使用左边线、圆角、阴影或 bracket chrome。Status dot 很小，running loader size 不变；chevron 紧随 Tool name，collapsed/expanded 为右/下。Live projection 位于 `live-session-projection.ts`，block update helper 为 `live-tool-events.ts`。`Session.tsx` route adapter 用 `display-units.ts` 的 `messagesToGroups` 配对，再用 `assistantWorkItems` 投射连续 process group；`SessionTranscript.tsx` 经 typed `group.kind` registry dispatch 并拥有 row JSX。

| `use` | `result` | `result.is_error` | `<ToolHeader>` state |
|---|---|---|---|
| present | absent | — | `input-available` |
| present | present | false | `output-available` |
| present | present | true | `output-error` |
| absent | present | false/absent | `output-available`（orphan） |
| absent | present | true | `output-error`（orphan） |

Header 显示不带 transport prefix 的 Tool name；可为兼容传 `type={\`tool-${tool_name}\`}`，`tool-display.ts` 去掉 `tool-`，缺名 fallback 为 `tool`。Expanded parameter/result 用带 label 的 compact payload block，error 用 destructive tone。所有 Session Tool row（含 batch/nested、任意状态）默认 collapsed，只由用户展开，但 collapsed trigger 仍显示 status。

### 7.9 Composer

```tsx
<PromptInput onSubmit={({ text }) => handleSend(text)}>
  <PromptInputBody>
    <ComposerAttachmentStrip />
    <PromptInputTextarea placeholder="Ask juex anything..." />
    <PromptInputFooter>
      <PromptInputTools>
        {composerHint ? <ComposerFeedback tone="hint">{composerHint}</ComposerFeedback> : null}
        {status.kind === "error" ? <ComposerFeedback tone="error">{status.detail}</ComposerFeedback> : null}
        <ContextUsageLabel usage={contextUsage} />
        <TokenUsageLabel usage={tokenUsage} />
      </PromptInputTools>
      <ComposerSubmitButton action={submitAction} onStop={onInterrupt} />
    </PromptInputFooter>
  </PromptInputBody>
</PromptInput>
```

本地 command 保留在文本 surface：`/status` 返回 runtime/session snapshot，`/compact` 触发 manual compaction；除非 command surface 不够，否则不加独立 chrome。Slash output 是普通 message text，显式换行必须可见。

Turn 运行时，pending input 在 composer 上方同一个 translucent/blurred floating surface 内，oldest first、小编号、label `Queued`，仅属于 live Session。Queue 与 composer 共用高度预算并内部滚动，窄/矮屏 composer 仍可达；drain 后 row 离开 queue 并进入 conversation。

Enter 提交、Shift+Enter 换行。Image preview 在 textarea 左上、无 separator，80px、窄屏自然换行、矮屏 bounded scroll，始终显示圆形 remove；textarea 也限制 viewport height。不采用通用 AI Elements `Attachments`。Composer 是 radius 16px 的暖纸 floating well，森林绿 shadow 与高对比 focus border，无 outer ring。48px top-only fade 限 760px，并向圆角后延伸 16px；同宽 occluder 从 opaque boundary 延伸到 prompt 下方，不缩短/覆盖 scrollbar。

Submit button 兼作状态 control：empty+idle 视觉 disabled 且点击提示；empty+running 为 square stop；text+idle 立即提交并清空；text+running 排入下一 provider call。Footer 左侧 feedback/context/token 可换行，右侧只有一个不换行 submit，手机宽度也不另起一行。

`ContextUsageLabel` 为最新成功 request 的 `context <total>` chip。Provider 有 input usage 时 total=`input_tokens + output_tokens`；缺 input usage 时为 estimated input breakdown + reported response。Tooltip 显示 model、context window、percent 与 system prompt/tools、MCP tools、skills、messages、response 分解。`TokenUsageLabel` 是累计 `tokens <total>`，tooltip 显示 input/output split。

### 7.10 Composer Submit 状态

| 状态 | 视觉 |
|---|---|
| empty + idle | send icon、disabled，点击显示输入提示 |
| empty + running | square stop icon |
| text + idle | send icon，立即提交 |
| text + running | send icon，排队 pending input |
| accepted image + vision disabled | Turn 继续，左侧显示短暂 warning |
| error | 左侧显示 compact error |

视觉状态只由 local draft 与 active Turn 是否运行派生；不能重新加入独立 idle/running chip 或第二个 Stop button。

---

## 8. Theme token

Juex Design System 以 token 为先，`index.css` 同时定义 brand ramp 与 Tailwind 消费的 shadcn variable：

| Token | Value | 用途 |
|---|---|---|
| `--juex-forest-800` | `#064032` | primary、wordmark、user bubble |
| `--juex-gold-400` | `#f6d78e` | accent、live glow、forest 上的 text |
| `--juex-cream-50` | `#fbf6ea` | light page background |
| `--juex-ink-900` | `#1c1916` | light body text |

Light mode 为 cream page、white card、ink text、forest primary、gold accent；dark mode 为 deep forest page/card、cream text、gold accent。Role surface 使用 role token，不直接用 `primary`，使 gold 保持 accent。Shadow 始终带 forest 色调，不用黑色。

```css
@layer base {
  :root {
    --juex-assistant: var(--juex-info); --juex-thinking: var(--juex-ink-600);
    --juex-tool: #6e4ea3; --juex-tool-bg: #f1ecf9;
    --juex-error: #b03a2e; --juex-done: var(--juex-forest-500);
    --juex-pending: var(--juex-gold-700); --juex-tool-border: #ded1ef;
    --juex-tool-header: #f3eefb; --juex-tool-surface: #fbf8ff;
  }
  .dark {
    --juex-assistant: var(--juex-cream-50); --juex-thinking: var(--juex-forest-300);
    --juex-tool: #d8c8ff; --juex-tool-bg: rgba(216, 200, 255, 0.11);
    --juex-tool-border: rgba(250, 227, 170, 0.14); --juex-tool-header: #0d3a2f;
    --juex-tool-surface: #073126; --juex-error: #f09a92;
    --juex-done: var(--juex-forest-300); --juex-pending: var(--juex-gold-400);
  }
}
```

Tailwind 通过 `@theme inline` 暴露 `text-juex-*`/`bg-juex-*`。新增颜色优先 token，不写 raw hex。Dark code/result 使用 raised forest tone，不用纯黑。Shiki token 同时遵循 `.dark` 和 `prefers-color-scheme: dark`，因为 v0.1 无手动主题开关。

---

## 9. 字体

不下载字体。Body/UI 使用 `ui-sans-serif, system-ui, -apple-system, "Segoe UI", Inter, "Helvetica Neue", Arial, sans-serif`；display/empty/wordmark 使用 `ui-serif, "Iowan Old Style", "New York", "Apple Garamond", Georgia, "Times New Roman", serif`，通常 italic；code/id/number 使用 `ui-monospace, SFMono-Regular, "SF Mono", "Cascadia Code", "JetBrains Mono", Menlo, Consolas, "Liberation Mono", monospace`。Body 为 15px/1.55，metadata 11–12px mono；普通 label 为 sentence case，eyebrow 为 11px uppercase + `0.14em` tracking。紧凑产品界面不使用负 letter spacing。

---

## 10. 实时更新

Transcript 从 `/api/sessions/<id>` 取 JSON；首次使用默认 window，`Load older messages` 请求 `?before=<oldest_message_id>` 并 prepend。

`live-session-projection.ts` 是 live message、optimistic Turn、pending input、compact progress、Tool delta、assistant text/reasoning delta 的浏览器 read model。每个 BrowserEvent 的 runtime status 是 authoritative snapshot，由 session read controller 应用。Assistant delta 按 provider block index 累积 provisional block；`llm.responded` 以 canonical ordered blocks 替换，避免 retry/chunking 重复；`llm.errored` 在 fallback 或 terminal failure 前清除失败 attempt 的 provisional block。

Composer 附近的 session-state control 先显示 Goal，再显示 model-owned Notes。Notes 用 Markdown；含 task item 时 tooltip 显示 completed/total 与细 progress。`notes.updated` 立即更新而无需 transcript refresh。只有选中 Scratchpad 时才请求 `/api/sessions/<id>/scratchpad`，preview 复用 workspace-bounded endpoint；仅具体 Session route 可用，换 route 恢复 Workspace。

Fleet/Agent operational read model 使用 snapshot + notification，不浏览器 polling。`/api/fleet/events` 传 typed roster、Fleet process、Agent activity snapshot。`fleet.roster.unavailable` 保留 last-known roster 并显示 reconciliation error；下一次成功 snapshot 即使内容未变也清除错误。选中 Agent 的 `/api/resource-events` invalidates workspace、scratchpad、Observable、process-lifetime runtime catalog。Workspace 或启用的 global `AGENTS.md`、active primary selection、active Session scratchpad 还会 invalidate Runtime snapshot，因为它们动态重建 prompt。External runtime-input directory 保留 parent watch，应对 rename/delete/recreate。Skill、MCP、Hook、Extension selection 仍是 startup fact。页面随后 refetch authoritative JSON，EventSource reconnect 从当前 frame 重新校准。Command 保持普通 HTTP，单向 invalidation 不需要 WebSocket command channel。

`session-read-controller.ts` 负责 route guard、snapshot/context refresh、EventSource dispatch、reconnect calibration、stream cleanup、timer、navigation 与 terminal refetch。`Session.tsx` 只是 route/view adapter；`SessionTranscript.tsx` 负责 row dispatch，`SessionComposer.tsx` 负责 composition/queue，`SessionStatusPanel.tsx` 负责 status control。

| Event | Effect |
|---|---|
| `turn.started`, `llm.*`, `tool.*` | 标记 Turn active，更新 live transcript/status |
| `pending_input.queued`, `pending_input.drained` | 更新 queue，保持 Turn active |
| `pending_input.rejected`, `pending_input.dropped` | 显示 compact error feedback |
| `turn.completed` | 清 queue/active，并要求页面 refetch |
| `turn.errored` | 清 queue/active、记录 error，并要求 refetch |
| `context.compact.*` | 管 optimistic marker，terminal 时要求 refetch |

绝不通过 SSE 注入 HTML；JSON 是 source of truth，SSE 是 notification channel。

---

## 11. 可访问性

- Message container 使用 `aria-live="polite"`，新 Turn 不打断输入。
- 所有交互元素可键盘访问；shadcn primitive 基于 Radix。
- 临时状态通过 icon、label、tooltip 共同表达，不能只靠颜色。
- Focus 使用可见 `2px --ring`；Prompt 的语义 focus border 是等价方案且必须可见。
- Juex token 在明暗模式均满足 WCAG AA；新增 color token 时重测。

---

## 12. Dark mode

CSS 同时支持 `prefers-color-scheme: dark` 和 `.dark`；v0.1 无手动 toggle。新组件落地前测试两种模式。Dark mode 使用 deep forest surface 与低 alpha gold border，避免纯黑、纯灰和冷蓝。

---

## 13. API 契约（客户端）

`frontend/src/types.ts` 镜像 Go type：

```ts
export interface SessionInfo {
  id: string; dir: string; kind: "primary" | "side"; active: boolean;
  started_at: string; last_active_at: string; turns: number; preview: string;
  token_usage: { input_tokens: number; output_tokens: number };
  context_usage?: ContextUsage;
}
export type Role = "user" | "assistant" | "system";
export type Block =
  | { type: "text"; text: string }
  | { type: "reasoning"; text: string; redacted?: boolean }
  | { type: "tool_use"; tool_name: string; tool_use_id: string; input: unknown }
  | { type: "tool_result"; tool_use_id?: string; content: string; is_error?: boolean };
export interface ContextUsage {
  model?: string; context_window?: number; input_tokens: number;
  output_tokens: number; total_tokens: number;
  breakdown?: { key: string; label: string; tokens: number }[];
}
export interface Message { role: Role; blocks: Block[]; }
```

Go side 是 source of truth；修改 `internal/llm/types.go` 时同一 PR 更新镜像。Render layer 用 `display-units.ts` 把 `Block[]` 折为 `DisplayUnit[]`，将匹配的 `tool_use`/`tool_result` 合为一个 display unit；只改变渲染，`types.ts` 与 JSONL 不变。

---

## 14. 暂缓范围

非图片附件、mobile breakpoint、跨 Session 搜索、inline regenerate/edit、OS dark mode 以外的主题定制、国际化（当前 UI string 仅英文）、AI SDK runtime hook（`useChat`/`streamText`）、以及尚未采用的 AI Elements：`MessageBranch`、`Sources`、`ModelSelector`、`Attachments`、`Suggestion`、`SpeechInput`、`Artifact`、`ChainOfThought`、`Plan`、`Task`、`Checkpoint`、`Confirmation`、`Persona`。本项目 SSE event stream 是 source of truth，AI Elements 由自身 state 驱动。

---

## 15. 流程

新增组件类别、color token、layout shift 等实质设计变化必须与代码在同一个 PR 更新本指南。Reviewer 检查两者。新 pattern 若不属于现有组件类别，应先在 §7 提议新增类别，再实现。
