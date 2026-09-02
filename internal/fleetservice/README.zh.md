# Fleet Service Registration

> [English](README.md) | 中文

本 package 把常驻 Fleet supervisor 安装到当前用户的原生 service manager，
支持 launchd、systemd user service 与 termux-services，不管理单个 Agent。

Definition 运行 `juex fleet serve`，保留选定的 `JUEX_HOME` identity 和安全的
executable search path，并通过 `homestore` 发布。Install 先发布再启动；
uninstall 先确认停止再删除；替换前会校验已有 definition。

CLI 展示属于 `internal/cli`；Agent reconciliation 属于 `internal/fleet`。
