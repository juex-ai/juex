# JueX Frontend

> English | [中文](README.zh.md)

React/TypeScript/Vite implements the Management dashboard. The server owns
identity, authorization, conversation and application truth.

```sh
mise exec -- make web
mise exec -- pnpm --dir frontend dev
```

Run a configured development Management service on port 8680. Vite binds
`0.0.0.0:5173` and proxies `/api` to that service. Production assets are copied
from `frontend/dist` to `internal/entrypoints/webassets/dist`; do not edit the
embedded output. Deployment uses the [platform operator](../deploy/managed/README.md).

`src/management` owns the dashboard, generated contracts, API clients and views.
`src/components/ui` owns shared primitives; `src/components/ai-elements/message`
provides conversation rendering. Shared tokens live in `src/index.css`.
Regenerate Management contracts with `go run ./scripts/gen-management-schema`
when changing public types; schema parity is checked by server tests.

`make web-check` covers types, unit tests, lint, production build and browser
interactions. Chrome must be available; set `CHROME_PATH` for a nonstandard path.
Visible behavior additionally needs a rebuilt live service/browser check.
See [DESIGN.md](../DESIGN.md) and the [verification skill](../.agents/skills/juex-localtest/SKILL.md).
