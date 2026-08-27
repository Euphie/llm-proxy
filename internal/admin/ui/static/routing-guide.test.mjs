import assert from "node:assert/strict";
import test from "node:test";

import { renderHelpPage } from "./routing-guide.js";

test("built-in help follows the current Profile workflow without external links", (t) => {
  const root = installFakeDOM(t);
  renderHelpPage(root, "intelligent-routing");

  const navigation = descendants(root).find(
    (element) => element.getAttribute("aria-label") === "帮助主题",
  );
  assert.ok(navigation);
  assert.deepEqual(navigation.children.map((link) => link.textContent), [
    "快速开始",
    "Profile 与模型目录",
    "Routing Policy",
    "视觉预处理",
    "重试与超时",
    "Agent 配置",
    "统计与排障",
    "术语表",
  ]);
  assert.equal(
    navigation.children.find((link) => link.textContent === "Routing Policy")
      .getAttribute("aria-current"),
    "page",
  );
  assert.ok(findText(root, "线上 Routing Policy"));
  assert.ok(findText(root, "保存并立即生效"));
  assert.ok(findText(root, "Production 与 Shadow"));
  assert.ok(findText(root, "Session 锁定 Token 阈值"));
  assert.ok(findText(root, "默认 100K"));
  assert.equal(
    descendants(root).find((element) => element.textContent === "先看名词解释")
      .getAttribute("href"),
    "/_admin/help/glossary",
  );
  const images = descendants(root).filter((element) => element.tagName === "IMG");
  assert.equal(images.length, 1);
  assert.equal(images[0].getAttribute("src"), "/_admin/assets/current/online-routing-flow.svg");
  assert.equal(images[0].getAttribute("loading"), "lazy");
  assert.ok(images[0].getAttribute("alt"));
  assert.ok(findText(root, "任务映射与 Route"));
  assert.ok(findText(root, "生产资格"));
  assert.ok(findText(root, "统一尝试预算"));
  assert.ok(findText(root, "变更会写入不可变历史"));
  for (const link of descendants(root).filter((item) => item.tagName === "A")) {
    assert.match(link.getAttribute("href"), /^\/_admin\//);
  }
});

test("built-in help reuses the four operational diagrams with accessible text", (t) => {
  const root = installFakeDOM(t);
  const images = [];
  for (const topic of ["overview", "intelligent-routing", "vision", "reliability"]) {
    renderHelpPage(root, topic);
    images.push(...descendants(root).filter((element) => element.tagName === "IMG"));
  }

  assert.deepEqual(images.map((image) => image.getAttribute("src")), [
    "/_admin/assets/current/system-architecture.svg",
    "/_admin/assets/current/online-routing-flow.svg",
    "/_admin/assets/current/vision-processing-flow.svg",
    "/_admin/assets/current/retry-timeout-state-machine.svg",
  ]);
  for (const image of images) {
    assert.equal(image.getAttribute("loading"), "lazy");
    assert.ok(image.getAttribute("alt"));
  }
});

test("every built-in help topic renders its current operational entry point", (t) => {
  const root = installFakeDOM(t);
  for (const [topic, expected] of [
    ["overview", "推荐操作顺序"],
    ["profiles-models", "模型目录修订"],
    ["intelligent-routing", "保存并立即生效"],
    ["vision", "最终回答模型不会自动切成识图模型"],
    ["reliability", "overload_rules"],
    ["agents", "不会把输入的临时密钥保存"],
    ["statistics", "0 / network"],
    ["glossary", "Production 候选"],
  ]) {
    renderHelpPage(root, topic);
    assert.ok(findText(root, expected), `${topic} content missing`);
  }
});

test("Statistics help documents safe intelligent routing debug logs", (t) => {
  const root = installFakeDOM(t);
  renderHelpPage(root, "statistics");

  assert.ok(findText(root, "LOG_LEVEL=debug"));
  assert.ok(findText(root, "request_trace_id"));
  assert.ok(findText(root, "不记录请求正文"));
	assert.ok(findText(root, "用量统计、路由轨迹和模型表现"));
	assert.ok(findText(root, "候选排除原因"));
});

test("glossary distinguishes classification dimensions from historical tools", (t) => {
	const root = installFakeDOM(t);
	renderHelpPage(root, "glossary");
	for (const text of ["任务类型", "难度", "风险", "分类置信度", "普通 Shell/Edit"]) {
		assert.ok(findText(root, text), text);
	}
});

class FakeElement {
  constructor(tagName) {
    this.tagName = tagName.toUpperCase();
    this.children = [];
    this.className = "";
    this.textContent = "";
    this.href = "";
    this.src = "";
    this.alt = "";
    this.loading = "";
    this.attributes = new Map();
  }

  append(...children) {
    this.children.push(...children);
  }

  replaceChildren(...children) {
    this.children = [...children];
  }

  setAttribute(name, value) {
    this.attributes.set(name, String(value));
  }

  getAttribute(name) {
    return this.attributes.get(name) ?? null;
  }
}

function installFakeDOM(t) {
  const originalDocument = globalThis.document;
  globalThis.document = { createElement: (tagName) => new FakeElement(tagName) };
  t.after(() => { globalThis.document = originalDocument; });
  return new FakeElement("main");
}

function descendants(root) {
  return [root, ...root.children.flatMap(descendants)];
}

function findText(root, text) {
  return descendants(root).find((element) => element.textContent.includes(text));
}
