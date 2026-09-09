# Proposal：Worker 层数限制与 Thread 导航改进

> [English](worker-depth-navigation.md) | 中文

状态：提案，功能尚未实现。更新：2026-09-09。
实施任务：Taskline `bfb66d37-fc99-425b-b389-6836515b7af7`。
建议强度：Strong。

## 问题与目标

目前 Worker 可以继续创建 Worker，没有嵌套层数限制。debaga 在 2026-09-08 发起的 CLI Proxy API 排查衍生了 310 个 Worker，最深 17 层，Worker 累计用量 221,226,752 tokens。层数限制用于阻止递归扩展，不等价于 Worker 总量、并发数或 token 预算限制。

Thread Explorer 每行有重复图标、留白偏大，未展示父子关系；对话正文区域缺少持续可见的当前 Thread 身份。

本提案限制创建深度，压缩列表，并用父线程定位替代树状展示。

## 一、Worker 最大层数

```yaml
modules:
  worker-threads:
    enabled: true
    max_depth: 1
```

Main 深度为 0，每条 parent 关系增加 1 层：

| max_depth | 允许结构 | 创建权限 |
| --- | --- | --- |
| 1（默认） | Main → Worker | 仅 Main 可以创建 |
| 2 | Main → Worker → Worker | Main 和第一层 Worker 可以创建，第二层不可以 |

### 配置规则

- 省略时默认 1，只接受整数 1 或 2；0、负数、大于 2、非整数及显式空值报配置错误。
- max_depth 与 enabled 独立分层合并：只改其中一项不覆盖另一项；高层显式值覆盖低层。
- minimal/standard 保持现有启用默认值，不改变深度默认值。
- 模块关闭时可以保存合法深度，但不启动执行能力；非法深度仍报错，避免重新启用时才暴露错误。
- 只有 worker-threads 接受 max_depth；其他模块使用该字段报错。
- 沿用现有配置加载和重启生效机制，不增加独立热更新。

### 创建边界

- 达到或超过上限的 Thread 不装配 worker-threads 模块，对该 Thread 而言该模块不存在。模块的全部工具、工具 schema、提示词、指导、资源和生命周期贡献均不发布、不初始化；不是只隐藏 thread_create。
- 因此 max_depth=1 时，仅 Main 拥有 worker-threads；max_depth=2 时，Main 与第一层 Worker 拥有该模块，第二层及更深 Thread 不拥有。最终可用性由模块配置开关与当前 Thread 深度共同决定，不改写 Agent 的全局配置。
- 符合新上限的 Worker 自身仍正常执行；其父 Thread 深度低于上限且启用了模块时，仍可管理它。限制的是 Worker 自身向下管理的模块能力。外部用户的 Thread Explorer、历史与存储管理不属于该 Thread 的模块贡献，继续保留。
- 服务端必须校验实际持久 parent 链，不能仅靠隐藏工具或提示词限制。模型工具、HTTP、CLI 间接调用和内部 Runtime 创建共用规则。
- 模型创建的 parent 仍从调用 Thread 推导，不允许模型伪造。
- 超深请求在创建目录、写索引、启动子 Runtime 和调用子 Provider 之前失败，错误明确指出 max_depth。
- 已存在的深层 Thread 不删除、不迁移、不改写 parent，历史与用量保留。若其父 Thread 已达到新上限，父方不再具有发送、订阅、停止等 worker-threads 能力；不为历史子节点例外恢复模块或管理工具。
- 这些历史深层 Thread 的恢复、执行、停止和保留管理由宿主执行/生命周期接口承担，用户通过现有 API、CLI 或界面操作，仍受 Agent 级模块开关及既有运行约束控制。恢复和清理不得依赖为上限父 Thread 重新装配模块；既有订阅/结果交接按正常停机与恢复规则收口，不恢复不可用的父方订阅。仍禁止继续向下创建。
- 重启或单独打开 Worker 后按持久父链计算；归档祖先不使深度归零；缺失 parent 或环形异常拒绝创建。
- 维持模块关闭后可管理 Thread 存储的契约；允许的存储创建入口也遵守层数限制，避免重新启用后绕过。

## 二、紧凑列表与父线程定位

保持 Active、Archived 两个平面区域和现有排序。

- 删除行内重复的对话图标和占位。
- 缩小行内外 padding、身份行和统计行间距，统计行取消原图标缩进。
- 桌面两行内容建议以约 48–56 px 行高为起点，以浏览器实测调整，不固定高度裁切内容。
- 保留现有状态、Turn/Generation、pending、context、累计用量及用量详情；操作按钮保持触屏和键盘可用。

身份行示意：

```text
reviewer · #abc123   parent → main · #0
```

- 沿用 alias · #id 身份格式。parent 标记使用轻量边框或底色。Main 不显示 parent。
- 父 alias 从完整列表快照获取，不逐个读取 Thread 正文或目录。
- 点击标记只将列表滚动至父行，不进入父对话、不改变选中 Thread、不触发行操作。
- 父行滚动到可见区域中部并获得焦点，高亮约 3 秒；重复点击重置计时，定位另一父行时转移高亮。
- 支持 Active/Archived 跨区域定位。父行缺失时显示 parent → #id 与不可用提示，不提供无效点击。
- 长 alias 可截断并查看完整名称，窄屏标记允许换行，不横向溢出。
- 支持键盘激活、减少动态效果偏好；卸载或切换 Agent 清理计时与高亮。

## 三、导航栏中的 Agent 与当前 Thread 身份

复用现有 Chat/Runtime 导航左侧的 Agent 身份区域，改为紧凑的上下两行，不在正文区域另加标题栏：

```text
debaga                         Chat   Runtime
reviewer · #abc123  [Idle]
```

- 上行显示 Agent 名称；下行显示当前 Thread 的 alias · #id，后面紧跟该 Thread 的状态标签。
- 导航栏维持现有高度（当前样式变量为 52px）；通过缩小行高、第二行字号及上下间距容纳两行，不增高、不额外占用对话空间。长名称截断并可查看全称，状态标签和 Chat/Runtime 切换仍可用。
- 身份取自当前 Thread 详情，标签取自同一 Thread 的权威状态快照；不要使用其他 Thread 或 Agent 汇总活动状态冒充当前 Thread 状态。归档 Thread 明确显示 Archived；加载或状态未知时不默认显示 Idle。
- 当前源码中 FleetStageHeader 的标签使用 agentVisualState(agent)：综合 Agent runtime_health 与 agent.activity.state，属于 Agent 展示状态，不保证对应当前页面 Thread。本次需调整数据来源，不能仅将现有标签挪到下行。
- Agent 进程健康与 Thread 工作状态保持分离；进程停止、连接异常等沿用已有健康/不可用提示，不能伪装成当前 Thread 的执行失败或空闲。
- 正文滚动时导航保持可见；切换 Thread、Agent 或更新 alias 后两行同步刷新，加载新 Thread 时不展示旧身份和状态。
- Thread Explorer 与 Agent Runtime 页面没有正在查看的 Thread 时，下行显示页面上下文（Threads/Runtime），不把 Agent selected_status 或上次打开的 Thread 当成当前 Thread，也不显示 Thread 状态标签。Fleet settings 保持自身标题。
- 不重复完整统计面板，不新增复杂导航。

## 模块归属与实现方向

- internal/app/config：参数解析、默认值、分层合并、合法性和 sparse overlay。
- internal/features/workerthreads：整模块的能力与生命周期贡献；达到上限的 Thread 不装配该模块。
- internal/framework/agent：统一创建约束；所有执行入口共用同一深度判断。
- internal/framework/thread：提供所需持久拓扑访问，不读取 YAML、不依赖 Feature 配置。
- internal/app：注入解析后的限制，按配置开关与持久深度计算每个 Thread 的模块装配；到达上限不构造其 worker-threads 模块，Worker 自身执行能力保持独立。
- internal/entrypoints/agenthttp：调用受约束入口，不另写一套深度算法。
- ThreadExplorer 页面：紧凑行、父标记、定位与高亮。
- AppShell/FleetStageHeader：固定高度的 Agent/Thread 两行身份区域；Thread 页面通过现有 shell 上下文传递当前 Thread 身份与状态，不额外创建正文标题栏。

复用已有 parent 元数据与列表索引，不新增持久 depth 字段，不根据临时 Runtime 指针推断深度；不扩展通用权限框架或树组件。

## 验收标准

1. 配置测试覆盖默认 1、显式 1/2、非法值/类型、其他模块误用、enabled 独立合并、imports 与 Agent overlay 保存读取。
2. 模型/API/CLI 跨包测试覆盖两种上限：允许层创建成功、超限失败、无残留 Thread 状态且无子 Provider 调用；检查实际 Provider 请求、工具目录和模块生命周期，证明上限 Thread 没有任何 worker-threads 工具、schema、提示词、指导或资源，也不初始化模块。
3. 覆盖重启、单独恢复、归档祖先和坏父链；历史保留、深度不被重置。符合上限的 Worker 可接收仍拥有模块的父方工作。另用“旧 depth=2、改为 max_depth=1”验证：depth=1 父方完全无模块，旧子节点由宿主接口恢复、执行、停止和管理，订阅/交接正确收口，不因保留历史而复活父方工具或模块。
4. 浏览器验证列表紧凑、统计可读；父标记跨区域定位、焦点、高亮及三秒后清除、重复点击与缺失父行。
5. 浏览器验证导航两行身份、Thread 状态及切换，导航高度保持 52px；特别覆盖当前 Thread 空闲但其他 Thread 工作、归档/加载/断线、Thread Explorer/Runtime 页面、窄屏与长 alias。父行定位同时覆盖减少动态效果。
6. 实现阶段遵循 [juex-localtest](../../.agents/skills/juex-localtest/SKILL.zh.md)，按变更范围完成自动化与重建产物的浏览器/API 验证。
7. 实现时同步更新 DOMAIN、DESIGN 和配置说明的中英文契约，运行 docs-check。

## 范围之外

不增加每层数量、总 Worker 数、并发数或 token 预算限制；不清理 debaga 已有 Thread；不做树状 UI；本提案不部署或重启本机 Agent。

## 后续实施

本文件是后续 Agent 的设计输入；提案合并不代表功能已实现，也不自动启动实施任务。实施前重新核对当前代码、相关领域契约和 Taskline 状态。交付后更新长期契约并收敛本提案的状态，避免把计划当成已交付行为。
