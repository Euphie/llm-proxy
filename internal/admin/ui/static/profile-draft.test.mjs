import assert from "node:assert/strict";
import test from "node:test";

import {
  defaultProfileDraft,
  mergeProfileSection,
  profileDraft,
  profilePayload,
} from "./profile-draft.js";

test("disabled intelligent routing retains its complete inactive configuration", () => {
  const draft = defaultProfileDraft();
  draft.config.models = [model("fast"), model("strong")];
  draft.config.auto_routing = {
    ...draft.config.auto_routing,
    enabled: false,
    participants: ["fast", "strong"],
    strong_baseline_model: "strong",
    task_analyzer_model: "fast",
    analyzer_timeout: "7s",
    analyzer_min_confidence_bps: 7300,
    session_ttl: "12h",
    dynamic_optimization: {
      enabled: false,
      sample_rate_bps: 1250,
      daily_budget_micro_usd: 300000,
      reviewer_model: "strong",
      max_concurrency: 3,
      queue_capacity: 96,
      task_timeout: "75s",
    },
    strategy: {
      ...draft.config.auto_routing.strategy,
      name: "20260802-001",
      alias: "稳健",
      default_route: "balanced",
      routes: [{
        id: "balanced",
        min_quality_bps: 9800,
				min_stability_bps: 9800,
        max_severe_error_rate_bps: 100,
				weights: {
					quality_bps: 4000, stability_bps: 2500,
					cost_bps: 2500, performance_bps: 1000,
				},
        candidates: [{
          model: "strong",
          quality_score_bps: 9900,
					stability_score_bps: 9900,
          severe_error_rate_bps: 10,
					expected_latency_ms: 800,
        }],
      }],
    },
  };

  const auto = profilePayload(draft).config.auto_routing;
  assert.equal(auto.enabled, false);
  assert.deepEqual(auto.participants, ["fast", "strong"]);
  assert.equal(auto.strong_baseline_model, "strong");
  assert.equal(auto.task_analyzer_model, "fast");
  assert.deepEqual(auto.dynamic_optimization, {
    enabled: false,
    auto_update_policy: false,
    sample_rate_bps: 1250,
    daily_budget_micro_usd: 300000,
    reviewer_model: "strong",
    max_concurrency: 3,
    queue_capacity: 96,
    task_timeout: "75s",
  });
  assert.equal(auto.strategy.name, "20260802-001");
  assert.equal(auto.strategy.routes[0].candidates[0].model, "strong");
});

test("disabled vision retains the configured model cache and prompt", () => {
  const draft = defaultProfileDraft();
  draft.config.vision = {
    ...draft.config.vision,
    enabled: false,
    model: "vision-pro",
    max_tokens: 4096,
    timeout: "45s",
    max_concurrency: 6,
    cache_ttl: "2h",
    cache_max_entries: 900,
    prompt: "只提取可见事实",
  };

  assert.deepEqual(profilePayload(draft).config.vision, {
    enabled: false,
    transport: "anthropic_messages",
    model: "vision-pro",
    unlisted_model_policy: "bypass",
    max_tokens: 4096,
    timeout: "45s",
    max_concurrency: 6,
    cache_ttl: "2h",
    cache_max_entries: 900,
    prompt: "只提取可见事实",
  });
});

test("Profile draft round-trips inactive feature details", () => {
  const source = defaultProfileDraft();
  source.id = 8;
  source.slug = "coding";
  source.display_name = "Coding";
  source.config.upstream = "https://upstream.example";
  source.config.auto_routing.participants = ["fast"];
  source.config.auto_routing.dynamic_optimization.reviewer_model = "judge";
  const saved = {
    id: source.id,
    slug: source.slug,
    display_name: source.display_name,
    enabled: source.enabled,
    config: profilePayload(source).config,
  };

  const reloaded = profileDraft(saved);
  assert.deepEqual(profilePayload(reloaded).config, saved.config);
});

test("Profile draft round-trips exact canonical model identities", () => {
  const source = defaultProfileDraft();
  source.config.models = [{
    ...model("gateway-alpha"),
    canonical_model_id: "acme/alpha-2026",
  }, model("unmapped")];

  const payload = profilePayload(source);
  assert.equal(
    payload.config.models[0].canonical_model_id,
    "acme/alpha-2026",
  );
  assert.equal(
    Object.hasOwn(payload.config.models[1], "canonical_model_id"),
    false,
  );

  const reloaded = profileDraft({ config: payload.config });
  assert.equal(
    reloaded.config.models[0].canonical_model_id,
    "acme/alpha-2026",
  );
});

test("section merging changes only the selected Profile section", () => {
  const source = defaultProfileDraft();
  source.slug = "old";
  source.config.upstream = "https://old.example";
  source.config.models = [model("strong")];
  source.config.vision.model = "strong";

  const merged = mergeProfileSection(source, "connection", {
    slug: "new",
    upstream: "https://new.example",
  });

  assert.equal(merged.slug, "new");
  assert.equal(merged.config.upstream, "https://new.example");
  assert.deepEqual(merged.config.models, source.config.models);
  assert.deepEqual(merged.config.vision, source.config.vision);
});

function model(id) {
  return {
    id,
    context_window: 128000,
    max_output_tokens: 16000,
    supports_vision: true,
    input_price_micro_usd_per_million: 1000000,
    output_price_micro_usd_per_million: 4000000,
  };
}
