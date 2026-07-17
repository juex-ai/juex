# Fleet Service Registration

This package registers the resident fleet supervisor with the current user's
native service manager. It does not manage individual agents.

- `New` validates a stable fleet address and builds a platform-specific plan.
- `Install` publishes the definition before enabling and starting it.
- `Uninstall` queries native manager state, confirms the supervisor is stopped,
  and then removes the definition.
- launchd definitions use `AbandonProcessGroup`; systemd user units use
  `KillMode=process`; Termux services require an explicit confirmed `down`.

Service identities include a normalized `JUEX_HOME` slug and hash so multiple
homes can be registered independently. Definition publication is atomic per
file and transactional across the two-file Termux definition. CLI flags and
output remain in `internal/cli`; agent reconciliation and detached child
lifecycle remain in `internal/fleet`.
