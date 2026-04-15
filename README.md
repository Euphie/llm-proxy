# anthropic-proxy

面向国内京东、百度等 Anthropic 兼容的 Coding Plan API 的轻量级反向代理。在上游服务过载时可自动等待并重试，确保 Claude Code 等客户端不会因接口报错而中断任务。

## 工作原理

```
Claude Code  ──►  localhost:8080  ──►  上游 Anthropic 兼容 API
                 (anthropic-proxy)
                       │
                       │ 收到过载错误
                       │ 自动等待 + 重试（线性退避）
                       └──────────────────────────►  重新转发
```

重试间隔：第 N 次等待 `delay + N × jitter`，默认 2s、3s、4s……

## 快速开始

### 内置 Provider

`config.yaml` 已预置以下 provider，直接通过 `.env` 选择即可，无需额外配置。

| Provider | 上游 URL | 过载条件 |
|---|---|---|
| `jdcloud` | `https://modelservice.jdcloud.com/coding/anthropic` | `400` + body 含 `overloaded` 或 `Too many requests` |

**1. 编辑 `.env`**

```bash
PROVIDER=jdcloud
```

**2. 启动**

```bash
# Docker
docker compose up -d

# 或本地运行
make build && PROVIDER=jdcloud ./bin/anthropic-proxy -config config.yaml
```

---

### 自定义 Provider

非内置 provider 需要先在 `config.yaml` 中定义，再启动。

**1. 在 `config.yaml` 的 `providers` 下新增条目**

```yaml
providers:
  # ... 已有条目 ...

  my-provider:
    upstream: https://your-endpoint.com/v1
    overload_rules:
      - status: 429
        body_contains: "overloaded"
        max_retries: 5
        delay: 5s
        jitter: 2s
```

**2. 修改 `.env` 指向新 provider**

```bash
PROVIDER=my-provider
```

**3. 启动**

```bash
docker compose up -d
```

---

## 配置说明

### 环境变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `PROVIDER` | `jdcloud` | 选择 provider，覆盖 `config.yaml` 中的 `active` 字段 |
| `PORT` | `8087` | 宿主机监听端口 |

### config.yaml 结构

每条 `overload_rules` 独立配置重试策略，未填字段使用默认值（`max_retries: 10`，`delay: 2s`，`jitter: 1s`）。

```yaml
listen: :8080
active: jdcloud   # 可被 PROVIDER 环境变量覆盖

providers:
  jdcloud:
    upstream: https://modelservice.jdcloud.com/coding/anthropic
    overload_rules:
      - status: 400
        body_contains: "overloaded"
        max_retries: 10
        delay: 2s
        jitter: 1s
      - status: 400
        body_contains: "Too many requests"
        max_retries: 10
        delay: 2s
        jitter: 1s
```