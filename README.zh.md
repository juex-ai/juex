# Juex

> [English](README.md) | 中文

Juex 是一个以受管 CLI package 分发的小型 Go Agent Runtime。它提供 CLI、本地 Web UI、Anthropic 与 OpenAI-compatible Provider、Builtin 文件/Shell Tool、Workspace Observable、本地/远程 MCP Tool、来自本地资源 bundle 的 Skill 与 Hook、可选的 Agent-owned Extension data，以及可恢复的 Session history。

项目有意保持窄范围：它是用于实验 Agent loop 的 Runtime，不是托管服务或一体化 integration framework。

## 快速开始

从已发布的 GitHub Release 安装：

```bash
curl -fsSL https://raw.githubusercontent.com/juex-ai/juex/main/scripts/install.sh | bash
```

POSIX 安装器替换 active package 后会刷新已有的 per-user Fleet service。Release package 包含 Builtin `grep` Tool 使用的固定 `rg`，默认安装到 `~/.local/lib/juex`，并在 `~/.local/bin` 放置直接指向某个 immutable generation 的 command symlink。每次安装都会先写入新的 immutable release generation，再切换 active pointer，因此重新安装同一版本不会删除仍被运行中 Juex process 使用的文件。安装后的 service-manager failure 以 warning 报告，不使 package installation 失效。只有设置 `INSTALL_FLEET_SERVICE=1` 才会安装新服务，且安装器绝不 restart detached Agent。

Linux arm64 release package 要求 glibc，因为 ripgrep 15.1.0 没有 upstream arm64 musl asset。Release 与本地 managed-package installer 会在打包前拒绝 musl 或未经验证的 libc；这些系统必须使用未打包 source build，并通过 `PATH` 或 `JUEX_RG` 提供 compatible `rg`。

在 Termux/Android arm64 与 armv7 上，POSIX release installer 会验证匹配 Linux archive，但只把 static Juex binary 安装到 `$PREFIX/bin`。它使用 `PATH` 中 Termux 原生 `rg`，必要时运行 `pkg install -y ripgrep`，而不安装 archive 中基于 glibc 的 managed ripgrep payload。

Windows PowerShell：

```powershell
iwr -UseBasicParsing https://raw.githubusercontent.com/juex-ai/juex/main/scripts/install.ps1 -OutFile install.ps1
.\install.ps1
```

或从源码构建：

```bash
make build
```

使用首次运行向导创建 Runtime config。默认 home 会把共享 Provider setting 写入 `~/.juex/juex.yaml`。当 `JUEX_HOME` 选择另一个 home 时，只写该 instance 的 `$JUEX_HOME/juex.yaml` override，不改变共享基线。仓库需要自己的 `.juex/juex.yaml` override 时使用 `--scope workspace`：

```bash
juex init
juex init --scope workspace
juex doctor
```

每个 home、Workspace 或显式本地 `juex.yaml` 都可在应用自身字段前 import 可复用的本地或 HTTP(S) 配置：

```yaml
imports:
  - source: ./shared/providers.yaml
  - source: https://config.example/juex/common.yaml
```

Import 按声明顺序运行，使用 declaring file 的 scope 与普通 YAML layer 相同的 field merge rule；declaring file 始终最后胜出。相对路径在该文件旁解析。Imported document 不能含 `imports`，包括 `imports: []`；`--config` 本身仍必须是本地路径。Remote import 只接受完整 `200 OK` representation（或带有效 cache 的 conditional `304 Not Modified`）；response 有界，并以权限受限 Last-Known-Good 副本缓存到 `$JUEX_HOME/cache/config-imports`。临时网络 failure 可使用未过期 cache；`juex doctor` 报告 source、fresh/stale state 与 digest，不包含 URL query value 或配置内容。Redirect 不转发 `Referer` 或原资源 conditional validator，避免 source query token 泄漏，也防止 redirect target 复用无关 cached representation。

一次完整配置加载对每个 remote identity 只解析一次，并只在 Runtime validation 成功后发布完整 pending LKG set；任一 publication failure 会恢复早先 record。Cache reader 在完整 load 期间持有同一个 home-scoped lock，不能观察两次 publication 的混合 record。权限受限的 prepared/committed journal 允许下一个持锁 reader 在 process/machine failure 后 rollback 中断的 set publication，或保留已完整提交 generation。Workspace update 的同一 journal 会在 replacement 可见前保存 previous Workspace byte，使文件与 LKG set 属于一个可恢复 generation。LKG entry 按 remote identity、declaring configuration 与完整 downstream load identity（Workspace 加可选 explicit config）限定，因此一次 load 不能替换另一 load 的验证版本。Fleet-only load 没有 Agent Workspace identity，会选择所有 remote Home import 共用的最新完整 Runtime-validated load context，并从该 context 消费全部 fallback record；没有完整检查不会替换它们。Workspace candidate validation 为 read-only；update 在写入期间继续持 cache lock，只在 Workspace file replacement 成功后发布 fresh LKG content；如果 cache publication 失败或 process 在 combined generation 提交前退出，则恢复 previous Workspace file（或删除新建文件）。配置仍只在 process startup 加载，修改 declaring/imported source 后要 restart Resident Agent。

Juex 为 `run`、`repl`、`listen` 与手动 Session compaction 只加载一次 Runtime environment。`environment.load_dotenv` 默认为 `true`，精确读取 `<WorkDir>/.env`；绝不搜索父目录，dotenv content 作为数据解析，不由 shell 执行。文件缺失没有问题；malformed input 会以 path/line 使 startup 失败。修改 YAML 或 `.env` 后 restart Runtime。

```yaml
environment:
  load_dotenv: true
  variables:
    NODE_ENV: production
```

编译期 Runtime/Session Module 默认 enabled。Runtime 必须完全省略某 capability 及其 resource 时，按稳定 ID disable；未知 Module ID 或字段使 startup 失败。Runtime ID 为 `builtin-tools`、`project-guidance`、`skills`、`side-sessions`、`observables`、`mcp`；Session ID 为 `session-context`、`goal`、`notes`、`hooks`。

```yaml
modules:
  skills:
    enabled: false
  side-sessions:
    enabled: false
```

第一方 Memory 是 `juex-extensions` 仓库分发的外部 Extension。把 `memory` bundle 安装到 Extension root，再用标准 allowlist 选择：

```yaml
extensions:
  allow:
    - memory
```

选中 bundle 贡献 `mcp__memory__memory_search`、`mcp__memory__memory_write`、`mcp__memory__memory_delete` Tool、一个 `ext:memory` Skill 与轻量 index-maintenance Hook。取消选择会移除全部 resource，但不删除 Agent-private data。

Environment precedence 依次为：选中 Extension manifest default；default-home YAML import 与其文件；独立 instance-home YAML import 与其文件；Workspace `.env`；Workspace YAML import 与其文件；显式 `--config` import 与其 YAML；启动时继承环境；child-local MCP/Observable value；最后是 Juex-owned Runtime injection。已有 Agent value（包括空值）会遮蔽 Extension default，并保留 service/shell override。`--config` 不改变 `.env` 位置。非 secret 默认值放 YAML，secret 放 Git 忽略的 Workspace `.env`：每个配置值都会有意提供给 Provider code 与受管 MCP、Observable、Hook、Shell 和 grep process。Juex 拒绝 portable-name violation、NUL byte、Windows case conflict 与 `JUEX_HOME`、`HOME`、`USERPROFILE`、`WORKDIR`、`JUEX_WORKDIR`、`JUEX_EXT_DIR`、`JUEX_EXT_DATA_DIR` 等 bootstrap/runtime name。

MCP Server 与 `juex.yaml` 分开配置。个人 server 位于 `~/.agents/mcp.json`；项目 server 位于 `<WorkDir>/.agents/mcp.json`，同名时覆盖个人 server。支持的 JSON shape 与 Claude MCP config 一致：省略 `type` 表示 `stdio`；remote server 要求 `type: "http"` 或等价 `type: "streamable-http"`。Static HTTP header 可引用 Runtime environment，无需把 secret 写入 resource file。将 `mcp.json.example` 复制到任一位置查看 remote 示例：

```json
{
  "mcpServers": {
    "remote-search": {
      "type": "http",
      "url": "https://mcp.example.com/mcp",
      "headers": {
        "Authorization": "Bearer ${REMOTE_MCP_TOKEN}"
      }
    }
  }
}
```

把 `REMOTE_MCP_TOKEN` 放入继承环境或 Git 忽略的 `<WorkDir>/.env`，然后 restart Juex。`juex doctor` 检查 remote server selection、credential 与 connectivity；`juex doctor --offline` 只跳过 network request。Header value 支持 `${VAR}` 与 `${VAR:-default}`。不支持 legacy SSE、Claude WebSocket extension、interactive OAuth 与 `headersHelper`。

Non-interactive setup 显式传 Provider、Model 与 key：

```bash
juex init --provider openai --model gpt-4.1 --api-key "$OPENAI_API_KEY" --skip-check --yes
```

随后运行：

```bash
juex run "summarize this repository"
juex run --attach screenshot.png "describe this image"
juex --models openai:gpt-4.1,anthropic:claude-sonnet-5 run "summarize this repository"
juex --debug run --json "summarize this repository"
juex repl
juex listen
juex listen --addr 127.0.0.1:9000
juex fleet serve
juex fleet status
```

顶层 `models` list 是完整有序 Model chain：第一个 `provider:model` ref 为 primary，之后为 fallback。更近 YAML layer 替换整个 list。Root `--models` 接受逗号分隔的同类 ref，并为单次 invocation 替换 effective YAML chain。每个 ref 必须解析到 merged Provider config 中声明的 Model。

Transient、authentication、permission 或 model-not-found failure 用尽后，Juex 继续下一个 Model。Process-local cooldown 期间跳过 unhealthy Model，并通过真实 request probe 回到更高优先级 Model。Context overflow、cancellation 与 streamed output 后的 failure 绝不触发 fallback。Model transition 默认不通知 Agent；设置 `runtime.notify_model_changes: true` 可加入 Provider-visible 且 durable 的 `model_change` reminder，不改变 fallback Event 或 selection。

Anthropic、OpenAI、OpenAI-compatible Chat、DeepSeek 与 Codex Provider Profile 会向 verbose CLI/Web Session stream Assistant text 与 reasoning，同时把完整 response 保留为 persisted transcript。只支持 blocking response 的 endpoint 设置 `providers[].capabilities.streaming: false`。

从源码构建但未安装时，用 `./dist/juex` 替代 `juex`。Source build 先从 `JUEX_RG` 再从 `PATH` 解析 `rg`；`juex doctor` 报告 active path、source 与 bundled version。Published release package 的 pinned `rg` payload 缺失或 invalid 时不会 fallback 到 `PATH`。Termux bare-binary installation 有意不打包，因此报告原生 `rg` source 为 `system`。

`juex listen` 通过 canonical local endpoint 发布当前 Agent JSON/SSE API，不打开单独 TCP port。显式传 `--addr` 增加 TCP API listener。该 listener 不提供 React SPA；非 API route 会指向 `juex fleet serve` 浏览器 UI。

`juex fleet` 管理 effective `JUEX_HOME` 下注册的全部 Resident Agent。Fleet setting 继承 `~/.juex/juex.yaml`，可在独立 `$JUEX_HOME/juex.yaml` 中逐字段覆盖；全部 Fleet state 仍只属于 effective home。`fleet add` 注册显式绝对 Workspace；`enable|disable`、`start|stop|restart`、`remove`、`status` 与 `logs` 接受精确 Agent id 或唯一 name。Disable 先停止再持久化可逆 flag。Remove 是独立、确认后的 destructive operation，绝不删除 Workspace file。`fleet serve` 做一次 startup reconciliation、启动 enabled autostart Agent、接管已验证 running Agent，然后在回环 `127.0.0.1:5839` 提供 Fleet Browser API。`/agents/<id>/api/...` request 只转发到刚验证的 Runtime endpoint。Supervisor 保持常驻，退出时不停止 detached Agent。`--addr` 可选择其他回环地址。绑定到非回环地址要求对显式 `--addr` 使用 `--unsafe-bind-any`，或在 home config 的 `fleet.addr` 旁设置 `fleet.unsafe_bind_any: true`；显式 `--addr` 不继承 home permission。`fleet install --addr ... --unsafe-bind-any` 把两项 setting 持久化到 `$JUEX_HOME/juex.yaml`。Installed service definition 启动时读取 home setting，因此修改 config 并重启服务即可移动地址。

`fleet install` 向当前用户 launchd、systemd 或 termux-services manager 注册 Supervisor。Registration name 从 effective `JUEX_HOME` 派生，因此独立 home 可共存。`fleet uninstall` 只删除 Supervisor registration；已 detached Agent 保持运行，可用普通 Fleet lifecycle command 管理。Fleet status 包含各 running Agent binary version，并在与当前 CLI 不同时告警。`fleet install --restart-agents` 在 service installation 后显式刷新当前健康、enabled、bound Agent；stopped、disabled、unbound、unhealthy 与 ambiguous Agent 不变。

Restart 健康 Agent 前，Fleet 检查 Runtime Session state。`turn_active` 与 `draining_pending` work 会在 graceful shutdown 期间以已确认 `runtime_restart` intent 干净取消。只有 healthy replacement 把同 Session/Turn 投影为由 restart 取消后，才收到一个普通 continuation Turn。已失败的 selected Turn 在 replacement 确认同失败 Turn 与 error kind 后也收到一次 continuation。Completed/User-cancelled Turn 不继续。两条路径都保留 prior history，并使用新 `system_notice` Turn，不 replay 原 input 或 Tool Call。缺少 acknowledgement 时跳过 continuation；continuation admission failure 会报告，但不把成功 process restart 变为失败。`fleet stop` 绝不提交 continuation。

## 常用命令

Agent、Session 与 troubleshooting command 从当前目录或 `--cwd` 解析 Workspace Agent。`juex fleet ...` 管理 effective `$JUEX_HOME` 下所有已注册 Agent。CLI information command 不作用于 Agent。`juex init` 设置 effective-home config（未设置 `JUEX_HOME` 时为共享默认 config）或当前 Workspace config。

### Workspace Agent（当前目录）

| 命令 | 用途 |
| --- | --- |
| `juex init` | 在 effective `$JUEX_HOME/juex.yaml` 或 Workspace 创建/合并首次 Runtime config；非默认 home 绝不修改共享基线。 |
| `juex run "<prompt>"` | 在 Active Primary Session 运行一个 prompt 后退出。 |
| `juex run --ephemeral "<prompt>"` | 使用隔离临时 Agent state；加 `--keep` 保留并输出 state path。 |
| `juex run --attach <path> ["<prompt>"]` | 为 text、image-only 或 mixed-content Turn 附加本地图片；多图重复 `--attach`。 |
| `juex --models <provider>:<model>[,...] run "<prompt>"` | 为本次 invocation 替换 configured Model chain。 |
| `juex --debug run --json "<prompt>"` | 写详细 Session log，同时输出正常 run result。 |
| `juex run --new "<prompt>"` | 为 prompt 创建新 Active Primary Session。 |
| `juex run --side "<prompt>"` | 创建 Side Session，不改变 Active Primary Session。 |
| `juex repl` | 启动附加到 Active Primary Session 的交互 CLI Session。 |
| `juex repl --ephemeral` | 启动隔离临时 REPL；加 `--keep` 保留 state。 |
| `juex repl` 中的 `/attach <path>` | 为下一普通 User Turn 暂存本地图片。 |
| `/new`、`/status`、`/compact [instructions]` | `run`、`repl` 与 Web Composer 接受的本地 slash command。 |
| `juex sessions list` | 列出已记录 Session。 |
| `juex sessions show <id>` | 输出 Session metadata 与 transcript。 |
| `juex sessions continue <id> "<prompt>"` | 在已记录 Session 再运行一个 Turn；Side Session 保持 inactive。 |
| `juex sessions activate <id>` | 让 Primary Session 成为 Active Workspace Session。 |
| `juex sessions context <id>` | 输出 Session 的 Active Provider context。 |
| `juex sessions compact <id> --instructions "<focus>"` | 向 Session 追加手动 compact summary marker。 |
| `juex sessions delete <id>` | 删除一个 Session 并从 history 移除。 |
| `juex listen` | 仅通过 canonical endpoint 发布当前 Agent JSON/SSE API。 |
| `juex listen --ephemeral` | 从隔离临时 state listen，不注册 Fleet；加 `--keep` 在 shutdown 后保留 state。 |
| `juex listen --addr 127.0.0.1:9000` | 为 Agent JSON/SSE API 增加显式回环 TCP listener。 |

### Troubleshooting（当前目录）

| 命令 | 用途 |
| --- | --- |
| `juex doctor` | 对 Workspace identity、config、不含值的 environment metadata、credential、connectivity、Shell、MCP 与 Skill 运行 read-only check。 |
| `juex bundle --session <id> --out debug.tar.gz` | 为一个 Session 创建脱敏可移植 debug bundle。 |

### Fleet（`$JUEX_HOME` 下所有 Agent）

| 命令 | 用途 |
| --- | --- |
| `juex fleet serve [--addr 127.0.0.1:5839]` | Reconcile autostart Agent，提供 Fleet API 与 embedded SPA。 |
| `juex fleet install [--addr 127.0.0.1:5839] [--restart-agents]` | 提供时持久化显式地址，注册并启动 Fleet Supervisor，可选刷新 eligible running Agent。 |
| `juex fleet uninstall` | 停止并删除 Supervisor service，不停止 detached Agent。 |
| `juex fleet status [--format table\|json]` | 显示全部 registry entry，并分开 Workspace binding 与 Runtime health。 |
| `juex fleet add <path> [--name N] [--autostart] [--start]` | 注册已有绝对 Workspace，可选启动。 |
| `juex fleet enable\|disable <agent>` | 持久化可逆 enabled state；disable 也停止 Agent。 |
| `juex fleet remove <agent> [--yes]` | 确认后永久删除已注册 Agent state，不删除 Workspace file。 |
| `juex fleet start\|stop\|restart <agent>` | 通过已验证 endpoint identity 管理一个 Resident Agent；restart 在 replacement 健康后继续 interrupted/failed Session work。 |
| `juex fleet logs <agent> [--lines 200]` | Tail Fleet-started Agent 有界 output；接管的外部进程保留原 logging destination。 |
| `juex fleet gc [--yes]` | 检查并显式删除确定 orphaned Agent state。 |

### CLI 信息

| 命令 | 用途 |
| --- | --- |
| `juex --version` / `juex -v` | 输出短 build version；等价 `juex version`。 |
| `juex version [--verbose] [--json]` | 输出 build info；可包含 Runtime context 或 machine-readable JSON。 |

macOS 的 `fleet install` 在 `~/Library/LaunchAgents` 写 LaunchAgent。Desktop Linux 在 `$XDG_CONFIG_HOME/systemd/user` 或 `~/.config/systemd/user` 写 user unit；user manager 要在 login 前启动时运行 `loginctl enable-linger "$USER"`。Termux 在 `$PREFIX/var/service` 写 runit service；安装并初始化 `termux-services`，设备 reboot 后需要启动时使用 Termux:Boot。Installed service 持久化安装器 `PATH` 中的绝对 entry，在前面加入 Juex executable directory 与 `~/.local/bin`，再追加平台默认值，因此 Resident Agent 与 MCP Server 不依赖 `.zshrc` 等交互 shell profile。每个 detached `juex -C <workspace> listen` child 解析自身 Workspace YAML 与 `.env`；Fleet Supervisor 绝不把一个 Agent 的环境导入另一个。

## Runtime 文件

每个 Workspace 有一个 Resident Agent identity。窄 Workspace marker 把它绑定到 `JUEX_HOME`（默认 `~/.juex`）下的 state。

只有普通 `run`、`repl` 或 `listen` 可创建该 durable identity。Session 与 bundle command 要求已有 marker，绝不创建、migrate 或 rebind。`doctor` 把 missing marker 报告为 warning；`version`、`init` 与 Fleet registry command 不需要 Workspace identity。

`run`、`repl` 与 `listen` 接受 `--ephemeral`。Ephemeral mode 保持正常 Workspace/用户 configuration/resource loading，但用私有临时 home 替换 identity-owned state，并在退出时删除。它忽略已有 marker，绝不改变 durable Agent state 或全局 Git exclude，且对 Fleet registry 不可见。`--keep` 保留临时 state，并向 stderr 输出绝对路径。`run --dry-run` 自动使用相同隔离 scratch-state 行为。

```text
<workspace>/.juex/
├── juex.local.json              # {"agent_id":"..."}
├── juex.yaml                    # workspace config
├── extensions/<name>/
│   └── juex.extension.json      # required Extension manifest
└── observables.json             # workspace-authored observable config

$JUEX_HOME/
├── juex.yaml                    # instance override; also the shared base when this is ~/.juex
├── extensions/<name>/
│   └── juex.extension.json      # required Extension manifest
├── .locks/
│   ├── endpoints/<agent-id>.lock # serving-process and GC maintenance guard
│   └── fleet/<agent-id>.lock     # fleet lifecycle serialization
├── fleet.lock                   # one resident supervisor per effective home
└── agents/<agent-id>/
    ├── agent.json
    ├── runtime.json             # agent/instance ids, pid, endpoint, start time, and binary version
    ├── api.sock                 # preferred local API endpoint while serving
    ├── history.json             # cached transcript summaries + active primary id
    ├── logs/fleet.log           # detached child stdout and stderr
    ├── artifacts/               # Agent-owned durable generated bytes
    │   ├── event-media/
    │   ├── read-media/
    │   └── sessions/<id>/       # media, user-inputs, and tool-results
    ├── extensions/<name>/       # Agent-owned persistent extension data
    ├── observables/             # generated runs, observations, and schedule state
    └── sessions/<id>/
        ├── logs/
        │   ├── juex.log
        │   └── debug.log
        ├── session.json         # versioned metadata + rebuildable transcript checkpoint
        ├── conversation.jsonl   # versioned, sequenced, sync-on-commit transcript journal
        ├── conversation.lock    # cross-instance transcript append guard
        ├── events.jsonl         # versioned, sequenced, sync-on-commit runtime journal
        ├── pending_input.jsonl
        ├── notes.md
        ├── scratchpad/
        └── goal_state.json
```

个人 Agent resource 位于 `~/.agents/`；Juex-home Extension 位于默认与 effective Juex home。Juex 始终把 `~/.juex/juex.yaml` 作为共享配置基线。`JUEX_HOME` 选择 canonically distinct directory 时，`$JUEX_HOME/juex.yaml` 覆盖该基线，而 configuration write、lock、Fleet state 与 Agent registry 隔离在 effective home。`JUEX_HOME` 不移动已有 `~/.agents` resource tree。默认情况下 Juex 先加载 `~/.agents/AGENTS.md`，再加载 work-local AGENTS.md，并从 `~/.agents/skills` 与 `~/.agents/mcp.json` 读取 user-global Skill/MCP Server。设置 `enable_user_agents_resources: false` 或传 `--enable-user-agents-resources=false` 可在一次运行中只忽略个人 `~/.agents` resource；不改变 Extension allowlist。

Extension 是在 `extensions.allow` 中按精确、区分大小写逻辑名称选择的目录。省略 setting 表示继承此前 default-Home、effective-Home 或 Workspace layer；显式 list 替换；`extensions.allow: []` 不选择 Extension。若没有 layer 配置该字段，Juex 不加载 Extension。每个 allowed name 从 `~/.juex/extensions/<name>/`、独立 `$JUEX_HOME/extensions/<name>/`、`.juex/extensions/<name>/` 中选择最高优先级安装。更高 layer 整体替换同名 Extension，不与低层 resource merge。

每个选中 Extension 根目录必须含精确大小写 `juex.extension.json`。Manifest version 1 要求 `name` 与目录名大小写精确一致，并要求 SemVer `version`。描述 metadata 可选：

```json
{
  "manifest_version": 1,
  "name": "example",
  "version": "1.0.0",
  "display_name": "Example",
  "description": "Example Extension",
  "author": "Example Author",
  "homepage": "https://example.com",
  "repository": "https://example.com/repository",
  "license": "MIT",
  "requirements": [{"name":"Example CLI","description":"Install and authenticate the Example CLI.","url":"https://example.com/cli"}],
  "agent": {"environment":{"variables":{"EXAMPLE_CONFIG_DIR":"${JUEX_EXT_DATA_DIR}"}}}
}
```

Lark CLI Extension 同时声明两个 Agent-local root：

```json
{"agent":{"environment":{"variables":{"LARKSUITE_CLI_CONFIG_DIR":"${JUEX_EXT_DATA_DIR}","LARKSUITE_CLI_DATA_DIR":"${JUEX_EXT_DATA_DIR}"}}}}
```

当前 upstream Lark CLI build 使用 `CONFIG_DIR` 保存 `config.json`；Linux build 还用 `DATA_DIR` 保存 encrypted keychain。Juex 在全部平台注入二者，使 Extension contract 稳定。

Juex 先选 winning directory，再读 manifest。只验证选中 winner；invalid winner 使 startup 失败，绝不 fallback 到低优先级 copy。Validation 拒绝 malformed JSON、duplicate JSON key、known field invalid value、unsupported manifest version、name mismatch 与 invalid SemVer。Unknown field 被忽略，允许其他 host 增加 metadata 而不阻止 Juex 加载。未选 installed directory 保持 inert。

可选扁平 `requirements` array 仅供信息展示。每项要求非空 `name`、`description`、`url`；顺序和值保留。Juex 在 Runtime status 中暴露这些 entry，但不解析、检测、检查、安装、执行，也不以其 gate startup。Web UI 只把安全绝对 HTTP(S) value 变为 link，否则以 plain text 保持可读。

Extension 可提供 `skills/`、`mcp.json`、`hooks.yaml`、`observables.json`；Runtime status 以 `ext:<name>` source 报告 selected resource。Work-local Extension Hook 必须设置 `trusted: true`；Juex-home Extension Hook 因位置可信。Allowlist 授权逻辑 name，因此 work-local 同名 Extension 可覆盖 Home Extension；它不是 publisher signature 或 source authentication。Selected Extension Observable 使用该 allowlist boundary，并随 Primary Session 启动；Sandbox policy 仍是 Command Observable 的 process capability boundary。

Web Runtime stage 提供 Overview、Extensions、Observables、Logs、Config subsection。Read-only Extensions view 从 startup 使用的同一 Runtime resource graph 显示 selected manifest、install scope/path、effective Skill/MCP Server/Hook/Observable count，列出 requirement name/description/external link，并只列 Extension-declared Agent environment variable name、source 与 effective/shadowed/deduplicated status；绝不返回 value。`juex doctor` 提供同样不含值的 declaration diagnostic。

本地 Extension MCP Server/Hook 接收 `JUEX_EXT_DIR`、selected installation root、`JUEX_EXT_DATA_DIR`（`$JUEX_HOME/agents/<id>/extensions/<name>` 私有持久目录），以及 `WORKDIR`、`JUEX_WORKDIR`。只有选中本地 MCP/Hook process 即将启动时，才以私有权限创建 data directory；configuration discovery、status、doctor inspection、remote-only Extension 与 state-free resource preview 不创建。Extension data 在 Runtime restart、Workspace move、allowlist change 与 Extension removal 后保留，随 owning Agent 删除。Workspace Artifact 与 project-owned Observable definition 留在 `.juex/`；Extension-owned definition 在选中 Extension 中保持 read-only。Manifest `agent.environment.variables` value 只支持 `${JUEX_EXT_DIR}`、`${JUEX_EXT_DATA_DIR}`、`${WORKDIR}`、`${JUEX_WORKDIR}` 确定性替换。它们到达普通 Agent child process，不改变 Juex process、Fleet Supervisor、parent shell 或 shell profile。相同 value default 去重；不同且未被遮蔽的 value、unknown placeholder 与危险 process-control name 使 startup 失败且不打印 value。以 `JUEX_`、`LD_`、`DYLD_` 开头的 name（包括 `JUEX_RG`）是 process-control name，Extension 不能提供。Provider config 使用同样 default-home 再 instance-home merge。Serving Agent 优先 `unix://$JUEX_HOME/agents/<id>/api.sock`；AF_UNIX 不可用时明确 fallback 到 ephemeral `tcp://127.0.0.1:<port>`。

Skill 使用 progressive disclosure。System prompt 包含有预算的紧凑 filesystem Skill catalog，而不是全部 `SKILL.md`；Model 可调用 `skill_search` 发现 catalog entry，再用 `skill_load` 读取完整 Markdown body 与 source path。Juex 还嵌入低频 `observable`、`session_state`、`chunked_write` Tool Group 的指南。它们在 search/Runtime status 中以 `source=builtin` 出现，被 dry-run 列出并由 doctor 计数，但不进入 prompt Skill catalog，因为相关 Tool description 已指向指南。加载只作建议：成功 Tool use 不依赖它；guided group 失败 call 包含命名相关指南的 remediation hint。使用 `skills.include`/`skills.exclude` 控制 merged filesystem Skill；Builtin guide 始终可用。`skills.prompt_budget_chars` 调整初始 filesystem catalog budget。`juex repl` 与 `juex run --verbose` 输出 resource summary；`juex run --dry-run --json` 含 per-section system-prompt token estimate。

Builtin file Tool 为 `read`、`write`、`edit`、`apply_patch`、`grep` 以及 `write_begin`、`write_chunk`、`write_commit`、`write_abort`。`read` 对文本返回 UTF-8，对支持图片返回 structured media reference，使 vision-capable Provider 能检查 screenshot/visual artifact，而不把 image byte inline 到 history。Web Composer 支持 paste/drop/select image；`juex run --attach` 与 REPL `/attach <path>` 接受本地 image path。相对 CLI path 从 workdir 解析，`--attach` 可重复。Image 被复制为 content-addressed、Session-scoped Artifact，并在 Runtime Turn 开始前重新验证；text-only、image-only、mixed-content Turn 共用同一路径。Selected Model `capabilities.vision: false` 时，Juex 保留 canonical media reference，但警告用户并告诉 Model image content unavailable，避免猜测。只对真实接受 image input 的 Model 启用 `providers[].models[].capabilities.vision`。

`apply_patch` 在 `patch_text` 中接受带 `*** Begin Patch` / `*** End Patch` 的紧凑 patch envelope，支持 add/update/delete/move。Path 可为 Workspace-relative 或 Workspace 内绝对路径；等价写法规范化为一个 Workspace-relative identity。写前验证整个 patch、拒绝 Workspace 外 path，返回短 changed-file summary，不向 Provider transcript 回显 patch text。长文件用 `write_begin`；chunked write session 接受有界 chunk、验证可选 chunk/full-file SHA-256，并用临时文件加 rename commit，失败验证不覆盖 target。每 chunk 约最多 2,000 character 或 4,000 byte，保证 Tool argument JSON 在 Model output limit 内。成功 Tool result 还持久化 machine-readable lifecycle fact；Provider-visible history 用这些 fact 保留近期 active chunk，并把已提交 session 折叠为 compact summary。Begin/commit 重验 Workspace、symlink 与 blocked-path boundary；persisted fact 始终使用 normalized relative path。Session resume 时，Juex 从 persisted lifecycle fact 加原始 Tool-use input 重建 active chunked-write state（当 transcript data 足够）。Durable conversation log 仍保留原 Tool-use input 供 replay/debug。

Builtin command Tool 为 `exec_command`、`write_stdin`、`list_shell_sessions`。Juex 根据 Runtime OS 解析 `ShellProfile`：Windows binary 默认 PowerShell（可用时），Linux/macOS 默认 POSIX shell，WSL 中 Linux binary 仍 POSIX，除非显式 `shell.profile: wsl`。`exec_command` 接受 `yield_time_ms`，仅进程仍运行时返回 numeric `session_id`。需要真实 terminal/follow-up input 的交互命令设置 `tty: true`；`write_stdin` poll 运行 session、向 TTY 写 `chars`，或向 non-TTY 发送 Ctrl-C（`\x03`），live output 通过 Runtime Event stream。`list_shell_sessions` 返回 Juex-managed Shell Session，使 Model 在 compaction/遗忘后恢复 active `session_id`；默认只列 running，可用 `include_completed` 显式加入 retained completed Session。Running Shell Session 还以有界 Runtime system-prompt section 出现在后续 Turn/compaction request 中，使 Model 可继续按 `session_id` poll 而不 replay command output。`yield_time_ms` 只限制当前 observation window，不 kill 仍运行 command。

Shell output 持续 drain，并以 1 MiB head/tail budget 保留。Truncated result 保留开头和结尾，并给出精确 omitted-byte marker；更低 `max_output_tokens` 使用相同 projection 但体积更小。Live output 使用最多 8 KiB 的 transient fragment，每个 Shell process 最多 10,000 fragment。这些 fragment 只对当前 subscriber 可见，不进入 `events.jsonl`；`tool.completed`/`tool.errored` 携带 refresh 后使用的有界 authoritative result。Tool Hook 后仍保留有界 Shell base；追加 hook/error context 有独立 128 KiB head/tail bound。因此 Provider-facing result 与 terminal Event 保持一致，不丢失 Shell stream 原始 omitted-byte marker。`exec_command`/`write_stdin` 不受通用 `runtime.tool_timeout` 管理；其 observation window 与 process lifecycle 显式管理。`list_shell_sessions` 仍使用普通 bounded Tool timeout。Shell process 在 parent cancellation、Juex shutdown、manager cleanup 或显式 interrupt input 时停止。非零 exit code 的 completed command 作为 error Tool result 返回，并保留 captured output。Shell execution metadata 也以 structured Runtime Event data 发出，使 consumer 无需解析 Provider-facing text 即可读取 Session、running、exit-code、chunk、truncation state。Binary/binary-like output 在进入 Provider-visible text、conversation history、Runtime Event 或 Web UI 前替换为含 byte count、SHA-256 与 first-bytes hex metadata 的紧凑 placeholder。

Linux/macOS 在整个 `sandbox` section 省略时使用 top-level safe default：启用 sandbox，Workspace 与当前 AgentStateDir 外 host path read-only，network enabled。Windows 默认 disabled，显式 enable 报 unsupported。Linux 要求 `bwrap`（`bubblewrap` package）；运行 `juex doctor` 验证 helper 在本地 user-namespace/kernel policy 下实际能启动，而非只查 executable。Rooted Termux 运行 `pkg install -y root-repo && pkg install -y bubblewrap`；unrooted Termux 无支持 backend，必须显式 `sandbox.enabled: false`。

为兼容，首次显式 `sandbox` section 使用历史 `enabled: false`、`outside_workspace: read_write`、network-enabled baseline，再只应用出现字段，包括 `sandbox: {}`。Existing partial/escape-hatch config 因此保持旧含义。新的显式安全配置应同时设置 `enabled: true` 与 `file_system.outside_workspace: read_only`。

相同文件策略保护 `write`、`edit`、`apply_patch`、chunked write、`exec_command`、grep subprocess 与 Command Observable。Workspace 与当前 AgentStateDir 是唯一默认 host writable root；`apply_patch`/chunked write 仍限 Workspace。`blocked_paths` 覆盖两 root；canonical path check 拒绝 relative traversal/symlink escape。Read-only preset 下，Builtin write 只在 link count 证明 writable root 外存在 alias 时拒绝 multiply-linked regular file。Shared file policy 的首次 restricted command launch 构建同一 inode-based index，只缓存安全结果；完全在 writable root 内的 link 可用。这样无需每条 command 重扫 tree 就能保护 Shell、grep subprocess 与 Command Observable。Linux/macOS 把 `TMPDIR` 指向当前 AgentStateDir，使临时 write 留在已允许 root，又不隐藏作为 Workspace 的 host path。Backend 缺失或不工作时 fail closed，不会无 sandbox 启动 command。

该 policy 是 host file-write isolation，不是 secrecy/approval engine。除 `blocked_paths` 外 host 可读，network 默认开启，所以 readable credential 仍可能 exfiltrate。Trusted lifecycle Hook 与 MCP Server process 在 sandbox boundary 外。Path check 也不声称防御 malicious local process 在验证与 filesystem operation 间竞态改变 symlink/hard link。

Observable 是发出 durable Observation 的 configured source。Definition 来自 writable `.juex/observables.json`（source `project`）和 selected Extension 的 read-only `observables.json`（source `ext:<name>`）。ID 跨 source 全局唯一；collision 会同时命名两 source 并失败。Extension definition 支持已有临时 start/stop/run lifecycle，但不能删除或持久化到 project file。

Command Observable 从 managed command 捕获有界 stdout/stderr batch；Schedule 从 one-shot、daily、monthly 或 interval timetable 发出预先编写 Observation。Monthly recurrence 选择 1–31 日与 IANA timezone 的本地 `HH:MM`；不存在日期当月跳过；DST gap 跳过；重复 DST wall-clock time 只在较早 UTC instant 运行一次。两类 source 共用 list/start/stop/delete/history lifecycle，把 generated state 存到 Resident Agent `$JUEX_HOME/agents/<id>/observables/`，向 Active Primary Session 交付 external Pending input，发出 `observable.*`/`observation.*` Event，并显示在 Web UI。

Web UI 还为 Schedule 提供 `Run`，发出一条 durable configured Observation，不改变 Schedule running/stopped。`Run` 仅是 Web/API control，不注册 Agent-facing Tool。

Project/Extension `observables.json` 只接受 tagged entry：`type: "command"` 加 `command_config`，或 `type: "schedule"` 加 `schedule_config`。旧顶层 command field 和早期 nested `source` shape 报 config issue，不自动 migrate。Model-facing `observable_create` 创建 Command Observable，`schedule_create` 创建 Schedule；其他 `observable_*` Tool 共用。`observable_list` 在 Runtime status 旁含 Schedule read-only tagged `schedule_config`，使 Agent 创建重复 timed work 前比较 recurrence/Observation content。JSONL command parser 可映射 `attachments_field` 的 `[{"path":"...","media_type":"..."}]`；Schedule Observation 可声明 static `observation.attachments`。相对 attachment path 在 Workspace 内解析；绝对 path 可指向 Workspace 或当前 AgentStateDir。Image attachment 在 Event accepted 时、batch/async delivery 前复制到当前 Agent Artifact root 下 content-addressed `event-media/`，随后成为 Provider image block。Validation failure 发出 `observation.errored`，仍在 context 留 structured text。

Command field 不通过 shell，直接在 command/args/cwd/env 中展开 `WORKDIR`、`JUEX_WORKDIR`，Extension definition 还展开 `JUEX_EXT_DIR`、`JUEX_EXT_DATA_DIR`。Project definition 不能设置/引用两个 Extension-only variable，child environment 会移除继承值。每个 Command Observable 获得 Workspace 与当前 AgentStateDir 作为 writable root。Sandbox enabled 且 `outside_workspace: read_only` 时，其他 Agent data 与无关 path 保持 read-only；显式 `blocked_paths` 优先。

Generated run、Observation、delivery、idempotency 与 schedule state 在 AgentStateDir 中跟随 Resident Agent。Creation request 可在 `name` 能 slug 为稳定 lowercase id 时省略 `id`；persisted project entry 含 resolved id。

Turn 期间 Juex 在 Runtime-visible failure ledger 记录 failed Tool Result。Ledger 分类 failure、记录有界 preview/related path、发出 `tool.failure.recorded`，并让后续 successful check 或相关 file mutation 发出 `tool.failure.resolved`/`tool.failure.stale`。Ledger 是 observability，不是独立 finish authority；final-answer continuation decision 属于 Model-owned `goal_state`、`goal-completion-gate` 与 configured Stop Hook。

Turn 已运行时 accepted Pending input 持久化到 Session `pending_input.jsonl`，restart 后在仍安全且未过期时 replay。使用 `runtime.pending_input_ttl` 配置 user steer TTL，`runtime.external_event_ttl` 配置 MCP/external Event TTL。

Juex 在 Session-local `notes.md` 保存 Model-owned working Notes。Model 通过 `update_notes` 重写整份 Markdown；没有 read Tool，因为当前 Notes 在每次 Provider request 中于 Goal 后复述。Notes 最多 2048 Unicode character、在 compaction 后保留，可用 Markdown task item（`- [ ]`、`- [x]`）表示进度。Juex 不把 Runtime fact 推断或镜像到 Notes。

Compaction summary request 携带当前 Goal contract 与 Notes 作为 authoritative Session state。Summary Model 把 contract 复制进 `Goal`，不从 transcript history 重建；unfinished Notes item 约束 `Next Steps`。`compaction.instructions` 设置持续 summary focus。Config instruction、手动 `/compact <focus>` 或 `juex sessions compact --instructions`、成功 `PreCompact` Hook stdout 按此顺序应用。Summary generation 失败时，Juex 沿有序 `models` chain retry，不在 Agent conversation 增加 Model-change message。

每个 persisted Session 还有 `scratchpad/`，保存超出 Notes budget 的长草稿、中间文件和 working material。System prompt 提供绝对 path；Model 用已有 `read`、`write`、`edit`、`grep` 管理。Scratchpad content 不自动加入 Provider context；需要时读回。Prompt 还为 `write_begin`/`write_chunk`/`write_commit` 写长文件提供 Workspace-relative path。Session page 可浏览该目录而不暴露 `.juex` 其他部分；删除 Session 一并删除 scratchpad。

Session-local `goal_state.json` 保存 Model-owned current Goal。Active contract 有意保持小：`description`、`acceptance`、`status`（`in_progress`、`wait_for_user`、`success`、`failure`）、可选 `status_reason`、`continuation_count`、`updated_at`。`acceptance` 是 criteria/artifact/constraint/verification requirement 的 free text；missing `status_reason` 不影响行为。Model 只通过 `get_goal`、`create_goal`、`update_goal` 访问；普通 input 不创建 Goal，Command Hook output 不能修改。Persisted status 为 `in_progress` 且 durable Assistant response 到达 finish attempt 时，Builtin `goal-completion-gate` queue 一次 continuation。该 boundary 前 Provider failure 不改变 Goal，也不创建第二 retry loop；Provider Adapter/Model fallback 负责 bounded request retry。`wait_for_user` 允许 Turn 完成；新 input 不修改 Model-owned contract，由 Model 评估后显式更新 status。Project Hook 仍可加 plain-text context，或用 exit code `2` 请求 Stop continuation。

Lifecycle Command Hook 在 `hooks.commands` 下配置，用于观察/gate Session start、User Prompt submission、Tool use、compaction 与 stop check。Default-home/instance-home Hook 因位置可信；project-local Hook 执行前必须 `hooks.trusted: true`。Hook 从 stdin 接收 JSON，以 plain stdout 加 exit code 响应：`0` allow，`2` 请求 event-specific block/correction，其他 code 报 Hook error。Command lookup、timeout、output-limit、data-directory 与 nonzero-exit failure 默认可观察但 non-blocking；命令设 `required: true` 才向 owning Runtime action 传播。Parent cancellation 始终传播。看似 JSON 的 stdout 仍作为 text。设置 `runtime.show_builtin_policy_traces: true` 可把 Builtin Hook/gate completion/failure 镜像为 conversation 中 UI-only policy trace row。Framework lifecycle fact 使用 `policy.*`；Hooks Module 提供 Hook Event/command name 与原 resource source，包括 `ext:<name>`。

`juex bundle --session <id> --out <file.tar.gz>` 为一个 Session 创建本地 debug archive，包含 manifest、Runtime snapshot、conversation、Event、Session state 与可用 log。默认启用 secret-like value redaction；用 `--include-artifacts`/`--include-worktree-summary` 增加可选 context。Configured Runtime environment value（包括 effective/shadowed Extension declaration）始终从 bundled payload 删除，即使 `--redact=false`；Runtime metadata 只含 key、source、source path。

`--debug` 启用详细 Session-local observability。`--log-level` 接受 `debug`、`info`、`warn`、`error`；默认 `info`，`--debug` 记录 streaming Tool output delta 等 debug-level Event。这些文件是 Runtime Event 的 human-readable projection，不改变 `conversation.jsonl`/`events.jsonl` compatibility contract。

## 开发

从仓库根目录使用匹配当前阶段的 verification tier：

```bash
make verify-plan EXPLAIN=1
make verify-focused PLANNED=1
make verify-focused PKGS="./internal/app ./internal/runtime"
make verify-candidate
make verify-candidate RACE=1 WEB=1
make verify-final
make verify-final RACE=1 WEB=1 COMPACTION=1
```

Validation planning 根据 Git diff 选择 focused package、web/race candidate flag 与 live/compaction final flag。Focused verification 允许 dirty worktree；`PLANNED=1` 显式选择 staged/unstaged/untracked path union，`PKGS=...` 保持必需 targeted scope。Candidate/final 默认用 `merge-base origin/main HEAD`，接受 `BASE=<sha>`，并把 report 绑定完整 pre-run `HEAD` SHA。运行前后均要求 snapshot clean。Report 位于 `.tmp/reports/development-validation/<full-head-sha>/<run-id>/`。Final 只在 record schema、SHA、plan 与稳定 environment fingerprint 全匹配时复用 passing candidate deterministic/build prefix；稳定 input 包括 effective Go setting、build Git description 与 resolved ripgrep binary。Final 始终运行不 retry 的 build-tagged deterministic integration contract，再运行 live integration 与 Provider smoke；plan 在需要时加入 compaction。`RACE=1` 替换 ordinary deterministic suite，`WEB=1` 加 frontend gate 且 binary build 不重建，`COMPACTION=1` 为 final 加 live compaction evaluator；显式 flag 只增加，不删除 planned gate。每个 Go tier 都在 Go-only check 前准备轻量 embedded-web stub，使 fresh checkout 中 focused web package/full suite 无需先构建 frontend。

底层 `make test`/`make race` 继承调用方环境，包括 `HOME`、`JUEX_HOME`、`CODEX_HOME`、默认 Provider config 与 Tool cache。`make test` 使用普通 Go test cache；candidate/final/race/integration 保留显式 rerun 语义。Fresh-checkout ripgrep provisioning 只把 bootstrap Go telemetry 重定向到 disposable path。`make integration` 组合 `integration-contracts` 与 `integration-live`：前者运行不含 credential 的 build-tagged deterministic E2E contract；后者读取 `JUEX_PROVIDER_CONFIG`，否则用调用方 `~/.juex/juex.yaml`。Live case 保留 selected Provider/Codex input，但使用自己的临时 Runtime state。

Frontend 位于 `frontend/`；`make build` 构建 frontend、复制到 `internal/web/dist` 并嵌入 `dist/juex`。`make build-go` 只从已同步 embed asset 编译 binary。

## 文档

| 文件 | 用途 |
| --- | --- |
| `AGENTS.md` | 本仓库 Agent 工作规则。 |
| `DOMAIN.md` / `DOMAIN.zh.md` | 规范产品语言、生命周期与领域不变量。 |
| `PHILOSOPHY.md` / `PHILOSOPHY.zh.md` | 产品与工程原则。 |
| `ARCHITECTURE.md` / `ARCHITECTURE.zh.md` | 实现地图：Module、interface、data flow、test。 |
| `DESIGN.md` / `DESIGN.zh.md` | Web UI 设计指南。 |
| `frontend/README.md` / `frontend/README.zh.md` | Frontend 开发说明。 |
| `tests/e2e/README.md` / `tests/e2e/README.zh.md` | 跨 package E2E 与 live integration 覆盖。 |
| `tests/eval/README.md` / `tests/eval/README.zh.md` | 本地 validation、live Provider smoke 与 evaluation harness 指南。 |
| `docs/compaction/` | Context compaction 调研、V2 设计与 live evaluation 说明。 |
| `docs/superpowers/` | 历史 spec 与 implementation plan。 |
