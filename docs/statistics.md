# Token 用量统计

配置非空 `stats_db` 后，代理会异步解析成功响应中的 Token 用量并写入 SQLite。统计写入失败只记录日志，不影响代理响应。

```yaml
stats_db: ./data/stats.db
stats_password: change-this-password
```

`stats_db` 留空时不会打开数据库，也不会注册统计页面和 JSON 接口。

## 统计内容

统计数据包括：

- 请求数；
- 输入、输出和总 Token；
- Anthropic cache read / cache creation Token；
- OpenAI cached prompt Token；
- 最近 30 天每日用量；
- 按模型聚合的用量。

主请求按原请求路径记录。每次真实执行成功的识图影子请求按 `/v1/messages#vision` 单独记录，缓存命中不会新增识图用量。

## 接口

| 地址 | 内容 |
|---|---|
| `/stats` | 可视化 Dashboard |
| `/stats/data` | JSON 聚合数据 |

两个接口都使用 HTTP Basic Auth：

- 用户名固定为 `admin`；
- 密码来自顶层 `stats_password`；
- 密码为空时使用默认值 `statspwd123456`。

默认密码只适合本机测试。对外提供服务时必须修改密码，并放在 HTTPS 反向代理之后。

## 协议差异

Anthropic 的非流式和 SSE 响应都会解析 `usage` 字段。

OpenAI 非流式响应可以直接统计。流式 Chat Completions 请求必须让上游返回 usage：

```json
{
  "stream": true,
  "stream_options": {
    "include_usage": true
  }
}
```

未携带 `stream_options.include_usage: true` 时，上游流式响应通常不包含用量，代理无法补算。

## 数据与运维

Docker Compose 将宿主机 `./data` 挂载到容器 `/app/data`，默认数据库因此可以跨容器重建保留。

统计记录使用 UTC 时间写入。当前没有数据清理或保留周期配置，需要长期运行时应自行备份和清理 SQLite 文件。
