import { profileSectionHref } from "./routes.js";

export function profileReadiness(draft, { saved = false } = {}) {
  const config = draft?.config || {};
  const models = config.models || [];
  const auto = config.auto_routing || {};
  const vision = config.vision || {};
  const profileId = Number(draft?.id || 0);
  const sectionHref = (section) =>
    profileId > 0 ? profileSectionHref(profileId, section) : "/_admin/setup";

  const connectionReasons = [];
  if (!String(draft?.slug ?? "").trim()) {
    connectionReasons.push("请设置 Profile Slug。");
  }
  if (!String(config.upstream ?? "").trim()) {
    connectionReasons.push("请设置上游地址。");
  }
  const connection = connectionReasons.length === 0 && saved
    ? state("ready")
    : state("blocked", connectionReasons.length > 0
      ? connectionReasons
      : ["请先保存连接配置。"], "配置连接", sectionHref("connection"));

  const modelSection = models.length > 0
    ? state("ready")
    : state("attention", ["模型是选填项；智能路由依赖已录入的模型。"],
      "录入模型", sectionHref("models"));

  const routing = routingReadiness(auto, models, sectionHref);
  const visionSection = visionReadiness(vision, auto, models, sectionHref);
  const reliability = state("ready");
  const agents = saved
    ? state("ready")
    : state("blocked", ["请先保存 Profile，再生成 Agent 配置。"],
      "配置连接", sectionHref("connection"));

  const sections = {
    overview: state(
      connection.state === "ready" ? "ready" : "blocked",
      connection.reasons,
      connection.action?.label,
      connection.action?.href,
    ),
    connection,
    models: modelSection,
    routing,
    vision: visionSection,
    reliability,
    agents,
  };
  const blockers = Object.entries(sections)
    .filter(([, value]) => value.state === "blocked")
    .map(([section, value]) => ({ section, ...value }));
  return {
    state: connection.state === "ready" ? "ready" : "blocked",
    sections,
    blockers,
  };
}

export function modelReferences(draft, modelID) {
  const id = String(modelID ?? "");
  if (!id) {
    return [];
  }
  const config = draft?.config || {};
  const auto = config.auto_routing || {};
  const strategy = auto.strategy || {};
  const references = [];
  const add = (section, label, path, value) => {
    if (String(value ?? "") === id) {
      references.push({ section, label, path });
    }
  };

  add(
    "routing",
    "强模型基线",
    "config.auto_routing.strong_baseline_model",
    auto.strong_baseline_model,
  );
  add(
    "routing",
    "任务分析模型",
    "config.auto_routing.task_analyzer_model",
    auto.task_analyzer_model,
  );
  add(
    "routing",
    "仲裁模型",
    "config.auto_routing.dynamic_optimization.reviewer_model",
    auto.dynamic_optimization?.reviewer_model,
  );
  add("vision", "视觉模型", "config.vision.model", config.vision?.model);
  (auto.participants || []).forEach((value, index) => add(
    "routing",
    "参与模型",
    `config.auto_routing.participants[${index}]`,
    value,
  ));
  (strategy.routes || []).forEach((route, routeIndex) => {
    (route.candidates || []).forEach((candidate, candidateIndex) => add(
      "routing",
      `Route ${route.id || routeIndex + 1}`,
      `config.auto_routing.strategy.routes[${routeIndex}].candidates[${candidateIndex}].model`,
      candidate.model,
    ));
  });
  (config.targets || []).forEach((target, targetIndex) => {
    (target.models || []).forEach((value, modelIndex) => add(
      "reliability",
      `Target ${target.id || targetIndex + 1}`,
      `config.targets[${targetIndex}].models[${modelIndex}]`,
      value,
    ));
  });
  return references;
}

function routingReadiness(auto, models, sectionHref) {
  if (models.length === 0) {
    return state(
      "blocked",
      ["请先录入模型，才能启用智能路由。"],
      "录入模型",
      sectionHref("models"),
    );
  }
  if (!auto.enabled) {
    const missingPrices = models
      .filter((model) => !hasPrices(model))
      .map((model) => String(model.id || "未命名"));
    if (missingPrices.length > 0) {
      return state(
        "attention",
        [`模型 ${missingPrices.join("、")} 缺少输入或输出价格；启用智能路由前需要补全。`],
        "完善模型信息",
        sectionHref("models"),
      );
    }
    return state("disabled", ["智能路由尚未启用。"]);
  }

  const catalog = new Map(models.map((model) => [String(model.id), model]));
  const reasons = [];
  if ((auto.participants || []).length === 0) {
    reasons.push("请选择至少一个参与路由的模型。");
  }
  if (!auto.strong_baseline_model) {
    reasons.push("请选择强模型基线。");
  }
  if (!auto.task_analyzer_model) {
    reasons.push("请选择任务分析模型。");
  }
  for (const id of [
    ...(auto.participants || []),
    auto.strong_baseline_model,
    auto.task_analyzer_model,
  ].filter(Boolean)) {
    const model = catalog.get(String(id));
    if (!model) {
      reasons.push(`模型 ${id} 尚未录入。`);
      continue;
    }
    if (!hasPrices(model)) {
      reasons.push(`模型 ${id} 缺少输入或输出价格。`);
    }
  }
  if (
    auto.strong_baseline_model &&
    !(auto.participants || []).includes(auto.strong_baseline_model)
  ) {
    reasons.push("强模型基线必须同时是参与模型。");
  }
  return reasons.length === 0
    ? state("ready")
    : state("blocked", unique(reasons), "完善模型信息", sectionHref("models"));
}

function visionReadiness(vision, auto, models, sectionHref) {
  if (!vision.enabled) {
    return state("disabled", ["视觉增强尚未启用。"]);
  }
  if (!String(vision.model ?? "").trim()) {
    return state("blocked", ["请选择视觉模型。"]);
  }
  if (!auto.enabled) {
    return state("ready");
  }

  const model = models.find((item) => item.id === vision.model);
  const reasons = [];
  if (!model) {
    reasons.push("启用智能路由时，视觉模型必须先录入模型目录。");
  } else {
    if (model.supports_vision !== true) {
      reasons.push("视觉模型必须明确支持视觉。");
    }
    if (!hasPrices(model)) {
      reasons.push("视觉模型必须填写输入和输出价格。");
    }
    if (!positiveInteger(model.context_window)) {
      reasons.push("视觉模型必须填写上下文窗口。");
    }
    if (!positiveInteger(model.max_output_tokens)) {
      reasons.push("视觉模型必须填写最大输出 Token。");
    }
  }
  return reasons.length === 0
    ? state("ready")
    : state("blocked", reasons, "完善模型信息", sectionHref("models"));
}

function hasPrices(model) {
  return nonnegativeInteger(model.input_price_micro_usd_per_million) &&
    nonnegativeInteger(model.output_price_micro_usd_per_million);
}

function positiveInteger(value) {
  return Number.isSafeInteger(Number(value)) && Number(value) > 0;
}

function nonnegativeInteger(value) {
  return value !== "" && value !== null && value !== undefined &&
    Number.isSafeInteger(Number(value)) && Number(value) >= 0;
}

function unique(values) {
  return [...new Set(values)];
}

function state(name, reasons = [], label, href) {
  const result = { state: name, reasons: [...reasons] };
  if (label && href) {
    result.action = { label, href };
  }
  return result;
}
