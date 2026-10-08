# Managed platform evaluation

> English | [中文](README.zh.md)

The Python module `tests.eval.juex_eval` plans commit-bound candidate/final gates.
The [local-test skill](../../.agents/skills/juex-localtest/SKILL.md) is authoritative.
Records contain the commit, clean-tree status, commands, environment fingerprints,
all service/client artifact identities and redacted live evidence. Reuse requires
matching candidate identity; failed or missing evidence cannot become success.

Live tests use the actual Runtime, isolated PostgreSQL databases, authenticated
public/RPC APIs and a native executor. Set `JUEX_TEST_POSTGRES_URL` to a disposable
test instance whose role can create databases, and `JUEX_PROVIDER_CONFIG` to a
private JSON/YAML file with this shape:

```json
{"models":[{"provider":"example","name":"MODEL","protocol":"openai/chat","endpoint":"https://provider.example/v1","api_key":"TEST_SECRET","context_window":131072,"max_output":8192}]}
```

For provider-default ordinary requests, set `max_output` to zero and supply a
positive `output_reserve`. A positive cap may omit the reserve to use the same
value. Anthropic zero-cap fixtures require at least 4096 reserved tokens.

No personal runtime configuration is discovered automatically. Keep credentials
outside Git with mode 0600. Reports redact selected API keys; temporary selected
model files are deleted after every outcome. Selection is seeded and reproducible;
`--only provider:model` and `--all-models` make the scope explicit.

```sh
mise exec -- uv run python -m tests.eval.juex_eval integration
mise exec -- bash tests/eval/provider_model_smoke.sh --only example:MODEL
mise exec -- bash tests/eval/compaction_eval.sh --only example:MODEL
```

Integration verifies a real assistant response through the public conversation
API. Provider smoke requires read/write/edit/grep and one real PTY/stdin workflow,
with completed operation evidence. Compaction requires a durable checkpoint,
Runtime restart and retained facts. Go success alone is insufficient: the live
evidence marker must exist. Missing configuration fails explicitly, never skips.
The harness tests under `tests/eval` verify these failure and secret boundaries.
