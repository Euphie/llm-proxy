import assert from "node:assert/strict";
import test from "node:test";

import { api } from "./api.js";

test("Policy preview generation and evaluation catalog APIs use fixed CSRF-protected routes", async (t) => {
  const calls = [];
  const originalDocument = globalThis.document;
  const originalFetch = globalThis.fetch;
  globalThis.document = { cookie: "llm_proxy_csrf=csrf-token" };
  globalThis.fetch = async (path, options) => {
    calls.push({
      path,
      method: options.method,
      body: options.body === undefined ? undefined : JSON.parse(options.body),
      csrf: options.headers.get("X-CSRF-Token"),
    });
    return new Response("{}", { status: 200 });
  };
  t.after(() => {
    globalThis.document = originalDocument;
    globalThis.fetch = originalFetch;
  });

  const intent = {
    objective: "balanced",
    participants: ["fast", "strong"],
    latency_target_ms: 1200,
  };
  await api.evaluationCatalog();
	await api.startEvaluationCatalogUpdate();
  await api.evaluationCatalogUpdateStatus();
  await api.generateRoutingPolicy(7, intent);

  assert.deepEqual(calls, [
    {
      path: "/_admin/api/evaluation-catalog",
      method: "GET",
      body: undefined,
      csrf: null,
    },
    {
      path: "/_admin/api/evaluation-catalog/update",
      method: "POST",
      body: {},
      csrf: "csrf-token",
    },
    {
      path: "/_admin/api/evaluation-catalog/update-status",
      method: "GET",
      body: undefined,
      csrf: null,
    },
    {
      path: "/_admin/api/profiles/7/routing-policy/generate",
      method: "POST",
      body: intent,
      csrf: "csrf-token",
    },
  ]);
});
