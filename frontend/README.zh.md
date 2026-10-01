# JueX 前端

> [English](README.md) | 中文

React/TypeScript/Vite 实现 Management Dashboard。身份、授权、对话和应用事实均由服务端持有。

```sh
mise exec -- make web
mise exec -- pnpm --dir frontend dev
```

在 8680 端口运行完成配置的开发 Management 服务。Vite 绑定 `0.0.0.0:5173`，
将 `/api` 代理到该服务。生产资源从 `frontend/dist` 复制到
`internal/entrypoints/webassets/dist`，不要直接编辑内嵌产物。
部署使用 [平台运维工具](../deploy/managed/README.zh.md)。

`src/management` 拥有 Dashboard、生成契约、API 客户端及视图；
`src/components/ui` 保存共享基础组件，`src/components/ai-elements/message`
提供对话渲染。共享设计变量位于 `src/index.css`。
修改公共类型时执行 `go run ./scripts/gen-management-schema` 更新契约；
服务端测试检查 schema 一致性。

`make web-check` 覆盖类型、单测、lint、生产构建和浏览器交互。
必须有 Chrome，非默认路径通过 `CHROME_PATH` 指定。
可见行为还需使用重新构建的运行服务进行浏览器检查。
详见 [DESIGN.zh.md](../DESIGN.zh.md) 和 [验收 Skill](../.agents/skills/juex-localtest/SKILL.zh.md)。
