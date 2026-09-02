# Agent Endpoint

> English | [中文](README.zh.md)

This package owns addressing and exact identity for one running Agent: listener
binding, endpoint parsing/dialing, `runtime.json` publication, identity probes,
instance-bound shutdown, and maintenance locking.

It consumes the explicit `agentstate.AgentAddress` and never infers Agent
identity from a directory name. Listening requires the Agent state directory
to exist and never recreates it. Process identity that cannot be read is
inconclusive, not proof of ownership.

HTTP routes belong to `internal/web`. Registry and process lifecycle policy
belong to `internal/fleet`.
