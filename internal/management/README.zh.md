# Management

> [English](README.md) | 中文

Management 拥有全局用户、租户、成员关系，以及用户在每个租户内的唯一 Fleet。登录凭据与稳定 User 身份分离。PostgreSQL adapter 拥有 `management` schema；其他服务通过业务接口调用，不直接写入这些表。App 负责数据库连接与组装。Management 不依赖以 Home 为基础的 Fleet 进程管理器。

当前目录是平台第一批基础，尚未接入公开 HTTP、认证、Web 或 Runtime。`CreateUser` 是身份创建原语，`CreateTenant` 是运维操作，两者都不是公开注册入口。适配器必须从已认证会话取得 actor ID。成员操作传入的角色是修改目标，不是调用者权限证明。

每个成员写入操作先锁定 Tenant 行，再读取当前权限并检查最后一位有效管理员。成员变更、actor/owner 审计事实与 outbox 在同一事务提交。成员版本保留停用／恢复的顺序。outbox 持久化只是意图记录，不证明下游取消完成；消费者不能因为新版本已恢复访问就丢弃先前的停用事件。

接受邀请要求登录匹配的账号，不会验证邮箱。令牌单次使用、有期限、以摘要保存，重新签发使旧令牌失效。已移除成员只能接受新邀请恢复，沿用保留的 Fleet。创建邀请时返回令牌原文；后续 Dashboard 持续展示复制链接需要加密保存令牌，摘要不能还原邀请链接。

`Fleet` 仅授权读取。有效租户管理员可以查看已移除或停用成员的保留数据；读取结果不授予配置修改或执行权。代管读取同时记录 actor 与 owner。执行及设备许可授权是独立的后续操作。

通过 `postgres.Migrate` 显式初始化 schema，迁移在事务中串行执行并校验摘要；未知或被修改的版本直接失败，不静默修复。`tests/e2e` 的数据库集成测试使用 `postgres` build tag，要求 `JUEX_TEST_POSTGRES_URL` 指向有建库权限的临时测试服务器。每个用例创建和删除独立数据库。CI 使用 PostgreSQL 18 执行这些用例。
