import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { readFile } from "node:fs/promises";
import test from "node:test";

import { expandEvidenceMarkers } from "../lib/content-contract.ts";
import { getStatusCalloutMatch } from "../lib/markdown-directives.ts";
import { getSiteOrigin } from "../lib/site-origin.ts";
import {
  buildSearchEntries,
  extractMarkdownHeadings,
  findSearchMatchRanges,
  markdownToPlainText,
  readingTimeMinutes,
  searchEntries,
} from "../lib/search.ts";

const dataUrl = new URL("../data/", import.meta.url);

async function readJson(name) {
  return JSON.parse(await readFile(new URL(name, dataUrl), "utf8"));
}

test("fails closed when a production public origin is missing or unsafe", () => {
  assert.equal(getSiteOrigin({ NODE_ENV: "development" }), "http://localhost:3000");
  assert.throws(() => getSiteOrigin({}), /NEXT_PUBLIC_SITE_URL.*HTTPS origin/i);
  assert.equal(
    getSiteOrigin({ NODE_ENV: "production", NEXT_PUBLIC_SITE_URL: "https://docs.example.test/" }),
    "https://docs.example.test",
  );
  for (const configured of [
    undefined,
    "http://docs.example.test",
    "https://user:secret@docs.example.test",
    "https://docs.example.test/path",
    "https://docs.example.test?preview=1",
    "not a URL",
  ]) {
    assert.throws(
      () => getSiteOrigin({ NODE_ENV: "production", NEXT_PUBLIC_SITE_URL: configured }),
      /NEXT_PUBLIC_SITE_URL.*HTTPS origin/i,
    );
  }
});

test("runs the strict public-origin validator before every production build", async () => {
  const siteRoot = new URL("../", import.meta.url);
  const withoutOrigin = { ...process.env };
  delete withoutOrigin.NEXT_PUBLIC_SITE_URL;
  const rejected = spawnSync(
    process.execPath,
    ["--experimental-strip-types", "scripts/validate-site-origin.mjs"],
    { cwd: siteRoot, env: withoutOrigin, encoding: "utf8" },
  );
  assert.notEqual(rejected.status, 0);
  assert.match(rejected.stderr, /NEXT_PUBLIC_SITE_URL.*HTTPS origin/i);

  const accepted = spawnSync(
    process.execPath,
    ["--experimental-strip-types", "scripts/validate-site-origin.mjs"],
    {
      cwd: siteRoot,
      env: { ...withoutOrigin, NEXT_PUBLIC_SITE_URL: "https://docs.example.test" },
      encoding: "utf8",
    },
  );
  assert.equal(accepted.status, 0, accepted.stderr);
  const packageJson = JSON.parse(await readFile(new URL("package.json", siteRoot), "utf8"));
  assert.match(packageJson.scripts.build, /validate-site-origin\.mjs/);
});

test("streams only explicit documentation inputs into fail-closed verification containers", async () => {
  const makefile = await readFile(new URL("../../Makefile", import.meta.url), "utf8");
  const docsTargets = /## docs-verify[\s\S]*?(?=## docker-build)/.exec(makefile)?.[0] ?? "";
  assert.match(makefile, /^DOCS_SITE_INPUTS\s*:=/m);
  assert.match(makefile, /^DOCS_ARCHIVE_INPUTS\s*:=\s*Makefile\s+/m);
  assert.match(makefile, /^SHELL\s*:=\s*\/bin\/bash$/m);
  assert.match(makefile, /^\.SHELLFLAGS\s*:=\s*-o pipefail -c$/m);
  assert.equal(
    (docsTargets.match(/COPYFILE_DISABLE=1 tar --no-xattrs --exclude='\._\*' --exclude='\.DS_Store' -C "\$\(CURDIR\)" -cf - \$\(DOCS_ARCHIVE_INPUTS\)/g) ?? []).length,
    3,
  );
  assert.equal((docsTargets.match(/docker run --rm -i/g) ?? []).length, 3);
  assert.doesNotMatch(docsTargets, /(?:^|\s)-v(?:\s|$)/m);
  assert.doesNotMatch(docsTargets, /cp -a/);
  const inputs = makefile.match(/^DOCS_SITE_INPUTS\s*:=.*$/m)?.[0] ?? "";
  for (const forbidden of ["node_modules", "dist", ".env", "test-results", "playwright-report"]) {
    assert.doesNotMatch(inputs, new RegExp(forbidden));
  }
  assert.doesNotMatch(docsTargets, /(?:^|\/)\._[^*]/m);
});

test("central evidence files use the pinned data envelopes", async () => {
  const [baselines, competitors] = await Promise.all([
    readJson("source-baselines.json"),
    readJson("competitor-evidence.json"),
  ]);

  assert.equal(baselines.runtime_baseline.commit, "81a2fc457c69323fc3e5bd63f68da9191bc9b587");
  assert.equal(baselines.runtime_baseline.verified_runtime_facts.length, 7);
  assert.deepEqual(Object.keys(baselines), ["runtime_baseline"]);

  assert.equal(competitors.length, 10);
  assert.equal(competitors.filter(({ evidence_type }) => evidence_type === "source").length, 6);
  assert.equal(
    competitors.filter(({ evidence_type }) => evidence_type === "official_documentation").length,
    3,
  );
  assert.equal(
    competitors.filter(({ evidence_type }) => evidence_type === "closed_documentation").length,
    1,
  );
  for (const entry of competitors) {
    assert.equal(typeof entry.native_mechanism, "string");
    assert.equal(typeof entry.mesotes_mapping, "string");
    assert.ok(entry.adopt.length > 0);
    assert.ok(entry.reject_or_defer.length > 0);
    assert.ok(entry.permalinks.length > 0);
  }
});

test("expands every evidence marker and rejects unknown contract markers", async () => {
  const [sourceBaselines, competitorEvidence] = await Promise.all([
    readJson("source-baselines.json"),
    readJson("competitor-evidence.json"),
  ]);
  const expanded = expandEvidenceMarkers(
    "{{SOURCE_BASELINE}}\n\n{{SOURCE_FACTS}}\n\n{{COMPETITOR_EVIDENCE}}",
    { sourceBaselines, competitorEvidence },
  );

  assert.match(expanded, /81a2fc457c69323fc3e5bd63f68da9191bc9b587/);
  assert.match(expanded, /Auto 配置按 Profile 编译并完整校验/);
  assert.match(expanded, /LiteLLM/);
  assert.doesNotMatch(expanded, /\{\{[A-Z][A-Z0-9_]*\}\}/);
  assert.throws(
    () => expandEvidenceMarkers("{{UNKNOWN_CONTRACT}}", { sourceBaselines, competitorEvidence }),
    /Unknown content contract marker: UNKNOWN_CONTRACT/,
  );
});

test("assigns stable unique IDs to duplicate, Chinese, and empty headings", () => {
  const headings = extractMarkdownHeadings(`
## 在线路由
## 在线路由
\`\`\`md
## fenced-code-is-not-a-heading
\`\`\`
##
##
`);

  assert.deepEqual(
    headings.map(({ id, title }) => ({ id, title })),
    [
      { id: "在线路由", title: "在线路由" },
      { id: "在线路由-2", title: "在线路由" },
      { id: "section", title: "" },
      { id: "section-2", title: "" },
    ],
  );
});

test("keeps headings hidden until a CommonMark fence is legally closed", () => {
  const headings = extractMarkdownHeadings(`
\`\`\`\`md
\`\`\`
## hidden-after-short-fence
\`\`\`\` trailing-text
## hidden-after-suffixed-fence
\`\`\`\`\`${"   "}
## visible-after-longer-fence
`);

  assert.deepEqual(
    headings.map(({ id }) => id),
    ["visible-after-longer-fence"],
  );
});

test("recognizes directives only from the first paragraph's first raw text child", () => {
  const inlineCode = { type: "element", tagName: "code", children: [{ type: "text", value: "[!CURRENT]" }] };
  const link = { type: "element", tagName: "a", properties: { href: "/docs/overview" }, children: [{ type: "text", value: "链接" }] };
  const valid = {
    type: "element",
    tagName: "blockquote",
    children: [{
      type: "element",
      tagName: "p",
      children: [
        { type: "text", value: "[!TARGET]\n保留 " },
        inlineCode,
        { type: "text", value: " 与 " },
        link,
      ],
    }],
  };

  assert.deepEqual(getStatusCalloutMatch(valid), {
    kind: "TARGET",
    strippedText: "\n保留 ",
  });
  assert.deepEqual(valid.children[0].children.slice(1), [inlineCode, { type: "text", value: " 与 " }, link]);

  const visibleInlineCodeToken = {
    type: "element",
    tagName: "blockquote",
    children: [{
      type: "element",
      tagName: "p",
      children: [inlineCode, { type: "text", value: " 是普通引用。" }],
    }],
  };
  assert.equal(getStatusCalloutMatch(visibleInlineCodeToken), undefined);
});

test("builds linkable section entries and ranks heading and body hits stably", () => {
  const entries = buildSearchEntries([
    {
      slug: "routing",
      title: "在线决策",
      summary: "同一 Profile 内生成执行计划。",
      content: "开篇说明。\n\n## 硬约束\n先过滤地域，再选择模型。\n\n## 调度\nTarget 容量准入。",
    },
    {
      slug: "reliability",
      title: "运行可靠性",
      summary: "有界重试。",
      content: "## 提交边界\n首次合法事件形成 ClientCommit。",
    },
  ]);

  assert.deepEqual(entries.map(({ href }) => href), [
    "/docs/routing",
    "/docs/routing#硬约束",
    "/docs/routing#调度",
    "/docs/reliability",
    "/docs/reliability#提交边界",
  ]);
  assert.equal(searchEntries(entries, "硬约束")[0].href, "/docs/routing#硬约束");
  assert.equal(searchEntries(entries, "clientcommit")[0].href, "/docs/reliability#提交边界");
  assert.deepEqual(
    searchEntries(entries, "Profile").map(({ href }) => href),
    ["/docs/routing"],
  );
  assert.equal(searchEntries(entries, "不存在").length, 0);
});

test("uses the page title for H1 without creating a duplicate section result", () => {
  const entries = buildSearchEntries([
    {
      slug: "scope",
      title: "Page title",
      summary: "Summary.",
      content: "# Page title\n\n## Linkable section\nProfile-scoped body.",
    },
  ]);

  assert.deepEqual(entries.map(({ href, title }) => ({ href, title })), [
    { href: "/docs/scope", title: "Page title" },
    { href: "/docs/scope#linkable-section", title: "Linkable section" },
  ]);
});

test("keeps opening callouts between H1 and H2 in the chapter root search entry", () => {
  const entries = buildSearchEntries([
    {
      slug: "intro",
      title: "Intro",
      summary: "Summary.",
      content: "# Intro\n\n[!TARGET]\nOpening callout sentinel.\n\n## First section\nSection body.",
    },
  ]);

  assert.deepEqual(
    searchEntries(entries, "opening callout sentinel").map(({ href }) => href),
    ["/docs/intro"],
  );
});

test("maps compatibility and canonical-equivalent queries to complete source graphemes", () => {
  assert.deepEqual(findSearchMatchRanges("x ﬃ y", "ﬃ"), [{ start: 2, end: 3 }]);
  assert.deepEqual(findSearchMatchRanges("x ﬃ y", "ffi"), [{ start: 2, end: 3 }]);
  assert.deepEqual(findSearchMatchRanges("Cafe\u0301", "é"), [{ start: 3, end: 5 }]);
  assert.deepEqual(findSearchMatchRanges("Café", "e\u0301"), [{ start: 3, end: 4 }]);
});

test("returns stable non-overlapping source ranges for every matched token", () => {
  assert.deepEqual(
    findSearchMatchRanges("foo ﬃ foo cafe\u0301", "ffi foo é"),
    [
      { start: 0, end: 3 },
      { start: 4, end: 5 },
      { start: 6, end: 9 },
      { start: 13, end: 15 },
    ],
  );
});

test("keeps the visible source match in snippets after NFKC expands a ligature", () => {
  const entries = buildSearchEntries([
    {
      slug: "compatibility",
      title: "Compatibility",
      summary: "NFKC snippet mapping.",
      content: `## Evidence\n${"ﬃ".repeat(60)}匹配词 ${"尾部文字".repeat(30)}`,
    },
  ]);

  const [result] = searchEntries(entries, "匹配词");
  assert.match(result.snippet, /匹配词/);
});

test("converts Markdown to plain text and computes bounded reading time", () => {
  const markdown = "## 标题\n[可见文本](https://example.com) 与 `inline_code`。\n\n```txt\nraw value\n```";
  assert.equal(markdownToPlainText(markdown), "标题 可见文本 与 inline_code。 raw value");
  assert.equal(readingTimeMinutes("很短"), 1);
  assert.equal(readingTimeMinutes("路".repeat(1041)), 3);
});

test("the trace is a bounded same-Profile design example without secret-bearing fields", async () => {
  const trace = await readJson("route-trace-example.json");
  assert.equal(trace.example_kind, "static_design");
  assert.equal(trace.projection_kind, "sanitized_example");
  assert.equal(trace.label, "静态设计示例");
  assert.deepEqual(trace.scope, { profile_id: "profile_acme_primary" });
  assert.equal(trace.execution_plan.profile_id, trace.scope.profile_id);
  assert.equal(trace.task_analysis.local_rule_result, "uncertain");
  assert.equal(trace.task_analysis.analyzer_called, true);
  assert.equal(trace.task_analysis.result, "code_change");
  assert.equal(trace.route.matched_by, "task_type");
  assert.equal(trace.route.task_type, trace.task_analysis.result);
  assert.ok(trace.hard_filter.excluded.length > 0);
  assert.match(trace.model_selection.selected_model_id, /^model_/);
  assert.deepEqual(Object.keys(trace.execution_plan.snapshot_refs).sort(), [
    "catalog_version", "price_version", "strategy_id",
  ]);
  assert.deepEqual(trace.execution_plan.auxiliary_attempts, []);
  assert.equal(trace.execution_plan.attempts.length, 2);
  assert.ok(trace.execution_plan.attempts.every((attempt) => (
    /^target_/.test(attempt.target_id) &&
    /^model_/.test(attempt.model_id) &&
    /^answer/.test(attempt.purpose)
  )));
  assert.equal(new Set(trace.execution_plan.attempts.map(({ target_id }) => target_id)).size, 1);
  assert.equal(trace.attempts[0].outcome, "retryable_pre_commit_failure");
  assert.equal(trace.attempts[1].outcome, "ClientCommit");
  assert.deepEqual(Object.keys(trace.attempt_budget), [
    "max_answer_attempts",
    "max_auxiliary_calls",
    "max_total_outbound_calls",
    "max_retries_per_target",
    "max_target_switches",
    "max_model_switches",
    "deadline",
    "max_worst_case_cost_micro_usd",
  ]);

  const json = JSON.stringify(trace);
  assert.doesNotMatch(json, /endpoint|credential|authorization|header|payload|transport/i);
  const ids = [...json.matchAll(/"(?:[a-z_]+_id)":"([^"]+)"/g)].map((match) => match[1]);
  assert.ok(
    ids.every((id) => id === "profile_acme_primary" || /^(?:model|target|route)_/.test(id) || /^\d{8}-\d{3}$/.test(id)),
  );
});
