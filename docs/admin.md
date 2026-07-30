# 管理控制台

管理控制台位于 `/_admin/`，包含 Profiles、统计和系统三个页面。

## 首次登录

首次启动使用账号 `admin`、密码 `admin` 登录，然后按页面提示设置至少 10 个字符的
新密码。

## 后台使用流程

### 1. 创建 Profile

进入“Profiles”，点击“创建第一个 Profile”或“新建 Profile”：

1. 填写名称和 slug。slug 会成为公开代理 URL 的路径前缀。
2. 选择与 Upstream 一致的 `anthropic` 或 `openai` 协议。
3. 填写 Upstream 基础地址，不要包含调用方密钥。
4. 保存 Profile。第一个 Profile 会自动成为默认项，后续保存会立即热更新运行时。

默认 Profile 使用 `/v1/...`；指定 Profile 使用 `/<slug>/v1/...`。例如 slug 为
`coding` 时，Anthropic Messages 地址为 `/coding/v1/messages`。

### 2. 配置模型与视觉增强

“模型能力”是选填项。粘贴已收录的模型 ID 会自动填写上下文窗口、最大输出 Token
和视觉能力；保存前可以人工调整。`支持视觉 = 否` 表示该主模型可使用图片增强，
`支持视觉 = 是` 表示图片直接交给主模型。

需要图片增强时：

1. 打开“启用视觉预处理”。
2. 在“识图模型”中填写 Upstream 实际支持的视觉模型。
3. 选择未收录模型的处理方式。
4. 如有需要，在“视觉参数”中调整 Token、超时、并发、缓存或提示词。

Profile 的过载规则按页面顺序匹配，命中第一条后按其重试次数和等待参数执行。
更完整的字段说明见 [Profiles](profiles.md) 和[图片预处理](vision.md)。

### 3. 生成 Agent 配置

在 Profile 列表点击“生成配置”：

1. 填写 Agent 能访问到的 llm-proxy 公网地址。
2. 选择 Claude Code、OpenCode 或 Codex CLI。
3. 选择全局、项目或命名配置目标，并填写模型映射。
4. 复制或下载生成结果，保存到页面标出的客户端路径。
5. 在 Agent 本地设置真实认证环境变量。

生成器不会保存公网地址、模型映射或密钥。Agent 支持范围和具体输出字段见
[模型能力与 Agent 上下文](models.md)。

### 4. 日常管理

- “Profiles”支持编辑、复制、启停、设为默认和删除；所有变更保存后立即生效。
- “统计”支持按 Profile、协议、模型、请求类型和时间筛选 Token 用量。
- “系统”支持修改管理员密码、查看运行信息和导出版本化 Profile JSON。
- Profile JSON 可用于审阅或迁移配置，不包含调用方密钥。

具体字段说明见 [Profiles](profiles.md)、[模型能力](models.md)、
[图片预处理](vision.md)和 [Token 用量统计](statistics.md)。
