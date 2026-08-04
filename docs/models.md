# 模型能力与 Agent 上下文

在 Profile 编辑页的“模型能力”中，保存模型 ID、容量、工具/结构化输出/视觉能力与参考价格。
这些值是视觉门控、智能路由和配置生成器的唯一事实来源：ID 精确且区分大小写，
`supports_vision: true` 表示主模型原生处理图片、跳过影子请求；`false` 表示可使用
视觉增强。上下文或输出上限可留空。

## 输入与建议

Token 上限可填正整数，或十进制 `K`/`M` 简写：`128K` = 128,000、`1M` = 1,000,000、
`1.5M` = 1,500,000，上限为 `9,007,199,254,740,991`。

页面内置 239 条 [Models.dev](https://github.com/anomalyco/models.dev) 离线快照，
内容 revision 为
`5b701eb1b0c50ba722f44aae382dea0cd312b87de5eb37193944ff5cef3376f0`，
获取日期为 2026-08-03。管理员可在“系统”页手动更新；激活的远程版本保存到
`DATA_DIR/model-catalog.json`，失败时继续使用上一有效版本或内置快照。目录只用于录入建议，
不会改写用户输入的模型 ID，也不会自动改动 Profile 或路由策略。
来源、原始 feed 摘要与许可证见[第三方声明](../THIRD_PARTY_NOTICES.md)。

目录仅收录同时支持 text input、text output 和 tool call，且至少一个已知 provider
记录未标为 deprecated 的模型；没有 provider 映射时，canonical 记录本身不得为
deprecated。stable 与 preview 均可收录。上游未知的上下文、输出上限等字段保持空白，
不作猜测；当 output 等于 context 时省略 output。

### 匹配与应用

“批量录入模型”支持每行一个 ID，单次最多 100 个。点击“批量导入”后，同批重复项和
Profile 已有 ID 自动跳过；唯一可靠匹配自动带入推荐参数，多候选或未知 ID 只保留 ID、
参数留空。导入只加入当前 Profile 草稿，仍需检查并保存。

唯一匹配时，以下类型可自动应用：目录 ID 精确匹配、API ID、官方 alias、显式
compatibility alias，以及在 ID 前增加一层合法 wrapper 命名空间。例如
`claude-glm-5.2` 通过本地显式 compatibility alias 匹配 `zhipuai/glm-5.2`。

family 以及 `preview`、`latest`、日期或已登记 snapshot 规则产生的候选只供手动选择；
歧义或非法 ID 不自动应用。界面最多显示 5 个候选。模型 ID 的 `input` 事件只更新预览，
粘贴完整 ID 时会立即应用唯一安全匹配；其他输入在 `change` 或 `blur` 时自动应用。
手动候选需点击应用按钮。

推荐只填写空字段，或替换仍由上一条推荐自动填写的值。用户手工编辑容量、能力或价格后，
该字段不再由推荐覆盖。推荐器从不替换模型 ID，保存时保留用户输入的原始 ID。
完全没有匹配结果时，后台会显示可搜索的模型参数模板列表。管理员明确选择并应用模板后，
它会替换当前容量、能力和价格，但仍保留实际模型 ID。

### 能力与价格

智能路由还读取 `supports_tools`、`supports_structured_output`、输入/输出价格，以及可选的
缓存读取和默认缓存写入价格。价格单位是“每百万
Token 的微美元”。后台页面直接填写美元小数，例如 `0.10 USD / 1M Token` 填 `0.10`；保存时
会精确转换为 API 中的整数 `100000` 微美元。零价格有效；留空表示未知，最多填写 6 位小数。

缓存价格不区分 5 分钟和 1 小时写入档位，统一填写当前 Upstream 的默认写入价。未配置缓存价格
不会阻止模型参与路由；但响应实际报告了对应缓存 Token 时，该次实际费用无法完整核算。Models.dev
存在 `cache_read` 或 `cache_write` 时，推荐目录会一并带入。

内置目录只采用模型厂商的直接 provider 价格；存在上下文或速度分档时使用较保守的最高公开
档位。它是录入参考，不代表代理实际 Upstream 的合同价、折扣价或中转价。启用 Auto 前应按
实际账单确认并修改；参与路由的模型缺少输入或输出价格时不能发布配置。

### 目录来源与更新

生成器以 `https://models.dev/models.json` 的 canonical 模型事实为基础，并用
`https://models.dev/api.json` 补充 provider、API ID 和状态信息。本地 override 对其
明确提供的字段拥有最高优先级，也可增加 compatibility alias 与官方参考链接；当前
override 位于 `scripts/model-catalog-overrides.mjs`。

日常运维在“系统”页点击“从 Models.dev 更新”。服务端只访问固定来源，校验成功后原子
替换运行时目录。更新后的潜在影响报告会列出容量、能力或价格变化涉及的 Profile 和策略，
但不自动覆盖已保存参数。

发版维护者要同步内置快照时使用：

```sh
make update-model-catalog
```

该命令只在维护时联网，在一次性 `node:24-alpine` Docker 容器中运行
`scripts/update-model-catalog.mjs`。脚本使用 `scripts/model-catalog-lib.mjs` 构建并
完整验证目录，再通过 `scripts/model-catalog-update-lib.mjs` 在目标文件同目录原子替换
`internal/admin/ui/static/model-catalog-data.js` 和 `internal/modelcatalog/data/catalog.json`；两个目标文件都使用原子替换，某个目标写入失败时该文件保留旧快照。revision
由两份原始 feed 的 SHA-256 组合生成，不依赖 GitHub API。

### 行为变化

旧的 12 条手工清单已由上述广目录替换；这是行为变化，不承诺保留旧清单的历史兼容性。

## 临时自动压缩

Claude Code、OpenCode 和 Codex 面板都有“自动压缩触发比例”：默认 `85`，只接受
`1..99` 的整数，仅存在于当前弹窗，关闭或刷新即恢复默认，不会保存到 Profile。对有
上下文窗口的已保存模型，触发点为 `floor(context × percent / 100)`；如果已保存最大输出
Token 要保留更多空间，则取更低的安全值 `context - max_output_tokens`。缺少上下文时，
生成器显示警告并省略相应设置，绝不从内置建议猜测。

代理自身不会压缩、总结或截断会话；这些字段只交给客户端 Agent。

## Agent 映射

| Agent | 使用的已保存模型事实 | 生成字段 |
|---|---|---|
| Claude Code | Sonnet/Haiku/Opus 映射中的全部模型 | `CLAUDE_CODE_AUTO_COMPACT_WINDOW` 与 `CLAUDE_AUTOCOMPACT_PCT_OVERRIDE`；多映射取保守安全值 |
| Codex | 选中模型 | `model_context_window`、`model_auto_compact_token_limit`，并固定 `wire_api = "responses"` |
| OpenCode | 选中模型 | context 与 output 都存在时，生成完整 `limit` 和顶层 `compaction: { "auto": true, "reserved": ... }` |

Codex 可能将较大的窗口限制到自身目录上限；生成器只能提示，不能保证客户端最终采用的
数值。[OpenCode 当前 stable schema](https://opencode.ai/config.json) 和
[V1 runtime schema](https://github.com/anomalyco/opencode/blob/dev/packages/core/src/v1/config/provider.ts)
都要求 `limit` 同时包含数字 `context` 与 `output`。因此 context-only 模型仍生成基础
provider/model 配置，但省略 `limit` 与依赖它的 `compaction` 并显示警告，不猜测
output。OpenCode 使用 stable `reserved` 格式；V2 不在本生成器范围内。
