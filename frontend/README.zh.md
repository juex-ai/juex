# Juex 前端

> [English](README.md) | 中文

本目录是由 `juex fleet serve` 提供的 React + Vite Fleet UI。Fleet 负责
Agent 列表 API，并把选中 Agent 的 JSON/SSE 请求代理到 resident Agent
Runtime。Resident Agent 只暴露 API，不提供 SPA。

## 技术栈

- React、TypeScript、Vite、React Router
- Tailwind CSS v4 与 shadcn/ui primitives
- AI Elements 与 streamdown，用于 transcript 渲染
- Shiki 与 lucide-react，用于代码和图标

## 开发

在仓库根目录准备嵌入 bundle，并启动 Fleet：

```bash
make web
go run ./cmd/juex fleet serve
```

在另一个 shell 中启动 Vite：

```bash
pnpm --dir frontend dev
```

Vite 把 Fleet `/api` 请求和选中 Agent 的 `/agents/:agentId/api` 请求代理到
默认 Fleet Server `127.0.0.1:5839`。

使用仓库验证入口，不要手工拼接重复检查：

```bash
make verify-focused PKGS="./internal/web ./internal/fleetweb"
make verify-candidate WEB=1
```

## Thread UI 所有权

- `src/pages/ThreadExplorer.tsx` 从 Agent index 展示活跃和归档 Thread。
- `src/pages/Thread.tsx` 读取一个 Thread、从 journal 末端向前分页、从 event
  cursor 订阅实时变化，并发送 Input。
- `src/lib/thread-read-state.ts` 与 `thread-read-controller.ts` 负责纯 read
  model 和 transport 协调。
- `src/lib/live-thread-projection.ts` 在持久 timeline 刷新前投影乐观 Input
  与实时 journal-backed event。
- `src/components/thread/` 渲染 composer、transcript 与状态。
- `src/api.ts` 是类型化 Fleet/Agent API 边界。

`/new` 和 `/compact` 都停留在当前 Thread。二者都会创建 Context
Generation；只有 `/compact` 保留 summary。归档 Thread 可以读取，但不能接收新
Input。

`make web` 会把 `frontend/dist/` 复制到 `internal/web/dist/`；不要直接编辑嵌入文件。
