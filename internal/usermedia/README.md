# User Media

> English | [中文](README.zh.md)

`usermedia` owns policy for image input attached to a Juex Thread.

It validates upload size and image type, stores content-addressed bytes through
`internal/artifact`, records integrity and dimension metadata, and verifies
that submitted references belong to the target Thread before Turn admission.
The default contract accepts up to eight images per turn and 10 MiB per image.

`PrepareFiles` resolves local paths from the workdir, validates every input,
and retains the bounded bytes without writing. Callers persist the same bytes
under the Agent media root only after all preparation succeeds, without a
second read. `InspectFiles`, `StoreFile`, and `StoreFiles` are convenience APIs
over the same policy. Absolute paths are allowed; relative paths are always
workdir-relative.

HTTP multipart parsing belongs to `internal/web`, CLI interaction belongs to
its transport adapter, and canonical image blocks plus provider
encoding belong to `internal/app` and `internal/llm`. This package does not own
transport behavior or duplicate artifact filesystem safety.
