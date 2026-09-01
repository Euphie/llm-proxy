const filterNames = [
  "profile_id",
  "gateway_id",
  "issued_key_id",
  "protocol",
  "model",
  "kind",
  "from",
  "to",
];

export function normalizeFilters(filters = {}) {
  const normalized = {};
  for (const name of filterNames) {
    const value = String(filters[name] ?? "").trim();
    if (value === "") {
      continue;
    }
    normalized[name] =
      name === "from" || name === "to"
        ? new Date(value).toISOString()
        : value;
  }
  return normalized;
}

export function tokenTotal(row = {}) {
  return Number(row.input_tokens ?? 0) + Number(row.output_tokens ?? 0);
}

export function kindLabel(kind) {
  if (kind === "main") {
    return "主请求";
  }
  if (kind === "vision") {
    return "视觉预处理";
  }
  return String(kind ?? "");
}

export async function renderStatsPage(
  root,
  {
    profiles = [],
    gateways = [],
    loadGatewayKeys = async () => ({ keys: [] }),
    loadStats,
    onUnauthorized = () => {},
  },
) {
  profiles = Array.isArray(profiles) ? profiles : [];
  gateways = Array.isArray(gateways) ? gateways : [];
  const page = element("div", "stack stats-page");
  const form = element("form", "card stats-filters");
  const fields = element("div", "form-grid");
  const profile = fieldSelect(fields, "Profile", "profile_id", [
    ["", "全部 Profile"],
    ...profiles.map((item) => [
      String(item.id),
      `${item.display_name} (${item.slug})`,
    ]),
  ]);
  const gateway = fieldSelect(fields, "聚合网关", "gateway_id", [
    ["", "全部网关"],
    ...gateways.map((item) => [
      String(item.id),
      `${item.display_name} (${item.slug})`,
    ]),
  ]);
  const issuedKey = fieldSelect(fields, "调用方秘钥", "issued_key_id", [
    ["", "请先选择网关"],
  ]);
  issuedKey.disabled = true;
  const protocol = fieldSelect(fields, "协议", "protocol", [
    ["", "全部协议"],
    ["anthropic", "Anthropic"],
    ["openai", "OpenAI"],
  ]);
  const model = fieldInput(fields, "模型", "model");
  const kind = fieldSelect(fields, "请求类型", "kind", [
    ["", "全部请求"],
    ["main", kindLabel("main")],
    ["vision", kindLabel("vision")],
  ]);
  const from = fieldInput(fields, "开始时间", "from", "datetime-local");
  const to = fieldInput(fields, "结束时间", "to", "datetime-local");
  const submit = textElement("button", "查询");
  submit.type = "submit";
  submit.className = "button";
  form.append(fields, submit);

  const output = element("div", "stack stats-output");
  page.append(form, output);
  root.replaceChildren(page);

  let keyLoadGeneration = 0;
  gateway.addEventListener("change", async () => {
    const generation = ++keyLoadGeneration;
    issuedKey.disabled = true;
    if (gateway.value === "") {
      setSelectChoices(issuedKey, [["", "请先选择网关"]]);
      return;
    }
    setSelectChoices(issuedKey, [["", "正在加载秘钥…"]]);
    try {
      const response = await loadGatewayKeys(Number(gateway.value));
      if (generation !== keyLoadGeneration) {
        return;
      }
      setSelectChoices(issuedKey, [
        ["", "全部秘钥"],
        ...(response?.keys || []).map((item) => [
          String(item.id),
          issuedKeyLabel(item),
        ]),
      ]);
      issuedKey.disabled = false;
    } catch (error) {
      if (generation !== keyLoadGeneration) {
        return;
      }
      if (error?.status === 401) {
        onUnauthorized();
        return;
      }
      setSelectChoices(issuedKey, [["", "秘钥加载失败"]]);
      output.replaceChildren(alertMessage(error?.message || "秘钥加载失败，请重试。"));
    }
  });

  async function refresh(filters) {
    output.replaceChildren(statusMessage("正在加载统计数据…"));
    submit.disabled = true;
    try {
      const response = await loadStats(filters);
      renderStatsResponse(output, response);
    } catch (error) {
      if (error?.status === 401) {
        onUnauthorized();
        return;
      }
      output.replaceChildren(alertMessage(error?.message || "请求失败，请重试。"));
    } finally {
      submit.disabled = false;
    }
  }

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    try {
      await refresh(
        normalizeFilters({
          profile_id: profile.value,
          gateway_id: gateway.value,
          issued_key_id: issuedKey.value,
          protocol: protocol.value,
          model: model.value,
          kind: kind.value,
          from: from.value,
          to: to.value,
        }),
      );
    } catch (error) {
      output.replaceChildren(alertMessage(error?.message || "筛选条件无效。"));
    }
  });

  await refresh({});
}

function renderStatsResponse(root, response) {
  const summary = response?.summary || {};
  const cards = element("div", "summary-grid");
  for (const [label, value] of [
    ["请求数", Number(summary.requests ?? 0)],
    ["输入 Token", Number(summary.input_tokens ?? 0)],
    ["输出 Token", Number(summary.output_tokens ?? 0)],
    ["Token 总计", tokenTotal(summary)],
    ["缓存读取 Token", Number(summary.cache_read_tokens ?? 0)],
    ["缓存创建 Token", Number(summary.cache_creation_tokens ?? 0)],
  ]) {
    const card = element("section", "card summary-card");
    card.append(textElement("h2", label), textElement("p", `${label}：${value}`));
    cards.append(card);
  }

  const children = [cards];
  if (Number(summary.requests ?? 0) === 0) {
    children.push(statusMessage("暂无统计数据。"));
    root.replaceChildren(...children);
    return;
  }
  const charts = element("div", "stats-chart-grid");
  const chartSections = [
    usageTrendChart(response?.by_day || []),
    usageBreakdownChart("网关 Token 构成", response?.by_gateway || [], "网关"),
    usageBreakdownChart("秘钥 Token 构成", response?.by_issued_key || [], "秘钥"),
  ].filter(Boolean);
  if (chartSections.length > 0) {
    charts.append(...chartSections);
    children.push(charts);
  }
  const details = usageDetails(response);
  if (details) {
    children.push(details);
  }
  root.replaceChildren(...children);
}

function usageTrendChart(rows) {
  if (rows.length === 0) {
    return null;
  }
  const section = element("section", "card stats-chart-card");
  section.append(textElement("h2", "每日 Token 趋势"));

  const legend = element("div", "stats-chart-legend");
  legend.append(
    legendItem("stats-legend-input", "输入 Token"),
    legendItem("stats-legend-output", "输出 Token"),
  );

  const plot = element("div", "stats-trend-chart");
  plot.setAttribute("role", "img");
  plot.setAttribute("aria-label", "按日期展示输入和输出 Token 的堆叠柱状图");
  const ordered = [...rows].reverse();
  const maximum = Math.max(...ordered.map(tokenTotal), 1);
  for (const row of ordered) {
    const input = Number(row.input_tokens ?? 0);
    const output = Number(row.output_tokens ?? 0);
    const column = element("div", "stats-trend-column");
    column.setAttribute(
      "aria-label",
      `${row.key}，输入 ${input}，输出 ${output}，总计 ${tokenTotal(row)}`,
    );
    const value = textElement("span", tokenTotal(row));
    value.className = "stats-trend-value";
    const total = input + output;
    const totalPercent = chartPercent(total, maximum);
    const inputPercent = total > 0 ? totalPercent * (input / total) : 0;
    const outputPercent = total > 0 ? totalPercent * (output / total) : 0;
    const inputY = 100 - inputPercent;
    const outputY = inputY - outputPercent;
    const bar = svgElement("svg", "stats-trend-bar");
    bar.setAttribute("viewBox", "0 0 100 100");
    bar.setAttribute("preserveAspectRatio", "none");
    bar.setAttribute("aria-hidden", "true");
    const inputSegment = svgElement("rect", "stats-trend-segment stats-trend-input");
    inputSegment.setAttribute("x", "0");
    inputSegment.setAttribute("y", String(inputY));
    inputSegment.setAttribute("width", "100");
    inputSegment.setAttribute("height", String(inputPercent));
    const outputSegment = svgElement("rect", "stats-trend-segment stats-trend-output");
    outputSegment.setAttribute("x", "0");
    outputSegment.setAttribute("y", String(outputY));
    outputSegment.setAttribute("width", "100");
    outputSegment.setAttribute("height", String(outputPercent));
    bar.append(inputSegment, outputSegment);
    const label = textElement("span", String(row.key).slice(5));
    label.className = "stats-trend-label";
    column.append(value, bar, label);
    plot.append(column);
  }
  section.append(legend, plot);
  return section;
}

function usageBreakdownChart(title, rows, dimension) {
  if (rows.length === 0) {
    return null;
  }
  const section = element("section", "card stats-chart-card");
  section.append(textElement("h2", title));
  const list = element("div", "stats-breakdown-chart");
  list.setAttribute("role", "list");
  const maximum = Math.max(...rows.map(tokenTotal), 1);
  for (const row of rows) {
    const item = element("div", "stats-breakdown-item");
    item.setAttribute("role", "listitem");
    item.setAttribute(
      "aria-label",
      `${dimension} ${row.key}，Token 总计 ${tokenTotal(row)}`,
    );
    const heading = element("div", "stats-breakdown-heading");
    heading.append(
      textElement("span", row.key),
      textElement("strong", tokenTotal(row)),
    );
    const progress = element("progress", "stats-breakdown-progress");
    progress.max = maximum;
    progress.value = tokenTotal(row);
    progress.setAttribute("aria-label", `${dimension} ${row.key} Token 占比`);
    item.append(heading, progress);
    list.append(item);
  }
  section.append(list);
  return section;
}

function chartPercent(value, maximum) {
  const numeric = Number(value ?? 0);
  if (numeric <= 0) {
    return 0;
  }
  return Math.max(2, (numeric / maximum) * 100);
}

function legendItem(className, text) {
  const item = element("span", "stats-chart-legend-item");
  item.append(element("i", className), textElement("span", text));
  return item;
}

function issuedKeyLabel(key) {
  const name = String(key?.name || "未命名秘钥");
  const prefix = String(key?.prefix || "");
  const lastFour = String(key?.last_four || "");
  return `${name} (${prefix}...${lastFour})`;
}

function usageDetails(response) {
  const groups = [
    { label: "按日期", heading: "日期", rows: response?.by_day || [] },
    { label: "按模型", heading: "模型", rows: response?.by_model || [] },
    { label: "按网关", heading: "网关", rows: response?.by_gateway || [] },
    { label: "按秘钥", heading: "秘钥", rows: response?.by_issued_key || [] },
  ].filter((group) => group.rows.length > 0);
  if (groups.length === 0) {
    return null;
  }

  const section = element("section", "stats-details");
  section.append(textElement("h2", "分组明细"));
  const tabs = element("div", "stats-detail-tabs");
  tabs.setAttribute("role", "tablist");
  tabs.setAttribute("aria-label", "统计分组");
  const panel = element("div", "stats-detail-panel");
  panel.id = "stats-detail-panel";
  panel.setAttribute("role", "tabpanel");
  const buttons = groups.map((group) => {
    const button = textElement("button", group.label);
    button.type = "button";
    button.className = "stats-detail-tab";
    button.setAttribute("role", "tab");
    button.setAttribute("aria-controls", panel.id);
    button.addEventListener("click", () => selectGroup(group));
    tabs.append(button);
    return button;
  });

  function selectGroup(group) {
    for (let index = 0; index < groups.length; index += 1) {
      buttons[index].setAttribute(
        "aria-selected",
        groups[index] === group ? "true" : "false",
      );
    }
    panel.replaceChildren(usageTable(group.label, group.heading, group.rows));
  }

  section.append(tabs, panel);
  selectGroup(groups[0]);
  return section;
}

function usageTable(captionText, keyHeading, rows) {
  const wrapper = element("div", "table-wrap");
  const table = element("table");
  const caption = textElement("caption", captionText);
  const head = element("thead");
  const headerRow = element("tr");
  for (const heading of [
    keyHeading,
    "请求数",
    "输入 Token",
    "输出 Token",
    "Token 总计",
    "缓存读取 Token",
    "缓存创建 Token",
  ]) {
    headerRow.append(textElement("th", heading));
  }
  head.append(headerRow);

  const body = element("tbody");
  for (const row of rows) {
    const tableRow = element("tr");
    for (const value of [
      row.key,
      Number(row.requests ?? 0),
      Number(row.input_tokens ?? 0),
      Number(row.output_tokens ?? 0),
      tokenTotal(row),
      Number(row.cache_read_tokens ?? 0),
      Number(row.cache_creation_tokens ?? 0),
    ]) {
      tableRow.append(textElement("td", value));
    }
    body.append(tableRow);
  }
  table.append(caption, head, body);
  wrapper.append(table);
  return wrapper;
}

function fieldInput(parent, labelText, name, type = "text") {
  const input = element("input");
  input.name = name;
  input.type = type;
  appendField(parent, labelText, input);
  return input;
}

function fieldSelect(parent, labelText, name, choices) {
  const select = element("select");
  select.name = name;
  setSelectChoices(select, choices);
  appendField(parent, labelText, select);
  return select;
}

function setSelectChoices(select, choices) {
  select.replaceChildren();
  for (const [value, label] of choices) {
    const option = textElement("option", label);
    option.value = value;
    select.append(option);
  }
}

function appendField(parent, labelText, control) {
  const wrapper = element("div", "form-field");
  const label = textElement("label", labelText);
  control.id = `stats-${control.name}`;
  label.setAttribute("for", control.id);
  wrapper.append(label, control);
  parent.append(wrapper);
}

function statusMessage(text) {
  const status = textElement("p", text);
  status.className = "muted";
  status.setAttribute("role", "status");
  return status;
}

function alertMessage(text) {
  const alert = textElement("div", text);
  alert.className = "error-banner";
  alert.setAttribute("role", "alert");
  return alert;
}

function element(tagName, className = "") {
  const result = document.createElement(tagName);
  result.className = className;
  return result;
}

function svgElement(tagName, className = "") {
  const result = document.createElementNS("http://www.w3.org/2000/svg", tagName);
  if (className !== "") {
    result.setAttribute("class", className);
  }
  return result;
}

function textElement(tagName, text) {
  const result = element(tagName);
  result.textContent = String(text);
  return result;
}
