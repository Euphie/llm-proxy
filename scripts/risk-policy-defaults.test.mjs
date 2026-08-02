import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import { defaultProfileDraft } from "../internal/admin/ui/static/profiles.js";

const goSource = await readFile(
  new URL("../internal/profile/auto_routing.go", import.meta.url),
  "utf8",
);

function goStringSlice(name) {
  const match = goSource.match(
    new RegExp(`var ${name} = \\[\\]string\\{([\\s\\S]*?)\\n\\}`),
  );
  assert.ok(match, `${name} not found`);
  return [...match[1].matchAll(/"(?:[^"\\]|\\.)*"/g)].map(
    ([literal]) => JSON.parse(literal),
  );
}

test("new Profile risk patterns mirror the Go defaults", () => {
  const policy = defaultProfileDraft().config.auto_routing.risk_policy;
  assert.deepEqual(
    policy.sensitive_text_patterns,
    goStringSlice("defaultSensitiveTextPatterns"),
  );
  assert.deepEqual(
    policy.sensitive_tool_patterns,
    goStringSlice("defaultSensitiveToolPatterns"),
  );
});
