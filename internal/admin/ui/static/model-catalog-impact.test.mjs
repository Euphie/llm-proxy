import assert from "node:assert/strict";
import test from "node:test";

import { analyzeCatalogImpact } from "./model-catalog-impact.js";

test("impact analysis classifies regressions prices and newly known facts", () => {
  const rows = analyzeCatalogImpact({
    previous: catalog([
      model("strong", { context_window: 200000, supports_vision: true, input_price_micro_usd_per_million: 3000000 }),
      model("fast", { context_window: 128000, supports_vision: false, input_price_micro_usd_per_million: 100000 }),
    ]),
    current: catalog([
      model("strong", { context_window: 128000, supports_vision: false, input_price_micro_usd_per_million: 2000000 }),
      model("fast", { context_window: 128000, supports_vision: false, input_price_micro_usd_per_million: 120000, cache_read_price_micro_usd_per_million: 12000 }),
      model("new-model", { context_window: 64000, supports_vision: false }),
    ]),
    profiles: [profileFixture()],
    strategiesByProfile: {},
  });

  assert.equal(rows.some((row) => row.modelId === "strong" && row.risk === "high"), true);
  assert.equal(rows.some((row) => row.modelId === "fast" && row.risk === "medium"), true);
  assert.equal(
    rows.find((row) => row.modelId === "fast")?.changes.some(
      (change) => change.label === "缓存读取价格",
    ),
    true,
  );
  assert.equal(rows.some((row) => row.modelId === "new-model" && row.risk === "low"), true);
  assert.equal(rows.some((row) => row.label === "视觉模型" && row.href === "/_admin/profiles/7/vision"), true);
  assert.equal(rows.some((row) => row.label === "强模型基线" && row.href === "/_admin/profiles/7/routing"), true);
  assert.equal(rows.some((row) => row.label === "上游节点 backup" && row.href === "/_admin/profiles/7/reliability"), true);
});

test("impact analysis scans every stored strategy lifecycle without mutating input", () => {
  const strategies = {
    7: {
      strategies: [
        strategy(1, "active", "正式", "fast"),
        strategy(2, "canary", "灰度", "fast"),
        strategy(3, "evaluating", "评估", "fast"),
        strategy(4, "draft", "草稿", "fast"),
      ],
    },
  };
  const before = structuredClone(strategies);
  const rows = analyzeCatalogImpact({
    previous: catalog([model("fast", { input_price_micro_usd_per_million: 100000 })]),
    current: catalog([model("fast", { input_price_micro_usd_per_million: 150000 })]),
    profiles: [profileFixture()],
    strategiesByProfile: strategies,
  });

  assert.deepEqual(
    [...new Set(rows.filter((row) => row.strategy).map((row) => row.strategy.state))].sort(),
    ["active", "canary", "draft", "evaluating"],
  );
  assert.equal(rows.filter((row) => row.strategy).every((row) => row.risk === "medium"), true);
  assert.deepEqual(strategies, before);
});

test("unchanged catalog facts produce no impact rows", () => {
  const same = catalog([model("fast", { context_window: 128000 })]);
  assert.deepEqual(analyzeCatalogImpact({
    previous: same,
    current: structuredClone(same),
    profiles: [profileFixture()],
    strategiesByProfile: {},
  }), []);
});

function catalog(models) {
  return {
    source: { name: "Models.dev", retrieved: "2026-08-02" },
    models,
  };
}

function model(id, overrides = {}) {
  return {
    id,
    canonicalId: `acme/${id}`,
    apiIds: [],
    aliases: [`acme/${id}`],
    compatibilityAliases: [],
    provider: "Acme",
    name: id,
    family: id,
    supports_tools: true,
    lifecycle: "stable",
    references: [],
    ...overrides,
  };
}

function profileFixture() {
  return {
    id: 7,
    slug: "coding",
    display_name: "Coding",
    config: {
      models: [{ id: "strong" }, { id: "fast" }, { id: "new-model" }],
      vision: { enabled: true, model: "strong" },
      targets: [{ id: "backup", models: ["strong"] }],
      auto_routing: {
        enabled: true,
        participants: ["fast", "strong"],
        strong_baseline_model: "strong",
        task_analyzer_model: "fast",
        strategy: {
          routes: [{ id: "balanced", candidates: [{ model: "strong" }] }],
        },
      },
    },
  };
}

function strategy(id, state, name, candidate) {
  return {
    id,
    state,
    config: {
      name,
      routes: [{ id: "balanced", candidates: [{ model: candidate }] }],
    },
  };
}
