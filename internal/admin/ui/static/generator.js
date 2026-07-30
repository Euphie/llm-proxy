const claudeSchema =
  "https://json.schemastore.org/claude-code-settings.json";

const claudeModelVariables = {
  sonnet: "ANTHROPIC_DEFAULT_SONNET_MODEL",
  haiku: "ANTHROPIC_DEFAULT_HAIKU_MODEL",
  opus: "ANTHROPIC_DEFAULT_OPUS_MODEL",
};

const claudeAuthVariables = new Set([
  "ANTHROPIC_AUTH_TOKEN",
  "ANTHROPIC_API_KEY",
]);

const adaptersByProtocol = {
  anthropic: [
    ["claude-code", "Claude Code"],
    ["opencode-anthropic", "OpenCode (Anthropic)"],
    ["generic", "Generic"],
  ],
  openai: [
    ["generic", "Generic"],
    ["opencode-responses", "OpenCode (Responses)"],
    ["codex-responses", "Codex (Responses)"],
  ],
};

export function availableAgentAdapters(protocol) {
  const adapters = adaptersByProtocol[String(protocol)];
  if (!adapters) {
    throw new Error(`Unsupported protocol: ${protocol}`);
  }
  return adapters.map((adapter) => [...adapter]);
}

export function calculateCompaction(model, percent) {
  requireCompactionPercent(percent);
  const contextWindow = requirePositiveSafeInteger(
    model?.context_window,
    "Model context window",
  );
  const maxOutputTokens = model?.max_output_tokens;
  if (maxOutputTokens != null) {
    requirePositiveSafeInteger(
      maxOutputTokens,
      "Model max output tokens",
    );
  }

  const requested = floorMultiplyDivide(
    contextWindow,
    percent,
    100,
    "Compaction threshold",
  );
  const compactAt =
    maxOutputTokens == null
      ? requested
      : Math.min(requested, contextWindow - maxOutputTokens);
  if (compactAt <= 0) {
    throw new RangeError("Compaction threshold must be positive");
  }
  return {
    contextWindow,
    compactAt,
    reserved: contextWindow - compactAt,
    percent,
  };
}

export function claudeContextEnvironment(
  profileModels,
  mappings,
  percent,
) {
  requireCompactionPercent(percent);
  const mappedIDs = Object.values(mappings ?? {})
    .map((value) => String(value ?? ""))
    .filter((value) => value.trim());
  if (mappedIDs.length === 0) {
    return missingClaudeContext(
      "请先填写至少一个 Claude 模型映射；未生成上下文压缩覆盖。",
    );
  }

  const calculations = [];
  for (const id of mappedIDs) {
    const model = (profileModels ?? []).find(
      (candidate) => candidate?.id === id,
    );
    if (!model || model.context_window == null) {
      return missingClaudeContext(
        `模型 ${id} 未在 Profile 中保存上下文窗口；已同时省略两项 Claude 压缩覆盖。`,
      );
    }
    try {
      const calculation = calculateCompaction(model, percent);
      calculations.push({
        ...calculation,
        uncappedCompactAt: floorMultiplyDivide(
          calculation.contextWindow,
          percent,
          100,
          "Requested compaction threshold",
        ),
      });
    } catch {
      return missingClaudeContext(
        `模型 ${id} 的安全压缩阈值无效；已同时省略两项 Claude 压缩覆盖。`,
      );
    }
  }

  const contextWindow = Math.min(
    ...calculations.map((calculation) => calculation.contextWindow),
  );
  const safeCompactAt = Math.min(
    ...calculations.map((calculation) => calculation.compactAt),
  );
  const uncappedSafeCompactAt = Math.min(
    ...calculations.map((calculation) => calculation.uncappedCompactAt),
  );
  let capped;
  let uncapped;
  try {
    capped = aggregateClaudeCompaction(contextWindow, safeCompactAt);
    uncapped = aggregateClaudeCompaction(
      contextWindow,
      uncappedSafeCompactAt,
    );
  } catch {
    return missingClaudeContext(
      "Claude 保守压缩比例无效；已同时省略两项压缩覆盖。",
    );
  }
  return {
    environment: {
      CLAUDE_CODE_AUTO_COMPACT_WINDOW: String(contextWindow),
      CLAUDE_AUTOCOMPACT_PCT_OVERRIDE: String(capped.percent),
    },
    warning: "",
    compaction: {
      contextWindow,
      compactAt: capped.compactAt,
      reserved: contextWindow - capped.compactAt,
      percent: capped.percent,
      safeCompactAt,
      outputReserveLimited: capped.compactAt < uncapped.compactAt,
    },
  };
}

function aggregateClaudeCompaction(contextWindow, safeCompactAt) {
  const percent = floorMultiplyDivide(
    safeCompactAt,
    100,
    contextWindow,
    "Claude conservative compaction percent",
  );
  const compactAt = Math.min(
    safeCompactAt,
    floorMultiplyDivide(
      contextWindow,
      percent,
      100,
      "Claude final compaction threshold",
    ),
  );
  return { percent, compactAt };
}

function missingClaudeContext(warning) {
  return { environment: {}, warning, compaction: null };
}

export function codexConfig({
  baseURL,
  model,
  providerID,
  providerName,
  envVariable,
  contextWindow,
  compactAt,
} = {}) {
  const normalizedModel = requiredText(model, "Model");
  const normalizedProviderID = providerIdentifier(providerID);
  const normalizedBaseURL = requiredText(baseURL, "Base URL");
  const normalizedProviderName = requiredText(providerName, "Provider name");
  const normalizedEnvVariable = optionalEnvironmentVariable(envVariable);
  const lines = [
    `model = ${tomlString(normalizedModel)}`,
    `model_provider = ${tomlString(normalizedProviderID)}`,
  ];
  if (contextWindow != null || compactAt != null) {
    requireCompactionNumbers(contextWindow, compactAt);
    lines.push(
      `model_context_window = ${contextWindow}`,
      `model_auto_compact_token_limit = ${compactAt}`,
    );
  }
  lines.push(
    "",
    `[model_providers.${tomlString(normalizedProviderID)}]`,
    `name = ${tomlString(normalizedProviderName)}`,
    `base_url = ${tomlString(normalizedBaseURL)}`,
  );
  if (normalizedEnvVariable) {
    lines.push(`env_key = ${tomlString(normalizedEnvVariable)}`);
  }
  lines.push('wire_api = "responses"', "");
  return lines.join("\n");
}

export function codexTarget({ scope = "global", profileName = "" } = {}) {
  if (scope === "global") {
    return {
      destination: "~/.codex/config.toml",
      filename: "config.toml",
      invocation: "codex",
    };
  }
  if (scope !== "named") {
    throw new Error(`Unsupported Codex scope: ${scope}`);
  }

  const normalizedName = String(profileName).trim();
  if (!/^[A-Za-z0-9_-]+$/.test(normalizedName)) {
    throw new Error("Codex profile name may contain only letters, numbers, _ and -");
  }
  return {
    destination: `~/.codex/${normalizedName}.config.toml`,
    filename: `${normalizedName}.config.toml`,
    invocation: `codex --profile ${normalizedName}`,
  };
}

export function openCodeConfig({
  protocol,
  baseURL,
  providerID,
  providerName,
  model,
  envVariable,
  contextWindow,
  maxOutputTokens,
  compactAt,
} = {}) {
  const normalizedProtocol = String(protocol);
  if (!["anthropic", "openai"].includes(normalizedProtocol)) {
    throw new Error(`Unsupported protocol: ${protocol}`);
  }
  const normalizedProviderID = providerIdentifier(providerID);
  const normalizedModel = requiredText(model, "Model");
  const options = {
    baseURL: requiredText(baseURL, "Base URL"),
  };
  const normalizedEnvVariable = optionalEnvironmentVariable(envVariable);
  if (normalizedEnvVariable) {
    options.apiKey = `{env:${normalizedEnvVariable}}`;
  }

  const modelConfig = { name: normalizedModel };
  let compaction;
  if (contextWindow != null && maxOutputTokens != null) {
    requirePositiveSafeInteger(contextWindow, "Model context window");
    requirePositiveSafeInteger(
      maxOutputTokens,
      "Model max output tokens",
    );
    if (maxOutputTokens >= contextWindow) {
      throw new RangeError(
        "Model max output tokens must be below the context window",
      );
    }
    modelConfig.limit = {
      context: contextWindow,
      output: maxOutputTokens,
    };
    if (compactAt != null) {
      requireCompactionNumbers(contextWindow, compactAt);
      compaction = {
        auto: true,
        reserved: contextWindow - compactAt,
      };
    }
  }

  const config = {
    $schema: "https://opencode.ai/config.json",
    model: `${normalizedProviderID}/${normalizedModel}`,
    provider: {
      [normalizedProviderID]: {
        npm:
          normalizedProtocol === "anthropic"
            ? "@ai-sdk/anthropic"
            : "@ai-sdk/openai",
        name: requiredText(providerName, "Provider name"),
        options,
        models: {
          [normalizedModel]: modelConfig,
        },
      },
    },
  };
  if (compaction) {
    config.compaction = compaction;
  }
  return config;
}

export function openCodeTarget({ scope = "global" } = {}) {
  if (scope === "global") {
    return {
      destination: "~/.config/opencode/opencode.json",
      filename: "opencode.global.json",
    };
  }
  if (scope === "project") {
    return {
      destination: "opencode.json",
      filename: "opencode.json",
    };
  }
  throw new Error(`Unsupported OpenCode scope: ${scope}`);
}

export function profileBaseURL(origin, slug) {
  const publicURL = new URL(String(origin).trim());
  if (!["http:", "https:"].includes(publicURL.protocol)) {
    throw new Error("Public origin must use HTTP or HTTPS");
  }
  if (publicURL.username || publicURL.password) {
    throw new Error("Public origin must not contain credentials");
  }

  const normalizedSlug = String(slug).trim().replace(/^\/+|\/+$/g, "");
  if (!normalizedSlug || normalizedSlug.includes("/")) {
    throw new Error("Profile slug must be one non-empty path segment");
  }
  return `${publicURL.origin}/${normalizedSlug}`;
}

export function openAIBaseURL(origin, slug) {
  return `${profileBaseURL(origin, slug)}/v1`;
}

export function claudeSettings({ baseURL, models = {}, authVariable } = {}) {
  const env = {
    ANTHROPIC_BASE_URL: String(baseURL ?? ""),
  };

  for (const [alias, variable] of Object.entries(claudeModelVariables)) {
    const value = String(models[alias] ?? "").trim();
    if (value) {
      env[variable] = value;
    }
  }
  if (claudeAuthVariables.has(authVariable)) {
    env[authVariable] = "<SET_LOCALLY>";
  }

  return {
    $schema: claudeSchema,
    env,
  };
}

export function openAIEnvironment({ baseURL, authVariable } = {}) {
  const environment = {
    OPENAI_BASE_URL: String(baseURL ?? ""),
  };
  if (authVariable === "OPENAI_API_KEY") {
    environment.OPENAI_API_KEY = "<SET_LOCALLY>";
  }
  return environment;
}

export function shellSnippet(environment) {
  return Object.entries(environment ?? {})
    .map(([name, value]) => {
      if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(name)) {
        throw new Error(`Invalid environment variable: ${name}`);
      }
      const quoted = String(value).replaceAll("'", "'\"'\"'");
      return `export ${name}='${quoted}'`;
    })
    .join("\n");
}

export function downloadText(
  filename,
  content,
  mimeType = "text/plain;charset=utf-8",
) {
  const objectURL = URL.createObjectURL(
    new Blob([String(content)], { type: mimeType }),
  );
  try {
    const anchor = document.createElement("a");
    anchor.href = objectURL;
    anchor.download = filename;
    anchor.click();
  } finally {
    URL.revokeObjectURL(objectURL);
  }
}

function requiredText(value, label) {
  const normalized = String(value ?? "").trim();
  if (!normalized) {
    throw new Error(`${label} is required`);
  }
  return normalized;
}

function providerIdentifier(value) {
  const normalized = requiredText(value, "Provider ID");
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(normalized)) {
    throw new Error("Provider ID contains unsupported characters");
  }
  return normalized;
}

function optionalEnvironmentVariable(value) {
  const normalized = String(value ?? "").trim();
  if (
    normalized &&
    !/^[A-Za-z_][A-Za-z0-9_]*$/.test(normalized)
  ) {
    throw new Error("Invalid environment variable");
  }
  return normalized;
}

function tomlString(value) {
  return JSON.stringify(String(value));
}

function requireCompactionNumbers(contextWindow, compactAt) {
  if (
    !Number.isSafeInteger(contextWindow) ||
    contextWindow <= 0 ||
    !Number.isSafeInteger(compactAt) ||
    compactAt <= 0 ||
    compactAt >= contextWindow
  ) {
    throw new RangeError(
      "Context window and compaction threshold must be valid integers",
    );
  }
}

function requirePositiveSafeInteger(value, label) {
  if (!Number.isSafeInteger(value) || value <= 0) {
    throw new RangeError(`${label} must be a positive safe integer`);
  }
  return value;
}

function floorMultiplyDivide(value, multiplier, divisor, label) {
  requirePositiveSafeInteger(value, `${label} value`);
  requirePositiveSafeInteger(multiplier, `${label} multiplier`);
  requirePositiveSafeInteger(divisor, `${label} divisor`);
  const result =
    (BigInt(value) * BigInt(multiplier)) / BigInt(divisor);
  if (result <= 0n || result > BigInt(Number.MAX_SAFE_INTEGER)) {
    throw new RangeError(`${label} must be a positive safe integer`);
  }
  const number = Number(result);
  return requirePositiveSafeInteger(number, label);
}

function requireCompactionPercent(percent) {
  if (!Number.isInteger(percent) || percent < 1 || percent > 99) {
    throw new RangeError("Compaction percent must be an integer from 1 to 99");
  }
}

export function openConfigurationGenerator(
  root,
  profile,
  { origin = currentOrigin() } = {},
) {
  const protocol = String(profile?.config?.protocol ?? "anthropic");
  const slug = String(profile?.slug ?? "");
  const dialog = generatorElement("dialog", "generator-dialog");
  const panel = generatorElement("section", "stack");
  const heading = generatorText(
    "h2",
    protocol === "openai" ? "OpenAI Agent 配置" : "Anthropic Agent 配置",
  );
  heading.id = `generator-${profile?.id ?? "draft"}-title`;
  dialog.setAttribute("aria-labelledby", heading.id);
  const description = generatorText(
    "p",
    `基于 ${String(profile?.display_name ?? slug)} (${slug}) 即时生成；临时输入不会保存。`,
  );
  description.className = "muted";
  const close = generatorButton("返回 Profiles", "button-secondary");
  const header = generatorElement("div", "card-header");
  const titleGroup = generatorElement("div");
  titleGroup.append(heading, description);
  header.append(titleGroup, close);
  panel.append(header);

  const adapterControls = generatorElement("div", "card generator-inputs");
  const adapter = generatorSelect(
    adapterControls,
    "Agent",
    "generator-agent",
    availableAgentAdapters(protocol),
  );
  const adapterRoot = generatorElement("div", "stack");

  function renderAdapter() {
    adapterRoot.replaceChildren();
    const details = {
      slug,
      displayName: String(profile?.display_name ?? slug),
      origin,
      profileModels: Array.isArray(profile?.config?.models)
        ? profile.config.models
        : [],
    };
    if (adapter.value === "claude-code") {
      renderAnthropicGenerator(
        adapterRoot,
        slug,
        origin,
        details.profileModels,
      );
    } else if (adapter.value === "opencode-anthropic") {
      renderOpenCodeGenerator(adapterRoot, "anthropic", details);
    } else if (adapter.value === "opencode-responses") {
      renderOpenCodeGenerator(adapterRoot, "openai", details);
    } else if (adapter.value === "codex-responses") {
      renderCodexGenerator(adapterRoot, details);
    } else {
      renderGenericGenerator(adapterRoot, protocol, slug, origin);
    }
  }
  adapter.addEventListener("input", renderAdapter);
  adapter.addEventListener("change", renderAdapter);
  panel.append(adapterControls, adapterRoot);
  renderAdapter();

  const closeDialog = () => {
    dialog.close();
    dialog.remove();
  };
  close.addEventListener("click", closeDialog);
  dialog.addEventListener("cancel", (event) => {
    event.preventDefault();
    closeDialog();
  });

  dialog.append(panel);
  root.append(dialog);
  dialog.showModal();
  return dialog;
}

function renderAnthropicGenerator(parent, slug, origin, profileModels) {
  const inputs = generatorElement("div", "card generator-inputs");
  const inputGrid = generatorElement("div", "form-grid");
  const publicOrigin = generatorInput(
    inputGrid,
    "公网地址",
    "generator-public-origin",
    normalizedOrigin(origin),
  );
  const sonnet = generatorModelInput(
    inputGrid,
    "Sonnet 模型",
    "generator-sonnet",
    "",
    profileModels,
  );
  const haiku = generatorModelInput(
    inputGrid,
    "Haiku 模型",
    "generator-haiku",
    "",
    profileModels,
  );
  const opus = generatorModelInput(
    inputGrid,
    "Opus 模型",
    "generator-opus",
    "",
    profileModels,
  );
  const compactionPercent = generatorNumberInput(
    inputGrid,
    "自动压缩触发比例",
    "generator-compaction-percent",
    85,
    { min: 1, max: 99, step: 1 },
  );
  const authVariable = generatorSelect(
    inputGrid,
    "认证变量",
    "generator-auth-variable",
    [
      ["不生成", "不生成"],
      ["ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_AUTH_TOKEN"],
      ["ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY"],
    ],
  );
  const secretNote = generatorText(
    "p",
    "这里只生成 <SET_LOCALLY> 占位；请在客户端本地设置真实密钥。",
  );
  secretNote.className = "muted";
  inputs.append(inputGrid, secretNote);

  const scopes = [
    {
      label: "全局 ~/.claude/settings.json",
      destination: "~/.claude/settings.json",
      type: "json",
    },
    {
      label: "项目共享 .claude/settings.json",
      destination: ".claude/settings.json",
      type: "json",
    },
    {
      label: "项目私有 .claude/settings.local.json",
      destination: ".claude/settings.local.json",
      type: "json",
    },
    {
      label: "Shell",
      destination: "Shell",
      type: "shell",
    },
  ];
  let activeScope = scopes[0];
  const tabs = generatorElement("div", "generator-tabs");
  tabs.setAttribute("role", "tablist");
  tabs.setAttribute("aria-label", "配置范围");
  const tabButtons = scopes.map((scope) => {
    const button = generatorButton(scope.label, "button-secondary");
    button.setAttribute("role", "tab");
    button.addEventListener("click", () => {
      activeScope = scope;
      update();
    });
    tabs.append(button);
    return button;
  });

  const alert = generatorElement("div", "error-banner");
  alert.setAttribute("role", "alert");
  alert.hidden = true;
  const capabilityWarning = generatorElement(
    "div",
    "warning-banner generator-capability-warning",
  );
  capabilityWarning.hidden = true;
  const compactionSummary = generatorElement(
    "p",
    "muted generator-compaction-summary",
  );
  compactionSummary.hidden = true;
  const output = generatorElement("div");

  function update() {
    for (let index = 0; index < scopes.length; index += 1) {
      tabButtons[index].setAttribute(
        "aria-selected",
        String(scopes[index] === activeScope),
      );
    }
    try {
      const baseURL = profileBaseURL(publicOrigin.value, slug);
      const settings = claudeSettings({
        baseURL,
        models: {
          sonnet: sonnet.value,
          haiku: haiku.value,
          opus: opus.value,
        },
        authVariable: authVariable.value,
      });
      const contextResult = claudeContextEnvironment(
        profileModels,
        {
          sonnet: sonnet.value,
          haiku: haiku.value,
          opus: opus.value,
        },
        Number(compactionPercent.value),
      );
      Object.assign(settings.env, contextResult.environment);
      renderCapabilityState(
        capabilityWarning,
        compactionSummary,
        contextResult,
      );
      const isShell = activeScope.type === "shell";
      renderOutput(output, {
        title: activeScope.label,
        destination: activeScope.destination,
        content: isShell
          ? shellSnippet(settings.env)
          : JSON.stringify(settings, null, 2),
        filename: isShell ? `${slug}.env.sh` : "settings.json",
        mimeType: isShell
          ? "text/x-shellscript;charset=utf-8"
          : "application/json;charset=utf-8",
      });
      alert.hidden = true;
      alert.textContent = "";
    } catch {
      output.replaceChildren();
      capabilityWarning.hidden = true;
      compactionSummary.hidden = true;
      alert.textContent =
        "无法生成配置，请检查公网地址、Profile slug 和 1–99 的整数压缩比例。";
      alert.hidden = false;
    }
  }

  for (const control of [
    publicOrigin,
    sonnet,
    haiku,
    opus,
    authVariable,
    compactionPercent,
  ]) {
    control.addEventListener("input", update);
    control.addEventListener("change", update);
  }

  parent.append(
    inputs,
    tabs,
    alert,
    capabilityWarning,
    compactionSummary,
    output,
  );
  update();
}

function renderGenericGenerator(parent, protocol, slug, origin) {
  const inputs = generatorElement("div", "card generator-inputs");
  const publicOrigin = generatorInput(
    inputs,
    "公网地址",
    "generator-public-origin",
    normalizedOrigin(origin),
  );
  const alert = generatorElement("div", "error-banner");
  alert.setAttribute("role", "alert");
  alert.hidden = true;
  const outputs = generatorElement("div", "generator-output-grid");

  function update() {
    try {
      const profileURL = profileBaseURL(publicOrigin.value, slug);
      const apiBaseURL = `${profileURL}/v1`;
      const environment =
        protocol === "openai"
          ? openAIEnvironment({
              baseURL: apiBaseURL,
              authVariable: "不生成",
            })
          : { ANTHROPIC_BASE_URL: profileURL };
      const shell = shellSnippet(
        environment,
      );
      const profileOutput = generatorElement("div");
      const apiOutput = generatorElement("div");
      const shellOutput = generatorElement("div");
      renderOutput(profileOutput, {
        title: "Profile 前缀",
        content: profileURL,
        filename: `${slug}.profile-url.txt`,
        outputClass: "generator-address",
      });
      renderOutput(apiOutput, {
        title:
          protocol === "openai"
            ? "OpenAI API base"
            : "Anthropic API base",
        content: apiBaseURL,
        filename:
          protocol === "openai"
            ? `${slug}.openai-base-url.txt`
            : `${slug}.anthropic-base-url.txt`,
        outputClass: "generator-address",
      });
      renderOutput(shellOutput, {
        title: "Shell",
        content: shell,
        filename: `${slug}.env.sh`,
        mimeType: "text/x-shellscript;charset=utf-8",
      });
      outputs.replaceChildren(profileOutput, apiOutput, shellOutput);
      alert.hidden = true;
      alert.textContent = "";
    } catch {
      outputs.replaceChildren();
      alert.textContent =
        "无法生成配置，请检查公网地址和 Profile slug。地址不能包含凭据。";
      alert.hidden = false;
    }
  }

  publicOrigin.addEventListener("input", update);
  publicOrigin.addEventListener("change", update);
  inputs.append(
    generatorText(
      "p",
      protocol === "openai"
        ? "SDK 会在 API base 后继续追加 /responses 或 /chat/completions。"
        : "Anthropic 客户端通常会在 API base 后追加 /messages。",
    ),
  );
  parent.append(inputs, alert, outputs);
  update();
}

function renderOpenCodeGenerator(
  parent,
  protocol,
  { slug, displayName, origin, profileModels },
) {
  const inputs = generatorElement("div", "card generator-inputs");
  const inputGrid = generatorElement("div", "form-grid");
  const publicOrigin = generatorInput(
    inputGrid,
    "公网地址",
    "generator-public-origin",
    normalizedOrigin(origin),
  );
  const model = generatorModelInput(
    inputGrid,
    "模型 ID",
    "generator-model",
    preferredModelID(
      profileModels,
      protocol === "openai" ? "gpt-5.4" : "sonnet",
    ),
    profileModels,
  );
  const envVariable = generatorInput(
    inputGrid,
    "密钥环境变量",
    "generator-env-variable",
    protocol === "openai" ? "OPENAI_API_KEY" : "ANTHROPIC_API_KEY",
  );
  const scope = generatorSelect(
    inputGrid,
    "配置范围",
    "generator-opencode-scope",
    [
      ["global", "全局"],
      ["project", "项目"],
    ],
  );
  const compactionPercent = generatorNumberInput(
    inputGrid,
    "自动压缩触发比例",
    "generator-compaction-percent",
    85,
    { min: 1, max: 99, step: 1 },
  );
  const note = generatorText(
    "p",
    protocol === "openai"
      ? "上游必须支持 /v1/responses；密钥只会以环境变量引用写入配置。"
      : "使用 Anthropic Messages API；密钥只会以环境变量引用写入配置。",
  );
  note.className = protocol === "openai" ? "warning-banner" : "muted";
  inputs.append(inputGrid, note);

  const alert = generatorElement("div", "error-banner");
  alert.setAttribute("role", "alert");
  alert.hidden = true;
  const capabilityWarning = generatorElement(
    "div",
    "warning-banner generator-capability-warning",
  );
  capabilityWarning.hidden = true;
  const compactionSummary = generatorElement(
    "p",
    "muted generator-compaction-summary",
  );
  compactionSummary.hidden = true;
  const output = generatorElement("div");

  function update() {
    try {
      const target = openCodeTarget({ scope: scope.value });
      const contextResult = selectedOpenCodeContext(
        profileModels,
        model.value,
        Number(compactionPercent.value),
      );
      const config = openCodeConfig({
        protocol,
        baseURL: openAIBaseURL(publicOrigin.value, slug),
        providerID: `llm-proxy-${slug}`,
        providerName: `llm-proxy / ${displayName}`,
        model: model.value,
        envVariable: envVariable.value,
        contextWindow: contextResult.compaction?.contextWindow,
        maxOutputTokens: contextResult.model?.max_output_tokens,
        compactAt: contextResult.compaction?.compactAt,
      });
      renderOutput(output, {
        title:
          protocol === "openai"
            ? "OpenCode (Responses)"
            : "OpenCode (Anthropic)",
        destination: target.destination,
        content: JSON.stringify(config, null, 2),
        filename: target.filename,
        mimeType: "application/json;charset=utf-8",
      });
      renderCapabilityState(
        capabilityWarning,
        compactionSummary,
        contextResult,
      );
      alert.hidden = true;
      alert.textContent = "";
    } catch {
      output.replaceChildren();
      capabilityWarning.hidden = true;
      compactionSummary.hidden = true;
      alert.textContent =
        "无法生成配置，请检查公网地址、模型 ID、环境变量名和 1–99 的整数压缩比例。";
      alert.hidden = false;
    }
  }

  for (const control of [
    publicOrigin,
    model,
    envVariable,
    scope,
    compactionPercent,
  ]) {
    control.addEventListener("input", update);
    control.addEventListener("change", update);
  }
  parent.append(
    inputs,
    alert,
    capabilityWarning,
    compactionSummary,
    output,
  );
  update();
}

function renderCodexGenerator(
  parent,
  { slug, displayName, origin, profileModels },
) {
  const inputs = generatorElement("div", "card generator-inputs");
  const inputGrid = generatorElement("div", "form-grid");
  const publicOrigin = generatorInput(
    inputGrid,
    "公网地址",
    "generator-public-origin",
    normalizedOrigin(origin),
  );
  const model = generatorModelInput(
    inputGrid,
    "模型 ID",
    "generator-model",
    preferredModelID(profileModels, "gpt-5.4"),
    profileModels,
  );
  const envVariable = generatorInput(
    inputGrid,
    "密钥环境变量",
    "generator-env-variable",
    "OPENAI_API_KEY",
  );
  const scope = generatorSelect(
    inputGrid,
    "配置范围",
    "generator-codex-scope",
    [
      ["global", "全局"],
      ["named", "命名 Profile"],
    ],
  );
  const profileName = generatorInput(
    inputGrid,
    "Codex Profile 名称",
    "generator-codex-profile",
    slug.replaceAll("-", "_"),
  );
  const compactionPercent = generatorNumberInput(
    inputGrid,
    "自动压缩触发比例",
    "generator-compaction-percent",
    85,
    { min: 1, max: 99, step: 1 },
  );
  const protocolNote = generatorText(
    "p",
    "上游必须支持 /v1/responses；Codex 不会使用 Chat Completions。Codex 可能把较大的窗口限制到自身目录上限。密钥只会以环境变量名写入配置。",
  );
  protocolNote.className = "warning-banner";
  const invocation = generatorElement("p", "muted");
  inputs.append(inputGrid, protocolNote, invocation);

  const alert = generatorElement("div", "error-banner");
  alert.setAttribute("role", "alert");
  alert.hidden = true;
  const capabilityWarning = generatorElement(
    "div",
    "warning-banner generator-capability-warning",
  );
  capabilityWarning.hidden = true;
  const compactionSummary = generatorElement(
    "p",
    "muted generator-compaction-summary",
  );
  compactionSummary.hidden = true;
  const output = generatorElement("div");

  function update() {
    profileName.parentNode.hidden = scope.value !== "named";
    try {
      const target = codexTarget({
        scope: scope.value,
        profileName: profileName.value,
      });
      const contextResult = selectedModelContext(
        profileModels,
        model.value,
        Number(compactionPercent.value),
      );
      const content = codexConfig({
        baseURL: openAIBaseURL(publicOrigin.value, slug),
        model: model.value,
        providerID: `llm_proxy_${slug.replaceAll("-", "_")}`,
        providerName: `llm-proxy / ${displayName}`,
        envVariable: envVariable.value,
        contextWindow: contextResult.compaction?.contextWindow,
        compactAt: contextResult.compaction?.compactAt,
      });
      invocation.textContent = `启动：${target.invocation}`;
      renderOutput(output, {
        title: "Codex (Responses)",
        destination: target.destination,
        content,
        filename: target.filename,
        mimeType: "application/toml;charset=utf-8",
      });
      renderCapabilityState(
        capabilityWarning,
        compactionSummary,
        contextResult,
      );
      alert.hidden = true;
      alert.textContent = "";
    } catch {
      output.replaceChildren();
      invocation.textContent = "";
      capabilityWarning.hidden = true;
      compactionSummary.hidden = true;
      alert.textContent =
        "无法生成配置，请检查公网地址、模型 ID、环境变量名、Profile 名称和 1–99 的整数压缩比例。";
      alert.hidden = false;
    }
  }

  for (const control of [
    publicOrigin,
    model,
    envVariable,
    scope,
    profileName,
    compactionPercent,
  ]) {
    control.addEventListener("input", update);
    control.addEventListener("change", update);
  }
  parent.append(
    inputs,
    alert,
    capabilityWarning,
    compactionSummary,
    output,
  );
  update();
}

function renderOutput(
  parent,
  {
    title,
    destination = "",
    content,
    filename,
    mimeType = "text/plain;charset=utf-8",
    outputClass = "generator-output",
  },
) {
  const card = generatorElement("section", "card generator-output-card");
  const heading = generatorText("h3", title);
  card.append(heading);
  if (destination) {
    const path = generatorText("p", `目标：${destination}`);
    path.className = "muted generator-destination";
    card.append(path);
  }
  const output = generatorText("pre", content);
  output.className = outputClass;
  const status = generatorElement("span", "muted");
  status.setAttribute("role", "status");
  const actions = generatorElement("div", "cluster generator-output-actions");
  const copy = generatorButton("复制", "button-secondary");
  const download = generatorButton("下载", "button-secondary");
  copy.addEventListener("click", async () => {
    try {
      if (!globalThis.navigator?.clipboard?.writeText) {
        throw new Error("Clipboard unavailable");
      }
      await globalThis.navigator.clipboard.writeText(content);
      status.textContent = "已复制";
    } catch {
      status.textContent = "无法访问剪贴板，请手动复制。";
    }
  });
  download.addEventListener("click", () => {
    downloadText(filename, content, mimeType);
    status.textContent = `已下载 ${filename}`;
  });
  actions.append(copy, download, status);
  card.append(output, actions);
  parent.replaceChildren(card);
}

function normalizedOrigin(origin) {
  try {
    const publicURL = new URL(String(origin).trim());
    if (
      !["http:", "https:"].includes(publicURL.protocol) ||
      publicURL.username ||
      publicURL.password
    ) {
      return String(origin ?? "");
    }
    return publicURL.origin;
  } catch {
    return String(origin ?? "");
  }
}

function currentOrigin() {
  return globalThis.location?.origin || "http://localhost";
}

function generatorElement(tagName, className = "") {
  const result = document.createElement(tagName);
  result.className = className;
  return result;
}

function generatorText(tagName, text) {
  const result = generatorElement(tagName);
  result.textContent = String(text);
  return result;
}

function generatorButton(text, variant) {
  const button = generatorText("button", text);
  button.type = "button";
  button.className = `button ${variant}`;
  return button;
}

function generatorInput(parent, labelText, name, value) {
  const input = generatorElement("input");
  input.type = "text";
  input.name = name;
  input.value = String(value ?? "");
  appendGeneratorField(parent, labelText, input);
  return input;
}

function generatorNumberInput(
  parent,
  labelText,
  name,
  value,
  { min, max, step },
) {
  const input = generatorElement("input");
  input.type = "number";
  input.name = name;
  input.value = String(value);
  input.min = String(min);
  input.max = String(max);
  input.step = String(step);
  appendGeneratorField(parent, labelText, input);
  return input;
}

function generatorModelInput(parent, labelText, name, value, profileModels) {
  const input = generatorInput(parent, labelText, name, value);
  const models = (profileModels ?? []).filter(
    (model) => typeof model?.id === "string" && model.id,
  );
  if (models.length === 0) {
    return input;
  }
  const list = generatorElement("datalist");
  list.id = `${name}-profile-models`;
  list.setAttribute("id", list.id);
  input.setAttribute("list", list.id);
  for (const model of models) {
    const option = generatorText("option", model.id);
    option.value = model.id;
    list.append(option);
  }
  parent.append(list);
  return input;
}

function preferredModelID(profileModels, fallback) {
  const first = (profileModels ?? []).find(
    (model) => typeof model?.id === "string" && model.id,
  );
  return first?.id ?? fallback;
}

function selectedModelContext(profileModels, value, percent) {
  requireCompactionPercent(percent);
  const modelID = String(value ?? "");
  const model = (profileModels ?? []).find(
    (candidate) => candidate?.id === modelID,
  );
  if (!model || model.context_window == null) {
    return {
      model: null,
      compaction: null,
      warning: `模型 ${modelID || "（空）"} 未在 Profile 中保存上下文窗口；已省略 Agent 上下文与压缩设置。`,
    };
  }
  const compaction = calculateCompaction(model, percent);
  compaction.outputReserveLimited =
    model.max_output_tokens != null &&
    model.context_window - model.max_output_tokens <
      floorMultiplyDivide(
        model.context_window,
        percent,
        100,
        "Requested compaction threshold",
      );
  return { model, compaction, warning: "" };
}

function selectedOpenCodeContext(profileModels, value, percent) {
  requireCompactionPercent(percent);
  const modelID = String(value ?? "");
  const model = (profileModels ?? []).find(
    (candidate) => candidate?.id === modelID,
  );
  if (model?.context_window != null && model.max_output_tokens == null) {
    return {
      model,
      compaction: null,
      warning:
        `模型 ${modelID} 未在 Profile 中保存最大输出 tokens；` +
        "OpenCode stable schema 要求 context 与 output 同时存在，已省略 limit 与 compaction。",
    };
  }
  return selectedModelContext(profileModels, modelID, percent);
}

function renderCapabilityState(warningElement, summaryElement, result) {
  warningElement.textContent = result.warning;
  warningElement.hidden = !result.warning;
  if (!result.compaction) {
    summaryElement.textContent = "";
    summaryElement.hidden = true;
    return;
  }

  const compaction = result.compaction;
  let summary =
    `有效阈值 ${compaction.compactAt} tokens；` +
    `保留 ${compaction.reserved} tokens；` +
    `窗口 ${compaction.contextWindow} tokens（${compaction.percent}%）。`;
  if (
    compaction.safeCompactAt != null &&
    compaction.safeCompactAt !== compaction.compactAt
  ) {
    summary += ` 映射模型的最小安全绝对阈值为 ${compaction.safeCompactAt} tokens。`;
  }
  if (compaction.outputReserveLimited) {
    summary += " 阈值受模型最大输出 tokens 的安全留白限制。";
  }
  summaryElement.textContent = summary;
  summaryElement.hidden = false;
}

function generatorSelect(parent, labelText, name, choices) {
  const select = generatorElement("select");
  select.name = name;
  for (const [value, label] of choices) {
    const option = generatorText("option", label);
    option.value = value;
    select.append(option);
  }
  select.value = choices[0][0];
  appendGeneratorField(parent, labelText, select);
  return select;
}

function appendGeneratorField(parent, labelText, control) {
  const wrapper = generatorElement("div", "form-field");
  const label = generatorText("label", labelText);
  control.id = control.name;
  label.setAttribute("for", control.id);
  wrapper.append(label, control);
  parent.append(wrapper);
}
