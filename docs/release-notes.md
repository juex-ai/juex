# Release Notes

## Unreleased

### Compatibility

- `juex serve` no longer opens `http://127.0.0.1:8080` by default. It
  publishes only the canonical local agent endpoint unless `--addr` is passed.
  Scripts that call the agent JSON/SSE API over TCP must now pass an explicit
  address, for example `juex serve --addr 127.0.0.1:8080`.
