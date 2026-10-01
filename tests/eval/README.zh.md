# Managed 平台评估

> [English](README.md) | 中文

Python 模块 `tests.eval.juex_eval` 规划绑定提交的 candidate/final 门禁。
[本地测试 Skill](../../.agents/skills/juex-localtest/SKILL.zh.md) 是权威流程。
记录包含提交、工作树状态、命令、环境指纹、全部服务及客户端产物身份和脱敏真实验证证据。
只有候选身份匹配才可复用；失败或缺少证据不能变成成功。

真实测试使用实际 Runtime、隔离 PostgreSQL 数据库、认证公共/RPC API 和原生执行端。
`JUEX_TEST_POSTGRES_URL` 指向可丢弃的测试实例，其角色须能创建数据库；
`JUEX_PROVIDER_CONFIG` 指向如下结构的私密 JSON/YAML 文件：

```json
{"models":[{"provider":"example","name":"MODEL","protocol":"openai/chat","endpoint":"https://provider.example/v1","api_key":"TEST_SECRET","context_window":131072,"max_output":8192}]}
```

不会自动发现个人 Runtime 配置。凭据不进入 Git，文件权限为 0600。
报告遮盖所选 API key，每次运行结束都会删除临时所选模型文件。
模型通过种子可重复选择；`--only provider:model` 和 `--all-models` 明确测试范围。

```sh
mise exec -- uv run python -m tests.eval.juex_eval integration
mise exec -- bash tests/eval/provider_model_smoke.sh --only example:MODEL
mise exec -- bash tests/eval/compaction_eval.sh --only example:MODEL
```

Integration 通过公共对话 API 验证真实 assistant 回复。Provider smoke 要求
read/write/edit/grep 以及一次实际 PTY/stdin 工作流，并核验操作完成证据。
压缩测试要求持久检查点、Runtime 重启和事实保留。Go 成功退出还不够，必须存在真实
验证标记。缺少配置明确失败，不能跳过。`tests/eval` 测试覆盖这些失败和凭据边界。
