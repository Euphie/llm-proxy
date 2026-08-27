import trace from "@/data/route-trace-example.json";

const budgetDimensions = [
  ["回答尝试", trace.attempt_budget.max_answer_attempts, "次"],
  ["辅助调用", trace.attempt_budget.max_auxiliary_calls, "次"],
  ["总出站调用", trace.attempt_budget.max_total_outbound_calls, "次"],
  ["单 Upstream 重试", trace.attempt_budget.max_retries_per_target, "次"],
  ["模型切换", trace.attempt_budget.max_model_switches, "次"],
  ["deadline", trace.attempt_budget.deadline, ""],
  ["最坏成本", trace.attempt_budget.max_worst_case_cost_micro_usd, "μUSD"],
] as const;

const planBindings = [
  ["Routing Policy", trace.execution_plan.snapshot_refs.routing_policy_version],
  ["模型目录修订", trace.execution_plan.snapshot_refs.model_catalog_revision],
  ["运行时修订", trace.execution_plan.snapshot_refs.runtime_revision],
] as const;

export function RouteTraceExplorer() {
  return (
    <section className="route-trace-explorer" data-route-trace-explorer="true" aria-labelledby="route-trace-title">
      <header className="route-trace-header">
        <div>
          <p className="route-trace-kicker">示例</p>
          <h2 id="route-trace-title">路由轨迹示例</h2>
        </div>
        <span className="route-trace-badge">{trace.label}</span>
      </header>
      <p className="route-trace-disclaimer">
        这是按当前实现字段整理的去敏示例，不是实时遥测。它用于说明一次请求为什么排除候选，以及失败后如何在统一预算内处理。
      </p>

      <div className="trace-grid trace-grid--scope">
        <section aria-labelledby="trace-scope-title">
          <h3 id="trace-scope-title">当前 Profile 与 Policy</h3>
          <dl className="trace-definition-list">
            <div><dt>profile_id</dt><dd>{trace.scope.profile_id}</dd></div>
            <div><dt>任务类型 → Route</dt><dd>{trace.route.task_type} → {trace.route.route_id}</dd></div>
            <div><dt>strategy_id</dt><dd>{trace.route.strategy_id}</dd></div>
            <div><dt>策略别名</dt><dd>{trace.route.strategy_alias}</dd></div>
          </dl>
        </section>
        <section aria-labelledby="trace-filter-title">
          <h3 id="trace-filter-title">分析与硬约束</h3>
          <p className="trace-reason">
            任务分析模型：{trace.task_analysis.analyzer_called
              ? `已调用，结果 ${trace.task_analysis.result} / ${trace.task_analysis.difficulty} / ${trace.task_analysis.risk}`
              : "未调用"}
          </p>
          <ul className="trace-list">
            {trace.hard_filter.excluded.map((candidate) => (
              <li key={candidate.model_id}>
                <strong>{candidate.model_id}</strong>
                <span>已排除：{candidate.reason}</span>
              </li>
            ))}
          </ul>
          <p className="trace-selection"><span>选定模型</span><strong>{trace.model_selection.selected_model_id}</strong></p>
          <p className="trace-reason">{trace.model_selection.reason}</p>
        </section>
      </div>

      <section className="trace-section" aria-labelledby="trace-plan-title">
        <h3 id="trace-plan-title">计划内尝试</h3>
        <ol className="trace-attempts">
          {trace.execution_plan.attempts.map((attempt) => {
            const outcome = trace.attempts.find(({ order }) => order === attempt.order);
            return (
              <li key={attempt.order}>
                <span className="trace-attempt-order">{attempt.order}</span>
                <div>
                  <strong>{attempt.model_id}</strong>
                </div>
                {outcome && <span className={`trace-outcome trace-outcome--${outcome.outcome}`}>{outcome.outcome}</span>}
              </li>
            );
          })}
        </ol>
      </section>

      <section className="trace-section" aria-labelledby="trace-bindings-title">
        <h3 id="trace-bindings-title">ExecutionPlan 固定信息</h3>
        <dl className="trace-definition-list">
          {planBindings.map(([label, value]) => (
            <div key={label}><dt>{label}</dt><dd>{value}</dd></div>
          ))}
        </dl>
      </section>

      <section className="trace-section" aria-labelledby="trace-budget-title">
        <h3 id="trace-budget-title">完整 AttemptBudget</h3>
        <dl className="trace-budget">
          {budgetDimensions.map(([label, value, unit]) => (
            <div key={label}><dt>{label}</dt><dd>{value}{unit}</dd></div>
          ))}
        </dl>
      </section>

      <p className="trace-commit-boundary">
        第一次尝试以 <strong>retryable_pre_commit_failure</strong> 结束；只有 <strong>overload_rules</strong>、规则次数和 Policy 预算同时允许，第二次才会在同一 Upstream 重试。首个完整合法响应事件形成 <strong>ClientCommit</strong>，此后不再重试或切换。
      </p>
    </section>
  );
}
