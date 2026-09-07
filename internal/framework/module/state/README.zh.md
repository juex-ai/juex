# Module 资源所有权

[English](README.md) | 中文

Thread factory 通过 `OwnsResources` 声明其实现可拥有持久资源；对应的当前状态 store 在首次写入时声明 Thread owner 与保留策略。`Prepare`
先发布所有权，再创建 owner 私有目录。状态文件、原子写残留与 Generation 清理
暂存备份都位于该目录内；不接管未登记的目录。

组合根持有生命周期 lease，直到所有 App 与延迟 writer 停止。每个 Agent 组合
只执行一次 factory 声明与所有权记录的核对，覆盖 inactive 和 archived Thread。
Agent 级意图先于任何退休删除持久化，因此崩溃后重新启用也不能跳过尚未访问的
Thread。资源已不存在视为已清理；失败保留意图并阻止发布。正常 Close 和只读检查
不触发退休，声明保留的资源不受影响。

这次引入所有权是一次明确的部署切换。升级已有 Agent 前，先停止 Agent，再手动
归档或移除 active 和 archived Thread 根目录中的旧 `goal_state.json`、`notes.md`
及其暂存的 Generation 清理备份。不通过迁移、接管、文件名扫描或历史重放将这些
无所有权文件纳入新生命周期。
