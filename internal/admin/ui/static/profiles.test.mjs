import assert from "node:assert/strict";
import test from "node:test";

import { api } from "./api.js";
import { bootstrap } from "./app.js";
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
  assert.equal(draft.config.version, 1);
  assert.equal(draft.config.protocol, "anthropic");
  assert.equal(draft.config.vision.model, "sonnet");
  assert.equal(draft.config.vision.timeout, "2m");
  assert.equal(draft.config.vision.unlisted_model_policy, "bypass");
  assert.deepEqual(draft.config.models, []);
  assert.equal(draft.config.auto_routing.enabled, false);
  assert.deepEqual(draft.config.overload_rules, []);
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
      supports_structured_output: "",
      input_price_micro_usd_per_million: "",
      output_price_micro_usd_per_million: "",
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
    strategy: {
      name: "20260802-001",
      alias: "均衡",
      default_route: "balanced",
      task_routes: [{ task_type: "simple", route: "balanced" }],
      routes: [{
        id: "balanced",
        min_quality_bps: "9000",
        max_severe_error_rate_bps: "100",
        candidates: [{
          model: "fast",
          quality_score_bps: "9200",
          severe_error_rate_bps: "50",
        }],
      }],
      budget: {
        max_answer_attempts: "2",
        max_auxiliary_calls: "2",
        max_total_outbound_calls: "5",
        max_retries_per_target: "1",
        max_target_switches: "0",
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
  assert.equal(auto.strategy.routes[0].min_quality_bps, 9000);
  assert.equal(auto.strategy.routes[0].candidates[0].quality_score_bps, 9200);
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
      auto_routing: { enabled: false },
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
      /微美元\/百万 Token/,
    );
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
    strategy: {
      name: "20260802-001",
      alias: "均衡",
      default_route: "balanced",
      task_routes: [{ task_type: "simple", route: "balanced" }],
      routes: [{
        id: "balanced",
        min_quality_bps: 9000,
        max_severe_error_rate_bps: 100,
        candidates: [{
          model: "fast",
          quality_score_bps: 9200,
          severe_error_rate_bps: 50,
        }],
      }],
      budget: {
        max_answer_attempts: 2,
        max_auxiliary_calls: 2,
        max_total_outbound_calls: 5,
        max_retries_per_target: 1,
        max_target_switches: 0,
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
  assert.equal(controlByName(root, "auto_analyzer_confidence").value, "70");
  assert.equal(controlByName(root, "auto-route-0-min-quality").value, "90");
  assert.match(
    fieldDescription(root, controlByName(root, "auto_strong_baseline_model")),
    /高风险/,
  );
  assert.match(
    fieldDescription(root, controlByName(root, "auto-budget-total")),
    /所有上游调用/,
  );

  controlByName(root, "auto_analyzer_confidence").value = "72.5";
  controlByName(root, "auto-route-0-min-quality").value = "91.25";
  await findTag(root, "FORM").dispatch("submit");

  assert.equal(saves.length, 1);
  assert.equal(saves[0].config.auto_routing.analyzer_min_confidence_bps, 7250);
  assert.equal(saves[0].config.auto_routing.strategy.routes[0].min_quality_bps, 9125);
  assert.deepEqual(saves[0].config.auto_routing.participants, ["fast", "strong"]);
});

test("saved model rows retain empty optional limits while still showing catalog provenance", async (t) => {
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
  assert.equal(controlByField(exact, "max_output_tokens").value, "");
  assert.equal(controlByField(exact, "supports_vision").value, "false");
  assert.ok(findText(exact, "来源：Models.dev"));
  assert.ok(findText(exact, "更新：2026-02-12"));
  assert.ok(findText(exact, "获取：2026-07-30"));
});

test("progressive model input renders recommendations but waits for change before applying exact values", async (t) => {
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
  assert.equal(context.value, "");

  id.value = "glm-5.2";
  await id.dispatch("input");
  assert.equal(context.value, "");
  assert.equal(output.value, "");
  assert.equal(vision.value, "");

  await id.dispatch("change");
  assert.equal(context.value, "1000000");
  assert.equal(output.value, "131072");
  assert.equal(vision.value, "false");
  assert.equal(id.value, "glm-5.2");
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
});

test("compatibility aliases retain the exact entered ID when change auto-applies values", async (t) => {
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
  assert.equal(controlByField(row, "context_window").value, "");

  await id.dispatch("change");
  assert.equal(id.value, "claude-glm-5.2");
  assert.equal(controlByField(row, "context_window").value, "1000000");
  assert.equal(controlByField(row, "max_output_tokens").value, "131072");
  assert.equal(controlByField(row, "supports_vision").value, "false");
});

test("initial render shows an exact recommendation without filling saved blank fields", (t) => {
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
  assert.equal(controlByField(row, "context_window").value, "");
  assert.equal(controlByField(row, "max_output_tokens").value, "");
  assert.equal(controlByField(row, "supports_vision").value, "");
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
  assert.equal(output.value, "131072");
  assert.equal(vision.value, "false");

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
  assert.ok(findText(cards[0], "获取：2026-07-30"));
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

test("authenticated app creates a Profile only through the injected API client", async (t) => {
  const root = installFakeDOM(t);
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
    },
  };

  await bootstrap({ root, client });
  await buttonByText(root, "创建第一个 Profile").dispatch("click");
  assert.equal(controlByName(root, "enabled").checked, true);
  assert.equal(controlByName(root, "enabled").disabled, true);
  controlByName(root, "display_name").value = "New Profile";
  controlByName(root, "slug").value = "new-profile";
  controlByName(root, "upstream").value = "https://new.example";
  await findTag(root, "FORM").dispatch("submit");

  assert.equal(listCalls, 2);
  assert.equal(creates.length, 1);
  assert.equal(creates[0].display_name, "New Profile");
  assert.equal(creates[0].slug, "new-profile");
  assert.equal(creates[0].make_default, true);
  assert.equal(creates[0].config.upstream, "https://new.example");
  assert.equal(typeof creates[0].config.vision.max_tokens, "number");
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

function openDialog(root) {
  const dialog = findAllTags(root, "DIALOG").find((candidate) => candidate.open);
  assert.ok(dialog, "open dialog not found");
  return dialog;
}
