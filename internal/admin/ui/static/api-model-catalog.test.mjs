import assert from "node:assert/strict";
import test from "node:test";

import { api } from "./api.js";

test("model catalog API uses authenticated fixed routes and CSRF on manual refresh", async (t) => {
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

  await api.modelCatalog();
  await api.refreshModelCatalog();

  assert.deepEqual(calls, [
    {
      path: "/_admin/api/model-catalog",
      method: "GET",
      body: undefined,
      csrf: null,
    },
    {
      path: "/_admin/api/model-catalog/refresh",
      method: "POST",
      body: {},
      csrf: "csrf-token",
    },
  ]);
});
