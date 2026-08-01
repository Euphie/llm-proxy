import { openConfigurationGenerator } from "./generator.js";
import {
  matchModelSuggestions,
  parseTokenLimit,
} from "./model-catalog.js";
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

function newDefaultAutoRouting() {
  return {
    enabled: false,
    participants: [],
    strong_baseline_model: "",
    task_analyzer_model: "",
    analyzer_timeout: "5s",
    analyzer_min_confidence_bps: 7000,
    session_ttl: "24h",
		dynamic_optimization: {
			enabled: false,
			sample_rate_bps: 1000,
			daily_budget_micro_usd: 250000,
			reviewer_model: "",
			max_concurrency: 2,
			queue_capacity: 128,
			task_timeout: "90s",
		},
    strategy: {
      name: "",
      alias: "",
      default_route: "default",
      task_routes: [],
      routes: [],
      budget: {
        max_answer_attempts: 2,
        max_auxiliary_calls: 2,
        max_total_outbound_calls: 5,
        max_retries_per_target: 1,
        max_target_switches: 0,
        max_model_switches: 1,
        deadline: "2m",
        max_worst_case_cost_micro_usd: 500000,
      },
    },
  };
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
      version: 1,
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

  return {
    slug: String(draft.slug ?? ""),
    display_name: String(draft.display_name ?? ""),
    enabled: Boolean(draft.enabled),
    make_default: Boolean(draft.make_default),
    config: {
      version: Number(draft.version ?? config.version ?? 1),
      protocol,
      upstream: String(draft.upstream ?? config.upstream ?? ""),
      models: modelCapabilitiesPayload(models),
      auto_routing: autoRoutingPayload(autoRouting),
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
        status: Number(rule.status),
        body_contains: String(rule.body_contains ?? ""),
        max_retries: Number(rule.max_retries),
        delay: String(rule.delay ?? ""),
        jitter: String(rule.jitter ?? ""),
      })),
    },
  };
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
      supports_structured_output: "",
      input_price_micro_usd_per_million: "",
      output_price_micro_usd_per_million: "",
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
  const modelList = element("div", "stack model-capability-list");
  let modelRows = [];
  let modelRecommendationStates = working.config.models.map(() => ({
    autoValues: {},
  }));
  let refreshAutoModelOptions = () => {};

  function syncModelCapabilities() {
    working.config.models = modelRows.map((row) => ({
      id: row.id.value,
      context_window: row.contextWindow.value,
      max_output_tokens: row.maxOutputTokens.value,
      supports_vision: selectBooleanValue(row.supportsVision.value),
      supports_tools: selectBooleanValue(row.supportsTools.value),
      supports_structured_output: selectBooleanValue(
        row.supportsStructuredOutput.value,
      ),
      input_price_micro_usd_per_million: row.inputPrice.value,
      output_price_micro_usd_per_million: row.outputPrice.value,
    }));
  }

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
        "输入价格",
        `model-${index}-input-price`,
        model.input_price_micro_usd_per_million,
        {
          type: "number",
          min: "0",
          step: "1",
          description:
            "选填，单位为微美元/百万 Token；例如 $0.10 填 100000。参与 Auto 时必填，0 表示免费。",
        },
      );
      inputPrice.setAttribute(
        "data-model-field",
        "input_price_micro_usd_per_million",
      );
      const outputPrice = fieldInput(
        fields,
        "输出价格",
        `model-${index}-output-price`,
        model.output_price_micro_usd_per_million,
        {
          type: "number",
          min: "0",
          step: "1",
          description:
            "选填，单位为微美元/百万 Token；例如 $0.40 填 400000。参与 Auto 时必填，0 表示免费。",
        },
      );
      outputPrice.setAttribute(
        "data-model-field",
        "output_price_micro_usd_per_million",
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
        supports_structured_output: supportsStructuredOutput,
        input_price_micro_usd_per_million: inputPrice,
        output_price_micro_usd_per_million: outputPrice,
      };
      const booleanCapabilityFields = new Set([
        "supports_vision",
        "supports_tools",
        "supports_structured_output",
      ]);
      let renderedRecommendationQuery = null;
      let renderedRecommendationSignature = null;
      let renderedMatches = [];

      function applyRecommendation(match) {
        for (const [field, control] of Object.entries(capabilityControls)) {
          const owned = Object.hasOwn(
            recommendationState.autoValues,
            field,
          );
          const oldAutoValue = recommendationState.autoValues[field];
          const canWrite = control.value === "" ||
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
            : String(match.entry[field]);
          control.value = value;
          recommendationState.autoValues[field] = value;
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
            `输出 ${recommendedPrice(match.entry, "output_price_micro_usd_per_million")}。` +
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
          apply.addEventListener("click", () => applyRecommendation(match));
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

      function refreshRecommendations() {
        const result = calculateRecommendations();
        renderRecommendations(result);
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
      }

      for (const [field, control] of Object.entries(capabilityControls)) {
        const releaseOwnership = () => {
          delete recommendationState.autoValues[field];
        };
        control.addEventListener("input", releaseOwnership);
        control.addEventListener("change", releaseOwnership);
      }
      id.addEventListener("input", (event) => {
        refreshRecommendations();
        refreshAutoModelOptions();
        if (event.inputType === "insertFromPaste") {
          commitRecommendations();
        }
      });
      id.addEventListener("change", commitRecommendations);
      id.addEventListener("blur", commitRecommendations);
      refreshRecommendations();

      const controls = element("div", "cluster model-capability-actions");
      const remove = actionButton("删除模型", "button-danger");
      remove.addEventListener("click", () => {
        syncModelCapabilities();
        working.config.models = removeModelCapability(
          working.config.models,
          index,
        );
        modelRecommendationStates = modelRecommendationStates.filter(
          (_, current) => current !== index,
        );
        renderModelCapabilities();
      });
      controls.append(remove);
      row.append(legend, fields, recommendation, controls);
      modelRows.push({
        id,
        contextWindow,
        maxOutputTokens,
        supportsVision,
        supportsTools,
        supportsStructuredOutput,
        inputPrice,
        outputPrice,
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
  });
  const modelListActions = element("div", "cluster model-list-actions");
  modelListActions.append(addModel);
  models.append(modelHelp, modelList, modelListActions);

  const autoRouting = editorSection("智能路由");
  const autoHelp = textElement(
    "p",
    "仅处理 model=auto。系统先判断任务，再在当前 Profile 的参与模型中选择满足能力和质量要求且预计总费用最低的模型。",
  );
  autoHelp.className = "muted";
  const autoEnabled = checkboxField(
    autoRouting,
    "启用智能路由",
    "auto_routing_enabled",
    {
      description:
        "关闭时不发起任务分析，也不会改写 model；客户端指定具体模型始终原样转发。",
    },
  );
  autoEnabled.checked = Boolean(working.config.auto_routing.enabled);
  const autoDetails = element("div", "stack auto-routing-details");
  const autoRoles = editorSubsection("模型角色");
  const participantList = element("div", "model-participant-list stack");
  const participantHelp = textElement(
    "p",
    "只有勾选的模型会参与主回答选型；新增模型不会自动加入。",
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
        "高风险、分析失败或没有普通候选达标时使用；必须属于参与模型。",
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
        "仅在本地规则无法确定任务时调用；建议选择快速、低价且支持文本的模型。",
    },
  );
  const analyzerTimeout = fieldInput(
    roleGrid,
    "任务分析超时",
    "auto_analyzer_timeout",
    working.config.auto_routing.analyzer_timeout,
    {
      description: "单次任务分析的上限，例如 5s；超时后保守回到强模型基线。",
    },
  );
  const analyzerConfidence = fieldInput(
    roleGrid,
    "最低分析置信度（%）",
    "auto_analyzer_confidence",
    basisPointsToPercent(
      working.config.auto_routing.analyzer_min_confidence_bps,
    ),
    {
      type: "number",
      min: "0",
      max: "100",
      step: "0.01",
      description: "低于该值的分析结果不参与选型，直接使用强模型基线。",
    },
  );
  const sessionTTL = fieldInput(
    roleGrid,
    "Session 绑定有效期",
    "auto_session_ttl",
    working.config.auto_routing.session_ttl,
    {
      description:
        "默认 24h。客户端发送 X-LLM-Proxy-Session-ID 且带鉴权头时，同一 Route 会沿用已选模型；只升级、不自动降级。",
    },
  );
  autoRoles.append(roleGrid);

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
		"每日评测预算（微美元）",
		"auto_dynamic_daily_budget",
		dynamicConfig.daily_budget_micro_usd,
		{
			type: "number", min: "1", step: "1",
			description: "对比回答和质量评审共用的每日硬上限；1 美元 = 1,000,000 微美元。",
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

  const strategySection = editorSubsection("策略");
  const strategyGrid = element("div", "form-grid");
  const strategyName = fieldInput(
    strategyGrid,
    "策略名称",
    "auto_strategy_name",
    working.config.auto_routing.strategy.name,
    {
      description: "唯一版本号，格式为 YYYYMMDD-NNN，例如 20260802-001。",
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
      description: "选填，用于后台识别，例如“质量优先”或“日常均衡”。",
    },
  );
  const defaultRoute = fieldSelect(
    strategyGrid,
    "默认 Route",
    "auto_default_route",
    working.config.auto_routing.strategy.default_route,
    [],
    {
      description: "任务类型没有显式映射时使用的 Route，不能为空。",
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
      max_severe_error_rate_bps: percentToBasisPoints(
        row.maxSevereError.value,
        `Route ${row.id.value || "未命名"}：严重错误率`,
      ),
      candidates: row.candidates.map((candidate) => ({
        model: candidate.model.value,
        quality_score_bps: percentToBasisPoints(
          candidate.quality.value,
          `候选模型 ${candidate.model.value || "未选择"}：质量`,
        ),
        severe_error_rate_bps: percentToBasisPoints(
          candidate.severeError.value,
          `候选模型 ${candidate.model.value || "未选择"}：严重错误率`,
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
          description: "任务映射引用的稳定标识，例如 simple、coding 或 long_context。",
        });
        const minQuality = fieldInput(
          fields,
          "最低质量（%）",
          `auto-route-${routeIndex}-min-quality`,
          basisPointsToPercent(route.min_quality_bps),
          {
            type: "number", min: "0", max: "100", step: "0.01",
            description: "低于该质量门槛的候选不会参与本 Route 选型。",
          },
        );
        const maxSevereError = fieldInput(
          fields,
          "最大严重错误率（%）",
          `auto-route-${routeIndex}-max-severe-error`,
          basisPointsToPercent(route.max_severe_error_rate_bps),
          {
            type: "number", min: "0", max: "100", step: "0.01",
            description: "超过该硬门槛的候选即使价格更低也会被排除。",
          },
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
              { description: "必须先在上方勾选为参与模型。" },
            );
            const quality = fieldInput(
              candidateFields,
              "当前质量估计（%）",
              `auto-route-${routeIndex}-candidate-${candidateIndex}-quality`,
              basisPointsToPercent(candidate.quality_score_bps),
              {
                type: "number", min: "0", max: "100", step: "0.01",
                description: "第一阶段由管理员填写；后续动态策略会用评测证据更新。",
              },
            );
            const severeError = fieldInput(
              candidateFields,
              "当前严重错误率（%）",
              `auto-route-${routeIndex}-candidate-${candidateIndex}-severe-error`,
              basisPointsToPercent(candidate.severe_error_rate_bps),
              {
                type: "number", min: "0", max: "100", step: "0.01",
                description: "严重错误是硬门槛，不会被低价格抵消。",
              },
            );
            const removeCandidate = actionButton("删除候选", "button-danger");
            removeCandidate.addEventListener("click", () => {
              syncAutoRoutes();
              working.config.auto_routing.strategy.routes[routeIndex].candidates.splice(candidateIndex, 1);
              renderAutoRoutes();
            });
            candidateControls.push({ model, quality, severeError });
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
            quality_score_bps: 9000,
            severe_error_rate_bps: 0,
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
        autoRouteRows.push({ id, minQuality, maxSevereError, candidates: candidateControls });
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
      max_severe_error_rate_bps: 100,
      candidates: [],
    });
    renderAutoRoutes();
  });
  strategySection.append(routeList, addRoute);

  const mappingSection = editorSubsection("任务映射");
  const mappingHelp = textElement("p", "选填。分析模型返回的任务类型先映射到 Route；未匹配时使用默认 Route。");
  mappingHelp.className = "field-help";
  const taskRouteList = element("div", "stack auto-task-route-list");

  function syncTaskRoutes() {
    working.config.auto_routing.strategy.task_routes = taskRouteRows.map((row) => ({
      task_type: row.taskType.value,
      route: row.route.value,
    }));
  }

  function renderTaskRoutes() {
    taskRouteRows = [];
    const choices = [["", "请选择"], ...routeIDs().map((id) => [id, id])];
    const rows = working.config.auto_routing.strategy.task_routes.map((mapping, index) => {
      const row = element("div", "card auto-task-route-row");
      const fields = element("div", "form-grid");
      const taskType = fieldInput(fields, "任务类型", `auto-task-${index}-type`, mapping.task_type, {
        description: "例如 simple、coding、long_context 或 high_risk。",
      });
      const route = fieldSelect(fields, "Route", `auto-task-${index}-route`, mapping.route, choices, {
        description: "该任务类型使用的候选集合和质量门槛。",
      });
      const remove = actionButton("删除映射", "button-danger");
      remove.addEventListener("click", () => {
        syncTaskRoutes();
        working.config.auto_routing.strategy.task_routes.splice(index, 1);
        renderTaskRoutes();
      });
      taskRouteRows.push({ taskType, route });
      row.append(fields, remove);
      return row;
    });
    taskRouteList.replaceChildren(...rows);
  }

  renderTaskRoutes();
  const addTaskRoute = actionButton("添加任务映射", "button-secondary");
  addTaskRoute.addEventListener("click", () => {
    syncTaskRoutes();
    working.config.auto_routing.strategy.task_routes.push({ task_type: "", route: defaultRoute.value });
    renderTaskRoutes();
  });
  mappingSection.append(mappingHelp, taskRouteList, addTaskRoute);

  const budgetSection = editorSubsection("统一尝试预算");
  const budgetGrid = element("div", "form-grid");
  const budget = working.config.auto_routing.strategy.budget;
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
    max_retries_per_target: fieldInput(budgetGrid, "单 Target 重试上限", "auto-budget-retries", budget.max_retries_per_target, {
      type: "number", min: "0", description: "同一部署发生可重试错误时允许追加的次数。",
    }),
    max_target_switches: fieldInput(budgetGrid, "Target 切换上限", "auto-budget-target-switches", budget.max_target_switches, {
      type: "number", min: "0", description: "第一阶段通常填 0；后续同模型多部署时使用。",
    }),
    max_model_switches: fieldInput(budgetGrid, "模型切换上限", "auto-budget-model-switches", budget.max_model_switches, {
      type: "number", min: "0", description: "回答尚未提交给客户端前，最多允许切换多少次主模型。",
    }),
    deadline: fieldInput(budgetGrid, "请求总截止时间", "auto-budget-deadline", budget.deadline, {
      description: "任务分析、视觉、主回答和重试共享的总时限，例如 2m。",
    }),
    max_worst_case_cost_micro_usd: fieldInput(
      budgetGrid,
      "最坏费用上限（微美元）",
      "auto-budget-cost",
      budget.max_worst_case_cost_micro_usd,
      {
        type: "number", min: "0", step: "1",
        description: "执行计划在最坏尝试次数下的费用上限；1 美元 = 1,000,000 微美元。",
      },
    ),
  };
  budgetSection.append(budgetGrid);

	const lifecycleSection = editorSubsection("策略版本");
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
		const snapshot = strategyOverview.snapshot || {};
		const lifecycleHelp = textElement(
			"p",
			"Profile 保存只更新模型角色等公共配置，不会直接覆盖线上策略。先在上方调整 Route 与预算，再保存为草稿；草稿进入评估后不可修改。",
		);
		lifecycleHelp.className = "field-help";
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
		const invokeStrategyAction = (action) => {
			void runEditorAction(alert, action).catch(() => {});
		};
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
				`今日评测：${spent.toLocaleString("en-US")} / ${dailyBudget.toLocaleString("en-US")} 微美元`,
			);
			budgetSummary.className = "muted";
			const reservedSummary = reserved > 0
				? textElement("p", `已预留：${reserved.toLocaleString("en-US")} 微美元`)
				: null;
			if (reservedSummary) {
				reservedSummary.className = "muted";
			}
			const estimates = element("div", "stack strategy-evidence-list");
			for (const estimate of strategyOverview.quality_estimates || []) {
				const card = element("article", "card strategy-evidence-card stack");
				const title = textElement(
					"h4",
					`${estimate.candidate_model} ↔ ${estimate.reference_model}`,
				);
				const sampleText = estimate.reliable ? "证据可靠" : "继续积累";
				const metrics = textElement(
					"p",
					`${estimate.raw_samples || 0} 个样本 · 保守质量 ${basisPointsToPercent(estimate.quality_lower_bps)}% · 严重错误上界 ${basisPointsToPercent(estimate.severe_error_upper_bps)}% · ${sampleText}`,
				);
				metrics.className = "muted";
				card.append(title, metrics);
				estimates.append(card);
			}
			const generateCandidate = actionButton("生成学习候选", "button-secondary");
			generateCandidate.disabled = !(strategyOverview.quality_estimates || [])
				.some((estimate) => estimate.reliable);
			generateCandidate.addEventListener("click", () => {
				if (!generateCandidate.disabled) {
					invokeStrategyAction(() => actions.generateStrategyCandidate?.());
				}
			});
			learningPanel.append(
				learningHelp,
				budgetSummary,
				...(reservedSummary ? [reservedSummary] : []),
				estimates,
				generateCandidate,
			);
		}

		const draftControls = element("div", "cluster strategy-draft-controls");
		const saveDraft = actionButton(
			editingStrategyID ? "更新当前草稿" : "保存为新草稿",
			"button-secondary",
		);
		saveDraft.addEventListener("click", async () => {
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
			} catch {}
		});
		draftControls.append(saveDraft);
		if (editingStrategyID) {
			const stopEditing = actionButton("退出草稿编辑", "button-secondary");
			stopEditing.addEventListener("click", () =>
				invokeStrategyAction(() => actions.editStrategy?.(0))
			);
			draftControls.append(stopEditing);
		}

		const versions = element("div", "stack strategy-version-list");
		for (const version of strategyOverview.strategies || []) {
			const card = element("article", "card strategy-version-card stack");
			const header = element("div", "cluster strategy-version-header");
			const title = textElement(
				"h4",
				version.config?.alias || version.config?.name || `策略 ${version.id}`,
			);
			const state = badge(
				strategyStateLabel(version.state),
				strategyStateBadge(version.state),
			);
			header.append(title, state);
			if (version.rating?.grade) {
				header.append(
					badge(
						`质量 ${version.rating.grade}`,
						strategyGradeBadge(version.rating.grade),
					),
				);
			}
			const metadata = textElement(
				"p",
				`${version.config?.name || "-"} · ID ${version.id}`,
			);
			metadata.className = "muted";
			const rating = version.rating
				? textElement(
					"p",
					`配置估算：${basisPointsToPercent(version.rating.display_score_bps)} 分 · 质量下限 ${basisPointsToPercent(version.rating.quality_floor_bps)}% · 严重错误上限 ${basisPointsToPercent(version.rating.severe_error_ceiling_bps)}%`,
				)
				: null;
			if (rating) {
				rating.className = "muted";
			}
			const controls = element("div", "cluster strategy-version-actions");

			if (version.state === "draft") {
				const edit = actionButton("载入草稿", "button-secondary");
				edit.addEventListener("click", () =>
					invokeStrategyAction(() => actions.editStrategy?.(version.id))
				);
				const evaluate = actionButton("开始评估", "button-secondary");
				evaluate.addEventListener("click", () =>
					invokeStrategyAction(() =>
						actions.advanceStrategy?.(version.id, "draft", "evaluating")
					)
				);
				controls.append(edit, evaluate);
			} else if (version.state === "evaluating") {
				const ready = actionButton("评估通过", "button-secondary");
				ready.addEventListener("click", () =>
					invokeStrategyAction(() =>
						actions.advanceStrategy?.(version.id, "evaluating", "ready")
					)
				);
				controls.append(ready);
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
				controls.append(percent, canary);
			}
			card.append(header, metadata);
			if (rating) {
				card.append(rating);
			}
			if (controls.children.length) {
				card.append(controls);
			}
			versions.append(card);
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
			summary,
			learningPanel,
			draftControls,
			versions,
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
		dynamicOptimizationSection,
		strategySection,
		mappingSection,
		budgetSection,
		lifecycleSection,
	);
  autoRouting.append(autoDetails);

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
  const visionModel = fieldInput(
    visionGrid,
    "识图模型",
    "vision_model",
    working.config.vision.model,
    {
      description:
        "影子识图请求使用的模型；留空时不执行视觉增强。",
    },
  );
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
          description: "需要重试的 HTTP 状态码，例如 429 或 529。",
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

  const footer = element("div", "cluster editor-actions");
  const save = actionButton("保存 Profile", "button");
  save.type = "submit";
  const cancel = actionButton("返回列表", "button-secondary");
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
			dynamic_optimization: dynamicEnabled.checked
				? {
					enabled: true,
					sample_rate_bps: percentToBasisPoints(
						dynamicSampleRate.value,
						"异步抽样比例",
					),
					daily_budget_micro_usd: dynamicDailyBudget.value,
					reviewer_model: dynamicReviewer.value,
					max_concurrency: dynamicConcurrency.value,
					queue_capacity: dynamicQueueCapacity.value,
					task_timeout: dynamicTimeout.value,
				}
				: { enabled: false },
        strategy: {
          ...working.config.auto_routing.strategy,
          name: strategyName.value,
          alias: strategyAlias.value,
          default_route: defaultRoute.value,
          budget: Object.fromEntries(
            Object.entries(budgetFields).map(([field, control]) => [
              field,
              control.value,
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
    save.disabled = true;
    try {
      await runEditorAction(alert, () => {
        syncForm();
        return actions.save?.(profilePayload(working));
      });
    } catch {
    } finally {
      save.disabled = false;
    }
  });

  form.append(basic, models, autoRouting, vision, retries, generator, alert, footer);
  root.replaceChildren(form);
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
	for (const [field, label] of [
		["supports_tools", "工具调用"],
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
    ...configured,
    participants: [...(configured?.participants || [])],
		dynamic_optimization: {
			...defaults.dynamic_optimization,
			...(configured?.dynamic_optimization || {}),
		},
    strategy: {
      ...defaults.strategy,
      ...strategy,
      task_routes: (strategy.task_routes || []).map((item) => ({ ...item })),
      routes: (strategy.routes || []).map((route) => ({
        ...route,
        candidates: (route.candidates || []).map((candidate) => ({
          ...candidate,
        })),
      })),
      budget: {
        ...defaults.strategy.budget,
        ...(strategy.budget || {}),
      },
    },
  };
}

function autoRoutingPayload(auto) {
  if (!auto?.enabled) {
    return { enabled: false };
  }
  const strategy = auto.strategy || {};
  return {
    enabled: true,
    participants: [...(auto.participants || [])].map(String),
    strong_baseline_model: String(auto.strong_baseline_model ?? ""),
    task_analyzer_model: String(auto.task_analyzer_model ?? ""),
    analyzer_timeout: String(auto.analyzer_timeout ?? ""),
    analyzer_min_confidence_bps: boundedBasisPoints(
      auto.analyzer_min_confidence_bps,
      "任务分析最低置信度",
    ),
    session_ttl: String(auto.session_ttl ?? "24h"),
		dynamic_optimization: dynamicOptimizationPayload(
			auto.dynamic_optimization,
		),
    strategy: {
      name: String(strategy.name ?? ""),
      alias: String(strategy.alias ?? ""),
      default_route: String(strategy.default_route ?? ""),
      task_routes: (strategy.task_routes || []).map((mapping) => ({
        task_type: String(mapping.task_type ?? ""),
        route: String(mapping.route ?? ""),
      })),
      routes: (strategy.routes || []).map((route) => ({
        id: String(route.id ?? ""),
        min_quality_bps: boundedBasisPoints(
          route.min_quality_bps,
          `Route ${route.id || "未命名"}：最低质量`,
        ),
        max_severe_error_rate_bps: boundedBasisPoints(
          route.max_severe_error_rate_bps,
          `Route ${route.id || "未命名"}：严重错误率`,
        ),
        candidates: (route.candidates || []).map((candidate) => ({
          model: String(candidate.model ?? ""),
          quality_score_bps: boundedBasisPoints(
            candidate.quality_score_bps,
            `候选模型 ${candidate.model || "未选择"}：质量`,
          ),
          severe_error_rate_bps: boundedBasisPoints(
            candidate.severe_error_rate_bps,
            `候选模型 ${candidate.model || "未选择"}：严重错误率`,
          ),
        })),
      })),
      budget: autoBudgetPayload(strategy.budget || {}),
    },
  };
}

function dynamicOptimizationPayload(config = {}) {
	if (!config?.enabled) {
		return { enabled: false };
	}
	return {
		enabled: true,
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
    ["max_retries_per_target", "单 Target 重试次数"],
    ["max_target_switches", "Target 切换次数"],
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

function requiredNonnegativeSafeInteger(value, label) {
  const normalized = typeof value === "string" ? value.trim() : value;
  const parsed = Number(normalized);
  if (!Number.isSafeInteger(parsed) || parsed < 0 || normalized === "") {
    throw new Error(`${label}必须是非负整数。`);
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
			return "评估中";
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
  return `$${entry[field] / 1_000_000}/百万 Token`;
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
  appendField(parent, labelText, input, options.description);
  return input;
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
  const items = [];
  for (const [choiceValue, choiceLabel] of choices) {
    const option = textElement("option", choiceLabel);
    option.value = choiceValue;
    if (String(choiceValue) === String(value)) {
      option.selected = true;
    }
    items.push(option);
  }
  select.replaceChildren(...items);
  select.value = String(value ?? "");
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
