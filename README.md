# llm-proxy

面向 Agent 和 LLM SDK 的轻量级多代理通道与聚合网关。

通过 Web 控制台管理上游协议、视觉增强、容错规则、供应商模型路由和调用方密钥。
调用方既可以通过代理通道 URL 使用完整代理能力，也可以使用独立网关 Key 直接访问
聚合网关。

## 核心能力

- 多代理通道：按 URL 选择独立的 Anthropic 或 OpenAI 兼容上游。
- 原子热更新：控制台保存后立即发布新的运行时快照，无需重启。
- 模型能力：每个代理通道保存精确模型 ID、上下文/可选输出上限和视觉支持事实；UI
  推荐目录不参与运行时路由。
- 视觉增强：为 Anthropic Messages 或 OpenAI Responses 中不支持图片的主模型
  补充图片描述。
- 弹性代理：流式转发，并按有序规则处理过载重试。
- 聚合网关：集中管理供应商 Secret、模型路由和多个调用方 Key；代理通道也可把已配置
  网关作为内部 Upstream。
- 用量统计：记录 Anthropic、OpenAI Chat Completions 和 Responses 主请求，支持按
  代理通道、聚合网关和调用方密钥筛选，并单独记录图片识别影子请求 Token。
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
设置新密码。首次密码修改完成前，数据面不会激活。

llm-proxy 支持三种独立使用方式：

| 方式 | 必须配置 | 调用入口 | 适用场景 |
|---|---|---|---|
| 只用代理通道 | 代理通道 | `/v1/...` 或 `/<slug>/v1/...` | 代理单个外部上游，并使用视觉增强、重试或 Agent 配置生成 |
| 只用聚合网关 | 供应商、网关、调用方 Key | `/gateways/{slug}/v1/...` | 集中管理供应商 Secret、模型路由和调用方 Key；不需要创建代理通道 |
| 组合使用 | 上述两类配置 | 代理通道入口 | 聚合多个供应商，同时保留代理通道的视觉增强、重试和配置生成 |

只使用聚合网关时，客户端需要手工配置网关 Base URL 和网关 Key。直接网关请求不经过
代理通道，因此不执行视觉增强和有序重试，也不能从后台生成 Agent 配置。

代理通道入口本身不执行 llm-proxy 鉴权。外部 URL 模式透传调用方凭据；引用聚合网关时
会改用已保存的供应商 Secret。将服务暴露到不可信网络前，请先阅读
[安全边界与部署](docs/configuration.md#安全边界与部署)。

完整操作见[管理控制台](docs/admin.md)。

## 架构

![llm-proxy 请求与配置架构](docs/assets/llm-proxy-architecture.svg)

上图展示基础代理通道、可选聚合网关和配置热更新。聚合网关既可以作为代理通道的内部
Upstream，也可以通过 `/gateways/{slug}/v1/...` 直接对外提供模型路由。

## 文档

| 文档 | 内容 |
|---|---|
| [运行配置](docs/configuration.md) | 环境变量、安全边界、持久化与备份 |
| [代理通道](docs/profiles.md) | 路由、协议、上游、热更新、视觉和重试 |
| [聚合网关](docs/aggregate-gateways.md) | 供应商、支持模型、模型路由和调用方 Key 分发 |
| [模型能力与 Agent 上下文](docs/models.md) | 代理通道模型事实、UI 推荐与 Claude Code/Codex/OpenCode 映射 |
| [管理控制台](docs/admin.md) | 登录、代理通道操作、配置生成、用量统计和系统管理 |
| [图片预处理](docs/vision.md) | 图片来源、缓存、并发、失败与日志 |
| [Token 用量统计](docs/statistics.md) | 控制台筛选、统计口径和协议限制 |
