---
name: juex-localtest
description: Validate changed managed-platform code, then run commit-bound candidate/final gates with real PostgreSQL and provider evidence.
metadata:
  internal: true
---

# JueX 本地测试

> [English](SKILL.md) | 中文

代码修改后执行本流程。已获授权的非破坏性检查不再请求确认。
始终使用隔离测试服务和数据库，不为验收重启生产 Agent 或迁移其状态。

## 验收门禁

在仓库根目录运行，优先使用 `mise exec --`。

1. 编辑期间执行 `make verify-focused PKGS="./changed/package ./tests/e2e"`。
   必须明确范围，或用 `PLANNED=1` 从差异推导。
2. 提交实现后执行 `make verify-candidate`。共享状态、Runtime、生命周期、工具、API
   和事件修改加 `RACE=1`；前端修改加 `WEB=1`。门禁前后工作树都必须干净。
3. 交付前执行 `make verify-final`。它验证相同候选，并加入 PostgreSQL 集成和真实
   Provider 测试。压缩、上下文投影、Provider 重放和长 Thread 修改加 `COMPACTION=1`。

Candidate 构建全部客户端和服务。Web 检查覆盖类型、单测、lint、生产构建与浏览器
交互，并直接供二进制构建使用，不重复构建前端。Final 复用绑定提交、计划、环境及
每个构建产物，变更或缺失证据会重新执行对应门禁。交付前仍须执行 `make docs-check`
和 `make lint`。

可见 Web 行为还需要针对重建后的运行服务做浏览器验证。API/Runtime 修改需要对应
`tests/e2e` 覆盖，包括带 PostgreSQL 构建标签的用例。原生执行端修改要求 Linux/macOS
编译及相关真实设备行为验证；托管边界变化要求 Linux Docker/runsc 验证。

## 隔离数据库与真实模型配置

`JUEX_TEST_POSTGRES_URL` 指向可创建和删除隔离数据库的 PostgreSQL 测试实例。
每个端到端夹具拥有其临时数据库，不使用生产凭据。`JUEX_PROVIDER_CONFIG` 指向
明确的私密 JSON/YAML 模型配置，格式见 [评估说明](../../../tests/eval/README.zh.md)。
Final 验收必须提供两者。缺少配置会失败，不发现个人 Home，不静默跳过真实测试。

Provider 测试通过公共及认证 RPC 边界运行实际持久 Runtime 和原生执行。
所选模型必须完成 read/write/edit/grep 及 PTY/stdin 交互。压缩验证保存检查点，
重启 Runtime，再核验事实保留。设备和模型测试状态隔离，临时 Secret 文件始终删除。
报告保存在 `.tmp/reports`，遮盖 API key 并记录模型选择证据。

失败诊断时可精确重跑：

```sh
bash tests/eval/provider_model_smoke.sh --only provider:model
bash tests/eval/compaction_eval.sh --only provider:model
```

`--all-models` 测试全部符合条件的已配置模型，`--selection-seed` 重现选择。
不能替换模型后声称原模型通过。确定性测试、真实模型检查、模拟容量和真实设备证据
必须分开报告。

## 失败处理

先解决编译，再进行行为检查。保留失败日志，区分配置、Provider 可用性、模型行为
和产品代码问题，重跑受影响契约。不能把跳过或没有证据的运行记为通过。
验收脚本修改必须执行 `make verify-focused PKGS="./tests/eval"`。
纯文档修改执行双语检查、差异检查和受影响命令帮助检查。
