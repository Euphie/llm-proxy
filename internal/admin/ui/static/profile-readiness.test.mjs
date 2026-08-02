import assert from "node:assert/strict";
import test from "node:test";

import { defaultProfileDraft } from "./profile-draft.js";
import { modelReferences, profileReadiness } from "./profile-readiness.js";

test("intelligent routing explains that models must be recorded first", () => {
  const draft = readyDraft();
  draft.config.models = [];

  const readiness = profileReadiness(draft, { saved: true });
  assert.equal(readiness.sections.routing.state, "blocked");
  assert.match(readiness.sections.routing.reasons[0], /录入模型/);
  assert.deepEqual(readiness.sections.routing.action, {
    label: "录入模型",
    href: "/_admin/profiles/7/models",
  });
});

test("Agent configuration is ready without models and reliability accepts no rules", () => {
  const draft = readyDraft();
  draft.config.models = [];
  draft.config.targets = [];
  draft.config.overload_rules = [];

  const readiness = profileReadiness(draft, { saved: true });
  assert.equal(readiness.sections.agents.state, "ready");
  assert.equal(readiness.sections.reliability.state, "ready");
});

test("Auto with vision requires a priced catalog model with usable limits", () => {
  const cases = [
    [[], /视觉模型.*录入/],
    [[catalogModel({ supports_vision: false })], /支持视觉/],
    [[catalogModel({ input_price_micro_usd_per_million: undefined })], /价格/],
    [[catalogModel({ context_window: undefined })], /上下文窗口/],
    [[catalogModel({ max_output_tokens: undefined })], /最大输出/],
  ];

  for (const [models, expected] of cases) {
    const draft = readyDraft();
    draft.config.models = [routingModel("fast"), ...models];
    draft.config.auto_routing.enabled = true;
    draft.config.auto_routing.participants = ["fast"];
    draft.config.auto_routing.strong_baseline_model = "fast";
    draft.config.auto_routing.task_analyzer_model = "fast";
    draft.config.vision.enabled = true;
    draft.config.vision.model = "vision";

    const readiness = profileReadiness(draft, { saved: true });
    assert.equal(readiness.sections.vision.state, "blocked");
    assert.match(readiness.sections.vision.reasons.join(" "), expected);
  }

  const valid = readyDraft();
  valid.config.models = [routingModel("fast"), catalogModel()];
  valid.config.auto_routing.enabled = true;
  valid.config.auto_routing.participants = ["fast"];
  valid.config.auto_routing.strong_baseline_model = "fast";
  valid.config.auto_routing.task_analyzer_model = "fast";
  valid.config.vision.enabled = true;
  valid.config.vision.model = "vision";
  assert.equal(
    profileReadiness(valid, { saved: true }).sections.vision.state,
    "ready",
  );
});

test("model references name every dependent feature and field", () => {
  const draft = readyDraft();
  draft.config.models = [routingModel("fast"), routingModel("strong")];
  draft.config.auto_routing = {
    ...draft.config.auto_routing,
    participants: ["fast"],
    strong_baseline_model: "strong",
    task_analyzer_model: "fast",
    dynamic_optimization: {
      ...draft.config.auto_routing.dynamic_optimization,
      reviewer_model: "strong",
    },
    strategy: {
      ...draft.config.auto_routing.strategy,
      routes: [{
        id: "balanced",
        candidates: [{ model: "strong" }],
      }],
    },
  };
  draft.config.vision.model = "strong";
  draft.config.targets = [{ id: "backup", models: ["strong"] }];

  assert.deepEqual(modelReferences(draft, "strong"), [
    {
      section: "routing",
      label: "强模型基线",
      path: "config.auto_routing.strong_baseline_model",
    },
    {
      section: "routing",
      label: "仲裁模型",
      path: "config.auto_routing.dynamic_optimization.reviewer_model",
    },
    {
      section: "vision",
      label: "视觉模型",
      path: "config.vision.model",
    },
    {
      section: "routing",
      label: "Route balanced",
      path: "config.auto_routing.strategy.routes[0].candidates[0].model",
    },
    {
      section: "reliability",
      label: "Target backup",
      path: "config.targets[0].models[0]",
    },
  ]);
});

function readyDraft() {
  const draft = defaultProfileDraft();
  draft.id = 7;
  draft.slug = "coding";
  draft.display_name = "Coding";
  draft.config.upstream = "https://upstream.example";
  return draft;
}

function routingModel(id) {
  return {
    id,
    context_window: 128000,
    max_output_tokens: 16000,
    supports_vision: true,
    input_price_micro_usd_per_million: 1000000,
    output_price_micro_usd_per_million: 4000000,
  };
}

function catalogModel(overrides = {}) {
  return { ...routingModel("vision"), ...overrides };
}
