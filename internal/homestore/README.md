# Home Store

This package owns the portable filesystem substrate used for durable JueX
state:

- advisory file locks with explicit blocking or try-lock behavior;
- the `$JUEX_HOME/.locks/<scope>/<id>.lock` layout;
- same-directory temporary-file publication with durable Windows replacement;
- parent-chain sync when atomic publication creates new directories; and
- parent-directory sync that tolerates filesystems where directory fsync is
  unsupported.

`agentstate`, `endpoint`, and `fleet` retain their identity and lifecycle
policies. `fleetservice` retains transactional publication of multiple native
service files. Atomic-write errors expose whether replacement occurred so that
transactional callers roll back only paths they own. They delegate only
filesystem mechanics to this package.

Workspace identity and global Git-exclude locks remain in the OS temporary
directory. The supervisor lock remains at `$JUEX_HOME/fleet.lock` for
mixed-version compatibility; both use the same portable lock primitive without
adopting the home lock layout.
