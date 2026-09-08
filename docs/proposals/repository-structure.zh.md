# 仓库结构整理提案

> [English](repository-structure.md) | 中文

状态：已通过 [PR #537](https://github.com/juex-ai/juex/pull/537) 实现。更新：2026-09-08。下方清单及迁移讨论描述 2026-09-07 的源码快照；当前归属和路径以 [ARCHITECTURE.md](../../ARCHITECTURE.zh.md) 为准，不以此历史包映射为准。

本文提出代码归属、包边界和目录组织的讨论基线，不替代当前 [架构契约](../../ARCHITECTURE.zh.md) 或 [ADR-0001](../adr/0001-lifecycle-driven-module-architecture.zh.md)。本文已补全基于当前源码的生产使用者清单；逐包目标归属及拆分边界供迁移前评审。

## 问题与目标

`cmd/juex` 保持薄入口、实现放在 `internal`，符合 [Go 官方组织建议](https://go.dev/doc/modules/layout)。问题在于 `internal` 的组织没有充分表达项目已经采用的 Foundation、Framework、Feature 分层。

当前结构的几个具体例子：

- `runtime`、`thread` 与 `jsonl`、`frontmatter`、`processmetrics` 同级，产品执行机制与通用技术能力难以区分。
- `cli`、`web`、`fleetweb` 与内部执行包平铺，接入方式和应用能力缺少目录分组。
- Feature 分散在 `modules`、`hooks`、`mcp`、`observable`、`skills` 等路径中。
- `llm` 同时包含中立消息类型、Provider 契约及供应商实现。
- `app` 同时承担依赖组装和 Main/Worker 管理、输入准入等应用编排。
- chunked-write 实现跨越 `modules/chunkedwrite`、`chunkedwrite` 和 `tools`，需要阅读多个位置才能理解功能。

现有 `internal/architecture/boundary_test.go` 已经对部分包分类并限制依赖。因此当前问题是架构表达不足和部分职责混合；这些例子不构成完整依赖审计，也不能据此断言现有依赖全部违反分层。

整理目标是：浏览目录能够判断职责；新增功能有明确归属；一个功能的多数修改集中在其所有者内部；核心执行机制不依赖具体功能或供应商实现。

## 范围

保留单仓库、单 Go module 和 `cmd + internal`。调整包归属、接口及依赖，保持产品行为、CLI/API、配置和持久化格式不变。

不在本次整理中引入插件系统、通用服务定位器、新功能或行为迁移。实际拆分若需要改变外部契约，应另行讨论。路径迁移完成后移除旧路径，不保留兼容转发包。

## 分类原则

第一层表示架构职责，第二层使用具体能力名称。分组目录可以不包含 Go 源文件；它们不要求成为包或独立 Go module。

| 分组 | 职责 |
| --- | --- |
| `app` | 选择实现、组装依赖、启动与关闭应用 |
| `entrypoints` | 对外操作入口：CLI 命令、HTTP 请求与 SSE 订阅 |
| `fleet` | Agent 注册、进程监督和生命周期管理 |
| `framework` | Agent/Thread 执行机制、持久化顺序与扩展契约 |
| `features` | 通过 Framework 契约提供的具体能力 |
| `providers` | 外部模型服务的具体协议实现 |
| `foundation` | 中立契约及共享底层机制 |

不建立含义宽泛的 `core`、`common`、`utils`。暂不建立统一 `domain` 包：领域类型和规则跟随行为所有者，避免产生共享模型集合。低层依赖位置不等于没有领域语义；例如 Thread 应拥有其产品规则，而 JSONL 仅负责文件机制。

## 对外入口的命名

`adapter` 在架构中可以涵盖模型服务、存储和协议转换，范围超过这里的 CLI、HTTP/SSE 职责。这一组已确定命名为 `entrypoints`，明确表示外部调用者操作 JueX 的入口。

`cmd/juex/main.go` 是操作系统启动进程的入口；`entrypoints/cli`、`entrypoints/agenthttp`、`entrypoints/fleethttp` 是运行后的操作入口，负责参数解析、请求转换、输出格式和事件订阅。它们调用内部操作接口，不拥有执行规则。SSE 属于同一接入协议的输出，不单独成为功能模块。

供应商协议实现仍放在 `providers`，功能自身的 MCP 连接放在对应 Feature，操作系统服务适配归 Fleet；不会因为它们也是某种 Adapter 就归入 `entrypoints`。

## Foundation 的内容与准入标准

Foundation 包含两类内容：有明确用途的技术工具包，以及上层共同使用的中立契约。它不应该成为所有可复用代码的默认位置。

| 类别 | 候选内容 | 边界 |
| --- | --- | --- |
| 技术机制 | `jsonl` 的追加与恢复、进程身份与指标读取 | 不解释 Thread、Turn 或 Feature 的业务规则 |
| 中立契约 | `llm` 的消息值与 Provider 接口、`tools` 的 Tool 输入输出契约 | 不包含供应商实现或具体文件、Shell 等工具实现 |
| 共享机制 | 通用事件传递、安全执行与文件访问机制 | 具体领域事件模式、授权决策及生命周期规则仍由其语义所有者负责 |

进入 Foundation 应当同时满足：职责可以具体命名；存在独立的基础契约或明确的跨模块基础用途；不依赖上层实现，也不决定上层生命周期。仅仅“代码短”“有多个调用者”或“暂时不知道放哪”不足以成为理由。

例如，JSONL 负责持久追加文件的机制，Thread 决定何时提交 Generation 事实；Tool 契约描述一次调用的输入输出，Runtime 决定调用顺序和取消行为，Feature 实现具体工具。Feature 私有的解析和状态辅助函数应留在 Feature 中。

候选表不表示整包可以直接迁移，特别是现有 `events`、`tools`、`sandbox` 中的契约、机制和策略需要分别确认。已确认保留 `foundation` 分组，在其内部直接用具体包名区分两类内容。下方全量清单进一步按实际职责决定归属。

## 候选目录

以下为重点结构示意，不是所有现有包的最终清单。

```text
cmd/juex/main.go
internal/
  app/
    config/
    modulecatalog/
    eventcatalog/
    providerreadiness/
  entrypoints/
    cli/
    agenthttp/
    fleethttp/
    webassets/
  fleet/
  framework/
    agent/
    agentstate/
    endpoint/
    status/
    thread/
    runtime/
    module/
    prompt/
  features/
    filetools/
    applypatch/
    filesearch/
    shell/
    contextcontrol/
    workerthreads/
    extensions/
    operatingcontext/
    goal/
    notes/
    scratchpad/
    agentsmd/
    skills/
    hooks/
    mcp/
    observables/
    chunkedwrite/
  providers/
    openai/
    anthropic/
  foundation/
    llm/
    tools/
    events/
    jsonl/
    sandbox/
    command/
frontend/
tests/
scripts/
docs/
```

Provider 是模型服务适配，不等同于通过 Module 启用的 Feature。独立分组用于区分供应商实现与中立 Provider 契约。

## 全量包归属清单

核查基线：2026-09-07，当前工作区 `HEAD d962f4a`。覆盖 `cmd` 与 `internal` 下全部 **59 个含生产 Go 文件的包目录**（包含嵌套包），不是只枚举第一层目录。

“生产使用者”是当前 `cmd`/`internal` 非 `_test.go` 文件中的直接 import 包，扫描包含各操作系统与 build tag 的源码，不代表这些路径会在一次运行中全部执行。它不包含间接使用者、接口动态调用或测试使用者；职责结合源文件、导出接口和模块文档核查。除 `cmd/juex` 外，当前包、使用者和目标路径均省略 `internal/`。目标是建议归属，逗号分隔表示先拆分再迁移；多个当前包可以合并到同一目标。

这是一份迁移前的架构快照，不作为长期维护的包清单；迁移完成后删除该清单，以代码、边界测试和精简架构文档为准。

### 组装、入口与 Fleet

| 当前包 | 职责 | 当前直接生产使用者 | 目标归属 |
| --- | --- | --- | --- |
| `cmd/juex` | 进程入口、网络引导与 Sandbox 子进程入口 | 进程启动 | `cmd/juex` |
| `app` | 组装、Agent 编排、Feature 注册与跨入口操作 | `cli`, `fleet`, `web` | `app`, `framework/agent`, `features/workerthreads`, `features/extensions` |
| `cli` | Cobra 命令、服务启动入口与终端输出 | `cmd/juex` | `entrypoints/cli`, `app` |
| `config` | 分层配置加载、验证、认证读取与配置发布 | `app`, `bundle`, `cli`, `fleet`, `fleetweb`, `observable`, `providerreadiness`, `web` | `app/config` |
| `fleet` | 注册、进程监督、重启、GC 与配置管理操作 | `cli`, `fleetweb` | `fleet` |
| `fleetservice` | Fleet 操作系统服务安装与管理 | `cli` | `fleet/service` |
| `fleetweb` | Fleet HTTP、目录浏览、活动订阅与代理 | `cli` | `entrypoints/fleethttp` |
| `providerreadiness` | 模型配置、凭据和连通性诊断 | `cli` | `app/providerreadiness` |
| `web` | 单 Agent HTTP/SSE、状态投影、运行组装与 SPA 嵌入 | `cli`, `fleetweb` | `entrypoints/agenthttp`, `entrypoints/webassets`, `app` |

### 执行、状态与领域存储

| 当前包 | 职责 | 当前直接生产使用者 | 目标归属 |
| --- | --- | --- | --- |
| `agentstate` | Agent 身份、Workspace 绑定、注册记录与生命周期锁 | `app`, `cli`, `config`, `fleet` | `framework/agentstate` |
| `bundle` | Thread 调试包、清单与运行快照导出 | `cli` | `framework/threadbundle` |
| `endpoint` | Agent 端点绑定、发现、身份探测与控制 | `cli`, `fleet`, `fleetweb`, `web` | `framework/endpoint` |
| `eventcatalog` | 事件模式校验、具体事件汇总与可见性元数据 | `app`, `web` | `foundation/events`, `app/eventcatalog` |
| `eventmedia` | 外部 Observation 附件解析、验证与持久化 | `app`, `observable` | `framework/observationmedia` |
| `observability` | 由运行事件生成 Thread 可读日志 | `app` | `framework/threadlog` |
| `prompt` | 基于 Module 贡献组装系统提示 | `app`, `runtime`, `runtime/contextbudget` | `framework/prompt` |
| `provenance` | Provider 请求选择身份、脱敏摘要和事件 | `app`, `eventcatalog`, `runtime`, `runtime/module` | `framework/provenance` |
| `runtime` | Input/Turn、Provider 循环、恢复、压缩与工具执行 | `app`, `eventcatalog`, `statusapi`, `web` | `framework/runtime`, `features/contextcontrol` |
| `runtime/contextbudget` | 上下文预算、历史选择与预览 | `runtime` | `framework/runtime/contextbudget` |
| `runtime/module` | 能力契约、注册、启动、激活与关闭 | `app`, `eventcatalog`, `hooks`, `mcp`, `modules/agentsmd`, `modules/builtintools`, `modules/chunkedwrite`, `modules/goal`, `modules/notes`, `modules/operatingcontext`, `modules/scratchpad`, `modules/shelltools`, `modules/skills`, `observable`, `prompt`, `runtime`, `runtime/contextbudget` | `framework/module` |
| `runtime/module/state` | Module 资源所有权、租约和退役 | `app`, `runtime/module`, `runtime/workmem` | `framework/module/state` |
| `runtime/policy` | 压缩和工具输出策略配置值 | `config`, `runtime`, `runtime/contextbudget` | `framework/runtime/policy` |
| `runtime/workmem` | Goal/Notes 状态存储、事件与辅助文件操作 | `app`, `eventcatalog`, `modules/goal`, `modules/notes`, `runtime`, `web` | `features/goal`, `features/notes`, `framework/module/state` |
| `statusapi` | 运行状态 DTO、状态转换与活动快照 | `fleet`, `fleetweb`, `web` | `framework/status` |
| `thread` | Thread 元数据、Generation 历史、索引与归档 | `app`, `bundle`, `cli`, `eventcatalog`, `fleetweb`, `runtime`, `runtime/workmem`, `web` | `framework/thread` |
| `usermedia` | 用户图片输入验证、Thread 作用域与存储 | `app`, `web` | `framework/inputmedia` |

### 功能及功能声明

| 当前包 | 职责 | 当前直接生产使用者 | 目标归属 |
| --- | --- | --- | --- |
| `chunkedwrite` | 分块写入生命周期事实 | `modules/chunkedwrite`, `tools` | `features/chunkedwrite` |
| `extensions` | Extension 发现、Manifest 与声明资源目录 | `app` | `features/extensions` |
| `frontmatter` | Skill frontmatter 解析 | `skills` | `features/skills/internal/frontmatter` |
| `hooks` | 命令 Hook 配置、执行及 Module 生命周期适配 | `app`, `config` | `features/hooks` |
| `mcp` | MCP 配置、连接、工具目录、通知与就绪探测 | `app`, `cli`, `web` | `features/mcp` |
| `modulecatalog` | 具体能力 ID、预设默认值与能力清单 | `app`, `config`, `hooks`, `mcp`, `modules/agentsmd`, `modules/builtintools`, `modules/chunkedwrite`, `modules/goal`, `modules/notes`, `modules/operatingcontext`, `modules/scratchpad`, `modules/shelltools`, `modules/skills`, `observable`, `runtime`, `web` | `app/modulecatalog`, `各 Feature` |
| `modules/agentsmd` | 自动读取 AGENTS.md 并提供上下文 | `app` | `features/agentsmd` |
| `modules/builtintools` | 基本文件、Patch 与搜索的 Module 包装 | `app` | `features/filetools`, `features/applypatch`, `features/filesearch` |
| `modules/chunkedwrite` | 分块写入工具、恢复及历史折叠 | `app` | `features/chunkedwrite` |
| `modules/goal` | Goal 工具、完成策略与压缩贡献 | `app` | `features/goal` |
| `modules/notes` | Notes 工具、上下文与压缩贡献 | `app` | `features/notes` |
| `modules/operatingcontext` | 工作目录、系统和时间上下文 | `app` | `features/operatingcontext` |
| `modules/scratchpad` | Thread 工作文件资源与使用指导 | `app`, `web` | `features/scratchpad` |
| `modules/shelltools` | Shell 工具、会话生命周期与上下文 | `app` | `features/shell` |
| `modules/skills` | Skill 工具和上下文的 Module 适配 | `app` | `features/skills` |
| `observable` | Observation 来源、调度、批处理、投递与工具 | `app`, `eventcatalog`, `web` | `features/observables` |
| `skills` | 内置与文件 Skill 的发现、加载和筛选 | `app`, `cli`, `modules/skills` | `features/skills` |

### 基础机制与模型实现

| 当前包 | 职责 | 当前直接生产使用者 | 目标归属 |
| --- | --- | --- | --- |
| `artifact` | Artifact 路径安全、原子存储与完整性校验 | `app`, `bundle`, `eventmedia`, `llm`, `modules/shelltools`, `observable`, `runtime`, `tools`, `usermedia`, `web` | `foundation/artifact` |
| `cancellation` | 取消原因和操作系统信号分类 | `app`, `cli`, `errorclass`, `runtime`, `runtime/module`, `tools`, `web` | `foundation/cancellation` |
| `environment` | 不可变环境快照、dotenv 与子进程环境解析 | `app`, `bundle`, `cli`, `config`, `extensions`, `hooks`, `mcp`, `observable`, `tools`, `web` | `foundation/environment` |
| `errorclass` | 错误分类及稳定错误种类 | `app`, `cli`, `mcp`, `runtime`, `tools` | `foundation/errorclass` |
| `events` | 事件信封、Bus、模式接口与提交后发布机制 | `app`, `eventcatalog`, `modules/goal`, `modules/notes`, `observability`, `observable`, `provenance`, `runtime`, `thread`, `toolevents`, `web` | `foundation/events` |
| `homestore` | 原子文件发布、目录同步与文件锁 | `agentstate`, `config`, `endpoint`, `fleet`, `fleetservice`, `jsonl`, `runtime`, `runtime/module/state`, `thread` | `foundation/homestore` |
| `jsonl` | JSONL 持久追加、尾部修复与有界读取 | `thread` | `foundation/jsonl` |
| `llm` | 中立消息、Provider 契约、供应商协议、模型健康与展示 | `app`, `cli`, `config`, `eventcatalog`, `fleet`, `hooks`, `modules/chunkedwrite`, `provenance`, `providerreadiness`, `runtime`, `runtime/contextbudget`, `runtime/module`, `statusapi`, `thread`, `toolevents`, `tools`, `usermedia`, `web` | `foundation/llm`, `providers`, `providers/openai`, `providers/anthropic`, `framework/modelhealth`, `entrypoints/cli` |
| `netbootstrap` | DNS 与 TLS 根证书启动补充机制 | `cmd/juex` | `foundation/netbootstrap` |
| `processidentity` | 跨平台进程启动身份读取 | `endpoint`, `fleet` | `foundation/processidentity` |
| `processmetrics` | 进程 CPU 与内存指标采样 | `cli`, `fleet`, `fleetweb` | `foundation/processmetrics` |
| `sandbox` | 文件访问约束、命令隔离与平台执行后端 | `app`, `cli`, `cmd/juex`, `config`, `eventmedia`, `modules/chunkedwrite`, `modules/skills`, `observable`, `tools`, `web` | `foundation/sandbox` |
| `statusstream` | 可替换快照、订阅与有限重放 | `runtime`, `statusapi` | `foundation/statusstream` |
| `toolevents` | Tool 调用事实契约与输出增量信封 | `app`, `eventcatalog`, `observability`, `runtime`, `thread`, `tools`, `web` | `foundation/toolevents` |
| `tools` | Tool 契约、注册和调用机制，文件、搜索、Shell 等实现 | `app`, `cli`, `mcp`, `modules/builtintools`, `modules/chunkedwrite`, `modules/goal`, `modules/notes`, `modules/shelltools`, `modules/skills`, `observable`, `runtime`, `runtime/module` | `foundation/tools`, `features/filetools`, `features/applypatch`, `features/filesearch`, `features/shell`, `features/chunkedwrite`, `foundation/command` |
| `version` | 构建版本元数据 | `bundle`, `cli`, `tools`, `web` | `foundation/version` |

### 测试包与非 Go 资源

| 当前位置 | 使用者与职责 | 目标处理 |
| --- | --- | --- |
| `internal/architecture` | 仅测试：扫描生产 import 并检查分层 | 迁至 `tests/architecture`，按新分组检查完整归属 |
| `tests/e2e` | 跨包、CLI/API 与运行行为测试 | 保留；按被迁移的行为更新 import 与构建路径 |
| `tests/e2e/testdata/env-mcp` | E2E 启动的 MCP 环境测试程序 | 保留，不计入产品生产包 |
| `tests/eval` | 能力评估程序及测试辅助实现 | 保留，其普通 `.go` 文件也不计入产品生产使用者 |
| `frontend` | 浏览器通过 HTTP/SSE 使用后端；不是 Go import 使用者 | 保留源码位置和对外协议 |
| `internal/web/dist` | Go 嵌入的前端构建产物 | 随 `embed.go` 移至 `entrypoints/webassets/dist`，同步构建、安装、CI 和忽略规则 |
| `internal/skills/builtin` | `skills/builtin.go` 嵌入的内置指南 | 随 Skills 移至 `features/skills/builtin`，保留资源语义 |
| 各包的 `_test.go` 与 README | 包行为验证和必要契约 | 随其行为所有者迁移或拆分；中英文同步 |

脚本、发布资源和根文档保留现有目录，仅更新受影响的路径。临时目录（如 `.tmp`）、生成文件、第三方依赖不作为产品包映射对象。

### 必须先拆分的依赖

以下是清单中多目标归属的具体含义，也是机械搬目录前必须解决的依赖。所有调整保持已有 Module ID、JSON 字段、状态文件和 CLI/API 行为。

1. **App 与运行编排。** `app.go`、`agent_runtime.go` 中的 Provider/Module 工厂与启用选择留在 App；输入准入、恢复、Thread 租约、Main/Worker 管理和订阅机制归 `framework/agent`。`worker_threads.go` 中管理器归 Framework，模型工具及 Module 包装归 `features/workerthreads`。Framework 接收已解析参数和构造回调，不反向 import App。`slash.go` 的公共命令路由归 Agent 操作层，Goal 专用指导归 Goal，由 App 显式连接。
2. **配置与预设。** `config` 归 `app/config`，它是应用配置边界，不是 Foundation。Hooks 配置可使用 `features/hooks/config` 的纯声明子包，执行器留在父包；配置验证不能构造 Feature 资源。`observable/manager.go` 当前使用 `config.ShellProfile`，应改成注入 `foundation/command` 的已解析执行参数，去掉 Feature 对 App 配置的依赖。`modulecatalog` 的预设清单归 `app/modulecatalog`，各 Feature 声明自己的稳定 ID，清单引用这些声明；配置包通过参数接收清单，不依赖工厂或形成环。只有 ID 声明而无实现的条目（如当前 Memory）不据此新增功能。
3. **Goal/Notes 状态。** `runtime/workmem` 的 Store、状态值及事件分别归 `features/goal`、`features/notes`。`runtime/thread_state_modules.go` 当前直接返回具体 Store，不能迁移后继续让 Runtime 引用 Feature：状态读取由 App 连接 Feature 读取接口并投影给入口；Runtime 只保留执行所需的 Module 能力。通用资源写入与退役机制归 `framework/module/state`，可复用的原子文件机制使用 `foundation/homestore`；保留当前持久化语义。不要为两个 Feature 再建立一个泛化 workmem 层。
4. **Context Control。** `runtime/context_control.go` 内的模型工具、能力 ID 和提醒贡献归 `features/contextcontrol`，通过窄接口请求上下文切换。实际切换、自动压缩、提交和恢复继续归 Runtime。移动的是工具贡献，不改变 `/new`、`/compact` 或开关语义。
5. **LLM。** `types.go` 和 Provider 调用接口归 `foundation/llm`；`provider.go` 同时含 SDK 依赖、构造与错误分类，需要按符号拆开。供应商文件归各自 Provider 包；统一构造选择放 `providers`，中立层不能 import SDK 或供应商包。Profile 值契约留在中立层，供应商默认值解析归 Providers。`model_health.go` 归 `framework/modelhealth`；终端展示归 CLI；Provider 共享的媒体编码等辅助实现放 `providers/internal`。中立转录校验和消息投影留在 LLM 契约附近。
6. **Tools 与功能实现。** `registry.go`、`schema.go`、`capabilities.go` 及通用调用结果/输出契约归 `foundation/tools`，Provider 顺序和 Turn 调度仍归 Runtime。基本文件、Patch、搜索、Shell 会话、chunked-write 的实现分别并入对应 Feature；删除统一内置工厂对这些实现的依赖，改由 App 逐项组装。Shell 与 Observables 确实共用的执行参数和机制归 `foundation/command`；TTY 会话仍由 Shell 拥有。跨文件功能的路径机制优先归现有 Sandbox；结果清洗和媒体契约只在实际共享时留在基础层。
7. **事件。** `eventcatalog/catalog.go` 的通用校验归 `foundation/events`；`builtin.go` 的具体事件汇总归 `app/eventcatalog`，模式与校验器由各行为所有者提供。核心事件和历史 Feature 事件的解码不能随当前 Feature 开关消失；静态 schema 注册不启动 Feature。`events` 的通用提交后发布机制可整体保留在基础层。`toolevents` 作为多个消费者使用的事实契约归基础层；它不决定何时开始或结束 Tool 调用。
8. **HTTP、Fleet 和状态。** `web/runtime.go` 的资源构造移入 App，HTTP 层接收服务接口。`fleet/web_backend.go` 中依赖 App 的配置验证改为注入配置操作接口，Fleet 不反向引用组装根。`statusapi` 归 `framework/status`，保留共享状态值、投影和快照机制，HTTP 状态码与 SSE 帧归入口。`web/files.go`、`handlers.go`、`observables.go` 的功能访问通过注入对应服务接口实现，避免入口绕过生命周期规则。SPA 嵌入独立为 `entrypoints/webassets`，Fleet HTTP 无需为了静态文件依赖 Agent HTTP 服务。
9. **Extension 与媒体。** Extension 发现、资源解释和功能私有状态归 `features/extensions`；App 根据声明选择并连接 Skills/MCP/Hooks/Observables，不让 Framework 理解 Extension 细节。`usermedia` 与 `eventmedia` 分别归 Framework 的用户输入和 Observation 附件准入，保留 Thread/Agent 作用域规则；底层字节与安全路径存储继续由 Artifact/Sandbox 提供。

`frontmatter` 当前只有 `skills` 一个生产使用者，因此归 `features/skills/internal/frontmatter`。此前 Foundation 的举例仅说明解析工具的性质，不足以证明共享归属。相比之下 JSONL 虽也仅由 Thread 直接使用，却具有独立的通用持久化接口和故障语义，保留为 Foundation 的明确基础机制。

## 依赖规则

- Foundation 不依赖 Framework、Feature、Provider 实现或接入层。
- Framework 使用中立契约，不引用具体 Feature 或 Provider 实现。
- Feature 通过 Framework 的明确接口参与执行；跨功能合作使用契约，不引用彼此私有实现。
- Provider 实现中立接口，不依赖应用编排。
- Entrypoint 调用应用操作接口，负责协议、参数转换和展示，不自行决定生命周期或直接修改领域状态。
- App 可以引用具体实现，负责最终组装；不能把所有跨包逻辑都归入 App。
- Fleet 管理 Agent 进程，Agent Framework 管理进程内执行；两者通过明确接口交互。

接口放在其语义所有者或使用方，而非集中建立一个万能 `interfaces` 包。目录嵌套本身不会限制 Go import；边界测试应覆盖新分组，且应对未分类的新包产生可见反馈。

## 实施顺序与验证

1. 以本文已完成的包归属和直接生产使用者清单为基线，评审上述拆分边界；实施前复核新增包和依赖变化。
2. 对归属明确的包进行纯路径迁移，同步修改引用、构建和嵌入资源路径；避免在同一批混入行为重构。
3. 分别处理 `llm`、`app` 和分散的 Feature 实现，每批保持可编译和可验证。
4. 更新架构边界检查、根架构文档及必要的模块文档，移除过时路径和说明。

代码变更按照仓库的 [本地验证 Skill](../../.agents/skills/juex-localtest/SKILL.md) 执行；跨包行为按实际影响补充验证。可见 Web 行为变化需要浏览器验证。中英文文档执行 `make docs-check`。

## 取舍与待讨论事项

增加分组会加长 import 路径，也会产生较大的机械迁移差异。收益应来自归属清晰和依赖约束；只有增加目录而未改善所有权，不算完成。

已确认的方向：

1. 第一层按架构职责组织。
2. Fleet 保留独立子系统，拥有进程监督生命周期。
3. App 收敛到组装，运行期编排迁入 Framework；具体接口和包拆分仍需分析。
4. Provider 实现独立分组。
5. 对外操作入口使用 `entrypoints` 命名。
6. Foundation 包含中立契约与底层技术机制，按具体包名组织。

逐包映射和九项拆分边界现已补齐，下一步评审重点是这些具体归属。整体方向的确认不等于本轮已授权代码迁移；本轮只更新提案。
