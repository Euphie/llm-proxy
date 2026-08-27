import { formatMicroUSD } from "./money.js";
import { renderModelPerformancePage } from "./stats-models.js";
import { routingSessionFlowView } from "./routing-session-flow.js";
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
		loadRoutingSessionFlow = async (id) => ({
			scope: "request", selected_trace_id: Number(id), truncated: false, requests: [],
		}),
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
	root.replaceChildren(form, categories, output);
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
			group: "session",
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
		await openRoutingTraceDialog(
			root, id, loadRoutingTrace, loadRoutingSessionFlow, onUnauthorized,
		);
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
			}, "个会话"));
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
	root.setAttribute("aria-label", "路由分类统计，各分类计数可能重叠");
	root.title = "分类计数可能重叠，例如一次请求可以同时属于分析回退和超出计划。";
	for (const [value, label, key] of [
		["all", "全部", "all"], ["normal", "正常", "normal"],
		["high_risk", "高风险", "high_risk"], ["fallback", "分析回退", "fallback"],
		["changed", "已升级 / 切换", "changed"],
		["failed", "请求失败", "failed"], ["cost_anomaly", "超出计划", "cost_anomaly"],
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
		const session = row.scope === "session";
		const card = element("article", "card routing-trace-card");
		const header = element("div", "routing-trace-header");
		header.append(
			textElement("h2", session ? `会话ID：${row.session_ref || "-"}` : `${row.task_type || "unknown"} · ${difficultyLabel(row.difficulty)}`),
			textElement("span", classificationSummary(row)),
		);
		const modelPath = row.initial_model === row.final_model ? row.initial_model : `${row.initial_model} → ${row.final_model}`;
		const summary = textElement("p", `${row.profile_slug || "-"} · ${row.strategy || "-"} / ${row.route || "-"} · ${modelPath || "-"}`);
		const metadata = textElement("p", session
			? `${Number(row.request_count || 0)} 次请求 · ${reliableDate(row.first_created_at)} 至 ${reliableDate(row.created_at)}`
			: `${reliableDate(row.created_at)} · ${Number(row.status_code || 0)} · ${visionModeLabel(row.vision_mode)} · ${Number(row.elapsed_ms || 0)} ms`);
		metadata.className = "muted";
		const costs = textElement("p", session
			? `${Number(row.fallback_requests || 0)} 次分析回退 · ${Number(row.changed_requests || 0)} 次升级 / 切换 · ${Number(row.failed_requests || 0)} 次失败 · 已知实际 ${formatMicroUSD(row.total_known_actual_cost_micro_usd)}`
			: `计划 ${formatMicroUSD(row.planned_worst_case_cost_micro_usd)} · 已消费估算 ${formatMicroUSD(row.consumed_estimated_cost_micro_usd)} · 已知实际 ${formatMicroUSD(row.known_actual_cost_micro_usd)}`);
		const action = textElement("button", session ? "查看会话" : "查看流转");
		action.type = "button";
		action.className = "button button-compact routing-detail-trigger";
		action.setAttribute("aria-label", `查看路由详情 #${row.id}`);
		action.addEventListener("click", () => onDetail(row.id));
		card.append(header, summary, metadata, costs, action);
		list.append(card);
	}
	return list;
}

async function openRoutingTraceDialog(
	root,
	id,
	loadRoutingTrace,
	loadRoutingSessionFlow,
	onUnauthorized,
) {
	const dialog = element("dialog", "routing-detail-dialog");
	const shell = element("section", "routing-detail-dialog-shell");
	const header = element("header", "routing-detail-dialog-header");
	const headingGroup = element("div", "stack routing-detail-dialog-heading");
	const eyebrow = textElement("span", "路由轨迹");
	eyebrow.className = "eyebrow";
	const heading = textElement("h2", `路由详情 #${id}`);
	heading.id = `routing-trace-${id}-title`;
	headingGroup.append(eyebrow, heading);
	const close = textElement("button", "关闭");
	close.type = "button";
	close.className = "button button-secondary button-compact routing-detail-close";
	const refresh = textElement("button", "刷新流转");
	refresh.type = "button";
	refresh.className = "button button-secondary button-compact";
	const headerActions = element("div", "cluster routing-detail-dialog-actions");
	headerActions.append(refresh, close);
	const content = element("div", "routing-detail-dialog-content");
	content.setAttribute("aria-live", "polite");
	content.replaceChildren(statusMessage("正在加载路由详情…"));
	header.append(headingGroup, headerActions);
	shell.append(header, content);
	dialog.append(shell);
	dialog.setAttribute("aria-labelledby", heading.id);
	dialog.setAttribute("aria-modal", "true");

	const closeDialog = () => {
		if (dialog.open) dialog.close();
		dialog.remove();
	};
	close.addEventListener("click", closeDialog);
	dialog.addEventListener("cancel", (event) => {
		event.preventDefault();
		closeDialog();
	});
	dialog.addEventListener("click", (event) => {
		if (event.target === dialog) closeDialog();
	});
	root.append(dialog);
	dialog.showModal();

	const refreshContent = async () => {
		refresh.disabled = true;
		content.replaceChildren(statusMessage("正在加载路由流转…"));
		const [detailResult, flowResult] = await Promise.allSettled([
			loadRoutingTrace(id),
			loadRoutingSessionFlow(id),
		]);
		for (const result of [detailResult, flowResult]) {
			if (result.status === "rejected" && result.reason?.status === 401) {
				closeDialog();
				onUnauthorized();
				return;
			}
		}
		const children = [];
		if (flowResult.status === "fulfilled") {
			children.push(routingSessionFlowView(flowResult.value));
		} else {
			children.push(alertMessage(flowResult.reason?.message || "会话流转图加载失败，可点击“刷新流转”重试。"));
		}
		if (detailResult.status === "fulfilled") {
			children.push(routingTraceDetailDisclosure(detailResult.value));
		} else {
			children.push(alertMessage(detailResult.reason?.message || "请求明细加载失败，可点击“刷新流转”重试。"));
		}
		if (dialog.parentNode) {
			content.replaceChildren(...children);
			refresh.disabled = false;
		}
	};
	refresh.addEventListener("click", refreshContent);
	await refreshContent();
}

function routingPager(page, onPage, unit = "条") {
	const wrapper = element("div", "routing-pager");
	const previous = textElement("button", "上一页");
	previous.type = "button";
	previous.className = "button button-secondary button-compact";
	previous.disabled = page.page <= 1;
	previous.addEventListener("click", () => onPage(page.page - 1));
	const label = textElement("span", `第 ${page.page} / ${Math.max(page.total_pages, 1)} 页 · 共 ${page.total} ${unit}`);
	const next = textElement("button", "下一页");
	next.type = "button";
	next.className = "button button-secondary button-compact";
	next.disabled = page.total_pages === 0 || page.page >= page.total_pages;
	next.addEventListener("click", () => onPage(page.page + 1));
	wrapper.append(previous, label, next);
	return wrapper;
}

function routingTraceDetail(response = {}) {
	const trace = response.trace || {};
	const wrapper = element("section", "routing-trace-detail");
	const explanation = element("section", "routing-detail-explanation");
	explanation.append(
		textElement("h4", `这次为什么使用 ${trace.final_model || trace.initial_model || "该模型"}`),
		textElement("p", routingTraceExplanation(trace)),
	);
	const summary = element("dl", "routing-detail-summary-grid");
	for (const [label, value] of [
		["处理方式", routingModeLabel(trace)],
		["任务判断", classificationDisplayLabel(trace)],
		["最终结果", routingResultLabel(trace)],
		["输入规模", `${Number(trace.estimated_input_tokens || 0)} Token`],
		["最终模型", trace.final_model || "-"],
		["实际费用", formatMicroUSD(trace.known_actual_cost_micro_usd)],
	]) {
		summary.append(textElement("dt", label), textElement("dd", value));
	}
	explanation.append(summary);
	const facts = element("dl", "detail-grid");
	for (const [label, value] of [
		["任务类型", sessionTaskTypeLabel(trace)], ["难度", sessionDifficultyLabel(trace)],
		["风险", riskLabel(trace.risk)], ["判断来源", classificationSourceLabel(trace.classification_source)],
		["综合置信度", classificationConfidence(trace, "classification_confidence_bps")],
		["任务类型置信度", classificationConfidence(trace, "task_type_confidence_bps")],
		["难度信号置信度", classificationConfidence(trace, "difficulty_confidence_bps")],
		["风险置信度", classificationConfidence(trace, "risk_confidence_bps")],
		["信息充分性", trace.classification_underspecified ? "信息不足" : "信息完整"],
		["复杂度信号", complexitySignalsLabel(trace.complexity_signals)],
		["判断依据", classificationReasonsLabel(trace.classification_reason_codes)],
		["估算输入", `${Number(trace.estimated_input_tokens || 0)} Token`],
		["请求输出", `${Number(trace.requested_output_tokens || 0)} Token`],
		["选型结论", decisionReasonLabel(trace.decision_reason)],
	]) {
		facts.append(textElement("dt", label), textElement("dd", value));
	}
	const technical = element("details", "routing-technical-details");
	technical.append(textElement("summary", "查看分类、评分和调用数据"));
	technical.append(facts, detailTable("候选模型", ["模型", "状态", "原因", "质量", "稳定性", "路由分", "预期延迟", "预期费用"], (response.candidates || []).map((row) => [
		row.model, candidateDecisionLabel(row.decision), candidateReasonLabel(row.reason_code),
		`${Number(row.quality_score_bps || 0) / 100}%`, `${Number(row.stability_score_bps || 0) / 100}%`,
		`${Number(row.routing_score_bps || 0) / 100}%`, `${Number(row.expected_latency_ms || 0)} ms`,
		formatMicroUSD(row.expected_cost_micro_usd),
	])), detailTable("上游调用链", ["序号", "调用类型", "模型", "结果", "估算费用", "实际费用"], (response.calls || []).map((row) => [
		row.sequence, callKindLabel(row.kind), row.model, callResultLabel(row),
		formatMicroUSD(row.estimated_cost_micro_usd), row.actual_cost_known ? formatMicroUSD(row.actual_cost_micro_usd) : "未知",
	])));
	wrapper.append(explanation, technical);
	return wrapper;
}

function classificationConfidence(trace, field) {
	if (trace.classification_source === "fallback") return "不可用";
	if (trace.classification_source === "session") return "不适用（本轮未分类）";
	const value = trace[field] ?? trace.classification_confidence_bps ?? 0;
	return `${Number(value) / 100}%`;
}

function complexitySignalsLabel(signals = {}) {
	const labels = {
		obvious_solution: "解法明显",
		localized_change: "局部改动",
		bounded_familiar_steps: "步骤有限且常规",
		formal_proof: "形式化推导或证明",
		multiple_interacting_constraints: "多约束相互影响",
		distributed_or_concurrent: "分布式或并发",
		unknown_or_nondeterministic: "原因未知或非确定性",
		cross_system_or_layer: "跨系统或层级",
		invariants_or_compatibility: "不变量或兼容性约束",
		security_or_safety_critical: "安全关键",
		quantitative_slo: "定量 SLO",
		broad_verification: "需要广泛验证",
		migration_or_rollback: "迁移或回滚",
	};
	const active = Object.entries(signals || {})
		.filter(([, enabled]) => Boolean(enabled))
		.map(([name]) => labels[name] || name);
	return active.join("、") || "无显著复杂度信号";
}

function routingTraceDetailDisclosure(response = {}) {
	const details = element("details", "routing-request-details");
	details.append(
		textElement("summary", "本次路由说明与排障数据"),
		routingTraceDetail(response),
	);
	return details;
}

function routingTraceExplanation(trace) {
	if (trace.classification_source === "session") {
		const reason = String((trace.classification_reason_codes || [])[0] || "");
		if (reason === "session_highest_model_locked") {
			return "这个会话已经可靠地锁定到最高模型，本轮不再分类，也不会降级。";
		}
		if (reason === "session_model_locked") {
			return "前几轮已经稳定选中同一模型并达到锁定条件，本轮直接复用。";
		}
		if (reason === "session_task_continuation") {
			return "这是同一任务的工具结果或续接回合，因此沿用上一轮判断和模型，不重复分类。";
		}
		return "本轮沿用了会话中已经保存的任务判断和模型。";
	}
	if (trace.classification_source === "fallback") {
		if (retainsSessionModelAfterAnalyzerFailure(trace)) return retainedSessionModelExplanation();
		return "任务分析没有得到可用结论，系统为避免低估难度，改用强模型安全兜底。";
	}
	return `${classificationDisplayLabel(trace)}。${decisionReasonLabel(trace.decision_reason)}。`;
}

function routingModeLabel(trace) {
	if (trace.classification_source === "session") return "沿用会话模型";
	if (trace.classification_source === "fallback") {
		return retainsSessionModelAfterAnalyzerFailure(trace) ? "分析失败后保持模型不变" : "分析失败后安全兜底";
	}
	return "重新分析并选型";
}

function classificationDisplayLabel(trace) {
	if (retainsSessionModelAfterAnalyzerFailure(trace)) return "本轮分析无效";
	if (trace.classification_source === "session" && !hasKnownClassification(trace)) {
		return "本轮未重新分类";
	}
	return `${taskTypeLabel(trace.task_type)} / ${difficultyLabel(trace.difficulty)} / ${riskLabel(trace.risk)}`;
}

function routingResultLabel(trace) {
	return Number(trace.status_code || 0) >= 200 && Number(trace.status_code || 0) < 300 && trace.client_committed
		? `成功 · HTTP ${Number(trace.status_code || 0)}`
		: `失败 · HTTP ${Number(trace.status_code || 0)}`;
}

function hasKnownClassification(trace) {
	return Boolean(trace.task_type && trace.task_type !== "unknown" && trace.difficulty && trace.difficulty !== "unknown");
}

function retainsSessionModelAfterAnalyzerFailure(trace) {
	return (trace.classification_reason_codes || []).includes("session_model_retained_after_analyzer_failure");
}

function retainedSessionModelExplanation() {
	return "本轮继续使用会话中原来的模型，且不会锁定或覆盖会话判断；下一轮会重新分析。";
}

function sessionTaskTypeLabel(trace) {
	return trace.classification_source === "session" && !hasKnownClassification(trace)
		? "本轮未重新分类"
		: taskTypeLabel(trace.task_type);
}

function sessionDifficultyLabel(trace) {
	return trace.classification_source === "session" && !hasKnownClassification(trace)
		? "沿用会话模型"
		: difficultyLabel(trace.difficulty);
}

function taskTypeLabel(value) {
	return {
		simple: "简单问答", general: "通用任务", reasoning: "推理任务", math: "数学任务",
		coding: "编码任务", tool_use: "工具操作", vision: "视觉任务", unknown: "未识别",
	}[value] || "未识别";
}

function classificationReasonsLabel(values = []) {
	const labels = {
		task_analyzer: "任务分析模型判断",
		session_reuse: "沿用会话判断",
		session_task_continuation: "同一任务续接",
		session_model_locked: "会话模型已经锁定",
		session_highest_model_locked: "会话已经锁定最高模型",
		session_model_retained_after_analyzer_failure: "分析失败，本轮保持会话模型不变",
		complexity_easy_localized: "解法明确且改动局部",
		complexity_formal_proof: "需要形式化推导或证明",
		complexity_distributed_invariants: "涉及分布式与不变量",
		complexity_unknown_cross_system: "原因未知且跨系统",
		complexity_interacting_constraints: "多个约束相互影响",
		complexity_bounded: "步骤范围明确",
		difficulty_low_confidence_hard_guard: "难度置信度不足，按困难保护",
		task_type_low_confidence: "任务类型置信度不足",
		difficulty_low_confidence: "难度置信度不足",
		risk_low_confidence: "风险置信度不足",
	};
	return values.map((value) => {
		if (String(value).startsWith("task_analyzer_malformed_response")) return "任务分析返回格式错误";
		return labels[value] || value;
	}).join("、") || "未记录";
}

function decisionReasonLabel(value) {
	return {
		"Session binding": "沿用会话绑定",
		"highest weighted routing score": "综合路由分最高",
		"lowest expected cost": "预计总成本最低",
		"guarded difficulty baseline": "难度需要保守保护，因此使用强模型基线",
		"task analyzer fallback": "任务分析失败，因此使用强模型兜底",
		"analyzer failure; retained Session model": "任务分析失败，保持会话模型不变",
		"high risk": "高风险任务，因此使用强模型基线",
	}[value] || value || "未记录";
}

function candidateDecisionLabel(value) {
	return { selected: "已选用", eligible: "可用", rejected: "已排除" }[value] || "状态未知";
}

function candidateReasonLabel(value) {
	return {
		session_binding: "沿用会话绑定",
		highest_weighted_routing_score: "综合路由分最高",
		passed_all_gates: "通过全部门槛",
		quality_below_route_minimum: "质量低于 Route 门槛",
		quality_below_session_minimum: "质量低于会话已达到水平",
		stability_below_route_minimum: "稳定性低于 Route 门槛",
		severe_error_rate_above_route_maximum: "严重错误率超过上限",
		latency_target_not_met: "不满足延迟目标",
		net_savings_below_minimum: "节省幅度不足",
		context_window_exceeded: "上下文容量不足",
		tools_not_supported: "不支持所需工具",
		agent_workflow_not_supported: "未通过 Agent 工作流兼容验证",
		structured_output_not_supported: "不支持结构化输出",
		lowest_expected_cost: "预计总成本最低",
		analyzer_failure_session_retained: "分析失败，保持会话模型不变",
	}[value] || value || "未记录原因";
}

function callKindLabel(value) {
	return { analyzer: "任务分析", vision: "视觉处理", answer: "模型回答" }[value] || value || "其他调用";
}

function callResultLabel(row) {
	const succeeded = Number(row.status_code || 0) >= 200 && Number(row.status_code || 0) < 300;
	return succeeded ? `成功 · HTTP ${Number(row.status_code || 0)}` : `失败 · HTTP ${Number(row.status_code || 0)}`;
}

function detailTable(captionText, headings, rows) {
	const widthClass = headings.length > 6 ? " routing-detail-table-wide" : "";
	const wrap = element("div", `table-wrap routing-detail-table${widthClass}`);
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
		const succeeded = Number(row.status_code || 0) >= 200 &&
			Number(row.status_code || 0) < 300 && Boolean(row.client_committed);
		if (retainsSessionModelAfterAnalyzerFailure(row)) {
			return succeeded
				? `请求成功 · 分析失败后保持 ${row.final_model || "会话模型"}`
				: "请求失败 · 分析失败时保持了会话模型";
		}
		return succeeded
			? "请求成功 · 分析回退到强模型"
			: "请求失败 · 分析阶段已回退";
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
    "模型路径",
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
    const escalationCount = Number(row.self_escalations ?? 0);
    const escalationSummary = escalationCount > 0
      ? ` / 主动升级 ${escalationCount}（${selfEscalationReasonLabel(row.self_escalation_reason)}）`
      : "";
    for (const value of [
      reliableDate(row.created_at),
      row.profile_slug,
      `${row.strategy} / ${row.route}`,
      `${row.task_type} · ${classificationSourceLabel(row.classification_source)}`,
      modelPath,
      visionModeLabel(row.vision_mode),
      `${Number(row.status_code ?? 0)} · ${row.client_committed ? "已提交" : "未提交"}`,
      `回答 ${Number(row.answer_attempts ?? 0)} / 辅助 ${Number(row.auxiliary_calls ?? 0)} / 模型切换 ${Number(row.model_switches ?? 0)}${escalationSummary}`,
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
