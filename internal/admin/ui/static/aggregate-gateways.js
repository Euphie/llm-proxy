export function providerAccountPayload(draft) {
  return {
    slug: String(draft.slug ?? "").trim(),
    display_name: String(draft.display_name ?? "").trim(),
    enabled: Boolean(draft.enabled),
    protocol: String(draft.protocol ?? "openai"),
    upstream: String(draft.upstream ?? "").trim(),
    auth_header: String(draft.auth_header ?? "Authorization").trim(),
    secret: String(draft.secret ?? "").trim(),
    models: (draft.models || []).map((model) => ({
      model_id: String(model.model_id ?? "").trim(),
      display_name: String(model.display_name ?? "").trim(),
      enabled: Boolean(model.enabled),
    })),
  };
}

export function aggregateGatewayPayload(draft) {
  return {
    slug: String(draft.slug ?? "").trim(),
    display_name: String(draft.display_name ?? "").trim(),
    enabled: Boolean(draft.enabled),
    protocol: String(draft.protocol ?? "openai"),
    routes: (draft.routes || []).map((route) => ({
      public_model: String(route.public_model ?? "").trim(),
      provider_account_id: Number(route.provider_account_id),
      provider_model: String(route.provider_model ?? "").trim(),
      enabled: Boolean(route.enabled),
    })),
  };
}

const aggregateSlugPattern = /^[a-z0-9][a-z0-9-]{0,62}$/;
const aggregateSlugHTMLPattern = "[a-z0-9][a-z0-9-]{0,62}";

function aggregateSlugValidationError(value) {
  const slug = String(value ?? "").trim();
  if (slug === "") {
    return "请填写 Slug。";
  }
  if (!aggregateSlugPattern.test(slug)) {
    return "Slug 只能使用小写字母、数字和连字符（-），长度为 1-63 位。";
  }
  if (slug === "v1" || slug === "gateways") {
    return "Slug 不能使用保留名称 v1 或 gateways。";
  }
  return "";
}

export function renderAggregateGatewaysPage(
  root,
  data = {},
  actions = {},
  options = {},
) {
  const providers = data.providers || [];
  const gateways = data.gateways || [];
  const keys = data.keys || [];
  const view = aggregateView(options.view || data.view);

  const page = element("div", "stack aggregate-page");
  const heading = element("div", "page-heading cluster");
  const description = textElement(
    "p",
    aggregateViewDescription(view),
  );
  description.className = "muted";
  heading.append(description);

  const alert = element("div", "error-banner");
  alert.setAttribute("role", "alert");
  alert.hidden = true;

  const activeSection = {
    providers: () => providerSection(providers, actions, alert),
    "gateway-list": () => gatewayListSection(gateways, providers),
    gateways: () => gatewaySection(
      providers,
      gateways,
      actions,
      alert,
      Number(options.gatewayID) || 0,
    ),
    keys: () => keySection(gateways, keys, actions, alert),
  }[view];

  page.append(heading, alert, activeSection());
  if (options.notice) {
    page.append(successToast(options.notice));
  }
  root.replaceChildren(page);
}

function aggregateView(view) {
  return ["providers", "gateway-list", "gateways", "keys"].includes(view)
    ? view
    : "gateway-list";
}

function aggregateViewDescription(view) {
  return {
    providers: "维护上游供应商地址、供应商 Key 和该供应商支持的模型。",
    "gateway-list": "查看和创建聚合网关，管理对外入口、状态和模型路由。",
    gateways: "配置对外网关入口，并从供应商支持模型中勾选要开放的模型。",
    keys: "配置分发给业务或 Agent 访问聚合网关的调用方 Key，与供应商 Key 分开管理。",
  }[view];
}

function providerSection(providers, actions, pageAlert) {
  const section = element("section", "card stack aggregate-section");
  const createProvider = actionButton("新增供应商", "button");
  section.append(sectionHeader("供应商管理", [createProvider]));

  let providerID = 0;
  let nextModelRowID = 1;
  let modelControls = [];
  const form = element("form", "aggregate-inline-form");
  form.setAttribute("data-aggregate-form", "provider");
  const fields = element("div", "form-grid aggregate-form-grid");
  const slug = fieldInput(fields, "Slug", "provider_slug", "", {
    required: true,
    pattern: aggregateSlugHTMLPattern,
    maxLength: 63,
    description: "仅支持小写字母、数字和连字符（-），长度 1-63 位；同一协议内必须唯一，不同协议可以相同。",
  });
  bindSlugValidation(slug, pageAlert);
  const displayName = fieldInput(fields, "名称", "provider_display_name", "", {
    required: true,
  });
  const protocol = fieldSelect(fields, "协议", "provider_protocol", "openai", [
    ["openai", "OpenAI"],
    ["anthropic", "Anthropic"],
  ]);
  const upstream = fieldInput(fields, "Upstream", "provider_upstream", "", {
    required: true,
    type: "url",
    description: "供应商兼容 API 的基础地址，不包含具体接口路径。",
  });
  const authHeader = fieldInput(
    fields,
    "认证 Header",
    "provider_auth_header",
    "Authorization",
    {
      required: true,
      description: "例如 Authorization、x-api-key 或 api-key。",
    },
  );
  const secret = fieldInput(fields, "供应商 Key / Secret", "provider_secret", "", {
    required: true,
    type: "password",
    description: "填写供应商 Key；Authorization 会自动添加 Bearer 前缀。只在保存时发送，列表不会回显。",
  });
  const enabled = checkboxField(fields, "启用上游供应商", "provider_enabled", true);

  const modelEditor = element("div", "aggregate-provider-model-editor stack");
  const modelHeader = element("div", "cluster aggregate-route-editor-header");
  modelHeader.append(textElement("h3", "支持模型"));
  const addModel = actionButton("添加模型", "button-secondary");
  modelHeader.append(addModel);
  const modelRows = element("div", "aggregate-provider-model-rows");
  modelEditor.append(modelHeader, modelRows);
  const defaultModel = (model = {}) => ({
    model_id: model.model_id ?? "",
    display_name: model.display_name ?? "",
    enabled: model.enabled !== false,
  });
  const syncModelRemoveButtons = () => {
    const canRemove = modelControls.length > 1;
    for (const controls of modelControls) {
      controls.remove.disabled = !canRemove;
    }
  };
  const renderModelRows = () => {
    modelRows.replaceChildren(...modelControls.map((controls) => controls.row));
    syncModelRemoveButtons();
  };
  const addModelRow = (model = {}) => {
    const rowID = nextModelRowID;
    nextModelRowID += 1;
    const values = defaultModel(model);
    const row = element("div", "aggregate-provider-model-row");
    const rowFields = element("div", "form-grid aggregate-form-grid aggregate-provider-model-grid");
    const modelID = fieldInput(rowFields, "模型 ID", `provider_model_id_${rowID}`, values.model_id, {
      description: "供应商真实模型 ID，例如 gpt-4o 或 claude-3-5-sonnet-latest。",
    });
    const display = fieldInput(rowFields, "显示名", `provider_model_display_name_${rowID}`, values.display_name, {
      description: "可选，只用于后台展示。",
    });
    const modelEnabled = checkboxField(rowFields, "启用模型", `provider_model_enabled_${rowID}`, values.enabled);
    const remove = actionButton("删除", "button-secondary aggregate-route-remove");
    remove.addEventListener("click", () => {
      if (modelControls.length <= 1) {
        return;
      }
      modelControls = modelControls.filter((controls) => controls.rowID !== rowID);
      renderModelRows();
    });
    const rowActions = element("div", "cluster aggregate-route-row-actions");
    rowActions.append(remove);
    row.append(rowFields, rowActions);
    modelControls.push({
      rowID,
      row,
      modelID,
      display,
      modelEnabled,
      remove,
    });
    renderModelRows();
  };
  const setModels = (models = []) => {
    modelControls = [];
    modelRows.replaceChildren();
    const nextModels = models.length > 0 ? models : [defaultModel()];
    for (const model of nextModels) {
      addModelRow(model);
    }
  };
  const modelDrafts = () =>
    modelControls
      .map((controls) => ({
        model_id: controls.modelID.value,
        display_name: controls.display.value,
        enabled: controls.modelEnabled.checked,
      }))
      .filter((model) => String(model.model_id ?? "").trim() !== "");

  setModels([]);
  const submit = actionButton("保存供应商", "button");
  submit.type = "submit";
  const cancel = actionButton("取消", "button-secondary");
  const message = element("div", "success-banner");
  message.hidden = true;
  const buttons = element("div", "cluster");
  buttons.append(submit, cancel);
  form.append(fields, modelEditor, message, buttons);
  form.hidden = true;
  const resetForm = () => {
    providerID = 0;
    slug.value = "";
    displayName.value = "";
    protocol.value = "openai";
    upstream.value = "";
    authHeader.value = "Authorization";
    secret.value = "";
    secret.required = true;
    enabled.checked = true;
    setModels([]);
    submit.textContent = "保存供应商";
  };
  const showForm = () => {
    pageAlert.hidden = true;
    message.hidden = true;
    form.hidden = false;
  };
  const hideForm = () => {
    resetForm();
    message.hidden = true;
    form.hidden = true;
  };
  if (providers.length === 0) {
    const empty = textElement("p", "还没有供应商。");
    empty.className = "muted";
    section.append(empty);
  } else {
    section.append(providerTable(providers, (provider) => {
      providerID = Number(provider.id);
      slug.value = provider.slug;
      displayName.value = provider.display_name;
      protocol.value = provider.protocol;
      upstream.value = provider.upstream;
      authHeader.value = provider.auth_header;
      secret.value = "";
      secret.required = false;
      enabled.checked = Boolean(provider.enabled);
      setModels(provider.models || []);
      submit.textContent = "更新供应商";
      showForm();
    }));
  }
  createProvider.addEventListener("click", () => {
    resetForm();
    showForm();
  });
  cancel.addEventListener("click", hideForm);
  addModel.addEventListener("click", () => addModelRow(defaultModel()));
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const slugError = aggregateSlugValidationError(slug.value);
    if (slugError !== "") {
      showError(pageAlert, new Error(slugError));
      return;
    }
    submit.disabled = true;
    try {
      await runAction(pageAlert, message, "供应商已保存。", () =>
        actions.saveProvider?.(providerAccountPayload({
          slug: slug.value,
          display_name: displayName.value,
          enabled: enabled.checked,
          protocol: protocol.value,
          upstream: upstream.value,
          auth_header: authHeader.value,
          secret: secret.value,
          models: modelDrafts(),
        }), providerID),
      );
    } finally {
      submit.disabled = false;
    }
  });
  section.append(form);
  return section;
}

function gatewayListSection(gateways, providers) {
  const section = element("section", "card stack aggregate-section");
  section.append(
    sectionHeader("全部网关", [
      actionLink("新建网关", "/_admin/aggregate-gateways/gateways", "button"),
    ]),
  );

  if (gateways.length === 0) {
    const empty = textElement("p", "还没有聚合网关。");
    empty.className = "muted";
    section.append(empty);
    return section;
  }

  section.append(gatewayList(gateways, providers));
  return section;
}

function gatewaySection(providers, gateways, actions, pageAlert, requestedGatewayID) {
  const section = element("section", "card stack aggregate-section");
  section.append(sectionHeader("网关管理", [
    actionLink("返回列表", "/_admin/aggregate-gateways", "button-secondary"),
  ]));

  let gatewayID = 0;
  let nextRouteRowID = 1;
  let routeControls = [];
  const form = element("form", "aggregate-inline-form");
  form.setAttribute("data-aggregate-form", "gateway");
  const fields = element("div", "form-grid aggregate-form-grid");
  const slug = fieldInput(fields, "Slug", "gateway_slug", "", {
    required: true,
    pattern: aggregateSlugHTMLPattern,
    maxLength: 63,
    description: "仅支持小写字母、数字和连字符（-），长度 1-63 位；对外地址为 /gateways/{slug}/v1。",
  });
  bindSlugValidation(slug, pageAlert);
  const displayName = fieldInput(fields, "名称", "gateway_display_name", "", {
    required: true,
  });
  const protocol = fieldSelect(fields, "协议", "gateway_protocol", "openai", [
    ["openai", "OpenAI"],
    ["anthropic", "Anthropic"],
  ]);
  const enabled = checkboxField(fields, "启用网关", "gateway_enabled", true);

  const routeEditor = element("div", "aggregate-route-editor stack");
  const routeHeader = element("div", "cluster aggregate-route-editor-header");
  routeHeader.append(textElement("h3", "选择模型"));
  const routeRows = element("div", "aggregate-route-editor-rows");
  routeEditor.append(routeHeader, routeRows);

  const providerModelsForProtocol = () =>
    providers
      .filter((provider) => provider.enabled !== false && provider.protocol === protocol.value)
      .flatMap((provider) =>
        (provider.models || [])
          .filter((model) => model.enabled !== false)
          .map((model) => ({ provider, model }))
      );
  const routeKey = (providerAccountID, providerModel) =>
    `${Number(providerAccountID)}:${providerModel}`;
  const routeMap = (routes = []) => {
    const matches = new Map();
    for (const route of routes) {
      matches.set(routeKey(route.provider_account_id, route.provider_model), route);
    }
    return matches;
  };
  const addRouteChoice = (provider, model, route) => {
    const rowID = nextRouteRowID;
    nextRouteRowID += 1;
    const selected = element("input");
    selected.name = `route_selected_${rowID}`;
    selected.type = "checkbox";
    selected.checked = Boolean(route);
    const row = element("div", "aggregate-route-choice");
    row.setAttribute("data-aggregate-route-model", `${provider.id}:${model.model_id}`);
    const label = element("label", "cluster aggregate-route-choice-label");
    const title = element("span", "aggregate-route-choice-title");
    title.append(
      textElement("strong", model.display_name || model.model_id),
      textElement("span", `${provider.display_name} · ${model.model_id}`),
    );
    label.append(selected, title);
    const details = element("div", "form-grid aggregate-form-grid aggregate-route-choice-details");
    const publicModel = fieldInput(details, "对外模型", `route_public_model_${rowID}`, route?.public_model ?? model.model_id, {
      required: true,
      description: "调用方请求中的 model；同一网关内必须唯一，重名请改成其它名称。",
    });
    const routeEnabled = checkboxField(details, "启用路由", `route_enabled_${rowID}`, route?.enabled !== false);
    const syncDetails = () => {
      details.hidden = !selected.checked;
      publicModel.required = selected.checked;
    };
    selected.addEventListener("change", syncDetails);
    syncDetails();
    row.append(label, details);
    routeControls.push({
      rowID,
      row,
      selected,
      publicModel,
      providerAccountID: String(provider.id),
      providerModel: model.model_id,
      routeEnabled,
    });
  };
  const setRoutes = (routes = []) => {
    routeControls = [];
    routeRows.replaceChildren();
    const choices = providerModelsForProtocol();
    if (choices.length === 0) {
      const empty = textElement("p", "请先在供应商管理中为当前协议添加启用的模型。");
      empty.className = "muted";
      routeRows.append(empty);
      return;
    }
    const matches = routeMap(routes);
    for (const choice of choices) {
      addRouteChoice(
        choice.provider,
        choice.model,
        matches.get(routeKey(choice.provider.id, choice.model.model_id)),
      );
    }
    routeRows.replaceChildren(...routeControls.map((controls) => controls.row));
  };
  const routeDrafts = () =>
    routeControls.filter((controls) => controls.selected.checked).map((controls) => ({
      public_model: controls.publicModel.value,
      provider_account_id: controls.providerAccountID,
      provider_model: controls.providerModel,
      enabled: controls.routeEnabled.checked,
    }));

  setRoutes([]);
  const submit = actionButton("保存网关", "button");
  submit.type = "submit";
  submit.disabled = providerModelsForProtocol().length === 0;
  const message = element("div", "success-banner");
  message.hidden = true;
  const buttons = element("div", "cluster");
  buttons.append(submit);
  form.append(fields, routeEditor, message, buttons);
  const resetForm = () => {
    gatewayID = 0;
    slug.value = "";
    displayName.value = "";
    protocol.value = "openai";
    enabled.checked = true;
    setRoutes([]);
    submit.disabled = providerModelsForProtocol().length === 0;
    submit.textContent = "保存网关";
  };
  const loadGateway = (gateway) => {
    gatewayID = Number(gateway.id);
    slug.value = gateway.slug;
    displayName.value = gateway.display_name;
    protocol.value = gateway.protocol;
    enabled.checked = Boolean(gateway.enabled);
    setRoutes(gateway.routes || []);
    submit.disabled = providerModelsForProtocol().length === 0;
    submit.textContent = "更新网关";
  };
  const editingGateway = gateways.find((gateway) =>
    Number(gateway.id) === requestedGatewayID
  );
  if (editingGateway) {
    loadGateway(editingGateway);
  } else if (requestedGatewayID > 0) {
    showError(pageAlert, new Error("网关不存在或已删除。"));
    submit.disabled = true;
  } else {
    resetForm();
  }
  protocol.addEventListener("change", () => {
    setRoutes(routeDrafts());
    submit.disabled = providerModelsForProtocol().length === 0;
  });
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (submit.disabled) {
      return;
    }
    const slugError = aggregateSlugValidationError(slug.value);
    if (slugError !== "") {
      showError(pageAlert, new Error(slugError));
      return;
    }
    const routes = routeDrafts();
    const publicModels = routes.map((route) => String(route.public_model).trim());
    const duplicate = publicModels.find((model, index) => publicModels.indexOf(model) !== index);
    if (duplicate) {
      showError(pageAlert, new Error(`对外模型 ${duplicate} 已重复，请改成其它名称。`));
      return;
    }
    submit.disabled = true;
    try {
      await runAction(pageAlert, message, "网关已保存。", () =>
        actions.saveGateway?.(aggregateGatewayPayload({
          slug: slug.value,
          display_name: displayName.value,
          enabled: enabled.checked,
          protocol: protocol.value,
          routes,
        }), gatewayID),
      );
    } finally {
      submit.disabled = providerModelsForProtocol().length === 0;
    }
  });
  section.append(form);
  return section;
}

function keySection(gateways, keys, actions, pageAlert) {
  const section = element("section", "card stack aggregate-section");
  const createKey = actionButton("添加秘钥", "button");
  createKey.disabled = gateways.length === 0;
  section.append(sectionHeader("秘钥管理", [createKey]));

  let currentKeys = [...keys];
  const selectorFields = element("div", "form-grid aggregate-form-grid");
  const gatewayID = fieldSelect(
    selectorFields,
    "网关",
    "key_gateway_id",
    gateways[0]?.id ?? "",
    gatewayChoices(gateways),
  );
  const keyList = element("div", "aggregate-key-list");

  const oneTimeKey = element("div", "warning-banner aggregate-issued-key");
  oneTimeKey.hidden = true;
  const form = element("form", "aggregate-inline-form");
  form.setAttribute("data-aggregate-form", "key");
  const fields = element("div", "form-grid aggregate-form-grid");
  const name = fieldInput(fields, "名称", "key_name", "", {
    required: true,
  });
  const expiresAt = fieldInput(fields, "过期时间", "key_expires_at", "", {
    type: "datetime-local",
    description: "留空表示不过期。",
  });
  const enabled = checkboxField(fields, "启用调用方 Key", "key_enabled", true);
  const submit = actionButton("创建调用方 Key", "button");
  submit.type = "submit";
  submit.disabled = gateways.length === 0;
  const message = element("div", "success-banner");
  message.hidden = true;
  const cancel = actionButton("取消", "button-secondary");
  const buttons = element("div", "cluster");
  buttons.append(submit, cancel);
  form.append(fields, message, buttons);
  form.hidden = true;

  const renderSelectedKeys = () => {
    keyList.replaceChildren();
    if (gateways.length === 0) {
      const empty = textElement("p", "请先在网关管理中创建聚合网关。");
      empty.className = "muted";
      keyList.append(empty);
      return;
    }
    const selectedGatewayID = Number(gatewayID.value);
    const gateway = gateways.find((item) =>
      Number(item.id) === selectedGatewayID
    );
    const gatewayKeys = currentKeys.filter((key) =>
      Number(key.gateway_id) === selectedGatewayID
    );
    const summary = textElement(
      "p",
      `${gateway?.display_name || "当前网关"} 已配置 ${gatewayKeys.length} 个调用方 Key。`,
    );
    summary.className = "muted";
    keyList.append(summary);
    if (gatewayKeys.length === 0) {
      const empty = textElement("p", "该网关还没有调用方 Key。");
      empty.className = "muted";
      keyList.append(empty);
      return;
    }
    keyList.append(keyTable(gatewayKeys));
  };

  gatewayID.addEventListener("change", renderSelectedKeys);
  renderSelectedKeys();

  const resetKeyForm = () => {
    name.value = "";
    expiresAt.value = "";
    enabled.checked = true;
    message.hidden = true;
  };
  createKey.addEventListener("click", () => {
    pageAlert.hidden = true;
    oneTimeKey.hidden = true;
    resetKeyForm();
    form.hidden = false;
  });
  cancel.addEventListener("click", () => {
    resetKeyForm();
    form.hidden = true;
  });
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (submit.disabled) {
      return;
    }
    submit.disabled = true;
    try {
      pageAlert.hidden = true;
      message.hidden = true;
      const issued = await actions.createKey?.(Number(gatewayID.value), {
        name: name.value.trim(),
        enabled: enabled.checked,
        expires_at: localDateTimeToRFC3339(expiresAt.value),
      });
      if (issued?.key) {
        oneTimeKey.textContent = `新调用方 Key 仅显示一次：${issued.key}`;
        oneTimeKey.hidden = false;
      }
      if (issued) {
        currentKeys = [...currentKeys, {
          id: issued.id,
          gateway_id: issued.gateway_id ?? Number(gatewayID.value),
          name: issued.name || name.value.trim(),
          prefix: issued.prefix,
          last_four: issued.last_four,
          enabled: issued.enabled ?? enabled.checked,
        }];
        renderSelectedKeys();
      }
      resetKeyForm();
      form.hidden = true;
    } catch (error) {
      showError(pageAlert, error);
    } finally {
      submit.disabled = gateways.length === 0;
    }
  });
  section.append(selectorFields, keyList, oneTimeKey, form);
  return section;
}

function providerTable(providers, onEdit) {
  const wrap = element("div", "table-wrap");
  const table = element("table");
  table.append(tableHead(["名称", "协议", "Upstream", "支持模型", "认证", "状态", "操作"]));
  const body = element("tbody");
  for (const provider of providers) {
    const row = element("tr");
    const edit = actionButton("编辑", "button-secondary");
    edit.addEventListener("click", () => onEdit?.(provider));
    row.append(
      tableCell(provider.display_name),
      tableCell(provider.protocol),
      tableCell(provider.upstream),
      tableCell(providerModelSummary(provider.models || [])),
      tableCell(`${provider.auth_header}${provider.has_secret ? " · 已保存" : ""}`),
      tableCell(provider.enabled ? "启用" : "停用"),
      tableCell(edit),
    );
    body.append(row);
  }
  table.append(body);
  wrap.append(table);
  return wrap;
}

function providerModelSummary(models) {
  if (models.length === 0) {
    return "未配置";
  }
  return models.map((model) =>
    `${model.display_name || model.model_id}${model.enabled === false ? "（停用）" : ""}`
  ).join("、");
}

function gatewayList(gateways, providers) {
  const group = element("div", "profile-settings-group aggregate-gateway-list");
  const names = new Map(providers.map((provider) => [Number(provider.id), provider.display_name]));
  for (const gateway of gateways) {
    const row = element("article", "profile-settings-row");
    const header = element("div", "card-header profile-row-header");
    const identity = element("div", "profile-identity");
    const icon = element("span", "profile-icon profile-icon-green");
    icon.textContent = "G";
    icon.setAttribute("aria-hidden", "true");
    const copy = element("div", "profile-identity-copy");
    copy.append(
      textElement("h3", gateway.display_name),
      textElement("p", gateway.slug),
    );
    copy.children[1].className = "profile-slug";
    identity.append(icon, copy);
    const badges = element("div", "cluster profile-badges");
    badges.append(badge(gateway.protocol, "badge-success"));
    if (!gateway.enabled) {
      badges.append(badge("已停用", "badge-danger"));
    }
    badges.append(actionLink(
      "编辑",
      `/_admin/aggregate-gateways/gateways?id=${encodeURIComponent(String(gateway.id))}`,
      "button-secondary",
    ));
    header.append(identity, badges);
    const summary = element("dl", "profile-summary");
    appendDefinition(summary, "对外地址", `/gateways/${gateway.slug}/v1`);
    appendDefinition(summary, "路由数量", String((gateway.routes || []).length));
    row.append(header, summary);
    const routes = element("div", "aggregate-route-list");
    for (const route of gateway.routes || []) {
      const item = element("div", "aggregate-route-item");
      item.append(
        textElement("strong", route.public_model),
        textElement(
          "span",
          `${names.get(Number(route.provider_account_id)) || "未知供应商"} · ${route.provider_model}`,
        ),
      );
      routes.append(item);
    }
    row.append(routes);
    group.append(row);
  }
  return group;
}

function keyTable(keys) {
  const wrap = element("div", "table-wrap");
  const table = element("table");
    table.append(tableHead(["名称", "前缀", "尾号", "状态"]));
  const body = element("tbody");
  for (const key of keys) {
    const row = element("tr");
    row.append(
      tableCell(key.name),
      tableCell(key.prefix),
      tableCell(key.last_four),
      tableCell(key.enabled ? "启用" : "停用"),
    );
    body.append(row);
  }
  table.append(body);
  wrap.append(table);
  return wrap;
}

function sectionHeader(title, actions = []) {
  const header = element("div", "cluster section-heading");
  header.append(textElement("h2", title));
  for (const action of actions) {
    header.append(action);
  }
  return header;
}

function gatewayChoices(gateways) {
  if (gateways.length === 0) {
    return [["", "请先创建网关"]];
  }
  return gateways.map((gateway) => [String(gateway.id), gateway.display_name]);
}

function runAction(pageAlert, message, success, action) {
  pageAlert.hidden = true;
  message.hidden = true;
  return Promise.resolve()
    .then(action)
    .then(() => {
      message.textContent = success;
      message.hidden = false;
    })
    .catch((error) => {
      showError(pageAlert, error);
      throw error;
    });
}

function showError(alert, error) {
  alert.textContent = error?.message || "请求失败，请重试。";
  alert.hidden = false;
}

function successToast(message) {
  const toast = element("div", "success-toast");
  toast.setAttribute("role", "status");
  toast.setAttribute("aria-live", "polite");
  toast.textContent = message;
  const close = textElement("button", "×");
  close.type = "button";
  close.className = "success-toast-close";
  close.setAttribute("aria-label", "关闭提示");
  close.setAttribute("title", "关闭提示");
  close.addEventListener("click", () => {
    toast.hidden = true;
  });
  toast.append(close);
  return toast;
}

function localDateTimeToRFC3339(value) {
  const trimmed = String(value ?? "").trim();
  if (trimmed === "") {
    return "";
  }
  const date = new Date(trimmed);
  if (Number.isNaN(date.getTime())) {
    return trimmed;
  }
  return date.toISOString();
}

function tableHead(columns) {
  const head = element("thead");
  const row = element("tr");
  for (const column of columns) {
    row.append(textElement("th", column));
  }
  head.append(row);
  return head;
}

function tableCell(value) {
  const cell = element("td");
  if (value && typeof value === "object" && "tagName" in value) {
    cell.append(value);
    return cell;
  }
  cell.textContent = String(value ?? "");
  return cell;
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

function actionLink(text, href, variant) {
  const link = textElement("a", text);
  link.className = `button ${variant}`;
  link.setAttribute("href", href);
  return link;
}

function checkboxField(parent, labelText, name, checked) {
  const wrapper = element("div", "form-field checkbox-field");
  const label = textElement("label", labelText);
  const input = element("input");
  input.name = name;
  input.type = "checkbox";
  input.checked = Boolean(checked);
  label.append(input);
  wrapper.append(label);
  parent.append(wrapper);
  return input;
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
  if (options.pattern !== undefined) {
    input.setAttribute("pattern", options.pattern);
  }
  if (options.maxLength !== undefined) {
    input.setAttribute("maxlength", options.maxLength);
  }
  appendField(parent, labelText, input, options.description);
  return input;
}

function bindSlugValidation(input, pageAlert) {
  input.addEventListener("input", () => {
    input.setCustomValidity?.("");
  });
  input.addEventListener("invalid", () => {
    const error = aggregateSlugValidationError(input.value);
    input.setCustomValidity?.(error);
    if (error !== "") {
      showError(pageAlert, new Error(error));
    }
  });
}

function fieldSelect(parent, labelText, name, value, choices) {
  const select = element("select");
  select.name = name;
  const options = [];
  for (const [choiceValue, choiceLabel] of choices) {
    const option = textElement("option", choiceLabel);
    option.value = String(choiceValue);
    options.push(option);
  }
  select.replaceChildren(...options);
  select.value = String(value ?? "");
  appendField(parent, labelText, select);
  return select;
}

function appendField(parent, labelText, control, description = "") {
  const wrapper = element("div", "form-field");
  const label = textElement("label", labelText);
  const id = `aggregate-${control.name}`;
  control.id = id;
  label.setAttribute("for", id);
  wrapper.append(label, control);
  if (description) {
    const help = textElement("small", description);
    help.className = "field-help";
    help.id = `${id}-description`;
    control.setAttribute("aria-describedby", help.id);
    wrapper.append(help);
  }
  parent.append(wrapper);
}

function appendDefinition(list, term, description) {
  list.append(textElement("dt", term), textElement("dd", description));
}

function element(tagName, className = "") {
  const result = document.createElement(tagName);
  result.className = className;
  return result;
}

function textElement(tagName, text) {
  const result = element(tagName);
  result.textContent = String(text ?? "");
  return result;
}
