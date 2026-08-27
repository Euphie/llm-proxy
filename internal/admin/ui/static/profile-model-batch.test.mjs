import assert from "node:assert/strict";
import test from "node:test";

import { createModelCatalogMatcher } from "./model-catalog.js";
import {
  importBatchModels,
  previewBatchModels,
} from "./profile-model-batch.js";

const models = [
  catalogModel("alpha-fast", 128000, 100000),
  catalogModel("alpha-strong", 256000, 3000000),
];
const matcher = createModelCatalogMatcher(models).matchModelSuggestions;

test("batch preview preserves order while marking duplicates and existing exact IDs", () => {
  const rows = previewBatchModels(
    "\nalpha-fast\n alpha-fast \nsaved\nunknown-model\n",
    [{ id: "saved", context_window: 999 }],
    matcher,
  );

  assert.deepEqual(rows.map((row) => [row.id, row.status]), [
    ["alpha-fast", "ready"],
    ["alpha-fast", "duplicate"],
    ["saved", "existing"],
    ["unknown-model", "unknown"],
  ]);
  assert.equal(rows[0].model.context_window, 128000);
  assert.equal(rows[0].model.canonical_model_id, "acme/alpha-fast");
  assert.deepEqual(rows[3].model, { id: "unknown-model" });
});

test("family matches import the original ID without guessing parameters", () => {
  const rows = previewBatchModels("alpha", [], matcher);
  assert.equal(rows[0].status, "choose");
  assert.equal(rows[0].candidates.length, 2);
  assert.deepEqual(importBatchModels(rows), [{ id: "alpha" }]);
});

test("batch preview rejects more than 100 unique non-empty IDs", () => {
  const input = Array.from({ length: 101 }, (_, index) => `unknown-${index}`).join("\n");
  assert.throws(
    () => previewBatchModels(input, [], matcher),
    /最多.*100/,
  );
});

test("batch import excludes duplicates and existing IDs while retaining unresolved IDs", () => {
  const rows = previewBatchModels(
    "saved\nalpha-fast\nalpha\nunknown\nalpha-fast",
    [{ id: "saved", context_window: 999 }],
    matcher,
  );
  assert.deepEqual(importBatchModels(rows).map((model) => model.id), [
    "alpha-fast",
    "alpha",
    "unknown",
  ]);
});

function catalogModel(id, contextWindow, inputPrice) {
  return {
    id,
    canonicalId: `acme/${id}`,
    apiIds: [],
    aliases: [`acme/${id}`],
    compatibilityAliases: [],
    provider: "Acme",
    name: id,
    family: "alpha",
    context_window: contextWindow,
    max_output_tokens: 16000,
    supports_vision: false,
    supports_tools: true,
    input_price_micro_usd_per_million: inputPrice,
    output_price_micro_usd_per_million: inputPrice * 4,
    lifecycle: "stable",
    references: [],
    source: { name: "test", retrieved: "2026-08-02" },
  };
}
