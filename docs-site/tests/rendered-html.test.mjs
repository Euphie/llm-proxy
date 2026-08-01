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
  assert.doesNotMatch(html, /迁移记录以旧/);
  assert.match(html, /<html[^>]*lang="zh-CN"/i);
  assert.match(html, /<title>产品目标与非目标 · Mesotes<\/title>/i);
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
    "产品目标与非目标", "源码基线与现状差距", "竞品源码证据矩阵", "核心对象与配置模型",
    "Profile 隔离与协议边界", "在线决策流水线", "质量目标与模型选择", "Target 调度、容量与健康",
    "尝试预算、流式与会话", "评测、反馈与稳定实验", "策略生命周期、观测与重放", "API、迁移与交付验收",
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
  assert.match(html, /<title>产品目标与非目标 · Mesotes<\/title>/i);
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
  assert.match(html, /aria-label=\"放大查看：在线决策流水线\"/);
});

test("server-renders one bounded precomputed route trace without secrets", async () => {
  const response = await render("/docs/online-routing");
  const html = await response.text();
  const explorer = explorerSubtree(html);

  for (const value of [
    "预计算示例",
    "profile_acme_primary",
    "generation",
    "sha256:8bb490e5d6c4",
    "model_economy",
    "缺少所需工具能力",
    "model_balanced",
    "target_balanced_a",
    "target_balanced_b",
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
    "ExecutionPlan 版本绑定",
  ]) {
    assert.ok(explorer.includes(value), `Rendered trace is missing ${value}.`);
  }
  assert.match(explorer, /固定的单一 Profile 范围/);
  assertExplorerHasNoSecrets(explorer);
  assert.throws(() => assertExplorerHasNoSecrets('<dt>endpoint</dt><dd>https://internal.example/v1</dd>'));
  assert.throws(() => assertExplorerHasNoSecrets('<dt>authorization</dt><dd>secret</dd>'));
});
