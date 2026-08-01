import assert from "node:assert/strict";
import test from "node:test";

const chapters = [
  ["overview", "智能路由概览", "一句话理解", "用一个简单流程说明 auto 如何在单个 Profile 内选模型、控成本并守住质量。"],
  ["source-baseline", "当前能力与目标", "固定基线", "区分当前已实现能力与智能路由的待实现目标。"],
  ["competitor-evidence", "参考方案与取舍", "参考矩阵", "提炼竞品中值得采用的机制，并明确不照搬的复杂设计。"],
  ["core-model", "Profile 配置与模型角色", "启用 auto 的必要配置", "说明启用 auto 所需配置，以及 ModelCard、Target 和模型角色。"],
  ["profile-isolation", "Profile 隔离边界", "边界如何工作", "URL 选定 Profile 后，路由、视觉、重试和评测始终留在该边界内。"],
  ["online-routing", "在线路由", "从请求到执行", "规则先判断，拿不准时调用轻量任务分析器，再生成不可变执行计划。"],
  ["quality-and-cost", "质量与成本", "Route 质量门槛", "先满足 Route 质量门槛，再选择完整成本更低的模型。"],
  ["target-reliability", "Target 与容错", "为什么要分开", "在同一 Profile 内选择模型部署，并以有界重试处理临时故障。"],
  ["runtime-reliability", "视觉、重试与 Session", "视觉三态", "把视觉辅助、尝试预算、流式提交和 Session 连续性放进同一执行边界。"],
  ["evaluation-feedback", "异步评测与策略优化", "开启条件", "用异步样本评测积累证据，只生成候选策略，不自动改变线上选择。"],
  ["strategy-lifecycle", "策略生命周期", "命名与状态", "以不可变版本、CAS、灰度和 LKG 安全发布或回滚策略。"],
  ["engineering-and-delivery", "实施范围与交付", "v1 边界", "明确 v1 范围、实施阶段和验收条件。"],
];

async function render(pathname, headers = {}) {
  const workerUrl = new URL("../dist/server/index.js", import.meta.url);
  workerUrl.searchParams.set("test", `${process.pid}-${Date.now()}-${pathname}`);
  const { default: worker } = await import(workerUrl.href);
  return worker.fetch(
    new Request(`http://localhost${pathname}`, {
      headers: { accept: "text/html", ...headers },
    }),
    { ASSETS: { fetch: async () => new Response("Not found", { status: 404 }) } },
    { waitUntil() {}, passThroughOnException() {} },
  );
}

test("the root route redirects to the canonical overview document", async () => {
  const response = await render("/");

  assert.equal(response.status, 307);
  const location = response.headers.get("location");
  assert.ok(location);
  assert.equal(new URL(location).pathname, "/docs/overview");
});

test("every document route server-renders its requested article without JavaScript", async () => {
  for (const [slug, title, articleHeading] of chapters) {
    const response = await render(`/docs/${slug}`);
    assert.equal(response.status, 200, slug);

    const html = await response.text();
    assert.match(html, /<article\b/i, `${slug} is missing an SSR article`);
    assert.match(html, new RegExp(title), `${slug} is missing its SSR title`);
    assert.match(html, new RegExp(articleHeading), `${slug} is missing its SSR body`);
  }
});

test("unknown document slugs return a 404 response", async () => {
  const response = await render("/docs/not-a-real-chapter");

  assert.equal(response.status, 404);
});

test("each document has page-specific safe canonical and Open Graph metadata", async () => {
  for (const [slug, title, , summary] of chapters) {
    const response = await render(`/docs/${slug}`);
    const html = await response.text();
    const canonical = `https://docs.example.test/docs/${slug}`;

    assert.match(html, new RegExp(`<title>${title} · Mesotes</title>`));
    assert.match(html, new RegExp(`name=\"description\" content=\"${summary}\"`));
    assert.match(html, new RegExp(`rel=\"canonical\" href=\"${canonical}\"`));
    assert.match(html, new RegExp(`property=\"og:url\" content=\"${canonical}\"`));
    assert.match(
      html,
      /property="og:image" content="https:\/\/docs\.example\.test\/og-routing-design\.png"/,
    );
    assert.match(html, /property="og:image:width" content="1200"/);
    assert.match(html, /property="og:image:height" content="630"/);
  }
});

test("request host headers cannot influence canonical metadata", async () => {
  const response = await render("/docs/overview", {
    host: "attacker.example",
    "x-forwarded-host": "also-attacker.example",
    "x-forwarded-proto": "https",
  });
  const html = await response.text();

  assert.match(html, /rel="canonical" href="https:\/\/docs\.example\.test\/docs\/overview"/);
  assert.doesNotMatch(html, /attacker\.example/);
});

test("serves the full-text search index only from its on-demand endpoint", async () => {
  const response = await render("/api/search-index");
  assert.equal(response.status, 200);
  assert.match(response.headers.get("content-type") ?? "", /^application\/json\b/i);
  assert.equal(response.headers.get("cache-control"), "public, max-age=0, must-revalidate");
  const version = response.headers.get("x-search-index-version");
  const etag = response.headers.get("etag");
  assert.match(version ?? "", /^[a-f0-9]{16}$/);
  assert.equal(etag, `"${version}"`);
  const entries = await response.json();
  assert.ok(Array.isArray(entries));
  assert.ok(entries.length > chapters.length);
  assert.ok(entries.some(({ text }) => text.includes("轻量任务分析器")));

  const versioned = await render(`/api/search-index?v=${version}`);
  assert.equal(versioned.status, 200);
  assert.equal(versioned.headers.get("cache-control"), "public, max-age=31536000, immutable");
  assert.equal(versioned.headers.get("etag"), etag);

  const unchanged = await render(`/api/search-index?v=${version}`, { "if-none-match": etag });
  assert.equal(unchanged.status, 304);

  const mismatched = await render("/api/search-index?v=0000000000000000");
  assert.equal(mismatched.status, 409);
  assert.equal(mismatched.headers.get("cache-control"), "no-store");
  assert.equal(mismatched.headers.get("x-search-index-version"), version);
  assert.equal(mismatched.headers.get("x-content-type-options"), "nosniff");
});
