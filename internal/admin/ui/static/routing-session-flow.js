export function routingFlowNodes(request = {}) {
	const trace = request.trace || {};
	const calls = [...(request.calls || [])].sort(
		(left, right) => Number(left.sequence || 0) - Number(right.sequence || 0),
	);
	const nodes = [requestNode(trace)];
	for (const call of calls.filter((item) => item.kind === "analyzer")) {
		nodes.push(callNode(call));
	}
	nodes.push(classificationNode(trace));
	nodes.push(candidateNode(request.candidates || [], trace));

	let previousAnswerModel = "";
	let escalationShown = false;
	for (const call of calls.filter((item) => item.kind !== "analyzer")) {
		if (
			call.kind === "answer" && previousAnswerModel &&
			call.model && call.model !== previousAnswerModel
		) {
			const selfEscalated = !escalationShown && Number(trace.self_escalations || 0) > 0;
			nodes.push(modelChangeNode(trace, previousAnswerModel, call.model, selfEscalated));
			escalationShown = escalationShown || selfEscalated;
		}
		nodes.push(callNode(call));
		if (call.kind === "answer" && call.model) {
			previousAnswerModel = call.model;
		}
	}
	nodes.push(resultNode(trace));
	return nodes;
}

export function routingSessionFlowView(flow = {}) {
	const wrapper = element("section", "routing-session-flow stack");
	const header = element("header", "routing-session-flow-header");
	const headingGroup = element("div", "stack routing-session-flow-heading");
	const sessionLabel = flow.scope === "session" && flow.session_ref
		? `会话 ${flow.session_ref}`
		: "单次请求流转";
	const requests = Array.isArray(flow.requests) ? flow.requests : [];
	headingGroup.append(
		textElement("h3", sessionLabel),
		textElement("p", `${requests.length} 次请求 · 点击节点查看判断依据和调用数据`, "muted"),
		textElement(
			"p",
			flow.scope === "session"
				? `已关联会话 · 安全引用 ${flow.session_ref}`
				: "未检测到会话标识，因此仅展示当前请求",
			"routing-flow-scope",
		),
	);
	header.append(headingGroup);
	wrapper.append(header);
	if (flow.truncated) {
		wrapper.append(textElement(
			"p",
			"当前仅显示选中请求之前最近 25 次会话请求。",
			"warning-banner",
		));
	}

	if (requests.length === 0) {
		wrapper.append(textElement("p", "当前会话没有可展示的路由请求。", "empty-state"));
		return wrapper;
	}
	const turns = element("nav", "routing-flow-turns stack");
	turns.setAttribute("aria-label", "会话请求");
	turns.append(
		textElement("h4", flow.scope === "session" ? "会话请求" : "当前请求"),
		textElement("p", "选择一次请求查看完整流转", "muted"),
	);
	const turnList = element("div", "routing-flow-turn-list");
	const stage = element("section", "routing-flow-stage");
	const turnButtons = [];
	let activeIndex = requests.findIndex(
		(request) => Number(request?.trace?.id || 0) === Number(flow.selected_trace_id || 0),
	);
	if (activeIndex < 0) activeIndex = 0;

	const renderRequest = (index) => {
		activeIndex = index;
		turnButtons.forEach((button, buttonIndex) => {
			button.className = `routing-flow-turn${buttonIndex === activeIndex ? " is-active" : ""}`;
			button.setAttribute("aria-current", buttonIndex === activeIndex ? "true" : "false");
		});
		const request = requests[activeIndex];
		const requestHeader = element("header", "routing-flow-request-header");
		requestHeader.append(
			textElement("h4", `第 ${activeIndex + 1} 次请求 · 当前查看`),
			textElement("span", reliableDate(request?.trace?.created_at), "muted"),
		);
		const track = element("div", "routing-flow-track");
		const inspector = element("aside", "routing-flow-inspector stack");
		inspector.setAttribute("aria-live", "polite");
		const nodes = routingFlowNodes(request);
		nodes.forEach((node) => {
			const step = element("div", "routing-flow-step");
			const button = textElement("button", node.title, `routing-flow-node tone-${node.tone}`);
			button.type = "button";
			button.setAttribute("aria-label", `${node.title}：${node.subtitle}`);
			button.append(textElement("span", node.subtitle, "routing-flow-node-meta"));
			button.addEventListener("click", () => renderInspector(inspector, node));
			step.append(button);
			track.append(step);
		});
		const focus = element("div", "routing-flow-focus");
		focus.append(track, inspector);
		stage.replaceChildren(requestHeader, focus);
		renderInspector(inspector, requestSummaryNode(request));
	};

	requests.forEach((request, index) => {
		const trace = request?.trace || {};
		const button = textElement("button", `第 ${index + 1} 次`, "routing-flow-turn");
		button.type = "button";
		button.append(
			textElement("span", turnClassificationLabel(trace), "routing-flow-turn-meta"),
			textElement("span", `${trace.final_model || "未选择模型"} · ${resultStatusLabel(trace)}`, "routing-flow-turn-result"),
		);
		button.addEventListener("click", () => renderRequest(index));
		turnButtons.push(button);
		turnList.append(button);
	});
	turns.append(turnList);
	const layout = element("div", "routing-flow-layout");
	layout.append(turns, stage);
	wrapper.append(layout);
	renderRequest(activeIndex);
	return wrapper;
}

function requestNode(trace) {
	return {
		kind: "request",
		title: "请求进入",
		subtitle: `${trace.path || "/v1/messages"} · ${Number(trace.estimated_input_tokens || 0)} Token`,
		tone: "neutral",
		details: [
			["请求路径", trace.path || "-"],
			["估算输入", `${Number(trace.estimated_input_tokens || 0)} Token`],
			["请求输出", `${Number(trace.requested_output_tokens || 0)} Token`],
		],
	};
}

function classificationNode(trace) {
	const source = String(trace.classification_source || "fallback");
	const reason = firstClassificationReason(trace);
	const retainedSessionModel = retainsSessionModelAfterAnalyzerFailure(trace);
	const sessionTitle = {
		session_highest_model_locked: "最高模型已锁定",
		session_model_locked: "会话模型已锁定",
		session_task_continuation: "续接同一任务",
	}[reason] || "沿用会话判断";
	const base = {
		kind: source === "session" ? "session" : "classification",
		title: source === "session"
			? sessionTitle
			: retainedSessionModel ? "分析失败 · 保持模型不变"
				: source === "fallback" ? "安全回退" : "任务判断",
		tone: source === "fallback" ? "warning" : source === "session" ? "accent" : "neutral",
		details: [
			["判断来源", classificationSourceLabel(source)],
			["任务类型", sessionTaskLabel(trace)],
			["难度", difficultyLabel(trace.difficulty)],
			["风险", riskLabel(trace.risk)],
			["任务类型置信度", classificationConfidence(trace, "task_type_confidence_bps")],
			["难度信号置信度", classificationConfidence(trace, "difficulty_confidence_bps")],
			["风险置信度", classificationConfidence(trace, "risk_confidence_bps")],
			["信息充分性", trace.classification_underspecified ? "信息不足" : "信息完整"],
			["复杂度信号", complexitySignalsLabel(trace.complexity_signals)],
			["判断依据", classificationReasonsLabel(trace.classification_reason_codes)],
		],
	};
	if (source === "session") {
		return {
			...base,
			subtitle: hasKnownClassification(trace)
				? `沿用 ${taskTypeLabel(trace.task_type)} · ${difficultyLabel(trace.difficulty)}`
				: "本轮未重新分类 · 沿用已绑定模型",
			description: sessionReuseExplanation(trace),
		};
	}
	if (retainedSessionModel) {
		return {
			...base,
			subtitle: `继续使用 ${trace.initial_model || trace.final_model || "会话模型"} · 下一轮重新分析`,
			description: retainedSessionModelExplanation(),
		};
	}
	const confidence = `${Number(trace.classification_confidence_bps || 0) / 100}%`;
	return { ...base, subtitle: `${taskTypeLabel(trace.task_type)} · ${difficultyLabel(trace.difficulty)} · ${confidence}` };
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

function candidateNode(candidates, trace) {
	const selected = candidates.find((item) => item.decision === "selected");
	const rejected = candidates.filter((item) => item.decision === "rejected");
	const selectedModel = selected?.model || trace.initial_model || "未选择";
	const reused = trace.classification_source === "session" && selected?.reason_code === "session_binding";
	return {
		kind: "candidates",
		title: reused ? "沿用已绑定模型" : "候选筛选",
		subtitle: reused
			? `继续使用 ${selectedModel} · 未重新比较其他模型`
			: `${selectedModel} 入选 · ${rejected.length} 个被排除`,
		tone: "accent",
		details: [
			["选中模型", selectedModel],
			["选型结论", decisionReasonLabel(trace.decision_reason || selected?.reason_code)],
			["候选结果", candidates.map((item) =>
				`${item.model}：${candidateDecisionLabel(item.decision)}，${candidateReasonLabel(item.reason_code)}`
			).join("；") || "无候选明细"],
		],
		description: reused
			? "本轮没有重新运行候选评分，代理直接使用会话中已经绑定的模型。"
			: "候选模型先通过能力和质量门槛，再按路由综合分选出本次回答模型。",
	};
}

function callNode(call) {
	const succeeded = Number(call.status_code || 0) >= 200 && Number(call.status_code || 0) < 300;
	const labels = { analyzer: "任务分析", vision: "视觉处理", answer: Number(call.retry_index || 0) > 0 ? "回答重试" : "模型回答" };
	return {
		kind: call.kind || "call",
		title: labels[call.kind] || "上游调用",
		subtitle: `${call.model || "-"} · ${Number(call.status_code || 0)} / ${call.outcome || "unknown"}`,
		tone: succeeded ? "success" : "error",
		details: [
			["模型", call.model || "-"],
			["结果", `${Number(call.status_code || 0)} / ${call.outcome || "unknown"}`],
			["重试序号", Number(call.retry_index || 0)],
			["估算费用", formatMicroUSD(call.estimated_cost_micro_usd)],
			["实际费用", call.actual_cost_known ? formatMicroUSD(call.actual_cost_micro_usd) : "未知"],
			["Token", `${Number(call.input_tokens || 0)} 输入 / ${Number(call.output_tokens || 0)} 输出`],
		],
	};
}

function modelChangeNode(trace, from, to, selfEscalated) {
	return {
		kind: selfEscalated ? "escalation" : "switch",
		title: selfEscalated ? "主动升级" : "模型切换",
		subtitle: `${from} → ${to}`,
		tone: "warning",
		details: [
			["模型路径", `${from} → ${to}`],
			["原因", selfEscalated
				? selfEscalationReasonLabel(trace.self_escalation_reason)
				: "前一次模型调用未完成请求"],
		],
	};
}

function resultNode(trace) {
	const succeeded = Number(trace.status_code || 0) >= 200 &&
		Number(trace.status_code || 0) < 300 && Boolean(trace.client_committed);
	return {
		kind: "result",
		title: succeeded ? "已返回客户端" : "请求失败",
		subtitle: `${Number(trace.status_code || 0)} · ${trace.final_model || "-"} · ${Number(trace.elapsed_ms || 0)} ms`,
		tone: succeeded ? "success" : "error",
		details: [
			["最终模型", trace.final_model || "-"],
			["HTTP 状态", Number(trace.status_code || 0)],
			["客户端已接收", trace.client_committed ? "是" : "否"],
			["耗时", `${Number(trace.elapsed_ms || 0)} ms`],
			["已知实际费用", formatMicroUSD(trace.known_actual_cost_micro_usd)],
		],
	};
}

function renderInspector(root, node) {
	if (!node) {
		root.replaceChildren(textElement("p", "选择一个节点查看详情。", "muted"));
		return;
	}
	const facts = element("dl", "routing-flow-node-facts");
	for (const [label, value] of node.details || []) {
		facts.append(textElement("dt", label), textElement("dd", String(value)));
	}
	const children = [
		textElement("h4", node.title),
		textElement("p", node.subtitle, "muted"),
	];
	if (node.description) children.push(textElement("p", node.description, "routing-flow-explanation"));
	children.push(facts);
	root.replaceChildren(...children);
}

function requestSummaryNode(request = {}) {
	const trace = request.trace || {};
	const calls = request.calls || [];
	const succeeded = Number(trace.status_code || 0) >= 200 && Number(trace.status_code || 0) < 300 && trace.client_committed;
	return {
		title: `本次为什么使用 ${trace.final_model || "该模型"}`,
		subtitle: succeeded ? "请求已成功返回客户端" : "请求未成功完成",
		description: routingExplanation(trace),
		details: [
			["处理方式", routingModeLabel(trace)],
			["任务判断", classificationDisplayLabel(trace)],
			["最终模型", trace.final_model || "-"],
			["上游调用", `${calls.length} 次`],
			["总耗时", `${Number(trace.elapsed_ms || 0)} ms`],
			["已知实际费用", formatMicroUSD(trace.known_actual_cost_micro_usd)],
		],
	};
}

function routingExplanation(trace) {
	if (trace.classification_source === "session") return sessionReuseExplanation(trace);
	if (trace.classification_source === "fallback") {
		if (retainsSessionModelAfterAnalyzerFailure(trace)) return retainedSessionModelExplanation();
		return "任务分析没有得到可用结论，系统为避免低估难度，改用强模型安全兜底。";
	}
	return `${classificationDisplayLabel(trace)}。${decisionReasonExplanation(trace.decision_reason)}`;
}

function sessionReuseExplanation(trace) {
	const reason = firstClassificationReason(trace);
	if (reason === "session_highest_model_locked") {
		return "这个会话已经可靠地锁定到最高模型，本轮不再调用任务分析器，也不会降级。";
	}
	if (reason === "session_model_locked") {
		return "前几轮已经稳定选中同一模型并达到锁定条件，本轮直接复用，不再重新分类。";
	}
	if (reason === "session_task_continuation") {
		return "这是同一任务的工具结果或续接回合，系统沿用上一轮判断和模型，避免重复分析。";
	}
	return "系统沿用了这个会话中已经保存的任务判断和模型选择。";
}

function retainedSessionModelExplanation() {
	return "本轮继续使用会话中原来的模型，且不会锁定或覆盖会话判断；下一轮会重新分析。";
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

function turnClassificationLabel(trace) {
	if (retainsSessionModelAfterAnalyzerFailure(trace)) return "分析失败 · 沿用上轮模型";
	if (trace.classification_source === "session" && !hasKnownClassification(trace)) {
		const reason = firstClassificationReason(trace);
		if (reason === "session_highest_model_locked") {
			return "最高模型已锁定 · 本轮跳过分析";
		}
		if (reason === "session_model_locked") {
			return "会话模型已锁定 · 本轮跳过分析";
		}
		return "沿用会话模型 · 本轮未分类";
	}
	return `${taskTypeLabel(trace.task_type)} · ${difficultyLabel(trace.difficulty)}`;
}

function sessionTaskLabel(trace) {
	return trace.classification_source === "session" && !hasKnownClassification(trace)
		? "本轮未重新分类"
		: taskTypeLabel(trace.task_type);
}

function hasKnownClassification(trace) {
	return Boolean(trace.task_type && trace.task_type !== "unknown" && trace.difficulty && trace.difficulty !== "unknown");
}

function firstClassificationReason(trace) {
	return String((trace.classification_reason_codes || [])[0] || "");
}

function retainsSessionModelAfterAnalyzerFailure(trace) {
	return (trace.classification_reason_codes || []).includes("session_model_retained_after_analyzer_failure");
}

function taskTypeLabel(value) {
	return {
		simple: "简单问答", general: "通用任务", reasoning: "推理任务", math: "数学任务",
		coding: "编码任务", tool_use: "工具操作", vision: "视觉任务", unknown: "未识别",
	}[value] || "未识别";
}

function resultStatusLabel(trace) {
	return Number(trace.status_code || 0) >= 200 && Number(trace.status_code || 0) < 300 && trace.client_committed
		? "成功"
		: "失败";
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
		high_risk: "高风险任务使用强模型",
		task_analyzer_fallback: "分析失败后使用强模型",
		no_route_candidate_passed_all_gates: "没有普通候选通过门槛",
		analyzer_failure_session_retained: "分析失败，保持会话模型不变",
	}[value] || value || "未记录原因";
}

function decisionReasonLabel(value) {
	return {
		"Session binding": "沿用会话绑定",
		session_binding: "沿用会话绑定",
		"highest weighted routing score": "综合路由分最高",
		highest_weighted_routing_score: "综合路由分最高",
		"lowest expected cost": "预计总成本最低",
		lowest_expected_cost: "预计总成本最低",
		"guarded difficulty baseline": "难度需要保守保护，使用强模型基线",
		"task analyzer fallback": "任务分析失败，使用强模型兜底",
		"analyzer failure; retained Session model": "任务分析失败，保持会话模型不变",
		"high risk": "高风险任务，使用强模型基线",
	}[value] || value || "未记录";
}

function decisionReasonExplanation(value) {
	const label = decisionReasonLabel(value);
	return label.endsWith("。") ? label : `${label}。`;
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

function difficultyLabel(value) {
	return { easy: "简单", medium: "中等", hard: "困难", unknown: "未知" }[value] || "未知";
}

function riskLabel(value) {
	return { normal: "普通", high: "高风险", unknown: "未知" }[value] || "未知";
}

function classificationSourceLabel(value) {
	return { rule: "本地规则", analyzer: "任务分析", session: "Session 复用", fallback: "安全回退" }[value] || "未知";
}

function selfEscalationReasonLabel(value) {
	return {
		insufficient_reasoning: "推理能力不足",
		missing_tool_capability: "缺少工具能力",
		context_too_long: "上下文过长",
	}[value] || value || "模型主动申请升级";
}

function formatMicroUSD(value) {
	return `$${(Number(value || 0) / 1_000_000).toFixed(6).replace(/0+$/, "").replace(/\.$/, "")}`;
}

function reliableDate(value) {
	const date = new Date(value);
	return Number.isNaN(date.getTime()) ? "时间未知" : date.toLocaleString();
}

function element(tag, className = "") {
	const value = document.createElement(tag);
	value.className = className;
	return value;
}

function textElement(tag, text, className = "") {
	const value = element(tag, className);
	value.textContent = String(text);
	return value;
}
