---
name: juex-localtest
description: 当项目中的功能开发、bugfix 或重构完成并需要验证代码时使用。实现完成后主动调用：自主运行 focused test、build 和相关本地 eval。
metadata:
  internal: true
---

# Juex 本地测试

> [English](SKILL.md) | 中文

完成任何代码修改后，先运行受影响的测试，再使用与本项目匹配的仓库级验证结束。不要在运行这些命令前询问用户；它们是非破坏性的。

## 前置条件——ripgrep

`grep` builtin 会调用 ripgrep，因此任何使用它的测试（`internal/tools` grep case、`tests/e2e`、`tests/eval` 文件搜索）在找不到 `rg` 时都会失败。运行时 resolver（`internal/tools/ripgrep_resolver.go`）按顺序检查三个来源：`JUEX_RG` override、运行二进制旁的 release-package 布局，以及系统 PATH。Package source 在 `go test` 下绝不会匹配（测试二进制位于 Go build cache，而不是 release package），所以本地运行需要 `PATH` 中有 `rg`（或提供 `JUEX_RG` override）。

`make verify-*` tier 以及较低层的 `make test`、`make race`、`make integration` 和 `make ripgrep` target 会自动准备它。验证 orchestrator 或 Make target 运行 `scripts/ensure-ripgrep.sh`，该脚本输出可用 `rg` 的目录（优先系统安装，否则把固定版本 ripgrep 下载到已缓存且被 Git 忽略的 `.tmp/dev-ripgrep`），并为本次运行把该目录放到 `PATH` 前面。这与 CI 一致：CI 也是把 ripgrep 加入 `PATH`，而不是设置 `JUEX_RG`。应通过 `PATH` 准备，不要使用 `JUEX_RG`：`JUEX_RG` 是会短路其他所有 resolver 来源的 override；对整个 `go test` 进程导出它，也会覆盖读取环境的 resolver 单元测试。`make verify-focused` 会准备不覆盖现有内容的 web embed stub、准备 ripgrep，并通过 `scripts/with-test-juex-home.sh` 运行，因此 fresh checkout 中的 web 测试可以编译，focused test 也不能在开发者 Fleet 中注册测试 Agent。

## 执行步骤

直接从仓库根目录运行命令。

1. **编辑期间的 focused test**——使用显式变更 package 运行 `make verify-focused PKGS="..."`。它允许 dirty worktree，且绝不会把空 scope 扩大到整个仓库。共享 web stub 让 fresh checkout 中依赖 web 的 package 选择仍有效。
2. **PR candidate**——提交实现后运行 `make verify-candidate`。对于 concurrency、shutdown、runtime Turn、MCP、Tool、Event、Thread、web request 或 shared-state 变更，加入 `RACE=1`。对于 frontend 变更，加入 `WEB=1`。Race 会替代普通 suite；web-check 为 Go-only binary build 提供资源，不会再次构建 frontend。不会覆盖的轻量 web stub 让 Go check 可在 fresh checkout 工作。Candidate 与 final gate 会验证运行后 worktree 仍干净。
3. **Final candidate**——交付前运行 `make verify-final`。它会重复 candidate plan，然后运行 live integration 和一个动态选中的 Provider smoke。对于 compaction、context projection、reasoning replay 或 long-Thread 变更，加入 `COMPACTION=1`。Candidate 和 final 要求 clean worktree，并在第一个失败步骤停止。

日常 Agent 交付不要手工组合 `make test`、`make race`、`make integration`、`make provider-smoke` 和 `make build`。这些命令仍可用于底层精确重跑和 harness 开发。

当前 suite 不需要本地服务启动步骤。Web 测试使用 `httptest`，live integration 直接驱动 Runtime。

## 关注范围

- **Shell/Tool/Runtime 变更**——使用 `make verify-focused PKGS="./internal/tools ./internal/runtime ./tests/e2e"`。对于跨平台 shell 行为，还要对修改的 package 运行 Windows target compile check，例如：

  ```bash
  GOOS=windows GOARCH=amd64 ./scripts/with-test-juex-home.sh go test -c ./internal/tools -o /tmp/juex-tools-windows.test.exe
  ```
- **Eval harness 变更**——运行 `make verify-focused PKGS="./tests/eval"`；其 contract suite 包含 module 与 wrapper help check。
- **仅文档或 Skill 变更**——运行 `git diff --check`、stale-reference 搜索，以及针对受影响命令示例的最小 focused test。
- **Web 可见变更**——在 candidate/final 上使用 `WEB=1`，随后在重建的二进制上运行 browser/API smoke（当行为在 UI 中可见时）。

## Live Provider/Model Sweep

日常交付通过 `make verify-final` 获得一个动态选择的 `provider:model` smoke。当用户要求测试所有已配置模型，或 Provider compatibility 变更需要更大矩阵时，对 final candidate binary 显式运行底层 smoke：

```bash
make verify-final
bash tests/eval/provider_model_smoke.sh --juex ./dist/juex --all-models
```

规范脚本按 `--config`、`JUEX_PROVIDER_CONFIG` 或原用户的 `~/.juex/juex.yaml` 顺序解析配置。日常运行使用记录的 selection seed 从该配置选择一个 eligible ref。当没有 eligible ref 时，脚本以 `provider_unavailable` 失败。对每个选中的模型，它会创建隔离的临时 workdir，只把对应 Provider/Model 复制到临时配置，并使用临时 `HOME` 运行 Juex，使全局 MCP server 与 Skill 不会加载；同时传入 `--enable-user-agents-resources=false`。临时配置包含 credential，成功后会删除，除非传入 `--keep`。

每个 case 运行一个 live Agent 工作流，该工作流必须使用 `read`、`write`、`edit`、`grep`、`exec_command` 和 `write_stdin`，其中包括带增量输出的 `tty:true` 命令与执行中途的 stdin 回复。结果行报告 Tool use、`exec_command`、TTY、stdin、filesystem、terminal-event 和 thinking 覆盖；短暂 output delta 由确定性 live-stream 测试验证，而不是从持久化 smoke artifact 验证。除非传入 `--report-dir`，脱敏报告写入 `.tmp/reports/provider-model-smoke/<run-id>/`。

常用选项：

```bash
bash tests/eval/provider_model_smoke.sh --only provider:model
bash tests/eval/provider_model_smoke.sh --all-models
bash tests/eval/provider_model_smoke.sh --selection-seed reproducible-seed
bash tests/eval/provider_model_smoke.sh --work-root /tmp/juex-provider-smoke --keep
bash tests/eval/provider_model_smoke.sh --report-dir /tmp/juex-provider-report
bash tests/eval/provider_model_smoke.sh --timeout 360
bash tests/eval/provider_model_smoke.sh --retries 0
```

`--all-models` 运行已解析 Provider 配置中所有 eligible ref。Provider smoke 只排除 effective tools capability 显式为 false 的 profile；每个选中 profile 使用同一个严格契约。

## 开发评估

```bash
bash tests/eval/development_eval.sh
```

Development evaluator 在 `.tmp/reports/development-validation/<run-id>/` 下记录命令日志和摘要。默认运行 deterministic test、build 和一个 seeded provider-config smoke。它的 deterministic plan 复用 candidate orchestrator，不会增加第二次 E2E 运行。当需要持久开发验证报告时使用它；`make verify-*` tier 仍是日常交付 gate。只有验证 harness 本身或 live Provider 无关的文档示例时，才使用 `--skip-tests` 和 `--no-provider-smoke`。

使用 `--only provider:model` 限定 Provider smoke。变更涉及 compaction、context projection、Provider reasoning replay 或 long-Thread 行为时使用 `--compaction-eval`。Compaction evaluator 按记录的 seed 从已解析 Provider 配置选择一个 eligible ref，并在开发记录下写入 scorecard。显式 context window 不足的模型会被排除。使用 `--compaction-only provider:model` 进行 focused compaction 运行；当更大改动需要一次覆盖所有 eligible 配置模型时使用 `--compaction-all-models`。JSON、Markdown 和 terminal summary 会记录 seed、candidate set、脱敏 config hash 和复现命令。

直接 compaction 入口：

```bash
bash tests/eval/compaction_eval.sh --only provider:model
bash tests/eval/compaction_eval.sh --all-models
```

## 失败处理

- 如果 build 失败：先修复编译错误，不要继续测试。
- 如果 unit test 失败：先修复，再运行 integration test。
- 如果 integration test 失败：报告失败和错误详情；不要压制或绕过。
- 如果因为默认 `~/.juex/juex.yaml` 不存在而使 `make integration` 跳过 live case，明确报告该路径；显式 `JUEX_PROVIDER_CONFIG` 路径或已存在但不可用的配置必须失败。不要发明 credential 或用假的 live test 替代。
- 如果 live Provider 或 compaction eval 失败，保留 `.tmp/reports` 输出，并在合并前说明失败属于配置、Provider capability、prompt-following 还是 Juex regression。
