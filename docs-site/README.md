# Mesotes 智能路由文档站

这是当前 V2 管理后台和运行时的可浏览说明，只记录已经实现的行为。后台内置帮助用于快速操作，
本站补充请求链路、控制面、证据和上线边界。

## 内容

站点包含 10 个独立章节：

- `/docs/overview`：系统概览
- `/docs/admin-workflow`：后台操作流程
- `/docs/profiles-models`：Profile 与模型目录
- `/docs/routing-policy`：Routing Policy
- `/docs/online-routing`：在线请求链路
- `/docs/vision`：视觉预处理
- `/docs/reliability`：重试、超时与 Session
- `/docs/evaluation`：评测与自动校准
- `/docs/observability`：统计与故障排查
- `/docs/operations`：运行边界与上线检查

标题可通过 `/docs/<slug>#<heading>` 直接访问。旧站的目标态、竞品、草稿、灰度和路线图章节已移除；
旧 slug 不提供兼容跳转。

## 验证

从仓库根目录执行：

```sh
make docs-verify
make docs-e2e
```

两个命令都在临时 Docker 容器中安装依赖和运行测试，不会在宿主工作区生成 `node_modules`
或构建产物。`docs-verify` 检查内容契约、代码质量、类型、生产构建和 10 个 SSR 页面；
`docs-e2e` 使用固定版本的 Playwright Chromium 验证导航、搜索、主题、移动端和代码块交互。

交互预览：

```sh
make docs-preview
```

打开 `http://localhost:3000/docs/overview`。公网生产构建必须设置裸 HTTPS 地址，例如
`NEXT_PUBLIC_SITE_URL=https://docs.example.com npm --prefix docs-site run build`，用于生成 canonical
和分享图地址。
