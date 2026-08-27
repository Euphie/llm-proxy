import { openConfigurationGenerator } from "./generator.js";
import { evaluationCatalogCoverage } from "./evaluation-catalog.js";
import {
  currentModelCatalog,
  matchModelSuggestions,
  parseTokenLimit,
} from "./model-catalog.js";
import {
  formatMicroUSD,
  microUSDToUSDInput,
  parseUSDToMicroUSD,
} from "./money.js";
import {
  openModelReferenceDialog,
  validateModelMutation,
} from "./profile-models.js";
import {
  importBatchModels,
  previewBatchModels,
} from "./profile-model-batch.js";
import { INTELLIGENT_ROUTING_HELP_HREF } from "./routes.js";
import { profileIconVisual } from "./visual.js";

const defaultVision = {
  enabled: false,
  transport: "anthropic_messages",
  model: "sonnet",
  unlisted_model_policy: "bypass",
  max_tokens: 2048,
  timeout: "2m",
  max_concurrency: 4,
  cache_ttl: "30m",
  cache_max_entries: 512,
  prompt: "",
};

const visionTransports = {
  anthropic: [["anthropic_messages", "Messages"]],
  openai: [
    ["openai_chat_completions", "Chat Completions"],
    ["openai_responses", "Responses"],
  ],
};

function defaultVisionTransport(protocol) {
  return protocol === "openai"
    ? "openai_chat_completions"
    : "anthropic_messages";
}

function normalizedVisionTransport(protocol, transport) {
  const value = String(transport ?? "");
  const choices = visionTransports[protocol] || [];
  return choices.some(([candidate]) => candidate === value)
    ? value
    : defaultVisionTransport(protocol);
}

const defaultRule = {
  status: 429,
  body_contains: "",
  max_retries: 3,
  delay: "1s",
  jitter: "0s",
};

const routingTaskLabels = {
	simple: "简单请求",
	general: "通用",
	reasoning: "推理",
	math: "数学",
	coding: "编程",
	tool_use: "工具调用",
	vision: "视觉",
};

const routingDifficultyLabels = {
	easy: "简单",
	medium: "中等",
	hard: "困难",
};

function routingGroupLabel(route) {
	switch (String(route || "")) {
	case "general":
		return "常规模型组（general）";
	case "strong":
		return "高能力模型组（strong）";
	default:
		return String(route || "未设置");
	}
}

function strategyModelIDs(config) {
	const roleModels = (config?.roles?.participants || []).map(String).filter(Boolean);
	if (roleModels.length > 0) {
		return [...new Set(roleModels)];
	}
	return [...new Set((config?.routes || []).flatMap((route) =>
		(route.candidates || []).map((candidate) => String(candidate?.model || ""))
	).filter(Boolean))];
}

function strategyTaskSummary(config) {
	const groups = strategyTaskRouteGroups(config?.task_routes || []);
	if (groups.length === 0) {
		return `所有任务 → ${routingGroupLabel(config?.default_route)}`;
	}
	return groups.map((group) => {
		const count = group.taskTypes.filter(Boolean).length;
		const difficulty = group.difficulties
			.filter(Boolean)
			.map((value) => routingDifficultyLabels[value] || value)
			.join("、");
		const scope = `${count || "所有"} 类任务${difficulty ? `（${difficulty}）` : ""}`;
		return `${scope} → ${routingGroupLabel(group.route)}`;
	}).join("；");
}

function strategyTaskRouteGroups(taskRoutes) {
	const routesByDifficulty = new Map();
	for (const item of taskRoutes) {
		const route = String(item?.route || "未设置");
		const difficulty = String(item?.difficulty || "");
		const taskType = String(item?.task_type || "");
		const key = `${difficulty}\u0000${route}`;
		if (!routesByDifficulty.has(key)) {
			routesByDifficulty.set(key, { difficulty, route, taskTypes: new Set() });
		}
		routesByDifficulty.get(key).taskTypes.add(taskType);
	}

	const merged = new Map();
	for (const group of routesByDifficulty.values()) {
		const taskTypes = [...group.taskTypes].sort();
		const key = `${group.route}\u0000${taskTypes.join("\u0000")}`;
		if (!merged.has(key)) {
			merged.set(key, { route: group.route, difficulties: [], taskTypes });
		}
		merged.get(key).difficulties.push(group.difficulty);
	}

	const difficultyOrder = new Map([["easy", 0], ["medium", 1], ["hard", 2], ["", 3]]);
	return [...merged.values()].map((group) => ({
		...group,
		difficulties: group.difficulties.sort((left, right) =>
			(difficultyOrder.get(left) ?? 4) - (difficultyOrder.get(right) ?? 4)
		),
	}));
}

function newDefaultAutoRouting() {
  return {
    enabled: false,
    participants: [],
    strong_baseline_model: "",
    task_analyzer_model: "",
    analyzer_timeout: "15s",
    analyzer_min_confidence_bps: 7000,
    session_ttl: "24h",
    session_lock_token_threshold: 100000,
    self_escalation: { enabled: true },
    dynamic_optimization: {
      enabled: false,
			auto_update_policy: false,
			sample_rate_bps: 1000,
			daily_budget_micro_usd: 250000,
			reviewer_model: "",
			max_concurrency: 2,
			queue_capacity: 128,
			task_timeout: "90s",
    },
    strategy: {
      name: defaultStrategyName(),
      alias: "",
      default_route: "default",
		min_net_savings_bps: 0,
		latency_target_ms: 0,
      task_routes: [],
      routes: [newDefaultRoute()],
      budget: {
        max_answer_attempts: 2,
        max_auxiliary_calls: 2,
        max_total_outbound_calls: 5,
        max_retries_per_target: 1,
        max_model_switches: 1,
        deadline: "2m",
        max_worst_case_cost_micro_usd: 500000,
      },
    },
  };
}

function newDefaultRoute() {
  return {
    id: "default",
    min_quality_bps: 9000,
		min_stability_bps: 8000,
    max_severe_error_rate_bps: 100,
		weights: {
			quality_bps: 4000,
			stability_bps: 2500,
			cost_bps: 2500,
			performance_bps: 1000,
		},
    candidates: [],
  };
}

function defaultStrategyName(now = new Date()) {
  const year = String(now.getFullYear()).padStart(4, "0");
  const month = String(now.getMonth() + 1).padStart(2, "0");
  const day = String(now.getDate()).padStart(2, "0");
  return `${year}${month}${day}-001`;
}

export function defaultProfileDraft(protocol = "anthropic") {
  return {
    id: 0,
    original_slug: "",
    slug: "",
    display_name: "",
    enabled: true,
    make_default: false,
    config: {
      version: 2,
      protocol,
      upstream: "",
      models: [],
      auto_routing: newDefaultAutoRouting(),
      vision: {
        ...defaultVision,
        transport: defaultVisionTransport(protocol),
      },
      overload_rules: [],
    },
  };
}

export function profilePayload(draft) {
  const config = draft.config || {};
  const protocol = String(draft.protocol ?? config.protocol ?? "anthropic");
  const vision = draft.vision || config.vision || {};
  const models = draft.models || config.models || [];
  const autoRouting = draft.auto_routing || config.auto_routing ||
    newDefaultAutoRouting();
  const rules = draft.overload_rules || config.overload_rules || [];
  const unlistedModelPolicy = String(
    vision.unlisted_model_policy || "bypass",
  );
  if (!["bypass", "enhance"].includes(unlistedModelPolicy)) {
    throw new Error("未收录模型策略无效。");
  }

  const modelPayload = modelCapabilitiesPayload(models);
  const autoPayload = autoRoutingPayload(autoRouting);
  validateAutoRoutingConfiguration(autoPayload, modelPayload, vision);

  return {
    slug: String(draft.slug ?? ""),
    display_name: String(draft.display_name ?? ""),
    enabled: Boolean(draft.enabled),
    make_default: Boolean(draft.make_default),
    config: {
      version: Number(draft.version ?? config.version ?? 1),
      protocol,
      upstream: String(draft.upstream ?? config.upstream ?? ""),
      models: modelPayload,
      auto_routing: autoPayload,
      vision: {
        enabled: Boolean(vision.enabled),
        transport: normalizedVisionTransport(protocol, vision.transport),
        model: String(vision.model ?? ""),
        unlisted_model_policy: unlistedModelPolicy,
        max_tokens: Number(vision.max_tokens),
        timeout: String(vision.timeout ?? ""),
        max_concurrency: Number(vision.max_concurrency),
        cache_ttl: String(vision.cache_ttl ?? ""),
        cache_max_entries: Number(vision.cache_max_entries),
        prompt: String(vision.prompt ?? ""),
      },
      overload_rules: rules.map((rule) => ({
        status: retryableStatus(rule.status),
        body_contains: String(rule.body_contains ?? ""),
        max_retries: Number(rule.max_retries),
        delay: String(rule.delay ?? ""),
        jitter: String(rule.jitter ?? ""),
      })),
    },
  };
}

function retryableStatus(value) {
  const status = Number(value);
  if (
    !Number.isInteger(status) ||
    !(status === 408 || status === 425 || status === 429 || (status >= 500 && status <= 599))
  ) {
    throw new Error("重试状态码必须是 408、425、429 或 500–599。");
  }
  return status;
}

export function addModelCapability(models) {
  return [
    ...models,
    {
      id: "",
      context_window: "",
      max_output_tokens: "",
      supports_vision: "",
      supports_tools: "",
      supports_agent_workflow: "",
      supports_structured_output: "",
      input_price_micro_usd_per_million: "",
      output_price_micro_usd_per_million: "",
      cache_read_price_micro_usd_per_million: "",
      cache_write_price_micro_usd_per_million: "",
    },
  ];
}

export function removeModelCapability(models, index) {
  return models.filter((_, current) => current !== index);
}

export function addRetryRule(rules) {
  return [...rules, { ...defaultRule }];
}

export function removeRetryRule(rules, index) {
  return rules.filter((_, current) => current !== index);
}

export function moveRetryRule(rules, index, offset) {
  const destination = index + offset;
  if (
    index < 0 ||
    index >= rules.length ||
    destination < 0 ||
    destination >= rules.length
  ) {
    return [...rules];
  }
  const moved = [...rules];
  [moved[index], moved[destination]] = [moved[destination], moved[index]];
  return moved;
}

export function profileDraft(profile, defaultProfileID = 0) {
  const protocol = String(profile?.config?.protocol ?? "anthropic");
  const draft = defaultProfileDraft(protocol);
  const configuredVision = profile?.config?.vision || {};
  return {
    ...draft,
    id: Number(profile?.id ?? 0),
    original_slug: String(profile?.slug ?? ""),
    slug: String(profile?.slug ?? ""),
    display_name: String(profile?.display_name ?? ""),
    enabled: Boolean(profile?.enabled),
    make_default: Number(profile?.id ?? 0) === Number(defaultProfileID),
    config: {
      version: Number(profile?.config?.version ?? draft.config.version),
      protocol: String(
        profile?.config?.protocol ?? draft.config.protocol,
      ),
      upstream: String(profile?.config?.upstream ?? ""),
      models: (profile?.config?.models || []).map((model) => ({
        ...model,
        id: String(model?.id ?? ""),
        ...(model?.canonical_model_id
          ? { canonical_model_id: String(model.canonical_model_id) }
          : {}),
        supports_vision:
          typeof model?.supports_vision === "boolean"
            ? model.supports_vision
            : "",
      })),
      auto_routing: autoRoutingDraft(profile?.config?.auto_routing),
      vision: {
        ...defaultVision,
        ...configuredVision,
        transport: normalizedVisionTransport(
          protocol,
          configuredVision.transport,
        ),
        unlisted_model_policy:
          profile?.config?.vision?.unlisted_model_policy || "bypass",
      },
      overload_rules: (profile?.config?.overload_rules || []).map((rule) => ({
        status: Number(rule.status),
        body_contains: String(rule.body_contains ?? ""),
        max_retries: Number(rule.max_retries),
        delay: String(rule.delay ?? ""),
        jitter: String(rule.jitter ?? ""),
      })),
    },
  };
}

export function renderProfileList(root, data, actions = {}) {
  const profiles = data?.profiles || [];
  const defaultProfileID = Number(data?.default_profile_id ?? 0);

  if (profiles.length === 0) {
    const empty = element("section", "card empty-state");
    const heading = textElement("h2", "创建第一个 Profile");
    const explanation = textElement(
      "p",
      "创建并启用 Profile 前，代理请求仍不可用。",
    );
    explanation.className = "muted";
    const create = actionButton("创建第一个 Profile", "button");
    create.addEventListener("click", () => actions.create?.());
    empty.append(heading, explanation, create);
    root.replaceChildren(empty);
    return;
  }

  const page = element("div", "stack");
  const heading = element("div", "page-heading cluster");
  const description = textElement(
    "p",
    "管理协议、上游地址、视觉增强和容错规则。",
  );
  description.className = "muted";
  const create = actionButton("新建 Profile", "button");
  create.addEventListener("click", () => actions.create?.());
  heading.append(description, create);

  const alert = element("div", "error-banner");
  alert.setAttribute("role", "alert");
  alert.hidden = true;

  const group = element("div", "profile-settings-group");
  for (const profile of profiles) {
    const card = element("article", "profile-settings-row");
    const header = element("div", "card-header profile-row-header");
    const identity = element("div", "profile-identity");
    const visual = profileIconVisual(profile);
    const icon = element(
      "span",
      `profile-icon ${visual.className}`,
    );
    icon.textContent = visual.label;
    icon.setAttribute("aria-hidden", "true");
    const identityCopy = element("div", "profile-identity-copy");
    const name = textElement("h2", String(profile.display_name ?? ""));
    const slug = textElement("p", String(profile.slug ?? ""));
    slug.className = "profile-slug";
    identityCopy.append(name, slug);
    identity.append(icon, identityCopy);

    const badges = element("div", "cluster profile-badges");
    if (Number(profile.id) === defaultProfileID) {
      badges.append(badge("默认", "badge-success"));
    }
    if (!profile.enabled) {
      badges.append(badge("已停用", "badge-danger"));
    }
    if (profile.config?.vision?.enabled) {
      badges.append(badge("视觉增强", "badge-success"));
    }
    if (profile.config?.auto_routing?.enabled) {
      badges.append(badge("智能路由", "badge-success"));
    }
    header.append(identity, badges);

    const summary = element("dl", "profile-summary");
    appendDefinition(
      summary,
      "协议",
      String(profile.config?.protocol ?? ""),
    );
    appendDefinition(
      summary,
      "Upstream",
      String(profile.config?.upstream ?? ""),
    );

    const usage = profile.usage_30d || {};
    const metrics = element("div", "profile-metrics");
    metrics.append(
      textElement("span", `近 30 天请求：${Number(usage.requests ?? 0)}`),
      textElement(
        "span",
        `Token：${
          Number(usage.input_tokens ?? 0) +
          Number(usage.output_tokens ?? 0)
        }`,
      ),
    );

    const buttons = element("div", "cluster profile-actions");
    const edit = actionButton("编辑", "button-secondary");
    const generate = actionButton("生成配置", "button-secondary");
    const copy = actionButton("复制", "button-secondary");
    const setDefault = actionButton("设为默认", "button-secondary");
    const toggle = actionButton(
      profile.enabled ? "停用" : "启用",
      "button-secondary",
    );
    const remove = actionButton("删除", "button-danger");

    setDefault.disabled =
      Number(profile.id) === defaultProfileID || !profile.enabled;
    toggle.disabled =
      Number(profile.id) === defaultProfileID && profile.enabled;
    remove.disabled = profiles.length === 1;

    edit.addEventListener("click", () => actions.edit?.(profile));
    generate.addEventListener("click", async () => {
      await runListAction(alert, async () => {
        await actions.generate?.(profile);
        openConfigurationGenerator(root, profile);
      });
    });
    copy.addEventListener("click", () => {
      openCopyDialog(root, profile, actions, alert);
    });
    setDefault.addEventListener("click", async () => {
      if (setDefault.disabled) {
        return;
      }
      await runListAction(alert, () => actions.setDefault?.(profile));
    });
    toggle.addEventListener("click", async () => {
      if (toggle.disabled) {
        return;
      }
      await runListAction(alert, () => actions.toggle?.(profile));
    });
    remove.addEventListener("click", () => {
      if (remove.disabled) {
        return;
      }
      openDeleteDialog(
        root,
        profile,
        profiles,
        defaultProfileID,
        actions,
        alert,
      );
    });

    buttons.append(edit, generate, copy, setDefault, toggle, remove);
    card.append(header, summary, metrics, buttons);
    group.append(card);
  }

  page.append(heading, alert, group);
  root.replaceChildren(page);
}

export function renderProfileEditor(root, source, actions = {}) {
	const strategyOverview = source?.strategy_overview || null;
	const editingStrategyID = Number(source?.strategy_editing_id || 0);
	const currentActiveStrategy = strategyOverview?.snapshot?.active || null;
	const strategyVersions = strategyOverview?.strategies || [];
	const strategyGenerations = strategyOverview?.generations || [];
	const editingStrategyVersion = strategyVersions.find(
		(version) => Number(version.id) === editingStrategyID && version.state === "draft",
	);
	const generatedStrategyIDs = new Set(
		strategyGenerations.map((generation) => Number(generation.strategy_id)),
	);
	const openGeneratedDraft = strategyVersions.find(
		(version) => version.state === "draft" && !version.archived_at &&
			generatedStrategyIDs.has(Number(version.id)),
	) || null;
	const refreshableVersion = editingStrategyVersion && !editingStrategyVersion.archived_at &&
		generatedStrategyIDs.has(editingStrategyID)
		? editingStrategyVersion
		: openGeneratedDraft;
	const refreshableGeneration = refreshableVersion
		? strategyGenerations.find(
			(generation) => Number(generation.strategy_id) === Number(refreshableVersion.id),
		) || null
		: null;
	const refreshIntent = refreshableGeneration?.intent || {};
  const working = profileDraft(
    {
      ...source,
      config: source?.config,
    },
    source?.make_default ? source?.id : 0,
  );
  working.original_slug = String(source?.original_slug ?? source?.slug ?? "");
  working.make_default = Boolean(source?.make_default);

  const form = element("form", "profile-editor stack");
  const alert = element("div", "error-banner");
  alert.setAttribute("role", "alert");
  alert.hidden = true;

  const basic = editorSection("基础配置");
  const basicGrid = element("div", "form-grid");
  const displayName = fieldInput(
    basicGrid,
    "名称",
    "display_name",
    working.display_name,
    { required: true },
  );
  const slug = fieldInput(basicGrid, "Slug", "slug", working.slug, {
    required: true,
    description: "用于 Profile URL，例如 coding 对应 /coding/v1/…。",
  });
  const protocol = fieldSelect(
    basicGrid,
    "协议",
    "protocol",
    working.config.protocol,
    [
      ["anthropic", "Anthropic"],
      ["openai", "OpenAI"],
    ],
    {
      description: "必须与 Upstream 实际提供的 API 协议一致。",
    },
  );
  const upstream = fieldInput(
    basicGrid,
    "Upstream",
    "upstream",
    working.config.upstream,
    {
      required: true,
      type: "url",
      description: "请求转发的基础地址；不要填写调用方密钥或具体接口路径。",
    },
  );
  const version = fieldInput(
    basicGrid,
    "配置版本",
    "version",
    working.config.version,
    {
      required: true,
      type: "number",
      min: "1",
      description: "Profile 配置结构版本，当前保持为 1。",
    },
  );
  const enabled = checkboxField(
    basicGrid,
    "启用 Profile",
    "enabled",
    {
      description: "关闭后，该 Profile 的代理地址将不可用。",
    },
  );
  enabled.checked = working.enabled;
  const isCurrentDefault = working.make_default;
  const makeDefault = checkboxField(
    basicGrid,
    "设为默认 Profile",
    "make_default",
    {
      description: "接收不带 slug 的 /v1/… 请求；默认 Profile 必须启用。",
    },
  );
  makeDefault.checked = working.make_default;
  makeDefault.disabled = isCurrentDefault;

  function applyDefaultState() {
    if (isCurrentDefault) {
      makeDefault.checked = true;
    }
    if (makeDefault.checked) {
      enabled.checked = true;
    }
    enabled.disabled = makeDefault.checked;
  }

  makeDefault.addEventListener("change", applyDefaultState);
  applyDefaultState();
  const slugWarning = textElement(
    "p",
    "修改 slug 会改变 Agent 使用的 Profile URL。",
  );
  slugWarning.className = "warning-banner";
  slugWarning.hidden =
    !working.original_slug || working.slug === working.original_slug;
  slug.addEventListener("input", () => {
    working.slug = slug.value;
    slugWarning.hidden =
      !working.original_slug || slug.value === working.original_slug;
  });
  basic.append(basicGrid, slugWarning);

  const models = editorSection("模型能力");
  const modelHelp = textElement(
    "p",
    "选填。用于判断模型是否需要视觉增强，并为 Agent 生成上下文与自动压缩配置；不添加时不影响请求转发。",
  );
  modelHelp.className = "muted";
  const modelBatch = element("section", "card model-batch-card stack");
  modelBatch.append(
    textElement("h3", "批量录入模型"),
    textElement(
      "p",
      "一行一个模型 ID，最多 100 个。唯一可靠匹配会自动填充参数，其余模型仅录入 ID。",
      "muted",
    ),
  );
  const modelBatchFields = element("div", "stack");
  const modelBatchInput = fieldTextarea(
    modelBatchFields,
    "模型 ID 列表",
    "model_batch_ids",
    "",
    { description: "已存在和同批重复的 ID 会跳过，不覆盖现有参数。" },
  );
  modelBatchInput.setAttribute("placeholder", "例如：\nclaude-haiku-4-5-20251001\nclaude-glm-5.2");
  const modelBatchAlert = element("div", "model-batch-message");
  modelBatchAlert.setAttribute("role", "alert");
  modelBatchAlert.hidden = true;
  const importBatch = actionButton("批量导入", "button-secondary");
  const modelList = element("div", "stack model-capability-list");
  let modelRows = [];
  let modelRecommendationStates = working.config.models.map(() => ({
    autoValues: {},
  }));
  let refreshAutoModelOptions = () => {};
  let refreshVisionModelOptions = () => {};

  function syncModelCapabilities() {
    working.config.models = modelRows.map((row) => ({
      id: row.id.value,
      ...(row.canonicalModelID()
        ? { canonical_model_id: row.canonicalModelID() }
        : {}),
      context_window: row.contextWindow.value,
      max_output_tokens: row.maxOutputTokens.value,
      supports_vision: selectBooleanValue(row.supportsVision.value),
      supports_tools: selectBooleanValue(row.supportsTools.value),
      supports_agent_workflow: selectBooleanValue(
		row.supportsAgentWorkflow.value,
	  ),
      supports_structured_output: selectBooleanValue(
        row.supportsStructuredOutput.value,
      ),
      input_price_micro_usd_per_million: optionalUSDInputToMicroUSD(
        row.inputPrice.value,
        `模型 ${row.id.value || "未命名"}：输入价格`,
      ),
      output_price_micro_usd_per_million: optionalUSDInputToMicroUSD(
        row.outputPrice.value,
        `模型 ${row.id.value || "未命名"}：输出价格`,
      ),
      cache_read_price_micro_usd_per_million: optionalUSDInputToMicroUSD(
        row.cacheReadPrice.value,
        `模型 ${row.id.value || "未命名"}：缓存读取价格`,
      ),
      cache_write_price_micro_usd_per_million: optionalUSDInputToMicroUSD(
        row.cacheWritePrice.value,
        `模型 ${row.id.value || "未命名"}：缓存写入价格`,
      ),
    }));
  }

  function promptMissingTemplates(rows) {
    let index = 0;
    const next = () => {
      while (index < rows.length) {
        const row = rows[index++];
        if (row.promptTemplateIfNeeded({ afterClose: next })) {
          return;
        }
      }
    };
    next();
  }

  importBatch.addEventListener("click", () => {
    modelBatchAlert.hidden = true;
    modelBatchAlert.className = "model-batch-message";
    try {
      syncModelCapabilities();
      const rows = previewBatchModels(
        modelBatchInput.value,
        working.config.models,
      );
      const additions = importBatchModels(rows);
      if (additions.length === 0) {
        modelBatchAlert.textContent = "没有可导入的新模型。";
        modelBatchAlert.className = "warning-banner model-batch-message";
        modelBatchAlert.hidden = false;
        return;
      }
      const firstAdditionIndex = working.config.models.length;
      working.config.models.push(...additions);
      modelRecommendationStates.push(
        ...additions.map(() => ({ autoValues: {} })),
      );
      modelBatchInput.value = "";
      renderModelCapabilities();
      updateAutoDependencyState();
      promptMissingTemplates(modelRows.slice(firstAdditionIndex));
      modelBatchAlert.textContent = `已将 ${additions.length} 个模型加入草稿，请检查后保存。`;
      modelBatchAlert.className = "success-banner model-batch-message";
      modelBatchAlert.hidden = false;
    } catch (error) {
      modelBatchAlert.textContent = error?.message || "无法批量导入模型。";
      modelBatchAlert.className = "error-banner model-batch-message";
      modelBatchAlert.hidden = false;
    }
  });
  modelBatch.append(modelBatchFields, modelBatchAlert, importBatch);

  function renderModelCapabilities() {
    modelRows = [];
    const rows = working.config.models.map((model, index) => {
      const recommendationState = modelRecommendationStates[index] ?? {
        autoValues: {},
      };
      modelRecommendationStates[index] = recommendationState;
      const row = element("fieldset", "card model-capability-row");
      const legend = textElement("legend", `模型 ${index + 1}`);
      const fields = element("div", "form-grid model-capability-fields");
      const id = fieldInput(
        fields,
        "模型 ID",
        `model-${index}-id`,
        model.id,
        {
          required: true,
          description:
            "必须与请求中的 model 完全一致；粘贴已收录 ID 可自动填充其余字段。",
        },
      );
      id.setAttribute("data-model-field", "id");
      let canonicalModelID = String(model.canonical_model_id || "");
      let canonicalOwnerID = canonicalModelID ? String(model.id || "") : "";
      let previousCanonicalIdentity = null;

      function applyCanonicalIdentity(entry) {
        canonicalModelID = String(entry?.canonicalId || entry?.id || "");
        canonicalOwnerID = canonicalModelID ? String(id.value) : "";
        if (canonicalModelID) {
          model.canonical_model_id = canonicalModelID;
        } else {
          delete model.canonical_model_id;
        }
        previousCanonicalIdentity = null;
      }

      function trackModelIDChange() {
        if (previousCanonicalIdentity && id.value === previousCanonicalIdentity.owner) {
          canonicalModelID = previousCanonicalIdentity.id;
          canonicalOwnerID = previousCanonicalIdentity.owner;
          model.canonical_model_id = canonicalModelID;
          previousCanonicalIdentity = null;
          return;
        }
        if (canonicalModelID && id.value !== canonicalOwnerID) {
          previousCanonicalIdentity = {
            id: canonicalModelID,
            owner: canonicalOwnerID,
          };
          canonicalModelID = "";
          canonicalOwnerID = "";
          delete model.canonical_model_id;
        }
      }

      const presetWrapper = element(
        "div",
        "form-field model-preset-field",
      );
      const presetLabel = textElement("label", "模型模板");
      const preset = element("input");
      preset.name = `model-${index}-preset`;
      preset.type = "search";
      preset.id = `profile-${preset.name}`;
      preset.setAttribute("placeholder", "搜索模型 ID、名称或提供方");
      presetLabel.setAttribute("for", preset.id);
      const presetList = element("datalist");
      presetList.id = `model-${index}-preset-options`;
      preset.setAttribute("list", presetList.id);
      const catalogModels = currentModelCatalog().models || [];
      for (const entry of catalogModels) {
        const option = element("option");
        option.value = String(entry.canonicalId || entry.id || "");
        option.setAttribute(
          "label",
          [entry.name, entry.provider].filter(Boolean).join(" · "),
        );
        presetList.append(option);
      }
      const applyPreset = actionButton("应用参数模板", "button-secondary");
      applyPreset.disabled = true;
      const presetControls = element("div", "model-preset-controls");
      presetControls.append(preset, applyPreset);
      presetWrapper.append(presetLabel, presetControls, presetList);
      appendFieldDescription(
        presetWrapper,
        preset,
        "可搜索并应用已收录模型的参数；不会修改当前模型 ID。",
      );
      fields.append(presetWrapper);

      const contextWindow = fieldInput(
        fields,
        "上下文窗口",
        `model-${index}-context-window`,
        model.context_window,
        {
          description:
            "选填，用于 Agent 上下文与自动压缩；支持 128K、1M 等写法。",
        },
      );
      contextWindow.setAttribute("data-model-field", "context_window");
      contextWindow.setAttribute("placeholder", "例如 128K、256K、1M");
      const maxOutputTokens = fieldInput(
        fields,
        "最大输出 Token",
        `model-${index}-max-output-tokens`,
        model.max_output_tokens,
        {
          description:
            "选填，用于预留输出空间；必须小于上下文窗口。",
        },
      );
      maxOutputTokens.setAttribute(
        "data-model-field",
        "max_output_tokens",
      );
      maxOutputTokens.setAttribute("placeholder", "例如 128K、256K、1M");
      const supportsVision = fieldSelect(
        fields,
        "支持视觉",
        `model-${index}-supports-vision`,
        booleanSelectValue(model.supports_vision),
        [
          ["", "请选择"],
          ["true", "是"],
          ["false", "否"],
        ],
        {
          description:
            "“是”表示图片直接发送主模型；“否”表示可使用视觉增强。",
        },
      );
      supportsVision.setAttribute("data-model-field", "supports_vision");
      const supportsTools = fieldSelect(
        fields,
        "支持工具调用",
        `model-${index}-supports-tools`,
        booleanSelectValue(model.supports_tools),
        [
          ["", "未知"],
          ["true", "是"],
          ["false", "否"],
        ],
        {
          description:
            "选填。Auto 请求包含 tools 时，未知或不支持的模型会被排除。",
        },
      );
      supportsTools.setAttribute("data-model-field", "supports_tools");
      const supportsAgentWorkflow = fieldSelect(
        fields,
        "支持 Agent 工作流",
        `model-${index}-supports-agent-workflow`,
        booleanSelectValue(model.supports_agent_workflow),
        [
          ["", "未验证"],
          ["true", "已验证"],
          ["false", "不支持"],
        ],
		{
		  description:
			"表示可完成 Claude Code/Codex 长上下文与多轮工具工作流；提示词是否外泄不再作为自动切换条件。未验证模型不会接收 Agent 的 Auto 请求。",
		},
      );
      supportsAgentWorkflow.setAttribute(
        "data-model-field",
        "supports_agent_workflow",
      );
      const supportsStructuredOutput = fieldSelect(
        fields,
        "支持结构化输出",
        `model-${index}-supports-structured-output`,
        booleanSelectValue(model.supports_structured_output),
        [
          ["", "未知"],
          ["true", "是"],
          ["false", "否"],
        ],
        {
          description:
            "选填。Auto 请求要求 JSON Schema 等结构化输出时用于硬能力过滤。",
        },
      );
      supportsStructuredOutput.setAttribute(
        "data-model-field",
        "supports_structured_output",
      );
      const inputPrice = fieldInput(
        fields,
        "输入价格（美元/百万 Token）",
        `model-${index}-input-price`,
        microUSDToUSDInput(model.input_price_micro_usd_per_million),
        {
          type: "number",
          min: "0",
          step: "0.000001",
          description:
            "选填，直接填写每百万 Token 的美元价格，例如 0.10。参与 Auto 时必填，0 表示免费，最多 6 位小数。",
        },
      );
      inputPrice.setAttribute(
        "data-model-field",
        "input_price_micro_usd_per_million",
      );
      const outputPrice = fieldInput(
        fields,
        "输出价格（美元/百万 Token）",
        `model-${index}-output-price`,
        microUSDToUSDInput(model.output_price_micro_usd_per_million),
        {
          type: "number",
          min: "0",
          step: "0.000001",
          description:
            "选填，直接填写每百万 Token 的美元价格，例如 0.40。参与 Auto 时必填，0 表示免费，最多 6 位小数。",
        },
      );
      outputPrice.setAttribute(
        "data-model-field",
        "output_price_micro_usd_per_million",
      );
      const cacheReadPrice = fieldInput(
        fields,
        "缓存读取价格（美元/百万 Token）",
        `model-${index}-cache-read-price`,
        microUSDToUSDInput(model.cache_read_price_micro_usd_per_million),
        {
          type: "number", min: "0", step: "0.000001",
          description: "选填。缓存命中后读取每百万 Token 的美元价格。",
        },
      );
      cacheReadPrice.setAttribute(
        "data-model-field",
        "cache_read_price_micro_usd_per_million",
      );
      const cacheWritePrice = fieldInput(
        fields,
        "缓存写入价格（美元/百万 Token）",
        `model-${index}-cache-write-price`,
        microUSDToUSDInput(model.cache_write_price_micro_usd_per_million),
        {
          type: "number", min: "0", step: "0.000001",
          description: "选填。按大多数 Agent 使用的默认缓存写入规则计价。",
        },
      );
      cacheWritePrice.setAttribute(
        "data-model-field",
        "cache_write_price_micro_usd_per_million",
      );

      const recommendation = element(
        "div",
        "model-recommendation stack",
      );
      recommendation.id = `${id.id}-recommendations`;
      recommendation.setAttribute("aria-live", "polite");
      id.setAttribute("aria-controls", recommendation.id);

      const capabilityControls = {
        context_window: contextWindow,
        max_output_tokens: maxOutputTokens,
        supports_vision: supportsVision,
        supports_tools: supportsTools,
        supports_agent_workflow: supportsAgentWorkflow,
        supports_structured_output: supportsStructuredOutput,
        input_price_micro_usd_per_million: inputPrice,
        output_price_micro_usd_per_million: outputPrice,
        cache_read_price_micro_usd_per_million: cacheReadPrice,
        cache_write_price_micro_usd_per_million: cacheWritePrice,
      };
      const booleanCapabilityFields = new Set([
        "supports_vision",
        "supports_tools",
        "supports_agent_workflow",
        "supports_structured_output",
      ]);
      const priceCapabilityFields = new Set([
        "input_price_micro_usd_per_million",
        "output_price_micro_usd_per_million",
        "cache_read_price_micro_usd_per_million",
        "cache_write_price_micro_usd_per_million",
      ]);
      let renderedRecommendationQuery = null;
      let renderedRecommendationSignature = null;
      let renderedMatches = [];

      function applyRecommendation(match, { overwrite = false } = {}) {
        for (const [field, control] of Object.entries(capabilityControls)) {
          const owned = Object.hasOwn(
            recommendationState.autoValues,
            field,
          );
          const oldAutoValue = recommendationState.autoValues[field];
          const canWrite = overwrite || control.value === "" ||
            (owned && control.value === oldAutoValue);

          if (!canWrite) {
            if (owned) {
              delete recommendationState.autoValues[field];
            }
            continue;
          }

          if (!Object.hasOwn(match.entry, field)) {
            if (owned) {
              control.value = "";
              delete recommendationState.autoValues[field];
            }
            continue;
          }

          const value = booleanCapabilityFields.has(field)
            ? booleanSelectValue(match.entry[field])
            : priceCapabilityFields.has(field)
              ? microUSDToUSDInput(match.entry[field])
              : String(match.entry[field]);
          control.value = value;
          recommendationState.autoValues[field] = value;
        }
        applyCanonicalIdentity(match.entry);
      }

      let selectedPresetEntry = null;
      let presetAutoSelected = false;
      let promptedTemplateID = "";
      let templateDialogOpen = false;
      let recommendationActionPending = false;
      const selectPreset = ({ manual = true } = {}) => {
        const query = String(preset.value).trim().toLowerCase();
        selectedPresetEntry = catalogModels.find((entry) =>
          [entry.canonicalId, entry.id].some(
            (value) => String(value || "").toLowerCase() === query,
          )
        ) || null;
        applyPreset.disabled = selectedPresetEntry === null;
        if (manual) {
          presetAutoSelected = false;
        }
      };
      preset.addEventListener("input", selectPreset);
      preset.addEventListener("change", selectPreset);
      applyPreset.addEventListener("click", () => {
        if (selectedPresetEntry) {
          applyRecommendation({
            entry: selectedPresetEntry,
            match: "preset",
            autoApply: false,
          }, { overwrite: true });
        }
      });

      function syncPresetWithRecommendation(matches) {
        const match = matches.length === 1 && matches[0].autoApply
          ? matches[0]
          : null;
        if (match) {
          preset.value = String(
            match.entry.canonicalId || match.entry.id || "",
          );
          presetAutoSelected = true;
          selectPreset({ manual: false });
          return;
        }
        if (presetAutoSelected) {
          preset.value = "";
          selectedPresetEntry = null;
          applyPreset.disabled = true;
          presetAutoSelected = false;
        }
      }

      function clearAutomaticValues() {
        for (const [field, control] of Object.entries(capabilityControls)) {
          if (!Object.hasOwn(recommendationState.autoValues, field)) {
            continue;
          }
          if (control.value === recommendationState.autoValues[field]) {
            control.value = "";
          }
          delete recommendationState.autoValues[field];
        }
      }

      function renderRecommendation(match) {
        const card = element(
          "article",
          "card model-recommendation-card stack",
        );
        const header = element("div", "cluster model-recommendation-header");
        const name = textElement(
          "h3",
          reliableValue(match.entry.name),
        );
        const lifecycle = textElement(
          "span",
          `生命周期：${lifecycleLabel(match.entry.lifecycle)}`,
        );
        lifecycle.className = "badge";
        header.append(name, lifecycle);

        const provider = textElement(
          "p",
          `提供方：${reliableValue(match.entry.provider)}`,
        );
        const details = textElement(
          "p",
          `匹配：${suggestionLabel(match.match)} · 来源：${
            reliableValue(match.entry.source?.name)
          }`,
        );
        const dates = textElement(
          "p",
          `获取：${reliableValue(match.entry.source?.retrieved)} · 更新：${
            reliableValue(match.entry.lastUpdated)
          }`,
        );
        const capabilities = textElement(
          "p",
          `上下文窗口：${recommendedField(match.entry, "context_window")} · ` +
            `最大输出 Token：${
              recommendedField(match.entry, "max_output_tokens")
            } · 视觉：${recommendedBoolean(match.entry, "supports_vision")} · ` +
            `工具：${recommendedBoolean(match.entry, "supports_tools")} · ` +
            `结构化输出：${
              recommendedBoolean(match.entry, "supports_structured_output")
            }`,
        );
        const pricing = textElement(
          "p",
          `参考价格：输入 ${recommendedPrice(match.entry, "input_price_micro_usd_per_million")} · ` +
            `输出 ${recommendedPrice(match.entry, "output_price_micro_usd_per_million")} · ` +
            `缓存读取 ${recommendedPrice(match.entry, "cache_read_price_micro_usd_per_million")} · ` +
            `缓存写入 ${recommendedPrice(match.entry, "cache_write_price_micro_usd_per_million")}。` +
            "用于 Auto 成本估算，请按实际上游账单确认。",
        );
        provider.className = "muted model-recommendation-provider";
        details.className = "muted model-recommendation-source";
        dates.className = "muted model-recommendation-meta";
        capabilities.className = "model-recommendation-capabilities";
        pricing.className = "muted model-recommendation-price";
        card.append(header, provider, details, dates, capabilities, pricing);

        if (!match.autoApply) {
          const apply = actionButton(
            `应用 ${reliableValue(match.entry.name)} 推荐值`,
            "button-secondary",
          );
          apply.addEventListener("pointerdown", () => {
            recommendationActionPending = true;
          });
          apply.addEventListener("pointercancel", () => {
            recommendationActionPending = false;
          });
          apply.addEventListener("click", () => {
            applyRecommendation(match);
            recommendationActionPending = false;
          });
          card.append(apply);
        }
        return card;
      }

      function calculateRecommendations() {
        const query = String(id.value);
        const matches = matchModelSuggestions(query, { limit: 5 });
        const signature = matches.map((match) => [
          match.entry.canonicalId ?? match.entry.id ?? "",
          match.match,
          match.autoApply ? "1" : "0",
        ].join("\u0000")).join("\u0001");
        return { query, matches, signature };
      }

      function renderRecommendations(result) {
        if (
          result.query === renderedRecommendationQuery &&
          result.signature === renderedRecommendationSignature
        ) {
          return;
        }
        syncPresetWithRecommendation(result.matches);
        recommendation.replaceChildren(
          ...result.matches.map(renderRecommendation),
        );
        renderedRecommendationQuery = result.query;
        renderedRecommendationSignature = result.signature;
        renderedMatches = result.matches;
      }

      function currentRecommendations() {
        if (String(id.value) === renderedRecommendationQuery) {
          return renderedMatches;
        }
        const result = calculateRecommendations();
        renderRecommendations(result);
        return result.matches;
      }

      function refreshRecommendations({ applyAuto = false } = {}) {
        const result = calculateRecommendations();
        renderRecommendations(result);
        if (
          applyAuto &&
          result.matches.length === 1 &&
          result.matches[0].autoApply
        ) {
          applyRecommendation(result.matches[0]);
        }
      }

      function commitRecommendations() {
        const matches = currentRecommendations();
        if (
          matches.length === 1 &&
          matches[0].autoApply
        ) {
          applyRecommendation(matches[0]);
        } else {
          clearAutomaticValues();
        }
        return matches;
      }

      function promptTemplateIfNeeded({ afterClose } = {}) {
        const modelID = String(id.value).trim();
        const matches = currentRecommendations();
        if (
          !modelID ||
          (matches.length === 1 && matches[0].autoApply) ||
          catalogModels.length === 0 ||
          promptedTemplateID === modelID ||
          templateDialogOpen
        ) {
          return false;
        }
        promptedTemplateID = modelID;
        templateDialogOpen = true;
        openModelTemplateDialog(root, {
          index,
          modelID,
          catalogModels,
          onApply(entry) {
            preset.value = String(entry.canonicalId || entry.id || "");
            selectPreset();
            applyRecommendation({
              entry,
              match: "preset",
              autoApply: false,
            }, { overwrite: true });
          },
          onClose() {
            templateDialogOpen = false;
            afterClose?.();
          },
        });
        return true;
      }

      function commitModelID() {
        const mutation = validateModelMutation(working, model.id, id.value);
        if (!mutation.allowed) {
          id.value = model.id;
          refreshRecommendations();
          refreshAutoModelOptions();
          openModelReferenceDialog(root, working, model.id, mutation.references);
          return;
        }
        commitRecommendations();
        if (!recommendationActionPending) {
          promptTemplateIfNeeded();
        }
      }

      for (const [field, control] of Object.entries(capabilityControls)) {
        const releaseOwnership = () => {
          delete recommendationState.autoValues[field];
        };
        control.addEventListener("input", releaseOwnership);
        control.addEventListener("change", releaseOwnership);
      }
      id.addEventListener("input", (event) => {
        recommendationActionPending = false;
        trackModelIDChange();
        refreshRecommendations({ applyAuto: true });
        refreshAutoModelOptions();
        updateAutoDependencyState();
        if (event.inputType === "insertFromPaste") {
          commitModelID();
        }
      });
      id.addEventListener("change", commitModelID);
      id.addEventListener("blur", commitModelID);
      refreshRecommendations({ applyAuto: true });

      const controls = element("div", "cluster model-capability-actions");
      const remove = actionButton("删除模型", "button-danger");
      remove.addEventListener("click", () => {
        const mutation = validateModelMutation(working, model.id, "");
        if (!mutation.allowed) {
          openModelReferenceDialog(root, working, model.id, mutation.references);
          return;
        }
        syncModelCapabilities();
        working.config.models = removeModelCapability(
          working.config.models,
          index,
        );
        modelRecommendationStates = modelRecommendationStates.filter(
          (_, current) => current !== index,
        );
        renderModelCapabilities();
        updateAutoDependencyState();
      });
      controls.append(remove);
      row.append(legend, fields, recommendation, controls);
      modelRows.push({
        id,
        canonicalModelID: () => canonicalModelID,
        contextWindow,
        maxOutputTokens,
        supportsVision,
        supportsTools,
        supportsAgentWorkflow,
        supportsStructuredOutput,
        inputPrice,
        outputPrice,
        cacheReadPrice,
        cacheWritePrice,
        promptTemplateIfNeeded,
      });
      return row;
    });
    modelList.replaceChildren(...rows);
    refreshAutoModelOptions();
  }

  renderModelCapabilities();
  const addModel = actionButton("添加模型", "button-secondary");
  addModel.addEventListener("click", () => {
    syncModelCapabilities();
    working.config.models = addModelCapability(working.config.models);
    modelRecommendationStates.push({ autoValues: {} });
    renderModelCapabilities();
    updateAutoDependencyState();
  });
  const modelListActions = element("div", "cluster model-list-actions");
  modelListActions.append(addModel);
  models.append(modelHelp, modelBatch, modelList, modelListActions);

  const autoRouting = editorSection("智能路由");
  const autoHelp = textElement(
    "p",
		"智能路由仅处理 model=auto。系统结合公开评测和本地证据，为参与模型生成角色、Route 门槛、权重、任务映射和预算。推荐结果始终是可编辑草稿，不会自动发布；客户端指定具体模型时仍原样转发。",
  );
  const strategyGuide = element("div", "routing-strategy-guide stack");
  const strategyGuideLink = textElement("a", "查看完整字段说明和选模示例 ↗");
  strategyGuideLink.href = INTELLIGENT_ROUTING_HELP_HREF;
  strategyGuide.append(autoHelp, strategyGuideLink);
  const autoEnabled = checkboxField(
    autoRouting,
    "启用智能路由",
    "auto_routing_enabled",
  );
  autoEnabled.checked = Boolean(working.config.auto_routing.enabled);
  const autoDependency = textElement(
    "p",
    "请先在“模型”页面录入至少两个不同模型，才能启用智能路由。",
  );
  autoDependency.className = "warning-banner";
  function updateAutoDependencyState() {
    const recorded = new Set(
      modelRows.map((row) => row.id.value.trim()).filter(Boolean),
    ).size >= 2;
    autoEnabled.disabled = !recorded && !autoEnabled.checked;
    autoDependency.hidden = recorded;
  }
  updateAutoDependencyState();
  const autoDetails = element("div", "stack auto-routing-details");
  const autoRoles = editorSubsection("模型角色");
  const participantList = element("div", "model-participant-list stack");
  const participantHelp = textElement(
    "p",
    "至少选择两个不同模型。只有勾选的模型会参与主回答选型；变更参与模型时，系统会要求先生成兼容的新策略草稿。",
  );
  participantHelp.className = "field-help";
  autoRoles.append(participantHelp, participantList);
  const roleGrid = element("div", "form-grid");
  const strongBaseline = fieldSelect(
    roleGrid,
    "强模型基线",
    "auto_strong_baseline_model",
    working.config.auto_routing.strong_baseline_model,
    [],
    {
      description:
        "高风险、首次分析失败且无会话模型，或没有普通候选达标时使用；必须属于参与模型。",
    },
  );
  const taskAnalyzer = fieldSelect(
    roleGrid,
    "任务分析模型",
    "auto_task_analyzer_model",
    working.config.auto_routing.task_analyzer_model,
    [],
    {
      description:
		"每个 model=auto 请求都会调用；必须明确支持普通工具调用，建议选择快速、低价的模型。",
    },
  );
  const analyzerTimeout = fieldInput(
    roleGrid,
    "任务分析超时",
    "auto_analyzer_timeout",
    working.config.auto_routing.analyzer_timeout,
    {
      description: "单次任务分析的上限，建议 15s；超时后保守回到强模型基线。",
    },
  );
  const analyzerConfidence = fieldInput(
    roleGrid,
		"各维度最低置信度（%）",
    "auto_analyzer_confidence",
    basisPointsToPercent(
      working.config.auto_routing.analyzer_min_confidence_bps,
    ),
    {
      type: "number",
      min: "0",
      max: "100",
      step: "0.01",
		description: "分别约束任务类型、复杂度和风险：低置信任务类型走默认 Route，复杂度按中等保护，风险不确定时使用强模型兜底。",
    },
  );
  const sessionTTL = fieldInput(
    roleGrid,
    "Session 绑定有效期",
    "auto_session_ttl",
    working.config.auto_routing.session_ttl,
    {
	  description:
		"默认 24h。Claude Code 和 Codex 原生会话会自动识别；其他客户端发送 X-LLM-Proxy-Session-ID。同一任务的工具回合直接复用；新任务会重新分析风险。",
    },
  );
  const sessionLockTokenThreshold = fieldInput(
    roleGrid,
    "Session 锁定 Token 阈值",
    "auto_session_lock_token_threshold",
    working.config.auto_routing.session_lock_token_threshold,
    {
      type: "number", min: "1", step: "1000",
      description: "默认 100K。连续 3 个任务稳定、最低置信度 85%，且会话输入或缓存读取达到阈值后锁定模型；修改阈值会撤销普通锁，但保留最高模型锁。",
    },
  );
  const selfEscalationEnabled = checkboxField(
    roleGrid,
    "启用模型主动升级",
    "auto_self_escalation_enabled",
    {
      description:
        "低级模型只能在输出任何文本前申请升级；代理拦截后用原始请求调用更强模型，不会把低级模型的内容交给最终模型。需要至少一次模型切换预算，所有非强基线候选模型必须支持工具调用。",
    },
  );
  selfEscalationEnabled.checked = Boolean(
    working.config.auto_routing.self_escalation?.enabled,
  );
  autoRoles.append(roleGrid);

  const riskSection = editorSubsection("高风险判定");
	const riskV2Help = textElement(
	  "p",
	  "高风险完全由任务分析模型按语义判断，支持多语言、标点和同义表达，并区分执行意图与引用、否定、审计和沙箱场景。普通 Shell、代码编辑、结构化输出和长上下文不会单独触发高风险。",
	);
	riskV2Help.className = "field-help";
	riskSection.append(riskV2Help);

	const dynamicOptimizationSection = editorSubsection("对比学习");
	const dynamicConfig = working.config.auto_routing.dynamic_optimization;
	const dynamicEnabled = checkboxField(
		dynamicOptimizationSection,
		"启用异步对比学习",
		"auto_dynamic_enabled",
		{
			description:
				"只在当前回答已经返回后抽样比较低成本模型与强模型基线，不影响当前回答，也不会自动发布策略。",
		},
	);
	dynamicEnabled.checked = Boolean(dynamicConfig.enabled);
	const dynamicDetails = element("div", "stack dynamic-optimization-details");
	const dynamicGrid = element("div", "form-grid");
	const dynamicSampleRate = fieldInput(
		dynamicGrid,
		"异步抽样比例（%）",
		"auto_dynamic_sample_rate",
		basisPointsToPercent(dynamicConfig.sample_rate_bps),
		{
			type: "number", min: "0.01", max: "100", step: "0.01",
			description: "仅抽取成功的 model=auto 请求；未命中时不会产生额外调用。",
		},
	);
	const dynamicDailyBudget = fieldInput(
		dynamicGrid,
		"每日评测预算（美元）",
		"auto_dynamic_daily_budget",
		microUSDToUSDInput(dynamicConfig.daily_budget_micro_usd),
		{
			type: "number", min: "0.000001", step: "0.000001",
			description: "对比回答和质量评审共用的每日硬上限，直接填写美元金额，最多 6 位小数。",
		},
	);
	const dynamicReviewer = fieldSelect(
		dynamicGrid,
		"质量评审模型",
		"auto_dynamic_reviewer_model",
		dynamicConfig.reviewer_model,
		[],
		{
			description: "对去掉模型身份的 A/B 回答做异步判断；开启后必填，且必须已录入当前 Profile。",
		},
	);
	const dynamicConcurrency = fieldInput(
		dynamicGrid,
		"评测并发",
		"auto_dynamic_concurrency",
		dynamicConfig.max_concurrency,
		{
			type: "number", min: "1", max: "32", step: "1",
			description: "当前 Profile 同时运行的异步评测任务上限。",
		},
	);
	const dynamicQueueCapacity = fieldInput(
		dynamicGrid,
		"等待队列容量",
		"auto_dynamic_queue",
		dynamicConfig.queue_capacity,
		{
			type: "number", min: "1", max: "4096", step: "1",
			description: "队列满时直接丢弃样本，绝不阻塞在线请求。",
		},
	);
	const dynamicTimeout = fieldInput(
		dynamicGrid,
		"单次评测超时",
		"auto_dynamic_timeout",
		dynamicConfig.task_timeout,
		{
			description: "对比回答与质量评审的总时限，例如 90s，最长 10m。",
		},
	);
	dynamicDetails.append(dynamicGrid);
	dynamicDetails.hidden = !dynamicEnabled.checked;
	dynamicEnabled.addEventListener("change", () => {
		dynamicDetails.hidden = !dynamicEnabled.checked;
	});
	dynamicOptimizationSection.append(dynamicDetails);

  const strategySection = editorSubsection("手动设置策略");
  const strategyGrid = element("div", "form-grid");
  const strategyName = fieldInput(
    strategyGrid,
    "策略名称",
    "auto_strategy_name",
    working.config.auto_routing.strategy.name,
    {
      description: "策略的唯一版本号，格式为 YYYYMMDD-NNN。保存后不可修改；调整规则时应创建新版本。",
    },
  );
	if (working.id > 0) {
		strategyName.readOnly = true;
	}
  const strategyAlias = fieldInput(
    strategyGrid,
    "策略别名",
    "auto_strategy_alias",
    working.config.auto_routing.strategy.alias,
    {
      description: "选填，仅用于后台识别，不参与路由判断，例如“质量优先”或“日常均衡”。",
    },
  );
  const defaultRoute = fieldSelect(
    strategyGrid,
    "默认 Route",
    "auto_default_route",
    working.config.auto_routing.strategy.default_route,
    [],
    {
      description: "任务分析结果没有匹配下方“任务映射”时使用的兜底 Route；启用智能路由后必填。",
    },
  );
	const minimumNetSavings = fieldInput(
		strategyGrid,
		"最低净节省（%）",
		"auto_min_net_savings",
		basisPointsToPercent(working.config.auto_routing.strategy.min_net_savings_bps || 0),
		{
			type: "number", min: "0", max: "100", step: "0.01",
			description: "计入任务分析和当前请求已产生的费用后，候选至少需要节省的比例；智能生成默认 10%。",
		},
	);
	const strategyLatencyTarget = fieldInput(
		strategyGrid,
		"候选延迟上限（ms）",
		"auto_strategy_latency_target",
		working.config.auto_routing.strategy.latency_target_ms || 0,
		{
			type: "number", min: "0", step: "1",
			description: "0 表示不限制；启用后，延迟未知或超过上限的非基线候选会回退强基线。",
		},
	);
  strategySection.append(strategyGrid);

  const routeList = element("div", "stack auto-route-list");
  let autoRouteRows = [];
  let taskRouteRows = [];
  let participantInputs = [];

  function participantModelIDs() {
    return participantInputs
      .filter(({ input }) => input.checked)
      .map(({ id }) => id);
  }

  function currentModelIDs() {
    return modelRows.map((row) => row.id.value.trim()).filter(Boolean);
  }

  function routeIDs() {
    return autoRouteRows.map((row) => row.id.value.trim()).filter(Boolean);
  }

  function syncAutoRoutes() {
    working.config.auto_routing.strategy.routes = autoRouteRows.map((row) => ({
      id: row.id.value,
      min_quality_bps: percentToBasisPoints(
        row.minQuality.value,
        `Route ${row.id.value || "未命名"}：最低质量`,
      ),
			min_stability_bps: percentToBasisPoints(
				row.minStability.value,
				`Route ${row.id.value || "未命名"}：最低稳定性`,
			),
      max_severe_error_rate_bps: percentToBasisPoints(
        row.maxSevereError.value,
        `Route ${row.id.value || "未命名"}：严重错误率`,
      ),
			weights: {
				quality_bps: percentToBasisPoints(row.qualityWeight.value, "质量权重"),
				stability_bps: percentToBasisPoints(row.stabilityWeight.value, "稳定性权重"),
				cost_bps: percentToBasisPoints(row.costWeight.value, "成本权重"),
				performance_bps: percentToBasisPoints(row.performanceWeight.value, "性能权重"),
			},
      candidates: row.candidates.map((candidate) => ({
        model: candidate.model.value,
        production_eligible: candidate.productionEligible.checked,
        quality_score_bps: percentToBasisPoints(
          candidate.quality.value,
          `候选模型 ${candidate.model.value || "未选择"}：质量`,
        ),
				stability_score_bps: percentToBasisPoints(
					candidate.stability.value,
					`候选模型 ${candidate.model.value || "未选择"}：稳定性`,
				),
        severe_error_rate_bps: percentToBasisPoints(
          candidate.severeError.value,
          `候选模型 ${candidate.model.value || "未选择"}：严重错误率`,
        ),
				expected_latency_ms: requiredNonnegativeSafeInteger(
					candidate.expectedLatency.value,
					`候选模型 ${candidate.model.value || "未选择"}：预期延迟`,
				),
      })),
    }));
  }

  function refreshRouteChoices() {
    const choices = [["", "请选择"], ...routeIDs().map((id) => [id, id])];
    setSelectChoices(defaultRoute, choices, defaultRoute.value);
    for (const row of taskRouteRows) {
      setSelectChoices(row.route, choices, row.route.value);
    }
  }

  function refreshCandidateModelChoices() {
    const choices = [
      ["", "请选择"],
      ...participantModelIDs().map((id) => [id, id]),
    ];
    for (const route of autoRouteRows) {
      for (const candidate of route.candidates) {
        setSelectChoices(candidate.model, choices, candidate.model.value);
      }
    }
  }

  function renderAutoRoutes() {
    autoRouteRows = [];
    const rows = working.config.auto_routing.strategy.routes.map(
      (route, routeIndex) => {
        const row = element("fieldset", "card auto-route-row");
        const legend = textElement("legend", `Route ${routeIndex + 1}`);
        const fields = element("div", "form-grid");
        const id = fieldInput(fields, "Route ID", `auto-route-${routeIndex}-id`, route.id, {
          description: "策略内唯一的内部标识，供任务映射引用，不会发送给上游。例如 simple、coding 或 long_context。",
        });
        const minQuality = fieldInput(
          fields,
          "最低质量（%）",
          `auto-route-${routeIndex}-min-quality`,
          basisPointsToPercent(route.min_quality_bps),
          {
            type: "number", min: "0", max: "100", step: "0.01",
            description: "本 Route 可接受的最低质量。例如设为 90，质量估计 89 的候选会被排除，92 的候选可继续参与选型。",
          },
        );
				const minStability = fieldInput(
					fields,
					"最低稳定性（%）",
					`auto-route-${routeIndex}-min-stability`,
					basisPointsToPercent(route.min_stability_bps),
					{
						type: "number", min: "0", max: "100", step: "0.01",
						description: "真实请求成功率、无严重错误、输出有效性与干净完成率的综合下限。低于该值的候选不会进入加权排序。",
					},
				);
        const maxSevereError = fieldInput(
          fields,
          "最大严重错误率（%）",
          `auto-route-${routeIndex}-max-severe-error`,
          basisPointsToPercent(route.max_severe_error_rate_bps),
          {
            type: "number", min: "0", max: "100", step: "0.01",
            description: "本 Route 可接受的严重错误率上限。例如设为 1%，候选为 1.2% 时即使更便宜也会被排除。",
          },
        );
				const weights = route.weights || {};
				const qualityWeight = fieldInput(
					fields, "质量权重（%）", `auto-route-${routeIndex}-weight-quality`,
					basisPointsToPercent(weights.quality_bps),
					{ type: "number", min: "0", max: "100", step: "0.01", description: "质量下界在最终路由分数中的占比。四项权重之和必须为 100%。" },
				);
				const stabilityWeight = fieldInput(
					fields, "稳定性权重（%）", `auto-route-${routeIndex}-weight-stability`,
					basisPointsToPercent(weights.stability_bps),
					{ type: "number", min: "0", max: "100", step: "0.01", description: "稳定性下界在最终路由分数中的占比。" },
				);
				const costWeight = fieldInput(
					fields, "成本权重（%）", `auto-route-${routeIndex}-weight-cost`,
					basisPointsToPercent(weights.cost_bps),
					{ type: "number", min: "0", max: "100", step: "0.01", description: "相对最低预期完整成本的效率得分占比。" },
				);
				const performanceWeight = fieldInput(
					fields, "性能权重（%）", `auto-route-${routeIndex}-weight-performance`,
					basisPointsToPercent(weights.performance_bps),
					{ type: "number", min: "0", max: "100", step: "0.01", description: "相对最低预期延迟的性能得分占比；未知延迟保守按 0 分处理。" },
				);
        const candidatesList = element("div", "stack auto-candidate-list");
        const candidateControls = [];
        const renderCandidates = () => {
          candidateControls.length = 0;
          const candidateRows = (route.candidates || []).map((candidate, candidateIndex) => {
            const candidateRow = element("div", "card auto-candidate-row");
            const candidateFields = element("div", "form-grid");
            const model = fieldSelect(
              candidateFields,
              "候选模型",
              `auto-route-${routeIndex}-candidate-${candidateIndex}-model`,
              candidate.model,
              [["", "请选择"], ...participantModelIDs().map((modelID) => [modelID, modelID])],
              { description: "该模型只参与当前 Route；必须先在上方勾选为参与模型。通过全部门槛后才会比较预计完整成本。" },
            );
            const productionEligible = checkboxField(
              candidateFields,
              "允许生产流量",
              `auto-route-${routeIndex}-candidate-${candidateIndex}-production-eligible`,
              {
                description: "关闭时仍可参加 Shadow 评测。手动开启会直接授权生产流量；自动晋升只使用可靠本地证据。",
              },
            );
            productionEligible.checked = Boolean(candidate.production_eligible);
            const quality = fieldInput(
              candidateFields,
              "当前质量估计（%）",
              `auto-route-${routeIndex}-candidate-${candidateIndex}-quality`,
              basisPointsToPercent(candidate.quality_score_bps),
              {
                type: "number", min: "0", max: "100", step: "0.01",
                description: "该模型在当前 Route 下的初始质量估计，用于和“最低质量”直接比较，不是模型的全局评分。后续评测只会生成含新估计的策略草稿。",
              },
            );
            const severeError = fieldInput(
              candidateFields,
              "当前严重错误率（%）",
              `auto-route-${routeIndex}-candidate-${candidateIndex}-severe-error`,
              basisPointsToPercent(candidate.severe_error_rate_bps),
              {
                type: "number", min: "0", max: "100", step: "0.01",
                description: "该模型在当前 Route 下的初始严重错误率，用于和上限直接比较。它是硬门槛，不会被低价格或高平均质量抵消。",
              },
            );
						const stability = fieldInput(
							candidateFields,
							"当前稳定性估计（%）",
							`auto-route-${routeIndex}-candidate-${candidateIndex}-stability`,
							basisPointsToPercent(candidate.stability_score_bps),
							{
								type: "number", min: "0", max: "100", step: "0.01",
								description: "当前 Route 下的稳定性先验；积累足够真实请求与评审证据后，学习草稿会更新为保守下界。",
							},
						);
						const expectedLatency = fieldInput(
							candidateFields,
							"预期延迟（ms）",
							`auto-route-${routeIndex}-candidate-${candidateIndex}-latency`,
							candidate.expected_latency_ms,
							{
								type: "number", min: "0", step: "1",
								description: "端到端回答延迟的初始估计；0 表示未知。在线学习会用真实请求平均延迟更新。",
							},
						);
            const removeCandidate = actionButton("删除候选", "button-danger");
            removeCandidate.addEventListener("click", () => {
              syncAutoRoutes();
              working.config.auto_routing.strategy.routes[routeIndex].candidates.splice(candidateIndex, 1);
              renderAutoRoutes();
            });
						candidateControls.push({ model, productionEligible, quality, stability, severeError, expectedLatency });
            candidateRow.append(candidateFields, removeCandidate);
            return candidateRow;
          });
          candidatesList.replaceChildren(...candidateRows);
        };
        renderCandidates();
        const addCandidate = actionButton("添加候选", "button-secondary");
        addCandidate.addEventListener("click", () => {
          syncAutoRoutes();
          working.config.auto_routing.strategy.routes[routeIndex].candidates.push({
            model: participantModelIDs()[0] || "",
            production_eligible: false,
            quality_score_bps: 9000,
						stability_score_bps: 9000,
            severe_error_rate_bps: 0,
						expected_latency_ms: 0,
          });
          renderAutoRoutes();
        });
        const removeRoute = actionButton("删除 Route", "button-danger");
        removeRoute.addEventListener("click", () => {
          syncAutoRoutes();
          working.config.auto_routing.strategy.routes.splice(routeIndex, 1);
          renderAutoRoutes();
        });
        id.addEventListener("input", refreshRouteChoices);
        row.append(legend, fields, candidatesList, addCandidate, removeRoute);
				autoRouteRows.push({
					id, minQuality, minStability, maxSevereError,
					qualityWeight, stabilityWeight, costWeight, performanceWeight,
					candidates: candidateControls,
				});
        return row;
      },
    );
    routeList.replaceChildren(...rows);
    refreshRouteChoices();
  }

  renderAutoRoutes();
  const addRoute = actionButton("添加 Route", "button-secondary");
  addRoute.addEventListener("click", () => {
    syncAutoRoutes();
    const index = working.config.auto_routing.strategy.routes.length + 1;
    working.config.auto_routing.strategy.routes.push({
      id: `route_${index}`,
      min_quality_bps: 9000,
			min_stability_bps: 8000,
      max_severe_error_rate_bps: 100,
			weights: {
				quality_bps: 4000, stability_bps: 2500,
				cost_bps: 2500, performance_bps: 1000,
			},
      candidates: [],
    });
    renderAutoRoutes();
  });
  strategySection.append(routeList, addRoute);

  const mappingSection = editorSubsection("任务映射");
  const mappingHelp = textElement("p", "把任务分析模型返回的任务类型分配给 Route。例如 simple → fast、coding → quality；未配置或未匹配的类型使用默认 Route。");
  mappingHelp.className = "field-help";
  const taskRouteList = element("div", "stack auto-task-route-list");

  function syncTaskRoutes() {
    working.config.auto_routing.strategy.task_routes = taskRouteRows.map((row) => ({
      task_type: row.taskType.value,
	  difficulty: row.difficulty.value,
      route: row.route.value,
    }));
  }

  function renderTaskRoutes() {
    taskRouteRows = [];
    const choices = [["", "请选择"], ...routeIDs().map((id) => [id, id])];
    const rows = working.config.auto_routing.strategy.task_routes.map((mapping, index) => {
      const row = element("div", "card auto-task-route-row");
      const fields = element("div", "form-grid");
	  const taskType = fieldSelect(
		fields,
		"任务类型",
		`auto-task-${index}-type`,
		mapping.task_type || "",
		[["", "不限"], ...Object.entries(routingTaskLabels)],
		{ description: "固定七类之一；可留空，仅按难度匹配。" },
	  );
	  const difficulty = fieldSelect(
		fields,
		"难度",
		`auto-task-${index}-difficulty`,
		mapping.difficulty || "",
		[["", "不限"], ["easy", "简单"], ["medium", "中等"], ["hard", "困难"]],
		{
		  description: "可单独按难度映射，也可与任务类型组合。匹配顺序：任务+难度、任务、难度、默认 Route。",
		},
	  );
      const route = fieldSelect(fields, "Route", `auto-task-${index}-route`, mapping.route, choices, {
        description: "该任务类型采用哪个 Route 的候选集合、质量门槛和严重错误门槛。",
      });
      const remove = actionButton("删除映射", "button-danger");
      remove.addEventListener("click", () => {
        syncTaskRoutes();
        working.config.auto_routing.strategy.task_routes.splice(index, 1);
        renderTaskRoutes();
      });
	  taskRouteRows.push({ taskType, difficulty, route });
      row.append(fields, remove);
      return row;
    });
    taskRouteList.replaceChildren(...rows);
  }

  renderTaskRoutes();
  const addTaskRoute = actionButton("添加任务映射", "button-secondary");
  addTaskRoute.addEventListener("click", () => {
    syncTaskRoutes();
	working.config.auto_routing.strategy.task_routes.push({ task_type: "", difficulty: "", route: defaultRoute.value });
    renderTaskRoutes();
  });
  mappingSection.append(mappingHelp, taskRouteList, addTaskRoute);

  const budgetSection = editorSubsection("统一尝试预算");
  const budgetGrid = element("div", "form-grid");
  const budget = strategyBudgetDraft(
    working.config.auto_routing.strategy.budget,
  );
  working.config.auto_routing.strategy.budget = budget;
  const budgetFields = {
    max_answer_attempts: fieldInput(budgetGrid, "主回答尝试上限", "auto-budget-answer", budget.max_answer_attempts, {
      type: "number", min: "1", description: "首次主回答、重试和模型切换合计允许的回答次数。",
    }),
    max_auxiliary_calls: fieldInput(budgetGrid, "辅助调用上限", "auto-budget-auxiliary", budget.max_auxiliary_calls, {
      type: "number", min: "1", description: "任务分析和实际未命中缓存的视觉调用合计上限。",
    }),
    max_total_outbound_calls: fieldInput(budgetGrid, "总上游调用上限", "auto-budget-total", budget.max_total_outbound_calls, {
      type: "number", min: "2", description: "当前在线请求发出的所有上游调用总数上限。",
    }),
    max_retries_per_target: fieldInput(budgetGrid, "单节点重试上限", "auto-budget-retries", budget.max_retries_per_target, {
      type: "number", min: "0", description: "同一部署发生可重试错误时允许追加的次数。",
    }),
    max_model_switches: fieldInput(budgetGrid, "模型切换上限", "auto-budget-model-switches", budget.max_model_switches, {
      type: "number", min: "0", description: "回答尚未提交给客户端前，最多允许切换多少次主模型。",
    }),
    deadline: fieldInput(budgetGrid, "请求总截止时间", "auto-budget-deadline", budget.deadline, {
      description: "任务分析、视觉、主回答和重试共享的总时限，例如 2m。",
    }),
    max_worst_case_cost_micro_usd: fieldInput(
      budgetGrid,
      "最坏费用上限（美元）",
      "auto-budget-cost",
      microUSDToUSDInput(budget.max_worst_case_cost_micro_usd),
      {
        type: "number", min: "0", step: "0.000001",
        description: "执行计划在最坏尝试次数下的费用上限，直接填写美元金额；0 表示不限制。",
      },
    ),
  };
  budgetSection.append(budgetGrid);

	const invokeStrategyAction = (action) => {
		void runEditorAction(alert, action).catch(() => {});
	};
	const generationPanel = element("div", "card stack strategy-generator-panel");
	const generationDescription = currentActiveStrategy
		? "根据当前模型配置、最新公开评测和本地证据生成一个新草稿，不会改写已有版本或影响正式策略。"
		: "选择侧重点，系统会结合公开评测和本地证据生成一份可编辑策略。";
	generationPanel.append(
		textElement("h4", "智能生成策略"),
		textElement("p", generationDescription),
		textElement(
			"p",
			"公开评测只用于冷启动排序和 Shadow 候选；只有可靠本地证据通过质量与稳定性门槛后，候选才可获得生产流量。",
			"muted",
		),
	);
	let evaluationCatalog = source?.evaluation_catalog || null;
	const catalogOverview = textElement("p", "", "strategy-catalog-overview");
	const catalogStatus = element("div", "stack strategy-catalog-status");
	const catalogMetadata = element("div", "stack strategy-catalog-metadata");
	const catalogProgress = element("div", "stack strategy-catalog-progress");
	const renderCatalogOverview = () => {
		catalogOverview.className = "strategy-catalog-overview";
		if (!evaluationCatalog) {
			catalogOverview.textContent = working.id
				? "公开评测状态暂不可用 · 仍可手动配置"
				: "保存 Profile 后可使用智能生成";
			catalogOverview.className += " is-warning";
			return;
		}
		const resultCount = (evaluationCatalog.results || []).length;
		const coverage = evaluationCatalogCoverage(
			evaluationCatalog,
			working.config.models,
		);
		const covered = coverage.summary.full + coverage.summary.partial;
		if (coverage.summary.total === 0) {
			catalogOverview.textContent = "请先录入模型，再生成智能路由策略";
			catalogOverview.className += " is-warning";
			return;
		}
		if (resultCount === 0) {
			catalogOverview.textContent = "公开评测尚未下载 · 仍可生成基础策略";
			catalogOverview.className += " is-warning";
			return;
		}
		catalogOverview.textContent = `公开评测可用 · 已覆盖 ${covered}/${coverage.summary.total} 个模型`;
		catalogOverview.className += covered > 0 ? " is-ready" : " is-warning";
	};
	const renderCatalogMetadata = () => {
		renderCatalogOverview();
		catalogMetadata.replaceChildren();
		if (evaluationCatalog) {
			const sources = evaluationCatalog.sources || [];
			const resultCount = (evaluationCatalog.results || []).length;
			const coverage = evaluationCatalogCoverage(
				evaluationCatalog,
				working.config.models,
			);
			const covered = coverage.summary.full + coverage.summary.partial;
			const loadedSummary = resultCount > 0
				? `已载入 ${sources.length} 个公开评测来源，共 ${resultCount} 条结果。`
				: `已载入 ${sources.length} 个公开评测来源定义，尚未下载结果。`;
			let coverageSummary = `${covered} / ${coverage.summary.total} 个模型有公开评测覆盖` +
				`（完整 ${coverage.summary.full}、部分 ${coverage.summary.partial}、` +
				`缺失 ${coverage.summary.missing}、未确认身份 ${coverage.summary.unmapped}）。`;
			if (coverage.summary.total === 0) {
				coverageSummary = "当前 Profile 尚未录入模型。";
			} else if (coverage.summary.unmapped === coverage.summary.total) {
				coverageSummary = `${coverage.summary.unmapped} 个模型尚未确认标准身份，暂时无法匹配公开评测。`;
			}
			const sourceDetails = element("details", "strategy-catalog-sources");
			sourceDetails.append(textElement("summary", "查看数据来源与版本"));
			const sourceList = element("div", "stack strategy-catalog-source-list");
			for (const source of sources) {
				const rawVersion = source.version || "";
				let displayVersion = rawVersion || "未知版本";
				if (rawVersion === "not-imported") {
					displayVersion = "尚未下载";
				} else if (rawVersion.length > 16) {
					const [revision, ...suffix] = rawVersion.split(":");
					displayVersion = `${revision.slice(0, 8)}${suffix.length ? ` · ${suffix.join(":")}` : ""}`;
				}
				const sourceVersion = textElement(
					"p",
					`${source.name || source.id} · ${displayVersion}`,
				);
				if (rawVersion) sourceVersion.setAttribute("title", rawVersion);
				sourceList.append(sourceVersion);
			}
			sourceDetails.append(sourceList);
			catalogMetadata.append(
				textElement("p", loadedSummary),
				textElement("p", coverageSummary),
				textElement(
					"p",
					resultCount > 0
						? "需要先在模型设置中确认 canonical ID，系统才会应用对应评测；不会按模型家族猜测。"
						: "可以先生成可编辑草稿；缺失评测时使用系统临时值并优先安排本地验证。",
				),
				sourceDetails,
			);
			catalogMetadata.children[1].className = "muted";
			catalogMetadata.children[2].className = "muted";
		} else {
			const unavailable = textElement(
				"p",
				working.id
					? "暂时无法读取公开评测目录状态，请重新载入后再试。"
					: "保存 Profile 后即可读取公开评测目录并智能生成策略。",
			);
			unavailable.className = "warning-banner";
			catalogMetadata.append(unavailable);
		}
	};
	renderCatalogMetadata();
	const advancedSettings = element("details", "strategy-generator-advanced");
	const sourceNamesByID = new Map(
		(evaluationCatalog?.sources || []).map((item) => [item.id, item.name || item.id]),
	);
	const progressLabels = {
		idle: "等待", running: "下载并解析", succeeded: "完成",
		failed: "失败", cancelled: "已取消",
	};
	const renderCatalogProgress = (job) => {
		const sources = job?.sources || [];
		const completed = sources.filter((source) =>
			["succeeded", "failed", "cancelled"].includes(source.state)
		).length;
		const percent = sources.length > 0 ? Math.round((completed / sources.length) * 100) : 0;
		const progressStateLabels = {
			succeeded: "公开评测更新完成",
			failed: "公开评测更新失败",
			cancelled: "公开评测更新已取消",
		};
		const progressLabel = progressStateLabels[job?.state]
			? `${progressStateLabels[job.state]} · ${completed} / ${sources.length} 个来源`
			: `正在更新公开评测 · 已处理 ${completed} / ${sources.length} 个来源`;
		const header = textElement("p", progressLabel);
		header.className = "strategy-catalog-progress-label";
		const track = element("div", "strategy-catalog-progress-track");
		track.setAttribute("role", "progressbar");
		track.setAttribute("aria-label", "公开评测更新进度");
		track.setAttribute("aria-valuemin", "0");
		track.setAttribute("aria-valuemax", "100");
		track.setAttribute("aria-valuenow", String(percent));
		const fill = element("span", "strategy-catalog-progress-fill");
		fill.setAttribute("style", `width: ${percent}%`);
		track.append(fill);
		catalogProgress.replaceChildren(
			header,
			track,
			...sources.map((sourceProgress) => {
				const sourceLine = textElement(
					"p",
					`${sourceNamesByID.get(sourceProgress.id) || sourceProgress.id} · ` +
						`${progressLabels[sourceProgress.state] || sourceProgress.stage || "等待"} · ` +
						`导入 ${Number(sourceProgress.imported || 0)} · ` +
						`未映射 ${Number(sourceProgress.skipped_unmapped || 0)}`,
				);
				sourceLine.className = `muted strategy-catalog-source-progress is-${sourceProgress.state || "idle"}`;
				return sourceLine;
			}),
		);
	};
	const reloadCatalog = actionButton("下载并更新公开评测", "button-secondary");
	reloadCatalog.className += " evaluation-catalog-update";
	reloadCatalog.disabled = !working.id || typeof actions.updateEvaluationCatalog !== "function";
	reloadCatalog.addEventListener("click", () => {
		if (reloadCatalog.disabled) return;
		advancedSettings.open = true;
		reloadCatalog.disabled = true;
		reloadCatalog.className = "button button-secondary evaluation-catalog-update is-loading";
		reloadCatalog.textContent = "正在更新公开评测…";
		reloadCatalog.setAttribute("aria-busy", "true");
		renderCatalogProgress({
			state: "running",
			sources: (evaluationCatalog?.sources || []).map((source) => ({
				id: source.id, state: "idle", stage: "queued",
			})),
		});
		return runEditorAction(alert, async () => {
			const job = await actions.updateEvaluationCatalog?.(renderCatalogProgress);
			if (job?.catalog) {
				evaluationCatalog = job.catalog;
				renderCatalogMetadata();
			}
			return job;
		}).catch(() => {}).finally(() => {
			reloadCatalog.disabled = false;
			reloadCatalog.className = "button button-secondary evaluation-catalog-update";
			reloadCatalog.textContent = "下载并更新公开评测";
			reloadCatalog.setAttribute("aria-busy", "false");
		});
	});
	catalogStatus.append(catalogMetadata, catalogProgress, reloadCatalog);
	const generationPrimaryGrid = element("div", "form-grid strategy-generation-primary");
	const generationObjective = fieldSelect(
		generationPrimaryGrid,
		"优化目标",
		"strategy_generation_objective",
		refreshIntent.objective || "balanced",
		[
			["balanced", "均衡"],
			["quality", "质量优先"],
			["cost", "成本优先"],
			["latency", "延迟优先"],
		],
		{ description: "决定质量、稳定性、成本和延迟的初始权重。" },
	);
	const generationAdvancedGrid = element("div", "form-grid strategy-generation-grid");
	const generationMaxCost = fieldInput(
		generationAdvancedGrid,
		"单请求费用上限（美元）",
		"strategy_generation_max_cost_usd",
		Number.isSafeInteger(refreshIntent.max_cost_per_request_micro_usd)
			? microUSDToUSDInput(refreshIntent.max_cost_per_request_micro_usd)
			: "",
		{ type: "number", min: "0", step: "0.000001", description: "选填，最多 6 位小数。" },
	);
	const generationLatency = fieldInput(
		generationAdvancedGrid,
		"目标延迟（ms）",
		"strategy_generation_latency_ms",
		Number.isSafeInteger(refreshIntent.latency_target_ms)
			? String(refreshIntent.latency_target_ms)
			: "",
		{ type: "number", min: "1", step: "1", description: "选填，作为延迟优先的生成约束。" },
	);
	const generationDailyEval = fieldInput(
		generationAdvancedGrid,
		"每日验证预算（美元）",
		"strategy_generation_daily_eval_usd",
		Number.isSafeInteger(refreshIntent.daily_eval_budget_micro_usd)
			? microUSDToUSDInput(refreshIntent.daily_eval_budget_micro_usd)
			: microUSDToUSDInput(
				working.config.auto_routing.dynamic_optimization.daily_budget_micro_usd,
			),
		{ type: "number", min: "0", step: "0.000001", description: "选填，生成后仍可手动调整。" },
	);
	const generationActionLabel = currentActiveStrategy
		? "重新生成新草稿"
		: "生成可编辑策略";
	const generateIntelligent = actionButton(
		generationActionLabel,
		currentActiveStrategy ? "button-secondary" : "button",
	);
	generateIntelligent.disabled = !working.id || typeof actions.generateStrategy !== "function";
	const generationIntent = () => {
			const latency = optionalNonnegativeSafeInteger(
				generationLatency.value,
				"目标延迟",
			);
			if (latency === 0) {
				throw new Error("目标延迟必须大于 0。");
			}
			const maxCost = parseUSDToMicroUSD(
				generationMaxCost.value,
				"单请求费用上限",
				{ optional: true },
			);
			const dailyEval = parseUSDToMicroUSD(
				generationDailyEval.value,
				"每日验证预算",
				{ optional: true },
			);
			return {
				objective: generationObjective.value,
				participants: participantModelIDs().length > 0
					? participantModelIDs()
					: currentModelIDs(),
				...(maxCost === null ? {} : { max_cost_per_request_micro_usd: maxCost }),
				...(latency === null ? {} : { latency_target_ms: latency }),
				...(dailyEval === null ? {} : { daily_eval_budget_micro_usd: dailyEval }),
			};
	};
	const generateStrategyDraft = async () => {
		const intent = generationIntent();
		await runEditorAction(alert, () => actions.generateStrategy?.(intent));
		showConfigurationMode(
			"manual",
			"推荐草稿已生成，可以直接调整后保存。",
		);
	};
	generateIntelligent.addEventListener("click", async () => {
		if (generateIntelligent.disabled) {
			return;
		}
		try {
			await generateStrategyDraft();
		} catch (error) {
			if (alert.hidden) {
				alert.textContent = error?.message || "请求失败，请重试。";
				alert.hidden = false;
			}
		}
	});
	const chooseManual = actionButton("直接手动配置", "button-secondary");
	const generationActions = element("div", "cluster strategy-generation-actions");
	generationActions.append(generateIntelligent, chooseManual);
	const advancedContent = element("div", "stack details-content strategy-generator-advanced-content");
	advancedContent.append(
		generationAdvancedGrid,
		textElement("h5", "公开评测数据管理"),
		catalogStatus,
	);
	advancedSettings.append(
		textElement("summary", "高级设置（可选）"),
		advancedContent,
	);
	generationPanel.append(
		catalogOverview,
		generationPrimaryGrid,
		generationActions,
		advancedSettings,
	);

	const configurationSection = editorSubsection("新策略配置");
	let configurationMode = source?.strategy_configuration_mode === "manual" || editingStrategyID
		? "manual"
		: "intelligent";
	const configuredRoutes = Array.isArray(working.config.auto_routing.strategy.routes)
		? working.config.auto_routing.strategy.routes
		: [];
	const configuredTaskRoutes = Array.isArray(working.config.auto_routing.strategy.task_routes)
		? working.config.auto_routing.strategy.task_routes
		: [];
	const configuredTaskTypes = [...new Set(
		configuredTaskRoutes.map((item) => String(item?.task_type || "")).filter(Boolean),
	)];
	const configuredCandidates = [...new Set(
		configuredRoutes.flatMap((route) =>
			(route.candidates || []).map((candidate) => String(candidate?.model || ""))
		).filter(Boolean),
	)];
	const activeCandidates = [...new Set(
		(currentActiveStrategy?.config?.routes || []).flatMap((route) =>
			(route.candidates || []).map((candidate) => String(candidate?.model || ""))
		).filter(Boolean),
	)];
	const configuredTaskRouteGroups = strategyTaskRouteGroups(configuredTaskRoutes);
	const strategySummary = element("div", "card stack strategy-recommendation-summary");
	const summaryList = element("ul", "strategy-recommendation-list");
	summaryList.append(
		textElement(
			"li",
			`未匹配到特定任务时使用：${routingGroupLabel(working.config.auto_routing.strategy.default_route)}`,
		),
		textElement(
			"li",
			`新策略只会在这些模型中选择：${configuredCandidates.length > 0 ? configuredCandidates.join("、") : "未设置"}`,
		),
		textElement(
			"li",
			`困难、高风险或其他模型不可用时使用：${working.config.auto_routing.strong_baseline_model || "未设置"}`,
		),
	);
	const taskRouteSummary = element("div", "strategy-task-route-summary");
	taskRouteSummary.append(textElement("h5", "任务路由"));
	if (configuredTaskRouteGroups.length === 0) {
		taskRouteSummary.append(textElement(
			"p",
			"未单独设置任务路由，全部使用默认 Route。",
			"muted",
		));
	} else {
		const taskRouteRows = element("div", "strategy-task-route-rows");
		for (const group of configuredTaskRouteGroups) {
			const difficultyText = group.difficulties.map((difficulty) =>
				routingDifficultyLabels[difficulty] || difficulty || "所有难度"
			).join("、");
			const taskLabels = group.taskTypes.map((taskType) =>
				(routingTaskLabels[taskType] || taskType || "所有任务").replace(/任务$/, "")
			);
			const row = element("div", "strategy-task-route-row");
			row.append(
				textElement("strong", `${difficultyText} → ${routingGroupLabel(group.route)}`),
				textElement(
					"span",
					`${taskLabels.length} 类任务：${taskLabels.join("、")}`,
					"muted",
				),
			);
			taskRouteRows.append(row);
		}
		taskRouteSummary.append(taskRouteRows);
	}
	const generationNotice = textElement(
		"p",
		String(source?.strategy_generation_notice || "下面是待发布的新策略，当前线上请求不受影响。"),
	);
	generationNotice.className = source?.strategy_generation_notice
		? "success-banner strategy-generation-notice"
		: "muted strategy-generation-notice";
	const editingCanary = Number(strategyOverview?.snapshot?.canary?.id || 0) === editingStrategyID;
	const activeStrategyName = currentActiveStrategy?.config?.alias ||
		currentActiveStrategy?.config?.name || "当前 Active 策略";
	const draftState = element("div", "strategy-state-comparison");
	if (editingStrategyID) {
		const onlineState = element("div", "strategy-state-item is-active");
		onlineState.append(
			textElement("span", "① 当前线上 · 正在使用", "strategy-state-label"),
			textElement("strong", activeCandidates.length > 0 ? activeCandidates.join("、") : "未设置模型"),
			textElement(
				"span",
				`策略：${activeStrategyName}`,
				"muted",
			),
		);
		const candidateState = element("div", "strategy-state-item is-draft");
		candidateState.append(
			textElement(
				"span",
				editingCanary ? "② 新策略 · 正在灰度" : "② 新策略草稿 · 尚未生效",
				"strategy-state-label",
			),
			textElement("strong", configuredCandidates.length > 0 ? configuredCandidates.join("、") : "未设置模型"),
			textElement(
				"span",
				editingCanary
					? `${basisPointsToPercent(strategyOverview?.snapshot?.canary_bps || 0)}% 请求使用新策略，其余仍使用线上策略。`
					: "当前没有请求使用这份策略。",
				"muted",
			),
		);
		const nextState = element("div", "strategy-state-item is-next");
		nextState.append(
			textElement("span", "③ 下一步", "strategy-state-label"),
			textElement("strong", editingCanary ? "验证正常后发布" : "保存修改并开始灰度"),
			textElement(
				"span",
				"只有发布为正式策略后，所有请求才会切换到新模型。",
				"muted",
			),
		);
		draftState.append(onlineState, candidateState, nextState);
	} else {
		draftState.append(textElement(
			"p",
			strategyOverview
				? "当前修改尚未保存；保存后会创建新规则草稿，不会直接替换线上 Active 策略。"
				: "首次保存后会创建当前 Profile 的正式策略。",
		));
	}
	const returnToGenerator = actionButton("重新生成草稿", "button-secondary");
	const hasStrategyLifecycle = Boolean(working.id && strategyOverview);
	const saveStrategy = actionButton(
		editingStrategyID
			? "保存草稿修改"
			: hasStrategyLifecycle ? "保存为新草稿" : "保存并启用策略",
		"button",
	);
	saveStrategy.type = hasStrategyLifecycle ? "button" : "submit";
	saveStrategy.hidden = actions.editorSection !== "routing";
	const persistStrategyDraft = async () => {
		saveStrategy.disabled = true;
		try {
			await runEditorAction(alert, async () => {
				syncForm();
				const config = autoRoutingPayload(
					working.config.auto_routing,
				).strategy;
				if (editingStrategyID) {
					await actions.updateStrategy?.(editingStrategyID, config);
					return;
				}
				await actions.createStrategy?.({ ...config, name: "" });
			});
		} catch {
		} finally {
			saveStrategy.disabled = false;
		}
	};
	if (hasStrategyLifecycle) {
		saveStrategy.addEventListener("click", persistStrategyDraft);
	}
	const summaryActions = element("div", "cluster strategy-manual-actions");
	summaryActions.append(saveStrategy, returnToGenerator);
	strategySummary.append(
		textElement("h4", "待发布的新策略"),
		generationNotice,
		draftState,
		textElement(
			"p",
			`包含 ${configuredRoutes.length} 个模型组，覆盖 ${configuredTaskTypes.length} 类任务。`,
			"strategy-recommendation-lead",
		),
		summaryList,
		taskRouteSummary,
		summaryActions,
	);
	const configurationDetails = element("details", "strategy-configuration-details");
	const configurationDetailsContent = element("div", "stack details-content");
	configurationDetailsContent.append(strategySection, mappingSection, budgetSection);
	configurationDetails.append(
		textElement("summary", "调整详细配置"),
		configurationDetailsContent,
	);
	const manualPanel = element("div", "stack strategy-manual-panel");
	manualPanel.append(strategySummary, configurationDetails);
	function showConfigurationMode(mode, notice = "") {
		configurationMode = mode;
		if (notice) {
			generationNotice.textContent = notice;
			generationNotice.className = "success-banner strategy-generation-notice";
		}
		generationNotice.hidden = mode !== "manual";
		const intelligent = configurationMode === "intelligent";
		generationPanel.hidden = !intelligent;
		manualPanel.hidden = intelligent;
	}
	chooseManual.addEventListener("click", () => showConfigurationMode("manual"));
	returnToGenerator.addEventListener("click", () => showConfigurationMode("intelligent"));
	showConfigurationMode(configurationMode);
	configurationSection.append(generationPanel, manualPanel);

	const lifecycleSection = editorSubsection("策略版本");
	const lifecycleSnapshot = strategyOverview?.snapshot || {};
	const activeStrategy = lifecycleSnapshot.active;
	const strategyUsage = element("div", "card stack strategy-usage-panel");
	const strategyUsageHeader = element("div", "cluster strategy-usage-header");
	strategyUsageHeader.append(
		textElement("h4", "当前使用状态"),
		badge(
			working.config.auto_routing.enabled && activeStrategy ? "已生效" : "未生效",
			working.config.auto_routing.enabled && activeStrategy ? "badge-success" : "badge-warning",
		),
	);
	strategyUsage.append(strategyUsageHeader);
	if (working.config.auto_routing.enabled && activeStrategy) {
		strategyUsage.append(
			textElement(
				"p",
				`当前正式策略：${activeStrategy.config?.alias || activeStrategy.config?.name || "未命名策略"}`,
			),
			textElement(
				"p",
				"请求中的 model 设置为 auto 时使用当前正式策略；指定具体模型时会直接转发。",
				"muted",
			),
			textElement("code", '"model": "auto"', "strategy-usage-code"),
		);
	} else {
		strategyUsage.append(textElement(
			"p",
			working.config.auto_routing.enabled
				? "保存当前 Profile 后，首个策略才会正式生效。"
				: "先启用并保存智能路由，客户端才能使用 model=auto。",
			"muted",
		));
	}
	lifecycleSection.append(strategyUsage);
	if (!working.id) {
		const initialHelp = textElement(
			"p",
			"首次保存 Profile 时，当前策略会作为第一个 active 版本。之后的修改通过草稿、评估、灰度和发布流程完成。",
		);
		initialHelp.className = "field-help";
		lifecycleSection.append(initialHelp);
	} else if (!strategyOverview) {
		const disabledHelp = textElement(
			"p",
			"启用并保存智能路由后，才能管理独立策略版本。",
		);
		disabledHelp.className = "field-help";
		lifecycleSection.append(disabledHelp);
	} else {
		const snapshot = lifecycleSnapshot;
		const lifecycleHelp = textElement(
			"p",
			"新策略不会自动替换当前正式策略。确认草稿后先灰度验证，正常后再发布为正式策略。",
		);
		lifecycleHelp.className = "field-help";
		const lifecycleStepsTitle = textElement("h4", "新策略生效步骤");
		const lifecycleSteps = element("ol", "strategy-lifecycle-steps");
		for (const step of [
			"1. 编辑并保存草稿",
			"2. 灰度验证",
			"3. 发布为正式策略",
		]) {
			lifecycleSteps.append(textElement("li", step));
		}
		const summary = element("div", "cluster strategy-lifecycle-summary");
		summary.append(
			badge(`revision ${snapshot.revision || 0}`, ""),
			badge(
				`active：${snapshot.active?.config?.alias || snapshot.active?.config?.name || "-"}`,
				"badge-success",
			),
		);
		if (snapshot.canary) {
			summary.append(
				badge(
					`灰度：${basisPointsToPercent(snapshot.canary_bps)}%`,
					"badge-warning",
				),
			);
		}
		const learningPanel = element("div", "stack strategy-learning-panel");
		if (working.config.auto_routing.dynamic_optimization.enabled) {
			const learningHelp = textElement(
				"p",
				"对比学习只积累匿名质量证据。可靠样本可生成新的草稿，但不会修改 active 策略。",
			);
			learningHelp.className = "field-help";
			const evaluationBudget = strategyOverview.evaluation_budget || {};
			const dailyBudget = Number(
				working.config.auto_routing.dynamic_optimization.daily_budget_micro_usd || 0,
			);
			const spent = Number(evaluationBudget.spent_micro_usd || 0);
			const reserved = Number(evaluationBudget.reserved_micro_usd || 0);
			const budgetSummary = textElement(
				"p",
				`今日评测：${formatMicroUSD(spent)} / ${formatMicroUSD(dailyBudget)}`,
			);
			budgetSummary.className = "muted";
			const reservedSummary = reserved > 0
				? textElement("p", `已预留：${formatMicroUSD(reserved)}`)
				: null;
			if (reservedSummary) {
				reservedSummary.className = "muted";
			}
			const evidenceRows = strategyOverview.quality_estimates || [];
			const stabilityRows = strategyOverview.stability_estimates || [];
			const stabilityKeys = new Set(stabilityRows
				.filter((estimate) => estimate.reliable)
				.map((estimate) => `${estimate.strategy}\u0000${estimate.route}\u0000${estimate.candidate_model}\u0000${estimate.reference_model}`));
			const reliableCount = evidenceRows.filter((estimate) => estimate.reliable && stabilityKeys.has(
				`${estimate.strategy}\u0000${estimate.route}\u0000${estimate.candidate_model}\u0000${estimate.reference_model}`,
			)).length;
			const evidenceSummary = textElement(
				"p",
				`模型表现：${evidenceRows.length} 个质量分组、${stabilityRows.length} 个稳定性分组，其中 ${reliableCount} 个可生成学习草稿。`,
			);
			evidenceSummary.className = "muted";
			const performanceLink = textElement("a", "查看模型表现");
			performanceLink.setAttribute(
				"href",
				`/_admin/stats/models?profile_id=${Number(working.id)}`,
			);
			const generateCandidate = actionButton("生成学习候选", "button-secondary");
			generateCandidate.disabled = reliableCount === 0;
			generateCandidate.addEventListener("click", () => {
				if (!generateCandidate.disabled) {
					invokeStrategyAction(() => actions.generateStrategyCandidate?.());
				}
			});
			learningPanel.append(
				learningHelp,
				budgetSummary,
				...(reservedSummary ? [reservedSummary] : []),
				evidenceSummary,
				performanceLink,
				generateCandidate,
			);
		}

		const generations = new Map(
			(strategyOverview.generations || []).map((generation) => [
				Number(generation.strategy_id), generation,
			]),
		);
		const allVersions = strategyOverview.strategies || [];
		const activeID = Number(snapshot.active?.id || 0);
		const canaryID = Number(snapshot.canary?.id || 0);
		const rollbackID = Number(snapshot.last_known_good?.id || 0);
		const currentChange = snapshot.canary || allVersions.find((version) =>
			!version.archived_at &&
			![activeID, canaryID, rollbackID].includes(Number(version.id)) &&
			["draft", "evaluating", "ready"].includes(version.state)
		) || null;
		const currentChangeID = Number(currentChange?.id || 0);
		const historyVersions = allVersions.filter((version) =>
			![activeID, canaryID, rollbackID, currentChangeID].includes(Number(version.id))
		);

		const draftControls = element("div", "cluster strategy-draft-controls");
		const saveDraft = actionButton(
			editingStrategyID ? "更新当前草稿" : "保存为新草稿",
			"button-secondary",
		);
		saveDraft.hidden = actions.editorSection === "routing" || Boolean(currentChange && !editingStrategyID);
		saveDraft.addEventListener("click", persistStrategyDraft);
		draftControls.append(saveDraft);
		if (editingStrategyID) {
			const stopEditing = actionButton("退出草稿编辑", "button-secondary");
			stopEditing.addEventListener("click", () =>
				invokeStrategyAction(() => actions.editStrategy?.(0))
			);
			draftControls.append(stopEditing);
		}

		function archiveControl(version, label) {
			const archive = actionButton(label, "button-secondary");
			archive.disabled = typeof actions.archiveStrategy !== "function";
			archive.addEventListener("click", () => {
				if (!archive.disabled) {
					invokeStrategyAction(() => actions.archiveStrategy?.(version.id));
				}
			});
			return archive;
		}

		function versionCard(version, context) {
			const card = element("article", "card strategy-version-card stack");
			card.className += context === "current"
				? " strategy-current-version"
				: context === "rollback"
					? " strategy-rollback-card"
					: " strategy-history-version";
			const header = element("div", "cluster strategy-version-header");
			const title = textElement(
				"h4",
				`${context === "current" ? "待发布：" : ""}${version.config?.alias || version.config?.name || `策略 ${version.id}`}`,
			);
			const state = badge(
				version.archived_at
					? "已废弃"
					: context === "current" && version.state === "draft"
						? "草稿 · 尚未生效"
						: context === "current" && version.state === "ready"
							? "已确认 · 等待灰度"
							: context === "current" && version.state === "canary"
								? "正在灰度"
								: strategyStateLabel(version.state),
				version.archived_at ? "" : strategyStateBadge(version.state),
			);
			header.append(title, state);
			if (version.rating?.grade) {
				header.append(badge(
					`质量 ${version.rating.grade}`,
					strategyGradeBadge(version.rating.grade),
				));
			}
			const generation = generations.get(Number(version.id));
			if (generation) {
				header.append(badge("智能生成", "badge-info"));
				if ((generation.manual_overrides || []).length > 0) {
					header.append(badge(
						`手动调整 ${(generation.manual_overrides || []).length} 项`,
						"badge-warning",
					));
				}
			}
			const metadata = textElement(
				"p",
				`${version.config?.name || "-"} · ID ${version.id}`,
			);
			metadata.className = "muted";
			const currentSummary = context === "current"
				? element("div", "stack strategy-current-summary")
				: null;
			if (currentSummary) {
				const models = strategyModelIDs(version.config);
				const isCanary = Number(snapshot.canary?.id || 0) === Number(version.id);
				currentSummary.append(
					textElement("p", `候选模型：${models.length > 0 ? models.join("、") : "未设置"}`),
					textElement(
						"p",
						`当前流量：${isCanary ? basisPointsToPercent(snapshot.canary_bps) : "0"}%`,
						isCanary ? "strategy-canary-traffic" : "muted",
					),
				);
			}
			const rating = version.rating
				? textElement(
					"p",
					`配置估算：${basisPointsToPercent(version.rating.display_score_bps)} 分 · 质量下限 ${basisPointsToPercent(version.rating.quality_floor_bps)}% · 稳定性下限 ${basisPointsToPercent(version.rating.stability_floor_bps)}% · 严重错误上限 ${basisPointsToPercent(version.rating.severe_error_ceiling_bps)}%`,
				)
				: null;
			if (rating) {
				rating.className = "muted";
			}
			const generationDetails = generation
				? element("div", "stack strategy-generation-details")
				: null;
			if (generationDetails) {
				const digest = String(generation.source_digest || "");
				const source = textElement(
					"p",
					`生成器 ${generation.generator_version || "-"} · 数据快照 ${digest.slice(0, 12) || "-"}`,
				);
				source.className = "muted";
				generationDetails.append(source);
				const explanationMessages = new Set(
					(generation.explanations || [])
						.map((explanation) => String(explanation.message || explanation.code || "").trim())
						.filter(Boolean),
				);
				for (const message of explanationMessages) {
					generationDetails.append(textElement("p", message));
				}
				if ((generation.manual_overrides || []).length > 0) {
					const overrides = element("div", "cluster strategy-generation-overrides");
					for (const override of generation.manual_overrides) {
						const field = String(override.path || "").split("/").at(-1) || "字段";
						const restore = actionButton(`恢复推荐：${field}`, "button-secondary");
						restore.disabled = version.state !== "draft" || Boolean(version.archived_at);
						restore.addEventListener("click", () => {
							if (!restore.disabled) {
								invokeStrategyAction(() =>
									actions.restoreGeneratedRecommendation?.(version.id, override.path)
								);
							}
						});
						overrides.append(restore);
					}
					generationDetails.append(overrides);
				}
			}
			const controls = element("div", "cluster strategy-version-actions");
			if (context === "current" && !version.archived_at) {
				if (version.state === "draft") {
					const edit = actionButton("载入草稿", "button-secondary");
					edit.addEventListener("click", () =>
						invokeStrategyAction(() => actions.editStrategy?.(version.id))
					);
					const ready = actionButton("确认并进入灰度准备", "button-secondary");
					ready.addEventListener("click", () =>
						invokeStrategyAction(() =>
							actions.advanceStrategy?.(version.id, "draft", "ready")
						)
					);
					controls.append(edit, ready);
					controls.append(archiveControl(version, "废弃草稿"));
				} else if (version.state === "evaluating") {
					const ready = actionButton("确认并进入灰度准备", "button-secondary");
					ready.addEventListener("click", () =>
						invokeStrategyAction(() =>
							actions.advanceStrategy?.(version.id, "evaluating", "ready")
						)
					);
					controls.append(ready, archiveControl(version, "废弃此版本"));
				} else if (version.state === "ready" && !snapshot.canary) {
					const percent = element("input");
					percent.type = "number";
					percent.min = "0.01";
					percent.max = "99.99";
					percent.step = "0.01";
					percent.value = "10";
					percent.setAttribute("aria-label", "灰度比例（%）");
					const canary = actionButton("开始灰度", "button-secondary");
					canary.addEventListener("click", async () => {
						try {
							const canaryBPS = percentToBasisPoints(percent.value, "灰度比例");
							if (canaryBPS <= 0 || canaryBPS >= 10000) {
								throw new Error("灰度比例必须大于 0% 且小于 100%。");
							}
							await runEditorAction(alert, () =>
								actions.startStrategyCanary?.(
									version.id,
									canaryBPS,
									snapshot.revision,
								),
							);
						} catch (error) {
							if (alert.hidden) {
								alert.textContent = error?.message || "请求失败，请重试。";
								alert.hidden = false;
							}
						}
					});
					controls.append(percent, canary, archiveControl(version, "废弃此版本"));
				}
			} else if (context === "history" && !version.archived_at &&
				["draft", "evaluating", "ready"].includes(version.state)) {
				controls.append(archiveControl(version, "废弃此版本"));
			}
			card.append(header, metadata);
			if (currentSummary) {
				card.append(currentSummary);
			}
			if (context === "current" && (rating || generationDetails)) {
				const technicalDetails = element("details", "stack strategy-current-technical-details");
				technicalDetails.append(textElement("summary", "查看质量估算和生成依据"));
				if (rating) technicalDetails.append(rating);
				if (generationDetails) technicalDetails.append(generationDetails);
				card.append(technicalDetails);
			} else {
				if (rating) card.append(rating);
				if (generationDetails) card.append(generationDetails);
			}
			if (controls.children.length) {
				card.append(controls);
			}
			return card;
		}

		const activeOverview = element("article", "card stack strategy-active-version");
		const activeHeader = element("div", "cluster strategy-version-header");
		activeHeader.append(
			textElement("h4", "线上正在使用"),
			badge(snapshot.canary ? `${basisPointsToPercent(10000 - snapshot.canary_bps)}% 请求` : "100% 请求", "badge-success"),
		);
		const activeConfig = snapshot.active?.config || {};
		const activeModels = strategyModelIDs(activeConfig);
		activeOverview.append(
			activeHeader,
			textElement(
				"p",
				`${activeConfig.alias || activeConfig.name || "未命名策略"} · ${activeConfig.name || "-"} · ID ${snapshot.active?.id || "-"}`,
				"muted",
			),
			textElement("p", `主回答候选：${activeModels.length > 0 ? activeModels.join("、") : "未设置"}`),
			textElement("p", `路由规则：${strategyTaskSummary(activeConfig)}`),
		);
		if (activeConfig.roles?.task_analyzer_model) {
			activeOverview.append(textElement(
				"p",
				`任务分析：${activeConfig.roles.task_analyzer_model}`,
			));
		}
		if (activeConfig.roles?.strong_baseline_model) {
			activeOverview.append(textElement(
				"p",
				`困难、高风险或异常时兜底：${activeConfig.roles.strong_baseline_model}`,
			));
		}
		if (activeConfig.roles?.reviewer_model) {
			activeOverview.append(textElement(
				"p",
				`异步质量评审（不参与主回答选型）：${activeConfig.roles.reviewer_model}`,
			));
		}

		const currentChangePanel = element("div", "stack strategy-current-change");
		currentChangePanel.append(textElement("h4", "线上与待发布"));
		const comparison = element("div", "strategy-version-comparison");
		comparison.append(activeOverview);
		if (currentChange) {
			comparison.append(versionCard(currentChange, "current"));
		} else {
			comparison.append(textElement("p", "当前没有待发布的策略，所有请求都使用左侧线上策略。", "muted"));
		}
		currentChangePanel.append(comparison);
		const rollbackPanel = element("div", "stack strategy-rollback-version");
		rollbackPanel.hidden = !snapshot.last_known_good;
		if (snapshot.last_known_good) {
			rollbackPanel.append(
				textElement("h4", "可回滚版本"),
				versionCard(snapshot.last_known_good, "rollback"),
			);
		}
		const historyDisclosure = element("details", "stack strategy-history-disclosure");
		historyDisclosure.hidden = historyVersions.length === 0;
		if (historyVersions.length > 0) {
			historyDisclosure.append(textElement("summary", `历史版本（${historyVersions.length}）`));
			const historyList = element("div", "stack strategy-version-list");
			const historyCards = historyVersions.map((version) => versionCard(version, "history"));
			let visibleHistory = 10;
			for (const [index, card] of historyCards.entries()) {
				card.hidden = index >= visibleHistory;
				historyList.append(card);
			}
			historyDisclosure.append(historyList);
			if (historyCards.length > visibleHistory) {
				const showMore = actionButton(
					`再显示 ${Math.min(10, historyCards.length - visibleHistory)} 个历史版本`,
					"button-secondary",
				);
				showMore.addEventListener("click", () => {
					visibleHistory = Math.min(visibleHistory + 10, historyCards.length);
					for (const [index, card] of historyCards.entries()) {
						card.hidden = index >= visibleHistory;
					}
					const remaining = historyCards.length - visibleHistory;
					showMore.hidden = remaining === 0;
					if (remaining > 0) {
						showMore.textContent = `再显示 ${Math.min(10, remaining)} 个历史版本`;
					}
				});
				historyDisclosure.append(showMore);
			}
		}

		const publicationControls = element(
			"div",
			"cluster strategy-publication-actions",
		);
		if (snapshot.canary) {
			const cancelCanary = actionButton("取消灰度", "button-secondary");
			cancelCanary.addEventListener("click", () =>
				invokeStrategyAction(() =>
					actions.cancelStrategyCanary?.(snapshot.revision)
				)
			);
			const promote = actionButton("发布为正式策略", "button");
			promote.addEventListener("click", () =>
				invokeStrategyAction(() =>
					actions.promoteStrategy?.(snapshot.revision)
				)
			);
			publicationControls.append(cancelCanary, promote);
		} else if (snapshot.last_known_good) {
			const rollback = actionButton("回滚到上一正式策略", "button-danger");
			rollback.addEventListener("click", () =>
				invokeStrategyAction(() =>
					actions.rollbackStrategy?.(snapshot.revision)
				)
			);
			publicationControls.append(rollback);
		}

		lifecycleSection.append(
			lifecycleHelp,
			lifecycleStepsTitle,
			lifecycleSteps,
			summary,
			learningPanel,
			draftControls,
			currentChangePanel,
			rollbackPanel,
			historyDisclosure,
			publicationControls,
		);
	}

  function refreshRoleChoices() {
    const participants = participantModelIDs();
    setSelectChoices(
      strongBaseline,
      [["", "请选择"], ...participants.map((id) => [id, id])],
      strongBaseline.value,
    );
    const models = currentModelIDs();
    setSelectChoices(
      taskAnalyzer,
      [["", "请选择"], ...models.map((id) => [id, id])],
      taskAnalyzer.value,
    );
		setSelectChoices(
			dynamicReviewer,
			[["", "请选择"], ...models.map((id) => [id, id])],
			dynamicReviewer.value,
		);
    refreshCandidateModelChoices();
  }

  refreshAutoModelOptions = () => {
    const selected = new Set([
      ...(working.config.auto_routing.participants || []),
      ...participantInputs.filter(({ input }) => input.checked).map(({ id }) => id),
    ]);
    participantInputs = currentModelIDs().map((id, index) => {
      const wrapper = element("div", "checkbox-field");
      const label = textElement("label", id);
      const input = element("input");
      input.type = "checkbox";
      input.name = `auto-participant-${index}`;
      input.checked = selected.has(id);
      input.addEventListener("change", () => {
        working.config.auto_routing.participants = participantModelIDs();
        refreshRoleChoices();
      });
      label.append(input);
      wrapper.append(label);
      return { id, input, wrapper };
    });
    participantList.replaceChildren(...participantInputs.map(({ wrapper }) => wrapper));
    refreshRoleChoices();
    refreshVisionModelOptions();
  };
  refreshAutoModelOptions();

  autoEnabled.addEventListener("change", () => {
    autoDetails.hidden = !autoEnabled.checked;
    if (autoEnabled.checked && !strategyName.value) {
      strategyName.value = nextStrategyName();
    }
  });
  autoDetails.hidden = !autoEnabled.checked;
	autoDetails.append(
		autoRoles,
		riskSection,
		dynamicOptimizationSection,
		configurationSection,
		lifecycleSection,
	);
	autoRouting.append(strategyGuide, autoDependency, autoDetails);

  const vision = editorSection("视觉增强");
  const visionNote = textElement(
    "p",
    "",
  );
  visionNote.className = "warning-banner";
  const visionControls = element("div", "vision-controls stack");
  const visionEnabled = checkboxField(
    visionControls,
    "启用视觉预处理",
    "vision_enabled",
    {
      description: "仅在请求包含图片且目标模型需要增强时发起识图请求。",
    },
  );
  visionEnabled.checked = Boolean(working.config.vision.enabled);
  const visionDetails = element("details", "card vision-details");
  const visionSummary = textElement("summary", "视觉参数");
  const visionGrid = element("div", "form-grid details-content");
  const visionTransport = fieldSelect(
    visionGrid,
    "视觉调用接口",
    "vision_transport",
    working.config.vision.transport,
    visionTransports[working.config.protocol] || [],
    {
      description: "仅控制识图请求；主请求仍使用当前 Profile 协议。",
    },
  );
  const visionModelField = fieldInputWithSuggestions(
    visionGrid,
    "识图模型",
    "vision_model",
    working.config.vision.model,
    currentModelIDs(),
    {
      description:
        "影子识图请求使用的模型；可从当前 Profile 已录入的模型中选择，也可直接输入上游模型 ID；留空时不执行视觉增强。",
    },
  );
  const visionModel = visionModelField.input;
  refreshVisionModelOptions = () => {
    visionModelField.setSuggestions(currentModelIDs());
  };
  refreshVisionModelOptions();
  const visionUnlistedModelPolicy = fieldSelect(
    visionGrid,
    "未收录模型",
    "vision_unlisted_model_policy",
    working.config.vision.unlisted_model_policy || "bypass",
    [
      ["bypass", "默认视为支持视觉，不增强"],
      ["enhance", "默认视为不支持视觉，使用增强"],
    ],
    {
      description:
        "请求模型未出现在“模型能力”列表时采用的默认策略。",
    },
  );
  const visionMaxTokens = fieldInput(
    visionGrid,
    "最大 Token",
    "vision_max_tokens",
    working.config.vision.max_tokens,
    {
      type: "number",
      min: "1",
      description: "单张图片描述允许返回的最大 Token 数。",
    },
  );
  const visionTimeout = fieldInput(
    visionGrid,
    "超时",
    "vision_timeout",
    working.config.vision.timeout,
    {
      description: "单张图片处理的总时限，例如 30s 或 2m。",
    },
  );
  const visionMaxConcurrency = fieldInput(
    visionGrid,
    "最大并发",
    "vision_max_concurrency",
    working.config.vision.max_concurrency,
    {
      type: "number",
      min: "1",
      description: "该 Profile 同时执行的识图请求上限。",
    },
  );
  const visionCacheTTL = fieldInput(
    visionGrid,
    "缓存 TTL",
    "vision_cache_ttl",
    working.config.vision.cache_ttl,
    {
      description: "成功图片描述的缓存时间，例如 30m。",
    },
  );
  const visionCacheMaxEntries = fieldInput(
    visionGrid,
    "缓存条目上限",
    "vision_cache_max_entries",
    working.config.vision.cache_max_entries,
    {
      type: "number",
      min: "0",
      description: "该 Profile 最多保留的图片描述缓存条目。",
    },
  );
  const visionPrompt = fieldTextarea(
    visionGrid,
    "提示词",
    "vision_prompt",
    working.config.vision.prompt,
    {
      description: "留空使用内置提示词；填写后作为基础识图提示词。图片同消息中的用户文本仍会自动用于提取当前问题相关的视觉证据。",
    },
  );
  visionDetails.append(visionSummary, visionGrid);
  visionControls.append(visionDetails);
  vision.append(visionNote, visionControls);

  const retries = editorSection("容错规则");
  const retryHelp = textElement(
    "p",
    "规则按从上到下的顺序匹配，第一个命中的规则生效。",
  );
  retryHelp.className = "muted";
  const retryList = element("div", "stack retry-list");
  let retryRows = [];

  function syncRetryRules() {
    working.config.overload_rules = retryRows.map((row) => ({
      status: row.status.value,
      body_contains: row.bodyContains.value,
      max_retries: row.maxRetries.value,
      delay: row.delay.value,
      jitter: row.jitter.value,
    }));
  }

  function renderRetryRules() {
    retryRows = [];
    const rows = working.config.overload_rules.map((rule, index) => {
      const row = element("fieldset", "card retry-rule");
      const legend = textElement("legend", `规则 ${index + 1}`);
      const fields = element("div", "form-grid");
      const status = fieldInput(
        fields,
        "状态码",
        `retry-${index}-status`,
        rule.status,
        {
          type: "number",
          min: "100",
          max: "599",
          description: "只接受可重试的 HTTP 状态码：408、425、429 或 500–599。401/403 等硬失败不能重试。",
        },
      );
      const bodyContains = fieldInput(
        fields,
        "响应正文包含",
        `retry-${index}-body_contains`,
        rule.body_contains,
        {
          description: "选填；留空时仅按状态码匹配。",
        },
      );
      const maxRetries = fieldInput(
        fields,
        "最大重试次数",
        `retry-${index}-max_retries`,
        rule.max_retries,
        {
          type: "number",
          min: "0",
          description: "首次请求失败后最多追加的尝试次数。",
        },
      );
      const delay = fieldInput(
        fields,
        "延迟",
        `retry-${index}-delay`,
        rule.delay,
        {
          description: "每次重试的基础等待时间，例如 1s。",
        },
      );
      const jitter = fieldInput(
        fields,
        "抖动",
        `retry-${index}-jitter`,
        rule.jitter,
        {
          description: "随重试次数递增的额外等待时间，例如 500ms。",
        },
      );
      retryRows.push({
        status,
        bodyContains,
        maxRetries,
        delay,
        jitter,
      });

      const controls = element("div", "cluster retry-actions");
      const up = actionButton("上移", "button-secondary");
      const down = actionButton("下移", "button-secondary");
      const remove = actionButton("删除", "button-danger");
      up.disabled = index === 0;
      down.disabled = index === working.config.overload_rules.length - 1;
      up.addEventListener("click", () => {
        syncRetryRules();
        working.config.overload_rules = moveRetryRule(
          working.config.overload_rules,
          index,
          -1,
        );
        renderRetryRules();
      });
      down.addEventListener("click", () => {
        syncRetryRules();
        working.config.overload_rules = moveRetryRule(
          working.config.overload_rules,
          index,
          1,
        );
        renderRetryRules();
      });
      remove.addEventListener("click", () => {
        syncRetryRules();
        working.config.overload_rules = removeRetryRule(
          working.config.overload_rules,
          index,
        );
        renderRetryRules();
      });
      controls.append(up, down, remove);
      row.append(legend, fields, controls);
      return row;
    });
    retryList.replaceChildren(...rows);
  }

  renderRetryRules();
  const addRule = actionButton("添加规则", "button-secondary");
  addRule.addEventListener("click", () => {
    syncRetryRules();
    working.config.overload_rules = addRetryRule(
      working.config.overload_rules,
    );
    renderRetryRules();
  });
  retries.append(retryHelp, retryList, addRule);

  const generator = editorSection("配置生成");
  const generatorHelp = textElement(
    "p",
    "保存前可将当前 Profile 交给配置生成流程。",
  );
  generatorHelp.className = "muted";
  const generate = actionButton("生成配置", "button-secondary");
  generator.append(generatorHelp, generate);

  const routingEditor = actions.editorSection === "routing";
  const footer = element(
    "div",
    `cluster editor-actions${routingEditor ? " routing-profile-actions" : ""}`,
  );
  const save = actionButton(
    routingEditor ? "保存路由配置" : "保存 Profile",
    "button",
  );
  save.type = "submit";
  const cancel = actionButton("返回列表", "button-secondary");
  if (routingEditor) {
    footer.append(
      textElement(
        "p",
        "保存模型角色、分析和优化设置；路由策略草稿需在策略摘要中单独保存。",
        "field-help routing-profile-save-help",
      ),
    );
  }
  footer.append(save, cancel);

  function syncForm() {
    syncModelCapabilities();
    syncRetryRules();
    working.display_name = displayName.value;
    working.slug = slug.value;
    working.make_default = isCurrentDefault || makeDefault.checked;
    working.enabled = working.make_default || enabled.checked;
    working.config.version = version.value;
    working.config.protocol = protocol.value;
    working.config.upstream = upstream.value;
    if (!autoEnabled.checked) {
      working.config.auto_routing = {
        ...working.config.auto_routing,
        enabled: false,
      };
    } else {
      syncAutoRoutes();
      syncTaskRoutes();
      working.config.auto_routing = {
        ...working.config.auto_routing,
        enabled: true,
        participants: participantModelIDs(),
        strong_baseline_model: strongBaseline.value,
        task_analyzer_model: taskAnalyzer.value,
        analyzer_timeout: analyzerTimeout.value,
        analyzer_min_confidence_bps: percentToBasisPoints(
          analyzerConfidence.value,
          "任务分析最低置信度",
        ),
        session_ttl: sessionTTL.value,
        session_lock_token_threshold: requiredPositiveSafeInteger(
          sessionLockTokenThreshold.value,
          "Session 锁定 Token 阈值",
        ),
        self_escalation: { enabled: selfEscalationEnabled.checked },
			dynamic_optimization: {
				...working.config.auto_routing.dynamic_optimization,
				enabled: dynamicEnabled.checked,
				sample_rate_bps: percentToBasisPoints(
					dynamicSampleRate.value,
					"异步抽样比例",
				),
				daily_budget_micro_usd: parseUSDToMicroUSD(
					dynamicDailyBudget.value,
					"每日评测预算",
				),
				reviewer_model: dynamicReviewer.value,
				max_concurrency: dynamicConcurrency.value,
				queue_capacity: dynamicQueueCapacity.value,
				task_timeout: dynamicTimeout.value,
			},
        strategy: {
          ...working.config.auto_routing.strategy,
          name: strategyName.value,
          alias: strategyAlias.value,
          default_route: defaultRoute.value,
			min_net_savings_bps: percentToBasisPoints(
				minimumNetSavings.value,
				"最低净节省",
			),
			latency_target_ms: requiredNonnegativeSafeInteger(
				strategyLatencyTarget.value,
				"候选延迟上限",
			),
          budget: Object.fromEntries(
            Object.entries(budgetFields).map(([field, control]) => [
              field,
              field === "max_worst_case_cost_micro_usd"
                ? parseUSDToMicroUSD(control.value, "最坏费用上限")
                : control.value,
            ]),
          ),
        },
      };
    }
    working.config.vision = {
      enabled: visionEnabled.checked,
      transport: normalizedVisionTransport(
        protocol.value,
        visionTransport.value,
      ),
      model: visionModel.value,
      unlisted_model_policy: visionUnlistedModelPolicy.value || "bypass",
      max_tokens: visionMaxTokens.value,
      timeout: visionTimeout.value,
      max_concurrency: visionMaxConcurrency.value,
      cache_ttl: visionCacheTTL.value,
      cache_max_entries: visionCacheMaxEntries.value,
      prompt: visionPrompt.value,
    };
  }

  function applyProtocolState() {
    const isAnthropic = protocol.value === "anthropic";
    const choices = visionTransports[protocol.value] || [];
    const selected = normalizedVisionTransport(
      protocol.value,
      visionTransport.value,
    );
    setSelectChoices(visionTransport, choices, selected);
    visionTransport.disabled = isAnthropic;
    working.config.vision.transport = selected;
    visionControls.hidden = false;
    visionNote.hidden = false;
    visionNote.textContent = isAnthropic
      ? "Anthropic 协议处理 /v1/messages 中的图片内容块。"
      : "OpenAI 协议处理 Chat Completions 与 Responses 中的图片；识图接口可独立选择。";
    visionEnabled.disabled = false;
  }

  protocol.addEventListener("change", () => {
    working.config.protocol = protocol.value;
    applyProtocolState();
  });
  applyProtocolState();

  generate.addEventListener("click", async () => {
    try {
      await runEditorAction(alert, async () => {
        syncForm();
        const payload = profilePayload(working);
        await actions.generate?.(payload);
        openConfigurationGenerator(root, payload);
      });
    } catch {}
  });
  cancel.addEventListener("click", () => actions.cancel?.());
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
		const participantChange = strategyParticipantChange(
			source?.persisted_auto_participants ||
				source?.config?.auto_routing?.participants,
			participantModelIDs(),
			currentActiveStrategy?.config,
			strategyVersions,
			{
				strong_baseline_model: strongBaseline.value,
				task_analyzer_model: taskAnalyzer.value,
				reviewer_model: dynamicReviewer.value,
			},
		);
		if (routingEditor && participantChange.requiresStrategy) {
			openParticipantStrategyDialog(root, participantChange, async () => {
				await generateStrategyDraft();
			});
			return;
		}
    save.disabled = true;
    saveStrategy.disabled = true;
    try {
      await runEditorAction(alert, () => {
			syncForm();
        return actions.save?.(profilePayload(working));
      });
    } catch {
    } finally {
      save.disabled = false;
      saveStrategy.disabled = false;
    }
  });

  form.append(basic, models, autoRouting, vision, retries, generator, alert, footer);
  root.replaceChildren(form);
}

function strategyParticipantChange(
	configured,
	selected,
	activeStrategy,
	strategyVersions = [],
	desiredRoles = {},
) {
	const previous = new Set((configured || []).map(String).filter(Boolean));
	const next = new Set((selected || []).map(String).filter(Boolean));
	const added = [...next].filter((model) => !previous.has(model)).sort();
	const removed = [...previous].filter((model) => !next.has(model)).sort();
	const activeModels = new Set();
	const affectedRoutes = [];
	for (const route of activeStrategy?.routes || []) {
		const routeModels = (route.candidates || [])
			.map((candidate) => String(candidate?.model || ""))
			.filter(Boolean);
		for (const model of routeModels) activeModels.add(model);
		const removedCandidates = routeModels.filter((model) => !next.has(model));
		if (removedCandidates.length > 0) {
			affectedRoutes.push({
				id: String(route?.id || "未命名 Route"),
				models: [...new Set(removedCandidates)].sort(),
			});
		}
	}
	const unrepresentedAdded = added.filter((model) => !activeModels.has(model));
	const compatibleVersion = strategyVersions.some((version) => {
		if (version?.archived_at || !["draft", "evaluating", "ready", "canary"].includes(version?.state)) {
			return false;
		}
		const roles = version?.config?.roles;
		if (!roles || !sameStringSet(roles.participants, selected)) {
			return false;
		}
		if (String(roles.strong_baseline_model || "") !== String(desiredRoles.strong_baseline_model || "") ||
			String(roles.task_analyzer_model || "") !== String(desiredRoles.task_analyzer_model || "") ||
			String(roles.reviewer_model || "") !== String(desiredRoles.reviewer_model || "")) {
			return false;
		}
		return (version.config.routes || []).every((route) =>
			(route.candidates || []).every((candidate) => next.has(String(candidate?.model || "")))
		);
	});
	return {
		added,
		removed,
		affectedRoutes,
		requiresStrategy: Boolean(activeStrategy) && !compatibleVersion &&
			(unrepresentedAdded.length > 0 || affectedRoutes.length > 0),
	};
}

function sameStringSet(left = [], right = []) {
	const leftSet = new Set(left.map(String).filter(Boolean));
	const rightSet = new Set(right.map(String).filter(Boolean));
	return leftSet.size === rightSet.size && [...leftSet].every((item) => rightSet.has(item));
}

function openParticipantStrategyDialog(root, change, generate) {
	const dialog = element("dialog", "profile-dialog participant-strategy-dialog");
	const body = element("section", "stack dialog-body");
	const heading = textElement("h2", "参与模型变化需要新策略");
	heading.id = "participant-strategy-dialog-title";
	dialog.setAttribute("aria-labelledby", heading.id);
	body.append(
		heading,
		textElement(
			"p",
			"当前正式策略与新的参与模型不一致。先生成兼容草稿即可保存模型角色；线上仍使用旧 Active 角色和规则，直到新草稿完成灰度并发布。",
		),
	);
	const changes = element("ul", "participant-strategy-change-list");
	if (change.added.length > 0) {
		changes.append(textElement("li", `新增参与模型：${change.added.join("、")}`));
	}
	if (change.removed.length > 0) {
		changes.append(textElement("li", `移除参与模型：${change.removed.join("、")}`));
	}
	for (const route of change.affectedRoutes) {
		changes.append(textElement(
			"li",
			`Route ${route.id} 仍引用：${route.models.join("、")}`,
		));
	}
	body.append(changes);
	const controls = element("div", "dialog-actions");
	const confirm = actionButton("生成新策略草稿", "button");
	const cancel = actionButton("取消", "button-secondary");
	let closed = false;
	const closeDialog = () => {
		if (closed) return;
		closed = true;
		dialog.close();
		dialog.remove();
	};
	cancel.addEventListener("click", closeDialog);
	dialog.addEventListener("cancel", (event) => {
		event.preventDefault();
		closeDialog();
	});
	confirm.addEventListener("click", async () => {
		confirm.disabled = true;
		try {
			await generate();
			closeDialog();
		} catch {
			confirm.disabled = false;
		}
	});
	controls.append(confirm, cancel);
	body.append(controls);
	dialog.append(body);
	root.append(dialog);
	dialog.showModal();
	return dialog;
}

function modelCapabilitiesPayload(models) {
  const ids = new Set();
  return models.map((model, index) => {
    const id = String(model?.id ?? "");
    if (id.trim() === "") {
      throw new Error(`模型 ${index + 1}：模型 ID 不能为空。`);
    }
    if (id.trim() !== id) {
      throw new Error(`模型 ${index + 1}：模型 ID 前后不能有空格。`);
    }
    if (ids.has(id)) {
      throw new Error(`模型 ID 不能重复：${id}`);
    }
    ids.add(id);
    if (typeof model?.supports_vision !== "boolean") {
      throw new Error(`模型 ${id}：请选择是否支持视觉。`);
    }

    let contextWindow;
    let maxOutputTokens;
    try {
      contextWindow = parseTokenLimit(
        model.context_window,
        { optional: true },
      );
    } catch {
      throw new Error(`模型 ${id}：上下文窗口格式无效。`);
    }
    try {
      maxOutputTokens = parseTokenLimit(
        model.max_output_tokens,
        { optional: true },
      );
    } catch {
      throw new Error(`模型 ${id}：最大输出 Token 格式无效。`);
    }
    if (
      contextWindow !== null &&
      maxOutputTokens !== null &&
      maxOutputTokens >= contextWindow
    ) {
      throw new Error(`模型 ${id}：最大输出 Token 必须小于上下文窗口。`);
    }

    const result = {
      id,
      supports_vision: model.supports_vision,
    };
	const canonicalModelID = String(model?.canonical_model_id ?? "");
	if (canonicalModelID) {
		result.canonical_model_id = canonicalModelID;
	}
	for (const [field, label] of [
		["supports_tools", "工具调用"],
		["supports_agent_workflow", "Agent 工作流"],
		["supports_structured_output", "结构化输出"],
	]) {
		if (model?.[field] === "" || model?.[field] === undefined) {
			continue;
		}
		if (typeof model[field] !== "boolean") {
			throw new Error(`模型 ${id}：${label}能力必须选择是、否或未知。`);
		}
		result[field] = model[field];
	}
	for (const [field, label] of [
		["input_price_micro_usd_per_million", "输入价格"],
		["output_price_micro_usd_per_million", "输出价格"],
		["cache_read_price_micro_usd_per_million", "缓存读取价格"],
		["cache_write_price_micro_usd_per_million", "缓存写入价格"],
	]) {
		const value = optionalNonnegativeSafeInteger(model?.[field], `模型 ${id}：${label}`);
		if (value !== null) {
			result[field] = value;
		}
	}
    if (contextWindow !== null) {
      result.context_window = contextWindow;
    }
    if (maxOutputTokens !== null) {
      result.max_output_tokens = maxOutputTokens;
    }
    return result;
  });
}

function autoRoutingDraft(configured = {}) {
  const defaults = newDefaultAutoRouting();
  const strategy = configured?.strategy || {};
  return {
    ...defaults,
		enabled: Boolean(configured?.enabled),
		strong_baseline_model: String(configured?.strong_baseline_model || ""),
		task_analyzer_model: String(configured?.task_analyzer_model || ""),
		analyzer_timeout: String(configured?.analyzer_timeout || defaults.analyzer_timeout),
		analyzer_min_confidence_bps: configured?.analyzer_min_confidence_bps ??
			defaults.analyzer_min_confidence_bps,
		session_ttl: String(configured?.session_ttl || defaults.session_ttl),
    participants: [...(configured?.participants || [])],
		session_lock_token_threshold: positiveOrDefault(
			configured?.session_lock_token_threshold,
			defaults.session_lock_token_threshold,
		),
		dynamic_optimization: {
			...defaults.dynamic_optimization,
			...(configured?.dynamic_optimization || {}),
		},
    self_escalation: {
      ...defaults.self_escalation,
      ...(configured?.self_escalation || {}),
    },
    strategy: {
      ...defaults.strategy,
      ...strategy,
      name: String(strategy.name || defaults.strategy.name),
      task_routes: (strategy.task_routes || []).map((item) => ({ ...item })),
      routes: (strategy.routes?.length ? strategy.routes : defaults.strategy.routes).map((route) => ({
        ...route,
        candidates: (route.candidates || []).map((candidate) => ({
          ...candidate,
        })),
      })),
      budget: strategyBudgetDraft(strategy.budget),
    },
  };
}

function strategyBudgetDraft(configured = {}) {
  const defaults = newDefaultAutoRouting().strategy.budget;
  return {
    max_answer_attempts: positiveOrDefault(
      configured?.max_answer_attempts,
      defaults.max_answer_attempts,
    ),
    max_auxiliary_calls: positiveOrDefault(
      configured?.max_auxiliary_calls,
      defaults.max_auxiliary_calls,
    ),
    max_total_outbound_calls: minimumOrDefault(
      configured?.max_total_outbound_calls,
      2,
      defaults.max_total_outbound_calls,
    ),
    max_retries_per_target: configured?.max_retries_per_target ??
      defaults.max_retries_per_target,
    max_model_switches: configured?.max_model_switches ??
      defaults.max_model_switches,
    deadline: String(configured?.deadline || defaults.deadline),
    max_worst_case_cost_micro_usd:
      configured?.max_worst_case_cost_micro_usd ??
      defaults.max_worst_case_cost_micro_usd,
  };
}

function positiveOrDefault(value, fallback) {
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) && parsed > 0 ? parsed : fallback;
}

function minimumOrDefault(value, minimum, fallback) {
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) && parsed >= minimum ? parsed : fallback;
}

function autoRoutingPayload(auto) {
  const strategy = auto.strategy || {};
  return {
    enabled: Boolean(auto?.enabled),
    participants: [...(auto.participants || [])].map(String),
    strong_baseline_model: String(auto.strong_baseline_model ?? ""),
    task_analyzer_model: String(auto.task_analyzer_model ?? ""),
    analyzer_timeout: String(auto.analyzer_timeout ?? ""),
    analyzer_min_confidence_bps: boundedBasisPoints(
      auto.analyzer_min_confidence_bps,
      "任务分析最低置信度",
    ),
    session_ttl: String(auto.session_ttl ?? "24h"),
    session_lock_token_threshold: positiveOrDefault(
      auto.session_lock_token_threshold,
      100000,
    ),
    self_escalation: {
      enabled: Boolean(auto.self_escalation?.enabled),
    },
    dynamic_optimization: dynamicOptimizationPayload(
      auto.dynamic_optimization,
    ),
    strategy: {
      name: String(strategy.name ?? ""),
      alias: String(strategy.alias ?? ""),
		roles: {
			participants: [...(auto.participants || [])].map(String),
			strong_baseline_model: String(auto.strong_baseline_model ?? ""),
			task_analyzer_model: String(auto.task_analyzer_model ?? ""),
			reviewer_model: String(auto.dynamic_optimization?.reviewer_model ?? ""),
		},
      default_route: String(strategy.default_route ?? ""),
		min_net_savings_bps: boundedBasisPoints(
			strategy.min_net_savings_bps || 0,
			"最低净节省",
		),
		latency_target_ms: requiredNonnegativeSafeInteger(
			strategy.latency_target_ms || 0,
			"候选延迟上限",
		),
      task_routes: (strategy.task_routes || []).map((mapping) => ({
        task_type: String(mapping.task_type ?? ""),
		difficulty: String(mapping.difficulty ?? ""),
        route: String(mapping.route ?? ""),
      })),
      routes: (strategy.routes || []).map((route) => ({
        id: String(route.id ?? ""),
        min_quality_bps: boundedBasisPoints(
          route.min_quality_bps,
          `Route ${route.id || "未命名"}：最低质量`,
        ),
				min_stability_bps: boundedBasisPoints(
					route.min_stability_bps,
					`Route ${route.id || "未命名"}：最低稳定性`,
				),
        max_severe_error_rate_bps: boundedBasisPoints(
          route.max_severe_error_rate_bps,
          `Route ${route.id || "未命名"}：严重错误率`,
        ),
				weights: {
					quality_bps: boundedBasisPoints(route.weights?.quality_bps, "质量权重"),
					stability_bps: boundedBasisPoints(route.weights?.stability_bps, "稳定性权重"),
					cost_bps: boundedBasisPoints(route.weights?.cost_bps, "成本权重"),
					performance_bps: boundedBasisPoints(route.weights?.performance_bps, "性能权重"),
				},
        candidates: (route.candidates || []).map((candidate) => ({
          model: String(candidate.model ?? ""),
          production_eligible: Boolean(candidate.production_eligible),
          quality_score_bps: boundedBasisPoints(
            candidate.quality_score_bps,
            `候选模型 ${candidate.model || "未选择"}：质量`,
          ),
					stability_score_bps: boundedBasisPoints(
						candidate.stability_score_bps,
						`候选模型 ${candidate.model || "未选择"}：稳定性`,
					),
          severe_error_rate_bps: boundedBasisPoints(
            candidate.severe_error_rate_bps,
            `候选模型 ${candidate.model || "未选择"}：严重错误率`,
          ),
					expected_latency_ms: requiredNonnegativeSafeInteger(
					candidate.expected_latency_ms,
					`候选模型 ${candidate.model || "未选择"}：预期延迟`,
				),
        })),
      })),
      budget: autoBudgetPayload(strategy.budget || {}),
    },
  };
}

function validateAutoRoutingConfiguration(auto, models, vision) {
  if (!auto.enabled) {
    return;
  }
  const byID = new Map(models.map((model) => [model.id, model]));
  if (byID.size < 2) {
    throw new Error("启用智能路由前，请先录入至少两个模型。");
  }
  const participants = [...new Set(auto.participants)];
  if (participants.length < 2) {
    throw new Error("启用智能路由后，请至少选择两个参与模型。");
  }
  for (const modelID of participants) {
    if (!byID.has(modelID)) {
      throw new Error(`参与模型 ${modelID} 尚未录入当前 Profile。`);
    }
  }
  if (!participants.includes(auto.strong_baseline_model)) {
    throw new Error("请选择一个参与模型作为强模型基线。");
  }
  if (!byID.has(auto.task_analyzer_model)) {
    throw new Error("请选择一个已录入模型作为任务分析模型。");
  }

  const strategy = auto.strategy;
  if (!/^\d{8}-\d{3}$/.test(strategy.name)) {
    throw new Error("策略名称必须使用 YYYYMMDD-NNN 格式。");
  }
  if (strategy.routes.length === 0) {
    throw new Error("启用智能路由后，请至少添加一个 Route。");
  }
  const routeIDs = new Set(strategy.routes.map((route) => route.id));
  if (!routeIDs.has(strategy.default_route)) {
    throw new Error("请选择一个已添加的 Route 作为默认 Route。");
  }
  for (const route of strategy.routes) {
    if (!/^[a-z0-9][a-z0-9_-]{0,62}$/.test(route.id)) {
      throw new Error(`Route ${route.id || "未命名"} 的 ID 无效。`);
    }
    if (route.candidates.length === 0) {
      throw new Error(`Route ${route.id} 至少需要一个候选模型。`);
    }
		const weightTotal = Object.values(route.weights || {}).reduce(
			(total, value) => total + Number(value || 0),
			0,
		);
		if (weightTotal !== 10000) {
			throw new Error(`Route ${route.id} 的质量、稳定性、成本和性能权重之和必须为 100%。`);
		}
    for (const candidate of route.candidates) {
      if (!participants.includes(candidate.model)) {
        throw new Error(`Route ${route.id} 的候选模型必须属于参与模型。`);
      }
      if (
        auto.self_escalation.enabled &&
        candidate.model !== auto.strong_baseline_model &&
        byID.get(candidate.model)?.supports_tools !== true
      ) {
        throw new Error(
          `启用模型主动升级时，候选模型 ${candidate.model} 必须支持工具调用。`,
        );
      }
    }
  }
	const taskRouteKeys = new Set();
	for (const mapping of strategy.task_routes || []) {
	  const taskType = String(mapping.task_type || "");
	  const difficulty = String(mapping.difficulty || "");
	  if (taskType && !Object.hasOwn(routingTaskLabels, taskType)) {
		throw new Error(`任务类型 ${taskType} 无效。`);
	  }
	  if (!["", "easy", "medium", "hard"].includes(difficulty)) {
		throw new Error(`任务难度 ${difficulty} 无效。`);
	  }
	  if (!taskType && !difficulty) {
		throw new Error("任务映射至少需要任务类型或难度。");
	  }
	  if (!routeIDs.has(mapping.route)) {
		throw new Error("任务映射必须选择已添加的 Route。");
	  }
	  const key = `${taskType}\u0000${difficulty}`;
	  if (taskRouteKeys.has(key)) {
		throw new Error("任务类型和难度的映射不能重复。");
	  }
	  taskRouteKeys.add(key);
	}

  const requiredModels = new Set([
    ...participants,
    auto.task_analyzer_model,
  ]);
  if (vision.enabled) {
    const visionModel = byID.get(String(vision.model || ""));
    if (!visionModel || visionModel.supports_vision !== true) {
      throw new Error("启用智能路由和视觉增强时，识图模型必须已录入且支持视觉。");
    }
    requiredModels.add(visionModel.id);
  }
  if (auto.dynamic_optimization.enabled) {
    if (!byID.has(auto.dynamic_optimization.reviewer_model)) {
      throw new Error("启用异步对比学习后，请选择一个已录入的质量评审模型。");
    }
    requiredModels.add(auto.dynamic_optimization.reviewer_model);
  }
	if (auto.dynamic_optimization.auto_update_policy && !auto.dynamic_optimization.enabled) {
		throw new Error("自动更新线上策略前必须启用异步评测。");
	}
  for (const modelID of requiredModels) {
    const model = byID.get(modelID);
    if (
      !Object.hasOwn(model, "input_price_micro_usd_per_million") ||
      !Object.hasOwn(model, "output_price_micro_usd_per_million")
    ) {
      throw new Error(`模型 ${modelID} 必须填写输入和输出价格。`);
    }
  }

  const budget = strategy.budget;
  if (
    budget.max_answer_attempts < 1 ||
    budget.max_auxiliary_calls < 1 ||
    budget.max_total_outbound_calls < 2
  ) {
    throw new Error("统一尝试预算必须至少允许一次任务分析和一次主回答。");
  }
  if (auto.self_escalation.enabled && budget.max_model_switches < 1) {
    throw new Error("启用模型主动升级时，模型切换上限至少为 1。");
  }
}

function dynamicOptimizationPayload(config = {}) {
	return {
		enabled: Boolean(config?.enabled),
		auto_update_policy: Boolean(config?.auto_update_policy),
		sample_rate_bps: boundedBasisPoints(
			config.sample_rate_bps,
			"异步抽样比例",
		),
		daily_budget_micro_usd: requiredNonnegativeSafeInteger(
			config.daily_budget_micro_usd,
			"每日评测预算",
		),
		reviewer_model: String(config.reviewer_model ?? ""),
		max_concurrency: requiredNonnegativeSafeInteger(
			config.max_concurrency,
			"评测并发",
		),
		queue_capacity: requiredNonnegativeSafeInteger(
			config.queue_capacity,
			"评测队列容量",
		),
		task_timeout: String(config.task_timeout ?? ""),
	};
}

function autoBudgetPayload(budget) {
  const result = {};
  for (const [field, label] of [
    ["max_answer_attempts", "主回答尝试次数"],
    ["max_auxiliary_calls", "辅助调用次数"],
    ["max_total_outbound_calls", "总上游调用次数"],
    ["max_retries_per_target", "单节点重试次数"],
    ["max_model_switches", "模型切换次数"],
    ["max_worst_case_cost_micro_usd", "最坏费用"],
  ]) {
    result[field] = requiredNonnegativeSafeInteger(budget[field], label);
  }
  result.deadline = String(budget.deadline ?? "");
  return result;
}

function boundedBasisPoints(value, label) {
  const parsed = requiredNonnegativeSafeInteger(value, label);
  if (parsed > 10000) {
    throw new Error(`${label}必须在 0% 到 100% 之间。`);
  }
  return parsed;
}

function optionalNonnegativeSafeInteger(value, label) {
  if (value === null || value === undefined || String(value).trim() === "") {
    return null;
  }
  return requiredNonnegativeSafeInteger(value, label);
}

function optionalUSDInputToMicroUSD(value, label) {
  return parseUSDToMicroUSD(value, label, { optional: true }) ?? "";
}

function requiredNonnegativeSafeInteger(value, label) {
  const normalized = typeof value === "string" ? value.trim() : value;
  const parsed = Number(normalized);
  if (!Number.isSafeInteger(parsed) || parsed < 0 || normalized === "") {
    throw new Error(`${label}必须是非负整数。`);
  }
  return parsed;
}

function requiredPositiveSafeInteger(value, label) {
  const parsed = requiredNonnegativeSafeInteger(value, label);
  if (parsed === 0) {
    throw new Error(`${label}必须大于 0。`);
  }
  return parsed;
}

function booleanSelectValue(value) {
  if (value === true) {
    return "true";
  }
  if (value === false) {
    return "false";
  }
  return "";
}

function selectBooleanValue(value) {
  if (value === "true") {
    return true;
  }
  if (value === "false") {
    return false;
  }
  return "";
}

function suggestionLabel(match) {
  switch (match) {
    case "exact":
      return "精确匹配";
    case "alias":
      return "别名匹配";
    case "compatibility":
      return "兼容别名匹配";
    case "wrapper":
      return "包装器匹配";
    case "snapshot":
      return "快照匹配";
    case "family":
      return "同系列候选";
    default:
      return "同系列候选";
  }
}

function lifecycleLabel(lifecycle) {
  switch (lifecycle) {
    case "stable":
      return "稳定";
    case "preview":
      return "预览";
    case "deprecated":
      return "已弃用";
    default:
      return "暂无可靠数据";
  }
}

function strategyStateLabel(state) {
	switch (state) {
		case "draft":
			return "草稿";
		case "evaluating":
			return "待确认（旧流程）";
		case "ready":
			return "待灰度";
		case "canary":
			return "灰度中";
		case "active":
			return "正式";
		default:
			return String(state || "未知");
	}
}

function strategyStateBadge(state) {
	if (state === "active") {
		return "badge-success";
	}
	if (state === "canary" || state === "evaluating") {
		return "badge-warning";
	}
	return "";
}

function strategyGradeBadge(grade) {
	if (grade === "A") {
		return "badge-success";
	}
	if (grade === "B" || grade === "C") {
		return "badge-warning";
	}
	return "badge-danger";
}

function reliableValue(value) {
  const text = String(value ?? "").trim();
  return text || "暂无可靠数据";
}

function recommendedField(entry, field) {
  return Object.hasOwn(entry, field)
    ? String(entry[field])
    : "暂无可靠数据";
}

function recommendedBoolean(entry, field) {
  if (!Object.hasOwn(entry, field)) {
    return "暂无可靠数据";
  }
  return entry[field] ? "是" : "否";
}

function recommendedPrice(entry, field) {
  if (!Object.hasOwn(entry, field)) {
    return "暂无可靠数据";
  }
  return `${formatMicroUSD(entry[field])}/百万 Token`;
}

function openModelTemplateDialog(root, {
  index,
  modelID,
  catalogModels,
  onApply,
  onClose,
}) {
  const dialog = element("dialog", "profile-dialog model-template-dialog");
  const panel = element("section", "stack dialog-body");
  const heading = textElement("h2", "选择模型参数模板");
  heading.id = `model-template-${index}-title`;
  dialog.setAttribute("aria-labelledby", heading.id);
  const description = textElement(
    "p",
    `未能为 ${modelID} 自动匹配参数。请选择一个接近的模型模板；只应用参数，不会修改模型 ID。`,
    "muted",
  );
  const field = element("div", "form-field");
  const label = textElement("label", "模型模板");
  const search = element("input");
  search.type = "search";
  search.name = `model_template_${index}`;
  search.id = `profile-${search.name}`;
  search.setAttribute("placeholder", "搜索模型 ID、名称或提供方");
  label.setAttribute("for", search.id);
  const list = element("datalist");
  list.id = `model-template-${index}-options`;
  search.setAttribute("list", list.id);
  for (const entry of catalogModels) {
    const option = element("option");
    option.value = String(entry.canonicalId || entry.id || "");
    option.setAttribute(
      "label",
      [entry.name, entry.provider].filter(Boolean).join(" · "),
    );
    list.append(option);
  }
  field.append(label, search, list);

  const controls = element("div", "cluster");
  const apply = actionButton("应用所选模板", "button");
  const skip = actionButton("暂不选择", "button-secondary");
  apply.disabled = true;
  let selectedEntry = null;
  const selectEntry = () => {
    const query = String(search.value).trim().toLowerCase();
    selectedEntry = catalogModels.find((entry) =>
      [entry.canonicalId, entry.id].some(
        (value) => String(value || "").toLowerCase() === query,
      )
    ) || null;
    apply.disabled = selectedEntry === null;
  };
  search.addEventListener("input", selectEntry);
  search.addEventListener("change", selectEntry);

  let closed = false;
  const closeDialog = () => {
    if (closed) {
      return;
    }
    closed = true;
    dialog.close();
    dialog.remove();
    onClose?.();
  };
  apply.addEventListener("click", () => {
    if (!selectedEntry) {
      return;
    }
    onApply(selectedEntry);
    closeDialog();
  });
  skip.addEventListener("click", closeDialog);
  dialog.addEventListener("cancel", (event) => {
    event.preventDefault();
    closeDialog();
  });

  controls.append(apply, skip);
  panel.append(heading, description, field, controls);
  dialog.append(panel);
  root.append(dialog);
  dialog.showModal();
  return dialog;
}

function openCopyDialog(root, profile, actions, pageAlert) {
  const dialog = element("dialog", "profile-dialog");
  const form = element("form", "stack");
  const heading = textElement("h2", "复制 Profile");
  heading.id = `copy-profile-${profile.id}-title`;
  dialog.setAttribute("aria-labelledby", heading.id);
  const fields = element("div", "form-grid");
  const displayName = fieldInput(
    fields,
    "名称",
    "copy-display-name",
    `${profile.display_name} 副本`,
    { required: true },
  );
  const slug = fieldInput(
    fields,
    "Slug",
    "copy-slug",
    `${profile.slug}-copy`,
    {
      required: true,
      description: "新副本的 Profile URL 标识，保存后不能与现有 Slug 重复。",
    },
  );
  const alert = element("div", "error-banner");
  alert.setAttribute("role", "alert");
  alert.hidden = true;
  const controls = element("div", "cluster");
  const submit = actionButton("复制 Profile", "button");
  submit.type = "submit";
  const cancel = actionButton("取消", "button-secondary");
  controls.append(submit, cancel);
  form.append(heading, fields, alert, controls);
  dialog.append(form);
  root.append(dialog);

  cancel.addEventListener("click", () => {
    dialog.close();
    dialog.remove();
  });
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    submit.disabled = true;
    try {
      await runEditorAction(alert, () =>
        actions.copy?.(profile, {
          display_name: displayName.value,
          slug: slug.value,
        }),
      );
      dialog.close();
      dialog.remove();
    } catch {
      submit.disabled = false;
    }
  });
  dialog.showModal();
  pageAlert.hidden = true;
}

function openDeleteDialog(
  root,
  profile,
  profiles,
  defaultProfileID,
  actions,
  pageAlert,
) {
  if (profiles.length === 1) {
    return;
  }
  const isDefault = Number(profile.id) === defaultProfileID;
  const dialog = element("dialog", "profile-dialog");
  const form = element("form", "stack");
  const heading = textElement("h2", "删除 Profile");
  heading.id = `delete-profile-${profile.id}-title`;
  dialog.setAttribute("aria-labelledby", heading.id);
  const warning = textElement(
    "p",
    `确认删除名称为 ${profile.display_name}、slug 为 ${profile.slug} 的 Profile。`,
  );
  const alert = element("div", "error-banner");
  alert.setAttribute("role", "alert");
  alert.hidden = true;
  let replacement = null;

  form.append(heading, warning);
  if (isDefault) {
    const replacementOptions = profiles
      .filter(
        (candidate) =>
          candidate.enabled && Number(candidate.id) !== Number(profile.id),
      )
      .map((candidate) => [
        String(candidate.id),
        `${candidate.display_name} (${candidate.slug})`,
      ]);
    replacement = fieldSelect(
      form,
      "新的默认 Profile",
      "replacement_default_id",
      "",
      [["", "请选择"], ...replacementOptions],
    );
  }

  const controls = element("div", "cluster");
  const submit = actionButton("删除 Profile", "button-danger");
  submit.type = "submit";
  submit.disabled = isDefault;
  const cancel = actionButton("取消", "button-secondary");
  controls.append(submit, cancel);
  form.append(alert, controls);
  dialog.append(form);
  root.append(dialog);

  replacement?.addEventListener("change", () => {
    submit.disabled = replacement.value === "";
  });
  cancel.addEventListener("click", () => {
    dialog.close();
    dialog.remove();
  });
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const replacementID = replacement ? Number(replacement.value) : 0;
    submit.disabled = true;
    try {
      await runEditorAction(alert, () =>
        actions.delete?.(profile, replacementID),
      );
      dialog.close();
      dialog.remove();
    } catch {
      submit.disabled = false;
    }
  });
  dialog.showModal();
  pageAlert.hidden = true;
}

async function runListAction(alert, action) {
  alert.hidden = true;
  alert.textContent = "";
  try {
    await action();
  } catch (error) {
    alert.textContent = error?.message || "请求失败，请重试。";
    alert.hidden = false;
  }
}

async function runEditorAction(alert, action) {
  alert.hidden = true;
  alert.textContent = "";
  try {
    await action();
  } catch (error) {
    alert.textContent = error?.message || "请求失败，请重试。";
    alert.hidden = false;
    throw error;
  }
}

function editorSection(title) {
  const section = element("section", "card editor-section");
  section.append(textElement("h2", title));
  return section;
}

function editorSubsection(title) {
  const section = element("section", "card auto-routing-subsection stack");
  section.append(textElement("h3", title));
  return section;
}

function basisPointsToPercent(value) {
  const parsed = Number(value);
  if (!Number.isFinite(parsed)) {
    return "";
  }
  return String(parsed / 100);
}

function percentToBasisPoints(value, label) {
  const normalized = String(value).trim();
  if (normalized === "") {
    throw new Error(`${label}必须是 0% 到 100% 之间的数值。`);
  }
  const parsed = Number(normalized);
  const basisPoints = parsed * 100;
  if (
    !Number.isFinite(parsed) ||
    parsed < 0 ||
    parsed > 100 ||
    !Number.isInteger(basisPoints)
  ) {
    throw new Error(`${label}必须是 0% 到 100% 之间、最多两位小数的数值。`);
  }
  return basisPoints;
}

function nextStrategyName(now = new Date()) {
  const year = String(now.getFullYear()).padStart(4, "0");
  const month = String(now.getMonth() + 1).padStart(2, "0");
  const day = String(now.getDate()).padStart(2, "0");
  return `${year}${month}${day}-001`;
}

function element(tagName, className = "") {
  const result = document.createElement(tagName);
  result.className = className;
  return result;
}

function textElement(tagName, text) {
  const result = element(tagName);
  result.textContent = String(text);
  return result;
}

function badge(text, variant) {
  const result = textElement("span", text);
  result.className = `badge ${variant}`;
  return result;
}

function actionButton(text, variant) {
  const button = textElement("button", text);
  button.type = "button";
  button.className = `button ${variant}`;
  return button;
}

function appendDefinition(list, term, description) {
  list.append(textElement("dt", term), textElement("dd", description));
}

function fieldInput(parent, labelText, name, value, options = {}) {
  const input = element("input");
  input.name = name;
  input.type = options.type || "text";
  input.value = String(value ?? "");
  input.required = Boolean(options.required);
  if (options.min !== undefined) {
    input.setAttribute("min", options.min);
  }
  if (options.max !== undefined) {
    input.setAttribute("max", options.max);
  }
  if (options.step !== undefined) {
    input.setAttribute("step", options.step);
  }
  appendField(parent, labelText, input, options.description);
  return input;
}

function fieldInputWithSuggestions(
  parent,
  labelText,
  name,
  value,
  suggestions,
  options = {},
) {
  const input = fieldInput(parent, labelText, name, value, options);
  const list = element("datalist");
  list.id = `${input.id}-suggestions`;
  input.setAttribute("list", list.id);
  input.parentNode.append(list);

  const setSuggestions = (values) => {
    const seen = new Set();
    const options = [];
    for (const value of values) {
      const normalized = String(value ?? "").trim();
      if (!normalized || seen.has(normalized)) {
        continue;
      }
      seen.add(normalized);
      const option = element("option");
      option.value = normalized;
      options.push(option);
    }
    list.replaceChildren(...options);
  };
  setSuggestions(suggestions);

  return { input, setSuggestions };
}

function fieldTextarea(parent, labelText, name, value, options = {}) {
  const textarea = element("textarea");
  textarea.name = name;
  textarea.value = String(value ?? "");
  appendField(parent, labelText, textarea, options.description);
  return textarea;
}

function fieldSelect(
  parent,
  labelText,
  name,
  value,
  choices,
  options = {},
) {
  const select = element("select");
  select.name = name;
  setSelectChoices(select, choices, value);
  appendField(parent, labelText, select, options.description);
  return select;
}

function setSelectChoices(select, choices, value) {
  const selectedValue = String(value ?? "");
  const hasSelectedValue = choices.some(
    ([choiceValue]) => String(choiceValue) === selectedValue,
  );
  const hasAvailableValue = choices.some(
    ([choiceValue]) => String(choiceValue) !== "",
  );
  const effectiveChoices = selectedValue !== "" && !hasSelectedValue &&
      !hasAvailableValue
    ? [...choices, [selectedValue, selectedValue]]
    : choices;
  const items = [];
  for (const [choiceValue, choiceLabel] of effectiveChoices) {
    const option = textElement("option", choiceLabel);
    option.value = choiceValue;
    if (String(choiceValue) === selectedValue) {
      option.selected = true;
    }
    items.push(option);
  }
  select.replaceChildren(...items);
  select.value = selectedValue;
}

function checkboxField(parent, labelText, name, options = {}) {
  const wrapper = element("div", "form-field checkbox-field");
  const label = textElement("label", labelText);
  const input = element("input");
  input.name = name;
  input.type = "checkbox";
  input.id = `profile-${name}`;
  label.append(input);
  wrapper.append(label);
  appendFieldDescription(wrapper, input, options.description);
  parent.append(wrapper);
  return input;
}

function appendField(parent, labelText, control, description = "") {
  const wrapper = element("div", "form-field");
  const label = textElement("label", labelText);
  const id = `profile-${control.name}`;
  control.id = id;
  label.setAttribute("for", id);
  wrapper.append(label, control);
  appendFieldDescription(wrapper, control, description);
  parent.append(wrapper);
}

function appendFieldDescription(wrapper, control, description) {
  if (!description) {
    return;
  }
  const help = textElement("small", description);
  help.className = "field-help";
  help.id = `${control.id}-description`;
  control.setAttribute("aria-describedby", help.id);
  wrapper.append(help);
}
