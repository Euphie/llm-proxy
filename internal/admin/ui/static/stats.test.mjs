import assert from "node:assert/strict";
import test from "node:test";

import { api } from "./api.js";
import { bootstrap } from "./app.js";
import {
  kindLabel,
  normalizeFilters,
  renderStatsPage,
  tokenTotal,
} from "./stats.js";
import {
  buildProfileExport,
  renderSystemPage,
} from "./system.js";

test("empty statistics filters are omitted", () => {
  assert.deepEqual(
    normalizeFilters({
      profile_id: "",
      protocol: " ",
      model: "",
      kind: "",
      from: "",
      to: "",
    }),
    {},
  );
});

test("statistics date-time filters become RFC3339 instants", () => {
  assert.deepEqual(
    normalizeFilters({
      from: "2026-07-29T10:30:00+08:00",
      to: "2026-07-29T12:45:00+08:00",
    }),
    {
      from: "2026-07-29T02:30:00.000Z",
      to: "2026-07-29T04:45:00.000Z",
    },
  );
});

test("token total excludes cache counters already represented in input", () => {
  assert.equal(
    tokenTotal({
      input_tokens: 10,
      output_tokens: 20,
      cache_read_tokens: 4,
      cache_creation_tokens: 5,
    }),
    30,
  );
});

test("request kinds have distinct user-facing labels", () => {
  assert.equal(kindLabel("main"), "主请求");
  assert.equal(kindLabel("vision"), "视觉预处理");
});

test("statistics and System API methods omit blanks and encode exact queries", async (t) => {
  const calls = [];
  const originalDocument = globalThis.document;
  const originalFetch = globalThis.fetch;
  globalThis.document = { cookie: "" };
  globalThis.fetch = async (path, options) => {
    calls.push([path, options.method]);
    return new Response("{}", {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  };
  t.after(() => {
    globalThis.document = originalDocument;
    globalThis.fetch = originalFetch;
  });

  await api.stats({
    profile_id: "7",
    protocol: "anthropic",
    model: "",
    kind: "vision",
    from: "2026-07-29T02:30:00.000Z",
    to: "",
  });
  await api.stats({});
	await api.routingTraces({ profile_id: "7", page: 2, page_size: 50 });
	await api.routingTrace(19);
	await api.routingSessionFlow(19);
	await api.modelPerformance({ profile_id: 7, difficulty: "hard", page: 1 });
	await api.agentTrajectories({ profile_id: 7, status: "evaluated", page: 2 });
	await api.agentTrajectory(23);
	await api.deleteAgentTrajectory(23);
  await api.system();

  assert.deepEqual(calls, [
    [
      "/_admin/api/stats?profile_id=7&protocol=anthropic&kind=vision&from=2026-07-29T02%3A30%3A00.000Z",
      "GET",
    ],
    ["/_admin/api/stats", "GET"],
	["/_admin/api/routing-traces?profile_id=7&page=2&page_size=50", "GET"],
	["/_admin/api/routing-traces/19", "GET"],
	["/_admin/api/routing-traces/19/session-flow", "GET"],
	["/_admin/api/model-performance?profile_id=7&difficulty=hard&page=1", "GET"],
	["/_admin/api/agent-trajectories?profile_id=7&status=evaluated&page=2", "GET"],
	["/_admin/api/agent-trajectories/23", "GET"],
	["/_admin/api/agent-trajectories/23", "DELETE"],
    ["/_admin/api/system", "GET"],
  ]);
});

test("statistics API trims real filters and omits whitespace-only filters", async (t) => {
  const calls = [];
  const originalDocument = globalThis.document;
  const originalFetch = globalThis.fetch;
  globalThis.document = { cookie: "" };
  globalThis.fetch = async (path) => {
    calls.push(path);
    return new Response("{}", {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  };
  t.after(() => {
    globalThis.document = originalDocument;
    globalThis.fetch = originalFetch;
  });

  await api.stats({
    profile_id: " \t ",
    protocol: " anthropic ",
    model: " sonnet / 4 ",
    kind: "\n",
  });
  await api.stats({ from: " ", to: "\t" });

  assert.deepEqual(calls, [
    "/_admin/api/stats?protocol=anthropic&model=sonnet+%2F+4",
    "/_admin/api/stats",
  ]);
});

test("statistics page exposes loading then exact summary and accessible tables", async (t) => {
  const root = installFakeDOM(t);
  let finish;
  const pending = new Promise((resolve) => {
    finish = resolve;
  });

  const rendered = renderStatsPage(root, {
    profiles: [
      { id: 1, display_name: "Coding", slug: "coding" },
    ],
    loadStats: async () => pending,
    onUnauthorized: () => {},
  });

  assert.equal(findRole(root, "status").textContent, "正在加载统计数据…");
  finish(statsFixture());
  await rendered;

  assert.deepEqual(labelTexts(root), [
    "Profile",
    "协议",
    "模型",
    "请求类型",
    "开始时间",
    "结束时间",
  ]);
  assert.equal(controlByName(root, "from").type, "datetime-local");
  assert.equal(controlByName(root, "to").type, "datetime-local");
  for (const value of [
    "请求数：2",
    "输入 Token：10",
    "输出 Token：20",
    "Token 总计：30",
    "缓存读取 Token：4",
    "缓存创建 Token：5",
  ]) {
    assert.ok(findText(root, value), value);
  }
  assert.deepEqual(
    findAllTags(root, "CAPTION").map((caption) => caption.textContent),
	["按日期", "按模型"],
  );
  assert.ok(findText(root, "2026-07-29"));
  assert.ok(findText(root, "sonnet"));
	assert.ok(findText(root, "路由轨迹"));
});

test("statistics filters submit RFC3339 values and omit blanks", async (t) => {
  const root = installFakeDOM(t);
  const filters = [];

  await renderStatsPage(root, {
    profiles: [
      { id: 7, display_name: "Coding", slug: "coding" },
    ],
    loadStats: async (current) => {
      filters.push(current);
      return statsFixture();
    },
    onUnauthorized: () => {},
  });

  controlByName(root, "profile_id").value = "7";
  controlByName(root, "protocol").value = "anthropic";
  controlByName(root, "model").value = "";
  controlByName(root, "kind").value = "vision";
  controlByName(root, "from").value = "2026-07-29T10:30";
  controlByName(root, "to").value = "";
  await findTag(root, "FORM").dispatch("submit");

  assert.equal(filters.length, 2);
  assert.deepEqual(filters[1], {
    profile_id: "7",
    protocol: "anthropic",
    kind: "vision",
    from: "2026-07-29T10:30:00.000Z",
  });
});

test("routing statistics page exposes classification filters pagination and detail", async (t) => {
	const root = installFakeDOM(t);
	const filters = [];
	const details = [];
	const flows = [];
	await renderStatsPage(root, {
		section: "routing",
		profiles: [{ id: 7, display_name: "Coding", slug: "coding" }],
		loadRoutingTraces: async (current) => {
			filters.push(current);
			return routingTraceFixture();
		},
		loadRoutingTrace: async (id) => {
			details.push(id);
			return routingTraceDetailFixture();
		},
		loadRoutingSessionFlow: async (id) => {
			flows.push(id);
			return routingSessionFlowFixture();
		},
	});
	assert.deepEqual(labelTexts(root), [
		"Profile", "任务类型", "难度", "风险", "模型", "判断来源", "视觉", "开始时间", "结束时间", "每页",
	]);
	assert.equal(filters[0].page, 1);
	assert.equal(filters[0].page_size, 25);
	assert.equal(filters[0].category, "all");
	assert.equal(filters[0].group, "session");
	assert.ok(findText(root, "会话ID：4d4d4d4d4d4d"));
	assert.ok(findText(root, "4 次请求"));
	assert.ok(findText(root, "1 次分析回退 · 1 次升级 / 切换 · 0 次失败"));
	assert.ok(findText(root, "第 1 / 3 页 · 共 6 个会话"));
	const detailButton = buttonByText(root, "查看会话");
	assert.match(detailButton.className, /routing-detail-trigger/);
	await detailButton.dispatch("click");
	assert.deepEqual(details, [1]);
	assert.deepEqual(flows, [1]);
	const dialog = findTag(root, "dialog");
	assert.equal(dialog.open, true);
	assert.match(dialog.className, /routing-detail-dialog/);
	assert.ok(findText(root, "路由详情 #1"));
	assert.ok(findText(root, "会话 4d4d4d4d4d4d"));
	assert.ok(findText(root, "请求进入"));
	assert.ok(findText(root, "本次路由说明与排障数据"));
	assert.ok(findText(root, "这次为什么使用 strong"));
	assert.ok(findText(root, "任务分析模型判断"));
	assert.ok(findText(root, "任务类型置信度"));
	assert.ok(findText(root, "难度信号置信度"));
	assert.ok(findText(root, "风险置信度"));
	assert.ok(findText(root, "93%"));
	assert.ok(findText(root, "97%"));
	assert.ok(findText(root, "多约束相互影响、跨系统或层级"));
	assert.ok(findText(root, "信息完整"));
	assert.ok(findText(root, "预计总成本最低"));
	assert.equal(descendants(root).some((element) => element.className.includes("routing-detail-region")), false);
	await buttonByText(dialog, "刷新流转").dispatch("click");
	assert.deepEqual(details, [1, 1]);
	assert.deepEqual(flows, [1, 1]);
	await buttonByText(dialog, "关闭").dispatch("click");
	assert.equal(dialog.parentNode, null);
	assert.equal(descendants(root).some((element) => element.textContent.includes("节点切换")), false);
	assert.equal(descendants(root).some((element) => element.textContent.includes("上游节点")), false);
});

test("routing statistics separates analyzer fallback from high risk", async (t) => {
	const root = installFakeDOM(t);
	const fallback = {
		...routingTraceFixture().items[0],
		id: 2,
		task_type: "unknown",
		difficulty: "unknown",
		risk: "unknown",
		classification_source: "fallback",
		classification_confidence_bps: 0,
		initial_model: "strong",
		final_model: "strong",
	};
	await renderStatsPage(root, {
		section: "routing",
		loadRoutingTraces: async () => ({
			items: [fallback], page: 1, page_size: 25, total: 1, total_pages: 1,
			category_counts: { all: 1, normal: 0, high_risk: 0, fallback: 1 },
		}),
		loadRoutingTrace: async () => ({
			...routingTraceDetailFixture(),
			trace: {
				...fallback,
				classification_reason_codes: ["task_analyzer_malformed_response"],
				decision_reason: "task analyzer fallback",
			},
		}),
	});
	assert.ok(findText(root, "分析回退 1"));
	assert.ok(findText(root, "请求成功 · 分析回退到强模型"));
	await buttonByText(root, "查看会话").dispatch("click");
	assert.ok(findText(root, "不可用"));
	assert.ok(findText(root, "任务分析返回格式错误"));
});

test("routing statistics explains analyzer failure that retains the session model", async (t) => {
	const root = installFakeDOM(t);
	const retained = {
		...routingTraceFixture().items[0],
		id: 3,
		task_type: "unknown",
		difficulty: "unknown",
		risk: "unknown",
		classification_source: "fallback",
		classification_confidence_bps: 0,
		classification_reason_codes: [
			"task_analyzer_malformed_response_invalid_json",
			"session_model_retained_after_analyzer_failure",
		],
		decision_reason: "analyzer failure; retained Session model",
		initial_model: "fast",
		final_model: "fast",
	};
	await renderStatsPage(root, {
		section: "routing",
		loadRoutingTraces: async () => ({
			items: [retained], page: 1, page_size: 25, total: 1, total_pages: 1,
			category_counts: { all: 1, normal: 0, high_risk: 0, fallback: 1 },
		}),
		loadRoutingTrace: async () => ({
			...routingTraceDetailFixture(),
			trace: retained,
		}),
	});
	assert.ok(findText(root, "请求成功 · 分析失败后保持 fast"));
	await buttonByText(root, "查看会话").dispatch("click");
	assert.ok(findText(root, "本轮继续使用会话中原来的模型，且不会锁定或覆盖会话判断；下一轮会重新分析。"));
	assert.equal(descendants(root).some((element) => element.textContent.includes("任务分析失败，因此使用强模型兜底")), false);
});

test("routing filters category and page survive reload through the URL", async (t) => {
	const root = installFakeDOM(t);
	const urls = installFakeNavigation(t,
		"?profile_id=7&task_type=code&difficulty=hard&risk=high&model=fast" +
		"&source=analyzer&vision=composite&category=changed&page=2&page_size=50" +
		"&from=2026-08-01T01%3A02%3A00.000Z&to=2026-08-02T03%3A04%3A00.000Z");
	const filters = [];
	await renderStatsPage(root, {
		section: "routing",
		profiles: [{ id: 7, display_name: "Coding", slug: "coding" }],
		loadRoutingTraces: async (current) => {
			filters.push(current);
			return {
				...routingTraceFixture(), page: Number(current.page),
				page_size: Number(current.page_size), total_pages: 3,
			};
		},
	});
	assert.equal(filters[0].page, 2);
	assert.equal(filters[0].page_size, 50);
	assert.equal(filters[0].category, "changed");
	assert.equal(filters[0].profile_id, "7");
	assert.equal(filters[0].task_type, "code");
	assert.equal(filters[0].difficulty, "hard");
	assert.equal(filters[0].risk, "high");
	assert.equal(filters[0].source, "analyzer");
	assert.equal(filters[0].vision, "composite");
	assert.equal(controlByName(root, "page_size").value, "50");
	assert.equal(controlByName(root, "from").value, "2026-08-01T01:02");
	assert.match(urls.at(-1), /^\/_admin\/stats\/routing\?/);
	assert.match(urls.at(-1), /category=changed/);
	assert.match(urls.at(-1), /page=2/);
	await buttonByText(root, "下一页").dispatch("click");
	assert.equal(filters.at(-1).page, 3);
	assert.match(urls.at(-1), /page=3/);
});

test("model performance page shows five dimensions summary filters and pagination", async (t) => {
	const root = installFakeDOM(t);
	const filters = [];
	await renderStatsPage(root, {
		section: "models",
		profiles: [{ id: 7, display_name: "Coding", slug: "coding" }],
		loadModelPerformance: async (current) => {
			filters.push(current);
			return modelPerformanceFixture();
		},
		loadEvaluationCatalog: async () => evaluationCatalogFixture(),
		loadAgentTrajectories: async () => agentTrajectoryFixture(),
		loadAgentTrajectory: async () => agentTrajectoryDetailFixture(),
		deleteAgentTrajectory: async () => ({}),
		initialProfileID: 7,
	});
	assert.deepEqual(labelTexts(root).slice(-9), [
		"Profile", "候选模型", "任务类型", "难度", "风险", "视觉处理",
		"开始日期", "结束日期", "每页",
	]);
	assert.equal(filters[0].profile_id, "7");
	assert.ok(findText(root, "公开评测"));
	assert.ok(findText(root, "2 个来源 · 208 条公开结果"));
	assert.ok(findText(root, "本地 Agent 评测"));
	assert.ok(findText(root, "完整轨迹：4"));
	assert.ok(findText(root, "已写入质量证据：2"));
	assert.ok(findText(root, "Coding · sonnet-4 → opus-4"));
	assert.ok(findText(root, "本地质量证据"));
	assert.ok(findText(root, "评测分组：2"));
	assert.ok(findText(root, "fast ↔ strong"));
	assert.ok(findText(root, "正确性"));
	assert.ok(findText(root, "格式与工具安全"));
	assert.ok(findText(root, "保守总质量 91.2%"));
	assert.ok(findText(root, "第 1 / 2 页 · 共 2 个分组"));
	assert.equal(
		buttonByText(root, "上一页").className,
		"button button-secondary button-compact",
	);
	assert.equal(
		buttonByText(root, "下一页").className,
		"button button-secondary button-compact",
	);
	assert.ok(findText(root, "30 天前的证据权重减半"));
});

test("Agent trajectory explains a failed reviewer protocol repair", async (t) => {
	const root = installFakeDOM(t);
	const failed = agentTrajectoryFixture();
	failed.items[0] = {
		...failed.items[0],
		status: "failed",
		evidence_recorded: false,
		reason_code: "review_repair_invalid_json",
	};
	await renderStatsPage(root, {
		section: "models",
		profiles: [{ id: 7, display_name: "Coding", slug: "coding" }],
		loadEvaluationCatalog: async () => evaluationCatalogFixture(),
		loadAgentTrajectories: async () => failed,
		loadAgentTrajectory: async () => agentTrajectoryDetailFixture(),
		deleteAgentTrajectory: async () => ({}),
		loadModelPerformance: async () => modelPerformanceFixture(),
	});
	assert.ok(findText(root, "评审模型返回了无效 JSON；系统已完成一次格式修复，仍未得到有效结论。"));
});

test("Agent trajectory explains reviewer output exhaustion", async (t) => {
	const root = installFakeDOM(t);
	const failed = agentTrajectoryFixture();
	failed.items[0] = {
		...failed.items[0],
		status: "failed",
		evidence_recorded: false,
		reason_code: "review_repair_output_exhausted",
	};
	await renderStatsPage(root, {
		section: "models",
		profiles: [{ id: 7, display_name: "Coding", slug: "coding" }],
		loadEvaluationCatalog: async () => evaluationCatalogFixture(),
		loadAgentTrajectories: async () => failed,
		loadAgentTrajectory: async () => agentTrajectoryDetailFixture(),
		deleteAgentTrajectory: async () => ({}),
		loadModelPerformance: async () => modelPerformanceFixture(),
	});
	assert.ok(findText(root, "评审模型在提交结论前耗尽输出额度；系统已提高额度并修复一次，仍未完成评审。"));
});

test("Agent trajectory detail is centered, readable, deletable, and cannot be reevaluated", async (t) => {
	const root = installFakeDOM(t);
	const deleted = [];
	await renderStatsPage(root, {
		section: "models",
		profiles: [{ id: 7, display_name: "Coding", slug: "coding" }],
		loadEvaluationCatalog: async () => evaluationCatalogFixture(),
		loadAgentTrajectories: async () => agentTrajectoryFixture(),
		loadAgentTrajectory: async () => agentTrajectoryDetailFixture(),
		deleteAgentTrajectory: async (id) => deleted.push(id),
		loadModelPerformance: async () => modelPerformanceFixture(),
	});
	await buttonByText(root, "查看轨迹").dispatch("click");
	const detail = findAllTags(root, "DIALOG").find((item) => item.open);
	assert.ok(detail);
	assert.ok(findText(detail, "完整任务轨迹"));
	assert.ok(findText(detail, "用户请求"));
	assert.ok(findText(detail, "工具调用 · search_docs"));
	assert.ok(findText(detail, "最终回答"));
	assert.equal(findAllTags(detail, "BUTTON").some((item) => item.textContent.includes("重新评测")), false);
	await buttonByText(detail, "删除记录").dispatch("click");
	const confirmation = findAllTags(root, "DIALOG").find((item) => item.open && item !== detail);
	assert.ok(confirmation);
	assert.ok(findText(confirmation, "删除后无法恢复"));
	await buttonByText(confirmation, "确认删除").dispatch("click");
	assert.deepEqual(deleted, [31]);
});

test("Agent trajectory with unreadable encrypted detail can still be deleted", async (t) => {
	const root = installFakeDOM(t);
	const deleted = [];
	await renderStatsPage(root, {
		section: "models",
		profiles: [{ id: 7, display_name: "Coding", slug: "coding" }],
		loadEvaluationCatalog: async () => evaluationCatalogFixture(),
		loadAgentTrajectories: async () => agentTrajectoryFixture(),
		loadAgentTrajectory: async () => { throw new Error("本地密钥无法解密该记录"); },
		deleteAgentTrajectory: async (id) => deleted.push(id),
		loadModelPerformance: async () => modelPerformanceFixture(),
	});

	await buttonByText(root, "查看轨迹").dispatch("click");
	const detail = findAllTags(root, "DIALOG").find((item) => item.open);
	assert.ok(detail);
	assert.ok(findText(detail, "本地密钥无法解密该记录"));
	await buttonByText(detail, "删除记录").dispatch("click");
	const confirmation = findAllTags(root, "DIALOG").find((item) => item.open && item !== detail);
	assert.ok(confirmation);
	await buttonByText(confirmation, "确认删除").dispatch("click");
	assert.deepEqual(deleted, [31]);
});

test("evaluated Agent trajectory opens its single review result from the card", async (t) => {
	const root = installFakeDOM(t);
	await renderStatsPage(root, {
		section: "models",
		profiles: [{ id: 7, display_name: "Coding", slug: "coding" }],
		loadEvaluationCatalog: async () => evaluationCatalogFixture(),
		loadAgentTrajectories: async () => agentTrajectoryFixture(),
		loadAgentTrajectory: async () => agentTrajectoryDetailFixture(),
		deleteAgentTrajectory: async () => ({}),
		loadModelPerformance: async () => modelPerformanceFixture(),
	});
	await buttonByText(root, "查看评测结果").dispatch("click");
	const detail = findAllTags(root, "DIALOG").find((item) => item.open);
	assert.ok(detail);
	assert.ok(findText(detail, "本次评测结果"));
	assert.ok(findText(detail, "候选模型胜出"));
	assert.ok(findText(detail, "候选模型sonnet-4"));
	assert.ok(findText(detail, "对照模型opus-4"));
	assert.ok(findText(detail, "正确性"));
	assert.ok(findText(detail, "完整性"));
	assert.ok(findText(detail, "表现相当"));
	assert.ok(findText(detail, "未发现候选回答严重错误"));
	assert.ok(findText(detail, "候选成本$0.0012"));
});

test("Agent trajectory dialogs remove themselves when Escape is pressed", async (t) => {
	const root = installFakeDOM(t);
	await renderStatsPage(root, {
		section: "models",
		profiles: [{ id: 7, display_name: "Coding", slug: "coding" }],
		loadEvaluationCatalog: async () => evaluationCatalogFixture(),
		loadAgentTrajectories: async () => agentTrajectoryFixture(),
		loadAgentTrajectory: async () => agentTrajectoryDetailFixture(),
		deleteAgentTrajectory: async () => ({}),
		loadModelPerformance: async () => modelPerformanceFixture(),
	});

	await buttonByText(root, "查看轨迹").dispatch("click");
	let detail = findAllTags(root, "DIALOG").find((item) => item.open);
	const detailCancel = await detail.dispatch("cancel");
	assert.equal(detailCancel.defaultPrevented, true);
	assert.equal(findAllTags(root, "DIALOG").length, 0);

	await buttonByText(root, "查看轨迹").dispatch("click");
	detail = findAllTags(root, "DIALOG").find((item) => item.open);
	await buttonByText(detail, "删除记录").dispatch("click");
	const confirmation = findAllTags(root, "DIALOG").find((item) => item.open && item !== detail);
	const confirmationCancel = await confirmation.dispatch("cancel");
	assert.equal(confirmationCancel.defaultPrevented, true);
	assert.equal(findAllTags(root, "DIALOG").length, 1);
});

test("model performance filters and page survive reload and flag missing priors", async (t) => {
	const root = installFakeDOM(t);
	const urls = installFakeNavigation(t,
		"?profile_id=7&model=fast&task_type=code&difficulty=hard&risk=normal" +
		"&vision=none&from=2026-08-01T00%3A00%3A00.000Z&page=2&page_size=50");
	const filters = [];
	await renderStatsPage(root, {
		section: "models",
		profiles: [{ id: 7, display_name: "Coding", slug: "coding" }],
		loadModelPerformance: async (current) => {
			filters.push(current);
			const fixture = modelPerformanceFixture();
			fixture.page = Number(current.page);
			fixture.page_size = Number(current.page_size);
			fixture.total_pages = 3;
			fixture.items[0].prior_known = false;
			return fixture;
		},
	});
	assert.deepEqual(filters[0], {
		profile_id: "7", model: "fast", task_type: "code", difficulty: "hard",
		risk: "normal", vision: "none", page: "2", page_size: "50",
		from: "2026-08-01T00:00:00.000Z",
	});
	assert.equal(controlByName(root, "from").value, "2026-08-01");
	assert.ok(findText(root, "策略先验不可用"));
	assert.match(urls.at(-1), /^\/_admin\/stats\/models\?/);
	assert.match(urls.at(-1), /page=2/);
	await buttonByText(root, "下一页").dispatch("click");
	assert.equal(filters.at(-1).page, "3");
	assert.match(urls.at(-1), /page=3/);
});

test("statistics page announces empty and error states and delegates 401 recovery", async (t) => {
  const root = installFakeDOM(t);
  let unauthorized = 0;

  await renderStatsPage(root, {
    profiles: [],
    loadStats: async () => emptyStatsFixture(),
    onUnauthorized: () => {
      unauthorized += 1;
    },
  });
  assert.equal(findRole(root, "status").textContent, "暂无统计数据。");

  await renderStatsPage(root, {
    profiles: [],
    loadStats: async () => {
      throw new Error("统计暂不可用");
    },
    onUnauthorized: () => {
      unauthorized += 1;
    },
  });
  assert.equal(findRole(root, "alert").textContent, "统计暂不可用");

  await renderStatsPage(root, {
    profiles: [],
    loadStats: async () => {
      const error = new Error("expired");
      error.status = 401;
      throw error;
    },
    onUnauthorized: () => {
      unauthorized += 1;
    },
  });
  assert.equal(unauthorized, 1);
});

test("Profile export keeps only versioned portable configuration", () => {
  const exported = buildProfileExport({
    default_profile_id: 1,
    profiles: [
      {
        id: 1,
        slug: "coding",
        display_name: "Coding",
        enabled: true,
        config: {
          version: 1,
          protocol: "anthropic",
          upstream: "https://coding.example",
          vision: {
            enabled: false,
            model: "sonnet",
            max_tokens: 2048,
            timeout: "2m",
            max_concurrency: 4,
            cache_ttl: "30m",
            cache_max_entries: 512,
            prompt: "",
          },
          overload_rules: [],
        },
        usage_30d: {
          requests: 9,
          input_tokens: 10,
          output_tokens: 20,
        },
        session_token: "must-not-export",
      },
    ],
  });

  assert.deepEqual(exported, {
    version: 1,
    profiles: [
      {
        slug: "coding",
        display_name: "Coding",
        enabled: true,
        default: true,
        config: {
          version: 1,
          protocol: "anthropic",
          upstream: "https://coding.example",
          vision: {
            enabled: false,
            model: "sonnet",
            max_tokens: 2048,
            timeout: "2m",
            max_concurrency: 4,
            cache_ttl: "30m",
            cache_max_entries: 512,
            prompt: "",
          },
          overload_rules: [],
        },
      },
    ],
  });
  assert.doesNotMatch(
    JSON.stringify(exported),
    /usage_30d|session_token|"id"/,
  );
});

test("System page renders exact safe values password change and sanitized export", async (t) => {
  const root = installFakeDOM(t);
  const downloads = [];
  const passwordChanges = [];

  await renderSystemPage(root, {
    loadSystem: async () => systemFixture(),
    loadProfiles: async () => ({
      default_profile_id: 7,
      profiles: [
        {
          id: 7,
          slug: "coding",
          display_name: "Coding",
          enabled: true,
          config: { version: 1 },
          usage_30d: { requests: 99 },
        },
      ],
    }),
    changePassword: async (...passwords) => {
      passwordChanges.push(passwords);
    },
    download: (filename, contents) => {
      downloads.push([filename, JSON.parse(contents)]);
    },
    onUnauthorized: () => {},
  });

  for (const value of [
    "版本：v1.2.3",
    "数据目录：/app/data",
    "数据库文件：llm-proxy.db",
    "数据库大小：12345 B",
    "Schema 版本：2",
    "默认 Profile ID：7",
    "必须修改密码：否",
  ]) {
    assert.ok(findText(root, value), value);
  }
  assert.doesNotMatch(
    descendants(root)
      .map((element) => element.textContent)
      .join(" "),
    /dsn|environment|session|username/i,
  );

  assert.deepEqual(
    labelTexts(root).filter((text) => text.includes("密码")),
    ["当前密码", "新密码", "确认新密码"],
  );
  controlByName(root, "current-password").value = "old-password";
  controlByName(root, "new-password").value = "new-password-123";
  controlByName(root, "confirm-password").value = "new-password-123";
  await findTag(root, "FORM").dispatch("submit");
  assert.deepEqual(passwordChanges, [
    ["old-password", "new-password-123"],
  ]);
  assert.equal(controlByName(root, "current-password").value, "");
  assert.equal(controlByName(root, "new-password").value, "");
  assert.equal(controlByName(root, "confirm-password").value, "");

  await buttonByText(root, "导出 Profiles").dispatch("click");
  assert.deepEqual(downloads, [
    [
      "llm-proxy-profiles-v1.json",
      {
        version: 1,
        profiles: [
          {
            slug: "coding",
            display_name: "Coding",
            enabled: true,
            default: true,
            config: { version: 1 },
          },
        ],
      },
    ],
  ]);
});

test("System page exposes loading and error states and delegates 401 recovery", async (t) => {
  const root = installFakeDOM(t);
  let finish;
  const pending = new Promise((resolve) => {
    finish = resolve;
  });
  const rendering = renderSystemPage(root, {
    loadSystem: async () => pending,
    loadProfiles: async () => ({ default_profile_id: 0, profiles: [] }),
    changePassword: async () => {},
    download: () => {},
    onUnauthorized: () => {},
  });
  assert.equal(findRole(root, "status").textContent, "正在加载系统信息…");
  finish(systemFixture());
  await rendering;

  await renderSystemPage(root, {
    loadSystem: async () => {
      throw new Error("系统信息暂不可用");
    },
    loadProfiles: async () => ({ default_profile_id: 0, profiles: [] }),
    changePassword: async () => {},
    download: () => {},
    onUnauthorized: () => {},
  });
  assert.equal(findRole(root, "alert").textContent, "系统信息暂不可用");

  let unauthorized = 0;
  await renderSystemPage(root, {
    loadSystem: async () => {
      const error = new Error("expired");
      error.status = 401;
      throw error;
    },
    loadProfiles: async () => ({ default_profile_id: 0, profiles: [] }),
    changePassword: async () => {},
    download: () => {},
    onUnauthorized: () => {
      unauthorized += 1;
    },
  });
  assert.equal(unauthorized, 1);
});

test("authenticated app routes the statistics path through the injected client", async (t) => {
  const root = installFakeDOM(t);
  const calls = [];

  await bootstrap({
    root,
    path: "/_admin/stats",
    client: {
      session: async () => ({
        username: "admin",
        must_change_password: false,
      }),
      listProfiles: async () => ({
        default_profile_id: 1,
        profiles: [
          { id: 1, display_name: "Coding", slug: "coding" },
        ],
      }),
      stats: async (filters) => {
        calls.push(filters);
        return statsFixture();
      },
    },
  });

  assert.equal(findAllTags(root, "H1")[0].textContent, "统计");
  assert.equal(linkByText(root, "统计").getAttribute("aria-current"), "page");
  assert.deepEqual(calls, [{}]);
  assert.ok(findText(root, "Token 总计：30"));
});

test("authenticated app routes the System path and recovers page-level 401", async (t) => {
  const root = installFakeDOM(t);

  await bootstrap({
    root,
    path: "/_admin/system",
    client: {
      session: async () => ({
        username: "admin",
        must_change_password: false,
      }),
      system: async () => systemFixture(),
      listProfiles: async () => ({
        default_profile_id: 0,
        profiles: [],
      }),
      changePassword: async () => {},
    },
  });

  assert.equal(findAllTags(root, "H1")[0].textContent, "系统");
  assert.equal(linkByText(root, "系统").getAttribute("aria-current"), "page");
  assert.ok(findText(root, "数据库大小：12345 B"));

  await bootstrap({
    root,
    path: "/_admin/stats",
    client: {
      session: async () => ({
        username: "admin",
        must_change_password: false,
      }),
      listProfiles: async () => ({
        default_profile_id: 0,
        profiles: [],
      }),
      stats: async () => {
        const error = new Error("expired");
        error.status = 401;
        throw error;
      },
    },
  });
  assert.ok(buttonByText(root, "登录"));
});

function statsFixture() {
  return {
    summary: {
      key: "total",
      requests: 2,
      input_tokens: 10,
      output_tokens: 20,
      cache_read_tokens: 4,
      cache_creation_tokens: 5,
      total_tokens: 30,
    },
    by_day: [
      {
        key: "2026-07-29",
        requests: 2,
        input_tokens: 10,
        output_tokens: 20,
        cache_read_tokens: 4,
        cache_creation_tokens: 5,
        total_tokens: 30,
      },
    ],
    by_model: [
      {
        key: "sonnet",
        requests: 2,
        input_tokens: 10,
        output_tokens: 20,
        cache_read_tokens: 4,
        cache_creation_tokens: 5,
        total_tokens: 30,
      },
    ],
  };
}

function emptyStatsFixture() {
  return {
    summary: {
      key: "total",
      requests: 0,
      input_tokens: 0,
      output_tokens: 0,
      cache_read_tokens: 0,
      cache_creation_tokens: 0,
      total_tokens: 0,
    },
    by_day: [],
    by_model: [],
  };
}

function routingTraceFixture() {
	return {
		page: 1,
		page_size: 25,
		total: 6,
		total_pages: 3,
		category_counts: { all: 6, normal: 3, high_risk: 1, changed: 2, failed: 1, cost_anomaly: 1 },
		items: [{
    id: 1,
		scope: "session",
		session_ref: "4d4d4d4d4d4d",
		request_count: 4,
		fallback_requests: 1,
		changed_requests: 1,
		failed_requests: 0,
		first_created_at: "2026-07-29T02:20:00Z",
		total_known_actual_cost_micro_usd: 42000,
    created_at: "2026-07-29T02:30:00Z",
    profile_id: 7,
    profile_slug: "coding",
    protocol: "anthropic",
    path: "/v1/messages",
    strategy: "20260802-001",
    route: "balanced",
    task_type: "simple",
		difficulty: "medium",
    risk: "normal",
    classification_source: "rule",
	classification_confidence_bps: 9100,
	task_type_confidence_bps: 9300,
	difficulty_confidence_bps: 9100,
	risk_confidence_bps: 9700,
	classification_underspecified: false,
	complexity_signals: {
		multiple_interacting_constraints: true,
		cross_system_or_layer: true,
	},
    initial_model: "fast",
    final_model: "strong",
    vision_mode: "native",
    status_code: 200,
    client_committed: true,
    answer_attempts: 2,
    auxiliary_calls: 1,
    total_outbound_calls: 3,
    model_switches: 1,
		self_escalations: 1,
		self_escalation_reason: "insufficient_reasoning",
    planned_worst_case_cost_micro_usd: 30126,
    consumed_estimated_cost_micro_usd: 15500,
    held_cost_micro_usd: 0,
    known_actual_cost_micro_usd: 12000,
    all_actual_costs_known: false,
    elapsed_ms: 42,
	  }],
	};
}

function routingTraceDetailFixture() {
	const page = routingTraceFixture();
	return {
		trace: {
			...page.items[0],
			classification_confidence_bps: 9100,
			classification_reason_codes: ["task_analyzer"],
			estimated_input_tokens: 1200,
			requested_output_tokens: 800,
			decision_reason: "lowest expected cost",
		},
		candidates: [{
			model: "fast", decision: "selected", reason_code: "lowest_expected_cost",
			quality_score_bps: 9200, stability_score_bps: 9300, severe_error_rate_bps: 50,
			cost_efficiency_score_bps: 10000, performance_score_bps: 10000,
			routing_score_bps: 9500, expected_latency_ms: 250,
			expected_cost_micro_usd: 400,
		}],
		calls: [{
			sequence: 1, kind: "answer", model: "fast",
			status_code: 200, outcome: "success", estimated_cost_micro_usd: 400,
			actual_cost_known: true, actual_cost_micro_usd: 350,
		}],
	};
}

function routingSessionFlowFixture() {
	const selected = routingTraceDetailFixture();
	selected.trace.classification_source = "session";
	selected.trace.classification_reason_codes = ["session_reuse"];
	return {
		scope: "session",
		session_ref: "4d4d4d4d4d4d",
		selected_trace_id: 1,
		truncated: false,
		requests: [selected],
	};
}

function modelPerformanceFixture() {
	const dimensions = Object.fromEntries([
		"correctness", "completeness", "instruction_following",
		"format_tool_safety", "task_completion",
	].map((dimension) => [dimension, {
		dimension, weight_bps: 2000, raw_samples: 30, effective_samples: 29.5,
		mean_bps: 9500, lower_bps: 9120, reliable: true,
	}]));
	return {
		page: 1, page_size: 25, total: 2, total_pages: 2,
		summary: {
			groups: 2, reliable_groups: 1, raw_samples: 30,
			candidate_cost_micro_usd: 1000, reference_cost_micro_usd: 4000,
			reviewer_cost_micro_usd: 500,
		},
		items: [{
			profile_id: 7, profile_slug: "coding", profile_name: "Coding",
			strategy: "20260802-001", route: "balanced", task_type: "code",
			difficulty: "hard", risk: "normal", vision_mode: "none",
			candidate_model: "fast", reference_model: "strong",
			estimate: {
				reliable: true, raw_samples: 30, effective_samples: 29.5,
				quality_lower_bps: 9120, severe_error_upper_bps: 250,
				candidate_cost_micro_usd: 1000, reference_cost_micro_usd: 4000,
				reviewer_cost_micro_usd: 500, self_escalation_precision_bps: 8000,
				missed_self_escalation_rate_bps: 1000, dimensions,
			},
		}],
	};
}

function evaluationCatalogFixture() {
	return {
		revision: "abc123",
		retrieved_at: "2026-08-07T08:00:00Z",
		sources: [
			{ id: "lmarena", name: "LMArena", version: "2026-08-07" },
			{ id: "swebench", name: "SWE-bench", version: "2026-08-07" },
		],
		results: Array.from({ length: 208 }, (_, index) => ({
			source_id: index % 2 === 0 ? "lmarena" : "swebench",
			model_id: "anthropic/claude-sonnet-4",
			domain: index % 2 === 0 ? "general" : "code",
		})),
	};
}

function agentTrajectoryFixture() {
	return {
		page: 1, page_size: 25, total: 1, total_pages: 1,
		summary: {
			total: 5, collecting: 1, completed: 4, evaluated: 3,
			evidence_recorded: 2, timed_out: 0, interrupted: 0,
			skipped: 1, failed: 0, evaluation_cost_micro_usd: 6200,
		},
		items: [{
			id: 31, profile_id: 7, profile_slug: "Coding", protocol: "anthropic_messages",
			source: "session", status: "evaluated", strategy: "20260807-001",
			route: "balanced", task_type: "code", difficulty: "hard", risk: "normal",
			vision_mode: "none", model_path: ["sonnet-4", "opus-4"], tool_calls: 1,
			turns: 2, elapsed_ms: 1200, evidence_recorded: true,
			candidate_cost_micro_usd: 1200, evaluation_cost_micro_usd: 6200,
			evaluation_result: {
				candidate_model: "sonnet-4", reference_model: "opus-4", reviewer_model: "judge",
				outcome: "candidate_win", severe_error: false, deterministic_failure: false,
				dimensions: {
					correctness: "candidate_win", completeness: "tie",
					instruction_following: "candidate_win", format_tool_safety: "tie",
					task_completion: "candidate_win",
				},
				candidate_cost_micro_usd: 1200, reference_cost_micro_usd: 4800,
				reviewer_cost_micro_usd: 200, candidate_latency_ms: 900, reference_latency_ms: 1400,
			},
			started_at: "2026-08-07T08:00:00Z", completed_at: "2026-08-07T08:00:01Z",
			expires_at: "2026-08-14T08:00:00Z",
		}],
	};
}

function agentTrajectoryDetailFixture() {
	return {
		record: agentTrajectoryFixture().items[0],
		trajectory: {
			events: [
				{ role: "user", kind: "user_message", text: "查找路由文档" },
				{ role: "assistant", kind: "tool_call", call_id: "call-1", tool_name: "search_docs", arguments: { query: "router" } },
				{ role: "tool", kind: "tool_result", call_id: "call-1", result: { matches: 2 } },
				{ role: "assistant", kind: "assistant_text", text: "最终回答" },
			],
			final_text: "最终回答",
		},
	};
}

function systemFixture() {
  return {
    version: "v1.2.3",
    data_dir: "/app/data",
    database_file: "llm-proxy.db",
    database_bytes: 12345,
    schema_version: 2,
    default_profile_id: 7,
    password_must_change: false,
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
    this.id = "";
    this.name = "";
    this.type = "";
    this.value = "";
    this.checked = false;
    this.required = false;
    this.disabled = false;
    this.hidden = false;
	this.open = false;
  }

  set innerHTML(_) {
    throw new Error("innerHTML is forbidden");
  }

  append(...children) {
    for (const child of children) {
      child.parentNode = this;
      this.children.push(child);
    }
  }

  replaceChildren(...children) {
    for (const child of this.children) {
      child.parentNode = null;
    }
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
    const listeners = this.listeners.get(name) ?? [];
    listeners.push(listener);
    this.listeners.set(name, listeners);
  }

  async dispatch(name) {
    const event = {
      target: this,
      currentTarget: this,
      defaultPrevented: false,
      preventDefault() {
        this.defaultPrevented = true;
      },
    };
    for (const listener of this.listeners.get(name) ?? []) {
      await listener(event);
    }
    return event;
  }

	showModal() {
		this.open = true;
	}

	close() {
		this.open = false;
	}

	remove() {
		if (!this.parentNode) return;
		this.parentNode.children = this.parentNode.children.filter((child) => child !== this);
		this.parentNode = null;
	}
}

function installFakeDOM(t) {
  const root = new FakeElement("main");
  const originalDocument = globalThis.document;
  const originalLocalStorage = globalThis.localStorage;
  const originalSessionStorage = globalThis.sessionStorage;
  globalThis.document = {
    createElement: (name) => new FakeElement(name),
  };
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    get() {
      throw new Error("localStorage access is forbidden");
    },
  });
  Object.defineProperty(globalThis, "sessionStorage", {
    configurable: true,
    get() {
      throw new Error("sessionStorage access is forbidden");
    },
  });
  t.after(() => {
    globalThis.document = originalDocument;
    restoreGlobal("localStorage", originalLocalStorage);
    restoreGlobal("sessionStorage", originalSessionStorage);
  });
  return root;
}

function installFakeNavigation(t, search) {
	const originalLocation = globalThis.location;
	const originalHistory = globalThis.history;
	const urls = [];
	Object.defineProperty(globalThis, "location", {
		configurable: true,
		value: { search },
	});
	Object.defineProperty(globalThis, "history", {
		configurable: true,
		value: { replaceState: (_state, _title, url) => urls.push(url) },
	});
	t.after(() => {
		restoreGlobal("location", originalLocation);
		restoreGlobal("history", originalHistory);
	});
	return urls;
}

function restoreGlobal(name, value) {
  if (value === undefined) {
    delete globalThis[name];
    return;
  }
  Object.defineProperty(globalThis, name, {
    configurable: true,
    value,
  });
}

function descendants(root) {
  return [root, ...root.children.flatMap(descendants)];
}

function findAllTags(root, tagName) {
  return descendants(root).filter(
    (element) => element.tagName === tagName.toUpperCase(),
  );
}

function findTag(root, tagName) {
  const result = findAllTags(root, tagName)[0];
  assert.ok(result, `${tagName} not found`);
  return result;
}

function findText(root, text) {
  const result = descendants(root).find((element) =>
    element.textContent.includes(text),
  );
  assert.ok(result, `text ${text} not found`);
  return result;
}

function findRole(root, role) {
  const result = descendants(root).find(
    (element) => element.getAttribute("role") === role,
  );
  assert.ok(result, `role ${role} not found`);
  return result;
}

function controls(root) {
  return descendants(root).filter((element) =>
    ["INPUT", "SELECT", "TEXTAREA"].includes(element.tagName),
  );
}

function controlByName(root, name) {
  const result = controls(root).find((element) => element.name === name);
  assert.ok(result, `control ${name} not found`);
  return result;
}

function labelTexts(root) {
  return findAllTags(root, "LABEL").map((label) => label.textContent);
}

function buttonByText(root, text) {
  const result = findAllTags(root, "BUTTON").find(
    (button) => button.textContent === text,
  );
  assert.ok(result, `button ${text} not found`);
  return result;
}

function linkByText(root, text) {
  const result = findAllTags(root, "A").find(
    (link) => link.textContent === text,
  );
  assert.ok(result, `link ${text} not found`);
  return result;
}
