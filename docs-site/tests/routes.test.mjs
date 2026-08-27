import assert from "node:assert/strict";
import test from "node:test";

const chapters = [
  ["overview", "系统概览", "两种请求模式", "说明显式模型与 model=auto 的边界，以及当前系统实际负责什么。"],
  ["admin-workflow", "后台操作流程", "第一次配置", "按照当前管理后台页面完成 Profile、模型、策略和 Agent 配置。"],
  ["profiles-models", "Profile 与模型目录", "Profile 是隔离边界", "说明连接配置、模型事实、状态和运行时修订如何立即生效。"],
  ["routing-policy", "Routing Policy", "当前生效模型", "说明策略生成、编辑、生产资格、立即生效和历史回滚。"],
  ["online-routing", "在线请求链路", "请求处理顺序", "从任务分析、候选门槛到执行计划解释一次 model=auto 请求。"],
  ["vision", "视觉预处理", "视觉不是最终回答模型", "解释原生视觉与复合视觉、识图缓存以及失败边界。"],
  ["reliability", "重试、超时与 Session", "重试何时发生", "解释 overload_rules、统一预算、ClientCommit 和 Session 锁定。"],
  ["evaluation", "评测与自动校准", "证据从哪里来", "说明公开评测、本地证据、Shadow 和自动生产资格更新。"],
  ["observability", "统计与故障排查", "先确定失败阶段", "使用路由轨迹、物理调用和模型表现定位请求问题。"],
  ["operations", "运行边界与上线检查", "上线前检查", "汇总 Profile 隔离、网关职责、热更新和上线验收边界。"],
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
    assert.doesNotMatch(html, /og-routing-design\.png/);
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
  assert.ok(entries.some(({ text }) => text.includes("任务分析模型")));

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
