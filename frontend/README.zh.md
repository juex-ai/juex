# Juex Frontend

> [English](README.md) | 中文

本 React + TypeScript + Vite 应用是 `juex fleet serve` 提供的 Fleet UI。
Fleet 管理 roster 与进程控制，并代理 selected-Agent JSON/SSE 请求。Server
始终是事实来源。

## 本地开发

在仓库根目录运行：

```bash
make web
go run ./cmd/juex fleet serve
pnpm --dir frontend dev
```

Vite 把 Fleet 与 selected-Agent API 请求代理到本地 Fleet server。生产输出
从 `frontend/dist/` 复制到 `internal/web/dist/`，不要直接编辑 embedded output。

前端验证门禁会针对生产构建运行浏览器交互测试，因此需要本地 Chrome 可执行文件。
如果 Chrome 不在平台标准位置，请设置 `CHROME_PATH`。

## 所有权

- `src/pages/` 负责 route 级 Fleet、Thread 和 Runtime view。
- `src/components/` 负责可复用展示与交互。
- `src/lib/` 负责 client read model 与 stream projection。
- `src/api.ts` 是类型化 Fleet/Agent transport 边界。
- `src/index.css` 负责生产 design token。

稳定交互与视觉规则见 [DESIGN.zh.md](../DESIGN.zh.md)。具体 component name 和
request shape 以代码与测试为准。验证流程使用仓库内
[Juex local-test skill](../.agents/skills/juex-localtest/SKILL.zh.md)。
