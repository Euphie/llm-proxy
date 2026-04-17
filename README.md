# anthropic-proxy

支持 **Anthropic 协议**与 **OpenAI 协议**的轻量级反向代理。在上游过载时自动等待并重试，确保 Claude Code 等客户端不因接口报错而中断任务；同时异步统计 token 用量并提供可视化看板。

适用场景：京东云、百度、自建中转等各类 Anthropic / OpenAI 兼容 API。

## 工作原理

```
Claude Code  ──►  localhost:8087  ──►  上游 Anthropic / OpenAI 兼容 API
               (anthropic-proxy)
                      │
                      │ 收到过载错误（可配置）
                      │ 自动等待 + 重试（线性退避）
                      └──────────────────────────►  重新转发
```

重试间隔：第 N 次等待 `delay + N × jitter`，例如默认配置下为 2s、3s、4s……

## 快速开始

### 使用 Docker（推荐）

**1. 检查 `.env`**

```bash
PROVIDER=jdcloud   # 选择 provider
PORT=8087          # 宿主机监听端口
```

**2. 启动**

```bash
docker compose up -d
```

代理监听在 `127.0.0.1:8087`，在 Claude Code 中配置 `ANTHROPIC_BASE_URL=http://127.0.0.1:8087` 即可。

---

### 本地运行（不使用 Docker）

需要先安装 [Go 1.25+](https://go.dev/dl/)。

```bash
make build
PROVIDER=jdcloud CONFIG_FILE=config.yaml ./bin/anthropic-proxy
```

---

## 内置 Provider

`config.yaml` 已预置以下 provider，通过 `.env` 的 `PROVIDER` 字段选择。

| Provider | 协议 | 上游 URL | 过载触发条件 |
|---|---|---|---|
| `jdcloud` | Anthropic | `https://modelservice.jdcloud.com/coding/anthropic` | `400` + body 含 `overloaded` 或 `Too many requests` |

---

## 自定义 Provider

在 `config.yaml` 的 `providers` 下新增条目，修改 `.env` 中的 `PROVIDER` 后重启即可。

**Anthropic 兼容接口示例：**

```yaml
providers:
  my-anthropic:
    upstream: https://your-anthropic-endpoint.com
    protocol: anthropic   # 默认值，可省略
    overload_rules:
      - status: 529
        max_retries: 10
        delay: 2s
        jitter: 1s
```

**OpenAI 兼容接口示例：**

```yaml
providers:
  my-openai:
    upstream: https://api.openai.com
    protocol: openai
    overload_rules:
      - status: 429
        max_retries: 8
        delay: 5s
        jitter: 2s
      - status: 503
        max_retries: 5
        delay: 3s
        jitter: 1s
```

> **注意**：使用 `protocol: openai` 时，流式请求的 token 统计需要客户端在请求 body 中携带 `"stream_options": {"include_usage": true}`，否则流式响应不返回用量数据（这是 OpenAI 接口的行为）。非流式请求无此限制。

---

## Token 用量统计

代理会异步捕获每次请求的响应，解析 Anthropic / OpenAI 协议中的 token 用量，写入本地 SQLite 数据库。

| 地址 | 说明 |
|---|---|
| `http://127.0.0.1:<PORT>/stats` | 可视化看板（手绘风格 Dashboard） |
| `http://127.0.0.1:<PORT>/stats/data` | JSON 数据接口 |

看板包含：总请求数、输入/输出/合计 token 数、近 30 天每日用量图、按模型分布表。

统计数据库默认保存在 `./data/stats.db`，Docker 模式下通过 volume 持久化。

---

## 配置说明

### 环境变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `PROVIDER` | `jdcloud` | 选择 provider，覆盖 `config.yaml` 中的 `active` 字段 |
| `PORT` | `8087` | 宿主机监听端口 |
| `UPSTREAM_URL` | — | 覆盖当前 provider 的上游 URL |
| `STATS_DB` | — | 覆盖统计数据库路径，留空则使用 `config.yaml` 中的 `stats_db` |

### config.yaml 完整结构

```yaml
listen: :8080          # 容器内监听地址，无需修改
active: jdcloud        # 默认 provider，可被 PROVIDER 覆盖
stats_db: ./data/stats.db  # SQLite 路径，留空禁用统计

providers:
  jdcloud:
    upstream: https://modelservice.jdcloud.com/coding/anthropic
    # protocol 省略则默认 anthropic
    overload_rules:
      - status: 400
        body_contains: "overloaded"
        max_retries: 10   # 省略则使用默认值 10
        delay: 2s         # 省略则使用默认值 2s
        jitter: 1s        # 省略则使用默认值 1s
      - status: 400
        body_contains: "Too many requests"
        max_retries: 10
        delay: 2s
        jitter: 1s
```

每条 `overload_rules` 独立配置重试策略；`max_retries`、`delay`、`jitter` 均可省略，使用内置默认值。

### 支持的 protocol

| 值 | 适用场景 |
|---|---|
| `anthropic`（默认）| 京东云、官方 Anthropic 及所有 Anthropic 兼容接口 |
| `openai` | OpenAI Chat Completions 格式的接口（流式统计需请求带 `stream_options.include_usage: true`） |
