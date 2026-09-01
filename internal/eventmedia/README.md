# Event Media

> English | [中文](README.zh.md)

`eventmedia` validates file references declared by external event envelopes.
Observable JSONL records, schedule observations, and MCP notifications share
this boundary.

Accepted relative source paths must resolve to regular files inside the active
Workspace; absolute sources may resolve inside that Workspace or the current
AgentStateDir, subject to `blocked_paths`.
Each file is read with a bounded limit, checked against its declared media type,
and copied into `event-media/` beneath the Agent Artifact root using
content-addressed storage.
The returned `ArtifactPath`, SHA-256, byte count, and image dimensions are safe
to persist in provider-visible `llm.MediaRef` values after the source file is
removed.

Validation is per attachment plus a total event-size gate. Callers must render
`ValidationReport.Errors` visibly; Observable ingress also records them and
emits `observation.errored` while preserving the event text. Command
Observables apply the total gate to the complete batch, not each parsed line,
so one Observation cannot exceed the event attachment budget by aggregation.
