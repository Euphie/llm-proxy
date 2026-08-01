import assert from "node:assert/strict";
import test from "node:test";

const chapters = [
  ["overview", "产品目标与非目标", "两层确定性路由", "以单一 Profile 边界组织模型选择、Target 调度和受控学习。"],
  ["source-baseline", "源码基线与现状差距", "固定快照", "用固定提交区分当前运行时事实、缺口与本版目标。"],
  ["competitor-evidence", "竞品源码证据矩阵", "集中证据矩阵", "从固定源码提炼可采用机制，并明确拒绝跨 Profile 编排。"],
  ["core-model", "核心对象与配置模型", "Profile：唯一边界", "定义全部归属单一 Profile 的路由、策略、计划与证据对象。"],
  ["profile-isolation", "Profile 隔离与协议边界", "入口协议与身份", "把 Profile 固化为 Provider 实例、信任域和唯一运行时隔离边界。"],
  ["online-routing", "在线决策流水线", "从请求到选择", "通过本地事实、硬约束、模型选择和 Target 调度生成不可变计划。"],
  ["quality-and-cost", "质量目标与模型选择", "Route-specific ServiceObjective", "先通过 Route 级质量门禁，再按条件优化成本与延迟。"],
  ["target-reliability", "Target 调度、容量与健康", "Target 与有效能力", "在同一 Profile、同一逻辑模型内完成准入、健康过滤和负载调度。"],
  ["runtime-reliability", "尝试预算、流式与会话", "统一 AttemptBudget", "用统一 AttemptBudget 和 ClientCommit 建立有界可靠性。"],
  ["evaluation-feedback", "评测、反馈与稳定实验", "反馈与数据绑定", "用 Profile-scoped 评测、强类型反馈和稳定分桶积累证据。"],
  ["strategy-lifecycle", "策略生命周期、观测与重放", "不可变策略与发布对象", "以不可变版本、CAS、灰度、LKG 和授权重放治理发布。"],
  ["engineering-and-delivery", "API、迁移与交付验收", "目标 API 与存储边界", "明确破坏性迁移、五个子项目和双重验收门。"],
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
  assert.ok(entries.some(({ text }) => text.includes("迁移记录以旧")));

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
