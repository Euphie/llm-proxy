import assert from "node:assert/strict";
import { access } from "node:fs/promises";
import test from "node:test";

const siteRoot = new URL("../", import.meta.url);

async function render(pathname = "/docs/overview", host = "localhost") {
  const workerUrl = new URL("../dist/server/index.js", import.meta.url);
  workerUrl.searchParams.set("test", `${process.pid}-${Date.now()}`);
  const { default: worker } = await import(workerUrl.href);
  return worker.fetch(
    new Request(`http://localhost${pathname}`, {
      headers: { accept: "text/html", host, "x-forwarded-host": "attacker.example", "x-forwarded-proto": "https" },
    }),
    { ASSETS: { fetch: async () => new Response("Not found", { status: 404 }) } },
    { waitUntil() {}, passThroughOnException() {} },
  );
}

function explorerSubtree(html) {
  const marker = 'data-route-trace-explorer="true"';
  const start = html.indexOf(marker);
  const boundary = html.indexOf('<nav class="chapter-pagination"', start);
  assert.notEqual(start, -1, "Route Trace Explorer needs a stable root marker.");
  assert.notEqual(boundary, -1, "Route Trace Explorer needs the following pagination boundary.");
  return html.slice(start, boundary);
}

function explorerFieldLabels(html) {
  return [...html.matchAll(/<(?:dt|th)\b[^>]*>([\s\S]*?)<\/(?:dt|th)>/gi)]
    .map(([, label]) => label.replace(/<[^>]*>/g, " "))
    .join(" ");
}

function explorerVisibleText(html) {
  return html.replace(/<[^>]*>/g, " ");
}

function assertExplorerHasNoSecrets(html) {
  assert.doesNotMatch(explorerFieldLabels(html), /\b(?:endpoint|credential|authorization|header|payload|transport(?:[_ -]?handle)?)\b/i);
  assert.doesNotMatch(explorerVisibleText(html), /(?:bearer\s+[a-z0-9._-]+|sk-[a-z0-9_-]{8,}|\bsecret\b)/i);
  assert.doesNotMatch(html, /(?:bearer\s+[a-z0-9._-]+|sk-[a-z0-9_-]{8,}|https?:\/\/[^\s"'<]+)/i);
}

test("server-renders every current chapter in the intelligent-routing documentation shell", async () => {
  const response = await render();
  assert.equal(response.status, 200);
  assert.match(response.headers.get("content-type") ?? "", /^text\/html\b/i);

  const html = await response.text();
  assert.ok(Buffer.byteLength(html) < 220_000, "The initial document must not serialize every chapter body.");
  assert.doesNotMatch(html, /三个实施阶段/);
  assert.match(html, /<html[^>]*lang="zh-CN"/i);
  assert.match(html, /<title>系统概览 · Mesotes<\/title>/i);
  assert.match(html, /MESOTES/);
  assert.match(html, /<img[^>]+src="\/brand\/mesotes-mark-256\.png"/);
  assert.doesNotMatch(html, /\/_vinext\/image\?url=%2Fbrand/i);
  assert.match(html, /<input[^>]+aria-label="搜索方案与模块"/);
  assert.match(html, /<link rel="(?:shortcut )?icon" href="\/favicon\.svg"/);
  assert.match(html, /mac-window/);
  assert.match(html, /docs-sidebar/);
  await Promise.all([
    access(new URL("public/brand/mesotes-mark.png", siteRoot)),
    access(new URL("public/brand/mesotes-mark-256.png", siteRoot)),
  ]);
  const requiredChapterTitles = [
    "系统概览", "后台操作流程", "Profile 与模型目录", "Routing Policy",
    "在线请求链路", "视觉预处理", "重试、超时与 Session", "评测与自动校准",
    "统计与故障排查", "运行边界与上线检查",
  ];
  const missing = requiredChapterTitles.filter((title) => !html.includes(title));
  assert.deepEqual(missing, [], `Rendered documentation is missing canonical chapters: ${missing.join("、")}`);
  assert.doesNotMatch(html, /provider_kind|provider_id/i);
  assert.doesNotMatch(html, /attacker\.example/i);
});

test("ignores malformed request hosts in favor of the configured public origin", async () => {
  const response = await render("/docs/overview", "localhost:99999");
  assert.equal(response.status, 200);
  const html = await response.text();
  assert.match(html, /<title>系统概览 · Mesotes<\/title>/i);
  assert.doesNotMatch(html, /og-routing-design\.png/i);
});

test("server-renders directive callouts with their accessible labels", async () => {
  const pages = await Promise.all([
    render("/docs/online-routing"),
    render("/docs/evaluation"),
    render("/docs/operations"),
  ]);
  const html = (await Promise.all(pages.map((response) => response.text()))).join("\n");

  for (const label of ["当前已实现", "证据边界"]) {
    assert.match(html, new RegExp(`<aside[^>]*role=\\"note\\"[^>]*><p class=\\"status-callout-label\\">${label}`));
  }
  const renderedArticles = [...html.matchAll(/<article\b[\s\S]*?<\/article>/g)].map(([article]) => article).join("\n");
  assert.doesNotMatch(renderedArticles, /\[!(?:CURRENT|TARGET|FUTURE|EVIDENCE)\]/);
});

test("server-renders language-labelled code blocks", async () => {
  const response = await render("/docs/online-routing");
  const html = await response.text();

  assert.match(html, /class=\"code-language\">text</);
  assert.match(html, /aria-live=\"polite\"/);
});

test("server-renders every flow diagram as an accessible zoom control", async () => {
  const pages = await Promise.all([
    render("/docs/overview"),
    render("/docs/online-routing"),
    render("/docs/vision"),
    render("/docs/reliability"),
    render("/docs/evaluation"),
    render("/docs/observability"),
  ]);
  const html = (await Promise.all(pages.map((response) => response.text()))).join("\n");

  for (const [name, alt] of [
    ["system-architecture", "当前 V2 系统架构"],
    ["online-routing-flow", "model=auto 在线请求链路"],
    ["vision-processing-flow", "视觉预处理决策与调用链路"],
    ["retry-timeout-state-machine", "重试、超时与提交边界状态机"],
    ["session-lock-lifecycle", "Session 锁定生命周期"],
    ["policy-calibration-loop", "Routing Policy 自动校准闭环"],
    ["observability-troubleshooting", "一次请求的统计与排障链路"],
  ]) {
    assert.match(html, new RegExp(`aria-label=\\"放大查看：${alt}\\"`));
    assert.match(html, new RegExp(`src=\\"/diagrams/${name}\\.svg\\"`));
  }
});

test("server-renders one bounded precomputed route trace without secrets", async () => {
  const response = await render("/docs/online-routing");
  const html = await response.text();
  const explorer = explorerSubtree(html);

  for (const value of [
    "当前字段示例",
    "路由轨迹示例",
    "profile_demo",
    "20260818-001",
    "日常均衡",
    "model_shadow",
    "没有 Production 资格",
    "model_primary",
    "retryable_pre_commit_failure",
    "ClientCommit",
    "回答尝试",
    "辅助调用",
    "总出站调用",
    "单 Upstream 重试",
    "模型切换",
    "deadline",
    "最坏成本",
    "ExecutionPlan 固定信息",
  ]) {
    assert.ok(explorer.includes(value), `Rendered trace is missing ${value}.`);
  }
  assert.match(explorer, /当前 Profile 与 Policy/);
  assert.doesNotMatch(explorer, /sha256:/);
  assertExplorerHasNoSecrets(explorer);
  assert.throws(() => assertExplorerHasNoSecrets('<dt>endpoint</dt><dd>https://internal.example/v1</dd>'));
  assert.throws(() => assertExplorerHasNoSecrets('<dt>authorization</dt><dd>secret</dd>'));
});
