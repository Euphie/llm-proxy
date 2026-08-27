import { formatMicroUSD } from "./money.js";
import { renderAgentTrajectorySection } from "./stats-agent-trajectories.js";
import {
	dateValue,
	positivePageParameter,
	replaceStatsURL,
	statsURLParameters,
} from "./stats-url.js";

const dimensionOrder = [
	"correctness",
	"completeness",
	"instruction_following",
	"format_tool_safety",
	"task_completion",
];

export function dimensionLabel(value) {
	return ({
		correctness: "正确性",
		completeness: "完整性",
		instruction_following: "指令遵循",
		format_tool_safety: "格式与工具安全",
		task_completion: "任务完成度",
	})[value] || value;
}

export function normalizeModelPerformanceFilters(filters = {}) {
	const result = {};
	for (const name of [
		"profile_id", "model", "task_type", "difficulty", "risk", "vision",
		"page", "page_size",
	]) {
		const value = String(filters[name] ?? "").trim();
		if (value !== "") result[name] = value;
	}
	for (const name of ["from", "to"]) {
		const value = String(filters[name] ?? "").trim();
		if (value !== "") result[name] = new Date(value).toISOString();
	}
	return result;
}

export async function renderModelPerformancePage(
	root,
	{
		profiles = [],
		loadModelPerformance = async () => emptyPage(),
		loadEvaluationCatalog = async () => null,
		loadAgentTrajectories = async () => emptyPage(),
		loadAgentTrajectory = async () => null,
		deleteAgentTrajectory = async () => {},
		onUnauthorized = () => {},
		initialProfileID,
	} = {},
) {
	const publicSection = element("section", "stack model-evaluation-section");
	publicSection.append(
		textElement("h2", "公开评测"),
		textElement("p", "用于新 Profile 冷启动和智能生成策略，只提供公开排名与能力先验，不代表本地 Agent 的实际质量。"),
	);
	const publicOutput = element("div", "stack");
	publicSection.append(publicOutput);
	const agentSection = element("section", "stack model-evaluation-section");
	agentSection.append(
		textElement("h2", "本地 Agent 评测"),
		textElement("p", "记录真实 Agent 的完整工具调用轨迹。任务明确结束后只评测一次；不保存上游凭据，也不支持重新评测。"),
	);
	const agentOutput = element("div", "stack");
	agentSection.append(agentOutput);
	const evidenceSection = element("section", "stack model-evaluation-section");
	evidenceSection.append(
		textElement("h2", "本地质量证据"),
		textElement("p", "本地 Agent 评审通过后会聚合到这里，供后续智能路由策略学习使用。"),
	);
	root.replaceChildren(publicSection, agentSection, evidenceSection);
	publicOutput.replaceChildren(statusMessage("正在加载公开评测目录…"));
	try {
		publicOutput.replaceChildren(publicCatalog(await loadEvaluationCatalog()));
	} catch (error) {
		if (error?.status === 401) {
			onUnauthorized();
			return;
		}
		publicOutput.replaceChildren(alertMessage(error?.message || "公开评测目录加载失败。"));
	}
	await renderAgentTrajectorySection(agentOutput, {
		profiles, loadAgentTrajectories, loadAgentTrajectory,
		deleteAgentTrajectory, onUnauthorized,
	});
	const initial = statsURLParameters();
	const state = {
		page: positivePageParameter(initial, "page", 1),
		page_size: positivePageParameter(initial, "page_size", 25, [25, 50, 100]),
	};
	const form = element("form", "card stats-filters model-performance-filters");
	const fields = element("div", "form-grid");
	const profile = fieldSelect(fields, "Profile", "profile_id", [
		["", "全部 Profile"],
		...profiles.map((item) => [String(item.id), `${item.display_name} (${item.slug})`]),
	]);
	profile.value = initialProfileID !== undefined
		? String(initialProfileID || "")
		: initial.get("profile_id") || "";
	const model = fieldInput(fields, "候选模型", "model");
	const taskType = fieldInput(fields, "任务类型", "task_type");
	const difficulty = fieldSelect(fields, "难度", "difficulty", [
		["", "全部难度"], ["easy", "简单"], ["medium", "中等"],
		["hard", "困难"], ["unknown", "未知"],
	]);
	const risk = fieldSelect(fields, "风险", "risk", [
		["", "全部风险"], ["normal", "普通"], ["high", "高风险"],
	]);
	const vision = fieldSelect(fields, "视觉处理", "vision", [
		["", "全部"], ["none", "无"], ["native", "原生视觉"],
		["composite", "视觉增强"],
	]);
	const from = fieldInput(fields, "开始日期", "from", "date");
	const to = fieldInput(fields, "结束日期", "to", "date");
	const pageSize = fieldSelect(fields, "每页", "page_size", [
		["25", "25"], ["50", "50"], ["100", "100"],
	]);
	const submit = textElement("button", "应用筛选");
	submit.type = "submit";
	submit.className = "button";
	form.append(fields, submit);
	const output = element("div", "stack model-performance-output");
	evidenceSection.append(form, output);
	model.value = initial.get("model") || "";
	taskType.value = initial.get("task_type") || "";
	difficulty.value = initial.get("difficulty") || "";
	risk.value = initial.get("risk") || "";
	vision.value = initial.get("vision") || "";
	from.value = dateValue(initial.get("from"));
	to.value = dateValue(initial.get("to"));
	pageSize.value = String(state.page_size);

	function filters() {
		return normalizeModelPerformanceFilters({
			profile_id: profile.value, model: model.value, task_type: taskType.value,
			difficulty: difficulty.value, risk: risk.value, vision: vision.value,
			from: from.value, to: to.value, page: state.page,
			page_size: pageSize.value || state.page_size,
		});
	}

	async function refresh() {
		output.replaceChildren(statusMessage("正在加载模型表现…"));
		submit.disabled = true;
		try {
			const page = normalizePage(await loadModelPerformance(filters()), state);
			state.page = page.page;
			state.page_size = page.page_size;
			pageSize.value = String(state.page_size);
			replaceStatsURL("/_admin/stats/models", filters());
			output.replaceChildren(
				performanceSummary(page.summary),
				performanceList(page.items),
				pager(page, async (next) => {
					state.page = next;
					await refresh();
				}),
			);
		} catch (error) {
			if (error?.status === 401) {
				onUnauthorized();
				return;
			}
			output.replaceChildren(alertMessage(error?.message || "模型表现加载失败。"));
		} finally {
			submit.disabled = false;
		}
	}
	form.addEventListener("submit", async (event) => {
		event.preventDefault();
		state.page = 1;
		await refresh();
	});
	await refresh();
}

function publicCatalog(catalog) {
	const card = element("article", "card public-evaluation-overview stack");
	if (!catalog) {
		card.append(
			textElement("strong", "公开评测状态暂不可用"),
			textElement("p", "本地 Agent 采集与评测不受影响；仍可在 Profile 中手动配置策略。"),
		);
		return card;
	}
	const sources = Array.isArray(catalog.sources) ? catalog.sources : [];
	const results = Array.isArray(catalog.results) ? catalog.results : [];
	card.append(
		textElement("strong", `${sources.length} 个来源 · ${results.length} 条公开结果`),
		textElement("p", results.length
			? "公开数据已载入。模型必须先确认 canonical ID，才能精确关联到对应结果。"
			: "已载入来源定义，但尚未下载评测结果。可在 Profile 的智能路由高级设置中下载并更新。"),
	);
	if (sources.length) {
		const details = element("details", "public-evaluation-sources");
		details.append(textElement("summary", "查看来源与版本"));
		const list = element("ul", "public-evaluation-source-list");
		for (const source of sources) {
			list.append(textElement("li", `${source.name || source.id} · ${source.version || "未知版本"}`));
		}
		details.append(list);
		card.append(details);
	}
	return card;
}

function performanceSummary(summary = {}) {
	const wrapper = element("div", "summary-grid model-performance-summary");
	const totalCost = Number(summary.candidate_cost_micro_usd || 0) +
		Number(summary.reference_cost_micro_usd || 0) +
		Number(summary.reviewer_cost_micro_usd || 0);
	for (const [label, value] of [
		["评测分组", Number(summary.groups || 0)],
		["可靠分组", Number(summary.reliable_groups || 0)],
		["抽样样本", Number(summary.raw_samples || 0)],
		["异步评测成本", formatMicroUSD(totalCost)],
	]) {
		const card = element("section", "card summary-card");
		card.append(textElement("h2", label), textElement("p", `${label}：${value}`));
		wrapper.append(card);
	}
	return wrapper;
}

function performanceList(items) {
	const list = element("div", "stack model-performance-list");
	if (!items.length) {
		list.append(statusMessage("还没有可聚合的本地质量证据。请先完成包含工具调用的 Agent 任务；轨迹评测成功后会自动显示。"));
		return list;
	}
	for (const item of items) {
		const estimate = item.estimate || {};
		const card = element("article", "card model-performance-card stack");
		const header = element("div", "model-performance-header");
		header.append(
			textElement("h2", `${item.candidate_model} ↔ ${item.reference_model}`),
			badge(estimate.reliable ? "证据可靠" : "继续积累", estimate.reliable),
		);
		const context = textElement("p",
			`${item.profile_name || item.profile_slug} · ${item.strategy} / ${item.route} · ` +
			`${item.task_type} · ${difficultyLabel(item.difficulty)} · ${riskLabel(item.risk)} · ${visionLabel(item.vision_mode)}`,
		);
		context.className = "muted";
		const totals = textElement("p",
			`保守总质量 ${percent(estimate.quality_lower_bps)} · 严重错误上界 ${percent(estimate.severe_error_upper_bps)} · ` +
			`${Number(estimate.raw_samples || 0)} 个样本（有效 ${Number(estimate.effective_samples || 0).toFixed(1)}）`,
		);
		const prior = textElement("p", item.prior_known
			? "策略先验：来自该评测证据对应的策略版本。"
			: "策略先验不可用：当前评分采用中性先验，仅供参考。");
		prior.className = item.prior_known ? "muted" : "notice-inline";
		const dimensions = element("div", "model-dimension-grid");
		for (const name of dimensionOrder) {
			const metric = estimate.dimensions?.[name] || {};
			const row = element("div", "model-dimension-row");
			const label = textElement("span", dimensionLabel(name));
			const progress = document.createElement("progress");
			progress.max = 10000;
			progress.value = Number(metric.lower_bps || 0);
			progress.setAttribute("aria-label", `${dimensionLabel(name)}保守评分`);
			row.append(label, progress, textElement("strong", percent(metric.lower_bps)));
			dimensions.append(row);
		}
		const costs = textElement("p",
			`候选成本 ${formatMicroUSD(estimate.candidate_cost_micro_usd)} · 基线成本 ${formatMicroUSD(estimate.reference_cost_micro_usd)} · ` +
			`评审成本 ${formatMicroUSD(estimate.reviewer_cost_micro_usd)} · 主动升级准确率 ${percent(estimate.self_escalation_precision_bps)} · 漏升率 ${percent(estimate.missed_self_escalation_rate_bps)}`,
		);
		costs.className = "muted";
		const trend = textElement("p", "动态估计：最近证据权重更高，30 天前的证据权重减半。");
		trend.className = "muted model-performance-trend";
		card.append(header, context, prior, totals, dimensions, costs, trend);
		list.append(card);
	}
	return list;
}

function pager(page, onPage) {
	const wrapper = element("div", "routing-pager");
	const previous = textElement("button", "上一页");
	previous.type = "button";
	previous.className = "button button-secondary button-compact";
	previous.disabled = page.page <= 1;
	previous.addEventListener("click", () => onPage(page.page - 1));
	const label = textElement("span", `第 ${page.page} / ${Math.max(page.total_pages, 1)} 页 · 共 ${page.total} 个分组`);
	const next = textElement("button", "下一页");
	next.type = "button";
	next.className = "button button-secondary button-compact";
	next.disabled = page.total_pages === 0 || page.page >= page.total_pages;
	next.addEventListener("click", () => onPage(page.page + 1));
	wrapper.append(previous, label, next);
	return wrapper;
}

function normalizePage(response, state) {
	return {
		items: Array.isArray(response?.items) ? response.items : [],
		page: Number(response?.page || state.page || 1),
		page_size: Number(response?.page_size || state.page_size || 25),
		total: Number(response?.total || 0),
		total_pages: Number(response?.total_pages || 0),
		summary: response?.summary || {},
	};
}

function emptyPage() {
	return { items: [], page: 1, page_size: 25, total: 0, total_pages: 0, summary: {} };
}

function percent(value) {
	return `${(Number(value || 0) / 100).toFixed(2).replace(/\.?0+$/, "")}%`;
}

function difficultyLabel(value) {
	return ({ easy: "简单", medium: "中等", hard: "困难", unknown: "未知" })[value] || value || "未知";
}

function riskLabel(value) {
	return ({ high: "高风险", normal: "普通风险", unknown: "未知风险" })[value] || "未知风险";
}

function visionLabel(value) {
	return ({ none: "无视觉", native: "原生视觉", composite: "视觉增强" })[value] || value || "无视觉";
}

function badge(label, reliable) {
	const node = textElement("span", label);
	node.className = reliable ? "badge badge-success" : "badge badge-warning";
	return node;
}

function fieldInput(parent, label, name, type = "text") {
	const wrapper = element("label", "field");
	wrapper.textContent = label;
	const input = document.createElement("input");
	input.name = name;
	input.type = type;
	wrapper.append(input);
	parent.append(wrapper);
	return input;
}

function fieldSelect(parent, label, name, options) {
	const wrapper = element("label", "field");
	wrapper.textContent = label;
	const select = document.createElement("select");
	select.name = name;
	for (const [value, text] of options) {
		const option = textElement("option", text);
		option.value = value;
		select.append(option);
	}
	wrapper.append(select);
	parent.append(wrapper);
	return select;
}

function statusMessage(message) {
	const node = textElement("p", message);
	node.setAttribute("role", "status");
	return node;
}

function alertMessage(message) {
	const node = textElement("p", message);
	node.setAttribute("role", "alert");
	return node;
}

function element(tag, className = "") {
	const node = document.createElement(tag);
	if (className) node.className = className;
	return node;
}

function textElement(tag, text) {
	const node = document.createElement(tag);
	node.textContent = text;
	return node;
}
