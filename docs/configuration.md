# 运行配置

运行行为由环境变量和 Web 控制台中的 Profiles 共同决定。环境变量只负责进程监听
和数据目录；协议、上游、视觉与重试设置见 [Profiles](profiles.md)。

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

## 数据目录

llm-proxy 在 `DATA_DIR` 中创建 `llm-proxy.db`，保存管理员、Session、
Profiles、默认项和 Token 用量。启动时目录权限会收紧为 `0700`，数据库文件为
`0600`。生产环境应使用持久化、仅服务账号可读写的目录，并按
[管理控制台](admin.md)中的停机流程备份。
