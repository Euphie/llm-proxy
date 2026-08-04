import { formatMicroUSD } from "./money.js";
import { renderModelPerformancePage } from "./stats-models.js";
import {
	localDateTimeValue,
	positivePageParameter,
	replaceStatsURL,
	statsURLParameters,
} from "./stats-url.js";

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

export async function renderStatsPage(root, options = {}) {
	const section = options.section || "usage";
	const page = element("div", "stack stats-page");
	page.append(statsNavigation(section));
	const content = element("div", "stack stats-section");
	page.append(content);
	root.replaceChildren(page);
	if (section === "routing") {
		await renderRoutingStatsContent(content, options);
		return;
	}
	if (section === "models") {
		await renderModelPerformancePage(content, options);
		return;
	}
	await renderUsageStatsContent(content, options);
}

function statsNavigation(current) {
	const nav = element("nav", "secondary-nav stats-nav");
	nav.setAttribute("aria-label", "统计分类");
	for (const [section, label, href] of [
		["usage", "用量统计", "/_admin/stats"],
		["routing", "路由轨迹", "/_admin/stats/routing"],
		["models", "模型表现", "/_admin/stats/models"],
	]) {
		const link = textElement("a", label);
		link.setAttribute("href", href);
		if (section === current) {
			link.setAttribute("aria-current", "page");
		}
		nav.append(link);
	}
	return nav;
}

async function renderUsageStatsContent(
  root,
  {
    profiles = [],
    loadStats,
    onUnauthorized = () => {},
  },
) {
	const initial = statsURLParameters();
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
	profile.value = initial.get("profile_id") || "";
	protocol.value = initial.get("protocol") || "";
	model.value = initial.get("model") || "";
	kind.value = initial.get("kind") || "";
	from.value = localDateTimeValue(initial.get("from"));
	to.value = localDateTimeValue(initial.get("to"));

  async function refresh(filters) {
    output.replaceChildren(statusMessage("正在加载统计数据…"));
    submit.disabled = true;
    try {
	  const response = await loadStats(filters);
	  replaceStatsURL("/_admin/stats", filters);
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

	await refresh(normalizeFilters({
		profile_id: profile.value, protocol: protocol.value, model: model.value,
		kind: kind.value, from: from.value, to: to.value,
	}));
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

async function renderRoutingStatsContent(
	root,
	{
		profiles = [],
		loadRoutingTraces = async () => ({ items: [], page: 1, page_size: 25, total: 0, total_pages: 0, category_counts: {} }),
		loadRoutingTrace = async () => null,
		onUnauthorized = () => {},
	},
) {
	const initial = statsURLParameters();
	const allowedCategories = ["all", "normal", "high_risk", "fallback", "changed", "failed", "cost_anomaly"];
	const initialCategory = initial.get("category");
	const state = {
		page: positivePageParameter(initial, "page", 1),
		page_size: positivePageParameter(initial, "page_size", 25, [25, 50, 100]),
		category: allowedCategories.includes(initialCategory) ? initialCategory : "all",
	};
	const form = element("form", "card stats-filters routing-filters");
	const fields = element("div", "form-grid");
	const profile = fieldSelect(fields, "Profile", "profile_id", [
		["", "全部 Profile"],
		...profiles.map((item) => [String(item.id), `${item.display_name} (${item.slug})`]),
	]);
	const taskType = fieldInput(fields, "任务类型", "task_type");
	const difficulty = fieldSelect(fields, "难度", "difficulty", [
		["", "全部难度"], ["easy", "简单"], ["medium", "中等"], ["hard", "困难"], ["unknown", "未知"],
	]);
	const risk = fieldSelect(fields, "风险", "risk", [
		["", "全部风险"], ["normal", "普通"], ["high", "高风险"], ["unknown", "未知"],
	]);
	const model = fieldInput(fields, "模型", "model");
	const source = fieldSelect(fields, "判断来源", "source", [
		["", "全部来源"], ["rule", "本地规则"], ["analyzer", "任务分析"],
		["session", "Session 复用"], ["fallback", "安全回退"],
	]);
	const vision = fieldSelect(fields, "视觉", "vision", [
		["", "全部"], ["none", "无"], ["used", "已使用视觉"],
		["native", "原生视觉"], ["composite", "视觉增强"],
	]);
	const from = fieldInput(fields, "开始时间", "from", "datetime-local");
	const to = fieldInput(fields, "结束时间", "to", "datetime-local");
	const pageSize = fieldSelect(fields, "每页", "page_size", [["25", "25"], ["50", "50"], ["100", "100"]]);
	const submit = textElement("button", "应用筛选");
	submit.type = "submit";
	submit.className = "button";
	form.append(fields, submit);
	const categories = element("div", "routing-categories");
	const output = element("div", "stack routing-output");
	const detail = element("div", "stack routing-detail-region");
	root.replaceChildren(form, categories, output, detail);
	profile.value = initial.get("profile_id") || "";
	taskType.value = initial.get("task_type") || "";
	difficulty.value = initial.get("difficulty") || "";
	risk.value = initial.get("risk") || "";
	model.value = initial.get("model") || "";
	source.value = initial.get("source") || "";
	vision.value = initial.get("vision") || "";
	from.value = localDateTimeValue(initial.get("from"));
	to.value = localDateTimeValue(initial.get("to"));
	pageSize.value = String(state.page_size);

	function filters() {
		const selected = {
			page: state.page,
			page_size: Number(pageSize.value || state.page_size),
			category: state.category,
			profile_id: profile.value,
			task_type: taskType.value,
			difficulty: difficulty.value,
			risk: risk.value,
			model: model.value,
			source: source.value,
			vision: vision.value,
		};
		if (from.value) selected.from = new Date(from.value).toISOString();
		if (to.value) selected.to = new Date(to.value).toISOString();
		return Object.fromEntries(Object.entries(selected).filter(([, value]) => value !== ""));
	}

	async function showDetail(id) {
		detail.replaceChildren(statusMessage("正在加载路由详情…"));
		try {
			const response = await loadRoutingTrace(id);
			detail.replaceChildren(routingTraceDetail(response));
		} catch (error) {
			if (error?.status === 401) {
				onUnauthorized();
				return;
			}
			detail.replaceChildren(alertMessage(error?.message || "路由详情加载失败。"));
		}
	}

	async function refresh() {
		output.replaceChildren(statusMessage("正在加载路由轨迹…"));
		submit.disabled = true;
		try {
			const response = await loadRoutingTraces(filters());
			const page = normalizeRoutingPage(response, state);
			state.page = page.page;
			state.page_size = page.page_size;
			pageSize.value = String(state.page_size);
			replaceStatsURL("/_admin/stats/routing", filters());
			renderRoutingCategories(categories, page.category_counts, state.category, async (category) => {
				state.category = category;
				state.page = 1;
				await refresh();
			});
			output.replaceChildren(routingTraceList(page.items, showDetail), routingPager(page, async (nextPage) => {
				state.page = nextPage;
				await refresh();
			}));
		} catch (error) {
			if (error?.status === 401) {
				onUnauthorized();
				return;
			}
			output.replaceChildren(alertMessage(error?.message || "路由轨迹加载失败。"));
		} finally {
			submit.disabled = false;
		}
	}

	form.addEventListener("submit", async (event) => {
		event.preventDefault();
		state.page = 1;
		state.page_size = Number(pageSize.value || 25);
		await refresh();
	});
	await refresh();
}

function normalizeRoutingPage(response, state) {
	if (Array.isArray(response)) {
		return { items: response, page: 1, page_size: state.page_size, total: response.length, total_pages: response.length ? 1 : 0, category_counts: { all: response.length } };
	}
	return {
		items: Array.isArray(response?.items) ? response.items : [],
		page: Number(response?.page || state.page || 1),
		page_size: Number(response?.page_size || state.page_size || 25),
		total: Number(response?.total || 0),
		total_pages: Number(response?.total_pages || 0),
		category_counts: response?.category_counts || {},
	};
}

function renderRoutingCategories(root, counts, selected, onSelect) {
	root.replaceChildren();
	for (const [value, label, key] of [
		["all", "全部", "all"], ["normal", "正常", "normal"],
		["high_risk", "高风险", "high_risk"], ["fallback", "分析回退", "fallback"],
		["changed", "已升级 / 切换", "changed"],
		["failed", "失败", "failed"], ["cost_anomaly", "费用异常", "cost_anomaly"],
	]) {
		const button = textElement("button", `${label} ${Number(counts?.[key] || 0)}`);
		button.type = "button";
		button.className = value === selected ? "category-card selected" : "category-card";
		button.setAttribute("aria-pressed", String(value === selected));
		button.addEventListener("click", () => onSelect(value));
		root.append(button);
	}
}

function routingTraceList(rows, onDetail) {
	const list = element("div", "stack routing-trace-list");
	if (rows.length === 0) {
		list.append(statusMessage("暂无符合条件的路由轨迹。"));
		return list;
	}
	for (const row of rows) {
		const card = element("article", "card routing-trace-card");
		const header = element("div", "routing-trace-header");
		header.append(
			textElement("h2", `${row.task_type || "unknown"} · ${difficultyLabel(row.difficulty)}`),
			textElement("span", classificationSummary(row)),
		);
		const modelPath = row.initial_model === row.final_model ? row.initial_model : `${row.initial_model} → ${row.final_model}`;
		const summary = textElement("p", `${row.profile_slug || "-"} · ${row.strategy || "-"} / ${row.route || "-"} · ${modelPath || "-"}`);
		const metadata = textElement("p", `${reliableDate(row.created_at)} · ${Number(row.status_code || 0)} · ${visionModeLabel(row.vision_mode)} · ${Number(row.elapsed_ms || 0)} ms`);
		metadata.className = "muted";
		const costs = textElement("p", `计划 ${formatMicroUSD(row.planned_worst_case_cost_micro_usd)} · 已消费估算 ${formatMicroUSD(row.consumed_estimated_cost_micro_usd)} · 已知实际 ${formatMicroUSD(row.known_actual_cost_micro_usd)}`);
		const action = textElement("button", "查看详情");
		action.type = "button";
		action.className = "button-secondary";
		action.addEventListener("click", () => onDetail(row.id));
		card.append(header, summary, metadata, costs, action);
		list.append(card);
	}
	return list;
}

function routingPager(page, onPage) {
	const wrapper = element("div", "routing-pager");
	const previous = textElement("button", "上一页");
	previous.type = "button";
	previous.className = "button-secondary";
	previous.disabled = page.page <= 1;
	previous.addEventListener("click", () => onPage(page.page - 1));
	const label = textElement("span", `第 ${page.page} / ${Math.max(page.total_pages, 1)} 页 · 共 ${page.total} 条`);
	const next = textElement("button", "下一页");
	next.type = "button";
	next.className = "button-secondary";
	next.disabled = page.total_pages === 0 || page.page >= page.total_pages;
	next.addEventListener("click", () => onPage(page.page + 1));
	wrapper.append(previous, label, next);
	return wrapper;
}

function routingTraceDetail(response = {}) {
	const trace = response.trace || {};
	const wrapper = element("section", "card routing-trace-detail");
	wrapper.append(textElement("h2", `路由详情 #${trace.id || "-"}`));
	const facts = element("dl", "detail-grid");
	for (const [label, value] of [
		["任务类型", trace.task_type || "unknown"], ["难度", difficultyLabel(trace.difficulty)],
		["风险", riskLabel(trace.risk)], ["判断来源", classificationSourceLabel(trace.classification_source)],
		["置信度", trace.classification_source === "fallback" ? "不可用" : `${Number(trace.classification_confidence_bps || 0) / 100}%`],
		["判断依据", (trace.classification_reason_codes || []).join("、") || "-"],
		["估算输入", `${Number(trace.estimated_input_tokens || 0)} Token`],
		["请求输出", `${Number(trace.requested_output_tokens || 0)} Token`],
		["选型结论", trace.decision_reason || "-"],
	]) {
		facts.append(textElement("dt", label), textElement("dd", value));
	}
	wrapper.append(facts, detailTable("候选模型", ["模型", "结果", "原因", "质量", "严重错误率", "预期费用"], (response.candidates || []).map((row) => [
		row.model, row.decision, row.reason_code, `${Number(row.quality_score_bps || 0) / 100}%`,
		`${Number(row.severe_error_rate_bps || 0) / 100}%`, formatMicroUSD(row.expected_cost_micro_usd),
	])), detailTable("上游调用链", ["序号", "类型", "模型", "节点", "结果", "估算费用", "实际费用"], (response.calls || []).map((row) => [
		row.sequence, row.kind, row.model, row.target, `${row.status_code} / ${row.outcome}`,
		formatMicroUSD(row.estimated_cost_micro_usd), row.actual_cost_known ? formatMicroUSD(row.actual_cost_micro_usd) : "未知",
	])));
	return wrapper;
}

function detailTable(captionText, headings, rows) {
	const wrap = element("div", "table-wrap");
	const table = element("table");
	const caption = textElement("caption", captionText);
	const head = element("thead");
	const headRow = element("tr");
	for (const heading of headings) headRow.append(textElement("th", heading));
	head.append(headRow);
	const body = element("tbody");
	for (const values of rows) {
		const row = element("tr");
		for (const value of values) row.append(textElement("td", value));
		body.append(row);
	}
	table.append(caption, head, body);
	wrap.append(table);
	return wrap;
}

function difficultyLabel(value) {
	return { easy: "简单", medium: "中等", hard: "困难", unknown: "未知" }[value] || "未知";
}

function riskLabel(value) {
	return ({ high: "高风险", normal: "普通", unknown: "未知" })[value] || "未知";
}

function classificationSummary(row) {
	if (row.classification_source === "fallback") {
		return "分析失败 · 已使用强模型兜底";
	}
	return `${riskLabel(row.risk)} · ${classificationSourceLabel(row.classification_source)} · 置信度 ${Number(row.classification_confidence_bps || 0) / 100}%`;
}

function routingTraceTable(rows) {
  const wrapper = element("div", "table-wrap");
  const table = element("table");
  const caption = textElement("caption", "最近 Auto 路由");
  const head = element("thead");
  const headerRow = element("tr");
  for (const heading of [
    "时间",
    "Profile",
    "策略 / Route",
    "任务判断",
    "模型 / 上游节点路径",
    "视觉",
    "结果",
    "调用预算",
    "成本估算",
    "耗时",
  ]) {
    headerRow.append(textElement("th", heading));
  }
  head.append(headerRow);

  const body = element("tbody");
  for (const row of rows) {
    const tableRow = element("tr");
    const modelPath = row.initial_model === row.final_model
      ? row.initial_model
      : `${row.initial_model} → ${row.final_model}`;
    const initialTarget = row.initial_target || "primary";
    const finalTarget = row.final_target || initialTarget;
    const targetPath = initialTarget === finalTarget
      ? initialTarget
      : `${initialTarget} → ${finalTarget}`;
    const escalationCount = Number(row.self_escalations ?? 0);
    const escalationSummary = escalationCount > 0
      ? ` / 主动升级 ${escalationCount}（${selfEscalationReasonLabel(row.self_escalation_reason)}）`
      : "";
    for (const value of [
      reliableDate(row.created_at),
      row.profile_slug,
      `${row.strategy} / ${row.route}`,
      `${row.task_type} · ${classificationSourceLabel(row.classification_source)}`,
      `${modelPath} · 上游节点 ${targetPath}`,
      visionModeLabel(row.vision_mode),
      `${Number(row.status_code ?? 0)} · ${row.client_committed ? "已提交" : "未提交"}`,
      `回答 ${Number(row.answer_attempts ?? 0)} / 辅助 ${Number(row.auxiliary_calls ?? 0)} / 模型切换 ${Number(row.model_switches ?? 0)} / 节点切换 ${Number(row.target_switches ?? 0)}${escalationSummary}`,
      `计划上限 ${formatMicroUSD(row.planned_worst_case_cost_micro_usd)} / 已消费估算 ${formatMicroUSD(row.consumed_estimated_cost_micro_usd)} / 持有 ${formatMicroUSD(row.held_cost_micro_usd)} / ${row.all_actual_costs_known ? "实际" : "已知实际（部分）"} ${formatMicroUSD(row.known_actual_cost_micro_usd)}`,
      `${Number(row.elapsed_ms ?? 0)} ms`,
    ]) {
      tableRow.append(textElement("td", value));
    }
    body.append(tableRow);
  }
  table.append(caption, head, body);
  wrapper.append(table);
  return wrapper;
}

function classificationSourceLabel(source) {
  if (source === "rule") {
    return "本地规则";
  }
  if (source === "analyzer") {
    return "任务分析";
  }
  if (source === "session") {
    return "Session 复用";
  }
  if (source === "fallback") {
    return "安全回退";
  }
  return String(source ?? "");
}

function selfEscalationReasonLabel(reason) {
  const labels = {
    insufficient_reasoning: "推理能力不足",
    missing_knowledge: "缺少必要知识",
    complex_tool_plan: "工具规划复杂",
    instruction_conflict: "指令存在冲突",
    other: "其他原因",
  };
  return labels[String(reason ?? "")] || "未说明原因";
}

function visionModeLabel(mode) {
  if (mode === "native") {
    return "原生视觉";
  }
  if (mode === "composite") {
    return "视觉增强";
  }
  return "无";
}

function reliableDate(value) {
  const text = String(value ?? "");
  return text || "-";
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
