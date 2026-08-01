import trace from "@/data/route-trace-example.json";

const budgetDimensions = [
  ["回答尝试", trace.attempt_budget.max_answer_attempts, "次"],
  ["辅助调用", trace.attempt_budget.max_auxiliary_calls, "次"],
  ["总出站调用", trace.attempt_budget.max_total_outbound_calls, "次"],
  ["单 Target 重试", trace.attempt_budget.max_retries_per_target, "次"],
  ["Target 切换", trace.attempt_budget.max_target_switches, "次"],
  ["模型切换", trace.attempt_budget.max_model_switches, "次"],
  ["deadline", trace.attempt_budget.deadline_ms, "ms"],
  ["最坏成本", trace.attempt_budget.max_worst_case_cost_micro_usd, "μUSD"],
] as const;

const planBindings = [
  ["PolicyVersion", trace.execution_plan.snapshot_refs.policy_version_id],
  ["catalog", trace.execution_plan.snapshot_refs.catalog_sha],
  ["deployment revision", trace.execution_plan.snapshot_refs.deployment_revision],
  ["adapter", trace.execution_plan.snapshot_refs.adapter_sha],
  ["price", trace.execution_plan.snapshot_refs.price_sha],
  ["selection metrics", trace.execution_plan.snapshot_refs.selection_metrics_sha],
] as const;

export function RouteTraceExplorer() {
  return (
    <section className="route-trace-explorer" data-route-trace-explorer="true" aria-labelledby="route-trace-title">
      <header className="route-trace-header">
        <div>
          <p className="route-trace-kicker">Route Trace Explorer</p>
          <h2 id="route-trace-title">路由轨迹</h2>
        </div>
        <span className="route-trace-badge">{trace.label}</span>
      </header>
      <p className="route-trace-disclaimer">
        此视图是一个固定 Profile 的去敏预计算审阅投影：列出规范 AttemptBudget 的全部维度和 ExecutionPlan 关键版本绑定，但不是实时遥测，也不会重新执行请求。
      </p>

      <div className="trace-grid trace-grid--scope">
        <section aria-labelledby="trace-scope-title">
          <h3 id="trace-scope-title">固定的单一 Profile 范围</h3>
          <dl className="trace-definition-list">
            <div><dt>profile_id</dt><dd>{trace.scope.profile_id}</dd></div>
            <div><dt>generation</dt><dd>{trace.scope.generation}</dd></div>
            <div><dt>envelope_sha</dt><dd>{trace.scope.envelope_sha}</dd></div>
            <div><dt>route_id</dt><dd>{trace.route.route_id}</dd></div>
            <div><dt>policy_id</dt><dd>{trace.route.policy_id}</dd></div>
          </dl>
        </section>
        <section aria-labelledby="trace-filter-title">
          <h3 id="trace-filter-title">硬约束过滤</h3>
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
        <h3 id="trace-plan-title">有序 Target 尝试</h3>
        <ol className="trace-attempts">
          {trace.execution_plan.attempts.map((attempt) => {
            const outcome = trace.attempts.find(({ order }) => order === attempt.order);
            return (
              <li key={attempt.order}>
                <span className="trace-attempt-order">{attempt.order}</span>
                <div>
                  <strong>{attempt.target_id}</strong>
                  <span>{attempt.model_id}</span>
                </div>
                {outcome && <span className={`trace-outcome trace-outcome--${outcome.outcome}`}>{outcome.outcome}</span>}
              </li>
            );
          })}
        </ol>
      </section>

      <section className="trace-section" aria-labelledby="trace-bindings-title">
        <h3 id="trace-bindings-title">ExecutionPlan 版本绑定</h3>
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
        第一次尝试以 <strong>retryable_pre_commit_failure</strong> 结束；第二次在首个合法响应事件后形成 <strong>ClientCommit</strong>，不再重试或切换 Target。
      </p>
    </section>
  );
}
