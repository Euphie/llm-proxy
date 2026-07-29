# 管理控制台

管理控制台位于 `/_admin/`，Profile、统计和系统页面共用同一管理员账号与
Cookie Session。

## 首次登录

首次启动会创建 `admin/admin`。这组公开初始凭据存在抢占风险：在密码修改前，
任何能访问控制台的人都可能先登录。首次初始化应保持 `HOST=127.0.0.1`，或先用
防火墙、可信反向代理和 HTTPS 限制访问。

首次登录后只能查看 Session 状态、修改密码或退出。新密码必须是有效 UTF-8，
至少 10 个 Unicode code point，并且不能跳过强制修改步骤。

## Session 与 CSRF

- 登录 Session 有效期为 24 小时，Cookie 路径限制在 `/_admin`。
- Session Cookie 使用 `HttpOnly` 和 `SameSite=Strict`。
- CSRF Cookie 使用 `SameSite=Strict`；所有 `POST`、`PUT`、`PATCH` 和
  `DELETE` 请求还必须携带匹配的 `X-CSRF-Token`。
- HTTPS 请求或可信代理提供 `X-Forwarded-Proto: https` 时，Cookie 标记为
  `Secure`。
- 修改密码会删除全部旧 Session，并签发新的 Session；退出会撤销当前 Session。

数据库只保存 Session 和 CSRF token 的哈希，但控制台仍应只通过受保护的 HTTPS
入口提供。

## Profile 导出

“系统”页面可以下载版本化的 Profile JSON。导出只包含 slug、名称、启用状态、
默认标记和 Profile 配置；不包含数据库 ID、Token 用量、管理员认证或 Session
数据，也不包含调用方密钥。

当前版本只提供 Profile 导出，没有 Profile 导入功能。导出用于审阅和外部备份，
不等同于完整数据库备份。

## 数据库备份与恢复

数据库位于 `DATA_DIR/llm-proxy.db`，采用 SQLite WAL。为避免复制到不一致的数据库
与 sidecar 文件，备份和恢复都应在服务停止时进行：

1. 停止 llm-proxy 服务。
2. 备份时复制整个 `DATA_DIR`；恢复时用同一版本产生的完整备份替换该目录。
3. 确认目录只允许服务账号访问，再启动服务。
4. 登录控制台，核对 Schema 版本、默认 Profile 和统计数据。

数据库包含密码哈希、Session 哈希、Profile 上游和使用记录。目录应保持 `0700`，
数据库文件保持 `0600`；备份也应加密、限制访问并纳入删除策略。不要在服务运行时
直接编辑 SQLite。

运行目录和 Compose 变量见[运行配置](configuration.md)。
