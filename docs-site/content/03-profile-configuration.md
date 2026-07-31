# Profile 配置与核心概念

> 状态：目标设计。当前 `main` 已有 Profile 和模型配置，但没有智能路由角色、策略发布和动态优化配置。

## 四种模型角色

| 角色 | 是否回答用户 | 主要职责 | 是否自动成为路由候选 |
|---|---:|---|---:|
| 参与路由模型 | 是 | 组成可选执行方案 | 是 |
| 强模型基线 | 是 | 安全兜底、质量基准 | 必须同时是参与模型 |
| 任务判断模型 | 否 | 判断任务、复杂度、风险与能力要求 | 否 |
| 质量评审模型 | 否 | 异步比较实际回答与基准回答 | 否 |

同一个模型 ID 可以承担多个角色，但角色含义仍需分别配置和统计。

## 启用智能路由

Profile 可以先保存配置，但在满足以下条件前不能接受 `model=auto`：

1. 至少有一个参与路由模型；
2. 已配置默认强模型基线；
3. 已配置任务判断模型；
4. 已生成并发布一份有效策略。

```json
{
  "routing": {
    "enabled": true,
    "participant_model_ids": [
      "fast-model",
      "balanced-model",
      "strong-model"
    ],
    "default_baseline_model_id": "strong-model",
    "capability_baselines": {
      "coding": "strong-model",
      "vision": "vision-strong-model",
      "long_context": "long-context-model"
    },
    "task_analyzer_model_id": "fast-model"
  }
}
```

首次发布应在同一事务中启用智能路由并创建策略部署记录。仅保存配置或生成草稿不能开放 `auto`。

## 启用动态策略优化

动态策略优化是独立开关。关闭它不会停止已发布策略。

```json
{
  "dynamic_optimization": {
    "enabled": true,
    "sample_percent": 5,
    "daily_budget_usd": 20,
    "quality_reviewer_model_id": "review-model",
    "redacted_content_retention_days": 7,
    "request_metadata_retention_days": 90
  }
}
```

| 字段 | 含义 | 运行时影响 |
|---|---|---|
| `sample_percent` | `auto` 请求的抽样比例 | 只影响异步评估量 |
| `daily_budget_usd` | 每日对照与评审上限 | 不足时跳过抽样 |
| `quality_reviewer_model_id` | 质量评审模型 | 不成为回答候选 |
| `redacted_content_retention_days` | 脱敏正文保留期 | 到期清理 |
| `request_metadata_retention_days` | 请求元数据保留期 | 不含原始正文 |

## 哪些配置立即生效

| 配置 | 保存后的行为 |
|---|---|
| 参与模型、基线、质量目标 | 生成新策略草稿，不能直接改变线上策略 |
| 最大切换、总尝试、整体超时 | 作为下一策略默认值 |
| 视觉路由规则 | 生成新策略草稿并重新评估 |
| 抽样比例、每日预算 | 动态优化运行控制，可独立调整 |
| 数据保留期限 | 影响后续清理任务 |
| 模型紧急停用 | 立即从活动候选排除并记录安全降级 |

后台字段旁应固定显示：

> 保存后生成草稿，不会直接修改线上策略。

## 模型能力

未填写的能力按“不确定”处理。“不确定”不能通过能力硬过滤。

| 能力类别 | 示例字段 |
|---|---|
| 容量 | 上下文窗口、最大输出 Token |
| 模态 | 文本、图片、图片数量、detail |
| 工具 | 工具调用、并行工具 |
| 结构化输出 | JSON Schema、必填字段 |
| 推理与响应 | reasoning、streaming |
| 缓存 | Prompt Cache |
| 协议 | Anthropic Messages、OpenAI Responses 等 |

## 上下文窗口与 Agent 自动压缩

`context_window` 是单个模型的能力上限，供路由器做硬过滤；`client_context_window`
是活动策略给 Agent 配置生成器使用的保守窗口，用于决定客户端何时自动压缩。两者不是同一个
值，代理本身也不压缩、总结或截断会话。

`client_context_window` 默认取常规候选执行方案中可稳定使用的保守窗口，并预留最大输出、
工具定义和视觉证据空间。长上下文基线只在普通候选装不下请求时用于升级，不能把客户端窗口
虚增到它的上限。参与模型或模型窗口改变后，应生成新策略并重新计算该值；会话已经绑定的
模型装不下请求时，路由器升级到合格的长上下文方案。

```text
模型 context_window
  -> 扣除输出、工具与视觉证据预留
  -> 汇总常规候选的保守可用窗口
  -> 策略 client_context_window
  -> Agent 自动压缩参数
```

## 当前 OpenAI 视觉调用接口

当前 `main@ea13e52` 已允许 OpenAI Responses 图片请求选择识图子请求的接口：

| `vision.transport` | 识图子请求 |
|---|---|
| `openai_chat_completions` | 调用 Upstream 的 Chat Completions，当前默认值 |
| `openai_responses` | 调用 Upstream 的 Responses，并设置 `store: false` |

这个字段只决定“图片交给视觉模型时使用哪种 OpenAI 请求格式”。它不会选择回答用户的主模型，
不会把模型加入路由候选，也不是 `model=auto` 智能路由。OpenAI 主请求仍是
`POST /responses` 或 `POST /v1/responses`；Chat Completions 主请求当前不会进入图片预处理。

## 模型价格

价格必须对应 Profile 实际使用的模型 ID 与 Upstream。内置目录只是预填，管理员覆盖值优先。

```json
{
  "id": "example-model",
  "context_window": 200000,
  "max_output_tokens": 64000,
  "supports_vision": true,
  "supports_tools": true,
  "supports_json_schema": true,
  "pricing": {
    "currency": "USD",
    "unit": "per_million_tokens",
    "input": 1,
    "output": 5,
    "cache_read": 0.1,
    "cache_write": 1.25,
    "image_input_each": null,
    "source": "catalog",
    "effective_at": "2026-07-31",
    "revision": "catalog-revision"
  }
}
```

未知价格不能按零成本处理：

- 可以显式调用该模型；
- 不能加入自动成本优化；
- 必须先补充目录价格或 Profile 覆盖值。

金额在数据库中使用定点 micro-USD：

```text
1 USD = 1,000,000 micro-USD
```

不得用二进制浮点数累计预算。

## 配置隔离与保护

- Profile 之间的样本、表现、策略和发布记录完全隔离；
- 新 Profile 只继承内置模型目录先验，不继承其他 Profile 的在线证据；
- 活动策略依赖的基线和任务判断模型不能无替代项停用；
- 启用动态优化后，质量评审模型遵守同样保护；
- 替换角色模型必须事务化，并生成待重新评估的草稿。

## 本章验收项

- [ ] 保存配置与发布策略的区别明确；
- [ ] 四种模型角色不会混淆；
- [ ] 新模型不会自动进入生产路由；
- [ ] 未知能力不能通过硬过滤；
- [ ] 未知价格不能进入成本优化；
- [ ] `client_context_window` 不会被误当成任一模型的真实上下文上限；
- [ ] OpenAI 视觉 `transport` 不会被误解为智能路由；
- [ ] 金额使用 micro-USD 累计；
- [ ] Profile 之间的数据与策略完全隔离。
