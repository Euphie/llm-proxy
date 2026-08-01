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

test("server-renders every canonical chapter in the intelligent-routing documentation shell", async () => {
  const response = await render();
  assert.equal(response.status, 200);
  assert.match(response.headers.get("content-type") ?? "", /^text\/html\b/i);

  const html = await response.text();
  assert.ok(Buffer.byteLength(html) < 220_000, "The initial document must not serialize every chapter body.");
  assert.doesNotMatch(html, /三个实施阶段/);
  assert.match(html, /<html[^>]*lang="zh-CN"/i);
  assert.match(html, /<title>智能路由概览 · Mesotes<\/title>/i);
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
    "智能路由概览", "当前能力与目标", "参考方案与取舍", "Profile 配置与模型角色",
    "Profile 隔离边界", "在线路由", "质量与成本", "Target 与容错",
    "视觉、重试与 Session", "异步评测与策略优化", "策略生命周期", "实施范围与交付",
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
  assert.match(html, /<title>智能路由概览 · Mesotes<\/title>/i);
  assert.match(html, /https:\/\/docs\.example\.test\/og-routing-design\.png/i);
});

test("server-renders directive callouts with their accessible labels", async () => {
  const pages = await Promise.all([
    render("/docs/online-routing"),
    render("/docs/competitor-evidence"),
  ]);
  const html = (await Promise.all(pages.map((response) => response.text()))).join("\n");

  for (const label of ["当前已实现", "本版目标", "后续方向", "证据边界"]) {
    assert.match(html, new RegExp(`<aside[^>]*role=\\"note\\"[^>]*><p class=\\"status-callout-label\\">${label}`));
  }
  const renderedArticles = [...html.matchAll(/<article\b[\s\S]*?<\/article>/g)].map(([article]) => article).join("\n");
  assert.doesNotMatch(renderedArticles, /\[!(?:CURRENT|TARGET|FUTURE|EVIDENCE)\]/);
});

test("server-renders language-labelled code and accessible diagram openers", async () => {
  const response = await render("/docs/online-routing");
  const html = await response.text();

  assert.match(html, /class=\"code-language\">text</);
  assert.match(html, /aria-live=\"polite\"/);
  assert.match(html, /aria-label=\"放大查看：在线路由流程\"/);
});

test("server-renders one bounded precomputed route trace without secrets", async () => {
  const response = await render("/docs/online-routing");
  const html = await response.text();
  const explorer = explorerSubtree(html);

  for (const value of [
    "静态设计示例",
    "路由轨迹示例",
    "profile_acme_primary",
    "20260801-001",
    "代码助手-均衡版",
    "model_economy",
    "缺少所需工具能力",
    "model_balanced",
    "target_balanced_primary",
    "retryable_pre_commit_failure",
    "ClientCommit",
    "回答尝试",
    "辅助调用",
    "总出站调用",
    "单 Target 重试",
    "Target 切换",
    "模型切换",
    "deadline",
    "最坏成本",
    "ExecutionPlan 固定信息",
  ]) {
    assert.ok(explorer.includes(value), `Rendered trace is missing ${value}.`);
  }
  assert.match(explorer, /当前 Profile 与策略/);
  assert.doesNotMatch(explorer, /sha256:/);
  assertExplorerHasNoSecrets(explorer);
  assert.throws(() => assertExplorerHasNoSecrets('<dt>endpoint</dt><dd>https://internal.example/v1</dd>'));
  assert.throws(() => assertExplorerHasNoSecrets('<dt>authorization</dt><dd>secret</dd>'));
});
