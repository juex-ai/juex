# 管理客户端

> [English](README.md) | 中文

此 CLI 调用 Management 的公共 HTTP API。它只保存私有登录凭据和选中的租户；
Agent 状态与执行仍由平台管理。部署运维使用 `juex-management` 初始化平台并配置
Provider；原生设备使用 `juex-executor`。

将 `JUEX_SERVER` 设为部署的 HTTPS origin。`juex login --email EMAIL
--password-stdin` 从标准输入读取密码，只输出账号。凭据按 origin 隔离，保存在
操作系统配置目录下。`--session-file` 可选择私有目录中的绝对文件路径。HTTP
需要显式启用开发选项 `--insecure-http`。退出登录会撤销服务端会话并删除本地凭据。
部署使用私有 CA 时，将 `JUEX_CA_FILE` 或 `--ca-file` 指向其 PEM 文件；仍校验主机名。

使用 `juex tenant list` 和 `juex tenant use ID` 选择成员关系。只有一个成员关系时
自动选择。`--owner USER_ID` 允许有权限的管理员管理其他成员；所有权限检查仍在
服务端执行。

使用 `juex fleet show`、`juex agent list` 和 `juex thread --agent ID list` 获取
稳定 ID。JSON 配置命令接受 `--data-file PATH` 或标准输入，请求内容以公共 API
schema 为准。更新使用当前版本。`juex request METHOD /tenants/...` 支持其他公共
资源操作，不另建一套授权或持久化实现。

`juex thread --agent ID send THREAD_ID --text-file PATH --request-id REQUEST_ID`
将输入持久化排队。通过 `thread events THREAD_ID --after SEQUENCE` 读取结果。
输入、Worker、压缩与清理请求失败时会报告请求 ID；响应不确定时应复用该 ID。
不可逆清理还要求 `--confirm` 重复目标 Agent ID 或 Fleet 所属用户 ID。
