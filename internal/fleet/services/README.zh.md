# Fleet 独立服务

> [English](README.md) | 中文

Fleet 管理服务进程与发现，Feature 拥有业务 API 和存储，App 负责组装。
普通 Agent Module 保持进程内作用域。`../service` 负责 OS 服务注册，职责独立。

只有所属 `JUEX_HOME/juex.yaml` 及其 imports 定义 `fleet.services`。
自定义 Home 不继承默认 Home 的服务定义，Workspace 和 Agent 层不能定义
Fleet 设置。重启 Fleet 管理以加载定义，再重启受影响服务以应用 command
或不透明 `config`。App 默认提供 managed Basic Memory，可显式设置
`fleet.services.memory.enabled: false` 关闭；其他服务需要显式定义。

```yaml
fleet:
  services:
    example:
      mode: managed
      enabled: true
      command: [/absolute/path/to/service-implementation]
      network: unix
```

可执行程序实现 lease/readiness 和类型化控制契约。App 在打开业务存储前
调用 `services.Acquire`，持有至优雅结算结束；恢复存储、打开监听器，
将业务 RPC 与 `serviceendpoint.ControlServer` 一起注册，再通过
`Lease.Ready` 写入候选记录。Fleet 在启动环境中传递 Home/Fleet/服务/实例。
[Memory](../../features/memory/README.zh.md) 基于此契约组装独立业务服务。

使用 `juex fleet services list`、`status`、`start`、`stop`、`restart`、`logs`。
除日志外输出 JSON；HTTP 在 `/api/services` 提供对应操作。管理启动不等待
可选服务，管理退出保留服务运行。没有服务 Web 页面。

## 所有权与恢复

- `fleet.json` 保存稳定 Fleet 身份，每次启动生成新实例。PID 和进程启动身份
  仅供诊断；停止使用精确身份 RPC，并确认 writer lease 释放。
- 显式停止在 RPC 前持久保存，管理重启后保留。启用配置单独管理：禁用停止
  受管写入者，未完成关闭仍可见，重新启用尊重显式停止。自动恢复最多使用
  三次持久启动预算，直到显式 start/restart 重置。每五秒协调一次。
- 生命周期锁串行管理操作，服务全程持有 writer lease。替换意图时持有 writer
  锁；子进程获得 lease 后重读意图，阻止延迟旧进程访问存储。lease 被占用
  但就绪未验证时，绝不另起写入者。
- 服务恢复后写入实例专属候选记录。Fleet 经 RPC 核对 Fleet/服务/实例，
  再原子发布公共发现记录。管理在发布前崩溃，重启后可从候选记录接管。
  清理会核对已检查的实例。

持久状态位于 `services/<id>/`，发现记录为 `run/services/<id>.json`，
不包含业务数据。本地默认 Unix socket，Windows 默认 TCP。显式 TCP 支持
端口零并发布实际地址。过长 Unix 路径使用按 Fleet 隔离的哈希临时路径。

外部定义必须有 `mode: external`、`network`、`address`，禁止 command/config。
endpoint 必须声明所属 Fleet/服务身份。Fleet 验证并发布当前实例，但从不
启动或停止它。禁用只移除本地发现，日志由外部部署管理。

## 客户端

注入明确 Home/Fleet 的 `serviceendpoint.Resolver`，不搜索其他 Fleet。
构造支持离线，调用可在失败或实例替换后重新解析。共享 Kitex 类型化 Thrift
控制调用期限两秒，每次请求/响应验证身份，不盲目重试变更。业务客户端必须
在实际业务 RPC 请求中核对同样的期望身份；独立探测不能验证池化业务连接。

身份校验防止意外串路，不防冒充；假设 OS 用户和网络可信。服务负责已接收
业务、幂等性和优雅结算。Fleet 不提供通用队列、业务代理或 Module host。

绑定前不会删除显式 Unix socket 路径，因为它可能属于其他 Fleet。
异常退出留下的固定路径需要检查；默认实例专属 socket 避免此类复用歧义。
