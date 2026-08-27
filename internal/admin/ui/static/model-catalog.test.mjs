import assert from "node:assert/strict";
import test from "node:test";

import * as modelCatalog from "./model-catalog.js";

const {
  applyModelSuggestion,
  BUILT_IN_MODELS,
  matchModelSuggestion,
  matchModelSuggestions,
  MODELS_DEV_SOURCE,
  parseTokenLimit,
} = modelCatalog;

test("parseTokenLimit accepts positive decimal K/M limits and plain integers", () => {
  assert.equal(parseTokenLimit("128K"), 128000);
  assert.equal(parseTokenLimit("1m"), 1000000);
  assert.equal(parseTokenLimit("1.5M"), 1500000);
  assert.equal(parseTokenLimit("262144"), 262144);
  assert.equal(parseTokenLimit(" 0.001k "), 1);
  assert.equal(
    parseTokenLimit(String(Number.MAX_SAFE_INTEGER)),
    Number.MAX_SAFE_INTEGER,
  );
  assert.equal(parseTokenLimit("", { optional: true }), null);
});

test("parseTokenLimit rejects invalid or unsafe token counts", () => {
  for (const value of [
    "",
    "0",
    "-1",
    "1.5",
    "1Ki",
    "1B",
    "NaN",
    "Infinity",
    "0K",
    "0.0001K",
    "9007199254740992",
    "9007199254741K",
    Number.POSITIVE_INFINITY,
  ]) {
    assert.throws(
      () => parseTokenLimit(value),
      undefined,
      `expected ${String(value)} to be rejected`,
    );
  }
});

test("built-in catalog is a broad immutable Models.dev snapshot", () => {
  assert.equal(MODELS_DEV_SOURCE.name, "Models.dev");
  assert.match(MODELS_DEV_SOURCE.revision, /^[0-9a-f]{64}$/);
  assert.match(MODELS_DEV_SOURCE.modelsSha256, /^[0-9a-f]{64}$/);
  assert.match(MODELS_DEV_SOURCE.providersSha256, /^[0-9a-f]{64}$/);
  assert.match(MODELS_DEV_SOURCE.retrieved, /^\d{4}-\d{2}-\d{2}$/);
  assert.ok(Object.isFrozen(MODELS_DEV_SOURCE));
  assert.ok(Object.isFrozen(BUILT_IN_MODELS));
  assert.ok(BUILT_IN_MODELS.length > 150);

  for (const canonicalId of [
    "anthropic/claude-sonnet-5",
    "openai/gpt-5.6-sol",
    "google/gemini-3.6-flash",
    "zhipuai/glm-5.2",
    "moonshotai/kimi-k3",
    "deepseek/deepseek-v4-pro",
    "alibaba/qwen3.7-plus",
    "minimax/MiniMax-M3",
  ]) {
    assert.ok(
      BUILT_IN_MODELS.some((model) =>
        model.canonicalId === canonicalId
      ),
      canonicalId,
    );
  }

  const canonicalIds = BUILT_IN_MODELS.map(({ canonicalId }) => canonicalId);
  assert.deepEqual(
    canonicalIds,
    [...canonicalIds].sort((left, right) =>
      left.localeCompare(right)
    ),
  );
  assert.equal(new Set(canonicalIds).size, canonicalIds.length);
  for (const entry of BUILT_IN_MODELS) {
    assertDeepFrozen(entry);
    assert.equal(entry.source, MODELS_DEV_SOURCE);
    assert.ok(entry.aliases.includes(entry.canonicalId));
    assert.ok(Number.isSafeInteger(entry.context_window));
    assert.ok(entry.context_window > 0);
    if (Object.hasOwn(entry, "max_output_tokens")) {
      assert.ok(Number.isSafeInteger(entry.max_output_tokens));
      assert.ok(entry.max_output_tokens > 0);
      assert.ok(entry.max_output_tokens < entry.context_window);
    }
    assert.equal(entry.supports_tools, true);
    for (const field of [
      "input_price_micro_usd_per_million",
      "output_price_micro_usd_per_million",
    ]) {
      if (Object.hasOwn(entry, field)) {
        assert.ok(Number.isSafeInteger(entry[field]));
        assert.ok(entry[field] >= 0);
      }
    }
  }

  for (const [alias, canonicalId] of [
    ["claude-glm-5.2", "zhipuai/glm-5.2"],
    ["azure-ds-v4-flash", "deepseek/deepseek-v4-flash"],
    ["azure-ds-v4-pro", "deepseek/deepseek-v4-pro"],
  ]) {
    const compatibilityOwners = BUILT_IN_MODELS.filter((entry) =>
      entry.compatibilityAliases.includes(alias)
    );
    assert.deepEqual(
      compatibilityOwners.map(({ canonicalId: owner }) => owner),
      [canonicalId],
    );
  }

  const audioOnly = BUILT_IN_MODELS.find(({ canonicalId }) =>
    canonicalId === "nvidia/nemotron-voicechat"
  );
  assert.equal(audioOnly.supports_vision, false);
});

test("installModelCatalog hot-swaps recommendations without mutating the supplied catalog", () => {
  const original = modelCatalog.currentModelCatalog();
  const remote = {
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
      supports_tools: true,
      lifecycle: "stable",
      references: [],
    }],
  };

  modelCatalog.installModelCatalog(remote);
  assert.equal(modelCatalog.matchModelSuggestion("remote-alpha").entry.name, "Remote Alpha");
  assert.equal(modelCatalog.currentModelCatalog().source.revision, "d".repeat(64));
  assert.equal(Object.isFrozen(modelCatalog.currentModelCatalog()), true);
  assert.equal(Object.isFrozen(remote), false);

  modelCatalog.installModelCatalog(original);
});

test("matcher exports the plural suggestion API", () => {
  assert.equal(typeof modelCatalog.matchModelSuggestions, "function");
  assert.equal(typeof modelCatalog.createModelCatalogMatcher, "function");
});

test("matcher follows exact, alias, compatibility, wrapper, and family priorities", () => {
  assertMatch("glm-5.2", "zhipuai/glm-5.2", "exact", true);
  assertMatch("zhipuai/glm-5.2", "zhipuai/glm-5.2", "alias", true);
  assertMatch(
    "claude-glm-5.2",
    "zhipuai/glm-5.2",
    "compatibility",
    true,
  );
  assertMatch(
    "azure-ds-v4-flash",
    "deepseek/deepseek-v4-flash",
    "compatibility",
    true,
  );
  assertMatch(
    "azure-ds-v4-pro",
    "deepseek/deepseek-v4-pro",
    "compatibility",
    true,
  );
  assertMatch("proxy/kimi-k3", "moonshotai/kimi-k3", "wrapper", true);
  assertMatch("proxy:kimi-k3", "moonshotai/kimi-k3", "wrapper", true);
  assertMatch("proxy-kimi-k3", "moonshotai/kimi-k3", "wrapper", true);
  assertMatch("my-proxy/kimi-k3", "moonshotai/kimi-k3", "wrapper", true);
  assertMatch("my-proxy:kimi-k3", "moonshotai/kimi-k3", "wrapper", true);
  assertMatch(
    "proxy/openai/gpt-5.6-sol",
    "openai/gpt-5.6-sol",
    "wrapper",
    true,
  );
  assertMatch(
    "openrouter/anthropic/claude-sonnet-5",
    "anthropic/claude-sonnet-5",
    "wrapper",
    true,
  );
  assertMatch(
    "claude-glm-5.2-preview",
    "zhipuai/glm-5.2",
    "family",
    false,
  );
});

test("matcher is case-insensitive without altering dots, numeric versions, or input IDs", () => {
  const kimi = matchModelSuggestion("Kimi-K2.5");
  assert.equal(kimi.entry.id, "kimi-k2.5");
  assert.equal(kimi.match, "exact");
  assert.equal(kimi.autoApply, true);

  const glm = matchModelSuggestion("GLM-5");
  assert.equal(glm.entry.id, "glm-5");
  assert.equal(glm.match, "exact");
  assert.equal(glm.autoApply, true);

  const applied = applyModelSuggestion(
    {
      id: "Kimi-K2.5",
      context_window: "",
      max_output_tokens: null,
      supports_vision: undefined,
      supports_tools: undefined,
      supports_structured_output: undefined,
      input_price_micro_usd_per_million: "",
      output_price_micro_usd_per_million: "",
      cache_read_price_micro_usd_per_million: "",
      cache_write_price_micro_usd_per_million: "",
    },
    {
      ...kimi,
      entry: {
        ...kimi.entry,
        cache_read_price_micro_usd_per_million: 60000,
        cache_write_price_micro_usd_per_million: 750000,
      },
    },
  );
  assert.equal(applied.id, "Kimi-K2.5");
  assert.equal(applied.context_window, 262144);
  assert.equal(applied.max_output_tokens, null);
  assert.equal(applied.supports_vision, true);
  assert.equal(applied.supports_tools, true);
  assert.equal(applied.input_price_micro_usd_per_million, 600000);
  assert.equal(applied.output_price_micro_usd_per_million, 3000000);
  assert.equal(
    applied.cache_read_price_micro_usd_per_million,
    60000,
  );
  assert.equal(
    applied.cache_write_price_micro_usd_per_million,
    750000,
  );
});

test("matcher prioritizes registered Haiku IDs before the snapshot rule", () => {
  const undated = matchModelSuggestion("claude-haiku-4-5");
  assert.equal(undated.entry.id, "claude-haiku-4-5");
  assert.equal(undated.match, "exact");
  assert.equal(undated.autoApply, true);

  const exact = matchModelSuggestion("claude-haiku-4-5-20251001");
  assert.equal(exact.entry.id, "claude-haiku-4-5-20251001");
  assert.equal(exact.match, "exact");
  assert.equal(exact.autoApply, true);

  const applied = applyModelSuggestion(
    {
      id: "Claude-Haiku-4-5-20251001",
      context_window: "",
      max_output_tokens: "",
      supports_vision: undefined,
    },
    exact,
  );
  assert.equal(applied.id, "Claude-Haiku-4-5-20251001");
  assert.equal(applied.context_window, 200000);
  assert.equal(applied.max_output_tokens, 64000);
  assert.equal(applied.supports_vision, true);

  const snapshot = matchModelSuggestion("claude-haiku-4-5-20251231");
  assert.equal(snapshot.entry.id, "claude-haiku-4-5-20251001");
  assert.equal(snapshot.match, "snapshot");
  assert.equal(snapshot.autoApply, false);

  const arbitraryDate = matchModelSuggestion("glm-5-20251001");
  assert.notEqual(arbitraryDate?.match, "snapshot");
  assert.notEqual(arbitraryDate?.autoApply, true);
});

test("matcher offers unique anchored family candidates without auto-applying", () => {
  const family = matchModelSuggestion("gpt-5.6-sol-preview");
  assert.equal(family.entry.id, "gpt-5.6-sol");
  assert.equal(family.match, "family");
  assert.equal(family.autoApply, false);

  assertMatch(
    "prefix-gpt-5.6-sol",
    "openai/gpt-5.6-sol",
    "wrapper",
    true,
  );
  assertMatch("glm-5.2-latest", "zhipuai/glm-5.2", "family", false);
  assertMatch("glm-5.2-20261231", "zhipuai/glm-5.2", "family", false);
});

test("matcher rejects malformed numeric versions, nested wrappers, and bare qualifiers", () => {
  for (const id of [
    "glm-5-2",
    "glm-5-2-preview",
    "claude-glm-5-2",
    "claude-glm-5.20",
    "claude-openai-glm-5.2",
    "proxy/claude-glm-5.2",
    "my-proxy-kimi-k3",
    "proxy/openai:kimi-k3",
    "bad wrapper/kimi-k3",
    "bad\nwrapper/kimi-k3",
    "_proxy/kimi-k3",
    ".proxy:kimi-k3",
    "claude-haiku-4-5-00000000",
    "claude-haiku-4-5-20250230",
    "glm-5.2-00000000",
    "glm-5.2-20250230",
    "sonnet",
    "haiku",
    "opus",
    "latest",
  ]) {
    assert.equal(matchModelSuggestion(id), null, id);
  }

  const deepseek = matchModelSuggestion("deepseek-chat");
  assert.equal(deepseek.entry.id, "deepseek-chat");
  assert.equal(deepseek.match, "exact");
  assert.equal(deepseek.autoApply, true);
});

test("matcher returns every ambiguous index owner as a manual candidate", () => {
  const models = [
    {
      id: "model-f",
      canonicalId: "provider/f",
      apiIds: ["shared-api"],
      aliases: [],
      compatibilityAliases: [],
      family: "model-f",
    },
    {
      id: "model-c",
      canonicalId: "provider/c",
      apiIds: ["shared-api"],
      aliases: [],
      compatibilityAliases: [],
      family: "model-c",
    },
    {
      id: "model-a",
      canonicalId: "provider/a",
      apiIds: ["shared-api"],
      aliases: [],
      compatibilityAliases: [],
      family: "model-a",
    },
    {
      id: "model-e",
      canonicalId: "provider/e",
      apiIds: ["shared-api"],
      aliases: [],
      compatibilityAliases: [],
      family: "model-e",
    },
    {
      id: "model-b",
      canonicalId: "provider/b",
      apiIds: ["shared-api"],
      aliases: [],
      compatibilityAliases: [],
      family: "model-b",
    },
    {
      id: "model-d",
      canonicalId: "provider/d",
      apiIds: ["shared-api"],
      aliases: [],
      compatibilityAliases: [],
      family: "model-d",
    },
  ];
  const forward = modelCatalog.createModelCatalogMatcher(models);
  const reversed = modelCatalog.createModelCatalogMatcher([...models].reverse());

  const expected = [
    ["provider/a", "alias", false],
    ["provider/b", "alias", false],
    ["provider/c", "alias", false],
    ["provider/d", "alias", false],
    ["provider/e", "alias", false],
  ];
  assert.deepEqual(summarize(forward.matchModelSuggestions("shared-api")), expected);
  assert.deepEqual(summarize(reversed.matchModelSuggestions("shared-api")), expected);
  assert.deepEqual(
    summarize(forward.matchModelSuggestions("shared-api", { limit: 2 })),
    expected.slice(0, 2),
  );
  assert.deepEqual(
    summarize(forward.matchModelSuggestions("shared-api", { limit: 99 })),
    expected,
  );
  assert.deepEqual(
    summarize(forward.matchModelSuggestions("proxy/shared-api")),
    expected.map(([canonicalId]) => [canonicalId, "wrapper", false]),
  );
  assert.deepEqual(
    summarize([forward.matchModelSuggestion("shared-api")]),
    expected.slice(0, 1),
  );
});

test("matcher never auto-applies ambiguous exact or compatibility keys", () => {
  const matcher = modelCatalog.createModelCatalogMatcher([
    {
      id: "duplicate",
      canonicalId: "provider/a",
      apiIds: [],
      aliases: [],
      compatibilityAliases: ["legacy"],
      family: "duplicate-a",
    },
    {
      id: "duplicate",
      canonicalId: "provider/b",
      apiIds: [],
      aliases: [],
      compatibilityAliases: ["legacy"],
      family: "duplicate-b",
    },
  ]);

  assert.deepEqual(summarize(matcher.matchModelSuggestions("duplicate")), [
    ["provider/a", "exact", false],
    ["provider/b", "exact", false],
  ]);
  assert.deepEqual(summarize(matcher.matchModelSuggestions("legacy")), [
    ["provider/a", "compatibility", false],
    ["provider/b", "compatibility", false],
  ]);
});

test("a unique exact ID wins over an ambiguous API ID", () => {
  const matcher = modelCatalog.createModelCatalogMatcher([
    {
      id: "shared",
      canonicalId: "provider/exact",
      apiIds: ["shared"],
      aliases: [],
      compatibilityAliases: [],
      family: "shared",
    },
    {
      id: "other",
      canonicalId: "provider/alias",
      apiIds: ["shared"],
      aliases: [],
      compatibilityAliases: [],
      family: "other",
    },
  ]);

  assert.deepEqual(summarize(matcher.matchModelSuggestions("shared")), [
    ["provider/exact", "exact", true],
  ]);
});

test("plural matcher returns an empty list for invalid IDs and zero limit", () => {
  assert.deepEqual(matchModelSuggestions(null), []);
  assert.deepEqual(matchModelSuggestions(""), []);
  assert.deepEqual(matchModelSuggestions("glm-5.2", { limit: 0 }), []);
});

test("applyModelSuggestion fills only empty capability fields", () => {
  const match = matchModelSuggestion("gpt-5.4");
  const model = {
    id: "GPT-5.4",
    context_window: 999,
    max_output_tokens: 333,
    supports_vision: false,
    note: "keep",
  };

  const applied = applyModelSuggestion(model, match);

  assert.notEqual(applied, model);
  assert.equal(applied.id, "GPT-5.4");
  assert.equal(applied.context_window, 999);
  assert.equal(applied.max_output_tokens, 333);
  assert.equal(applied.supports_vision, false);
  assert.equal(applied.supports_tools, true);
  assert.equal(applied.supports_structured_output, true);
  assert.equal(applied.input_price_micro_usd_per_million, 5000000);
  assert.equal(applied.output_price_micro_usd_per_million, 22500000);
  assert.equal(applied.note, "keep");

  assert.deepEqual(
    applyModelSuggestion(
      {
        id: "GLM-5",
        context_window: "",
        max_output_tokens: undefined,
        supports_vision: null,
      },
      matchModelSuggestion("GLM-5"),
    ),
    {
      id: "GLM-5",
      context_window: 204800,
      max_output_tokens: 131072,
      supports_vision: false,
      supports_tools: true,
      input_price_micro_usd_per_million: 1000000,
      output_price_micro_usd_per_million: 3200000,
      cache_read_price_micro_usd_per_million: 200000,
      cache_write_price_micro_usd_per_million: 0,
    },
  );
});

function assertMatch(id, canonicalId, match, autoApply) {
  const result = matchModelSuggestion(id);
  assert.ok(result, id);
  assert.equal(result.entry.canonicalId, canonicalId, id);
  assert.equal(result.match, match, id);
  assert.equal(result.autoApply, autoApply, id);
}

function summarize(suggestions) {
  return suggestions.map(({ entry, match, autoApply }) => [
    entry.canonicalId,
    match,
    autoApply,
  ]);
}

function assertDeepFrozen(value) {
  if (value === null || typeof value !== "object") {
    return;
  }
  assert.ok(Object.isFrozen(value));
  for (const nested of Object.values(value)) {
    assertDeepFrozen(nested);
  }
}
