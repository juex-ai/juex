# User Media

`usermedia` owns policy for image input attached to a Juex session.

It validates upload size and image type, stores content-addressed bytes through
`internal/artifact`, records integrity and dimension metadata, and verifies
that submitted references belong to the target session before turn admission.
The default contract accepts up to eight images per turn and 10 MiB per image.

HTTP multipart parsing belongs to `internal/web`; canonical image blocks and
provider encoding belong to `internal/app` and `internal/llm`. This package
does not own transport behavior or duplicate artifact filesystem safety.
