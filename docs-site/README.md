# Mesotes 智能路由设计站

这是 Mesotes 的 Profile-local 智能路由工程规范。Mesotes 是产品品牌；`llm-proxy` 仍是当前仓库与运行时标识。文档把固定源码事实、目标契约和后续方向分开标注，不表示目标能力已经在 Go 运行时中实现。

## 真实路由

站点包含 12 个可独立刷新、分享和生成 metadata 的页面：

- `/docs/overview`
- `/docs/source-baseline`
- `/docs/competitor-evidence`
- `/docs/core-model`
- `/docs/profile-isolation`
- `/docs/online-routing`
- `/docs/quality-and-cost`
- `/docs/target-reliability`
- `/docs/runtime-reliability`
- `/docs/evaluation-feedback`
- `/docs/strategy-lifecycle`
- `/docs/engineering-and-delivery`

标题深链使用 `/docs/<slug>#<heading>`。旧版 `/#...` 单页 hash 地址不会保留兼容映射，这是有意的破坏性迁移；外部书签需要更新到真实页面 URL。

首屏只序列化当前章节与导航摘要；全文索引在首次输入搜索词时从带内容版本的 `/api/search-index?v=<version>` 按需加载。保存的明暗主题会在 hydration 前应用，避免暗色用户看到亮色首帧。

## 证据与验收边界

运行时事实固定在 `ea13e527647cb701376c152f71086f01e68789ea`，采集时间、漂移状态和永久链接集中维护在 `data/source-baselines.json`。竞品证据集中维护在 `data/competitor-evidence.json`：开源实现固定 commit，官方文档与闭源参考使用独立证据等级，不能冒充源码验证。

- `DOC_CONTRACT` 证明页面、证据、示例与内容测试完整表达设计契约。
- `RUNTIME_PROVEN` 需要后续 Go 运行时的隔离、故障注入、并发和安全套件；本站不声称已经达到该门槛。

## 隔离预览与验证

从仓库根目录运行；显式源码清单通过标准输入送入临时容器，依赖和构建产物只留在容器内：

```sh
make docs-verify
make docs-e2e
```

`docs-verify` 使用 Node 22 执行内容契约、内容模型、lint、TypeScript、生产构建、12 个 SSR 页面、搜索 API 和渲染检查。`docs-e2e` 使用固定的 Playwright `1.62.0` Chromium 容器测试导航、深链、历史、按需搜索、首绘主题、移动端焦点与目录、图表灯箱和 reduced motion。

需要交互预览时可使用同样的临时副本：

```sh
make docs-preview
```

三个目标都不会挂载宿主工作区，只把构建所需的显式文件清单通过 tar 输入流送入临时容器；本地依赖、构建产物、测试输出与 `.env*` 对容器不可见。归档会剥离扩展属性并排除 macOS AppleDouble 文件，`pipefail` 保证宿主归档失败时整个目标立即失败。打开 `http://localhost:3000/docs/overview`。公网构建必须设置裸 HTTPS origin，例如 `NEXT_PUBLIC_SITE_URL=https://docs.example.com npm run build`，用于生成可信 canonical 与分享图地址；缺失、HTTP、凭据、path、query 或 fragment 会使生产构建失败。
