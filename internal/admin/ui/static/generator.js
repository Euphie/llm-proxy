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
    protocol === "openai" ? "OpenAI 客户端配置" : "Claude Code 配置",
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

  if (protocol === "openai") {
    renderOpenAIGenerator(panel, slug, origin);
  } else {
    renderAnthropicGenerator(panel, slug, origin);
  }

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

function renderAnthropicGenerator(parent, slug, origin) {
  const inputs = generatorElement("div", "card generator-inputs");
  const inputGrid = generatorElement("div", "form-grid");
  const publicOrigin = generatorInput(
    inputGrid,
    "公网地址",
    "generator-public-origin",
    normalizedOrigin(origin),
  );
  const sonnet = generatorInput(
    inputGrid,
    "Sonnet 模型",
    "generator-sonnet",
    "",
  );
  const haiku = generatorInput(
    inputGrid,
    "Haiku 模型",
    "generator-haiku",
    "",
  );
  const opus = generatorInput(
    inputGrid,
    "Opus 模型",
    "generator-opus",
    "",
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
      alert.textContent =
        "无法生成配置，请检查公网地址和 Profile slug。地址不能包含凭据。";
      alert.hidden = false;
    }
  }

  for (const control of [
    publicOrigin,
    sonnet,
    haiku,
    opus,
    authVariable,
  ]) {
    control.addEventListener("input", update);
    control.addEventListener("change", update);
  }

  parent.append(inputs, tabs, alert, output);
  update();
}

function renderOpenAIGenerator(parent, slug, origin) {
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
      const apiBaseURL = openAIBaseURL(publicOrigin.value, slug);
      const shell = shellSnippet(
        openAIEnvironment({
          baseURL: apiBaseURL,
          authVariable: "不生成",
        }),
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
        title: "OpenAI API base",
        content: apiBaseURL,
        filename: `${slug}.openai-base-url.txt`,
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
      "SDK 会在 API base 后继续追加 /responses 或 /chat/completions。",
    ),
  );
  parent.append(inputs, alert, outputs);
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
