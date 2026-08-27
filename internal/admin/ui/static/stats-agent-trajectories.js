import { formatMicroUSD } from "./money.js";

export async function renderAgentTrajectorySection(
	root,
	{
		profiles = [],
		loadAgentTrajectories = async () => emptyPage(),
		loadAgentTrajectory = async () => null,
		deleteAgentTrajectory = async () => {},
		onUnauthorized = () => {},
	} = {},
) {
	const state = { page: 1, pageSize: 25 };
	const form = element("form", "card stats-filters agent-trajectory-filters");
	const fields = element("div", "form-grid");
	const profile = fieldSelect(fields, "Profile", "agent_profile_id", [
		["", "全部 Profile"],
		...profiles.map((item) => [String(item.id), `${item.display_name} (${item.slug})`]),
	]);
	const model = fieldInput(fields, "执行模型", "agent_model");
	const status = fieldSelect(fields, "任务状态", "agent_status", [
		["", "全部状态"], ["collecting", "采集中"], ["completed", "待评测"],
		["queued", "排队中"], ["evaluating", "评测中"], ["evaluated", "已评测"],
		["timed_out", "未完整结束"], ["interrupted", "服务重启中断"],
		["skipped", "仅审计"], ["failed", "失败"],
	]);
	const source = fieldSelect(fields, "轨迹来源", "agent_source", [
		["", "全部来源"], ["session", "跨请求 Session"],
		["request_snapshot", "请求历史快照"],
	]);
	const risk = fieldSelect(fields, "风险", "agent_risk", [
		["", "全部风险"], ["normal", "普通"], ["high", "高风险"], ["unknown", "未知"],
	]);
	const from = fieldInput(fields, "开始日期", "agent_from", "date");
	const to = fieldInput(fields, "结束日期", "agent_to", "date");
	const pageSize = fieldSelect(fields, "每页", "agent_page_size", [
		["25", "25"], ["50", "50"], ["100", "100"],
	]);
	const submit = textElement("button", "应用筛选");
	submit.type = "submit";
	submit.className = "button";
	form.append(fields, submit);
	const output = element("div", "stack agent-trajectory-output");
	root.replaceChildren(form, output);

	function filters() {
		return compact({
			profile_id: profile.value,
			model: model.value,
			status: status.value,
			source: source.value,
			risk: risk.value,
			from: startOfDay(from.value),
			to: endOfDay(to.value),
			page: state.page,
			page_size: pageSize.value || state.pageSize,
		});
	}

	async function refresh() {
		output.replaceChildren(statusMessage("正在加载本地 Agent 评测…"));
		submit.disabled = true;
		try {
			const page = normalizePage(await loadAgentTrajectories(filters()), state);
			state.page = page.page;
			state.pageSize = page.page_size;
			pageSize.value = String(state.pageSize);
			const contents = [
				summaryCards(page.summary),
				collectionExplanation(page.summary),
				trajectoryList(page.items, {
					onEvaluation: (item) => openEvaluationResult(root, item),
					onDetail: (id) => openDetail(root, id, {
						loadAgentTrajectory,
						deleteAgentTrajectory,
						onDeleted: refresh,
						onUnauthorized,
					}),
				}),
			];
			if (page.total_pages > 1) {
				contents.push(pager(page, async (next) => {
					state.page = next;
					await refresh();
				}));
			}
			output.replaceChildren(...contents);
		} catch (error) {
			if (error?.status === 401) {
				onUnauthorized();
				return;
			}
			output.replaceChildren(alertMessage(error?.message || "本地 Agent 评测加载失败。"));
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

function summaryCards(summary = {}) {
	const wrapper = element("div", "summary-grid agent-trajectory-summary");
	for (const [label, value] of [
		["完整轨迹", Number(summary.completed || 0)],
		["已完成评测", Number(summary.evaluated || 0)],
		["已写入质量证据", Number(summary.evidence_recorded || 0)],
		["评测成本", formatMicroUSD(summary.evaluation_cost_micro_usd || 0)],
	]) {
		const card = element("section", "card summary-card");
		card.append(textElement("h3", label), textElement("p", `${label}：${value}`));
		wrapper.append(card);
	}
	return wrapper;
}

function collectionExplanation(summary = {}) {
	const panel = element("div", "card agent-trajectory-explanation stack");
	if (Number(summary.total || 0) === 0) {
		panel.append(
			textElement("strong", "还没有采集到 Agent 任务"),
			textElement("p", "只有包含工具调用、保留完整上下文，并收到明确结束信号的任务才会进入质量评测。普通对话不会显示在这里。"),
		);
		return panel;
	}
	panel.append(textElement("strong", `已采集 ${Number(summary.total || 0)} 个 Agent 任务`));
	const messages = [];
	if (Number(summary.collecting || 0) > 0) messages.push(`${summary.collecting} 个仍在等待工具结果或最终回答`);
	if (Number(summary.timed_out || 0) > 0) messages.push(`${summary.timed_out} 个超过 30 分钟未完整结束，仅保留审计`);
	if (Number(summary.interrupted || 0) > 0) messages.push(`${summary.interrupted} 个因服务重启中断`);
	if (Number(summary.skipped || 0) > 0) messages.push(`${summary.skipped} 个因高风险、模型路径无法明确归因或数据截断而不参与质量评分`);
	if (Number(summary.failed || 0) > 0) messages.push(`${summary.failed} 个采集或评测失败`);
	panel.append(textElement("p", messages.length ? `${messages.join("；")}。` : "当前任务均已完成采集。"));
	return panel;
}

function trajectoryList(items, { onEvaluation, onDetail } = {}) {
	const list = element("div", "stack agent-trajectory-list");
	if (!items.length) {
		list.append(statusMessage("当前筛选条件下没有本地 Agent 轨迹。"));
		return list;
	}
	for (const item of items) {
		const card = element("article", "card agent-trajectory-card stack");
		const header = element("div", "agent-trajectory-card-header");
		const title = `${item.profile_slug || `Profile ${item.profile_id}`} · ${modelPath(item.model_path)}`;
		header.append(textElement("h3", title), statusBadge(item.status));
		const metadata = textElement(
			"p",
			`${sourceLabel(item.source)} · ${Number(item.turns || 0)} 轮 · ${Number(item.tool_calls || 0)} 次工具调用 · ${formatDuration(item.elapsed_ms)}`,
		);
		metadata.className = "muted";
		const context = textElement(
			"p",
			`${item.task_type || "未分类任务"} · ${riskLabel(item.risk)} · ${item.strategy || "未记录策略"} / ${item.route || "未记录 Route"}`,
		);
		context.className = "muted";
		const outcome = textElement("p", outcomeText(item));
		outcome.className = item.evidence_recorded ? "agent-evidence-success" : "notice-inline";
		const actions = element("div", "cluster agent-trajectory-actions");
		if (item.evidence_recorded) {
			const evaluation = textElement("button", "查看评测结果");
			evaluation.type = "button";
			evaluation.className = "button button-compact";
			evaluation.addEventListener("click", () => onEvaluation?.(item));
			actions.append(evaluation);
		}
		const detail = textElement("button", "查看轨迹");
		detail.type = "button";
		detail.className = "button button-secondary button-compact";
		detail.addEventListener("click", () => onDetail?.(Number(item.id)));
		actions.append(detail);
		card.append(header, metadata, context, outcome, actions);
		list.append(card);
	}
	return list;
}

function openEvaluationResult(root, record) {
	const dialog = element("dialog", "agent-trajectory-dialog agent-evaluation-dialog");
	const body = element("section", "dialog-body stack");
	const heading = textElement("h2", "本次评测结果");
	heading.id = `agent-evaluation-${record.id}-title`;
	dialog.setAttribute("aria-labelledby", heading.id);
	const result = record.evaluation_result;
	if (!result) {
		body.append(
			heading,
			alertMessage("这条任务完成于单次评测快照上线前，只保留了聚合质量证据。请在页面下方查看“本地质量证据”。"),
		);
	} else {
		body.append(heading, evaluationResultContent(result));
	}
	const controls = element("div", "dialog-actions");
	const close = secondaryButton("关闭");
	close.addEventListener("click", () => closeDialog(dialog));
	controls.append(close);
	body.append(controls);
	dialog.append(body);
	root.append(dialog);
	dialog.addEventListener("cancel", (event) => {
		event.preventDefault();
		closeDialog(dialog);
	});
	dialog.showModal();
}

function evaluationResultContent(result) {
	const wrapper = element("div", "stack agent-evaluation-result");
	const outcome = textElement("strong", evaluationOutcomeLabel(result.outcome));
	outcome.className = `agent-evaluation-outcome ${result.outcome === "reference_win" ? "is-warning" : "is-success"}`;
	const overview = element("div", "agent-evaluation-overview");
	for (const [label, value] of [
		["候选模型", result.candidate_model || "未记录"],
		["对照模型", result.reference_model || "未记录"],
		["评审模型", result.reviewer_model || "未记录"],
	]) {
		overview.append(textElement("p", `${label}${value}`));
	}
	const dimensions = element("div", "agent-evaluation-dimensions");
	for (const key of ["correctness", "completeness", "instruction_following", "format_tool_safety", "task_completion"]) {
		const row = element("div", "agent-evaluation-dimension");
		row.append(
			textElement("span", evaluationDimensionLabel(key)),
			textElement("strong", evaluationOutcomeLabel(result.dimensions?.[key])),
		);
		dimensions.append(row);
	}
	const severe = textElement("p", result.severe_error
		? "候选回答存在严重错误"
		: "未发现候选回答严重错误");
	severe.className = result.severe_error ? "notice-inline" : "agent-evidence-success";
	const costs = element("div", "agent-evaluation-overview");
	for (const [label, value] of [
		["候选成本", formatMicroUSD(result.candidate_cost_micro_usd || 0)],
		["对照成本", formatMicroUSD(result.reference_cost_micro_usd || 0)],
		["评审成本", formatMicroUSD(result.reviewer_cost_micro_usd || 0)],
		["候选耗时", `${Number(result.candidate_latency_ms || 0)} ms`],
		["对照耗时", `${Number(result.reference_latency_ms || 0)} ms`],
	]) {
		costs.append(textElement("p", `${label}${value}`));
	}
	const note = textElement("p", "这里只保存模型、胜负、五维结论、严重错误、成本和耗时，不保存评审提示词或回答正文。");
	note.className = "muted";
	wrapper.append(outcome, overview, dimensions, severe, costs, note);
	return wrapper;
}

function evaluationOutcomeLabel(outcome) {
	return ({
		candidate_win: "候选模型胜出",
		reference_win: "对照模型胜出",
		tie: "表现相当",
	})[outcome] || "未记录结论";
}

function evaluationDimensionLabel(dimension) {
	return ({
		correctness: "正确性",
		completeness: "完整性",
		instruction_following: "指令遵循",
		format_tool_safety: "格式与工具安全",
		task_completion: "任务完成度",
	})[dimension] || dimension;
}

async function openDetail(root, id, actions) {
	const dialog = element("dialog", "agent-trajectory-dialog");
	const body = element("section", "dialog-body stack");
	const heading = textElement("h2", "完整任务轨迹");
	heading.id = `agent-trajectory-${id}-title`;
	dialog.setAttribute("aria-labelledby", heading.id);
	const loading = statusMessage("正在解密并载入轨迹…");
	body.append(heading, loading);
	dialog.append(body);
	root.append(dialog);
	dialog.addEventListener("cancel", (event) => {
		event.preventDefault();
		closeDialog(dialog);
	});
	dialog.showModal();
	try {
		const detail = await actions.loadAgentTrajectory(id);
		body.replaceChildren(heading, detailContent(detail, dialog, root, actions));
	} catch (error) {
		if (error?.status === 401) {
			closeDialog(dialog);
			actions.onUnauthorized();
			return;
		}
		const message = alertMessage(error?.message || "轨迹详情加载失败。该记录仍可删除。");
		const controls = element("div", "dialog-actions");
		const remove = textElement("button", "删除记录");
		remove.type = "button";
		remove.className = "button button-danger";
		remove.addEventListener("click", () => openDeleteConfirmation(root, dialog, Number(id), actions));
		const close = secondaryButton("关闭");
		close.addEventListener("click", () => closeDialog(dialog));
		controls.append(remove, close);
		body.replaceChildren(heading, message, controls);
	}
}

function detailContent(detail, dialog, root, actions) {
	const wrapper = element("div", "stack agent-trajectory-detail");
	const record = detail?.record || {};
	const timeline = element("ol", "agent-trajectory-timeline");
	for (const event of detail?.trajectory?.events || []) {
		const row = element("li", "agent-trajectory-event");
		row.append(
			textElement("strong", eventLabel(event)),
			textElement("pre", eventBody(event)),
		);
		timeline.append(row);
	}
	if (!timeline.children.length) timeline.append(textElement("li", "轨迹内容为空。"));
	const metadata = textElement(
		"p",
		`${modelPath(record.model_path)} · ${Number(record.turns || 0)} 轮 · ${Number(record.tool_calls || 0)} 次工具调用 · 保留至 ${formatDate(record.expires_at)}`,
	);
	metadata.className = "muted";
	const retention = textElement("p", "内容已脱敏并使用本地密钥加密；到期后自动删除。此记录不支持重新评测。当前评测只会在任务首次完成时使用当次内存中的上游凭据。",
	);
	retention.className = "notice-inline";
	const controls = element("div", "dialog-actions");
	const remove = textElement("button", "删除记录");
	remove.type = "button";
	remove.className = "button button-danger";
	remove.addEventListener("click", () => openDeleteConfirmation(root, dialog, Number(record.id), actions));
	const close = secondaryButton("关闭");
	close.addEventListener("click", () => closeDialog(dialog));
	controls.append(remove, close);
	wrapper.append(metadata, retention, timeline, controls);
	return wrapper;
}

function openDeleteConfirmation(root, detailDialog, id, actions) {
	const dialog = element("dialog", "profile-dialog agent-trajectory-delete-dialog");
	const body = element("section", "dialog-body stack");
	const heading = textElement("h2", "删除本地 Agent 轨迹？");
	body.append(heading, textElement("p", "删除后无法恢复，相关原始轨迹也会立即从本地数据库移除。"));
	const alert = alertMessage("");
	alert.hidden = true;
	const controls = element("div", "dialog-actions");
	const confirm = textElement("button", "确认删除");
	confirm.type = "button";
	confirm.className = "button button-danger";
	const cancel = secondaryButton("取消");
	cancel.addEventListener("click", () => closeDialog(dialog));
	confirm.addEventListener("click", async () => {
		confirm.disabled = true;
		try {
			await actions.deleteAgentTrajectory(id);
			closeDialog(dialog);
			closeDialog(detailDialog);
			await actions.onDeleted();
		} catch (error) {
			if (error?.status === 401) {
				closeDialog(dialog);
				closeDialog(detailDialog);
				actions.onUnauthorized();
				return;
			}
			alert.textContent = error?.message || "删除失败，请重试。";
			alert.hidden = false;
			confirm.disabled = false;
		}
	});
	controls.append(confirm, cancel);
	body.append(alert, controls);
	dialog.append(body);
	root.append(dialog);
	dialog.addEventListener("cancel", (event) => {
		event.preventDefault();
		closeDialog(dialog);
	});
	dialog.showModal();
}

function pager(page, onPage) {
	const wrapper = element("div", "routing-pager");
	const previous = secondaryButton("上一页", true);
	previous.disabled = page.page <= 1;
	previous.addEventListener("click", () => onPage(page.page - 1));
	const label = textElement("span", `第 ${page.page} / ${Math.max(page.total_pages, 1)} 页 · 共 ${page.total} 条轨迹`);
	const next = secondaryButton("下一页", true);
	next.disabled = page.total_pages === 0 || page.page >= page.total_pages;
	next.addEventListener("click", () => onPage(page.page + 1));
	wrapper.append(previous, label, next);
	return wrapper;
}

function statusBadge(status) {
	const node = textElement("span", statusLabel(status));
	node.className = `badge ${statusClass(status)}`;
	return node;
}

function statusLabel(status) {
	return ({
		collecting: "采集中", completed: "待评测", queued: "排队中", evaluating: "评测中",
		evaluated: "已评测", timed_out: "未完整结束", interrupted: "服务重启中断",
		skipped: "仅审计", failed: "失败",
	})[status] || status || "未知";
}

function statusClass(status) {
	if (status === "evaluated") return "badge-success";
	if (["collecting", "completed", "queued", "evaluating"].includes(status)) return "badge-warning";
	if (["timed_out", "interrupted", "failed"].includes(status)) return "badge-danger";
	return "badge-neutral";
}

function outcomeText(item) {
	if (item.evidence_recorded) return "质量评审已完成，结果已写入本地模型质量证据。";
	const reasons = {
		high_risk: "高风险任务仅保留审计，不用于模型质量评分。",
		mixed_models: "模型路径无法明确归因；仅支持单模型或候选升级到强基线的两段路径。",
		ambiguous_model_path: "模型路径超过两段，或最终模型不是强基线，无法可靠归因，仅保留审计。",
		trajectory_truncated: "轨迹超过保存上限，内容已截断，仅保留审计。",
		idle_timeout: "客户端未在 30 分钟内提交最终回答，仅保留审计。",
		evaluation_disabled: "当前 Profile 未开启动态策略优化，轨迹已保存但不会发起质量评测。",
		evaluation_service_unavailable: "本地评测服务不可用，轨迹仅保留审计。",
		no_evaluation_pair: "当前模型没有可用的候选与强基线组合，无法评测。",
		reviewer_unavailable: "评审模型未配置或能力参数不完整，无法评测。",
		reference_model_unavailable: "对照模型未配置或能力参数不完整，无法评测。",
		selected_model_not_in_pair: "实际执行模型不在当前评测组合中，无法归因。",
		reference_context_too_large: "完整轨迹超过对照模型上下文容量，未发起评测。",
		reviewer_context_too_large: "完整轨迹超过评审模型上下文容量，未发起评测。",
		evaluation_cost_unavailable: "缺少完整模型价格，无法安全预留评测预算。",
		sampled_out: "本次任务未命中配置的评测抽样比例。",
		adaptive_sampled_out: "当前证据已较充分，自适应抽样跳过了本次评测。",
		queue_full: "评测队列已满，本次任务仅保留审计。",
		credentials_expired: "评测开始前，当次内存凭据已过期。",
		budget_exceeded: "今日评测预算不足，本次任务仅保留审计。",
		evaluation_service_closed: "评测服务正在关闭，本次任务未评测。",
		no_evidence_recorded: "评测已运行，但结果不满足写入质量证据的条件。",
		evaluation_failed: "对照生成或质量评审失败，未写入质量证据。",
		review_empty: "评审模型没有返回可解析内容，未写入质量证据。",
		review_wrong_tool: "评审模型调用了非评测工具，未写入质量证据。",
		review_invalid_json: "评审模型返回了无效 JSON，未写入质量证据。",
		review_missing_fields: "评审结论缺少必要评分字段，未写入质量证据。",
		review_output_exhausted: "评审模型在提交结论前耗尽输出额度，未写入质量证据。",
		review_repair_empty: "评审模型仍未返回可解析内容；系统已完成一次格式修复，未得到有效结论。",
		review_repair_wrong_tool: "评审模型仍调用了非评测工具；系统已完成一次格式修复，未得到有效结论。",
		review_repair_invalid_json: "评审模型返回了无效 JSON；系统已完成一次格式修复，仍未得到有效结论。",
		review_repair_missing_fields: "评审结论仍缺少必要评分字段；系统已完成一次格式修复，未得到有效结论。",
		review_repair_output_exhausted: "评审模型在提交结论前耗尽输出额度；系统已提高额度并修复一次，仍未完成评审。",
	};
	return reasons[item.reason_code] || `${statusLabel(item.status)}；当前没有写入质量证据。`;
}

function eventLabel(event) {
	if (event.kind === "user_message") return "用户请求";
	if (event.kind === "tool_call") return `工具调用 · ${event.tool_name || "未命名工具"}`;
	if (event.kind === "tool_result") return `工具结果 · ${event.call_id || "未记录调用 ID"}`;
	if (event.kind === "assistant_text") return "模型回答";
	return event.kind || event.role || "轨迹事件";
}

function eventBody(event) {
	if (event.kind === "tool_call") return readableJSON(event.arguments);
	if (event.kind === "tool_result") return readableJSON(event.result);
	return String(event.text || "");
}

function readableJSON(value) {
	if (typeof value === "string") {
		try { return JSON.stringify(JSON.parse(value), null, 2); } catch { return value; }
	}
	try { return JSON.stringify(value ?? {}, null, 2); } catch { return String(value ?? ""); }
}

function normalizePage(response, state) {
	return {
		items: Array.isArray(response?.items) ? response.items : [],
		page: Number(response?.page || state.page || 1),
		page_size: Number(response?.page_size || state.pageSize || 25),
		total: Number(response?.total || 0),
		total_pages: Number(response?.total_pages || 0),
		summary: response?.summary || {},
	};
}

function compact(values) {
	return Object.fromEntries(Object.entries(values)
		.map(([key, value]) => [key, String(value ?? "").trim()])
		.filter(([, value]) => value !== ""));
}

function startOfDay(value) {
	return value ? new Date(`${value}T00:00:00`).toISOString() : "";
}

function endOfDay(value) {
	return value ? new Date(`${value}T23:59:59.999`).toISOString() : "";
}

function modelPath(models) {
	return Array.isArray(models) && models.length ? models.join(" → ") : "未记录模型";
}

function sourceLabel(source) {
	return source === "session" ? "跨请求完整轨迹" : "请求历史快照";
}

function riskLabel(risk) {
	return ({ normal: "普通风险", high: "高风险", unknown: "未知风险" })[risk] || "未知风险";
}

function formatDuration(milliseconds) {
	const value = Number(milliseconds || 0);
	return value >= 1000 ? `${(value / 1000).toFixed(value >= 10000 ? 0 : 1)} 秒` : `${value} 毫秒`;
}

function formatDate(value) {
	if (!value) return "未记录";
	const date = new Date(value);
	return Number.isNaN(date.valueOf()) ? String(value) : date.toLocaleString();
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

function secondaryButton(label, compact = false) {
	const button = textElement("button", label);
	button.type = "button";
	button.className = `button button-secondary${compact ? " button-compact" : ""}`;
	return button;
}

function closeDialog(dialog) {
	dialog.close();
	dialog.remove();
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

function emptyPage() {
	return { items: [], page: 1, page_size: 25, total: 0, total_pages: 0, summary: {} };
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
