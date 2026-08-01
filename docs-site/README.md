# Mesotes 智能路由设计站

这是 [llm-proxy 智能路由设计](../docs/intelligent-routing.md)的可浏览版本。它解释准备实现的
Profile-local 智能路由，不表示这些能力已经存在于当前 Go 运行时。

## 内容

站点把方案拆成 12 个可独立访问的章节：

- `/docs/overview`：智能路由概览
- `/docs/source-baseline`：当前能力与目标
- `/docs/competitor-evidence`：参考方案与取舍
- `/docs/core-model`：Profile 配置与模型角色
- `/docs/profile-isolation`：Profile 隔离边界
- `/docs/online-routing`：在线路由
- `/docs/quality-and-cost`：质量与成本
- `/docs/target-reliability`：Target 与容错
- `/docs/runtime-reliability`：视觉、重试与 Session
- `/docs/evaluation-feedback`：异步评测与策略优化
- `/docs/strategy-lifecycle`：策略生命周期
- `/docs/engineering-and-delivery`：实施范围与交付

标题可以使用 `/docs/<slug>#<heading>` 直接访问。源码事实和竞品证据固定到明确版本；页面会
区分“当前已实现”“目标设计”和“后续方向”。正式设计以
[`docs/intelligent-routing.md`](../docs/intelligent-routing.md) 为准。

## 验证

从仓库根目录执行：

```sh
make docs-verify
make docs-e2e
```

两个命令都在临时 Docker 容器中安装依赖和运行测试，不会在宿主工作区生成 `node_modules`
或构建产物。`docs-verify` 检查内容契约、代码质量、类型、生产构建和 12 个 SSR 页面；
`docs-e2e` 使用固定版本的 Playwright Chromium 验证导航、搜索、主题、移动端和图表交互。

交互预览：

```sh
make docs-preview
```

打开 `http://localhost:3000/docs/overview`。公网生产构建必须设置裸 HTTPS 地址，例如
`NEXT_PUBLIC_SITE_URL=https://docs.example.com npm --prefix docs-site run build`，用于生成 canonical
和分享图地址。
