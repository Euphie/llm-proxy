# 公开模型评测目录

公共评测数据只用于智能策略的冷启动质量先验，不写入本地异步评测证据，也不能替代本地请求轨迹、A/B 对比或人工发布。

## 后台下载与更新

在 Profile 的“智能路由 → 配置策略”中点击“下载并更新公开评测”。后台会固定下载 LiveBench、BFCL、
Open VLM Leaderboard（VLMEvalKit）、LMArena 和 SWE-bench，显示每个来源的下载、解析、导入和未映射数量。
请求立即返回后台任务；重复点击不会启动第二个任务。

五个来源必须全部成功，服务才会把完整快照原子写入 `DATA_DIR/model-evaluations.json` 并切换内存目录。
任一来源失败、超时、格式变化或校验失败时继续使用旧快照。内容未变化时 revision 保持不变；服务重启后
会继续读取已保存的快照。更新只改变评测目录，不修改 active、canary 或现有草稿，也不会自动重新生成策略。

下载器只接受代码内固定的 HTTPS 官方地址，重定向和 DNS 解析结果会重新校验；单个文件上限为 64 MiB。
LiveBench 只读取模型、分数和分类列，不依赖问题 ID 的存储类型。BFCL 同一模型存在多种展示模式时，
固定按 `FC`、`FC thinking`、无后缀、`Prompt`、`Prompt + Thinking` 的顺序选取一条；同一优先级重复仍会让更新失败。
SWE-bench 的 Scaffold 同时绑定官方结果名称、folder、mini-swe-agent 版本和 System 标签，避免混合不同 Agent 产物。

## 离线导入

联网更新不可用时，可使用 Manifest 生成同一格式的离线快照：

## 支持的输入

| Adapter | 输入 | 编译语义 |
|---|---|---|
| `livebench_csv` | LiveBench `all_groups.csv` 等固定导出 | 有样本量的有界分数 |
| `bfcl_csv` | BFCL 固定版本的汇总 CSV | `tool_use` 有界分数 |
| `vlmevalkit_csv` | VLMEvalKit 固定评测结果 CSV | `vision` 有界分数 |
| `arena_csv` | Arena 固定版本排行榜 CSV | 默认仅导入排名，不生成质量硬门槛 |
| `swebench_json` | SWE-bench 站点 `leaderboards.json` | 有界解决率，必须绑定 Scaffold |

离线导入器不联网下载数据。管理员先从官方来源取得一个固定版本，核对该数据文件的许可证，再在 Manifest 中声明来源版本、检索时间和原始文件 SHA-256。任何散列不匹配、未知列、非法模型 ID 或不完整 Scaffold 都会让整个快照失败，旧快照保持不变。

## Manifest

下面是一个单来源示例；`sources` 可以同时包含五类 Adapter：

```json
{
  "schema_version": 1,
  "retrieved_at": "2026-08-05T00:00:00Z",
  "sources": [
    {
      "id": "livebench",
      "name": "LiveBench",
      "url": "https://github.com/LiveBench/LiveBench",
      "license": "REPLACE_WITH_VERIFIED_DATA_LICENSE",
      "version": "REPLACE_WITH_PINNED_RELEASE_OR_COMMIT",
      "retrieved_at": "2026-08-05T00:00:00Z",
      "path": "raw/livebench-all-groups.csv",
      "sha256": "REPLACE_WITH_LOWERCASE_SHA256",
      "adapter": "livebench_csv",
      "model_map": {
        "exact-upstream-name": "provider/exact-model-version"
      },
      "rules": [
        {
          "benchmark": "livebench-reasoning",
          "domain": "reasoning",
          "metric": "bounded_score",
          "model_column": "model",
          "score_column": "reasoning",
          "fixed_samples": 100,
          "score_scale": "percent",
          "settings_sha256": "REPLACE_WITH_EVALUATION_SETTINGS_SHA256"
        }
      ]
    }
  ]
}
```

规则中的列名区分大小写，必须与固定导出完全一致。`model_map` 是唯一允许的身份映射；未显式映射的行会跳过，不做模糊匹配。目标 ID 必须是 `provider/model-version` 形式，并与 Profile 模型的 canonical ID 或管理员确认的精确 Alias 一致。

有界分数需要 `samples_column` 或 `fixed_samples`。如果原始文件没有上下界，导入器按样本量计算 90% Wilson 区间；如果提供 `lower_column` 和 `upper_column`，两者必须同时提供。SWE-bench 还必须为每个映射项提供 `scaffold_map` 和 `scaffold_sha256_map`。

## 生成并加载快照

所有命令都在 Docker 中运行：

```sh
make update-evaluation-catalog \
  MANIFEST=./evaluation-import/manifest.json \
  OUTPUT=./data/model-evaluations.json
```

导入成功会输出导入数、未映射跳过数和确定性的快照 revision。将输出放入
`DATA_DIR/model-evaluations.json` 后重启服务即可载入。生成策略仍然只创建 draft；管理员可以修改字段或
恢复单个推荐值，再显式进入评估、灰度和发布流程。目录或证据变化时重新生成新草稿，已有版本不原地刷新。

## 安全边界

- 公共先验的总强度最多为 20 个等效样本，本地可靠证据优先。
- 公开证据只匹配 Profile 保存的 `canonical_model_id`；不从上游模型 ID 做家族或跨版本推断。
- 排名数据只影响候选顺序，不生成硬质量阈值。
- BFCL 在本地工具评测协议完善前只能提供冷启动先验。
- SWE-bench 的模型、Agent Scaffold 和关键设置缺一不可。
- 仓库不附带第三方排行榜数据；代码许可证不代表全部下游评测数据可再分发。
