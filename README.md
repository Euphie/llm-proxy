# llm-proxy

面向 Agent 和 LLM SDK 的轻量级多 Profile 代理。

通过 Web 控制台管理上游协议、视觉增强和容错规则；客户端使用简单的
Profile URL 选择运行配置，鉴权信息始终由请求透传，llm-proxy 不管理调用方密钥。

## 核心能力

- 多 Profile：按 URL 选择独立的 Anthropic 或 OpenAI 兼容上游。
- 原子热更新：控制台保存后立即发布新的运行时快照，无需重启。
- 模型能力：每个 Profile 保存精确模型 ID、上下文/输出上限、能力，以及输入、输出和缓存参考价格；支持批量匹配录入和管理员手动更新推荐目录，运行时仍只读取管理员保存的事实。
- 智能路由：客户端发送 `model=auto` 时，以轻量语义分析识别固定任务类型、复杂度信号和风险，在能力、质量、稳定性
  和严重错误门槛达标的候选中按质量、稳定性、成本效率与性能加权选型；Session 只升不降，绑定强模型基线后后续请求
  直接复用最高模型并跳过任务分析，低级模型还可在回答前主动申请升级。
- 单一 Upstream 边界：每个 Profile 只连接一个上游网关；负载均衡、健康检查、熔断和节点故障转移由网关负责。
- 视觉增强：为 Anthropic Messages 或 OpenAI Responses 中不支持图片的主模型
  补充图片描述。
- 弹性代理：流式转发，并按有序规则处理过载重试。
- 用量与路由统计：记录 Anthropic、OpenAI Chat Completions 和 Responses 的 Token，并
  展示最近 Auto 路由、模型切换、主动升级、预算和费用估算。
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
| [智能路由](docs/intelligent-routing.md) | 已上线的 `model=auto`、Profile 隔离、质量/成本选型与后续路线图 |
| [公共评测目录导入](docs/evaluation-catalog-import.md) | 固定版本的 LiveBench、BFCL、VLMEvalKit、Arena 与 SWE-bench 冷启动先验 |
| [Mesotes 智能路由文档站](docs-site/README.md) | 当前 V2 后台操作、在线链路、评测、排障与上线边界 |
