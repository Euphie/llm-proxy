import {
  importBatchModels,
  previewBatchModels,
} from "./profile-model-batch.js";

function node(tag, className = "", text = "") {
  const value = document.createElement(tag);
  if (className) value.className = className;
  if (text !== "") value.textContent = text;
  return value;
}

function button(text, className = "button-secondary") {
  const classes = className === "button" ? "button" : `button ${className}`;
  const value = node("button", classes, text);
  value.type = "button";
  return value;
}

function field(label, control, help = "") {
  const wrapper = node("label", "field stack");
  wrapper.append(node("span", "field-label", label), control);
  if (help) wrapper.append(node("span", "field-help muted", help));
  return wrapper;
}

function showMessage(target, message, error = false) {
  target.className = error ? "error-banner" : "success-banner";
  target.textContent = message;
  target.hidden = false;
}

async function run(target, action) {
  target.hidden = true;
  try {
    await action();
  } catch (error) {
    showMessage(target, error?.message || "操作失败，请刷新后重试。", true);
  }
}

export function renderImmediateRoutingPolicy(source, actions = {}) {
  const overview = source?.routing_policy_overview || {};
  const state = overview.runtime_state || {};
  const active = overview.active || null;
  const loadedPolicy = cloneValue(active?.policy || {});
  let workingPolicy = cloneValue(loadedPolicy);
  let collectPolicy = () => cloneValue(workingPolicy);
  let screen = "overview";
  let editStep = "roles";
  let pendingPreview = null;
  let changeReason = "";
  const generationState = {
    objective: "balanced",
    maxCostUSD: "",
    latencyMS: policyPositiveValue(workingPolicy.latency_target_ms),
    dailyEvalUSD: policyPositiveValue(
      microUSDToUSD(workingPolicy.dynamic_optimization?.daily_budget_micro_usd),
    ),
  };
  const modelIDs = availableModelIDs(source, workingPolicy);
  const section = node("section", "stack immediate-routing-policy");
  const header = node("div", "cluster section-heading");
  header.append(
    node("h2", "", "线上 Routing Policy"),
    node("span", "status-pill status-ready", "立即生效"),
  );
  const notice = node(
    "p",
    "warning-banner",
    "保存成功会立即影响所有新请求。系统会先验证并构建运行时，任何失败都不会改变当前线上策略。",
  );
  const identities = node("dl", "profile-overview-facts routing-runtime-identities");
  appendFact(identities, "运行时修订", state.revision ? `#${state.revision}` : "未初始化");
  appendFact(identities, "线上策略版本", active?.id ? `#${active.id}` : "无");
  appendFact(identities, "模型目录修订", state.model_catalog_revision ? `#${state.model_catalog_revision}` : "未初始化");
  const message = node("div", "success-banner");
  message.hidden = true;
  const workspace = node("section", "card stack policy-workspace");

  const syncWorkingPolicy = () => {
    workingPolicy = collectPolicy();
  };
  const mutateWorkingPolicy = (mutator) => {
    syncWorkingPolicy();
    mutator(workingPolicy);
    actions.markDirty?.();
    renderWorkspace();
  };
  const goToOverview = (discard = false) => {
    if (discard) {
      workingPolicy = cloneValue(loadedPolicy);
      pendingPreview = null;
      changeReason = "";
      actions.clearDirty?.();
    } else if (screen === "edit") {
      syncWorkingPolicy();
    }
    screen = "overview";
    message.hidden = true;
    renderWorkspace();
  };

  const renderOverview = () => {
    collectPolicy = () => cloneValue(workingPolicy);
    const home = node("div", "stack policy-home");
    const title = node("div", "cluster section-heading");
    title.append(
      node("h3", "", "当前策略概览"),
      node("span", "status-pill status-ready", "线上使用中"),
    );
    const identity = node("div", "policy-home-identity");
    identity.append(
      node("strong", "", loadedPolicy.name || "未命名策略"),
      node("span", "muted", loadedPolicy.alias || "未设置显示名称"),
    );
    const modelChips = node("div", "policy-home-models");
    for (const modelID of loadedPolicy.roles?.participants || []) {
      modelChips.append(node("span", "policy-model-chip", modelID));
    }
    const facts = node("dl", "profile-overview-facts policy-home-facts");
    appendFact(facts, "任务分析", loadedPolicy.roles?.task_analyzer_model || "未配置");
    appendFact(facts, "困难任务基线", loadedPolicy.roles?.strong_baseline_model || "未配置");
    appendFact(facts, "质量评审", loadedPolicy.roles?.reviewer_model || "未启用");
    appendFact(facts, "默认 Route", loadedPolicy.default_route || "未配置");
    appendFact(facts, "任务映射", `${loadedPolicy.task_routes?.length || 0} 条`);
    appendFact(facts, "Route", `${loadedPolicy.routes?.length || 0} 组`);
    appendFact(
      facts,
      "策略自动更新",
      automaticUpdateStatusLabel(loadedPolicy, overview.automatic_update),
    );
    const routes = node("div", "policy-home-routes");
    for (const route of loadedPolicy.routes || []) {
      const item = node("article", "policy-home-route");
      item.append(
        node("strong", "", route.id),
        node(
          "span",
          "muted",
          (route.candidates || []).map((candidate) =>
            `${candidate.model}${candidate.production_eligible ? " · Production" : " · Shadow"}`
          ).join("、") || "无候选模型",
        ),
      );
      routes.append(item);
    }
    const controls = node("div", "policy-home-actions");
    const intelligent = button("智能生成策略", "button");
    const manual = button("手动调整", "button-secondary");
    intelligent.addEventListener("click", () => {
      screen = "generate";
      renderWorkspace();
    });
    manual.addEventListener("click", () => {
      screen = "edit";
      editStep = "roles";
      renderWorkspace();
    });
    controls.append(intelligent, manual);
    home.append(title, identity, modelChips, facts, routes, controls);
    workspace.replaceChildren(home);
  };

  const renderGenerator = () => {
    collectPolicy = () => cloneValue(workingPolicy);
    const panel = node("div", "stack policy-generation-workspace");
    const heading = node("div", "cluster section-heading");
    const headingCopy = node("div", "stack");
    headingCopy.append(
      node("h3", "", "智能生成策略"),
      node(
        "p",
        "muted",
        "根据当前可用模型、公开评测和本地证据生成建议。生成建议本身不会影响线上；是否自动校准由高级设置单独控制。",
      ),
    );
    const back = button("返回概览", "button-tertiary");
    back.addEventListener("click", () => goToOverview(false));
    heading.append(headingCopy, back);
    const objective = selectInput([
      ["balanced", "均衡"],
      ["quality", "质量优先"],
      ["cost", "成本优先"],
      ["latency", "延迟优先"],
    ], generationState.objective);
    objective.name = "routing_policy_generation_objective";
    const primary = node("div", "policy-generation-primary");
    primary.append(field("优化目标", objective, "决定质量、稳定性、成本和延迟的初始权重。"));
    const constraints = node("details", "policy-generation-advanced");
    constraints.append(node("summary", "", "生成约束（选填）"));
    const constraintGrid = node("div", "policy-form-grid policy-generation-constraints");
    const maxCost = numberInput(generationState.maxCostUSD, 0.000001, 0);
    maxCost.value = generationState.maxCostUSD;
    maxCost.name = "routing_policy_generation_max_cost_usd";
    const latency = numberInput(generationState.latencyMS, 1, 1);
    latency.value = generationState.latencyMS;
    latency.name = "routing_policy_generation_latency_ms";
    const dailyEval = numberInput(generationState.dailyEvalUSD, 0.000001, 0);
    dailyEval.value = generationState.dailyEvalUSD;
    dailyEval.name = "routing_policy_generation_daily_eval_usd";
    constraintGrid.append(
      field("单请求费用上限（USD）", maxCost, "最多 6 位小数。"),
      field("目标延迟（ms）", latency, "延迟优先时用于约束候选。"),
      field("每日评测预算（USD）", dailyEval, "载入后仍可在高级设置中调整。"),
    );
    constraints.append(constraintGrid);
    const generate = button(pendingPreview ? "重新生成建议" : "智能生成建议", "button");
    generate.disabled = typeof actions.generateRoutingPolicy !== "function";
    generate.addEventListener("click", () => run(message, async () => {
      generationState.objective = objective.value;
      generationState.maxCostUSD = maxCost.value;
      generationState.latencyMS = latency.value;
      generationState.dailyEvalUSD = dailyEval.value;
      const participants = workingPolicy.roles?.participants || [];
      if (participants.length < 2) throw new Error("智能生成至少需要选择两个参与模型。");
      const intent = { objective: objective.value, participants };
      const maxCostValue = optionalUSDToMicroUSD(maxCost, "单请求费用上限");
      const latencyValue = optionalPositiveInteger(latency, "目标延迟");
      const dailyEvalValue = optionalUSDToMicroUSD(dailyEval, "每日评测预算");
      if (maxCostValue !== null) intent.max_cost_per_request_micro_usd = maxCostValue;
      if (latencyValue !== null) intent.latency_target_ms = latencyValue;
      if (dailyEvalValue !== null) intent.daily_eval_budget_micro_usd = dailyEvalValue;
      pendingPreview = await actions.generateRoutingPolicy?.(intent);
      renderWorkspace();
    }));
    panel.append(heading, primary, constraints, generate);
    if (pendingPreview) {
      const result = node("section", "policy-generation-result stack");
      renderGenerationResult(result, pendingPreview);
      const differences = renderPolicyDifferences(loadedPolicy, pendingPreview.policy, "与当前策略的差异");
      const resultActions = node("div", "policy-generation-actions");
      const load = button("载入并编辑", "button");
      const ignore = button("放弃建议", "button-tertiary");
      load.addEventListener("click", () => {
        workingPolicy = cloneValue(pendingPreview.policy || {});
        pendingPreview = null;
        screen = "edit";
        editStep = "roles";
        actions.markDirty?.();
        renderWorkspace();
      });
      ignore.addEventListener("click", () => {
        pendingPreview = null;
        renderWorkspace();
      });
      resultActions.append(load, ignore);
      panel.append(result, differences, resultActions);
    }
    workspace.replaceChildren(panel);
  };

  const renderEditor = () => {
    const editor = node("div", "stack policy-step-editor");
    const header = node("div", "cluster section-heading");
    header.append(
      node("div", "stack", ""),
      node("span", "status-pill status-warning", "尚未生效"),
    );
    header.children[0].append(
      node("h3", "", "调整策略"),
      node("p", "muted", "按模块修改，最后统一确认并立即生效。"),
    );
    const stepNav = node("nav", "policy-step-nav");
    stepNav.setAttribute("aria-label", "策略配置步骤");
    for (const [id, label] of [
      ["roles", "1 模型与角色"],
      ["tasks", "2 任务映射"],
      ["routes", "3 Route 与模型"],
      ["advanced", "高级设置"],
      ["review", "4 确认生效"],
    ]) {
      const stepButton = button(label, id === editStep ? "button-secondary policy-step-active" : "button-tertiary");
      if (id === editStep) stepButton.setAttribute("aria-current", "step");
      stepButton.addEventListener("click", () => {
        syncWorkingPolicy();
        editStep = id;
        renderWorkspace();
      });
      stepNav.append(stepButton);
    }
    const body = node("div", "stack policy-step-content");
    if (editStep === "review") {
      collectPolicy = () => cloneValue(workingPolicy);
      body.append(renderPolicyDifferences(loadedPolicy, workingPolicy, "待生效变更"));
      const reason = textInput(changeReason);
      reason.placeholder = "例如：只使用 DeepSeek 与 GLM";
      reason.addEventListener("input", () => { changeReason = reason.value; });
      body.append(
        field("变更原因", reason, "会写入不可变历史，便于审计和回滚。"),
        node("p", "warning-banner", "保存成功会立即影响所有新请求；校验失败时当前线上策略保持不变。"),
      );
    } else {
      const rendered = structuredPolicyForm(workingPolicy, modelIDs, {
        mutateStructure: mutateWorkingPolicy,
      }, editStep);
      collectPolicy = rendered.collect;
      body.append(rendered.content);
      body.addEventListener("input", () => actions.markDirty?.());
      body.addEventListener("change", () => actions.markDirty?.());
    }
    const footer = node("div", "policy-step-footer");
    const discard = button("放弃修改", "button-tertiary");
    discard.addEventListener("click", () => goToOverview(true));
    const footerMain = node("div", "cluster");
    if (editStep === "review") {
      const previous = button("返回 Route 设置", "button-secondary");
      previous.addEventListener("click", () => {
        editStep = "routes";
        renderWorkspace();
      });
      const save = button("保存并立即生效", "button");
      save.addEventListener("click", () => run(message, async () => {
        await actions.applyRoutingPolicy?.(
          Number(state.revision),
          cloneValue(workingPolicy),
          changeReason.trim(),
        );
        actions.clearDirty?.();
      }));
      footerMain.append(previous, save);
    } else {
      const sequence = ["roles", "tasks", "routes"];
      const index = sequence.indexOf(editStep);
      if (index > 0) {
        const previous = button("上一步", "button-secondary");
        previous.addEventListener("click", () => {
          syncWorkingPolicy();
          editStep = sequence[index - 1];
          renderWorkspace();
        });
        footerMain.append(previous);
      }
      if (index >= 0) {
        const next = button(index === sequence.length - 1 ? "检查并生效" : "下一步", "button");
        next.addEventListener("click", () => {
          syncWorkingPolicy();
          editStep = index === sequence.length - 1 ? "review" : sequence[index + 1];
          renderWorkspace();
        });
        footerMain.append(next);
      } else {
        const backToFlow = button("返回配置步骤", "button-secondary");
        backToFlow.addEventListener("click", () => {
          syncWorkingPolicy();
          editStep = "roles";
          renderWorkspace();
        });
        footerMain.append(backToFlow);
      }
    }
    footer.append(discard, footerMain);
    editor.append(header, stepNav, body, footer);
    workspace.replaceChildren(editor);
  };

  function renderWorkspace() {
    if (screen === "generate") renderGenerator();
    else if (screen === "edit") renderEditor();
    else renderOverview();
  }

  renderWorkspace();

  const history = node("details", "card stack policy-history");
  const versions = overview.history || [];
  history.append(node("summary", "", `历史版本（${versions.length}）`));
  const historyBody = node("div", "stack policy-history-body");
  if (versions.length === 0) {
    historyBody.append(node("p", "muted", "暂无历史版本。"));
  }
  for (const version of versions) {
    const row = node("article", "policy-history-row");
    const copy = node("div", "stack");
    copy.append(
      node("strong", "", `#${version.id} · ${version.policy?.name || "未命名"}`),
      node("span", "muted", `${changeKindLabel(version.change_kind, version.created_by)} · ${version.change_reason || "未填写原因"}`),
    );
    if (version.rollback_compatible === false) {
      copy.append(node("span", "error-text", `当前不可回滚：${version.compatibility_error || "与当前模型目录不兼容"}`));
    }
    const restore = button(version.id === active?.id ? "当前线上" : "回滚到此版本");
    restore.disabled = version.id === active?.id || version.rollback_compatible === false;
    restore.addEventListener("click", () => run(message, async () => {
      const confirmed = typeof window === "undefined" || window.confirm(
        `将版本 #${version.id} 复制为新的线上版本。确定继续吗？`,
      );
      if (!confirmed) return;
      await actions.rollbackRoutingPolicy?.(
        Number(state.revision), Number(version.id), `回滚到版本 #${version.id}`,
      );
    }));
    row.append(copy, restore);
    historyBody.append(row);
  }
  history.append(historyBody);

  section.append(header, notice, identities, message, workspace, history);
  return section;
}

function renderGenerationResult(target, preview) {
  const confidence = preview?.confidence || {};
  const explanations = preview?.explanations || [];
  const header = node("div", "cluster section-heading");
  header.append(
    node("strong", "", "生成建议已完成"),
    node("span", "status-pill status-ready", `置信度：${confidenceLabel(confidence.level)}`),
  );
  const evidence = node(
    "p",
    "muted",
    `本地候选 ${Number(confidence.local_candidates || 0)} · ` +
      `公开评测 Shadow ${Number(confidence.external_candidates || 0)} · ` +
      `临时 Shadow ${Number(confidence.provisional_candidates || 0)}`,
  );
  const digest = String(preview?.source_digest || "");
  const source = node(
    "p",
    "muted policy-generation-source",
    digest ? `数据摘要：${digest.slice(0, 16)}${digest.length > 16 ? "…" : ""}` : "数据摘要：未提供",
  );
  if (digest) source.setAttribute("title", digest);
  target.replaceChildren(header, evidence, source);
  if (explanations.length) {
    const details = node("details", "policy-generation-explanations");
    details.append(node("summary", "", `查看生成依据（${explanations.length} 条）`));
    const list = node("ul", "stack");
    for (const explanation of explanations) {
      list.append(node("li", "", explanation.message || explanation.code || "未提供说明"));
    }
    details.append(list);
    target.append(details);
  }
  target.hidden = false;
}

function renderPolicyDifferences(current, next, title) {
  const section = node("section", "policy-difference-panel stack");
  section.append(node("h4", "", title));
  const grid = node("div", "policy-difference-grid");
  const rows = [
    ["显示名称", current?.alias || "未设置", next?.alias || "未设置"],
    ["参与模型", current?.roles?.participants || [], next?.roles?.participants || []],
    ["任务分析", current?.roles?.task_analyzer_model || "未配置", next?.roles?.task_analyzer_model || "未配置"],
    ["困难任务基线", current?.roles?.strong_baseline_model || "未配置", next?.roles?.strong_baseline_model || "未配置"],
    ["任务映射", `${current?.task_routes?.length || 0} 条`, `${next?.task_routes?.length || 0} 条`],
    ["Route", `${current?.routes?.length || 0} 组`, `${next?.routes?.length || 0} 组`],
    ["最坏成本上限", formatPolicyUSD(current?.budget?.max_worst_case_cost_micro_usd), formatPolicyUSD(next?.budget?.max_worst_case_cost_micro_usd)],
  ];
  let changed = 0;
  for (const [label, beforeValue, afterValue] of rows) {
    const before = Array.isArray(beforeValue) ? beforeValue.join("、") || "无" : String(beforeValue);
    const after = Array.isArray(afterValue) ? afterValue.join("、") || "无" : String(afterValue);
    const item = node("article", "policy-difference-item");
    const isChanged = before !== after;
    if (isChanged) changed += 1;
    item.append(
      node("span", "muted", label),
      node("strong", "", isChanged ? after : "保持不变"),
    );
    if (isChanged) item.append(node("span", "muted policy-difference-before", `原值：${before}`));
    grid.append(item);
  }
  const summary = node(
    "p",
    changed ? "info-banner" : "success-banner",
    changed ? `共 ${changed} 项关键配置发生变化。` : "关键配置与当前线上策略一致。",
  );
  section.append(summary, grid);
  return section;
}

function formatPolicyUSD(value) {
  return `$${microUSDToUSD(value).toFixed(6).replace(/0+$/, "").replace(/\.$/, "") || "0"}`;
}

function confidenceLabel(level) {
  return { high: "高", medium: "中", low: "低" }[level] || "未知";
}

function policyPositiveValue(value) {
  const numeric = Number(value || 0);
  return numeric > 0 ? String(numeric) : "";
}

function optionalPositiveInteger(input, label) {
  if (!String(input.value || "").trim()) return null;
  return integerValue(input, label, 1);
}

function optionalUSDToMicroUSD(input, label) {
  const raw = String(input.value || "").trim();
  if (!raw) return null;
  if (!/^\d+(?:\.\d{1,6})?$/.test(raw)) {
    throw new Error(`${label}必须是非负金额，最多 6 位小数。`);
  }
  return usdToMicroUSD(input, label);
}

const taskTypes = [
  ["general", "通用"],
  ["coding", "编程"],
  ["reasoning", "推理"],
  ["math", "数学"],
  ["tool_use", "工具调用"],
  ["vision", "视觉"],
  ["simple", "简单任务"],
];

const difficulties = [
  ["easy", "简单"],
  ["medium", "中等"],
  ["hard", "困难"],
];

function structuredPolicyForm(policy, modelIDs, actions, view = "all") {
  const content = node("div", "stack");
  const refs = {};
  const routeIDs = (policy.routes || []).map((route) => route.id).filter(Boolean);

  const basics = policySection("策略与分析", "策略标识、默认路由和会话判断参数。");
  const basicGrid = node("div", "policy-form-grid");
  refs.name = textInput(policy.name || "");
  refs.alias = textInput(policy.alias || "");
  refs.defaultRoute = selectInput(routeIDs.map((id) => [id, id]), policy.default_route || routeIDs[0] || "");
  refs.minNetSavings = numberInput(bpsToPercent(policy.min_net_savings_bps), 0.1, 0, 100);
  refs.analyzerTimeout = textInput(policy.analyzer_timeout || "15s");
  refs.analyzerConfidence = numberInput(bpsToPercent(policy.analyzer_min_confidence_bps), 0.1, 0, 100);
  refs.sessionTTL = textInput(policy.session_ttl || "24h");
  refs.sessionLockTokenThreshold = numberInput(policy.session_lock_token_threshold || 100000, 1000, 1);
  basicGrid.append(
    field("策略名称", refs.name),
    field("显示名称", refs.alias),
    field("默认 Route", refs.defaultRoute),
    field("最低净节省（%）", refs.minNetSavings),
    field("任务分析超时", refs.analyzerTimeout),
    field("最低分析置信度（%）", refs.analyzerConfidence),
    field("Session 绑定有效期", refs.sessionTTL),
    field(
      "Session 锁定 Token 阈值",
      refs.sessionLockTokenThreshold,
      "默认 100K；会话输入或缓存读取达到该值，并满足稳定任务与置信度条件后锁定模型。修改后只撤销普通锁，最高模型锁保留。",
    ),
  );
  basics.append(basicGrid);

  const roles = policySection("模型角色", "参与模型决定 auto 可以使用的范围；关键角色必须从参与模型中选择。");
  const participantList = node("div", "policy-model-options");
  refs.participants = new Map();
  const selectedParticipants = new Set(policy.roles?.participants || []);
  for (const modelID of modelIDs) {
    const checkbox = checkboxInput(selectedParticipants.has(modelID));
    refs.participants.set(modelID, checkbox);
    const option = node("label", "policy-model-option");
    option.append(checkbox, node("span", "", modelID));
    participantList.append(option);
  }
  const roleGrid = node("div", "policy-form-grid");
  refs.strongBaseline = selectInput(modelIDs.map((id) => [id, id]), policy.roles?.strong_baseline_model || "");
  refs.taskAnalyzer = selectInput(modelIDs.map((id) => [id, id]), policy.roles?.task_analyzer_model || "");
  refs.reviewer = selectInput([["", "不启用"], ...modelIDs.map((id) => [id, id])], policy.roles?.reviewer_model || "");
  refs.selfEscalation = checkboxInput(Boolean(policy.self_escalation?.enabled));
  roleGrid.append(
    field("困难任务基线", refs.strongBaseline),
    field("任务分析模型", refs.taskAnalyzer),
    field("质量评审模型", refs.reviewer),
    toggleField("允许模型主动升级", refs.selfEscalation, "模型判断当前任务超出能力时，可以请求升级。"),
  );
  roles.append(participantList, roleGrid);

  const taskRoutes = policySection("任务映射", "为每种任务与难度选择 Route；留空表示没有显式映射。");
  const taskTable = node("div", "policy-task-matrix");
  const taskHeader = node("div", "policy-task-matrix-row policy-task-matrix-header");
  taskHeader.append(node("strong", "", "任务类型"));
  for (const [, label] of difficulties) taskHeader.append(node("strong", "", label));
  taskTable.append(taskHeader);
  refs.taskRoutes = [];
  const standardKeys = new Set();
  const taskRouteLookup = new Map();
  for (const mapping of policy.task_routes || []) {
    taskRouteLookup.set(`${mapping.task_type}:${mapping.difficulty}`, mapping.route);
  }
  for (const [taskType, taskLabel] of taskTypes) {
    const row = node("div", "policy-task-matrix-row");
    row.append(node("span", "policy-task-label", taskLabel));
    for (const [difficulty, difficultyLabel] of difficulties) {
      const key = `${taskType}:${difficulty}`;
      standardKeys.add(key);
      const control = selectInput(
        [["", "不设置"], ...routeIDs.map((id) => [id, id])],
        taskRouteLookup.get(key) || "",
      );
      control.setAttribute("aria-label", `${taskLabel} · ${difficultyLabel}`);
      refs.taskRoutes.push({ taskType, difficulty, control });
      row.append(control);
    }
    taskTable.append(row);
  }
  refs.extraTaskRoutes = cloneValue((policy.task_routes || []).filter(
    (mapping) => !standardKeys.has(`${mapping.task_type}:${mapping.difficulty}`),
  ));
  taskRoutes.append(taskTable);
  if (refs.extraTaskRoutes.length) {
    taskRoutes.append(node("p", "field-help muted", `另保留 ${refs.extraTaskRoutes.length} 条高级映射。`));
  }

  const routes = policySection("Route 与候选模型", "为每个 Route 选择实际回答模型；低频门槛和评分参数在高级设置中统一调整。");
  const routeGrid = node("div", "policy-route-grid");
  const advancedRoutes = policySection("Route 门槛与评分", "调整 Route 准入门槛、评分权重和候选模型的质量先验。");
  const advancedRouteGrid = node("div", "policy-route-grid policy-route-advanced-grid");
  refs.routes = [];
  for (const [routeIndex, route] of (policy.routes || []).entries()) {
    const routeCard = node("article", "policy-route-card stack");
    const routeHeading = node("div", "cluster section-heading");
    routeHeading.append(node("h4", "", `Route · ${route.id}`));
    const routeHeadingActions = node("div", "cluster policy-route-heading-actions");
    routeHeadingActions.append(node("span", "muted", `${(route.candidates || []).length} 个候选模型`));
    const removeRoute = button("移除 Route", "button-tertiary button-compact");
    removeRoute.disabled = (policy.routes || []).length <= 1;
    removeRoute.addEventListener("click", () => actions.mutateStructure((next) => {
      const removedID = next.routes[routeIndex]?.id;
      next.routes.splice(routeIndex, 1);
      const fallback = next.routes[0]?.id || "";
      if (next.default_route === removedID) next.default_route = fallback;
      for (const mapping of next.task_routes || []) {
        if (mapping.route === removedID) mapping.route = fallback;
      }
    }));
    routeHeadingActions.append(removeRoute);
    routeHeading.append(routeHeadingActions);
    const routeRef = {
      base: cloneValue(route),
      id: textInput(route.id || ""),
      minQuality: numberInput(bpsToPercent(route.min_quality_bps), 0.1, 0, 100),
      minStability: numberInput(bpsToPercent(route.min_stability_bps), 0.1, 0, 100),
      maxError: numberInput(bpsToPercent(route.max_severe_error_rate_bps), 0.1, 0, 100),
      qualityWeight: numberInput(bpsToPercent(route.weights?.quality_bps), 0.1, 0, 100),
      stabilityWeight: numberInput(bpsToPercent(route.weights?.stability_bps), 0.1, 0, 100),
      costWeight: numberInput(bpsToPercent(route.weights?.cost_bps), 0.1, 0, 100),
      performanceWeight: numberInput(bpsToPercent(route.weights?.performance_bps), 0.1, 0, 100),
      candidates: [],
    };
    routeRef.id.readOnly = true;
    const candidates = node("div", "stack policy-candidate-list");
    const candidateHeader = node("div", "cluster section-heading");
    candidateHeader.append(
      node("strong", "", "回答模型"),
      node("span", "muted", "按顺序参与当前 Route 选型"),
    );
    candidates.append(candidateHeader);
    const candidateSummaries = node("div", "stack policy-candidate-summaries");
    const candidateMetrics = node("div", "stack policy-candidate-metrics-list");
    for (const [candidateIndex, candidate] of (route.candidates || []).entries()) {
      const candidateRef = {
        base: cloneValue(candidate),
        model: selectInput(modelIDs.map((id) => [id, id]), candidate.model || ""),
        quality: numberInput(bpsToPercent(candidate.quality_score_bps), 0.1, 0, 100),
        stability: numberInput(bpsToPercent(candidate.stability_score_bps), 0.1, 0, 100),
        error: numberInput(bpsToPercent(candidate.severe_error_rate_bps), 0.1, 0, 100),
        latency: numberInput(candidate.expected_latency_ms || 0, 1, 0),
        productionEligible: checkboxInput(Boolean(candidate.production_eligible)),
      };
      const removeCandidate = button("移除", "button-tertiary button-compact");
      removeCandidate.addEventListener("click", () => actions.mutateStructure((next) => {
        next.routes[routeIndex].candidates.splice(candidateIndex, 1);
      }));
      const candidateSummary = node("div", "policy-candidate-summary");
      candidateSummary.append(
        node("span", "policy-candidate-index", String(candidateIndex + 1)),
        field("模型", candidateRef.model),
        removeCandidate,
      );
      const metricGroup = node("section", "policy-candidate-metrics stack");
      metricGroup.append(
        node("strong", "", `候选 ${candidateIndex + 1} · 评分参数`),
        node("p", "muted", "用于候选排序和成本收益判断，不影响模型身份。"),
      );
      const metricFields = node("div", "policy-candidate-metric-fields");
      metricFields.append(
        field("质量（%）", candidateRef.quality),
        field("稳定性（%）", candidateRef.stability),
        field("严重错误（%）", candidateRef.error),
        field("延迟（ms）", candidateRef.latency),
        toggleField(
          "允许生产流量",
          candidateRef.productionEligible,
          "关闭时仍可参加 Shadow 评测。管理员手动开启会直接授权生产流量；自动晋升只使用可靠本地证据。",
        ),
      );
      metricGroup.append(metricFields);
      routeRef.candidates.push(candidateRef);
      candidateSummaries.append(candidateSummary);
      candidateMetrics.append(metricGroup);
    }
    const addCandidate = button("添加候选模型", "button-secondary button-compact");
    addCandidate.addEventListener("click", () => actions.mutateStructure((next) => {
      next.routes[routeIndex].candidates = next.routes[routeIndex].candidates || [];
      next.routes[routeIndex].candidates.push({
        model: modelIDs[0] || "",
        production_eligible: false,
        quality_score_bps: 8000,
        stability_score_bps: 8000,
        severe_error_rate_bps: 500,
        expected_latency_ms: 0,
      });
    }));
    candidates.append(candidateSummaries, addCandidate);

    const routeAdvanced = node("details", "policy-route-advanced");
    routeAdvanced.append(node("summary", "", "高级设置：门槛、权重和候选评分"));
    const advancedBody = node("div", "stack policy-route-advanced-body");
    const thresholdGroup = node("section", "stack policy-route-advanced-group");
    thresholdGroup.append(
      node("strong", "", "准入门槛"),
      node("p", "muted", "候选必须满足全部门槛，才会参与当前 Route 选型。"),
    );
    const thresholdFields = node("div", "policy-route-threshold-fields");
    thresholdFields.append(
      field("质量下限（%）", routeRef.minQuality),
      field("稳定性下限（%）", routeRef.minStability),
      field("严重错误上限（%）", routeRef.maxError),
    );
    thresholdGroup.append(thresholdFields);
    const weightGroup = node("section", "stack policy-route-advanced-group");
    weightGroup.append(
      node("strong", "", "评分权重"),
      node("p", "muted", "四项权重共同决定通过门槛后的候选排序。"),
    );
    const weightFields = node("div", "policy-route-weight-fields");
    weightFields.append(
      field("质量（%）", routeRef.qualityWeight),
      field("稳定性（%）", routeRef.stabilityWeight),
      field("成本（%）", routeRef.costWeight),
      field("性能（%）", routeRef.performanceWeight),
    );
    weightGroup.append(weightFields);
    advancedBody.append(thresholdGroup, weightGroup);
    if (routeRef.candidates.length) {
      const candidateMetricGroup = node("section", "stack policy-route-advanced-group");
      candidateMetricGroup.append(node("strong", "", "候选评分"), candidateMetrics);
      advancedBody.append(candidateMetricGroup);
    }
    routeAdvanced.append(advancedBody);
    const advancedRouteCard = node("article", "policy-route-card policy-route-advanced-card stack");
    advancedRouteCard.append(node("h4", "", `Route · ${route.id}`), routeAdvanced);
    routeCard.append(routeHeading, candidates);
    routeGrid.append(routeCard);
    advancedRouteGrid.append(advancedRouteCard);
    refs.routes.push(routeRef);
  }
  const addRoute = button("添加 Route", "button-secondary");
  addRoute.addEventListener("click", () => actions.mutateStructure((next) => {
    const used = new Set((next.routes || []).map((route) => route.id));
    let index = used.size + 1;
    while (used.has(`route_${index}`)) index += 1;
    next.routes = next.routes || [];
    next.routes.push({
      id: `route_${index}`,
      min_quality_bps: 8000,
      min_stability_bps: 8000,
      max_severe_error_rate_bps: 500,
      weights: { quality_bps: 4000, stability_bps: 2500, cost_bps: 2500, performance_bps: 1000 },
      candidates: [],
    });
  }));
  routes.append(routeGrid, addRoute);
  advancedRoutes.append(advancedRouteGrid);

  const budget = policySection("调用预算", "限制一次请求中的回答尝试、辅助调用、切换次数和最坏成本。");
  const budgetGrid = node("div", "policy-form-grid policy-form-grid-compact");
  refs.maxAnswerAttempts = numberInput(policy.budget?.max_answer_attempts ?? 2, 1, 1);
  refs.maxAuxiliaryCalls = numberInput(policy.budget?.max_auxiliary_calls ?? 2, 1, 0);
  refs.maxOutboundCalls = numberInput(policy.budget?.max_total_outbound_calls ?? 5, 1, 1);
  refs.maxRetries = numberInput(policy.budget?.max_retries_per_target ?? 1, 1, 0);
  refs.maxSwitches = numberInput(policy.budget?.max_model_switches ?? 1, 1, 0);
  refs.deadline = textInput(policy.budget?.deadline || "2m");
  refs.maxWorstCaseCost = numberInput(microUSDToUSD(policy.budget?.max_worst_case_cost_micro_usd), 0.01, 0);
  budgetGrid.append(
    field("回答尝试上限", refs.maxAnswerAttempts),
    field("辅助调用上限", refs.maxAuxiliaryCalls),
    field("总上游调用上限", refs.maxOutboundCalls),
    field("单模型重试上限", refs.maxRetries),
    field("模型切换上限", refs.maxSwitches),
    field("总截止时间", refs.deadline),
    field("最坏成本上限（USD）", refs.maxWorstCaseCost),
  );
  budget.append(budgetGrid);

  const optimization = policySection("异步评测与风险", "控制质量采样、后台任务资源和运行时风险边界。");
  const optimizationGrid = node("div", "policy-form-grid policy-form-grid-compact");
  refs.optimizationEnabled = checkboxInput(Boolean(policy.dynamic_optimization?.enabled));
  refs.sampleRate = numberInput(bpsToPercent(policy.dynamic_optimization?.sample_rate_bps), 0.1, 0, 100);
  refs.dailyBudget = numberInput(microUSDToUSD(policy.dynamic_optimization?.daily_budget_micro_usd), 0.01, 0);
  refs.optimizationReviewer = selectInput(
    [["", "不设置"], ...modelIDs.map((id) => [id, id])],
    policy.dynamic_optimization?.reviewer_model || "",
  );
  refs.maxConcurrency = numberInput(policy.dynamic_optimization?.max_concurrency ?? 2, 1, 1);
  refs.queueCapacity = numberInput(policy.dynamic_optimization?.queue_capacity ?? 128, 1, 1);
  refs.taskTimeout = textInput(policy.dynamic_optimization?.task_timeout || "90s");
  refs.autoUpdatePolicy = checkboxInput(Boolean(policy.dynamic_optimization?.auto_update_policy));
  optimizationGrid.append(
    toggleField("启用异步评测", refs.optimizationEnabled, "按采样率写入质量证据。"),
    toggleField(
      "评测可靠后自动更新线上策略",
      refs.autoUpdatePolicy,
      "需同时启用异步评测。Shadow 晋升需可靠本地证据、连续两次一致确认和 6 小时冷却；证据缺失、不可靠或跌破门槛会立即撤销生产资格。",
    ),
    field("采样率（%）", refs.sampleRate),
    field("每日预算（USD）", refs.dailyBudget),
    field("评审模型", refs.optimizationReviewer),
    field("最大并发", refs.maxConcurrency),
    field("队列容量", refs.queueCapacity),
    field("单任务超时", refs.taskTimeout),
  );
  optimization.append(optimizationGrid);

  if (view === "roles") content.append(roles);
  else if (view === "tasks") content.append(taskRoutes);
  else if (view === "routes") content.append(routes);
  else if (view === "advanced") content.append(basics, advancedRoutes, budget, optimization, rawPolicyPreview(policy));
  else content.append(basics, roles, taskRoutes, routes, advancedRoutes, budget, optimization, rawPolicyPreview(policy));
  return { content, collect: () => collectStructuredPolicy(policy, refs) };
}

function rawPolicyPreview(policy) {
  const advanced = node("details", "policy-advanced-config");
  advanced.append(
    node("summary", "", "高级：查看原始 JSON"),
    node("pre", "policy-json-preview", JSON.stringify(policy, null, 2)),
  );
  return advanced;
}

function collectStructuredPolicy(base, refs) {
  const policy = cloneValue(base);
  policy.name = requiredText(refs.name, "策略名称");
  policy.alias = refs.alias.value.trim();
  policy.default_route = refs.defaultRoute.value;
  if (hasOwn(base, "min_net_savings_bps") || wasTouched(refs.minNetSavings)) {
    policy.min_net_savings_bps = percentToBPS(refs.minNetSavings, "最低净节省");
  }
  if (hasOwn(base, "analyzer_timeout") || wasTouched(refs.analyzerTimeout)) {
    policy.analyzer_timeout = requiredText(refs.analyzerTimeout, "任务分析超时");
  }
  if (hasOwn(base, "analyzer_min_confidence_bps") || wasTouched(refs.analyzerConfidence)) {
    policy.analyzer_min_confidence_bps = percentToBPS(refs.analyzerConfidence, "最低分析置信度");
  }
  if (hasOwn(base, "session_ttl") || wasTouched(refs.sessionTTL)) {
    policy.session_ttl = requiredText(refs.sessionTTL, "Session 绑定有效期");
  }
  policy.session_lock_token_threshold = integerValue(
    refs.sessionLockTokenThreshold,
    "Session 锁定 Token 阈值",
    1,
  );

  policy.roles = { ...(policy.roles || {}) };
  policy.roles.participants = [...refs.participants.entries()]
    .filter(([, control]) => control.checked)
    .map(([modelID]) => modelID);
  policy.roles.strong_baseline_model = refs.strongBaseline.value;
  policy.roles.task_analyzer_model = refs.taskAnalyzer.value;
  if (hasOwn(base.roles || {}, "reviewer_model") || wasTouched(refs.reviewer)) {
    policy.roles.reviewer_model = refs.reviewer.value;
  }
  if (hasOwn(base, "self_escalation") || wasTouched(refs.selfEscalation)) {
    policy.self_escalation = { ...(policy.self_escalation || {}), enabled: refs.selfEscalation.checked };
  }

  policy.task_routes = refs.taskRoutes
    .filter(({ control }) => control.value)
    .map(({ taskType, difficulty, control }) => ({
      task_type: taskType,
      difficulty,
      route: control.value,
    }));
  policy.task_routes.push(...cloneValue(refs.extraTaskRoutes));

  policy.routes = refs.routes.map((routeRef) => {
    const route = cloneValue(routeRef.base);
    route.id = requiredText(routeRef.id, "Route ID");
    route.min_quality_bps = percentToBPS(routeRef.minQuality, `${route.id} 质量下限`);
    route.min_stability_bps = percentToBPS(routeRef.minStability, `${route.id} 稳定性下限`);
    route.max_severe_error_rate_bps = percentToBPS(routeRef.maxError, `${route.id} 严重错误上限`);
    route.weights = { ...(route.weights || {}) };
    route.weights.quality_bps = percentToBPS(routeRef.qualityWeight, `${route.id} 质量权重`);
    route.weights.stability_bps = percentToBPS(routeRef.stabilityWeight, `${route.id} 稳定性权重`);
    route.weights.cost_bps = percentToBPS(routeRef.costWeight, `${route.id} 成本权重`);
    route.weights.performance_bps = percentToBPS(routeRef.performanceWeight, `${route.id} 性能权重`);
    route.candidates = routeRef.candidates.map((candidateRef) => {
      const candidate = cloneValue(candidateRef.base);
      candidate.model = candidateRef.model.value;
      candidate.quality_score_bps = percentToBPS(candidateRef.quality, `${route.id} 候选质量`);
      candidate.stability_score_bps = percentToBPS(candidateRef.stability, `${route.id} 候选稳定性`);
      candidate.severe_error_rate_bps = percentToBPS(candidateRef.error, `${route.id} 候选严重错误率`);
      candidate.expected_latency_ms = integerValue(candidateRef.latency, `${route.id} 候选延迟`, 0);
      candidate.production_eligible = candidateRef.productionEligible.checked;
      return candidate;
    });
    return route;
  });

  policy.budget = { ...(policy.budget || {}) };
  policy.budget.max_answer_attempts = integerValue(refs.maxAnswerAttempts, "回答尝试上限", 1);
  policy.budget.max_auxiliary_calls = integerValue(refs.maxAuxiliaryCalls, "辅助调用上限", 0);
  policy.budget.max_total_outbound_calls = integerValue(refs.maxOutboundCalls, "总上游调用上限", 1);
  policy.budget.max_retries_per_target = integerValue(refs.maxRetries, "单模型重试上限", 0);
  policy.budget.max_model_switches = integerValue(refs.maxSwitches, "模型切换上限", 0);
  policy.budget.deadline = requiredText(refs.deadline, "总截止时间");
  policy.budget.max_worst_case_cost_micro_usd = usdToMicroUSD(refs.maxWorstCaseCost, "最坏成本上限");

  const optimizationControls = [
    refs.optimizationEnabled,
    refs.autoUpdatePolicy,
    refs.sampleRate,
    refs.dailyBudget,
    refs.optimizationReviewer,
    refs.maxConcurrency,
    refs.queueCapacity,
    refs.taskTimeout,
  ];
  if (hasOwn(base, "dynamic_optimization") || wasTouched(...optimizationControls)) {
    if (refs.autoUpdatePolicy.checked && !refs.optimizationEnabled.checked) {
      throw new Error("自动更新线上策略前必须启用异步评测。");
    }
    policy.dynamic_optimization = { ...(policy.dynamic_optimization || {}) };
    policy.dynamic_optimization.enabled = refs.optimizationEnabled.checked;
    policy.dynamic_optimization.auto_update_policy = refs.autoUpdatePolicy.checked;
    policy.dynamic_optimization.sample_rate_bps = percentToBPS(refs.sampleRate, "采样率");
    policy.dynamic_optimization.daily_budget_micro_usd = usdToMicroUSD(refs.dailyBudget, "每日预算");
    policy.dynamic_optimization.reviewer_model = refs.optimizationReviewer.value;
    policy.dynamic_optimization.max_concurrency = integerValue(refs.maxConcurrency, "最大并发", 1);
    policy.dynamic_optimization.queue_capacity = integerValue(refs.queueCapacity, "队列容量", 1);
    policy.dynamic_optimization.task_timeout = requiredText(refs.taskTimeout, "单任务超时");
  }

  return policy;
}

function policySection(title, description) {
  const section = node("section", "policy-editor-section stack");
  section.append(node("h4", "", title));
  if (description) section.append(node("p", "muted policy-section-help", description));
  return section;
}

function textInput(value = "") {
  const input = node("input");
  input.type = "text";
  input.value = String(value ?? "");
  return trackControl(input);
}

function numberInput(value = 0, step = 1, min = 0, max = null) {
  const input = node("input");
  input.type = "number";
  input.value = String(value ?? 0);
  input.step = String(step);
  input.min = String(min);
  if (max !== null) input.max = String(max);
  return trackControl(input);
}

function checkboxInput(checked = false) {
  const input = node("input");
  input.type = "checkbox";
  input.checked = checked;
  return trackControl(input);
}

function selectInput(options, selected) {
  const select = node("select");
  for (const [value, label] of options) {
    const option = node("option", "", label);
    option.value = value;
    option.selected = value === selected;
    select.append(option);
  }
  select.value = selected;
  return trackControl(select);
}

function trackControl(control) {
  control.policyTouched = false;
  const markTouched = () => { control.policyTouched = true; };
  control.addEventListener("input", markTouched);
  control.addEventListener("change", markTouched);
  return control;
}

function wasTouched(...controls) {
  return controls.some((control) => Boolean(control?.policyTouched));
}

function hasOwn(value, key) {
  return Object.prototype.hasOwnProperty.call(value || {}, key);
}

function toggleField(label, control, help = "") {
  const wrapper = node("label", "field policy-toggle-field");
  const line = node("span", "policy-toggle-line");
  line.append(control, node("span", "field-label", label));
  wrapper.append(line);
  if (help) wrapper.append(node("span", "field-help muted", help));
  return wrapper;
}

function availableModelIDs(source, policy) {
  const ids = new Set(
    (source?.model_directory_overview?.models || [])
      .filter((model) => model.status === "available")
      .map((model) => model.model_id),
  );
  const roles = policy.roles || {};
  for (const modelID of roles.participants || []) ids.add(modelID);
  for (const modelID of [roles.strong_baseline_model, roles.task_analyzer_model, roles.reviewer_model]) {
    if (modelID) ids.add(modelID);
  }
  for (const route of policy.routes || []) {
    for (const candidate of route.candidates || []) {
      if (candidate.model) ids.add(candidate.model);
    }
  }
  return [...ids];
}

function cloneValue(value) {
  return JSON.parse(JSON.stringify(value ?? null));
}

function bpsToPercent(value) {
  return Number(value || 0) / 100;
}

function microUSDToUSD(value) {
  return Number(value || 0) / 1_000_000;
}

function requiredText(input, label) {
  const value = String(input.value || "").trim();
  if (!value) throw new Error(`${label}不能为空。`);
  return value;
}

function numberValue(input, label, min = 0, max = null) {
  const value = Number(input.value);
  if (!Number.isFinite(value) || value < min || (max !== null && value > max)) {
    const range = max === null ? `不小于 ${min}` : `介于 ${min} 和 ${max} 之间`;
    throw new Error(`${label}必须${range}。`);
  }
  return value;
}

function integerValue(input, label, min = 0) {
  const value = numberValue(input, label, min);
  if (!Number.isInteger(value)) throw new Error(`${label}必须是整数。`);
  return value;
}

function percentToBPS(input, label) {
  return Math.round(numberValue(input, label, 0, 100) * 100);
}

function usdToMicroUSD(input, label) {
  return Math.round(numberValue(input, label, 0) * 1_000_000);
}

function optionalIntegerValue(input, label, min = 1) {
  if (String(input.value ?? "").trim() === "") return undefined;
  const value = Number(input.value);
  if (!Number.isSafeInteger(value) || value < min) {
    throw new Error(`${label}必须是${min > 0 ? "正" : "非负"}整数。`);
  }
  return value;
}

function optionalCapabilityUSDToMicroUSD(input, label) {
  const value = optionalUSDToMicroUSD(input, label);
  return value === null ? undefined : value;
}

function optionalMicroUSDToUSD(value) {
  if (value === null || value === undefined || value === "") return "";
  return Number(value) / 1_000_000;
}

function booleanCapabilityInput(value, required = false) {
  return selectInput(
    [
      ...(required ? [] : [["", "未知"]]),
      ["true", "是"],
      ["false", "否"],
    ],
    value === true ? "true" : value === false ? "false" : "",
  );
}

function modelCapabilityForm(modelID, capability, disabled = false) {
  const original = cloneValue(capability || {});
  const container = node("div", "stack model-capability-form");
  const core = node("div", "model-capability-form-grid");
  const canonicalModelID = textInput(original.canonical_model_id || "");
  canonicalModelID.placeholder = "例如 deepseek/deepseek-v4-flash";
  const contextWindow = numberInput(original.context_window ?? "", 1, 1);
  contextWindow.placeholder = "例如 1000000";
  const maxOutputTokens = numberInput(original.max_output_tokens ?? "", 1, 1);
  maxOutputTokens.placeholder = "例如 128000";
  const supportsVision = booleanCapabilityInput(original.supports_vision, true);
  const supportsTools = booleanCapabilityInput(original.supports_tools);
  const supportsAgentWorkflow = booleanCapabilityInput(original.supports_agent_workflow);
  const supportsStructuredOutput = booleanCapabilityInput(original.supports_structured_output);
  const controls = [
    canonicalModelID,
    contextWindow,
    maxOutputTokens,
    supportsVision,
    supportsTools,
    supportsAgentWorkflow,
    supportsStructuredOutput,
  ];
  core.append(
    field("标准模型 ID", canonicalModelID, "用于关联公开模型目录；格式为 provider/model。"),
    field("上下文窗口", contextWindow, "模型可接收的最大 Token 数。"),
    field("最大输出 Token", maxOutputTokens, "必须小于上下文窗口。"),
    field("支持视觉", supportsVision),
    field("支持工具调用", supportsTools),
	field(
	  "支持 Agent 工作流",
	  supportsAgentWorkflow,
	  "表示可完成 Claude Code/Codex 长上下文与多轮工具工作流；提示词是否外泄不再作为自动切换条件。未知模型不会接收 Agent 的 Auto 请求。",
	),
    field("支持结构化输出", supportsStructuredOutput),
  );

  const pricing = node("details", "model-capability-pricing");
  pricing.append(node("summary", "", "价格与缓存"));
  const pricingGrid = node("div", "model-capability-form-grid");
  const inputPrice = numberInput(optionalMicroUSDToUSD(original.input_price_micro_usd_per_million), 0.000001, 0);
  const outputPrice = numberInput(optionalMicroUSDToUSD(original.output_price_micro_usd_per_million), 0.000001, 0);
  const cacheReadPrice = numberInput(optionalMicroUSDToUSD(original.cache_read_price_micro_usd_per_million), 0.000001, 0);
  const cacheWritePrice = numberInput(optionalMicroUSDToUSD(original.cache_write_price_micro_usd_per_million), 0.000001, 0);
  controls.push(inputPrice, outputPrice, cacheReadPrice, cacheWritePrice);
  pricingGrid.append(
    field("输入价格（美元/百万 Token）", inputPrice),
    field("输出价格（美元/百万 Token）", outputPrice),
    field("缓存读取价格（美元/百万 Token）", cacheReadPrice),
    field("缓存写入价格（美元/百万 Token）", cacheWritePrice),
  );
  pricing.append(pricingGrid);
  for (const control of controls) control.disabled = disabled;
  container.append(core, pricing);

  return {
    element: container,
    collect(currentModelID = modelID) {
      const result = { ...original, id: currentModelID };
      setOptionalText(result, "canonical_model_id", canonicalModelID.value);
      setOptionalValue(result, "context_window", optionalIntegerValue(contextWindow, "上下文窗口"));
      setOptionalValue(result, "max_output_tokens", optionalIntegerValue(maxOutputTokens, "最大输出 Token"));
      if (supportsVision.value === "") throw new Error("请选择是否支持视觉。");
      result.supports_vision = supportsVision.value === "true";
      setOptionalValue(result, "supports_tools", optionalBooleanValue(supportsTools.value));
      setOptionalValue(result, "supports_agent_workflow", optionalBooleanValue(supportsAgentWorkflow.value));
      setOptionalValue(result, "supports_structured_output", optionalBooleanValue(supportsStructuredOutput.value));
      setOptionalValue(result, "input_price_micro_usd_per_million", optionalCapabilityUSDToMicroUSD(inputPrice, "输入价格"));
      setOptionalValue(result, "output_price_micro_usd_per_million", optionalCapabilityUSDToMicroUSD(outputPrice, "输出价格"));
      setOptionalValue(result, "cache_read_price_micro_usd_per_million", optionalCapabilityUSDToMicroUSD(cacheReadPrice, "缓存读取价格"));
      setOptionalValue(result, "cache_write_price_micro_usd_per_million", optionalCapabilityUSDToMicroUSD(cacheWritePrice, "缓存写入价格"));
      return result;
    },
  };
}

function optionalBooleanValue(value) {
  if (value === "") return undefined;
  return value === "true";
}

function setOptionalText(target, key, value) {
  const normalized = String(value || "").trim();
  if (normalized) target[key] = normalized;
  else delete target[key];
}

function setOptionalValue(target, key, value) {
  if (value === undefined) delete target[key];
  else target[key] = value;
}

function renderBatchModelImporter(models, state, actions) {
  const section = node("section", "card stack model-batch-card model-directory-batch");
  section.append(
    node("h3", "", "批量录入模型"),
    node("p", "muted", "一行一个模型 ID，最多 100 个。系统会跳过已存在和重复项，并为可靠匹配的模型自动填充能力参数。"),
  );
  const input = node("textarea");
  input.name = "model_batch_ids";
  input.rows = 6;
  input.placeholder = "例如：\nazure-ds-v4-flash\nclaude-glm-5.2";
  const reason = node("input");
  reason.type = "text";
  reason.placeholder = "例如：更新可用模型目录";
  const alert = node("div", "model-batch-message");
  alert.setAttribute("role", "status");
  alert.hidden = true;
  const previewRoot = node("div", "stack model-batch-preview");
  const controls = node("div", "cluster");
  const preview = button("预览批量导入", "button-secondary");
  const confirm = button("确认导入", "button");
  confirm.hidden = true;
  let editors = [];

  preview.addEventListener("click", () => {
    alert.hidden = true;
    confirm.hidden = true;
    editors = [];
    previewRoot.replaceChildren();
    try {
      const existing = models.map((model) => model.capability || { id: model.model_id });
      const rows = previewBatchModels(input.value, existing);
      const additions = importBatchModels(rows);
      if (additions.length === 0) {
        showMessage(alert, "没有可导入的新模型。", true);
        return;
      }
      const additionByID = new Map(additions.map((capability) => [capability.id, capability]));
      for (const row of rows) {
        const skipped = ["duplicate", "existing"].includes(row.status);
        const item = node(skipped ? "article" : "details", "model-batch-preview-row");
        const heading = node(skipped ? "div" : "summary", "cluster section-heading");
        heading.append(
          node("strong", "", row.id),
          node("span", `status-pill model-batch-status-${row.status}`, batchStatusLabel(row.status)),
        );
        item.append(heading);
        if (skipped) {
          item.append(node("p", "muted", row.status === "existing" ? "模型目录中已存在，将跳过。" : "同一批次中重复，将跳过。"));
          previewRoot.append(item);
          continue;
        }
        if (row.status === "ready") {
          item.append(node("p", "muted", "已唯一匹配模型模板，请确认自动填充的能力参数。"));
        } else if (row.candidates?.length) {
          item.append(node("p", "warning-banner", `存在多个可能模板：${row.candidates.map((candidate) => candidate.entry?.canonicalId || candidate.entry?.id).filter(Boolean).join("、")}。系统不会猜测，请手动补充参数。`));
        } else {
          item.append(node("p", "warning-banner", "没有可靠模板，请手动补充能力参数。"));
        }
        const editor = modelCapabilityForm(row.id, additionByID.get(row.id) || { id: row.id });
        editors.push(editor);
        item.append(editor.element);
        previewRoot.append(item);
      }
      confirm.textContent = `确认导入 ${editors.length} 个模型`;
      confirm.hidden = false;
      showMessage(alert, `已生成 ${editors.length} 个模型的导入预览，请确认参数。`);
    } catch (error) {
      showMessage(alert, error?.message || "无法解析模型列表。", true);
    }
  });

  confirm.addEventListener("click", () => run(alert, async () => {
    const capabilities = editors.map((editor) => editor.collect());
    if (!capabilities.length) throw new Error("请先生成批量导入预览。");
    await actions.addProfileModels?.(
      Number(state.revision),
      capabilities,
      reason.value.trim(),
    );
  }));
  controls.append(preview, confirm);
  section.append(
    field("模型 ID 列表", input, "只接受精确模型 ID；预览不会修改线上目录。"),
    field("导入原因", reason),
    alert,
    previewRoot,
    controls,
  );
  return section;
}

function batchStatusLabel(status) {
  return {
    ready: "已匹配",
    choose: "需要确认",
    unknown: "待补充",
    existing: "已存在",
    duplicate: "重复",
  }[status] || status;
}

export function renderProfileModelDirectory(source, actions = {}) {
  const overview = source?.model_directory_overview || {};
  const state = overview.runtime_state || {};
  const models = overview.models || [];
  const usageByModel = policyUsages(source?.routing_policy_overview?.active?.policy);
  const section = node("section", "stack profile-model-directory");
  const header = node("div", "cluster section-heading");
  header.append(node("h2", "", "Profile 模型目录"), node("span", "status-pill status-ready", `修订 #${state.model_catalog_revision || 0}`));
  const notice = node(
    "p",
    "info-banner",
    "available 可参与新请求；offline 与 retired 会拒绝显式请求。retired 是永久保留的墓碑，同一模型 ID 不能重新添加。",
  );
  const message = node("div", "success-banner");
  message.hidden = true;
  const list = node("div", "stack model-directory-list");
  const batch = renderBatchModelImporter(models, state, actions);
  const retired = node("details", "card stack retired-models");
  retired.append(node("summary", "", `已移除模型（${models.filter((model) => model.status === "retired").length}）`));
  for (const model of models) {
    const card = node("article", "card stack model-directory-card");
    const cardHeader = node("div", "cluster section-heading");
    cardHeader.append(
      node("h3", "", model.model_id),
      node("span", `status-pill model-status-${model.status}`, modelStatusLabel(model.status)),
    );
    const capability = modelCapabilityForm(
      model.model_id,
      model.capability,
      model.status === "retired",
    );
    const editor = node("details", "model-capability-details");
    editor.append(
      node("summary", "", model.status === "retired" ? "查看能力配置" : "编辑能力配置"),
      capability.element,
    );
    const reason = node("input");
    reason.type = "text";
    reason.placeholder = "变更原因";
    const controls = node("div", "cluster");
    const update = button("保存能力配置");
    update.disabled = model.status === "retired";
    update.addEventListener("click", () => run(message, () =>
      actions.updateProfileModel?.(
        Number(state.revision), model.model_id, capability.collect(), reason.value.trim(),
      )));
    controls.append(update);
    if (model.status === "available") {
      const offline = button("紧急下线", "button-danger");
      offline.addEventListener("click", () => openOfflineDialog(
        section, model, state, actions, message,
      ));
      const retire = button("退役", "button-danger");
      retire.addEventListener("click", () => run(message, () =>
        actions.retireProfileModel?.(Number(state.revision), model.model_id, reason.value.trim())));
      controls.append(offline, retire);
    } else if (model.status === "offline") {
      const restore = button("恢复 available", "button");
      restore.addEventListener("click", () => run(message, () =>
        actions.restoreProfileModel?.(Number(state.revision), model.model_id, reason.value.trim())));
      const retire = button("退役", "button-danger");
      retire.addEventListener("click", () => run(message, () =>
        actions.retireProfileModel?.(Number(state.revision), model.model_id, reason.value.trim())));
      controls.append(restore, retire);
    }
    const usages = usageByModel.get(model.model_id) || [];
    card.append(
      cardHeader,
      node("p", usages.length ? "model-usage" : "muted", usages.length ? `当前线上用途：${usages.join("、")}` : "当前线上 Policy 未使用"),
      node("p", "muted", model.status_reason || "无状态说明"),
      editor,
      field("变更原因", reason, "保存能力、下线、恢复或退役前填写。"),
      controls,
    );
    if (model.status === "retired") retired.append(card);
    else list.append(card);
  }

  const add = node("section", "card stack");
  add.append(node("h3", "", "添加模型"));
  const modelID = node("input");
  modelID.placeholder = "模型 ID";
  const capability = modelCapabilityForm("", {
    id: "",
    context_window: 200000,
    max_output_tokens: 32000,
    supports_vision: false,
  });
  const reason = node("input");
  reason.placeholder = "添加原因";
  const submit = button("添加并立即生效", "button");
  submit.addEventListener("click", () => run(message, async () => {
    const parsed = capability.collect(requiredText(modelID, "模型 ID"));
    await actions.addProfileModel?.(Number(state.revision), parsed.id, parsed, reason.value.trim());
  }));
  add.append(
    field("模型 ID", modelID, "请求中的模型名称，创建后不可修改。"),
    capability.element,
    field("添加原因", reason),
    submit,
  );
  section.append(header, notice, message, batch, list);
  if (retired.children.length > 1) section.append(retired);
  section.append(add);
  return section;
}

function openOfflineDialog(root, model, state, actions, message) {
  root.querySelector(".model-offline-dialog")?.remove();
  const dialog = node("section", "card stack model-offline-dialog");
  dialog.append(
    node("h3", "", `紧急下线 ${model.model_id}`),
    node("p", "muted", "系统会先推导并验证替代策略；任何 Route 为空或关键角色无替代时，整次操作不会写入。"),
  );
  const inputs = {};
  for (const [key, label] of [
    ["participant", "补充参与模型"],
    ["strong_baseline", "强模型替代"],
    ["task_analyzer", "分析模型替代"],
    ["reviewer", "评审模型替代"],
    ["vision", "视觉模型替代"],
  ]) {
    inputs[key] = node("input");
    inputs[key].placeholder = "不需要时留空";
    dialog.append(field(label, inputs[key]));
  }
  const visionAction = node("select");
  for (const [value, label] of [["", "不涉及视觉"], ["replace", "替换视觉模型"], ["disable", "关闭视觉增强"]]) {
    const option = node("option", "", label);
    option.value = value;
    visionAction.append(option);
  }
  const reason = node("input");
  reason.placeholder = "必填，例如：上游故障";
  const controls = node("div", "cluster");
  const confirm = button("验证并立即下线", "button-danger");
  const cancel = button("取消");
  cancel.addEventListener("click", () => dialog.remove());
  confirm.addEventListener("click", () => run(message, () => actions.offlineProfileModel?.({
    expected_runtime_revision: Number(state.revision),
    model_id: model.model_id,
    replacements: Object.fromEntries(
      Object.entries(inputs).map(([key, input]) => [key, input.value.trim()]).filter(([, value]) => value),
    ),
    vision_action: visionAction.value,
    reason: reason.value.trim(),
  })));
  controls.append(confirm, cancel);
  dialog.append(field("视觉处理", visionAction), field("下线原因", reason), controls);
  root.prepend(dialog);
}

function appendFact(list, term, description) {
  const wrapper = node("div");
  wrapper.append(node("dt", "", term), node("dd", "", description));
  list.append(wrapper);
}

function modelStatusLabel(status) {
  return { available: "可用", offline: "已紧急下线", retired: "已移除" }[status] || status;
}

function policyUsages(policy) {
  const usages = new Map();
  const add = (model, usage) => {
    if (!model) return;
    const current = usages.get(model) || [];
    if (!current.includes(usage)) current.push(usage);
    usages.set(model, current);
  };
  const roles = policy?.roles || {};
  for (const model of roles.participants || []) add(model, "参与模型");
  add(roles.strong_baseline_model, "困难任务基线");
  add(roles.task_analyzer_model, "任务分析");
  add(roles.reviewer_model, "质量评审");
  for (const route of policy?.routes || []) {
    for (const candidate of route.candidates || []) add(candidate.model, `Route ${route.id}`);
  }
  return usages;
}

function changeKindLabel(kind, actor = "") {
  if (actor === "system:policy-reconciler") return "评测自动校准";
  return {
    migration: "迁移",
    apply: "保存生效",
    rollback: "回滚副本",
    emergency_offline: "紧急下线",
  }[kind] || kind;
}

function automaticUpdateStatusLabel(policy, status = {}) {
  if (!policy?.dynamic_optimization?.auto_update_policy) return "未启用";
  if (status.last_outcome === "waiting_confirmation") return "等待第 2 次一致证据";
  if (status.last_outcome === "cooldown") return "冷却中，证据会继续保留";
  if (status.dirty) return "已收到新证据，等待校准";
  if (status.last_outcome === "applied") return "已按可靠证据自动更新";
  if (status.last_outcome === "no_change") return "已检查，暂无显著变化";
  if (["load_failed", "calibration_failed", "apply_failed"].includes(status.last_outcome)) {
    return "上次校准失败，将自动重试";
  }
  return "已启用，等待可靠证据";
}
