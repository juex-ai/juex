# Event Media

`eventmedia` validates file references declared by external event envelopes.
Observable JSONL records, schedule observations, and MCP notifications share
this boundary.

Accepted source paths must resolve to regular files inside the active workdir.
Each file is read with a bounded limit, checked against its declared media type,
and copied into `.juex/artifacts/event-media/` using content-addressed storage.
The returned `ArtifactPath`, SHA-256, byte count, and image dimensions are safe
to persist in provider-visible `llm.MediaRef` values after the source file is
removed.

Validation is per attachment plus a total event-size gate. Callers must render
`ValidationReport.Errors` visibly; Observable ingress also records them and
emits `observation.errored` while preserving the event text.
