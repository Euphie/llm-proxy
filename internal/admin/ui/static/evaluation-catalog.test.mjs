import assert from "node:assert/strict";
import test from "node:test";

import {
  evaluationCatalogCoverage,
  runEvaluationCatalogUpdate,
} from "./evaluation-catalog.js";

test("evaluation catalog coverage matches only persisted exact canonical identities", () => {
  const catalog = {
    results: [
      { model_id: "acme/alpha", domain: "general" },
      { model_id: "acme/alpha", domain: "coding" },
      { model_id: "acme/beta", domain: "general" },
    ],
  };
  const coverage = evaluationCatalogCoverage(catalog, [
    { id: "gateway-alpha", canonical_model_id: "acme/alpha" },
    { id: "gateway-beta", canonical_model_id: "acme/beta" },
    { id: "gateway-gamma", canonical_model_id: "acme/gamma" },
    { id: "acme/alpha" },
  ]);

  assert.deepEqual(coverage.domains, ["coding", "general"]);
  assert.deepEqual(coverage.summary, {
    total: 4,
    full: 1,
    partial: 1,
    missing: 1,
    unmapped: 1,
  });
  assert.deepEqual(coverage.models.map((model) => [
    model.id,
    model.status,
    model.domains,
  ]), [
    ["gateway-alpha", "full", ["coding", "general"]],
    ["gateway-beta", "partial", ["general"]],
    ["gateway-gamma", "missing", []],
    ["acme/alpha", "unmapped", []],
  ]);
});

test("evaluation catalog update polls one started job to success", async () => {
  let starts = 0;
  let statusIndex = 0;
  const progress = [];
  const statuses = [
    { id: "job-1", state: "running", sources: [{ id: "livebench", state: "succeeded" }] },
    { id: "job-1", state: "succeeded", changed: true, sources: [] },
  ];

  const job = await runEvaluationCatalogUpdate({
    start: async () => {
      starts++;
      return { id: "job-1", state: "running", sources: [] };
    },
    status: async () => statuses[statusIndex++],
    wait: async () => {},
    onProgress: (value) => progress.push(value.state),
  });

  assert.equal(starts, 1);
  assert.equal(job.state, "succeeded");
  assert.deepEqual(progress, ["running", "running", "succeeded"]);
});

test("evaluation catalog update exposes terminal failure without retrying start", async () => {
  let starts = 0;
  await assert.rejects(
    () => runEvaluationCatalogUpdate({
      start: async () => {
        starts++;
        return { id: "job-2", state: "running", sources: [] };
      },
      status: async () => ({
        id: "job-2",
        state: "failed",
        error: "LiveBench 更新失败，仍在使用之前的数据。",
        sources: [],
      }),
      wait: async () => {},
      onProgress: () => {},
    }),
    /LiveBench 更新失败/,
  );
  assert.equal(starts, 1);
});
