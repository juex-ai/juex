# Hosted base image

> English | [中文](README.zh.md)

Build this operator-owned recipe with Docker, then configure Execution with the
resulting `sha256:` image ID. It includes Git, Python virtual environments,
Node.js 24, a C/C++ toolchain and ripgrep. The guest binary is built from the same
JueX release and mounted read-only by Execution.
Build the trusted operator recipe with a normal Docker builder. The resulting
Agent workloads always use gVisor. Isolated builders can pass
`--build-arg BUILD_DNS=<routable-IP>` with a bridge network.

Python environments belong under `/workspace` or `/home/agent`, for example
`python3 -m venv /home/agent/.venvs/project`. Install Node project dependencies
under the project's Workspace; global npm tools use `/home/agent/.local`.
These paths survive container replacement. System packages belong in an
explicit image recipe; the running environment's root filesystem is read-only.
Record the recipe and resulting image ID with the deployment backup.

Execution requires Linux 5.14 or newer, cgroup v2, Docker's iptables backend,
gVisor `runsc`, and `findmnt`. Provision a dedicated XFS filesystem mounted with
`prjquota` for `backend.workspace_root`, owned by root with mode 0700. Set
`backend.storage_identity` to its filesystem UUID. Control state must reside
on a different filesystem. Execution never formats or mounts disks.

Workspace and Home share each Agent's hard byte and inode quota, defaulting to
2 GiB and 131072 inodes. PostgreSQL persists the pool identity, project ID and
limits. Missing mounts, changed identity or disabled enforcement prevent startup.
Allocate pool capacity separately from PostgreSQL/control state; per-Agent limits
do not reserve total disk space. Back up file contents together with allocation
records and quota metadata; restore those before allowing execution.
See the [Execution contract](../../internal/execution/README.md).
