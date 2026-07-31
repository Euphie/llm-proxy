import assert from "node:assert/strict";
import { access, readFile } from "node:fs/promises";
import test from "node:test";

const siteRoot = new URL("../", import.meta.url);

async function render(host = "localhost") {
  const workerUrl = new URL("../dist/server/index.js", import.meta.url);
  workerUrl.searchParams.set("test", `${process.pid}-${Date.now()}`);
  const { default: worker } = await import(workerUrl.href);

  return worker.fetch(
    new Request("http://localhost/", {
      headers: {
        accept: "text/html",
        host,
        "x-forwarded-host": "attacker.example",
        "x-forwarded-proto": "https",
      },
    }),
    {
      ASSETS: {
        fetch: async () => new Response("Not found", { status: 404 }),
      },
    },
    {
      waitUntil() {},
      passThroughOnException() {},
    },
  );
}

test("server-renders the intelligent-routing documentation shell", async () => {
  const response = await render();
  assert.equal(response.status, 200);
  assert.match(response.headers.get("content-type") ?? "", /^text\/html\b/i);

  const html = await response.text();
  assert.match(html, /<html[^>]*lang="zh-CN"/i);
  assert.match(html, /<title>llm-proxy 智能路由设计<\/title>/i);
  assert.match(html, /llm-proxy/);
  assert.match(html, /智能路由设计/);
  assert.match(html, /产品目标与设计原则/);
  assert.match(html, /model=auto/);
  assert.match(html, /mac-window/);
  assert.match(html, /docs-sidebar/);
  assert.match(html, /http:\/\/localhost\/og-routing-design\.png/);
  assert.doesNotMatch(html, /<p[^>]*>\s*<figure\b/i);
  assert.doesNotMatch(html, /attacker\.example/i);
  assert.doesNotMatch(html, /codex-preview|Your site is taking shape|react-loading-skeleton/i);
});

test("rejects malformed preview hosts without failing the page", async () => {
  const response = await render("localhost:99999");
  assert.equal(response.status, 200);

  const html = await response.text();
  assert.match(html, /<title>llm-proxy 智能路由设计<\/title>/i);
  assert.doesNotMatch(html, /og-routing-design\.png/i);
});

test("ships all chapters and exported architecture diagrams", async () => {
  const chapterNames = [
    "01-overview.md",
    "02-current-vs-target.md",
    "03-profile-configuration.md",
    "04-online-routing.md",
    "05-quality-and-cost.md",
    "06-runtime-reliability.md",
    "07-dynamic-optimization.md",
    "08-strategy-lifecycle.md",
    "09-engineering-and-delivery.md",
  ];
  const diagramNames = [
    "overview-architecture.svg",
    "current-modules.svg",
    "online-routing.svg",
    "vision-plans.svg",
    "dynamic-optimization.svg",
    "strategy-lifecycle.svg",
    "data-model.svg",
  ];

  await Promise.all([
    ...chapterNames.map((name) => access(new URL(`content/${name}`, siteRoot))),
    ...diagramNames.map((name) => access(new URL(`public/diagrams/${name}`, siteRoot))),
  ]);

  const contents = await Promise.all(
    chapterNames.map((name) => readFile(new URL(`content/${name}`, siteRoot), "utf8")),
  );
  assert.ok(
    contents.every((content) =>
      /^## (本章验收项|验收检查|上线门槛)$/m.test(content),
    ),
  );
  assert.ok(contents.some((content) => content.includes("/diagrams/data-model.svg")));
  assert.ok(contents.some((content) => content.includes("/diagrams/vision-plans.svg")));

  const [packageJson, viteConfig, readme] = await Promise.all([
    readFile(new URL("package.json", siteRoot), "utf8"),
    readFile(new URL("vite.config.ts", siteRoot), "utf8"),
    readFile(new URL("README.md", siteRoot), "utf8"),
  ]);
  const allContent = contents.join("\n");

  assert.match(readme, /main` 提交 `ea13e52/);
  assert.match(allContent, /client_context_window/);
  assert.match(allContent, /分层贝叶斯质量估计/);
  assert.match(allContent, /20260731-001/);
  assert.match(allContent, /关键词[^。\n]{0,40}不能单独触发高风险/);
  assert.doesNotMatch(allContent, /strategy-2026-07-31\.3/);
  assert.doesNotMatch(packageJson, /react-loading-skeleton/);
  assert.doesNotMatch(viteConfig, /CODEX_SANDBOX|site-creator|from\s+["']\.\/\.openai/);
  await assert.rejects(access(new URL("app/_sites-preview/SkeletonPreview.tsx", siteRoot)));
});
