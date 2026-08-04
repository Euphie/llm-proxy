import assert from "node:assert/strict";
import test from "node:test";

import { validateModelMutation } from "./profile-models.js";

test("unreferenced model removal and capability edits remain allowed", () => {
  const draft = referencedDraft();
  assert.deepEqual(validateModelMutation(draft, "unused", ""), {
    allowed: true,
    references: [],
  });
  assert.deepEqual(validateModelMutation(draft, "strong", "strong"), {
    allowed: true,
    references: [],
  });
});

test("every exact routing vision and Target reference blocks deletion", () => {
  const result = validateModelMutation(referencedDraft(), "strong", "");
  assert.equal(result.allowed, false);
  assert.deepEqual(result.references.map((reference) => reference.label), [
    "强模型基线",
    "仲裁模型",
    "视觉模型",
    "Route balanced",
    "上游节点 backup",
  ]);
});

test("reference safety is case-sensitive and blocks ID renames", () => {
  const draft = referencedDraft();
  assert.equal(validateModelMutation(draft, "strong", "STRONG").allowed, false);
  assert.equal(validateModelMutation(draft, "STRONG", "new").allowed, true);
});

function referencedDraft() {
  return {
    id: 7,
    config: {
      models: [{ id: "strong" }, { id: "unused" }],
      auto_routing: {
        participants: [],
        strong_baseline_model: "strong",
        task_analyzer_model: "",
        dynamic_optimization: { reviewer_model: "strong" },
        strategy: {
          routes: [{ id: "balanced", candidates: [{ model: "strong" }] }],
        },
      },
      vision: { model: "strong" },
      targets: [{ id: "backup", models: ["strong"] }],
    },
  };
}
