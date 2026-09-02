# Fleet Service Registration

> English | [中文](README.zh.md)

This package installs the resident Fleet supervisor into the current user's
native service manager. It supports launchd, systemd user services, and
termux-services; it does not manage individual Agents.

Definitions run `juex fleet serve`, preserve the selected `JUEX_HOME` identity
and a safe executable search path, and are published through `homestore`.
Install publishes before start; uninstall confirms stop before removal.
Existing definitions are validated before replacement.

CLI presentation belongs to `internal/cli`. Agent reconciliation belongs to
`internal/fleet`.
