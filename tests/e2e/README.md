# JueX E2E Coverage

> English | [中文](README.zh.md)

This directory proves behavior that crosses package, process, protocol, or
storage boundaries. Local edge cases belong in package unit tests.

The suite covers Management authority and tenant isolation, durable Runtime
execution and recovery, Execution environments and files, and Memory/Calendar
application lifecycles. Cases cross public HTTP, authenticated RPC, CLI, native
executor and storage boundaries. Test files are the authoritative case inventory.

PostgreSQL cases use the `postgres` build tag and create and drop isolated test
databases. Live Provider cases additionally use the `integration` tag and an
explicit private model fixture. Untagged tests do not run those cases;
deterministic Provider fixtures prove contracts, not live model behavior.

Never commit credentials or generated live reports. The
[evaluation guide](../eval/README.md) describes model fixtures and live evidence.

Use the repository-local
[JueX local-test skill](../../.agents/skills/juex-localtest/SKILL.md) to choose
and run the correct verification tier.
