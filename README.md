# llm-proxy

面向 Agent 和 LLM SDK 的轻量级多 Profile 代理。

通过 Web 控制台管理上游协议、视觉增强和容错规则；客户端使用简单的
Profile URL 选择运行配置，鉴权信息始终由请求透传，llm-proxy 不管理调用方密钥。

## 核心能力

- 多 Profile：按 URL 选择独立的 Anthropic 或 OpenAI 兼容上游。
- 原子热更新：控制台保存后立即发布新的运行时快照，无需重启。
- 模型能力：每个 Profile 保存精确模型 ID、上下文/可选输出上限和视觉支持事实；UI
  推荐目录不参与运行时路由。
- 视觉增强：为 Anthropic Messages 或 OpenAI Responses 中不支持图片的主模型
  补充图片描述。
- 弹性代理：流式转发，并按有序规则处理过载重试。
- 用量统计：记录 Anthropic、OpenAI Chat Completions 和 Responses 主请求，并
  单独记录图片识别影子请求 Token。
- 本地配置生成：浏览器生成 Claude Code、OpenCode 和 Codex CLI 配置，不接收或
  保存真实密钥。

## 快速开始

准备 `.env`：

```sh
cp .env.example .env
```

启动：

```sh
docker compose up -d --build
```

打开 `http://<host>:<port>/_admin/`，使用 `admin/admin` 首次登录，并按页面提示
设置新密码。随后创建 Profile、填写 Upstream，并从 Profile 列表生成 Agent 配置。
完整操作见[管理控制台](docs/admin.md)。

## 架构

![llm-proxy Profile 架构](docs/assets/llm-proxy-architecture.svg)

## 文档

| 文档 | 内容 |
|---|---|
| [运行配置](docs/configuration.md) | 进程与 Docker Compose 环境变量 |
| [Profiles](docs/profiles.md) | 路由、协议、上游、热更新、视觉和重试 |
| [模型能力与 Agent 上下文](docs/models.md) | Profile 模型事实、UI 推荐与 Claude Code/Codex/OpenCode 映射 |
| [管理控制台](docs/admin.md) | 登录、Profile 操作、配置生成、统计和系统管理 |
| [图片预处理](docs/vision.md) | 图片来源、缓存、并发、失败与日志 |
| [Token 用量统计](docs/statistics.md) | 控制台筛选、统计口径和协议限制 |
| [Mesotes 智能路由设计站](docs-site/README.md) | 固定证据支持的 Profile-local 目标设计；不代表 Go 运行时已实现 |
