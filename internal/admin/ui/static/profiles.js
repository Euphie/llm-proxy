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
      upstream_type: "url",
      upstream: "",
      upstream_gateway_id: 0,
      models: [],
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
  const upstreamType = effectiveUpstreamType(
    draft.upstream_type ?? config.upstream_type,
  );
  const upstreamGatewayID = Number(
    draft.upstream_gateway_id ?? config.upstream_gateway_id ?? 0,
  );
  const vision = draft.vision || config.vision || {};
  const models = draft.models || config.models || [];
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
      upstream_type: upstreamType,
      upstream: upstreamType === "aggregate_gateway"
        ? ""
        : String(draft.upstream ?? config.upstream ?? ""),
      upstream_gateway_id: upstreamType === "aggregate_gateway"
        ? upstreamGatewayID
        : 0,
      models: modelCapabilitiesPayload(models),
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

function effectiveUpstreamType(value) {
  return value === "aggregate_gateway" ? "aggregate_gateway" : "url";
}

function profileUpstreamSummary(profile, aggregateGatewayNames) {
  const config = profile?.config || {};
  if (effectiveUpstreamType(config.upstream_type) !== "aggregate_gateway") {
    return String(config.upstream ?? "");
  }
  const id = Number(config.upstream_gateway_id ?? 0);
  const name = aggregateGatewayNames.get(id) || `#${id}`;
  return `聚合网关：${name}`;
}

function aggregateGatewayModels(gateway) {
  const seen = new Set();
  const models = [];
  for (const route of gateway?.routes || []) {
    if (!route?.enabled) {
      continue;
    }
    const id = String(route.public_model ?? "").trim();
    if (id === "" || seen.has(id)) {
      continue;
    }
    seen.add(id);
    models.push({
      id,
      context_window: "",
      max_output_tokens: "",
      supports_vision: false,
    });
  }
  return models;
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
      upstream_type: effectiveUpstreamType(profile?.config?.upstream_type),
      upstream: String(profile?.config?.upstream ?? ""),
      upstream_gateway_id: Number(profile?.config?.upstream_gateway_id ?? 0),
      models: (profile?.config?.models || []).map((model) => ({
        ...model,
        id: String(model?.id ?? ""),
        supports_vision:
          typeof model?.supports_vision === "boolean"
            ? model.supports_vision
            : "",
      })),
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
  const aggregateGatewayNames = new Map(
    (data?.aggregate_gateways || []).map((gateway) => [
      Number(gateway.id),
      String(gateway.display_name ?? gateway.slug ?? gateway.id),
    ]),
  );

  if (profiles.length === 0) {
    const empty = element("section", "card empty-state");
    const heading = textElement("h2", "创建第一个代理通道");
    const explanation = textElement(
      "p",
      "创建并启用代理通道前，代理请求仍不可用。",
    );
    explanation.className = "muted";
    const create = actionButton("创建第一个代理通道", "button");
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
  const create = actionButton("新建代理通道", "button");
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
      profileUpstreamSummary(profile, aggregateGatewayNames),
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
  const aggregateGateways = actions.aggregateGateways || source?.aggregate_gateways || [];
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
    description: "用于代理通道 URL，例如 coding 对应 /coding/v1/…。",
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
  const upstreamType = fieldSelect(
    basicGrid,
    "Upstream 类型",
    "upstream_type",
    effectiveUpstreamType(working.config.upstream_type),
    [
      ["url", "外部 URL"],
      ["aggregate_gateway", "聚合网关"],
    ],
    {
      description: "外部 URL 直连上游；聚合网关复用后台已配置的上游供应商和模型路由。",
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
  const upstreamGateway = fieldSelect(
    basicGrid,
    "聚合网关",
    "upstream_gateway_id",
    working.config.upstream_gateway_id || "",
    aggregateGatewayChoices(aggregateGateways),
    {
      description: "选择后协议和模型 ID 会从该网关的启用路由带入。",
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
    "启用代理通道",
    "enabled",
    {
      description: "关闭后，该代理通道的代理地址将不可用。",
    },
  );
  enabled.checked = working.enabled;
  const isCurrentDefault = working.make_default;
  const makeDefault = checkboxField(
    basicGrid,
    "设为默认代理通道",
    "make_default",
    {
      description: "接收不带 slug 的 /v1/… 请求；默认代理通道必须启用。",
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
    "修改 slug 会改变 Agent 使用的代理通道 URL。",
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

  function syncModelCapabilities() {
    working.config.models = modelRows.map((row) => ({
      id: row.id.value,
      context_window: row.contextWindow.value,
      max_output_tokens: row.maxOutputTokens.value,
      supports_vision: selectBooleanValue(row.supportsVision.value),
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
      };
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

          const value = field === "supports_vision"
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

      function hasEmptyRecommendedValues(match) {
        return Object.entries(capabilityControls).some(([field, control]) =>
          control.value === "" && Object.hasOwn(match.entry, field)
        );
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
            } · 视觉：${recommendedVision(match.entry)}`,
        );
        provider.className = "muted model-recommendation-provider";
        details.className = "muted model-recommendation-source";
        dates.className = "muted model-recommendation-meta";
        capabilities.className = "model-recommendation-capabilities";
        card.append(header, provider, details, dates, capabilities);

        if (!match.autoApply || hasEmptyRecommendedValues(match)) {
          const apply = actionButton(
            `应用 ${reliableValue(match.entry.name)} 推荐值`,
            "button-secondary",
          );
          apply.addEventListener("click", () => {
            applyRecommendation(match);
            if (match.autoApply) {
              apply.remove();
            }
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
      });
      return row;
    });
    modelList.replaceChildren(...rows);
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
    "保存前可将当前代理通道交给配置生成流程。",
  );
  generatorHelp.className = "muted";
  const generate = actionButton("生成配置", "button-secondary");
  generator.append(generatorHelp, generate);

  const footer = element("div", "cluster editor-actions");
  const save = actionButton("保存代理通道", "button");
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
    working.config.upstream_type = effectiveUpstreamType(upstreamType.value);
    working.config.upstream = working.config.upstream_type === "aggregate_gateway"
      ? ""
      : upstream.value;
    working.config.upstream_gateway_id =
      working.config.upstream_type === "aggregate_gateway"
        ? upstreamGateway.value
        : 0;
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
    const usesAggregateGateway = upstreamType.value === "aggregate_gateway";
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
    visionNote.textContent = usesAggregateGateway
      ? "识图请求与主请求都通过所选聚合网关转发；识图模型请填写网关已配置的对外模型。"
      : isAnthropic
      ? "Anthropic 协议处理 /v1/messages 中的图片内容块。"
      : "OpenAI 主请求处理 Responses input_image；识图接口可选 Responses 或 Chat Completions。";
    visionEnabled.disabled = false;
  }

  function selectedGateway() {
    return aggregateGateways.find(
      (gateway) => Number(gateway.id) === Number(upstreamGateway.value),
    );
  }

  function inheritAggregateGateway(gateway) {
    if (!gateway) {
      return;
    }
    protocol.value = String(gateway.protocol ?? protocol.value);
    working.config.protocol = protocol.value;
    working.config.models = aggregateGatewayModels(gateway);
    modelRecommendationStates = working.config.models.map(() => ({
      autoValues: {},
    }));
    renderModelCapabilities();
  }

  function applyUpstreamState(inherit = false) {
    const usesAggregateGateway = upstreamType.value === "aggregate_gateway";
    upstream.parentNode.hidden = usesAggregateGateway;
    upstreamGateway.parentNode.hidden = !usesAggregateGateway;
    upstream.disabled = usesAggregateGateway;
    upstream.required = !usesAggregateGateway;
    upstreamGateway.disabled = !usesAggregateGateway || aggregateGateways.length === 0;
    upstreamGateway.required = usesAggregateGateway;
    if (usesAggregateGateway) {
      upstream.value = "";
      if (!upstreamGateway.value && aggregateGateways.length > 0) {
        upstreamGateway.value = String(aggregateGateways[0].id);
      }
      const gateway = selectedGateway();
      protocol.disabled = Boolean(gateway);
      if (inherit) {
        inheritAggregateGateway(gateway);
      }
    } else {
      protocol.disabled = false;
    }
    applyProtocolState();
  }

  protocol.addEventListener("change", () => {
    working.config.protocol = protocol.value;
    applyProtocolState();
  });
  upstreamType.addEventListener("change", () => applyUpstreamState(true));
  upstreamGateway.addEventListener("change", () => applyUpstreamState(true));
  applyUpstreamState(false);
  applyProtocolState();

  generate.addEventListener("click", async () => {
    syncForm();
    try {
      await runEditorAction(alert, async () => {
        const payload = profilePayload(working);
        await actions.generate?.(payload);
        openConfigurationGenerator(root, payload);
      });
    } catch {}
  });
  cancel.addEventListener("click", () => actions.cancel?.());
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    syncForm();
    save.disabled = true;
    try {
      await runEditorAction(alert, () => actions.save?.(profilePayload(working)));
    } catch {
    } finally {
      save.disabled = false;
    }
  });

  form.append(basic, models, vision, retries, generator, alert, footer);
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
    if (contextWindow !== null) {
      result.context_window = contextWindow;
    }
    if (maxOutputTokens !== null) {
      result.max_output_tokens = maxOutputTokens;
    }
    return result;
  });
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

function reliableValue(value) {
  const text = String(value ?? "").trim();
  return text || "暂无可靠数据";
}

function recommendedField(entry, field) {
  return Object.hasOwn(entry, field)
    ? String(entry[field])
    : "暂无可靠数据";
}

function recommendedVision(entry) {
  if (!Object.hasOwn(entry, "supports_vision")) {
    return "暂无可靠数据";
  }
  return entry.supports_vision ? "是" : "否";
}

function openCopyDialog(root, profile, actions, pageAlert) {
  const dialog = element("dialog", "profile-dialog");
  const form = element("form", "stack");
  const heading = textElement("h2", "复制代理通道");
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
      description: "新副本的代理通道 URL 标识，保存后不能与现有 Slug 重复。",
    },
  );
  const alert = element("div", "error-banner");
  alert.setAttribute("role", "alert");
  alert.hidden = true;
  const controls = element("div", "cluster");
  const submit = actionButton("复制代理通道", "button");
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
  const heading = textElement("h2", "删除代理通道");
  heading.id = `delete-profile-${profile.id}-title`;
  dialog.setAttribute("aria-labelledby", heading.id);
  const warning = textElement(
    "p",
    `确认删除名称为 ${profile.display_name}、slug 为 ${profile.slug} 的代理通道。`,
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
      "新的默认代理通道",
      "replacement_default_id",
      "",
      [["", "请选择"], ...replacementOptions],
    );
  }

  const controls = element("div", "cluster");
  const submit = actionButton("删除代理通道", "button-danger");
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

function aggregateGatewayChoices(gateways) {
  if (gateways.length === 0) {
    return [["", "请先创建聚合网关"]];
  }
  return gateways.map((gateway) => [
    String(gateway.id),
    `${gateway.display_name} (${gateway.protocol}${gateway.enabled ? "" : "，已停用"})`,
  ]);
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
