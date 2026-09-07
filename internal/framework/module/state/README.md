# Module resource ownership

English | [中文](README.zh.md)

A Thread factory declares `OwnsResources` to identify its implementation as an
available owner. Its current-state store declares a Thread owner and retention policy at
its first write. `Prepare` publishes ownership before creating the private owner
directory. Files, atomic-write leftovers and staged Generation-clear backups
stay inside that directory. An unrecorded directory is not adopted.

The composition root holds a lifecycle lease until all Apps and deferred writers
stop. It reconciles actual factory declarations against ownership records once
per Agent composition, including inactive and archived Threads. An Agent-wide
intent precedes every retirement deletion, so a crash followed by re-enablement
cannot skip unvisited Threads. Missing resources are already clean; failed
cleanup preserves the intent and prevents publication. Ordinary Close and
read-only inspection do not retire resources. Retained resources are untouched.

Ownership introduced here is a clean deployment boundary. Before upgrading an
existing Agent, stop it and manually archive or remove its old Thread-root
`goal_state.json` and `notes.md` files and any staged renewal backups, in both
active and archived Thread locations. No migration, adoption, filename scan or
historical replay brings those unowned files into the new lifecycle.
