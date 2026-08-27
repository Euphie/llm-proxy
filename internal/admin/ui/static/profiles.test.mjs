import assert from "node:assert/strict";
import test from "node:test";

import { api } from "./api.js";
import { bootstrap } from "./app.js";
import {
  currentModelCatalog,
  installModelCatalog,
  MODELS_DEV_SOURCE,
} from "./model-catalog.js";
import {
  addModelCapability,
  addRetryRule,
  defaultProfileDraft,
  moveRetryRule,
  profileDraft,
  profilePayload,
  removeModelCapability,
  removeRetryRule,
  renderProfileEditor,
  renderProfileList,
} from "./profiles.js";

test("new Anthropic Profile contains explicit defaults", () => {
  const draft = defaultProfileDraft("anthropic");
  assert.equal(draft.config.version, 2);
  assert.equal(draft.config.protocol, "anthropic");
  assert.equal(draft.config.vision.model, "sonnet");
  assert.equal(draft.config.vision.timeout, "2m");
  assert.equal(draft.config.vision.unlisted_model_policy, "bypass");
  assert.deepEqual(draft.config.models, []);
  assert.equal(draft.config.auto_routing.enabled, false);
  assert.equal(draft.config.auto_routing.analyzer_timeout, "15s");
  assert.match(draft.config.auto_routing.strategy.name, /^\d{8}-001$/);
  assert.equal(draft.config.auto_routing.strategy.default_route, "default");
  assert.deepEqual(draft.config.auto_routing.strategy.routes, [{
    id: "default",
    min_quality_bps: 9000,
		min_stability_bps: 8000,
    max_severe_error_rate_bps: 100,
		weights: {
			quality_bps: 4000, stability_bps: 2500,
			cost_bps: 2500, performance_bps: 1000,
		},
    candidates: [],
  }]);
	assert.equal(draft.config.auto_routing.session_lock_token_threshold, 100000);
	assert.equal(Object.hasOwn(draft.config.auto_routing, "risk_policy"), false);
	assert.equal(
		draft.config.auto_routing.dynamic_optimization.enabled,
		false,
	);
  assert.deepEqual(draft.config.overload_rules, []);
});

test("legacy backup upstream fields are omitted from editable drafts and save payloads", () => {
  const draft = profileDraft(profileFixture({
    config: {
      ...profileFixture().config,
      provider_id: "acme-ai",
      credential_scope: "team-a",
      targets: [{
        id: "backup",
        upstream: "https://backup.example",
        provider_id: "acme-ai",
        credential_scope: "team-a",
        models: ["strong"],
      }],
    },
  }));

  for (const field of ["provider_id", "credential_scope", "targets"]) {
    assert.equal(Object.hasOwn(draft.config, field), false);
    assert.equal(Object.hasOwn(profilePayload(draft).config, field), false);
  }
});

test("legacy target-switch budget is omitted from drafts and save payloads", () => {
  const source = defaultProfileDraft();
  source.config.auto_routing.strategy.budget.max_target_switches = 4;
  const draft = profileDraft(profileFixture({ config: source.config }));

  assert.equal(
    Object.hasOwn(draft.config.auto_routing.strategy.budget, "max_target_switches"),
    false,
  );
  assert.equal(
    Object.hasOwn(
      profilePayload(draft).config.auto_routing.strategy.budget,
      "max_target_switches",
    ),
    false,
  );
});

test("legacy disabled Auto budgets reopen with safe editable defaults", () => {
  const draft = profileDraft(profileFixture({
    config: {
      version: 1,
      protocol: "anthropic",
      upstream: "https://profile.example",
      auto_routing: {
        enabled: false,
        strategy: {
          name: "",
          budget: {
            max_answer_attempts: 0,
            max_auxiliary_calls: 0,
            max_total_outbound_calls: 0,
            deadline: "",
          },
        },
      },
      vision: profileFixture().config.vision,
      overload_rules: [],
    },
  }));

  assert.match(draft.config.auto_routing.strategy.name, /^\d{8}-001$/);
  assert.equal(draft.config.auto_routing.strategy.budget.max_answer_attempts, 2);
  assert.equal(draft.config.auto_routing.strategy.budget.max_auxiliary_calls, 2);
  assert.equal(draft.config.auto_routing.strategy.budget.max_total_outbound_calls, 5);
  assert.equal(draft.config.auto_routing.strategy.budget.deadline, "2m");
  assert.equal(draft.config.auto_routing.strategy.default_route, "default");
  assert.equal(draft.config.auto_routing.strategy.routes[0].id, "default");
});

test("routing budget controls never render legacy zero values", (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft();
  Object.assign(draft.config.auto_routing.strategy.budget, {
    max_answer_attempts: 0,
    max_auxiliary_calls: 0,
    max_total_outbound_calls: 0,
    deadline: "",
  });

  renderProfileEditor(root, draft, {
    save: async () => {},
    cancel: () => {},
    generate: () => {},
  });

  assert.equal(controlByName(root, "auto-budget-answer").value, "2");
  assert.equal(controlByName(root, "auto-budget-auxiliary").value, "2");
  assert.equal(controlByName(root, "auto-budget-total").value, "5");
  assert.equal(controlByName(root, "auto-budget-deadline").value, "2m");
  assert.equal(controlByName(root, "auto_default_route").value, "default");
  assert.equal(controlByName(root, "auto-route-0-id").value, "default");
});

test("saved Profile model capabilities and unlisted policy remain authoritative in its draft", () => {
  const draft = profileDraft(profileFixture({
    config: {
      version: 1,
      protocol: "anthropic",
      upstream: "https://profile.example",
      models: [
        {
          id: "GLM-5",
          context_window: 204800,
          max_output_tokens: 131072,
          supports_vision: false,
        },
        {
          id: "vision-only",
          supports_vision: true,
        },
      ],
      vision: {
        enabled: true,
        model: "vision-only",
        unlisted_model_policy: "enhance",
        max_tokens: 2048,
        timeout: "2m",
        max_concurrency: 4,
        cache_ttl: "30m",
        cache_max_entries: 512,
        prompt: "",
      },
      overload_rules: [],
    },
  }));

  assert.deepEqual(draft.config.models, [
    {
      id: "GLM-5",
      context_window: 204800,
      max_output_tokens: 131072,
      supports_vision: false,
    },
    {
      id: "vision-only",
      supports_vision: true,
    },
  ]);
  assert.equal(draft.config.vision.unlisted_model_policy, "enhance");
});

test("saved Session lock threshold round-trips", () => {
	const draft = profileDraft(profileFixture({
		config: {
			...profileFixture().config,
			auto_routing: {
				...defaultProfileDraft().config.auto_routing,
				enabled: false,
				session_lock_token_threshold: 250000,
			},
		},
	}));
	assert.equal(draft.config.auto_routing.session_lock_token_threshold, 250000);
	assert.equal(profilePayload(draft).config.auto_routing.session_lock_token_threshold, 250000);
});

test("disabled Auto routing persists the explicit Session lock threshold", () => {
	const draft = defaultProfileDraft();
	draft.config.auto_routing.enabled = false;
	draft.config.auto_routing.session_lock_token_threshold = 250000;
	const first = profilePayload(draft).config.auto_routing;
	assert.equal(first.enabled, false);
	assert.equal(first.session_lock_token_threshold, 250000);
	assert.equal(Object.hasOwn(first, "risk_policy"), false);
	assert.deepEqual(first.dynamic_optimization, {
		enabled: false,
		auto_update_policy: false,
		sample_rate_bps: 1000,
		daily_budget_micro_usd: 250000,
		reviewer_model: "",
		max_concurrency: 2,
		queue_capacity: 128,
		task_timeout: "90s",
	});

	const reloaded = profileDraft(profileFixture({
		config: {
			...profileFixture().config,
			auto_routing: structuredClone(first),
		},
	}));
	assert.deepEqual(profilePayload(reloaded).config.auto_routing, first);
});

test("model capability helpers append an explicit row and remove without sorting", () => {
  const first = {
    id: "first",
    context_window: 128000,
    supports_vision: false,
  };
  const second = {
    id: "second",
    max_output_tokens: 64000,
    supports_vision: true,
  };

  assert.deepEqual(addModelCapability([first, second]), [
    first,
    second,
    {
      id: "",
      context_window: "",
      max_output_tokens: "",
      supports_vision: "",
      supports_tools: "",
      supports_agent_workflow: "",
      supports_structured_output: "",
      input_price_micro_usd_per_million: "",
      output_price_micro_usd_per_million: "",
      cache_read_price_micro_usd_per_million: "",
      cache_write_price_micro_usd_per_million: "",
    },
  ]);
  assert.deepEqual(removeModelCapability([first, second], 0), [second]);
});

test("payload preserves optional routing capabilities and integer micro-USD prices", () => {
  const draft = defaultProfileDraft();
  draft.config.models = [{
    id: "priced-model",
    context_window: "128K",
    max_output_tokens: "16K",
    supports_vision: false,
    supports_tools: true,
    supports_structured_output: false,
    input_price_micro_usd_per_million: "100000",
    output_price_micro_usd_per_million: "400000",
  }];

  assert.deepEqual(profilePayload(draft).config.models, [{
    id: "priced-model",
    context_window: 128000,
    max_output_tokens: 16000,
    supports_vision: false,
    supports_tools: true,
    supports_structured_output: false,
    input_price_micro_usd_per_million: 100000,
    output_price_micro_usd_per_million: 400000,
  }]);
});

test("payload serializes a complete Profile-local Auto strategy", () => {
  const draft = defaultProfileDraft();
  draft.config.models = [
    {
      id: "fast",
      context_window: 128000,
      max_output_tokens: 16000,
      supports_vision: false,
      supports_tools: true,
      input_price_micro_usd_per_million: 100000,
      output_price_micro_usd_per_million: 400000,
    },
    {
      id: "strong",
      context_window: 128000,
      max_output_tokens: 16000,
      supports_vision: true,
      input_price_micro_usd_per_million: 3000000,
      output_price_micro_usd_per_million: 15000000,
    },
  ];
  draft.config.auto_routing = {
    enabled: true,
    participants: ["fast", "strong"],
    strong_baseline_model: "strong",
    task_analyzer_model: "fast",
    analyzer_timeout: "5s",
    analyzer_min_confidence_bps: "7000",
    session_ttl: "24h",
		self_escalation: { enabled: true },
		dynamic_optimization: {
			enabled: true,
			sample_rate_bps: "1250",
			daily_budget_micro_usd: "250000",
			reviewer_model: "strong",
			max_concurrency: "2",
			queue_capacity: "128",
			task_timeout: "90s",
		},
    strategy: {
      name: "20260802-001",
      alias: "均衡",
      default_route: "balanced",
      task_routes: [{ task_type: "simple", route: "balanced" }],
      routes: [{
        id: "balanced",
        min_quality_bps: "9000",
				min_stability_bps: "8000",
        max_severe_error_rate_bps: "100",
				weights: {
					quality_bps: "4000", stability_bps: "2500",
					cost_bps: "2500", performance_bps: "1000",
				},
        candidates: [{
          model: "fast",
          quality_score_bps: "9200",
					stability_score_bps: "9300",
          severe_error_rate_bps: "50",
					expected_latency_ms: "250",
        }],
      }],
      budget: {
        max_answer_attempts: "2",
        max_auxiliary_calls: "2",
        max_total_outbound_calls: "5",
        max_retries_per_target: "1",
        max_model_switches: "1",
        deadline: "2m",
        max_worst_case_cost_micro_usd: "500000",
      },
    },
  };

  const auto = profilePayload(draft).config.auto_routing;
  assert.equal(auto.enabled, true);
  assert.deepEqual(auto.participants, ["fast", "strong"]);
  assert.equal(auto.analyzer_min_confidence_bps, 7000);
  assert.equal(auto.session_ttl, "24h");
	assert.deepEqual(auto.self_escalation, { enabled: true });
	assert.deepEqual(auto.dynamic_optimization, {
		enabled: true,
		auto_update_policy: false,
		sample_rate_bps: 1250,
		daily_budget_micro_usd: 250000,
		reviewer_model: "strong",
		max_concurrency: 2,
		queue_capacity: 128,
		task_timeout: "90s",
	});
  assert.equal(auto.strategy.routes[0].min_quality_bps, 9000);
	assert.equal(auto.strategy.routes[0].min_stability_bps, 8000);
	assert.deepEqual(auto.strategy.routes[0].weights, {
		quality_bps: 4000, stability_bps: 2500,
		cost_bps: 2500, performance_bps: 1000,
	});
  assert.equal(auto.strategy.routes[0].candidates[0].quality_score_bps, 9200);
	assert.equal(auto.strategy.routes[0].candidates[0].stability_score_bps, 9300);
	assert.equal(auto.strategy.routes[0].candidates[0].expected_latency_ms, 250);
  assert.equal(auto.strategy.budget.max_worst_case_cost_micro_usd, 500000);
});

test("payload parses decimal K/M model limits and omits blank optional limits", () => {
  const draft = defaultProfileDraft();
  draft.config.models = [
    {
      id: "compact",
      context_window: "128K",
      max_output_tokens: "",
      supports_vision: false,
    },
    {
      id: "large",
      context_window: "1.5M",
      max_output_tokens: "262144",
      supports_vision: true,
    },
  ];

  const payload = profilePayload(draft);
  assert.deepEqual(payload.config.models, [
    {
      id: "compact",
      context_window: 128000,
      supports_vision: false,
    },
    {
      id: "large",
      context_window: 1500000,
      max_output_tokens: 262144,
      supports_vision: true,
    },
  ]);
  assert.equal(payload.config.vision.unlisted_model_policy, "bypass");
});

test("payload rejects invalid model rows while keeping exact IDs case-sensitive", () => {
  const modelDraft = (models) => {
    const draft = defaultProfileDraft();
    draft.config.models = models;
    return draft;
  };

  assert.throws(
    () => profilePayload(modelDraft([
      { id: "", supports_vision: false },
    ])),
    /模型 ID 不能为空/,
  );
  assert.throws(
    () => profilePayload(modelDraft([
      { id: "same", supports_vision: false },
      { id: "same", supports_vision: true },
    ])),
    /模型 ID 不能重复/,
  );
  assert.throws(
    () => profilePayload(modelDraft([
      { id: "missing-choice", supports_vision: "" },
    ])),
    /是否支持视觉/,
  );
  assert.throws(
    () => profilePayload(modelDraft([
      {
        id: "invalid-limits",
        context_window: "128K",
        max_output_tokens: "128K",
        supports_vision: false,
      },
    ])),
    /最大输出 Token 必须小于上下文窗口/,
  );

  const payload = profilePayload(modelDraft([
    { id: "GLM-5", supports_vision: false },
    { id: "glm-5", supports_vision: false },
  ]));
  assert.deepEqual(payload.config.models.map(({ id }) => id), [
    "GLM-5",
    "glm-5",
  ]);
});

test("OpenAI draft preserves vision and defaults to Chat Completions", () => {
  const draft = defaultProfileDraft("openai");
  draft.config.vision.enabled = true;
  const payload = profilePayload({
    ...draft,
    slug: "openai",
    display_name: "OpenAI",
    upstream: "https://example.test",
  });
  assert.equal(payload.config.protocol, "openai");
  assert.equal(payload.config.upstream, "https://example.test");
  assert.equal(payload.config.vision.enabled, true);
  assert.equal(payload.config.vision.transport, "openai_chat_completions");
});

test("payload preserves every configured field with numeric JSON types and retry order", () => {
  const draft = defaultProfileDraft("anthropic");
  draft.slug = "coding";
  draft.display_name = "Coding";
  draft.enabled = false;
  draft.make_default = true;
  draft.config.version = "1";
  draft.config.upstream = "https://upstream.example";
  draft.config.vision = {
    enabled: true,
    model: "vision-model",
    max_tokens: "4096",
    timeout: "75s",
    max_concurrency: "6",
    cache_ttl: "45m",
    cache_max_entries: "123",
    prompt: "Describe exactly.",
  };
  draft.config.overload_rules = [
    {
      status: "529",
      body_contains: "first",
      max_retries: "5",
      delay: "2s",
      jitter: "250ms",
    },
    {
      status: "429",
      body_contains: "second",
      max_retries: "3",
      delay: "1s",
      jitter: "100ms",
    },
  ];

  assert.deepEqual(profilePayload(draft), {
    slug: "coding",
    display_name: "Coding",
    enabled: false,
    make_default: true,
    config: {
      version: 1,
      protocol: "anthropic",
      upstream: "https://upstream.example",
      models: [],
      auto_routing: {
        ...structuredClone(draft.config.auto_routing),
        strategy: {
          ...structuredClone(draft.config.auto_routing.strategy),
          roles: {
            participants: [],
            strong_baseline_model: "",
            task_analyzer_model: "",
            reviewer_model: "",
          },
        },
      },
      vision: {
        enabled: true,
        transport: "anthropic_messages",
        model: "vision-model",
        unlisted_model_policy: "bypass",
        max_tokens: 4096,
        timeout: "75s",
        max_concurrency: 6,
        cache_ttl: "45m",
        cache_max_entries: 123,
        prompt: "Describe exactly.",
      },
      overload_rules: [
        {
          status: 529,
          body_contains: "first",
          max_retries: 5,
          delay: "2s",
          jitter: "250ms",
        },
        {
          status: 429,
          body_contains: "second",
          max_retries: 3,
          delay: "1s",
          jitter: "100ms",
        },
      ],
    },
  });
});

test("payload rejects hard-failure statuses as overload rules", () => {
  for (const status of [400, 401, 403, 404, 409, 422]) {
    const draft = defaultProfileDraft("anthropic");
    draft.config.overload_rules = [{
      status,
      body_contains: "",
      max_retries: 1,
      delay: "0s",
      jitter: "0s",
    }];
    assert.throws(
      () => profilePayload(draft),
      /408.*425.*429.*500.*599/,
    );
  }
});

test("retry helpers add remove and move without implicit sorting", () => {
  const first = {
    status: 503,
    body_contains: "first",
    max_retries: 1,
    delay: "1s",
    jitter: "0s",
  };
  const second = {
    status: 429,
    body_contains: "second",
    max_retries: 2,
    delay: "2s",
    jitter: "0s",
  };

  const added = addRetryRule([first, second]);
  assert.deepEqual(added.slice(0, 2), [first, second]);
  assert.equal(added.length, 3);

  assert.deepEqual(removeRetryRule([first, second], 0), [second]);
  assert.deepEqual(moveRetryRule([first, second], 1, -1), [second, first]);
  assert.deepEqual(moveRetryRule([first, second], 0, 1), [second, first]);
  assert.deepEqual(moveRetryRule([first, second], 0, -1), [first, second]);
});

test("Profile API methods use exact routes, methods, JSON, and CSRF", async (t) => {
  const calls = [];
  const originalDocument = globalThis.document;
  const originalFetch = globalThis.fetch;
  globalThis.document = { cookie: "llm_proxy_csrf=csrf-token" };
  globalThis.fetch = async (requestPath, options) => {
    calls.push({
      path: requestPath,
      method: options.method,
      body: options.body === undefined ? undefined : JSON.parse(options.body),
      csrf: options.headers.get("X-CSRF-Token"),
    });
    return new Response("{}", {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  };
  t.after(() => {
    globalThis.document = originalDocument;
    globalThis.fetch = originalFetch;
  });

  const profile = { slug: "coding" };
  const copy = { slug: "coding-copy", display_name: "Coding Copy" };
  await api.listProfiles();
  await api.getProfile(7);
  await api.createProfile(profile);
  await api.updateProfile(7, profile);
  await api.copyProfile(7, copy);
  await api.deleteProfile(7);
  await api.deleteProfile(7, 12);
  await api.setDefaultProfile(12);
  await api.routingPolicy(7);
  await api.applyRoutingPolicy(7, 4, { name: "policy-2" }, "replace models");
  await api.generateRoutingPolicy(7, { objective: "balanced" });
  await api.rollbackRoutingPolicy(7, 5, 9, "restore known-good policy");
  await api.profileModels(7);
  await api.addProfileModel(7, 6, "glm", { id: "glm" }, "add glm");
  await api.updateProfileModel(7, 7, "glm", { id: "glm", supports_tools: true }, "update glm");
  await api.offlineProfileModel(7, { expected_runtime_revision: 8, model_id: "glm", reason: "incident" });
  await api.restoreProfileModel(7, 9, "glm", "incident resolved");
  await api.retireProfileModel(7, 10, "glm", "retire glm");

  assert.deepEqual(calls, [
    {
      path: "/_admin/api/profiles",
      method: "GET",
      body: undefined,
      csrf: null,
    },
    {
      path: "/_admin/api/profiles/7",
      method: "GET",
      body: undefined,
      csrf: null,
    },
    {
      path: "/_admin/api/profiles",
      method: "POST",
      body: profile,
      csrf: "csrf-token",
    },
    {
      path: "/_admin/api/profiles/7",
      method: "PUT",
      body: profile,
      csrf: "csrf-token",
    },
    {
      path: "/_admin/api/profiles/7/copy",
      method: "POST",
      body: copy,
      csrf: "csrf-token",
    },
    {
      path: "/_admin/api/profiles/7",
      method: "DELETE",
      body: {},
      csrf: "csrf-token",
    },
    {
      path: "/_admin/api/profiles/7?replacement_default_id=12",
      method: "DELETE",
      body: {},
      csrf: "csrf-token",
    },
    {
      path: "/_admin/api/default-profile",
      method: "PUT",
      body: { profile_id: 12 },
      csrf: "csrf-token",
    },
    {
      path: "/_admin/api/profiles/7/routing-policy",
      method: "GET",
      body: undefined,
      csrf: null,
    },
    {
      path: "/_admin/api/profiles/7/routing-policy",
      method: "PUT",
      body: { expected_runtime_revision: 4, policy: { name: "policy-2" }, change_reason: "replace models" },
      csrf: "csrf-token",
    },
    {
      path: "/_admin/api/profiles/7/routing-policy/generate",
      method: "POST",
      body: { objective: "balanced" },
      csrf: "csrf-token",
    },
    {
      path: "/_admin/api/profiles/7/routing-policy/rollback",
      method: "POST",
      body: { expected_runtime_revision: 5, version_id: 9, change_reason: "restore known-good policy" },
      csrf: "csrf-token",
    },
    {
      path: "/_admin/api/profiles/7/models",
      method: "GET",
      body: undefined,
      csrf: null,
    },
    {
      path: "/_admin/api/profiles/7/models",
      method: "POST",
      body: { expected_runtime_revision: 6, model_id: "glm", capability: { id: "glm" }, reason: "add glm" },
      csrf: "csrf-token",
    },
    {
      path: "/_admin/api/profiles/7/models/update",
      method: "PUT",
      body: { expected_runtime_revision: 7, model_id: "glm", capability: { id: "glm", supports_tools: true }, reason: "update glm" },
      csrf: "csrf-token",
    },
    {
      path: "/_admin/api/profiles/7/models/offline",
      method: "POST",
      body: { expected_runtime_revision: 8, model_id: "glm", reason: "incident" },
      csrf: "csrf-token",
    },
    {
      path: "/_admin/api/profiles/7/models/restore",
      method: "POST",
      body: { expected_runtime_revision: 9, model_id: "glm", reason: "incident resolved" },
      csrf: "csrf-token",
    },
    {
      path: "/_admin/api/profiles/7/models/retire",
      method: "POST",
      body: { expected_runtime_revision: 10, model_id: "glm", reason: "retire glm" },
      csrf: "csrf-token",
    },
  ]);
});

test("empty Profile list exposes only the exact first-create action", async (t) => {
  const root = installFakeDOM(t);
  let creates = 0;

  renderProfileList(
    root,
    { default_profile_id: 0, profiles: [] },
    {
      create: () => {
        creates += 1;
      },
    },
  );

  assert.ok(findText(root, "代理请求仍不可用"));
  assert.deepEqual(buttonTexts(root), ["创建第一个 Profile"]);
  const create = buttonByText(root, "创建第一个 Profile");
  assert.ok(create.className.includes("button"));
  await create.dispatch("click");
  assert.equal(creates, 1);
});

test("Profile list shows identity badges usage and usable action hooks without interpreting names as markup", async (t) => {
  const root = installFakeDOM(t);
  const data = profileListFixture();
  const calls = [];

  renderProfileList(root, data, {
    edit: (profile) => calls.push(["edit", profile.id]),
    generate: (profile) => calls.push(["generate", profile.id]),
    copy: async (profile, body) =>
      calls.push(["copy", profile.id, body]),
    setDefault: async (profile) => calls.push(["default", profile.id]),
    toggle: async (profile) => calls.push(["toggle", profile.id]),
    delete: async (profile, replacement) =>
      calls.push(["delete", profile.id, replacement]),
  });

  assert.ok(findText(root, "Primary"));
  assert.ok(findText(root, "primary"));
  assert.ok(findText(root, "默认"));
  assert.ok(findText(root, "anthropic"));
  assert.ok(findText(root, "https://primary.example"));
  assert.ok(findText(root, "近 30 天请求：7"));
  assert.ok(findText(root, "Token：150"));
  assert.ok(findText(root, "已停用"));
  assert.ok(findText(root, "视觉增强"));
  assert.ok(findText(root, "<img src=x onerror=alert(1)>"));
  assert.equal(sectionHeadings(root).includes("Profiles"), false);
  assert.equal(findAllTags(root, "IMG").length, 0);

  const rows = elementsByClass(root, "profile-settings-row");
  const icons = elementsByClass(root, "profile-icon");
  assert.equal(rows.length, 3);
  assert.equal(icons.length, 3);
  assert.equal(icons[0].textContent, "P");
  assert.match(
    icons[0].className,
    /profile-icon-(blue|cyan|green|orange|red|yellow)/,
  );

  const primary = profileCard(root, "primary");
  const secondary = profileCard(root, "secondary");
  const disabled = profileCard(root, "disabled");
  assert.equal(buttonByText(primary, "设为默认").disabled, true);
  assert.equal(buttonByText(primary, "停用").disabled, true);
  assert.equal(buttonByText(disabled, "设为默认").disabled, true);

  await buttonByText(primary, "编辑").dispatch("click");
  await buttonByText(primary, "生成配置").dispatch("click");
  await buttonByText(secondary, "设为默认").dispatch("click");
  await buttonByText(secondary, "停用").dispatch("click");
  await buttonByText(disabled, "启用").dispatch("click");

  assert.deepEqual(calls, [
    ["edit", 1],
    ["generate", 1],
    ["default", 2],
    ["toggle", 2],
    ["toggle", 3],
  ]);
});

test("Profile generator opens as a dialog and returns to the unchanged list", async (t) => {
  const root = installFakeDOM(t);
  const data = profileListFixture();
  renderProfileList(root, data, {
    edit: () => {},
    generate: () => {},
    copy: async () => {},
    setDefault: async () => {},
    toggle: async () => {},
    delete: async () => {},
  });

  await buttonByText(profileCard(root, "primary"), "生成配置").dispatch(
    "click",
  );
  const dialog = openDialog(root);
  assert.ok(findText(dialog, "全局 ~/.claude/settings.json"));

  await buttonByText(dialog, "返回 Profiles").dispatch("click");
  assert.equal(findAllTags(root, "DIALOG").length, 0);
  assert.ok(profileCard(root, "primary"));
});

test("editor renders exact accessible labels and sections and submits every configured field", async (t) => {
  const root = installFakeDOM(t);
  const data = profileListFixture();
  const draft = profileDraft(data.profiles[0], data.default_profile_id);
  const saves = [];
  const generated = [];

  renderProfileEditor(root, draft, {
    save: async (payload) => saves.push(payload),
    cancel: () => {},
    generate: (current) => generated.push(current.slug),
  });

  assert.deepEqual(sectionHeadings(root), [
    "基础配置",
    "模型能力",
    "智能路由",
    "视觉增强",
    "容错规则",
    "配置生成",
  ]);
  assert.ok(
    findText(
      root,
      "选填。用于判断模型是否需要视觉增强，并为 Agent 生成上下文与自动压缩配置；不添加时不影响请求转发。",
    ),
  );
  assert.equal(
    descendants(root).some((element) =>
      element.textContent.includes("Token 上限支持整数或十进制 K/M 简写")
    ),
    false,
  );
  for (const exactLabel of [
    "名称",
    "Slug",
    "协议",
    "Upstream",
    "设为默认 Profile",
  ]) {
    assert.ok(labelTexts(root).includes(exactLabel), exactLabel);
  }
  assert.equal(buttonByText(root, "保存 Profile").type, "submit");
  assert.ok(findAllTags(root, "DETAILS").length > 0);
  assert.equal(controlByName(root, "vision_max_tokens").type, "number");
  assert.equal(controlByName(root, "vision_max_concurrency").type, "number");
  assert.equal(controlByName(root, "vision_cache_max_entries").type, "number");
  assert.ok(labelTexts(root).includes("识图模型"));
  assert.equal(
    controlByName(root, "vision_unlisted_model_policy").value,
    "bypass",
  );
  assert.equal(controlByName(root, "enabled").checked, true);
  assert.equal(controlByName(root, "enabled").disabled, true);
  assert.equal(controlByName(root, "make_default").checked, true);
  assert.equal(controlByName(root, "make_default").disabled, true);
  assert.equal(controlByName(root, "vision_prompt").value, "Original prompt");
  assert.match(
    fieldDescription(root, controlByName(root, "slug")),
    /Profile URL/,
  );
  assert.match(
    fieldDescription(root, controlByName(root, "upstream")),
    /请求转发/,
  );
  assert.match(
    fieldDescription(root, controlByName(root, "vision_model")),
    /影子识图请求/,
  );
  assert.match(
    fieldDescription(root, controlByName(root, "retry-0-delay")),
    /等待时间/,
  );
  assert.match(
    fieldDescription(root, controlByName(root, "retry-0-status")),
    /408.*425.*429.*500.*599/,
  );
  assert.equal(
    controls(root).some(
      (control) =>
        control.type === "password" ||
        /api.?key|authorization|secret/i.test(control.name),
    ),
    false,
  );

  const slug = controlByName(root, "slug");
  slug.value = "primary-renamed";
  await slug.dispatch("input");
  const warning = findText(root, "修改 slug 会改变 Agent 使用的 Profile URL。");
  assert.equal(warning.hidden, false);

  controlByName(root, "vision_max_tokens").value = "8192";
  controlByName(root, "retry-0-status").value = "529";
  await findTag(root, "FORM").dispatch("submit");

  assert.equal(saves.length, 1);
  assert.equal(saves[0].slug, "primary-renamed");
  assert.equal(saves[0].config.vision.max_tokens, 8192);
  assert.equal(
    saves[0].config.vision.unlisted_model_policy,
    "bypass",
  );
  assert.equal(saves[0].config.vision.prompt, "Original prompt");
  assert.equal(saves[0].config.overload_rules[0].status, 529);
  assert.equal(typeof saves[0].config.overload_rules[0].max_retries, "number");

  await buttonByText(root, "生成配置").dispatch("click");
  assert.deepEqual(generated, ["primary-renamed"]);
});

test("vision model suggests recorded models while accepting a manual model ID", async (t) => {
  const root = installFakeDOM(t);
  const data = profileListFixture();
  const draft = profileDraft(data.profiles[0], data.default_profile_id);
  draft.config.models = [
    { id: "vision-fast", supports_vision: true },
    { id: "vision-strong", supports_vision: true },
  ];
  const saves = [];

  renderProfileEditor(root, draft, {
    save: async (payload) => saves.push(payload),
    cancel: () => {},
    generate: () => {},
  });

  const visionModel = controlByName(root, "vision_model");
  const listID = visionModel.getAttribute("list");
  const suggestions = descendants(root).find((element) => element.id === listID);
  assert.equal(visionModel.tagName, "INPUT");
  assert.ok(listID);
  assert.equal(suggestions?.tagName, "DATALIST");
  assert.deepEqual(
    suggestions.children.map((option) => option.value),
    ["vision-fast", "vision-strong"],
  );
  assert.match(fieldDescription(root, visionModel), /也可直接输入/);

  const firstModelID = controlByName(root, "model-0-id");
  firstModelID.value = "vision-renamed";
  await firstModelID.dispatch("input");
  assert.deepEqual(
    suggestions.children.map((option) => option.value),
    ["vision-renamed", "vision-strong"],
  );

  visionModel.value = "manual-vision-model";
  await findTag(root, "FORM").dispatch("submit");

  assert.equal(saves.length, 1);
  assert.equal(saves[0].config.vision.model, "manual-vision-model");
});

test("model editor rows add and remove in Profile order with explicit token and vision controls", async (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft();
  draft.config.models = [
    {
      id: "manual-first",
      context_window: 900000,
      max_output_tokens: 90000,
      supports_vision: false,
    },
    {
      id: "manual-second",
      supports_vision: true,
    },
  ];

  renderProfileEditor(root, draft, {
    save: async () => {},
    cancel: () => {},
    generate: () => {},
  });

  assert.deepEqual(modelIDs(root), ["manual-first", "manual-second"]);
  for (const row of modelRows(root)) {
    assert.match(
      fieldDescription(root, controlByField(row, "id")),
      /粘贴/,
    );
    assert.equal(
      controlByField(row, "context_window").getAttribute("placeholder"),
      "例如 128K、256K、1M",
    );
    assert.equal(
      controlByField(row, "max_output_tokens").getAttribute("placeholder"),
      "例如 128K、256K、1M",
    );
    const choice = controlByField(row, "supports_vision");
    assert.equal(choice.tagName, "SELECT");
    assert.deepEqual(
      choice.children.map((option) => [option.value, option.textContent]),
      [["", "请选择"], ["true", "是"], ["false", "否"]],
    );
    assert.match(
      fieldDescription(root, controlByField(row, "supports_tools")),
      /Auto 请求包含 tools/,
    );
    assert.match(
      fieldDescription(
        root,
        controlByField(row, "input_price_micro_usd_per_million"),
      ),
      /美元价格.*最多 6 位小数/,
    );
    for (const field of [
      "input_price_micro_usd_per_million",
      "output_price_micro_usd_per_million",
      "cache_read_price_micro_usd_per_million",
      "cache_write_price_micro_usd_per_million",
    ]) {
      const price = controlByField(row, field);
      assert.equal(price.type, "number");
      assert.equal(price.getAttribute("min"), "0");
      assert.equal(price.getAttribute("step"), "0.000001");
    }
  }
  assert.ok(findText(root, "不添加时不影响请求转发"));

  const addModel = buttonByText(root, "添加模型");
  assert.ok(
    addModel.parentNode.className.split(/\s+/).includes("model-list-actions"),
  );
  await addModel.dispatch("click");
  assert.deepEqual(modelIDs(root), ["manual-first", "manual-second", ""]);
  await buttonsByText(root, "删除模型")[0].dispatch("click");
  assert.deepEqual(modelIDs(root), ["manual-second", ""]);
  assert.equal(buttonTexts(root).includes("上移"), false);
});

test("editor uses dollar decimals while preserving integer micro-USD API values", async (t) => {
  const root = installFakeDOM(t);
  const draft = autoLifecycleDraft();
  draft.config.auto_routing.dynamic_optimization = {
    enabled: true,
    sample_rate_bps: 1000,
    daily_budget_micro_usd: 250000,
    reviewer_model: "strong",
    max_concurrency: 2,
    queue_capacity: 128,
    task_timeout: "90s",
  };
  draft.config.models[0].cache_read_price_micro_usd_per_million = 10000;
  draft.config.models[0].cache_write_price_micro_usd_per_million = 125000;
  let saved;
  renderProfileEditor(root, draft, {
    save: async (payload) => { saved = payload; },
    cancel: () => {},
    generate: () => {},
  });

  const fast = modelRows(root)[0];
  const inputPrice = controlByField(
    fast,
    "input_price_micro_usd_per_million",
  );
  const outputPrice = controlByField(
    fast,
    "output_price_micro_usd_per_million",
  );
  const cacheReadPrice = controlByField(
    fast,
    "cache_read_price_micro_usd_per_million",
  );
  const cacheWritePrice = controlByField(
    fast,
    "cache_write_price_micro_usd_per_million",
  );
  assert.equal(inputPrice.value, "0.1");
  assert.equal(outputPrice.value, "0.4");
  assert.equal(cacheReadPrice.value, "0.01");
  assert.equal(cacheWritePrice.value, "0.125");
  assert.equal(controlByName(root, "auto_dynamic_daily_budget").value, "0.25");
  assert.equal(controlByName(root, "auto-budget-cost").value, "0.5");

  inputPrice.value = "0.123456";
  outputPrice.value = "4.25";
  cacheReadPrice.value = "0.012345";
  cacheWritePrice.value = "0.625";
  controlByName(root, "auto_dynamic_daily_budget").value = "1.5";
  controlByName(root, "auto-budget-cost").value = "0.75";
  await findTag(root, "FORM").dispatch("submit");

  assert.equal(saved.config.models[0].input_price_micro_usd_per_million, 123456);
  assert.equal(saved.config.models[0].output_price_micro_usd_per_million, 4250000);
  assert.equal(saved.config.models[0].cache_read_price_micro_usd_per_million, 12345);
  assert.equal(saved.config.models[0].cache_write_price_micro_usd_per_million, 625000);
  assert.equal(
    saved.config.auto_routing.dynamic_optimization.daily_budget_micro_usd,
    1500000,
  );
  assert.equal(
    saved.config.auto_routing.strategy.budget.max_worst_case_cost_micro_usd,
    750000,
  );
});

test("unmatched model ID can apply a searchable catalog preset without changing its ID", async (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft();
  draft.config.models = [{
    id: "private-shadow-model",
    context_window: "",
    max_output_tokens: "",
    supports_vision: false,
  }];

  let saved;
  renderProfileEditor(root, draft, {
    save: async (payload) => { saved = payload; },
    cancel: () => {},
    generate: () => {},
  });

  const row = modelRows(root)[0];
  const preset = controlByName(row, "model-0-preset");
  assert.equal(preset.type, "search");
  assert.ok(preset.getAttribute("list"));
  preset.value = "claude-haiku-4-5-20251001";
  await preset.dispatch("input");
  await buttonByText(row, "应用参数模板").dispatch("click");

  assert.equal(controlByField(row, "id").value, "private-shadow-model");
  assert.equal(controlByField(row, "context_window").value, "200000");
  assert.equal(controlByField(row, "max_output_tokens").value, "64000");
  assert.equal(controlByField(row, "supports_vision").value, "true");
  assert.equal(controlByField(row, "supports_tools").value, "true");
  assert.equal(
    controlByField(row, "input_price_micro_usd_per_million").value,
    "1",
  );
  assert.equal(
    controlByField(row, "output_price_micro_usd_per_million").value,
    "5",
  );
  await findTag(root, "FORM").dispatch("submit");
  assert.equal(
    saved.config.models[0].canonical_model_id,
    "anthropic/claude-haiku-4-5-20251001",
  );
});

test("every model row keeps the searchable template beside model ID", (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft();
  draft.config.models = [{
    id: "claude-haiku-4-5-20251001",
    context_window: 200000,
    max_output_tokens: 64000,
    supports_vision: true,
  }];

  renderProfileEditor(root, draft, {
    save: async () => {},
    cancel: () => {},
    generate: () => {},
  });

  const row = modelRows(root)[0];
  const preset = controlByName(row, "model-0-preset");
  assert.equal(preset.type, "search");
  assert.equal(preset.value, "anthropic/claude-haiku-4-5-20251001");
  assert.equal(buttonByText(row, "应用参数模板").disabled, false);
  assert.deepEqual(
    labelTexts(findClass(row, "model-capability-fields")).slice(0, 4),
    ["模型 ID", "模型模板", "上下文窗口", "最大输出 Token"],
  );
});

test("model editor imports matched and unknown batch IDs with one action", async (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft();

  renderProfileEditor(root, draft, {
    save: async () => {},
    cancel: () => {},
  });
  const input = controlByName(root, "model_batch_ids");
  input.value = [
    "claude-haiku-4-5-20251001",
    "unknown-batch-model",
    "claude-haiku-4-5-20251001",
  ].join("\n");
  assert.deepEqual(
    buttonTexts(root).filter((text) => text.includes("批量")),
    ["批量导入"],
  );
  await buttonByText(root, "批量导入").dispatch("click");

  assert.deepEqual(modelIDs(root), [
    "claude-haiku-4-5-20251001",
    "unknown-batch-model",
  ]);
  assert.equal(
    controlByField(modelRows(root)[0], "context_window").value,
    "200000",
  );
});

test("model batch import skips an exact ID already saved in the Profile", async (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft();
  draft.config.models = [{ id: "saved", context_window: 999 }];
  renderProfileEditor(root, draft, { save: async () => {}, cancel: () => {} });

  controlByName(root, "model_batch_ids").value = "saved";
  await buttonByText(root, "批量导入").dispatch("click");

  assert.ok(findText(root, "没有可导入的新模型"));
  assert.equal(controlByField(modelRows(root)[0], "context_window").value, "999");
});

test("model editor blocks deletion while an exact feature reference remains", async (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft();
  draft.id = 7;
  draft.slug = "coding";
  draft.display_name = "Coding";
  draft.config.upstream = "https://upstream.example";
  draft.config.models = [{ id: "strong", supports_vision: true }];
  draft.config.vision.model = "strong";

  renderProfileEditor(root, draft, { save: async () => {}, cancel: () => {} });
  await buttonByText(root, "删除模型").dispatch("click");

  assert.equal(elementsByClass(root, "model-capability-row").length, 1);
  const dialog = findAllTags(root, "DIALOG")[0];
  assert.ok(dialog.open);
  assert.ok(findText(dialog, "模型仍被其他功能使用"));
  assert.equal(linkByText(dialog, "视觉模型").getAttribute("href"), "/_admin/profiles/7/vision");
});

test("blocked model rename restores Auto model choices", async (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft();
  draft.id = 7;
  draft.slug = "coding";
  draft.display_name = "Coding";
  draft.config.upstream = "https://upstream.example";
  draft.config.models = [{ id: "strong", supports_vision: true }];
  draft.config.auto_routing.participants = ["strong"];
  draft.config.vision.model = "strong";

  renderProfileEditor(root, draft, { save: async () => {}, cancel: () => {} });
  const id = controlByField(modelRows(root)[0], "id");
  id.value = "renamed";
  await id.dispatch("input");
  await id.dispatch("blur");

  assert.equal(id.value, "strong");
  assert.equal(
    controlByName(root, "auto-participant-0").parentNode.textContent,
    "strong",
  );
});

test("Auto editor explains roles routes and budget and saves percentage inputs as basis points", async (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft();
  draft.config.models = [
    {
      id: "fast", context_window: 128000, max_output_tokens: 16000,
      supports_vision: false,
      input_price_micro_usd_per_million: 100000,
      output_price_micro_usd_per_million: 400000,
    },
    {
      id: "strong", context_window: 128000, max_output_tokens: 16000,
      supports_vision: true,
      input_price_micro_usd_per_million: 3000000,
      output_price_micro_usd_per_million: 15000000,
    },
  ];
  draft.config.auto_routing = {
    enabled: true,
    participants: ["fast", "strong"],
    strong_baseline_model: "strong",
    task_analyzer_model: "fast",
    analyzer_timeout: "5s",
    analyzer_min_confidence_bps: 7000,
    session_ttl: "48h",
		session_lock_token_threshold: 100000,
		self_escalation: { enabled: true },
		dynamic_optimization: {
			enabled: true,
			sample_rate_bps: 1000,
			daily_budget_micro_usd: 250000,
			reviewer_model: "strong",
			max_concurrency: 2,
			queue_capacity: 128,
			task_timeout: "90s",
		},
    strategy: {
      name: "20260802-001",
      alias: "均衡",
      default_route: "balanced",
      task_routes: [{ task_type: "simple", route: "balanced" }],
      routes: [{
        id: "balanced",
        min_quality_bps: 9000,
				min_stability_bps: 8000,
        max_severe_error_rate_bps: 100,
				weights: {
					quality_bps: 4000, stability_bps: 2500,
					cost_bps: 2500, performance_bps: 1000,
				},
        candidates: [{
          model: "fast",
					production_eligible: false,
          quality_score_bps: 9200,
					stability_score_bps: 9300,
          severe_error_rate_bps: 50,
					expected_latency_ms: 250,
        }],
      }],
      budget: {
        max_answer_attempts: 2,
        max_auxiliary_calls: 2,
        max_total_outbound_calls: 5,
        max_retries_per_target: 1,
        max_model_switches: 1,
        deadline: "2m",
        max_worst_case_cost_micro_usd: 500000,
      },
    },
  };
  const saves = [];
  renderProfileEditor(root, draft, {
    save: async (payload) => saves.push(payload),
    cancel: () => {},
    generate: () => {},
  });

  assert.equal(controlByName(root, "auto_routing_enabled").checked, true);
	assert.ok(findText(
		root,
		"智能路由仅处理 model=auto。系统结合公开评测和本地证据，为参与模型生成角色、Route 门槛、权重、任务映射和预算。推荐结果始终是可编辑草稿，不会自动发布；客户端指定具体模型时仍原样转发。",
	));
  assert.equal(controlByName(root, "auto_analyzer_confidence").value, "70");
  assert.equal(controlByName(root, "auto_session_ttl").value, "48h");
	assert.equal(controlByName(root, "auto_session_lock_token_threshold").value, "100000");
	assert.equal(controlByName(root, "auto_self_escalation_enabled").checked, true);
	assert.match(
		fieldDescription(root, controlByName(root, "auto_self_escalation_enabled")),
		/输出任何文本前.*原始请求|原始请求.*低级模型/,
	);
	assert.equal(controlByName(root, "auto-task-0-difficulty").value, "");
	assert.equal(controlByName(root, "auto_dynamic_enabled").checked, true);
	assert.equal(controlByName(root, "auto_dynamic_sample_rate").value, "10");
	assert.equal(controlByName(root, "auto_dynamic_reviewer_model").value, "strong");
	assert.match(
		fieldDescription(root, controlByName(root, "auto_dynamic_enabled")),
		/当前回答|不影响/,
	);
  assert.match(
    fieldDescription(root, controlByName(root, "auto_session_ttl")),
    /X-LLM-Proxy-Session-ID/,
  );
  assert.equal(controlByName(root, "auto-route-0-min-quality").value, "90");
	assert.equal(controlByName(root, "auto-route-0-min-stability").value, "80");
	assert.equal(controlByName(root, "auto-route-0-weight-quality").value, "40");
	assert.equal(controlByName(root, "auto-route-0-candidate-0-stability").value, "93");
	assert.equal(controlByName(root, "auto-route-0-candidate-0-latency").value, "250");
	assert.equal(controlByName(root, "auto-route-0-candidate-0-production-eligible").checked, false);
  assert.match(
    fieldDescription(root, controlByName(root, "auto-route-0-min-quality")),
    /质量估计 89.*排除.*92.*参与选型/,
  );
  assert.match(
    fieldDescription(root, controlByName(root, "auto-route-0-candidate-0-quality")),
    /当前 Route.*不是模型的全局评分/,
  );
  const strategyGuide = linkByText(root, "查看完整字段说明和选模示例 ↗");
  assert.equal(
    strategyGuide.href,
    "/_admin/help/intelligent-routing",
  );
	assert.equal(
		descendants(findClass(root, "strategy-manual-panel")).includes(strategyGuide),
		false,
	);
  assert.match(
    fieldDescription(root, controlByName(root, "auto_strong_baseline_model")),
    /高风险/,
  );
	assert.ok(findText(root, "支持多语言、标点和同义表达"));
	assert.ok(findText(root, "高风险完全由任务分析模型按语义判断"));
  assert.match(
    fieldDescription(root, controlByName(root, "auto-budget-total")),
    /所有上游调用/,
  );

  controlByName(root, "auto_analyzer_confidence").value = "72.5";
	controlByName(root, "auto_session_ttl").value = "72h";
	controlByName(root, "auto_session_lock_token_threshold").value = "250000";
	controlByName(root, "auto_dynamic_sample_rate").value = "12.5";
	controlByName(root, "auto-task-0-difficulty").value = "easy";
	controlByName(root, "auto_self_escalation_enabled").checked = false;
	controlByName(root, "auto-route-0-candidate-0-production-eligible").checked = true;
  controlByName(root, "auto-route-0-min-quality").value = "91.25";
  await findTag(root, "FORM").dispatch("submit");

  assert.equal(saves.length, 1);
  assert.equal(saves[0].config.auto_routing.analyzer_min_confidence_bps, 7250);
  assert.equal(saves[0].config.auto_routing.session_ttl, "72h");
	assert.equal(saves[0].config.auto_routing.session_lock_token_threshold, 250000);
	assert.equal(
		saves[0].config.auto_routing.dynamic_optimization.sample_rate_bps,
		1250,
	);
	assert.equal(
		saves[0].config.auto_routing.dynamic_optimization.daily_budget_micro_usd,
		250000,
	);
  assert.equal(saves[0].config.auto_routing.strategy.routes[0].min_quality_bps, 9125);
	assert.equal(saves[0].config.auto_routing.strategy.routes[0].min_stability_bps, 8000);
	assert.equal(saves[0].config.auto_routing.strategy.routes[0].candidates[0].stability_score_bps, 9300);
	assert.equal(saves[0].config.auto_routing.strategy.routes[0].candidates[0].expected_latency_ms, 250);
	assert.equal(saves[0].config.auto_routing.strategy.routes[0].candidates[0].production_eligible, true);
  assert.deepEqual(saves[0].config.auto_routing.participants, ["fast", "strong"]);
	assert.equal(Object.hasOwn(saves[0].config.auto_routing, "risk_policy"), false);
	assert.equal(saves[0].config.auto_routing.strategy.task_routes[0].difficulty, "easy");
	assert.deepEqual(saves[0].config.auto_routing.self_escalation, { enabled: false });

  controlByName(root, "auto-route-0-min-quality").value = "";
  await findTag(root, "FORM").dispatch("submit");

  assert.equal(saves.length, 1);
  assert.equal(findClass(root, "error-banner").hidden, false);
  assert.match(findClass(root, "error-banner").textContent, /最低质量必须是/);
});

test("Auto enable control waits for two recorded models", async (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft();
  draft.id = 7;
  draft.slug = "coding";
  draft.display_name = "Coding";
  draft.config.upstream = "https://upstream.example";

  renderProfileEditor(root, draft, { save: async () => {}, cancel: () => {} });
  assert.equal(controlByName(root, "auto_routing_enabled").disabled, true);

  await buttonByText(root, "添加模型").dispatch("click");
  controlByField(modelRows(root)[0], "id").value = "fast";
  await controlByField(modelRows(root)[0], "id").dispatch("input");
  assert.equal(controlByName(root, "auto_routing_enabled").disabled, true);

  await buttonByText(root, "添加模型").dispatch("click");
  controlByField(modelRows(root)[1], "id").value = "strong";
  await controlByField(modelRows(root)[1], "id").dispatch("input");
  assert.equal(controlByName(root, "auto_routing_enabled").disabled, false);
});

test("incomplete enabled Auto configuration fails locally with actionable guidance", () => {
  const draft = autoLifecycleDraft();
  draft.config.auto_routing.dynamic_optimization = structuredClone(
    defaultProfileDraft().config.auto_routing.dynamic_optimization,
  );
  draft.config.auto_routing.participants = [];

  assert.throws(
    () => profilePayload(draft),
    /至少选择两个参与模型/,
  );
});

test("strategy lifecycle UI focuses the current change and folds older versions", async (t) => {
	const root = installFakeDOM(t);
	const draft = autoLifecycleDraft();
	const active = {
		id: 1,
		profile_id: 7,
		state: "active",
		config: structuredClone(draft.config.auto_routing.strategy),
		rating: {
			display_score_bps: 9907,
			grade: "A",
			quality_floor_bps: 9900,
			severe_error_ceiling_bps: 50,
			passing_routes: 1,
			total_routes: 1,
			source: "configured",
		},
	};
	active.config.roles = {
		participants: ["fast", "strong"],
		strong_baseline_model: "strong",
		task_analyzer_model: "fast",
		reviewer_model: "strong",
	};
	const candidateConfig = structuredClone(active.config);
	candidateConfig.name = "20260802-002";
	candidateConfig.alias = "候选";
	const draftVersion = {
		id: 2,
		profile_id: 7,
		state: "draft",
		config: candidateConfig,
		rating: structuredClone(active.rating),
	};
	const ready = { id: 4, profile_id: 7, state: "ready", config: { ...candidateConfig, name: "20260802-004" } };
	const lastKnownGood = {
		id: 5,
		profile_id: 7,
		state: "ready",
		config: { ...candidateConfig, name: "20260801-001", alias: "上一正式策略" },
	};
	const archived = {
		id: 3,
		profile_id: 7,
		state: "draft",
		archived_at: "2026-08-05T08:00:00Z",
		config: { ...candidateConfig, name: "20260802-003", alias: "已废弃候选" },
	};
	draft.strategy_overview = {
		snapshot: { revision: 4, active, canary_bps: 0, last_known_good: lastKnownGood },
		strategies: [draftVersion, ready, archived, active, lastKnownGood],
	};
	const calls = [];
	renderProfileEditor(root, draft, {
		editStrategy: (id) => calls.push(["edit", id]),
		advanceStrategy: async (id, from, to) => calls.push(["advance", id, from, to]),
		archiveStrategy: async (id) => calls.push(["archive", id]),
		startStrategyCanary: async (id, bps, revision) => calls.push(["canary", id, bps, revision]),
		save: async () => {},
		cancel: () => {},
		generate: () => {},
	});

	assert.ok(findText(root, "当前使用状态"));
	assert.ok(findText(root, "已生效"));
	assert.ok(findText(root, "当前正式策略：当前"));
	assert.ok(findText(root, "请求中的 model 设置为 auto 时使用当前正式策略"));
	assert.ok(findText(root, '"model": "auto"'));
	assert.ok(findText(root, "新策略生效步骤"));
	assert.ok(findText(root, "1. 编辑并保存草稿"));
	assert.ok(findText(root, "3. 发布为正式策略"));
	assert.ok(findText(root, "线上与待发布"));
	assert.ok(findText(root, "线上正在使用"));
	assert.ok(findText(root, "100% 请求"));
	assert.ok(findText(root, "主回答候选：fast、strong"));
	assert.ok(findText(root, "路由规则：1 类任务 → balanced"));
	assert.ok(findText(root, "任务分析：fast"));
	assert.ok(findText(root, "困难、高风险或异常时兜底：strong"));
	assert.ok(findText(root, "异步质量评审（不参与主回答选型）：strong"));
	assert.ok(findText(root, "待发布：候选"));
	assert.ok(findText(root, "草稿 · 尚未生效"));
	assert.ok(findText(root, "候选模型：fast、strong"));
	assert.ok(findText(root, "当前流量：0%"));
	const technicalDetails = findClass(root, "strategy-current-technical-details");
	assert.equal(technicalDetails.tagName, "DETAILS");
	assert.equal(technicalDetails.open, false);
	assert.ok(descendants(technicalDetails).some((item) => item.textContent.startsWith("配置估算：99.07 分")));
	assert.equal(controlByName(root, "auto_strategy_name").readOnly, true);
	const current = findClass(root, "strategy-current-change");
	const rollback = findClass(root, "strategy-rollback-version");
	const history = findClass(root, "strategy-history-disclosure");
	assert.ok(descendants(current).some((item) => item.textContent === "待发布：候选"));
	assert.ok(descendants(rollback).some((item) => item.textContent === "上一正式策略"));
	assert.equal(history.tagName, "DETAILS");
	assert.equal(history.open, false);
	assert.ok(descendants(history).some((item) => item.textContent === "已废弃候选"));
	await buttonByText(current, "载入草稿").dispatch("click");
	await buttonByText(current, "确认并进入灰度准备").dispatch("click");
	await buttonByText(current, "废弃草稿").dispatch("click");
	assert.deepEqual(calls, [
		["edit", 2],
		["advance", 2, "draft", "ready"],
		["archive", 2],
	]);

	const canaryRoot = root;
	const canaryDraft = autoLifecycleDraft();
	canaryDraft.strategy_overview = {
		snapshot: {
			revision: 5,
			active,
			canary: { ...ready, state: "canary" },
			canary_bps: 1250,
			last_known_good: null,
		},
		strategies: [{ ...ready, state: "canary" }, active],
	};
	const publication = [];
	renderProfileEditor(canaryRoot, canaryDraft, {
		cancelStrategyCanary: async (revision) => publication.push(["cancel", revision]),
		promoteStrategy: async (revision) => publication.push(["promote", revision]),
		save: async () => {}, cancel: () => {}, generate: () => {},
	});
	await buttonByText(canaryRoot, "取消灰度").dispatch("click");
	await buttonByText(canaryRoot, "发布为正式策略").dispatch("click");
	assert.deepEqual(publication, [["cancel", 5], ["promote", 5]]);

	const rollbackRoot = root;
	const rollbackDraft = autoLifecycleDraft();
	rollbackDraft.strategy_overview = {
		snapshot: { revision: 6, active: ready, canary_bps: 0, last_known_good: active },
		strategies: [ready, active],
	};
	let rollbackRevision = 0;
	renderProfileEditor(rollbackRoot, rollbackDraft, {
		rollbackStrategy: async (revision) => { rollbackRevision = revision; },
		save: async () => {}, cancel: () => {}, generate: () => {},
	});
	await buttonByText(rollbackRoot, "回滚到上一正式策略").dispatch("click");
	assert.equal(rollbackRevision, 6);
});

test("strategy history reveals ten versions at a time", async (t) => {
	const root = installFakeDOM(t);
	const draft = autoLifecycleDraft();
	const active = {
		id: 1,
		profile_id: 7,
		state: "active",
		config: structuredClone(draft.config.auto_routing.strategy),
	};
	const history = Array.from({ length: 12 }, (_, index) => ({
		id: index + 2,
		profile_id: 7,
		state: "draft",
		archived_at: `2026-08-${String(index + 1).padStart(2, "0")}T00:00:00Z`,
		config: {
			...structuredClone(active.config),
			name: `20260801-${String(index + 2).padStart(3, "0")}`,
			alias: `历史 ${index + 1}`,
		},
	}));
	draft.strategy_overview = {
		snapshot: { revision: 1, active, canary_bps: 0 },
		strategies: [...history, active],
	};
	renderProfileEditor(root, draft, {
		save: async () => {}, cancel: () => {}, generate: () => {},
	});
	const disclosure = findClass(root, "strategy-history-disclosure");
	const cards = descendants(disclosure).filter((item) =>
		String(item.className || "").split(" ").includes("strategy-history-version")
	);
	assert.equal(cards.filter((card) => !card.hidden).length, 10);
	await buttonByText(disclosure, "再显示 2 个历史版本").dispatch("click");
	assert.equal(cards.filter((card) => !card.hidden).length, 12);
});

test("strategy lifecycle shows conservative evidence and generates only a draft", async (t) => {
	const root = installFakeDOM(t);
	const draft = autoLifecycleDraft();
	const active = {
		id: 1,
		profile_id: 7,
		state: "active",
		config: structuredClone(draft.config.auto_routing.strategy),
	};
	draft.config.auto_routing.dynamic_optimization = {
		enabled: true,
		sample_rate_bps: 1000,
		daily_budget_micro_usd: 250000,
		reviewer_model: "strong",
		max_concurrency: 2,
		queue_capacity: 128,
		task_timeout: "90s",
	};
	draft.strategy_overview = {
		snapshot: { revision: 1, active, canary_bps: 0 },
		strategies: [active],
		evaluation_budget: {
			day: "2026-08-02",
			reserved_micro_usd: 1000,
			spent_micro_usd: 5000,
		},
		quality_estimates: [{
			strategy: "20260802-001",
			route: "balanced",
			candidate_model: "fast",
			reference_model: "strong",
			raw_samples: 30,
			effective_samples: 29.5,
			reliable: true,
			quality_mean_bps: 9500,
			quality_lower_bps: 9134,
			severe_error_mean_bps: 120,
			severe_error_upper_bps: 364,
			self_escalation_eligible_samples: 20,
			self_escalations: 5,
			supported_self_escalations: 4,
			unnecessary_self_escalations: 1,
			missed_self_escalations: 2,
			self_escalation_precision_bps: 8000,
			missed_self_escalation_rate_bps: 1333,
		}],
		stability_estimates: [{
			strategy: "20260802-001",
			route: "balanced",
			candidate_model: "fast",
			reference_model: "strong",
			raw_online_samples: 30,
			effective_review_samples: 29.5,
			mean_bps: 9500,
			lower_bps: 9100,
			expected_latency_ms: 250,
			reliable: true,
		}],
	};
	let generated = 0;
	renderProfileEditor(root, draft, {
		generateStrategyCandidate: async () => { generated++; },
		save: async () => {}, cancel: () => {}, generate: () => {},
	});

	assert.ok(findText(root, "今日评测：$0.005 / $0.25"));
	assert.ok(findText(root, "模型表现：1 个质量分组、1 个稳定性分组，其中 1 个可生成学习草稿。"));
	const performanceLink = findText(root, "查看模型表现");
	assert.equal(performanceLink.getAttribute("href"), "/_admin/stats/models?profile_id=7");
	await buttonByText(root, "生成学习候选").dispatch("click");
	assert.equal(generated, 1);
});

test("intelligent strategy generator creates immutable drafts and exposes sources constraints and restore", async (t) => {
	const root = installFakeDOM(t);
	const draft = autoLifecycleDraft();
	const active = {
		id: 1,
		profile_id: 7,
		state: "active",
		config: structuredClone(draft.config.auto_routing.strategy),
	};
	const generatedConfig = structuredClone(active.config);
	generatedConfig.name = "20260802-002";
	generatedConfig.alias = "智能生成 · 均衡";
	const generatedVersion = {
		id: 2,
		profile_id: 7,
		state: "draft",
		config: generatedConfig,
	};
	draft.strategy_overview = {
		snapshot: { revision: 1, active, canary_bps: 0 },
		strategies: [generatedVersion, active],
		generations: [{
			strategy_id: 2,
			profile_id: 7,
			generator_version: "strategy-compiler/v1",
			intent: { objective: "balanced", participants: ["fast", "strong"] },
			source_digest: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
			manual_overrides: [
				{ path: "/alias", value: "人工别名" },
				{ path: "/routes/0/candidates/0/quality_score_bps", value: 9700 },
			],
			explanations: [
				{ code: "external_prior", message: "使用公开评测冷启动。" },
				{ code: "insufficient_data", model: "fast", message: "没有可精确匹配的公开评测或可靠本地证据，使用系统临时质量值。" },
				{ code: "insufficient_data", model: "strong", message: "没有可精确匹配的公开评测或可靠本地证据，使用系统临时质量值。" },
				{ code: "system_provisional", model: "fast", message: "稳定性 80%、严重错误上界 5%、延迟未知均为系统临时值，需要优先采样验证。" },
				{ code: "system_provisional", model: "strong", message: "稳定性 80%、严重错误上界 5%、延迟未知均为系统临时值，需要优先采样验证。" },
			],
		}],
	};
	draft.evaluation_catalog = {
		revision: "1234567890abcdef",
		sources: [{ id: "livebench", name: "LiveBench", version: "2026-08" }],
		results: [{ model_id: "acme/fast", domain: "general" }],
	};
	draft.config.models[0].canonical_model_id = "acme/fast";
	const calls = [];
	renderProfileEditor(root, draft, {
		generateStrategy: async (intent) => calls.push(["generate", intent]),
		restoreGeneratedRecommendation: async (id, path) => calls.push(["restore", id, path]),
		updateEvaluationCatalog: async (onProgress) => {
			calls.push(["catalog"]);
			onProgress?.({
				id: "job-1", state: "running",
				sources: [{ id: "livebench", state: "running", stage: "download_and_import", imported: 0, skipped_unmapped: 0 }],
			});
			const job = {
				id: "job-1", state: "succeeded", changed: true,
				sources: [{ id: "livebench", state: "succeeded", stage: "completed", imported: 12, skipped_unmapped: 3 }],
			};
			onProgress?.(job);
			return job;
		},
		save: async () => {}, cancel: () => {}, generate: () => {},
	});

	assert.ok(findText(root, "新策略配置"));
	const generatorPanel = findClass(root, "strategy-generator-panel");
	const manualPanel = findClass(root, "strategy-manual-panel");
	assert.equal(generatorPanel.hidden, false);
	assert.equal(manualPanel.hidden, true);
	assert.equal(
		controls(root).some((control) => control.name === "strategy_configuration_mode"),
		false,
	);
	assert.ok(findText(root, "重新生成新草稿"));
	assert.ok(findText(root, "根据当前模型配置、最新公开评测和本地证据生成一个新草稿，不会改写已有版本或影响正式策略。"));
	assert.ok(findText(root, "公开评测只用于冷启动排序和 Shadow 候选；只有可靠本地证据通过质量与稳定性门槛后，候选才可获得生产流量。"));
	assert.ok(buttonByText(root, "重新生成新草稿").className.includes("button-secondary"));
	assert.ok(findText(root, "直接手动配置"));
	assert.ok(findText(root, "公开评测可用 · 已覆盖 1/2 个模型"));
	const advancedGenerator = findClass(root, "strategy-generator-advanced");
	assert.equal(advancedGenerator.tagName, "DETAILS");
	assert.equal(advancedGenerator.open, false);
	assert.ok(descendants(advancedGenerator).includes(
		controlByName(root, "strategy_generation_max_cost_usd"),
	));
	assert.ok(descendants(advancedGenerator).includes(
		buttonByText(root, "下载并更新公开评测"),
	));
	assert.equal(descendants(advancedGenerator).includes(
		controlByName(root, "strategy_generation_objective"),
	), false);
	assert.ok(findText(root, "LiveBench · 2026-08"));
	assert.ok(findText(root, "已载入 1 个公开评测来源，共 1 条结果。"));
	assert.ok(findText(root, "1 / 2 个模型有公开评测覆盖"));
	assert.ok(findText(root, "手动调整 2 项"));
	assert.ok(findText(root, "使用公开评测冷启动"));
	assert.equal(
		descendants(root).filter((item) =>
			item.textContent === "没有可精确匹配的公开评测或可靠本地证据，使用系统临时质量值。"
		).length,
		1,
	);
	assert.equal(
		descendants(root).filter((item) =>
			item.textContent === "稳定性 80%、严重错误上界 5%、延迟未知均为系统临时值，需要优先采样验证。"
		).length,
		1,
	);
	await buttonByText(root, "直接手动配置").dispatch("click");
	assert.equal(generatorPanel.hidden, true);
	assert.equal(manualPanel.hidden, false);
	await buttonByText(root, "重新生成草稿").dispatch("click");
	assert.equal(generatorPanel.hidden, false);
	assert.equal(manualPanel.hidden, true);
	assert.equal(controlByName(root, "strategy_generation_objective").disabled, false);
	await buttonByText(root, "下载并更新公开评测").dispatch("click");
	assert.ok(findText(root, "LiveBench · 完成 · 导入 12 · 未映射 3"));
	await buttonByText(root, "重新生成新草稿").dispatch("click");
	assert.equal(generatorPanel.hidden, true);
	assert.equal(manualPanel.hidden, false);
	assert.ok(findText(root, "推荐草稿已生成，可以直接调整后保存。"));
	assert.equal(buttonsByText(root, "刷新智能推荐").length, 0);
	await buttonByText(root, "恢复推荐：quality_score_bps").dispatch("click");

	assert.equal(calls[0][0], "catalog");
	assert.equal(calls[1][0], "generate");
	assert.deepEqual(calls[2], ["restore", 2, "/routes/0/candidates/0/quality_score_bps"]);
});

test("intelligent recommendation defaults to every recorded model and opens manual editing", async (t) => {
	const root = installFakeDOM(t);
	const draft = defaultProfileDraft();
	draft.id = 7;
	draft.slug = "coding";
	draft.display_name = "Coding";
	draft.config.upstream = "https://upstream.example";
	draft.config.models = [{ id: "fast" }, { id: "strong" }];
	const intents = [];

	renderProfileEditor(root, draft, {
		generateStrategy: async (intent) => intents.push(intent),
		save: async () => {}, cancel: () => {}, generate: () => {},
	});
	const enabled = controlByName(root, "auto_routing_enabled");
	enabled.checked = true;
	await enabled.dispatch("change");
	await buttonByText(root, "生成可编辑策略").dispatch("click");

	assert.deepEqual(intents, [{
		objective: "balanced",
		participants: ["fast", "strong"],
		daily_eval_budget_micro_usd: 250000,
	}]);
	assert.equal(findClass(root, "strategy-generator-panel").hidden, true);
	assert.equal(findClass(root, "strategy-manual-panel").hidden, false);
});

test("generated strategy stays a draft and saves through the strategy lifecycle", async (t) => {
	const root = installFakeDOM(t);
	const draft = autoLifecycleDraft();
	const active = {
		id: 1,
		profile_id: 7,
		state: "active",
		config: structuredClone(draft.config.auto_routing.strategy),
	};
	const generated = {
		id: 2,
		profile_id: 7,
		state: "draft",
		config: structuredClone(draft.config.auto_routing.strategy),
	};
	draft.strategy_overview = {
		snapshot: { revision: 1, active, canary_bps: 0 },
		strategies: [generated, active],
		generations: [{
			strategy_id: 2,
			profile_id: 7,
			generator_version: "strategy-compiler/v1",
			intent: { objective: "balanced", participants: ["fast", "strong"] },
			source_digest: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
			manual_overrides: [],
			explanations: [],
		}],
	};
	draft.strategy_editing_id = 2;
	draft.strategy_configuration_mode = "manual";
	draft.strategy_generation_notice = "推荐草稿已生成，可以直接调整后保存。";
	const updates = [];
	const generations = [];
	let profileSaves = 0;

	renderProfileEditor(root, draft, {
		editorSection: "routing",
		generateStrategy: async (intent) => generations.push(intent),
		updateStrategy: async (id, config) => updates.push([id, config]),
		save: async () => { profileSaves++; }, cancel: () => {}, generate: () => {},
	});

	assert.ok(findText(root, "待发布的新策略"));
	assert.ok(findText(root, "① 当前线上 · 正在使用"));
	assert.ok(findText(root, "策略：当前"));
	assert.ok(findText(root, "② 新策略草稿 · 尚未生效"));
	assert.ok(findText(root, "当前没有请求使用这份策略。"));
	assert.ok(findText(root, "③ 下一步"));
	assert.ok(findText(root, "保存修改并开始灰度"));
	assert.ok(findText(root, "只有发布为正式策略后，所有请求才会切换到新模型。"));
	assert.ok(findText(root, "包含 1 个模型组，覆盖 1 类任务。"));
	assert.ok(findText(root, "未匹配到特定任务时使用：balanced"));
	assert.ok(findText(root, "新策略只会在这些模型中选择：fast、strong"));
	assert.ok(findText(root, "困难、高风险或其他模型不可用时使用：strong"));
	assert.ok(findText(root, "所有难度 → balanced"));
	assert.ok(findText(root, "1 类任务：简单"));
	assert.equal(elementsByClass(root, "strategy-task-route-row").length, 1);
	const details = findClass(root, "strategy-configuration-details");
	assert.equal(details.tagName, "DETAILS");
	assert.equal(details.open, false);
	assert.ok(descendants(details).some((item) => item.textContent === "手动设置策略"));
	assert.ok(descendants(details).some((item) => item.textContent === "任务映射"));
	assert.ok(descendants(details).some((item) => item.textContent === "统一尝试预算"));
	const save = buttonByText(root, "保存草稿修改");
	assert.equal(save.type, "button");
	await save.dispatch("click");
	assert.equal(updates.length, 1);
	assert.equal(updates[0][0], 2);
	assert.equal(profileSaves, 0);
	assert.equal(buttonByText(root, "更新当前草稿").hidden, true);
	const profileSave = buttonByText(root, "保存路由配置");
	assert.equal(profileSave.type, "submit");
	assert.equal(findClass(root, "routing-profile-actions").hidden, false);
	assert.ok(findText(root, "保存模型角色、分析和优化设置；路由策略草稿需在策略摘要中单独保存。"));
	controlByName(root, "auto_task_analyzer_model").value = "strong";
	await descendants(root).find((element) => element.tagName === "FORM").dispatch("submit");
	assert.equal(profileSaves, 1);
	await buttonByText(root, "重新生成草稿").dispatch("click");
	assert.equal(controlByName(root, "strategy_generation_objective").disabled, false);
	await buttonByText(root, "重新生成新草稿").dispatch("click");
	assert.equal(generations.length, 1);
});

test("strategy summary groups repeated task mappings by difficulty and route", (t) => {
	const root = installFakeDOM(t);
	const draft = autoLifecycleDraft();
	const taskTypes = ["general", "simple", "reasoning", "math", "coding", "tool_use", "vision"];
	draft.config.auto_routing.strategy.task_routes = taskTypes.flatMap((taskType) => [
		{ task_type: taskType, difficulty: "easy", route: "general" },
		{ task_type: taskType, difficulty: "medium", route: "general" },
		{ task_type: taskType, difficulty: "hard", route: "strong" },
	]);
	const active = {
		id: 1,
		profile_id: 7,
		state: "active",
		config: { ...structuredClone(draft.config.auto_routing.strategy), alias: "线上策略" },
	};
	draft.strategy_overview = {
		snapshot: { revision: 1, active, canary_bps: 0 },
		strategies: [active],
	};
	draft.strategy_configuration_mode = "manual";

	renderProfileEditor(root, draft, {
		editorSection: "routing",
		save: async () => {}, cancel: () => {}, generate: () => {},
	});

	assert.ok(findText(root, "简单、中等 → 常规模型组（general）"));
	assert.ok(findText(root, "困难 → 高能力模型组（strong）"));
	assert.ok(findText(root, "7 类任务：编程、通用、数学、推理、简单请求、工具调用、视觉"));
	assert.equal(elementsByClass(root, "strategy-task-route-row").length, 2);
	assert.equal(descendants(root).some((item) => item.textContent.includes("任务安排：")), false);
});

test("an open generated draft is never refreshed in place", async (t) => {
	const root = installFakeDOM(t);
	const draft = autoLifecycleDraft();
	const active = {
		id: 1,
		profile_id: 7,
		state: "active",
		config: structuredClone(draft.config.auto_routing.strategy),
	};
	const generated = {
		id: 2,
		profile_id: 7,
		state: "draft",
		config: { ...structuredClone(active.config), name: "20260805-002" },
	};
	draft.strategy_overview = {
		snapshot: { revision: 1, active, canary_bps: 0 },
		strategies: [generated, active],
		generations: [{
			strategy_id: 2,
			profile_id: 7,
			generator_version: "strategy-compiler/v1",
			intent: { objective: "balanced", participants: ["fast", "strong"] },
			source_digest: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
			manual_overrides: [],
			explanations: [],
		}],
	};
	const generations = [];
	renderProfileEditor(root, draft, {
		generateStrategy: async (intent) => generations.push(intent),
		save: async () => {}, cancel: () => {}, generate: () => {},
	});
	assert.equal(buttonsByText(root, "刷新当前草稿").length, 0);
	await buttonByText(root, "重新生成新草稿").dispatch("click");
	assert.equal(generations.length, 1);
});

test("changing participating models requires a compatible strategy draft", async (t) => {
	const root = installFakeDOM(t);
	const draft = autoLifecycleDraft();
	draft.config.models.splice(1, 0, {
		id: "balanced", context_window: 128000, max_output_tokens: 16000,
		supports_vision: false, supports_tools: true,
		supports_structured_output: true,
		input_price_micro_usd_per_million: 500000,
		output_price_micro_usd_per_million: 1500000,
	});
	draft.config.auto_routing.participants = ["fast", "balanced", "strong"];
	draft.config.auto_routing.strategy.routes[0].candidates.splice(1, 0, {
		model: "balanced", quality_score_bps: 9500, stability_score_bps: 9500,
		severe_error_rate_bps: 30, expected_latency_ms: 400,
	});
	const active = {
		id: 1, profile_id: 7, state: "active",
		config: structuredClone(draft.config.auto_routing.strategy),
	};
	draft.strategy_overview = {
		snapshot: { revision: 1, active, canary_bps: 0 },
		strategies: [active], generations: [],
	};
	const generations = [];
	let saves = 0;
	renderProfileEditor(root, draft, {
		editorSection: "routing",
		generateStrategy: async (intent) => generations.push(intent),
		save: async () => { saves++; }, cancel: () => {}, generate: () => {},
	});

	const strong = controlByName(root, "auto-participant-2");
	strong.checked = false;
	await strong.dispatch("change");
	await findAllTags(root, "FORM")[0].dispatch("submit");

	const dialog = openDialog(root);
	assert.ok(findText(dialog, "参与模型变化需要新策略"));
	assert.ok(findText(dialog, "移除参与模型：strong"));
	assert.ok(findText(dialog, "Route balanced 仍引用：strong"));
	assert.equal(saves, 0);
	await buttonByText(dialog, "生成新策略草稿").dispatch("click");
	assert.equal(dialog.parentNode, null);
	assert.deepEqual(generations, [{
		objective: "balanced",
		participants: ["fast", "balanced"],
		daily_eval_budget_micro_usd: 250000,
	}]);
});

test("adding a participating model also requires regenerating the strategy", async (t) => {
	const root = installFakeDOM(t);
	const draft = autoLifecycleDraft();
	draft.config.models.push({
		id: "new", context_window: 128000, max_output_tokens: 16000,
		supports_vision: false, supports_tools: true,
		supports_structured_output: true,
		input_price_micro_usd_per_million: 500000,
		output_price_micro_usd_per_million: 1500000,
	});
	const active = {
		id: 1, profile_id: 7, state: "active",
		config: structuredClone(draft.config.auto_routing.strategy),
	};
	draft.strategy_overview = {
		snapshot: { revision: 1, active, canary_bps: 0 },
		strategies: [active], generations: [],
	};
	let saves = 0;
	renderProfileEditor(root, draft, {
		editorSection: "routing",
		generateStrategy: async () => {},
		save: async () => { saves++; }, cancel: () => {}, generate: () => {},
	});

	const added = controlByName(root, "auto-participant-2");
	added.checked = true;
	await added.dispatch("change");
	await findAllTags(root, "FORM")[0].dispatch("submit");

	const dialog = openDialog(root);
	assert.ok(findText(dialog, "新增参与模型：new"));
	assert.equal(saves, 0);
	await buttonByText(dialog, "取消").dispatch("click");
	assert.equal(dialog.parentNode, null);
});

test("compatible published strategy allows the pending model roles to save", async (t) => {
	const root = installFakeDOM(t);
	const draft = autoLifecycleDraft();
	draft.config.models.splice(1, 0, {
		id: "balanced", context_window: 128000, max_output_tokens: 16000,
		supports_vision: false, supports_tools: true,
		supports_structured_output: true,
		input_price_micro_usd_per_million: 500000,
		output_price_micro_usd_per_million: 1500000,
	});
	draft.persisted_auto_participants = ["fast", "balanced", "strong"];
	draft.config.auto_routing.participants = ["fast", "balanced"];
	draft.config.auto_routing.strong_baseline_model = "balanced";
	draft.config.auto_routing.strategy.routes[0].candidates = [
		draft.config.auto_routing.strategy.routes[0].candidates[0],
		{
			model: "balanced", quality_score_bps: 9500, stability_score_bps: 9500,
			severe_error_rate_bps: 30, expected_latency_ms: 400,
		},
	];
	const active = {
		id: 2, profile_id: 7, state: "active",
		config: structuredClone(draft.config.auto_routing.strategy),
	};
	draft.strategy_overview = {
		snapshot: { revision: 2, active, canary_bps: 0 },
		strategies: [active], generations: [],
	};
	let saves = 0;
	renderProfileEditor(root, draft, {
		editorSection: "routing",
		generateStrategy: async () => {},
		save: async () => { saves++; }, cancel: () => {}, generate: () => {},
	});

	await findAllTags(root, "FORM")[0].dispatch("submit");
	assert.equal(saves, 1);
	assert.equal(findAllTags(root, "DIALOG").length, 0);
});

test("compatible strategy draft allows replacement model roles to save", async (t) => {
	const root = installFakeDOM(t);
	const draft = autoLifecycleDraft();
	draft.config.models.splice(1, 0, {
		id: "replacement", context_window: 128000, max_output_tokens: 16000,
		supports_vision: false, supports_tools: true,
		supports_structured_output: true,
		input_price_micro_usd_per_million: 500000,
		output_price_micro_usd_per_million: 1500000,
	});
	draft.persisted_auto_participants = ["fast", "strong"];
	draft.config.auto_routing.participants = ["fast", "replacement"];
	draft.config.auto_routing.strong_baseline_model = "replacement";
	draft.config.auto_routing.task_analyzer_model = "fast";
	const active = {
		id: 1, profile_id: 7, state: "active",
		config: {
			...structuredClone(draft.config.auto_routing.strategy),
			roles: {
				participants: ["fast", "strong"],
				strong_baseline_model: "strong",
				task_analyzer_model: "fast",
			},
		},
	};
	const compatibleDraft = {
		id: 2, profile_id: 7, state: "draft",
		config: {
			...structuredClone(draft.config.auto_routing.strategy),
			roles: {
				participants: ["fast", "replacement"],
				strong_baseline_model: "replacement",
				task_analyzer_model: "fast",
			},
			routes: [{
				...structuredClone(draft.config.auto_routing.strategy.routes[0]),
				candidates: [
					draft.config.auto_routing.strategy.routes[0].candidates[0],
					{
						model: "replacement", quality_score_bps: 9500,
						stability_score_bps: 9500, severe_error_rate_bps: 30,
						expected_latency_ms: 400,
					},
				],
			}],
		},
	};
	draft.strategy_overview = {
		snapshot: { revision: 1, active, canary_bps: 0 },
		strategies: [compatibleDraft, active], generations: [],
	};
	draft.config.auto_routing.strategy = structuredClone(compatibleDraft.config);
	draft.strategy_editing_id = compatibleDraft.id;
	draft.strategy_configuration_mode = "manual";
	let saves = 0;
	renderProfileEditor(root, draft, {
		editorSection: "routing",
		generateStrategy: async () => {},
		save: async () => { saves++; }, cancel: () => {}, generate: () => {},
	});

	await findAllTags(root, "FORM")[0].dispatch("submit");
	assert.equal(saves, 1);
	assert.equal(findAllTags(root, "DIALOG").length, 0);
});

test("empty embedded evaluation catalog is reported as loaded", (t) => {
	const root = installFakeDOM(t);
	const draft = autoLifecycleDraft();
	draft.evaluation_catalog = {
		revision: "e3b0c44298fc1c14",
		sources: [{ id: "livebench", name: "LiveBench", version: "not-imported" }],
		results: [],
	};
	renderProfileEditor(root, draft, {
		generateStrategy: async () => {},
		updateEvaluationCatalog: async () => {},
		save: async () => {}, cancel: () => {}, generate: () => {},
	});

	assert.ok(findText(root, "已载入 1 个公开评测来源定义，尚未下载结果。"));
	assert.ok(findText(root, "尚未下载"));
	assert.ok(findText(root, "2 个模型尚未确认标准身份，暂时无法匹配公开评测。"));
	assert.equal(
		descendants(root).some((element) => element.textContent.includes("尚未载入公开评测目录")),
		false,
	);
});

test("routing coverage uses uniquely matched model identities before the next save", (t) => {
	const root = installFakeDOM(t);
	const draft = autoLifecycleDraft();
	const aliases = {
		fast: "claude-glm-5.2",
		strong: "claude-haiku-4-5-20251001",
	};
	for (const model of draft.config.models) model.id = aliases[model.id];
	draft.config.auto_routing.participants = [aliases.fast, aliases.strong];
	draft.config.auto_routing.strong_baseline_model = aliases.strong;
	draft.config.auto_routing.task_analyzer_model = aliases.fast;
	for (const candidate of draft.config.auto_routing.strategy.routes[0].candidates) {
		candidate.model = aliases[candidate.model];
	}
	draft.evaluation_catalog = {
		revision: "catalog-revision",
		sources: [{ id: "arena", name: "LMArena", version: "v1" }],
		results: [
			{ model_id: "zhipuai/glm-5.2", domain: "general" },
			{ model_id: "anthropic/claude-haiku-4-5-20251001", domain: "general" },
		],
	};
	renderProfileEditor(root, draft, {
		generateStrategy: async () => {},
		updateEvaluationCatalog: async () => {},
		save: async () => {}, cancel: () => {}, generate: () => {},
	});

	assert.ok(findText(root, "2 / 2 个模型有公开评测覆盖"));
	assert.equal(
		descendants(root).some((item) => item.textContent.includes("未确认身份 2")),
		false,
	);
});

test("evaluation catalog versions stay behind details and update progress is visible", async (t) => {
	const root = installFakeDOM(t);
	const draft = autoLifecycleDraft();
	const fullVersion = "4e52c8e709c90a4cad8498d9db5aad11709b04e0";
	draft.evaluation_catalog = {
		revision: "catalog-revision",
		sources: [{ id: "lmarena", name: "LMArena", version: fullVersion }],
		results: [{ model_id: "acme/fast", domain: "general" }],
	};
	let finishUpdate;
	renderProfileEditor(root, draft, {
		generateStrategy: async () => {},
		updateEvaluationCatalog: (onProgress) => new Promise((resolve) => {
			finishUpdate = () => {
				const job = {
					state: "succeeded",
					sources: [{
						id: "lmarena", state: "succeeded", stage: "completed",
						imported: 18, skipped_unmapped: 2,
					}],
				};
				onProgress(job);
				resolve(job);
			};
		}),
		save: async () => {}, cancel: () => {}, generate: () => {},
	});

	assert.ok(findText(root, "查看数据来源与版本"));
	const compactVersion = findText(root, "LMArena · 4e52c8e7");
	assert.equal(compactVersion.getAttribute("title"), fullVersion);
	assert.equal(
		descendants(root).some((item) => item.textContent.includes(fullVersion)),
		false,
	);

	const updateButton = buttonByText(root, "下载并更新公开评测");
	const click = updateButton.dispatch("click");
	await Promise.resolve();
	assert.equal(findClass(root, "strategy-generator-advanced").open, true);
	assert.equal(updateButton.disabled, true);
	assert.equal(updateButton.getAttribute("aria-busy"), "true");
	assert.ok(updateButton.className.includes("is-loading"));
	assert.equal(updateButton.textContent, "正在更新公开评测…");
	assert.ok(findText(root, "正在更新公开评测 · 已处理 0 / 1 个来源"));
	assert.equal(findClass(root, "strategy-catalog-progress-track").getAttribute("aria-valuenow"), "0");

	finishUpdate();
	await click;
	assert.equal(updateButton.disabled, false);
	assert.equal(updateButton.getAttribute("aria-busy"), "false");
	assert.ok(findText(root, "公开评测更新完成 · 1 / 1 个来源"));
	assert.equal(findClass(root, "strategy-catalog-progress-track").getAttribute("aria-valuenow"), "100");
});

test("saved model rows fill blank catalog values while preserving configured values", async (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft();
  draft.config.models = [
    {
      id: "GLM-5",
      context_window: 204800,
      max_output_tokens: "",
      supports_vision: false,
    },
  ];

  renderProfileEditor(root, draft, {
    save: async () => {},
    cancel: () => {},
    generate: () => {},
  });

  const [exact] = modelRows(root);
  assert.equal(controlByField(exact, "id").value, "GLM-5");
  assert.equal(controlByField(exact, "context_window").value, "204800");
  assert.equal(controlByField(exact, "max_output_tokens").value, "131072");
  assert.equal(controlByField(exact, "supports_vision").value, "false");
  assert.ok(findText(exact, "来源：Models.dev"));
  assert.ok(findText(exact, "更新：2026-02-12"));
  assert.ok(findText(exact, `获取：${MODELS_DEV_SOURCE.retrieved}`));
});

test("unique model input immediately applies exact values", async (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft();
  draft.config.models = [{
    id: "",
    context_window: "",
    max_output_tokens: "",
    supports_vision: "",
  }];

  renderProfileEditor(root, draft, {
    save: async () => {},
    cancel: () => {},
    generate: () => {},
  });

  const [row] = modelRows(root);
  const id = controlByField(row, "id");
  const context = controlByField(row, "context_window");
  const output = controlByField(row, "max_output_tokens");
  const vision = controlByField(row, "supports_vision");

  id.value = "glm-5";
  await id.dispatch("input");

  id.value = "glm-5.2";
  await id.dispatch("input");
  assert.equal(context.value, "1000000");
  assert.equal(output.value, "131072");
  assert.equal(vision.value, "false");
  assert.equal(id.value, "glm-5.2");
});

test("changing an upstream model ID clears its persisted canonical identity", async (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft();
  draft.config.models = [{
    id: "glm-5.2",
    canonical_model_id: "zhipuai/glm-5.2",
    context_window: 1000000,
    max_output_tokens: 131072,
    supports_vision: false,
  }];
  const saves = [];

  renderProfileEditor(root, draft, {
    save: async (payload) => saves.push(payload),
    cancel: () => {},
    generate: () => {},
  });

  await findTag(root, "FORM").dispatch("submit");
  assert.equal(
    saves[0].config.models[0].canonical_model_id,
    "zhipuai/glm-5.2",
  );

  const id = controlByField(modelRows(root)[0], "id");
  id.value = "private-upstream-model";
  await id.dispatch("input");
  await findTag(root, "FORM").dispatch("submit");
  assert.equal(
    Object.hasOwn(saves[1].config.models[0], "canonical_model_id"),
    false,
  );
});

test("pasting a unique model ID immediately applies its safe recommendation", async (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft();
  draft.config.models = [{
    id: "",
    context_window: "",
    max_output_tokens: "",
    supports_vision: "",
  }];

  renderProfileEditor(root, draft, {
    save: async () => {},
    cancel: () => {},
    generate: () => {},
  });

  const row = modelRows(root)[0];
  const id = controlByField(row, "id");
  id.value = "claude-haiku-4-5-20251001";
  await id.dispatch("input", { inputType: "insertFromPaste" });

  assert.equal(id.value, "claude-haiku-4-5-20251001");
  assert.equal(controlByField(row, "context_window").value, "200000");
  assert.equal(controlByField(row, "max_output_tokens").value, "64000");
  assert.equal(controlByField(row, "supports_vision").value, "true");
  assert.equal(controlByField(row, "supports_tools").value, "true");
  assert.equal(
    controlByField(row, "input_price_micro_usd_per_million").value,
    "1",
  );
  assert.equal(
    controlByField(row, "output_price_micro_usd_per_million").value,
    "5",
  );
  assert.ok(findText(row, "参考价格"));
  assert.ok(findText(row, "实际上游账单"));
});

test("compatibility aliases immediately apply all template values without changing the entered ID", async (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft();
  draft.config.models = [{
    id: "",
    context_window: "",
    max_output_tokens: "",
    supports_vision: "",
  }];

  renderProfileEditor(root, draft, {
    save: async () => {},
    cancel: () => {},
    generate: () => {},
  });

  const row = modelRows(root)[0];
  const id = controlByField(row, "id");
  id.value = "claude-glm-5.2";
  await id.dispatch("input");

  assert.equal(id.value, "claude-glm-5.2");
  assert.equal(
    controlByName(row, "model-0-preset").value,
    "zhipuai/glm-5.2",
  );
  assert.equal(controlByField(row, "context_window").value, "1000000");
  assert.equal(controlByField(row, "max_output_tokens").value, "131072");
  assert.equal(controlByField(row, "supports_vision").value, "false");
  assert.equal(
    controlByField(row, "input_price_micro_usd_per_million").value,
    "1.4",
  );
  assert.equal(
    controlByField(row, "output_price_micro_usd_per_million").value,
    "4.4",
  );
  assert.equal(
    controlByField(row, "cache_read_price_micro_usd_per_million").value,
    "0.26",
  );
  assert.equal(
    controlByField(row, "cache_write_price_micro_usd_per_million").value,
    "0",
  );
});

test("CRS DeepSeek aliases restore and persist canonical identities", async (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft();
  draft.config.models = [
    {
      id: "azure-ds-v4-flash",
      context_window: 1000000,
      max_output_tokens: 384000,
      supports_vision: false,
    },
    {
      id: "azure-ds-v4-pro",
      context_window: 1000000,
      max_output_tokens: 384000,
      supports_vision: false,
    },
  ];
  let saved;

  renderProfileEditor(root, draft, {
    save: async (payload) => { saved = payload; },
    cancel: () => {},
    generate: () => {},
  });

  const rows = modelRows(root);
  assert.equal(
    controlByName(rows[0], "model-0-preset").value,
    "deepseek/deepseek-v4-flash",
  );
  assert.equal(
    controlByName(rows[1], "model-1-preset").value,
    "deepseek/deepseek-v4-pro",
  );

  await findTag(root, "FORM").dispatch("submit");
  assert.deepEqual(
    saved.config.models.map(({ id, canonical_model_id }) => ({
      id,
      canonical_model_id,
    })),
    [
      {
        id: "azure-ds-v4-flash",
        canonical_model_id: "deepseek/deepseek-v4-flash",
      },
      {
        id: "azure-ds-v4-pro",
        canonical_model_id: "deepseek/deepseek-v4-pro",
      },
    ],
  );
});

test("an unmatched committed model prompts for a template and applies it without changing the ID", async (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft();
  draft.config.models = [{
    id: "",
    context_window: "",
    max_output_tokens: "",
    supports_vision: "",
  }];

  renderProfileEditor(root, draft, {
    save: async () => {},
    cancel: () => {},
    generate: () => {},
  });

  const row = modelRows(root)[0];
  const id = controlByField(row, "id");
  id.value = "private-glm-wrapper";
  await id.dispatch("change");

  const dialog = findClass(root, "model-template-dialog");
  assert.ok(findText(dialog, "未能为 private-glm-wrapper 自动匹配参数"));
  const template = controlByName(dialog, "model_template_0");
  template.value = "zhipuai/glm-5.2";
  await template.dispatch("input");
  await buttonByText(dialog, "应用所选模板").dispatch("click");

  assert.equal(elementsByClass(root, "model-template-dialog").length, 0);
  assert.equal(id.value, "private-glm-wrapper");
  assert.equal(controlByField(row, "context_window").value, "1000000");
  assert.equal(
    controlByField(row, "input_price_micro_usd_per_million").value,
    "1.4",
  );
  assert.equal(
    controlByField(row, "cache_read_price_micro_usd_per_million").value,
    "0.26",
  );
});

test("initial render fills blank fields from an exact recommendation", (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft();
  draft.config.models = [{
    id: "glm-5.2",
    context_window: "",
    max_output_tokens: "",
    supports_vision: "",
  }];

  renderProfileEditor(root, draft, {
    save: async () => {},
    cancel: () => {},
    generate: () => {},
  });

  const row = modelRows(root)[0];
  assert.equal(controlByField(row, "context_window").value, "1000000");
  assert.equal(controlByField(row, "max_output_tokens").value, "131072");
  assert.equal(controlByField(row, "supports_vision").value, "false");
  assert.equal(
    controlByField(row, "input_price_micro_usd_per_million").value,
    "1.4",
  );
  assert.equal(
    controlByField(row, "cache_read_price_micro_usd_per_million").value,
    "0.26",
  );
  assert.ok(findText(row, "GLM-5.2"));
  assert.ok(findText(row, "精确匹配"));
});

test("switching exact models preserves manually edited fields and replaces still-owned values", async (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft();
  draft.config.models = [{
    id: "",
    context_window: "",
    max_output_tokens: "",
    supports_vision: "",
  }];

  renderProfileEditor(root, draft, {
    save: async () => {},
    cancel: () => {},
    generate: () => {},
  });

  const row = modelRows(root)[0];
  const id = controlByField(row, "id");
  const context = controlByField(row, "context_window");
  const output = controlByField(row, "max_output_tokens");
  const vision = controlByField(row, "supports_vision");

  id.value = "glm-5.2";
  await id.dispatch("change");
  context.value = "900000";
  await context.dispatch("input");

  id.value = "gpt-5.6-sol";
  await id.dispatch("input");
  assert.equal(context.value, "900000");
  assert.equal(output.value, "128000");
  assert.equal(vision.value, "true");

  await id.dispatch("change");
  assert.equal(context.value, "900000");
  assert.equal(output.value, "128000");
  assert.equal(vision.value, "true");
});

test("switching to a recommendation without output clears the old still-owned output", async (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft();
  draft.config.models = [{
    id: "",
    context_window: "",
    max_output_tokens: "",
    supports_vision: "",
  }];

  renderProfileEditor(root, draft, {
    save: async () => {},
    cancel: () => {},
    generate: () => {},
  });

  const row = modelRows(root)[0];
  const id = controlByField(row, "id");
  id.value = "glm-5.2";
  await id.dispatch("change");
  assert.equal(controlByField(row, "max_output_tokens").value, "131072");

  id.value = "command-a-translate-08-2025";
  await id.dispatch("change");
  assert.equal(controlByField(row, "context_window").value, "8000");
  assert.equal(controlByField(row, "max_output_tokens").value, "");
  assert.equal(controlByField(row, "supports_vision").value, "false");
});

test("committing an unmatched ID clears old automatic values but preserves manual edits", async (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft();
  draft.config.models = [{
    id: "",
    context_window: "",
    max_output_tokens: "",
    supports_vision: "",
  }];

  renderProfileEditor(root, draft, {
    save: async () => {},
    cancel: () => {},
    generate: () => {},
  });

  const row = modelRows(root)[0];
  const id = controlByField(row, "id");
  const context = controlByField(row, "context_window");
  const output = controlByField(row, "max_output_tokens");
  const vision = controlByField(row, "supports_vision");
  id.value = "glm-5.2";
  await id.dispatch("change");
  context.value = "900000";
  await context.dispatch("change");

  id.value = "custom-unlisted-model";
  await id.dispatch("input");
  assert.equal(context.value, "900000");
  assert.equal(output.value, "131072");
  assert.equal(vision.value, "false");

  await id.dispatch("change");
  assert.equal(context.value, "900000");
  assert.equal(output.value, "");
  assert.equal(vision.value, "");

  await id.dispatch("blur");
  assert.equal(context.value, "900000");
  assert.equal(output.value, "");
  assert.equal(vision.value, "");
});

test("committing a family candidate clears old automatic values until its named action is used", async (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft();
  draft.config.models = [{
    id: "",
    context_window: "",
    max_output_tokens: "",
    supports_vision: "",
  }];

  renderProfileEditor(root, draft, {
    save: async () => {},
    cancel: () => {},
    generate: () => {},
  });

  const row = modelRows(root)[0];
  const id = controlByField(row, "id");
  const context = controlByField(row, "context_window");
  const output = controlByField(row, "max_output_tokens");
  const vision = controlByField(row, "supports_vision");
  id.value = "glm-5.2";
  await id.dispatch("change");

  id.value = "gpt-5.6-sol-preview";
  await id.dispatch("input");
  assert.equal(context.value, "1000000");
  assert.equal(output.value, "131072");
  assert.equal(vision.value, "false");

  await id.dispatch("change");
  assert.equal(context.value, "");
  assert.equal(output.value, "");
  assert.equal(vision.value, "");

  await buttonByText(row, "应用 GPT-5.6 Sol 推荐值").dispatch("click");
  assert.equal(id.value, "gpt-5.6-sol-preview");
  assert.equal(context.value, "1050000");
  assert.equal(output.value, "128000");
  assert.equal(vision.value, "true");
});

test("automatic-value ownership survives model-row add and delete rerenders", async (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft();
  draft.config.models = [{
    id: "",
    context_window: "",
    max_output_tokens: "",
    supports_vision: "",
  }];

  renderProfileEditor(root, draft, {
    save: async () => {},
    cancel: () => {},
    generate: () => {},
  });

  let row = modelRows(root)[0];
  let id = controlByField(row, "id");
  id.value = "glm-5.2";
  await id.dispatch("change");

  await buttonByText(root, "添加模型").dispatch("click");
  row = modelRows(root)[0];
  id = controlByField(row, "id");
  id.value = "gpt-5.6-sol";
  await id.dispatch("change");
  assert.equal(controlByField(row, "context_window").value, "1050000");
  assert.equal(controlByField(row, "max_output_tokens").value, "128000");
  assert.equal(controlByField(row, "supports_vision").value, "true");

  await buttonsByText(root, "删除模型")[1].dispatch("click");
  row = modelRows(root)[0];
  id = controlByField(row, "id");
  id.value = "command-a-translate-08-2025";
  await id.dispatch("change");
  assert.equal(controlByField(row, "context_window").value, "8000");
  assert.equal(controlByField(row, "max_output_tokens").value, "");
  assert.equal(controlByField(row, "supports_vision").value, "false");
});

test("family candidates show complete provenance and expose a named apply action", async (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft();
  draft.config.models = [{
    id: "gpt-5.6-sol-preview",
    context_window: "777K",
    max_output_tokens: "",
    supports_vision: "",
  }];

  renderProfileEditor(root, draft, {
    save: async () => {},
    cancel: () => {},
    generate: () => {},
  });

  const row = modelRows(root)[0];
  assert.equal(controlByField(row, "context_window").value, "777K");
  assert.equal(controlByField(row, "max_output_tokens").value, "");
  assert.equal(controlByField(row, "supports_vision").value, "");
  const cards = elementsByClass(row, "model-recommendation-card");
  assert.equal(cards.length, 1);
  assert.ok(cards.length <= 5);
  assert.ok(findText(cards[0], "GPT-5.6 Sol"));
  assert.ok(findText(cards[0], "OpenAI"));
  assert.ok(findText(cards[0], "生命周期：稳定"));
  assert.ok(findText(cards[0], "匹配：同系列候选"));
  assert.ok(findText(cards[0], "来源：Models.dev"));
  assert.ok(findText(cards[0], `获取：${MODELS_DEV_SOURCE.retrieved}`));
  assert.ok(findText(cards[0], "更新：2026-07-09"));
  assert.ok(findText(cards[0], "上下文窗口：1050000"));
  assert.ok(findText(cards[0], "最大输出 Token：128000"));
  assert.ok(findText(cards[0], "视觉：是"));

  await buttonByText(row, "应用 GPT-5.6 Sol 推荐值").dispatch("click");
  assert.equal(controlByField(row, "id").value, "gpt-5.6-sol-preview");
  assert.equal(controlByField(row, "context_window").value, "777K");
  assert.equal(controlByField(row, "max_output_tokens").value, "128000");
  assert.equal(controlByField(row, "supports_vision").value, "true");
});

test("a candidate button keeps its DOM identity through change and blur before direct click", async (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft();
  draft.config.models = [{
    id: "",
    context_window: "",
    max_output_tokens: "",
    supports_vision: "",
  }];

  renderProfileEditor(root, draft, {
    save: async () => {},
    cancel: () => {},
    generate: () => {},
  });

  const row = modelRows(root)[0];
  const id = controlByField(row, "id");
  id.value = "gpt-5.6-sol-preview";
  await id.dispatch("input");
  const recommendation = findClass(row, "model-recommendation");
  const card = findClass(row, "model-recommendation-card");
  const apply = buttonByText(row, "应用 GPT-5.6 Sol 推荐值");

  await id.dispatch("input");
  assert.equal(findClass(row, "model-recommendation"), recommendation);
  assert.equal(findClass(row, "model-recommendation-card"), card);
  assert.equal(
    buttonByText(row, "应用 GPT-5.6 Sol 推荐值"),
    apply,
  );

  await apply.dispatch("pointerdown");
  await id.dispatch("change");
  assert.equal(findClass(row, "model-recommendation-card"), card);
  assert.equal(
    buttonByText(row, "应用 GPT-5.6 Sol 推荐值"),
    apply,
  );

  await id.dispatch("blur");
  assert.equal(findClass(row, "model-recommendation-card"), card);
  assert.equal(
    buttonByText(row, "应用 GPT-5.6 Sol 推荐值"),
    apply,
  );
  assert.equal(
    descendants(root).some((element) => element.tagName === "DIALOG"),
    false,
  );

  await apply.dispatch("click");
  assert.equal(controlByField(row, "context_window").value, "1050000");
  assert.equal(controlByField(row, "max_output_tokens").value, "128000");
  assert.equal(controlByField(row, "supports_vision").value, "true");
});

test("model IDs announce recommendation updates through a controlled live region", (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft();
  draft.config.models = [{
    id: "",
    context_window: "",
    max_output_tokens: "",
    supports_vision: "",
  }];

  renderProfileEditor(root, draft, {
    save: async () => {},
    cancel: () => {},
    generate: () => {},
  });

  const row = modelRows(root)[0];
  const id = controlByField(row, "id");
  const recommendation = findClass(row, "model-recommendation");
  assert.equal(
    recommendation.id,
    "profile-model-0-id-recommendations",
  );
  assert.equal(id.getAttribute("aria-controls"), recommendation.id);
  assert.equal(recommendation.getAttribute("aria-live"), "polite");
  assert.equal(recommendation.getAttribute("role"), null);
});

test("recommendation details label every matcher kind in Chinese", async (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft();
  draft.config.models = [{
    id: "",
    context_window: "",
    max_output_tokens: "",
    supports_vision: "",
  }];

  renderProfileEditor(root, draft, {
    save: async () => {},
    cancel: () => {},
    generate: () => {},
  });

  const row = modelRows(root)[0];
  const id = controlByField(row, "id");
  for (const [modelID, label] of [
    ["glm-5.2", "精确匹配"],
    ["zhipuai/glm-5.2", "别名匹配"],
    ["claude-glm-5.2", "兼容别名匹配"],
    ["proxy/glm-5.2", "包装器匹配"],
    ["claude-haiku-4-5-20251231", "快照匹配"],
    ["gpt-5.6-sol-preview", "同系列候选"],
  ]) {
    id.value = modelID;
    await id.dispatch("input");
    assert.ok(findText(row, `匹配：${label}`), modelID);
  }
});

test("unknown recommendation fields state that reliable data is unavailable", (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft();
  draft.config.models = [{
    id: "command-a-translate-08-2025",
    context_window: "",
    max_output_tokens: "",
    supports_vision: "",
  }];

  renderProfileEditor(root, draft, {
    save: async () => {},
    cancel: () => {},
    generate: () => {},
  });

  const row = modelRows(root)[0];
  assert.ok(findText(row, "最大输出 Token：暂无可靠数据"));
  assert.equal(controlByField(row, "max_output_tokens").value, "");
});

test("an unmatched model clears candidates without errors and remains saveable", async (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft();
  draft.config.models = [{
    id: "custom-unlisted-model",
    context_window: "128K",
    max_output_tokens: "32K",
    supports_vision: false,
  }];
  let saved;

  renderProfileEditor(root, draft, {
    save: async (payload) => {
      saved = payload;
    },
    cancel: () => {},
    generate: () => {},
  });

  const row = modelRows(root)[0];
  const id = controlByField(row, "id");
  id.value = "no-such-model";
  await id.dispatch("input");
  assert.equal(elementsByClass(row, "model-recommendation-card").length, 0);
  await findTag(root, "FORM").dispatch("submit");

  assert.equal(saved.config.models[0].id, "no-such-model");
  assert.equal(saved.config.models[0].context_window, 128000);
  assert.equal(saved.config.models[0].max_output_tokens, 32000);
  assert.equal(saved.config.models[0].supports_vision, false);
  assert.equal(findClass(root, "error-banner").hidden, true);
});

test("model editor rejects invalid rows inline before save", async (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft();
  draft.config.models = [{
    id: "too-large-output",
    context_window: "128K",
    max_output_tokens: "256K",
    supports_vision: false,
  }];
  let saves = 0;

  renderProfileEditor(root, draft, {
    save: async () => {
      saves += 1;
    },
    cancel: () => {},
    generate: () => {},
  });

  await findTag(root, "FORM").dispatch("submit");

  assert.equal(saves, 0);
  const alert = findClass(root, "error-banner");
  assert.equal(alert.hidden, false);
  assert.match(alert.textContent, /最大输出 Token 必须小于上下文窗口/);
});

test("unlisted model policy offers approved Chinese choices and persists selection", async (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft();
  draft.config.vision.unlisted_model_policy = "enhance";
  let saved;

  renderProfileEditor(root, draft, {
    save: async (payload) => {
      saved = payload;
    },
    cancel: () => {},
    generate: () => {},
  });

  const policy = controlByName(root, "vision_unlisted_model_policy");
  assert.equal(policy.value, "enhance");
  assert.deepEqual(
    policy.children.map((option) => [option.value, option.textContent]),
    [
      ["bypass", "默认视为支持视觉，不增强"],
      ["enhance", "默认视为不支持视觉，使用增强"],
    ],
  );
  policy.value = "bypass";
  await findTag(root, "FORM").dispatch("submit");
  assert.equal(saved.config.vision.unlisted_model_policy, "bypass");
});

test("editor Generate rejection stays inline without replacing the editor", async (t) => {
  const root = installFakeDOM(t);
  const data = profileListFixture();
  const draft = profileDraft(data.profiles[0], data.default_profile_id);
  renderProfileEditor(root, draft, {
    save: async () => {},
    cancel: () => {},
    generate: async () => {
      throw new Error("generator unavailable");
    },
  });
  const editor = findTag(root, "FORM");

  await buttonByText(root, "生成配置").dispatch("click");

  const alert = findClass(root, "error-banner");
  assert.equal(alert.getAttribute("role"), "alert");
  assert.equal(alert.hidden, false);
  assert.equal(alert.textContent, "generator unavailable");
  assert.equal(findTag(root, "FORM"), editor);
  assert.equal(findAllTags(root, "DIALOG").length, 0);
});

test("checking make_default enables and locks a disabled Profile", async (t) => {
  const root = installFakeDOM(t);
  const data = profileListFixture();
  const draft = profileDraft(data.profiles[2], data.default_profile_id);

  renderProfileEditor(root, draft, {
    save: async () => {},
    cancel: () => {},
    generate: () => {},
  });

  const enabled = controlByName(root, "enabled");
  const makeDefault = controlByName(root, "make_default");
  assert.equal(enabled.checked, false);
  assert.equal(enabled.disabled, false);

  makeDefault.checked = true;
  await makeDefault.dispatch("change");

  assert.equal(enabled.checked, true);
  assert.equal(enabled.disabled, true);
});

test("default submission cannot contain enabled false even with inconsistent controls", async (t) => {
  const root = installFakeDOM(t);
  const data = profileListFixture();
  const draft = profileDraft(data.profiles[2], data.default_profile_id);
  let saved;

  renderProfileEditor(root, draft, {
    save: async (payload) => {
      saved = payload;
    },
    cancel: () => {},
    generate: () => {},
  });

  controlByName(root, "make_default").checked = true;
  controlByName(root, "enabled").checked = false;
  await findTag(root, "FORM").dispatch("submit");

  assert.equal(saved.make_default, true);
  assert.equal(saved.enabled, true);
});

test("switching the editor to OpenAI preserves vision and selects Chat Completions", async (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft("anthropic");
  draft.slug = "openai";
  draft.display_name = "OpenAI";
  draft.config.upstream = "https://openai.example";
  draft.config.vision.enabled = true;
  let saved;

  renderProfileEditor(root, draft, {
    save: async (payload) => {
      saved = payload;
    },
    cancel: () => {},
    generate: () => {},
  });

  const protocol = controlByName(root, "protocol");
  protocol.value = "openai";
  await protocol.dispatch("change");

  assert.equal(findClass(root, "vision-controls").hidden, false);
  assert.equal(controlByName(root, "vision_enabled").checked, true);
  assert.equal(controlByName(root, "vision_enabled").disabled, false);
  assert.equal(controlByName(root, "vision_transport").value, "openai_chat_completions");

  await findTag(root, "FORM").dispatch("submit");
  assert.equal(saved.config.protocol, "openai");
  assert.equal(saved.config.vision.enabled, true);
  assert.equal(saved.config.vision.transport, "openai_chat_completions");
});

test("old OpenAI Profile without transport loads and saves the default", async (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft("openai");
  delete draft.config.vision.transport;
  let saved;

  renderProfileEditor(root, draft, {
    save: async (payload) => {
      saved = payload;
    },
    cancel: () => {},
    generate: () => {},
  });

  assert.equal(controlByName(root, "vision_transport").value, "openai_chat_completions");
  await findTag(root, "FORM").dispatch("submit");
  assert.equal(saved.config.vision.transport, "openai_chat_completions");
});

test("retry editor add remove up and down actions keep visible first-match order", async (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft();
  draft.config.overload_rules = [
    {
      status: 503,
      body_contains: "first",
      max_retries: 1,
      delay: "1s",
      jitter: "0s",
    },
    {
      status: 429,
      body_contains: "second",
      max_retries: 2,
      delay: "2s",
      jitter: "0s",
    },
  ];

  renderProfileEditor(root, draft, {
    save: async () => {},
    cancel: () => {},
    generate: () => {},
  });

  assert.deepEqual(retryBodies(root), ["first", "second"]);
  await buttonsByText(root, "上移")[1].dispatch("click");
  assert.deepEqual(retryBodies(root), ["second", "first"]);
  await buttonsByText(root, "下移")[0].dispatch("click");
  assert.deepEqual(retryBodies(root), ["first", "second"]);
  await buttonByText(root, "添加规则").dispatch("click");
  assert.equal(retryBodies(root).length, 3);
  await buttonsByText(root, "删除")[2].dispatch("click");
  assert.deepEqual(retryBodies(root), ["first", "second"]);
});

test("copy action uses a native dialog with editable non-secret fields", async (t) => {
  const root = installFakeDOM(t);
  const data = profileListFixture();
  const copies = [];
  renderProfileList(root, data, {
    edit: () => {},
    generate: () => {},
    copy: async (profile, body) => copies.push([profile.id, body]),
    setDefault: async () => {},
    toggle: async () => {},
    delete: async () => {},
  });

  await buttonByText(profileCard(root, "secondary"), "复制").dispatch("click");
  const dialog = openDialog(root);
  assert.equal(controlByName(dialog, "copy-display-name").value, "Secondary 副本");
  assert.equal(controlByName(dialog, "copy-slug").value, "secondary-copy");
  controlByName(dialog, "copy-display-name").value = "Secondary Clone";
  controlByName(dialog, "copy-slug").value = "secondary-clone";
  await findTag(dialog, "FORM").dispatch("submit");

  assert.deepEqual(copies, [
    [
      2,
      {
        display_name: "Secondary Clone",
        slug: "secondary-clone",
      },
    ],
  ]);
});

test("delete dialog blocks the only Profile and requires an enabled replacement for the default", async (t) => {
  const root = installFakeDOM(t);
  const only = profileFixture({ id: 9, slug: "only", display_name: "Only" });
  renderProfileList(
    root,
    { default_profile_id: 9, profiles: [only] },
    {
      edit: () => {},
      generate: () => {},
      copy: async () => {},
      setDefault: async () => {},
      toggle: async () => {},
      delete: async () => {
        throw new Error("must not delete");
      },
    },
  );
  assert.equal(buttonByText(root, "删除").disabled, true);
  assert.equal(findAllTags(root, "DIALOG").length, 0);

  const data = profileListFixture();
  const deletes = [];
  renderProfileList(root, data, {
    edit: () => {},
    generate: () => {},
    copy: async () => {},
    setDefault: async () => {},
    toggle: async () => {},
    delete: async (profile, replacementId) =>
      deletes.push([profile.id, replacementId]),
  });
  await buttonByText(profileCard(root, "primary"), "删除").dispatch("click");

  const dialog = openDialog(root);
  assert.ok(findText(dialog, "Primary"));
  assert.ok(findText(dialog, "primary"));
  const replacement = controlByName(dialog, "replacement_default_id");
  assert.deepEqual(
    replacement.children.map((option) => option.value),
    ["", "2"],
  );
  const submit = buttonByText(dialog, "删除 Profile");
  assert.equal(submit.disabled, true);
  replacement.value = "2";
  await replacement.dispatch("change");
  assert.equal(submit.disabled, false);
  await findTag(dialog, "FORM").dispatch("submit");
  assert.deepEqual(deletes, [[1, 2]]);
});

test("authenticated app opens the four-step Profile creation wizard and persists its first step", async (t) => {
  const root = installFakeDOM(t);
	const timeouts = [];
	const originalSetTimeout = globalThis.setTimeout;
	globalThis.setTimeout = (callback, delay) => {
		timeouts.push({ callback, delay });
		return timeouts.length;
	};
	t.after(() => {
		globalThis.setTimeout = originalSetTimeout;
	});
  let listCalls = 0;
  const creates = [];
  const client = {
    session: async () => ({
      username: "admin",
      must_change_password: false,
    }),
    listProfiles: async () => {
      listCalls += 1;
      return { default_profile_id: 0, profiles: [] };
    },
    createProfile: async (payload) => {
      creates.push(payload);
			return { ...structuredClone(payload), id: 7 };
    },
		updateProfile: async (_id, payload) => ({ ...structuredClone(payload), id: 7 }),
		profileModels: async () => ({
			runtime_state: { revision: 1, model_catalog_revision: 1 },
			models: [],
		}),
  };

  await bootstrap({ root, client, path: "/_admin/profiles" });
  await buttonByText(root, "创建第一个 Profile").dispatch("click");
	assert.deepEqual(
		elementsByClass(root, "profile-create-step").map((item) => item.textContent),
		["连接上游", "录入模型", "智能路由", "完成"],
	);
	assert.equal(controls(root).some((control) => control.name === "model_batch_ids"), false);
  assert.equal(controlByName(root, "enabled").checked, true);
  assert.equal(controlByName(root, "enabled").disabled, true);
  controlByName(root, "display_name").value = "New Profile";
  controlByName(root, "slug").value = "new-profile";
  controlByName(root, "upstream").value = "https://new.example";
  await findTag(root, "FORM").dispatch("submit");

	assert.equal(listCalls, 1);
  assert.equal(creates.length, 1);
  assert.equal(creates[0].display_name, "New Profile");
  assert.equal(creates[0].slug, "new-profile");
  assert.equal(creates[0].make_default, true);
  assert.equal(creates[0].config.upstream, "https://new.example");
  assert.equal(typeof creates[0].config.vision.max_tokens, "number");
	assert.ok(controlByName(root, "model_batch_ids"));
	assert.ok(findText(root, "保存并继续"));
	await buttonByText(root, "暂不录入").dispatch("click");
	assert.ok(findText(root, "智能路由仅处理 model=auto"));
	await findTag(root, "FORM").dispatch("submit");
	const success = findClass(root, "app-flash");
	assert.equal(success.hidden, false);
	assert.equal(success.getAttribute("role"), "status");
	assert.equal(success.textContent, "Profile 保存成功。");
	assert.equal(success.parentNode.className, "desktop-stage");
	assert.equal(timeouts.length, 1);
	assert.equal(timeouts[0].delay, 4000);
	timeouts[0].callback();
	assert.equal(success.hidden, true);
	assert.equal(success.textContent, "");
});

test("authenticated Profile detail route mounts only its selected settings section", async (t) => {
  const root = installFakeDOM(t);
  const profile = profileFixture({
    id: 7,
    slug: "coding",
    display_name: "Coding",
  });
  await bootstrap({
    root,
    path: "/_admin/profiles/7/models",
    client: {
      session: async () => ({ username: "admin", must_change_password: false }),
      getProfile: async () => profile,
      listProfiles: async () => ({ default_profile_id: 7, profiles: [profile] }),
    },
  });

  assert.equal(findAllTags(root, "H1")[0].textContent, "Coding");
  assert.equal(linkByText(root, "模型").getAttribute("aria-current"), "page");
  assert.ok(findText(root, "模型能力"));
  assert.equal(
    descendants(root).some((element) => element.textContent === "基础配置"),
    false,
  );
  assert.equal(findText(root, "视觉增强"), linkByText(root, "视觉增强"));
});

test("authenticated v2 model page previews and imports a batch with advancing revisions", async (t) => {
  const root = installFakeDOM(t);
  const profile = autoLifecycleDraft();
  profile.config.version = 2;
  let revision = 4;
  const models = profile.config.models.map((capability) => ({
    model_id: capability.id,
    status: "available",
    capability: structuredClone(capability),
  }));
  const calls = [];
  const policy = structuredClone(profile.config.auto_routing.strategy);
  policy.roles = {
    participants: ["fast", "strong"],
    strong_baseline_model: "strong",
    task_analyzer_model: "fast",
  };
  const directory = () => ({
    runtime_state: { revision, model_catalog_revision: revision },
    models: structuredClone(models),
  });
  const client = {
    session: async () => ({ username: "admin", must_change_password: false }),
    getProfile: async () => profile,
    listProfiles: async () => ({ default_profile_id: 7, profiles: [profile] }),
    routingPolicy: async () => ({
      runtime_state: { revision, model_catalog_revision: revision },
      active: { id: 11, policy },
      history: [],
    }),
    profileModels: async () => directory(),
    addProfileModel: async (_profileID, expectedRevision, modelID, capability) => {
      calls.push({ expectedRevision, modelID });
      assert.equal(expectedRevision, revision);
      models.push({ model_id: modelID, status: "available", capability });
      revision += 1;
      return directory();
    },
  };

  await bootstrap({ root, path: "/_admin/profiles/7/models", client });
  controlByName(root, "model_batch_ids").value = "qwen-flash\nqwen-max";
  await buttonByText(root, "预览批量导入").dispatch("click");
  assert.ok(buttonByText(root, "确认导入 2 个模型"));
  await buttonByText(root, "确认导入 2 个模型").dispatch("click");

  assert.deepEqual(calls, [
    { expectedRevision: 4, modelID: "qwen-flash" },
    { expectedRevision: 5, modelID: "qwen-max" },
  ]);
  assert.ok(findText(root, "qwen-flash"));
  assert.ok(findText(root, "qwen-max"));
});

test("authenticated v2 routing page shows the one active Policy without draft or canary controls", async (t) => {
  const root = installFakeDOM(t);
  const profile = autoLifecycleDraft();
  profile.config.version = 2;
  const policy = structuredClone(profile.config.auto_routing.strategy);
  policy.roles = {
    participants: ["fast", "strong"],
    strong_baseline_model: "strong",
    task_analyzer_model: "fast",
  };
  const overview = {
    runtime_state: { revision: 4, model_catalog_revision: 2 },
    active: { id: 11, policy },
    history: [{ id: 11, policy, change_kind: "apply", change_reason: "initial" }],
  };
  const client = {
    session: async () => ({ username: "admin", must_change_password: false }),
    getProfile: async () => profile,
    listProfiles: async () => ({ default_profile_id: 7, profiles: [profile] }),
    routingPolicy: async () => overview,
    profileModels: async () => ({
      runtime_state: overview.runtime_state,
      models: profile.config.models.map((capability) => ({
        model_id: capability.id,
        status: "available",
        capability,
      })),
    }),
  };

  await bootstrap({ root, path: "/_admin/profiles/7/routing", client });
  assert.ok(findText(root, "线上 Routing Policy"));
  assert.ok(findText(root, "立即生效"));
  assert.ok(findText(root, "当前策略概览"));
  assert.ok(findText(root, "20260802-001"));
  assert.ok(buttonByText(root, "手动调整"));
  assert.equal(buttonsByText(root, "保存并立即生效").length, 0);
  assert.equal(descendants(root).some((element) => element.textContent === "载入草稿"), false);
  assert.equal(descendants(root).some((element) => element.textContent === "开始灰度"), false);
});

test("authenticated v2 routing page saves a complete Policy immediately", async (t) => {
  const root = installFakeDOM(t);
  const profile = autoLifecycleDraft();
  profile.config.version = 2;
  const policy = structuredClone(profile.config.auto_routing.strategy);
  policy.roles = {
    participants: ["fast", "strong"],
    strong_baseline_model: "strong",
    task_analyzer_model: "fast",
  };
	policy.session_lock_token_threshold = 100000;
	policy.routes[0].candidates[0].production_eligible = false;
  const overview = {
    runtime_state: { revision: 4, model_catalog_revision: 2 },
    active: { id: 11, policy },
    history: [{ id: 11, policy, change_kind: "apply", change_reason: "initial" }],
  };
  const applications = [];
  const client = {
    session: async () => ({ username: "admin", must_change_password: false }),
    getProfile: async () => profile,
    listProfiles: async () => ({ default_profile_id: 7, profiles: [profile] }),
    routingPolicy: async () => overview,
    profileModels: async () => ({
      runtime_state: overview.runtime_state,
      models: profile.config.models.map((capability) => ({
        model_id: capability.id,
        status: "available",
        capability,
      })),
    }),
    applyRoutingPolicy: async (...args) => {
      applications.push(args);
      return overview;
    },
  };

  await bootstrap({ root, path: "/_admin/profiles/7/routing", client });
  await buttonByText(root, "手动调整").dispatch("click");
	await buttonByText(root, "高级设置").dispatch("click");
	controlByLabel(root, "Session 锁定 Token 阈值").value = "250000";
	controlByLabel(root, "允许生产流量").checked = true;
	assert.equal(descendants(root).some((item) => item.textContent.includes("长上下文阈值（%）")), false);
	assert.equal(descendants(root).some((item) => item.textContent.includes("结构化输出按高风险处理")), false);
  await buttonByText(root, "4 确认生效").dispatch("click");
  await buttonByText(root, "保存并立即生效").dispatch("click");

	const expected = structuredClone(policy);
	expected.session_lock_token_threshold = 250000;
	expected.routes[0].candidates[0].production_eligible = true;
	for (const route of expected.routes) {
		for (const candidate of route.candidates || []) {
			candidate.production_eligible = Boolean(candidate.production_eligible);
		}
	}
	assert.deepEqual(applications, [[7, 4, expected, ""]]);
});

test("authenticated v2 routing page keeps the complete intelligent generation workflow", async (t) => {
  const root = installFakeDOM(t);
  const profile = autoLifecycleDraft();
  profile.config.version = 2;
  const policy = structuredClone(profile.config.auto_routing.strategy);
  policy.roles = {
    participants: ["fast", "strong"],
    strong_baseline_model: "strong",
    task_analyzer_model: "fast",
  };
  const overview = {
    runtime_state: { revision: 4, model_catalog_revision: 2 },
    active: { id: 11, policy },
    history: [{ id: 11, policy, change_kind: "apply", change_reason: "initial" }],
  };
  const generationCalls = [];
  const generatedPolicy = structuredClone(policy);
  generatedPolicy.alias = "质量优先";
  const client = {
    session: async () => ({ username: "admin", must_change_password: false }),
    getProfile: async () => profile,
    listProfiles: async () => ({ default_profile_id: 7, profiles: [profile] }),
    routingPolicy: async () => overview,
    profileModels: async () => ({
      runtime_state: overview.runtime_state,
      models: profile.config.models.map((capability) => ({
        model_id: capability.id,
        status: "available",
        capability,
      })),
    }),
    generateRoutingPolicy: async (profileID, intent) => {
      generationCalls.push([profileID, intent]);
      return {
        policy: generatedPolicy,
        confidence: {
          level: "medium",
          local_candidates: 2,
          external_candidates: 4,
          provisional_candidates: 1,
        },
        source_digest: "source-digest-1234567890",
        explanations: [{ code: "role", message: "根据本地证据选择 strong。" }],
      };
    },
  };

  await bootstrap({ root, path: "/_admin/profiles/7/routing", client });
  await buttonByText(root, "智能生成策略").dispatch("click");
  assert.ok(findText(root, "智能生成策略"));
  controlByName(root, "routing_policy_generation_objective").value = "quality";
  controlByName(root, "routing_policy_generation_max_cost_usd").value = "0.25";
  controlByName(root, "routing_policy_generation_latency_ms").value = "900";
  controlByName(root, "routing_policy_generation_daily_eval_usd").value = "1.5";
  await buttonByText(root, "智能生成建议").dispatch("click");

  assert.deepEqual(generationCalls, [[7, {
    objective: "quality",
    participants: ["fast", "strong"],
    max_cost_per_request_micro_usd: 250000,
    latency_target_ms: 900,
    daily_eval_budget_micro_usd: 1500000,
  }]]);
  assert.ok(findText(root, "生成建议已完成"));
  assert.ok(findText(root, "根据本地证据选择 strong。"));
  assert.ok(descendants(root).some((element) => element.textContent.includes("本地候选 2")));
  assert.ok(findText(root, "与当前策略的差异"));
  assert.ok(descendants(root).some((element) => element.textContent === "显示名称"));
  assert.ok(descendants(root).some((element) => element.textContent === "质量优先"));
  assert.ok(buttonByText(root, "载入并编辑"));
  assert.equal(descendants(root).some((element) => element.textContent === "1 模型与角色"), false);

  await buttonByText(root, "载入并编辑").dispatch("click");
  assert.ok(findText(root, "1 模型与角色"));
  assert.ok(findText(root, "模型角色"));
});

test("authenticated help route renders the built-in intelligent routing guide", async (t) => {
  const root = installFakeDOM(t);
  await bootstrap({
    root,
    path: "/_admin/help/intelligent-routing",
    client: {
      session: async () => ({ username: "admin", must_change_password: false }),
    },
  });

  assert.equal(findAllTags(root, "H1")[0].textContent, "帮助");
  assert.equal(linkByText(root, "帮助").getAttribute("aria-current"), "page");
  assert.equal(
    linkByText(root, "Routing Policy").getAttribute("aria-current"),
    "page",
  );
  assert.ok(findText(root, "线上 Routing Policy"));
	assert.ok(findText(root, "保存并立即生效"));
  assert.ok(findText(root, "Production 与 Shadow"));
});

test("authenticated help overview renders the secondary help navigation", async (t) => {
  const root = installFakeDOM(t);
  await bootstrap({
    root,
    path: "/_admin/help/overview",
    client: {
      session: async () => ({ username: "admin", must_change_password: false }),
    },
  });

  assert.equal(findAllTags(root, "H1")[0].textContent, "帮助");
  assert.equal(linkByText(root, "帮助").getAttribute("href"), "/_admin/help/overview");
  assert.equal(
    linkByText(root, "快速开始").getAttribute("aria-current"),
    "page",
  );
  assert.ok(findText(root, "推荐操作顺序"));
  assert.ok(findText(root, "显式模型与 model=auto"));
});

test("model detail loads the active server catalog before rendering recommendations", async (t) => {
  const root = installFakeDOM(t);
  const originalCatalog = currentModelCatalog();
  t.after(() => installModelCatalog(originalCatalog));
  const profile = profileFixture({
    id: 7,
    slug: "coding",
    display_name: "Coding",
    config: {
      version: 1,
      protocol: "anthropic",
      upstream: "https://upstream.example",
      models: [{ id: "remote-alpha" }],
      vision: { enabled: false, model: "" },
      overload_rules: [],
    },
  });
  let catalogCalls = 0;
  await bootstrap({
    root,
    path: "/_admin/profiles/7/models",
    client: {
      session: async () => ({ username: "admin", must_change_password: false }),
      getProfile: async () => profile,
      listProfiles: async () => ({ default_profile_id: 7, profiles: [profile] }),
      modelCatalog: async () => {
        catalogCalls++;
        return remoteCatalogFixture();
      },
    },
  });

  assert.equal(catalogCalls, 1);
  assert.ok(findText(root, "Remote Alpha"));
});

test("saving one Profile detail section keeps unrelated configuration", async (t) => {
  const root = installFakeDOM(t);
  const profile = profileFixture({
    id: 7,
    slug: "coding",
    display_name: "Coding",
    config: {
      version: 1,
      protocol: "anthropic",
      upstream: "https://upstream.example",
      models: [{ id: "strong", context_window: 128000, supports_vision: true }],
      vision: {
        enabled: false,
        model: "strong",
        max_tokens: 4096,
        timeout: "45s",
        max_concurrency: 3,
        cache_ttl: "2h",
        cache_max_entries: 700,
        prompt: "保留这个提示词",
      },
      overload_rules: [{
        status: 529,
        body_contains: "busy",
        max_retries: 2,
        delay: "1s",
        jitter: "100ms",
      }],
    },
  });
  const updates = [];
  const client = {
    session: async () => ({ username: "admin", must_change_password: false }),
    getProfile: async () => profile,
    listProfiles: async () => ({ default_profile_id: 7, profiles: [profile] }),
    updateProfile: async (_id, payload) => updates.push(payload),
  };
  await bootstrap({ root, path: "/_admin/profiles/7/models", client });
  controlByName(root, "model-0-context-window").value = "256K";
  await findTag(root, "FORM").dispatch("submit");

  assert.equal(updates.length, 1);
  assert.equal(updates[0].config.models[0].context_window, 256000);
  assert.equal(updates[0].config.upstream, "https://upstream.example");
  assert.equal(updates[0].config.vision.prompt, "保留这个提示词");
  assert.equal(updates[0].config.vision.cache_ttl, "2h");
  assert.deepEqual(updates[0].config.overload_rules, profile.config.overload_rules);
  const success = findClass(root, "success-banner");
  assert.equal(success.hidden, false);
  assert.equal(success.getAttribute("role"), "status");
  assert.equal(success.textContent, "Profile 保存成功。");
});

test("Agent detail generates protocol-specific configuration without a model catalog", async (t) => {
  const root = installFakeDOM(t);
  const profile = profileFixture({ id: 7, slug: "coding", display_name: "Coding" });
  await bootstrap({
    root,
    path: "/_admin/profiles/7/agents",
    client: {
      session: async () => ({ username: "admin", must_change_password: false }),
      getProfile: async () => profile,
      listProfiles: async () => ({ default_profile_id: 7, profiles: [profile] }),
    },
  });

  await buttonByText(root, "生成配置").dispatch("click");
  const dialog = findAllTags(root, "DIALOG")[0];
  assert.ok(dialog.open);
  assert.deepEqual(
    controlByName(dialog, "generator-agent").children.map((option) => option.value),
    ["claude-code", "opencode-anthropic", "generic"],
  );
});

function profileListFixture() {
  return {
    default_profile_id: 1,
    profiles: [
      profileFixture({
        id: 1,
        slug: "primary",
        display_name: "Primary",
        config: {
          version: 1,
          protocol: "anthropic",
          upstream: "https://primary.example",
          vision: {
            enabled: true,
            model: "vision-primary",
            max_tokens: 3072,
            timeout: "90s",
            max_concurrency: 5,
            cache_ttl: "20m",
            cache_max_entries: 200,
            prompt: "Original prompt",
          },
          overload_rules: [
            {
              status: 503,
              body_contains: "overloaded",
              max_retries: 4,
              delay: "2s",
              jitter: "200ms",
            },
          ],
        },
        usage_30d: {
          requests: 7,
          input_tokens: 100,
          output_tokens: 50,
        },
      }),
      profileFixture({
        id: 2,
        slug: "secondary",
        display_name: "Secondary",
        config: {
          version: 1,
          protocol: "anthropic",
          upstream: "https://secondary.example",
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
      }),
      profileFixture({
        id: 3,
        slug: "disabled",
        display_name: "<img src=x onerror=alert(1)>",
        enabled: false,
        config: {
          version: 1,
          protocol: "openai",
          upstream: "https://disabled.example",
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
      }),
    ],
  };
}

function remoteCatalogFixture() {
  return {
    source: {
      name: "Models.dev",
      revision: "d".repeat(64),
      modelsSha256: "e".repeat(64),
      providersSha256: "f".repeat(64),
      retrieved: "2026-08-02",
    },
    models: [{
      id: "remote-alpha",
      canonicalId: "acme/remote-alpha",
      apiIds: [],
      aliases: ["acme/remote-alpha"],
      compatibilityAliases: [],
      provider: "Acme",
      name: "Remote Alpha",
      family: "remote-alpha",
      context_window: 128000,
      max_output_tokens: 16000,
      supports_vision: false,
      supports_tools: true,
      input_price_micro_usd_per_million: 100000,
      output_price_micro_usd_per_million: 400000,
      lifecycle: "stable",
      references: [],
    }],
  };
}

function profileFixture(overrides = {}) {
  const base = {
    id: 1,
    slug: "profile",
    display_name: "Profile",
    enabled: true,
    config: {
      version: 1,
      protocol: "anthropic",
      upstream: "https://profile.example",
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
      requests: 0,
      input_tokens: 0,
      output_tokens: 0,
    },
  };
  return {
    ...base,
    ...overrides,
    config: overrides.config || base.config,
    usage_30d: overrides.usage_30d || base.usage_30d,
  };
}

function autoLifecycleDraft() {
	const draft = defaultProfileDraft();
	draft.id = 7;
	draft.slug = "auto";
	draft.original_slug = "auto";
	draft.display_name = "Auto";
	draft.make_default = true;
	draft.config.models = [
		{
			id: "fast", context_window: 128000, max_output_tokens: 16000,
			supports_vision: false, supports_tools: true,
			supports_structured_output: true,
			input_price_micro_usd_per_million: 100000,
			output_price_micro_usd_per_million: 400000,
		},
		{
			id: "strong", context_window: 128000, max_output_tokens: 16000,
			supports_vision: true, supports_tools: true,
			supports_structured_output: true,
			input_price_micro_usd_per_million: 3000000,
			output_price_micro_usd_per_million: 15000000,
		},
	];
	draft.config.auto_routing = {
		enabled: true,
		participants: ["fast", "strong"],
		strong_baseline_model: "strong",
		task_analyzer_model: "fast",
		analyzer_timeout: "5s",
		analyzer_min_confidence_bps: 7000,
		session_ttl: "24h",
		strategy: {
			name: "20260802-001",
			alias: "当前",
			default_route: "balanced",
			task_routes: [{ task_type: "simple", route: "balanced" }],
			routes: [{
				id: "balanced",
				min_quality_bps: 9000,
				min_stability_bps: 8000,
				max_severe_error_rate_bps: 100,
				weights: {
					quality_bps: 4000, stability_bps: 2500,
					cost_bps: 2500, performance_bps: 1000,
				},
				candidates: [
					{ model: "fast", quality_score_bps: 9200, stability_score_bps: 9300, severe_error_rate_bps: 50, expected_latency_ms: 250 },
					{ model: "strong", quality_score_bps: 9900, stability_score_bps: 9900, severe_error_rate_bps: 10, expected_latency_ms: 800 },
				],
			}],
			budget: {
				max_answer_attempts: 2,
				max_auxiliary_calls: 2,
				max_total_outbound_calls: 5,
				max_retries_per_target: 1,
				max_model_switches: 1,
				deadline: "2m",
				max_worst_case_cost_micro_usd: 500000,
			},
		},
	};
	return draft;
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
    this.classList = {
      values: new Set(),
      add: (...names) => {
        for (const name of names) {
          this.classList.values.add(name);
        }
      },
    };
    this.id = "";
    this.name = "";
    this.type = "";
    this._value = "";
    this.checked = false;
    this.required = false;
    this.disabled = false;
    this.hidden = false;
    this.open = false;
  }

  set innerHTML(_) {
    throw new Error("innerHTML is forbidden");
  }

  get value() {
    if (this.tagName !== "SELECT") {
      return this._value;
    }
    return this.children.find((child) => child.selected)?.value ??
      this.children[0]?.value ?? "";
  }

  set value(value) {
    const normalized = String(value ?? "");
    if (this.tagName !== "SELECT") {
      this._value = normalized;
      return;
    }
    for (const child of this.children) {
      child.selected = child.value === normalized;
    }
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

  remove() {
    if (!this.parentNode) {
      return;
    }
    this.parentNode.children = this.parentNode.children.filter(
      (child) => child !== this,
    );
    this.parentNode = null;
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

  async dispatch(name, eventInit = {}) {
    const event = {
      ...eventInit,
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

function elementsByClass(root, className) {
  return descendants(root).filter((element) =>
    String(element.className ?? "")
      .split(/\s+/)
      .includes(className),
  );
}

function findAllTags(root, tagName) {
  return descendants(root).filter(
    (element) => element.tagName === tagName.toUpperCase(),
  );
}

function findTag(root, tagName) {
  const element = findAllTags(root, tagName)[0];
  assert.ok(element, `${tagName} not found`);
  return element;
}

function findText(root, text) {
  const element = descendants(root).find((candidate) =>
    candidate.textContent.includes(text),
  );
  assert.ok(element, `text ${text} not found`);
  return element;
}

function findClass(root, className) {
  const element = descendants(root).find((candidate) =>
    candidate.className.split(/\s+/).includes(className),
  );
  assert.ok(element, `class ${className} not found`);
  return element;
}

function controls(root) {
  return descendants(root).filter((element) =>
    ["INPUT", "SELECT", "TEXTAREA"].includes(element.tagName),
  );
}

function controlByName(root, name) {
  const control = controls(root).find((element) => element.name === name);
  assert.ok(control, `control ${name} not found`);
  return control;
}

function fieldDescription(root, control) {
  const descriptionID = control.getAttribute("aria-describedby");
  assert.ok(descriptionID, `control ${control.name} has no description`);
  const description = descendants(root).find(
    (element) => element.id === descriptionID,
  );
  assert.ok(description, `description ${descriptionID} not found`);
  assert.equal(description.tagName, "SMALL");
  return description.textContent;
}

function labelTexts(root) {
  return findAllTags(root, "LABEL").map((label) => label.textContent);
}

function buttonTexts(root) {
  return findAllTags(root, "BUTTON").map((button) => button.textContent);
}

function buttonsByText(root, text) {
  return findAllTags(root, "BUTTON").filter(
    (button) => button.textContent === text,
  );
}

function buttonByText(root, text) {
  const button = buttonsByText(root, text)[0];
  assert.ok(button, `button ${text} not found`);
  return button;
}

function linkByText(root, text) {
  const link = descendants(root).find(
    (element) => element.tagName === "A" && element.textContent === text,
  );
  assert.ok(link, `link ${text} not found`);
  return link;
}

function sectionHeadings(root) {
  return findAllTags(root, "H2").map((heading) => heading.textContent);
}

function profileCard(root, slug) {
  const card = descendants(root).find(
    (element) =>
      element.className.split(/\s+/).includes("profile-settings-row") &&
      descendants(element).some((child) => child.textContent === slug),
  );
  assert.ok(card, `Profile card ${slug} not found`);
  return card;
}

function retryBodies(root) {
  return controls(root)
    .filter((control) => /^retry-\d+-body_contains$/.test(control.name))
    .map((control) => control.value);
}

function modelRows(root) {
  return elementsByClass(root, "model-capability-row");
}

function modelIDs(root) {
  return modelRows(root).map((row) => controlByField(row, "id").value);
}

function controlByField(root, field) {
  const control = controls(root).find(
    (candidate) => candidate.getAttribute("data-model-field") === field,
  );
  assert.ok(control, `model control ${field} not found`);
  return control;
}

function controlByLabel(root, text) {
	const label = findAllTags(root, "LABEL").find((candidate) =>
		descendants(candidate).some((item) => item.textContent.includes(text))
	);
	assert.ok(label, `label ${text} not found`);
	const control = controls(label)[0];
	assert.ok(control, `control for ${text} not found`);
	return control;
}

function openDialog(root) {
  const dialog = findAllTags(root, "DIALOG").find((candidate) => candidate.open);
  assert.ok(dialog, "open dialog not found");
  return dialog;
}
