# 运行配置

运行行为由环境变量和 Web 控制台共同决定。环境变量只负责进程监听和数据目录；协议、
上游、视觉与重试设置见[代理通道](profiles.md)，供应商、网关和调用方 Key 设置见
[聚合网关](aggregate-gateways.md)。

## 变量

| 变量 | 所有者 | 默认值 | 作用 |
|---|---|---|---|
| `LISTEN` | llm-proxy 进程 | `:8080` | 容器或本地进程的监听地址 |
| `DATA_DIR` | llm-proxy 进程 | `./data` | SQLite 数据目录 |
| `HOST` | Docker Compose | `127.0.0.1` | 发布端口绑定的宿主机地址 |
| `PORT` | Docker Compose | `8087` | 映射到容器 `8080` 的宿主机端口 |

Compose 将 `${DATA_DIR}` 绑定到容器 `/app/data`，并在容器中设置
`DATA_DIR=/app/data`。`HOST` 和 `PORT` 只参与宿主机端口映射，不会传给
llm-proxy 进程。

示例 `.env`：

```dotenv
HOST=127.0.0.1
PORT=8087
DATA_DIR=./data
```

启动：

```sh
docker compose up -d --build
```

## 安全边界与部署

llm-proxy 在同一个监听端口提供管理后台、代理通道和聚合网关，但三类入口的鉴权行为不同：

| 入口 | llm-proxy 鉴权 | 转发给供应商的凭据 |
|---|---|---|
| `/_admin/...` | 管理员 Session 与 CSRF | 不适用 |
| `/gateways/{slug}/...` | 必须使用该网关签发、已启用且未过期的调用方 Key | 删除调用方认证，注入路由供应商 Secret |
| `/v1/...`、`/<slug>/v1/...`，外部 URL 模式 | 不鉴权 | 透传调用方 `Authorization`、`X-Api-Key` 等请求头 |
| `/v1/...`、`/<slug>/v1/...`，内部聚合网关模式 | 不鉴权 | 删除调用方认证，注入路由供应商 Secret |

内部聚合网关模式会使用数据库中保存的供应商 Secret，但代理通道入口本身没有访问控制。
如果该入口可被不可信调用方访问，对方无需网关 Key 也可能消耗供应商额度。生产部署应：

- 保持默认 `HOST=127.0.0.1`，或在反向代理、VPN、mTLS、IP 白名单等访问控制之后再发布；
- 使用 HTTPS；反向代理终止 TLS 时传递 `X-Forwarded-Proto: https`，让后台 Cookie 带上
  `Secure`；
- 对 `/_admin/` 施加额外的网络访问限制，不要只依赖管理员密码；
- 保护 `DATA_DIR` 和数据库备份，因为供应商 Secret 以明文保存在 SQLite 中；
- 不在日志、截图或工单中记录首次显示的调用方 Key。

llm-proxy 不内置 TLS 终止或代理通道访问控制，这些能力应由部署层提供。

## 数据目录

llm-proxy 在 `DATA_DIR` 中创建 `llm-proxy.db`，保存管理员、Session、
代理通道、默认项、供应商、供应商 Secret、聚合网关、模型路由、调用方 Key 摘要和
Token 用量。供应商 Secret 以明文保存；调用方 Key 只保存 SHA-256 摘要、前缀和尾号，
创建后无法恢复明文。

默认 Compose 把宿主机 `${DATA_DIR:-./data}` 绑定到容器 `/app/data`。只要该目录或替代的
Docker named volume 仍然挂载，重启或重建容器不会丢失数据；未挂载 `/app/data` 时，
删除容器也会删除容器内数据库。启动时目录权限会收紧为 `0700`，数据库文件为 `0600`。
生产环境应使用持久化、仅服务账号可读写的目录。

备份 SQLite 前先停止服务写入。默认数据目录示例：

```sh
docker compose stop llm-proxy
cp ./data/llm-proxy.db ./data/llm-proxy.db.bak
docker compose start llm-proxy
```

自定义 `DATA_DIR` 时替换示例中的 `./data`。恢复时同样先停止服务，并同时保护备份文件，
因为其中包含管理员密码摘要和供应商 Secret。

“系统设置”导出的代理通道 JSON 不包含供应商、聚合网关、调用方 Key、用量或供应商
Secret，不是完整备份；完整恢复应使用 SQLite 备份。
