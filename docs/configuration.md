# 配置参考

anthropic-proxy 的运行行为由 YAML 配置文件决定。环境变量只负责指定配置文件和 Docker 端口映射，不覆盖 Provider 字段。

## 环境变量

Docker Compose 从 `.env` 读取：

| 变量 | 默认值 | 说明 |
|---|---|---|
| `HOST` | `127.0.0.1` | 宿主机监听地址；`0.0.0.0` 表示允许外部访问 |
| `PORT` | 无 | 宿主机监听端口，示例使用 `8087` |
| `CONFIG_FILE` | `./config.yaml` | 挂载到容器中的宿主机配置文件 |

容器内始终通过 `CONFIG_FILE=/app/config.yaml` 加载挂载后的文件。本地直接运行时，也可以使用环境变量或命令行参数：

```bash
CONFIG_FILE=./config.yaml ./bin/anthropic-proxy
./bin/anthropic-proxy -config ./config.yaml
```

没有 `PROVIDER`、`UPSTREAM_URL`、`STATS_DB` 或 `STATS_PASSWORD` 环境变量覆盖逻辑。切换 Provider 或统计配置时应修改 YAML 文件。

## 顶层配置

```yaml
listen: :8080
active: jdcloud
stats_db: ./data/stats.db
stats_password: statspwd123456
```

| 字段 | 默认值 | 说明 |
|---|---:|---|
| `listen` | `:8080` | 进程监听地址 |
| `active` | 无 | 当前 Provider 名称，必须存在于 `providers` |
| `stats_db` | 空 | SQLite 路径；留空时禁用统计接口 |
| `stats_password` | `statspwd123456` | `/stats` 和 `/stats/data` 的密码 |

统计用户名固定为 `admin`。

## Provider

```yaml
providers:
  my-anthropic:
    upstream: https://your-anthropic-compatible-endpoint.com
    protocol: anthropic
    vision:
      enabled: true
      model: Kimi-K2.5
    overload_rules:
      - status: 529
        body_contains: overloaded
        max_retries: 10
        delay: 2s
        jitter: 1s
```

| 字段 | 默认值 | 说明 |
|---|---:|---|
| `upstream` | 无 | 上游基础地址，必须配置；代理会在其后追加原请求 URI |
| `protocol` | `anthropic` | Token 响应解析协议：`anthropic` 或 `openai` |
| `vision` | 关闭 | 图片预处理配置，详见[图片预处理](vision.md) |
| `overload_rules` | 空 | 过载匹配与重试规则 |

`upstream` 不应重复包含客户端请求路径。例如客户端请求为 `/v1/messages` 时，通常配置 `https://api.example.com`，不要配置成 `https://api.example.com/v1`。

图片预处理只支持 `anthropic` 协议。为 OpenAI Provider 启用 `vision` 会导致启动失败。

## 过载规则

规则按顺序匹配 HTTP 状态码和可选的响应正文：

```yaml
overload_rules:
  - status: 429
    max_retries: 5
    delay: 5s
    jitter: 2s
  - status: 503
    body_contains: overloaded
```

| 字段 | 默认值 | 说明 |
|---|---:|---|
| `status` | 无 | 要匹配的 HTTP 状态码 |
| `body_contains` | 空 | 可选的响应正文子串 |
| `max_retries` | `10` | 最大重试次数 |
| `delay` | `2s` | 基础等待时间 |
| `jitter` | `1s` | 每次尝试增加的等待时间 |

第 N 次重试等待时间为 `delay + N × jitter`。

`overload_rules` 可以省略或留空。此时普通上游错误原样返回，网络错误直接返回 `502`，不会启动重试。

## 完整示例

```yaml
listen: :8080
active: jdcloud
stats_db: ./data/stats.db
stats_password: change-this-password

providers:
  jdcloud:
    upstream: https://modelservice.jdcloud.com/coding/anthropic
    protocol: anthropic
    vision:
      enabled: true
      model: Kimi-K2.5
    overload_rules:
      - status: 400
        body_contains: overloaded
        max_retries: 10
        delay: 2s
        jitter: 1s

  openai-compatible:
    upstream: https://api.example.com
    protocol: openai
    overload_rules:
      - status: 429
        max_retries: 5
        delay: 5s
        jitter: 2s
```

修改配置后需要重启进程或容器，当前不支持热加载。
