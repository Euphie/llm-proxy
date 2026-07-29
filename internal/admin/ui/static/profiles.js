import { openConfigurationGenerator } from "./generator.js";

const defaultVision = {
  enabled: false,
  model: "sonnet",
  max_tokens: 2048,
  timeout: "2m",
  max_concurrency: 4,
  cache_ttl: "30m",
  cache_max_entries: 512,
  prompt: "",
};

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
      upstream: "",
      vision: { ...defaultVision },
      overload_rules: [],
    },
  };
}

export function profilePayload(draft) {
  const config = draft.config || {};
  const protocol = String(draft.protocol ?? config.protocol ?? "anthropic");
  const vision = draft.vision || config.vision || {};
  const rules = draft.overload_rules || config.overload_rules || [];

  return {
    slug: String(draft.slug ?? ""),
    display_name: String(draft.display_name ?? ""),
    enabled: Boolean(draft.enabled),
    make_default: Boolean(draft.make_default),
    config: {
      version: Number(draft.version ?? config.version ?? 1),
      protocol,
      upstream: String(draft.upstream ?? config.upstream ?? ""),
      vision: {
        enabled: protocol === "anthropic" && Boolean(vision.enabled),
        model: String(vision.model ?? ""),
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
  const draft = defaultProfileDraft(profile?.config?.protocol);
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
      vision: {
        ...defaultVision,
        ...(profile?.config?.vision || {}),
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
  const titleGroup = element("div");
  const title = textElement("h2", "Profiles");
  const description = textElement(
    "p",
    "管理协议、上游地址、视觉增强和容错规则。",
  );
  description.className = "muted";
  titleGroup.append(title, description);
  const create = actionButton("新建 Profile", "button");
  create.addEventListener("click", () => actions.create?.());
  heading.append(titleGroup, create);

  const alert = element("div", "error-banner");
  alert.setAttribute("role", "alert");
  alert.hidden = true;

  const grid = element("div", "profile-grid");
  for (const profile of profiles) {
    const card = element("article", "card profile-card");
    const header = element("div", "card-header");
    const identity = element("div");
    const name = textElement("h2", String(profile.display_name ?? ""));
    const slug = textElement("p", String(profile.slug ?? ""));
    slug.className = "profile-slug";
    identity.append(name, slug);

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
    grid.append(card);
  }

  page.append(heading, alert, grid);
  root.replaceChildren(page);
}

export function renderProfileEditor(root, source, actions = {}) {
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
  );
  const upstream = fieldInput(
    basicGrid,
    "Upstream",
    "upstream",
    working.config.upstream,
    { required: true, type: "url" },
  );
  const version = fieldInput(
    basicGrid,
    "配置版本",
    "version",
    working.config.version,
    { required: true, type: "number", min: "1" },
  );
  const enabled = checkboxField(basicGrid, "启用 Profile", "enabled");
  enabled.checked = working.enabled;
  const isCurrentDefault = working.make_default;
  const makeDefault = checkboxField(
    basicGrid,
    "设为默认 Profile",
    "make_default",
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

  const vision = editorSection("视觉增强");
  const visionNote = textElement(
    "p",
    "视觉预处理目前需要 Anthropic 协议。",
  );
  visionNote.className = "warning-banner";
  const visionControls = element("div", "vision-controls stack");
  const visionEnabled = checkboxField(
    visionControls,
    "启用视觉预处理",
    "vision_enabled",
  );
  visionEnabled.checked = Boolean(working.config.vision.enabled);
  const visionDetails = element("details", "card vision-details");
  const visionSummary = textElement("summary", "视觉参数");
  const visionGrid = element("div", "form-grid details-content");
  const visionModel = fieldInput(
    visionGrid,
    "模型",
    "vision_model",
    working.config.vision.model,
  );
  const visionMaxTokens = fieldInput(
    visionGrid,
    "最大 Token",
    "vision_max_tokens",
    working.config.vision.max_tokens,
    { type: "number", min: "1" },
  );
  const visionTimeout = fieldInput(
    visionGrid,
    "超时",
    "vision_timeout",
    working.config.vision.timeout,
  );
  const visionMaxConcurrency = fieldInput(
    visionGrid,
    "最大并发",
    "vision_max_concurrency",
    working.config.vision.max_concurrency,
    { type: "number", min: "1" },
  );
  const visionCacheTTL = fieldInput(
    visionGrid,
    "缓存 TTL",
    "vision_cache_ttl",
    working.config.vision.cache_ttl,
  );
  const visionCacheMaxEntries = fieldInput(
    visionGrid,
    "缓存条目上限",
    "vision_cache_max_entries",
    working.config.vision.cache_max_entries,
    { type: "number", min: "0" },
  );
  const visionPrompt = fieldTextarea(
    visionGrid,
    "提示词",
    "vision_prompt",
    working.config.vision.prompt,
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
        { type: "number", min: "100", max: "599" },
      );
      const bodyContains = fieldInput(
        fields,
        "响应正文包含",
        `retry-${index}-body_contains`,
        rule.body_contains,
      );
      const maxRetries = fieldInput(
        fields,
        "最大重试次数",
        `retry-${index}-max_retries`,
        rule.max_retries,
        { type: "number", min: "0" },
      );
      const delay = fieldInput(
        fields,
        "延迟",
        `retry-${index}-delay`,
        rule.delay,
      );
      const jitter = fieldInput(
        fields,
        "抖动",
        `retry-${index}-jitter`,
        rule.jitter,
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
    syncRetryRules();
    working.display_name = displayName.value;
    working.slug = slug.value;
    working.make_default = isCurrentDefault || makeDefault.checked;
    working.enabled = working.make_default || enabled.checked;
    working.config.version = version.value;
    working.config.protocol = protocol.value;
    working.config.upstream = upstream.value;
    working.config.vision = {
      enabled:
        protocol.value === "anthropic" && visionEnabled.checked,
      model: visionModel.value,
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
    visionControls.hidden = !isAnthropic;
    visionNote.hidden = isAnthropic;
    visionEnabled.disabled = !isAnthropic;
    if (!isAnthropic) {
      visionEnabled.checked = false;
      working.config.vision.enabled = false;
    }
  }

  protocol.addEventListener("change", () => {
    working.config.protocol = protocol.value;
    applyProtocolState();
  });
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
    } finally {
      save.disabled = false;
    }
  });

  form.append(basic, vision, retries, generator, alert, footer);
  root.replaceChildren(form);
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
    { required: true },
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
  appendField(parent, labelText, input);
  return input;
}

function fieldTextarea(parent, labelText, name, value) {
  const textarea = element("textarea");
  textarea.name = name;
  textarea.value = String(value ?? "");
  appendField(parent, labelText, textarea);
  return textarea;
}

function fieldSelect(parent, labelText, name, value, choices) {
  const select = element("select");
  select.name = name;
  for (const [choiceValue, choiceLabel] of choices) {
    const option = textElement("option", choiceLabel);
    option.value = choiceValue;
    if (String(choiceValue) === String(value)) {
      option.selected = true;
    }
    select.append(option);
  }
  select.value = String(value ?? "");
  appendField(parent, labelText, select);
  return select;
}

function checkboxField(parent, labelText, name) {
  const wrapper = element("div", "form-field checkbox-field");
  const label = textElement("label", labelText);
  const input = element("input");
  input.name = name;
  input.type = "checkbox";
  label.append(input);
  wrapper.append(label);
  parent.append(wrapper);
  return input;
}

function appendField(parent, labelText, control) {
  const wrapper = element("div", "form-field");
  const label = textElement("label", labelText);
  const id = `profile-${control.name}`;
  control.id = id;
  label.setAttribute("for", id);
  wrapper.append(label, control);
  parent.append(wrapper);
}
