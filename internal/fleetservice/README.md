# Fleet Service Registration

This package registers the resident fleet supervisor with the current user's
native service manager. It does not manage individual agents.

- `New` builds a platform-specific plan whose command is `juex fleet serve`;
  the definition never bakes in `fleet.addr`.
- `ExistingServeOptions` reads the current launchd, systemd, or Termux
  definition so the CLI can migrate legacy baked-in serve options safely.
- `Install` publishes the definition before enabling and starting it.
- `Installed` checks a valid definition first, then strict native-manager state
  where the platform can retain a loaded service without that file.
- `Uninstall` queries native manager state, confirms the supervisor is stopped,
  and then removes the definition.
- launchd definitions use `AbandonProcessGroup`; systemd user units use
  `KillMode=process`; Termux services require an explicit confirmed `down`.
- Definitions persist an explicit executable search path for resident
  agents and their child processes. The JueX executable directory and
  `~/.local/bin` come first, safe absolute entries from the installer's `PATH`
  retain their order, relative entries are discarded, and platform defaults
  are appended.

Service identities include a normalized `JUEX_HOME` slug and hash so multiple
homes can be registered independently. Definition publication is atomic per
file and transactional across the multi-file Termux definition. Termux writes
the `down` sentinel before exposing `log/run` and `run`, then enables and
restarts the service so reinstallations adopt the updated command. CLI flags
and output, stable address validation, and home-config persistence remain in
`internal/cli` and `internal/config`; agent reconciliation and detached child
lifecycle remain in `internal/fleet`.
