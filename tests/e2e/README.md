# Juex E2E Coverage

> English | [中文](README.zh.md)

This directory proves behavior that crosses package, process, protocol, or
storage boundaries. Local edge cases belong in package unit tests.

The suite covers complete Agent/Thread execution, restart and recovery,
Provider/Tool protocol validity, Context Generation transitions, Worker and
Observation routing, CLI/Web/Fleet composition, storage, and platform
integration. The test files are the authoritative case inventory.

Build-tagged live tests read explicitly selected local Provider configuration.
Never commit credentials or generated live reports.

Use the repository-local
[Juex local-test skill](../../.agents/skills/juex-localtest/SKILL.md) to choose
and run the correct verification tier.
