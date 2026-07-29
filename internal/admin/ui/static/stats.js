const filterNames = [
  "profile_id",
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
  { profiles = [], loadStats, onUnauthorized = () => {} },
) {
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
  const submit = textElement("button", "应用筛选");
  submit.type = "submit";
  submit.className = "button";
  form.append(fields, submit);

  const output = element("div", "stack stats-output");
  page.append(form, output);
  root.replaceChildren(page);

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
  }
  children.push(
    usageTable("按日期", "日期", response?.by_day || []),
    usageTable("按模型", "模型", response?.by_model || []),
  );
  root.replaceChildren(...children);
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
  for (const [value, label] of choices) {
    const option = textElement("option", label);
    option.value = value;
    select.append(option);
  }
  appendField(parent, labelText, select);
  return select;
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

function textElement(tagName, text) {
  const result = element(tagName);
  result.textContent = String(text);
  return result;
}
