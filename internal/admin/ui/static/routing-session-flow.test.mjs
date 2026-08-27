import assert from "node:assert/strict";
import test from "node:test";

import {
	routingFlowNodes,
	routingSessionFlowView,
} from "./routing-session-flow.js";

test("routing flow orders analyzer selection retry escalation and result from real trace data", () => {
	const nodes = routingFlowNodes(sessionFlowFixture().requests[0]);
	assert.deepEqual(nodes.map((node) => node.kind), [
		"request", "analyzer", "classification", "candidates",
		"answer", "escalation", "answer", "result",
	]);
	assert.equal(nodes[1].title, "任务分析");
	assert.equal(nodes[2].subtitle, "编码任务 · 中等 · 91%");
	assert.deepEqual(nodes[2].details.slice(4, 9), [
		["任务类型置信度", "93%"],
		["难度信号置信度", "91%"],
		["风险置信度", "97%"],
		["信息充分性", "信息完整"],
		["复杂度信号", "多约束相互影响、跨系统或层级"],
	]);
	assert.equal(nodes[3].subtitle, "fast 入选 · 1 个被排除");
	assert.equal(nodes[4].subtitle, "fast · 503 / overload");
	assert.equal(nodes[5].subtitle, "fast → strong");
	assert.equal(nodes[6].subtitle, "strong · 200 / success");
	assert.equal(nodes[7].tone, "success");
});

test("routing flow represents session reuse without inventing an analyzer call", () => {
	const nodes = routingFlowNodes(sessionFlowFixture().requests[1]);
	assert.deepEqual(nodes.map((node) => node.kind), [
		"request", "session", "candidates", "answer", "result",
	]);
	assert.equal(nodes[1].title, "沿用会话判断");
	assert.equal(nodes[1].subtitle, "沿用 编码任务 · 中等");
	assert.match(nodes[1].description, /沿用了这个会话/);
	assert.equal(nodes[1].details[4][1], "不适用（本轮未分类）");
	assert.equal(nodes.some((node) => node.kind === "analyzer"), false);
});

test("routing flow keeps failed uncommitted responses visible", () => {
	const request = structuredClone(sessionFlowFixture().requests[1]);
	request.trace.status_code = 400;
	request.trace.client_committed = false;
	request.calls[0].status_code = 400;
	request.calls[0].outcome = "request_error";
	const nodes = routingFlowNodes(request);
	assert.equal(nodes.at(-1).title, "请求失败");
	assert.equal(nodes.at(-1).tone, "error");
});

test("session reuse without stored classification does not claim unknown at 100 percent", () => {
	const request = structuredClone(sessionFlowFixture().requests[1]);
	request.trace.task_type = "unknown";
	request.trace.difficulty = "unknown";
	request.trace.classification_reason_codes = ["session_task_continuation"];
	const nodes = routingFlowNodes(request);
	assert.equal(nodes[1].title, "续接同一任务");
	assert.equal(nodes[1].subtitle, "本轮未重新分类 · 沿用已绑定模型");
	assert.equal(nodes[1].details[1][1], "本轮未重新分类");
	assert.equal(nodes[1].details[4][1], "不适用（本轮未分类）");
	assert.doesNotMatch(nodes[1].subtitle, /100%|未知/);
});

test("highest-model locked turns do not appear as unrecognized unknown tasks", (t) => {
	const root = installFakeDOM(t);
	const flow = sessionFlowFixture();
	const request = structuredClone(flow.requests[1]);
	request.trace.task_type = "unknown";
	request.trace.difficulty = "unknown";
	request.trace.classification_reason_codes = ["session_highest_model_locked"];
	flow.requests = [request];
	flow.selected_trace_id = request.trace.id;

	root.append(routingSessionFlowView(flow));

	assert.ok(findText(root, "最高模型已锁定 · 本轮跳过分析"));
	assert.ok(!findText(root, "未识别 · 未知"));
});

test("analyzer failure that retains the session model is explained without claiming a strong fallback", (t) => {
	const root = installFakeDOM(t);
	const flow = sessionFlowFixture();
	const request = structuredClone(flow.requests[0]);
	request.trace.task_type = "unknown";
	request.trace.difficulty = "unknown";
	request.trace.risk = "unknown";
	request.trace.classification_source = "fallback";
	request.trace.classification_reason_codes = [
		"task_analyzer_malformed_response_invalid_json",
		"session_model_retained_after_analyzer_failure",
	];
	request.trace.decision_reason = "analyzer failure; retained Session model";
	request.trace.initial_model = "fast";
	request.trace.final_model = "fast";
	flow.requests = [request];
	flow.selected_trace_id = request.trace.id;

	const nodes = routingFlowNodes(request);
	assert.equal(nodes.find((node) => node.kind === "classification")?.title, "分析失败 · 保持模型不变");
	assert.equal(nodes.find((node) => node.kind === "classification")?.subtitle, "继续使用 fast · 下一轮重新分析");

	root.append(routingSessionFlowView(flow));
	assert.ok(findText(root, "分析失败 · 沿用上轮模型"));
	assert.ok(findText(root, "本轮继续使用会话中原来的模型，且不会锁定或覆盖会话判断；下一轮会重新分析。"));
	assert.ok(!findText(root, "任务分析失败，使用强模型兜底"));
});

test("session flow renders connected turns and reveals clicked node facts", async (t) => {
	const root = installFakeDOM(t);
	root.append(routingSessionFlowView(sessionFlowFixture()));

	assert.ok(findText(root, "会话 4d4d4d4d4d4d"));
	assert.ok(findText(root, "2 次请求"));
	assert.ok(findText(root, "选择一次请求查看完整流转"));
	assert.equal(findAll(root, ".routing-flow-turn").length, 2);
	assert.equal(findAll(root, ".routing-flow-stage").length, 1);
	assert.equal(findAll(root, ".routing-flow-layout").length, 1);
	assert.equal(findAll(root, ".routing-flow-step").length, 5);
	assert.equal(findAll(root, ".routing-flow-edge").length, 0);
	assert.equal(buttonByText(root, "请求进入").getAttribute("role"), null);
	assert.ok(findText(root, "本次为什么使用 strong"));

	await buttonByText(root, "第 1 次").dispatch("click");
	assert.equal(findAll(root, ".routing-flow-step").length, 8);

	await buttonByText(root, "主动升级").dispatch("click");
	const inspector = findAll(root, ".routing-flow-inspector")[0];
	assert.ok(findText(inspector, "推理能力不足"));
	assert.ok(findText(inspector, "fast → strong"));
});

test("single request flow explains why no session was associated", (t) => {
	const root = installFakeDOM(t);
	const flow = sessionFlowFixture();
	flow.scope = "request";
	flow.session_ref = "";
	flow.requests = [flow.requests[1]];
	root.append(routingSessionFlowView(flow));

	assert.ok(findText(root, "未检测到会话标识，因此仅展示当前请求"));
});

function sessionFlowFixture() {
	return {
		scope: "session",
		session_ref: "4d4d4d4d4d4d",
		selected_trace_id: 12,
		truncated: false,
		requests: [
			{
				trace: {
					id: 11, created_at: "2026-08-11T08:00:00Z", path: "/v1/messages",
					task_type: "coding", difficulty: "medium", risk: "normal",
					classification_source: "analyzer", classification_confidence_bps: 9100,
					task_type_confidence_bps: 9300, difficulty_confidence_bps: 9100,
					risk_confidence_bps: 9700, classification_underspecified: false,
					complexity_signals: {
						multiple_interacting_constraints: true,
						cross_system_or_layer: true,
					},
					classification_reason_codes: ["task_analyzer"],
					initial_model: "fast", final_model: "strong", model_switches: 1,
					self_escalations: 1, self_escalation_reason: "insufficient_reasoning",
					status_code: 200, client_committed: true, elapsed_ms: 4200,
					known_actual_cost_micro_usd: 2100,
				},
				candidates: [
					{ model: "fast", decision: "selected", reason_code: "highest_weighted_routing_score", routing_score_bps: 9300 },
					{ model: "strong", decision: "rejected", reason_code: "higher_expected_cost", routing_score_bps: 9100 },
				],
				calls: [
					{ sequence: 1, kind: "analyzer", model: "fast", status_code: 200, outcome: "success", estimated_cost_micro_usd: 30 },
					{ sequence: 2, kind: "answer", model: "fast", retry_index: 0, model_switch_index: 0, status_code: 503, outcome: "overload", estimated_cost_micro_usd: 400 },
					{ sequence: 3, kind: "answer", model: "strong", retry_index: 0, model_switch_index: 1, status_code: 200, outcome: "success", estimated_cost_micro_usd: 2400, actual_cost_known: true, actual_cost_micro_usd: 2100 },
				],
			},
			{
				trace: {
					id: 12, created_at: "2026-08-11T08:01:00Z", path: "/v1/messages",
					task_type: "coding", difficulty: "medium", risk: "normal",
					classification_source: "session", classification_confidence_bps: 10000,
					classification_reason_codes: ["session_reuse"],
					initial_model: "strong", final_model: "strong",
					status_code: 200, client_committed: true, elapsed_ms: 900,
					known_actual_cost_micro_usd: 900,
				},
				candidates: [
					{ model: "strong", decision: "selected", reason_code: "session_binding", routing_score_bps: 9900 },
				],
				calls: [
					{ sequence: 1, kind: "answer", model: "strong", status_code: 200, outcome: "success", estimated_cost_micro_usd: 1000, actual_cost_known: true, actual_cost_micro_usd: 900 },
				],
			},
		],
	};
}

class FakeElement {
	constructor(tagName) {
		this.tagName = tagName.toUpperCase();
		this.children = [];
		this.parentNode = null;
		this.attributes = new Map();
		this.listeners = new Map();
		this.textContent = "";
		this.className = "";
		this.type = "";
	}

	append(...children) {
		for (const child of children) {
			child.parentNode = this;
			this.children.push(child);
		}
	}

	replaceChildren(...children) {
		this.children = [];
		this.append(...children);
	}

	setAttribute(name, value) {
		this.attributes.set(name, String(value));
	}

	getAttribute(name) {
		return this.attributes.get(name) ?? null;
	}

	addEventListener(name, listener) {
		const listeners = this.listeners.get(name) || [];
		listeners.push(listener);
		this.listeners.set(name, listeners);
	}

	async dispatch(name) {
		for (const listener of this.listeners.get(name) || []) {
			await listener({ target: this, currentTarget: this });
		}
	}
}

function installFakeDOM(t) {
	const root = new FakeElement("main");
	const original = globalThis.document;
	globalThis.document = { createElement: (name) => new FakeElement(name) };
	t.after(() => { globalThis.document = original; });
	return root;
}

function descendants(root) {
	return [root, ...root.children.flatMap(descendants)];
}

function findAll(root, selector) {
	const className = selector.slice(1);
	return descendants(root).filter((item) =>
		String(item.className).split(/\s+/).includes(className)
	);
}

function findText(root, text) {
	return descendants(root).find((item) => item.textContent.includes(text));
}

function buttonByText(root, text) {
	const result = descendants(root).find((item) =>
		item.tagName === "BUTTON" && item.textContent === text
	);
	assert.ok(result, `button ${text} not found`);
	return result;
}
