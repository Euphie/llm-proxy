# llm-proxy

面向 Agent 和 LLM SDK 的轻量级多 Profile 代理。

通过 Web 控制台管理上游协议、视觉增强和容错规则；客户端使用简单的
Profile URL 选择运行配置，鉴权信息始终由请求透传，llm-proxy 不管理调用方密钥。

## 核心能力

- 多 Profile：按 URL 选择独立的 Anthropic 或 OpenAI 兼容上游。
- 原子热更新：控制台保存后立即发布新的运行时快照，无需重启。
- 视觉增强：为不支持图片的 Anthropic 主模型补充图片描述。
- 弹性代理：流式转发，并按有序规则处理过载重试。
- 用量统计：在同一 SQLite 数据库中记录主请求和视觉请求 Token。
- 本地配置生成：浏览器生成 Agent 配置，只输出 `<SET_LOCALLY>` 占位符。

## 快速开始

准备 `.env`：

```sh
cp .env.example .env
```

启动：

```sh
docker compose up -d --build
```

打开 `http://<host>:<port>/_admin/`。首次登录账号和密码均为 `admin`；
这是可被公网抢占的高风险初始凭据，登录后必须立即修改密码，再创建第一个
Profile。

## 架构

![llm-proxy Profile 架构](docs/assets/llm-proxy-architecture.svg)

[可编辑的 Excalidraw 源文件](docs/assets/llm-proxy-architecture.excalidraw)

## 文档

| 文档 | 内容 |
|---|---|
| [运行配置](docs/configuration.md) | 进程与 Docker Compose 环境变量 |
| [Profiles](docs/profiles.md) | 路由、协议、上游、热更新、视觉和重试 |
| [管理控制台](docs/admin.md) | 首次登录、Session、备份恢复和安全 |
| [图片预处理](docs/vision.md) | 图片来源、缓存、并发、失败与日志 |
| [Token 用量统计](docs/statistics.md) | 控制台筛选、统计口径和协议限制 |

## 升级提示

**破坏性迁移：**旧版基于文件的运行配置不会加载。替换旧部署前，请先记录旧配置
内容，再在新控制台重建 Profiles；不保留历史配置兼容性。
