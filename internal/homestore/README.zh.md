# Home 存储

> [English](README.md) | 中文

此包负责持久 Juex 状态使用的可移植文件系统基础能力：

- 具有显式阻塞或 try-lock 行为的建议文件锁；
- `$JUEX_HOME/.locks/<scope>/<id>.lock` 布局；
- 同目录临时文件发布与 Windows 上的持久替换；
- 原子发布创建新目录时的父目录链同步；
- 在文件系统不支持目录 fsync 时仍可容忍的父目录同步。

Windows 替换会在同一临时文件且持久标志不变的前提下，最多重试七次 access-denied 和 sharing-violation 错误。六次指数退避总计请求休眠 315ms；操作系统调用和调度还可能增加耗时。其他错误立即返回；持续冲突会失败，且不会删除目标文件或报告替换成功。

`agentstate`、`endpoint` 和 `fleet` 继续拥有各自的身份与生命周期策略。`fleetservice` 继续负责多个原生服务文件的事务式发布。原子写入错误会表明是否已经发生替换，使事务调用方只回滚自己拥有的路径。它们仅把文件系统机制委托给此包。

Workspace 身份和全局 Git exclude 锁仍放在操作系统临时目录中。为兼容混合版本，Supervisor 锁仍位于 `$JUEX_HOME/fleet.lock`；两者使用同一个可移植锁原语，但不采用 home 锁布局。
