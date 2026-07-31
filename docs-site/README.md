# Mesotes 智能路由设计站

Mesotes 是产品品牌；在单独的迁移获得批准前，`llm-proxy` 仍是当前仓库与运行时标识。

这是一份基于 `main` 提交 `ea13e52` 整理的交互式设计文档。页面明确区分当前实现与目标方案，覆盖：

- 现有程序架构与模块边界；
- `model=auto` 在线路由；
- 模型能力、价格、质量与风险约束；
- 视觉执行方案、会话固定、缓存和容错；
- 异步动态优化、策略灰度、热加载与回滚；
- 数据模型、安全边界和分阶段交付。

## 本地预览

要求 Node.js `>=22.13.0`：

```bash
npm ci
npm run dev
```

打开 `http://localhost:3000/`。

公网部署时可设置 `NEXT_PUBLIC_SITE_URL=https://docs.example.com`，用于生成可信的
Open Graph 分享图地址；未配置时，非本机请求不会根据客户端 Host 生成分享图 URL。

## 验证

```bash
npm run lint
npm test
```

`npm test` 会执行生产构建、服务端渲染检查，以及 9 章 Markdown 和 7 张架构图的完整性检查。

## 目录

```text
app/                  页面入口与全局样式
components/docs/      文档导航、Markdown 渲染和交互
content/              9 个 Markdown 章节
lib/chapters.ts       章节元数据、目录与搜索索引
public/diagrams/      Excalidraw 导出的 SVG
tests/                构建产物与内容完整性测试
```

当前目录只包含方案站点，不代表智能路由已经在 Go 主程序中实现。
