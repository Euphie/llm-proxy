import assert from "node:assert/strict";
import test from "node:test";

import { renderHelpPage } from "./routing-guide.js";

test("built-in routing guide explains fields selection and budgets without external links", (t) => {
  const root = installFakeDOM(t);
  renderHelpPage(root, "intelligent-routing");

  const navigation = descendants(root).find(
    (element) => element.getAttribute("aria-label") === "帮助主题",
  );
  assert.ok(navigation);
  assert.deepEqual(navigation.children.map((link) => link.textContent), [
    "使用概览",
    "名词解释",
    "Profiles 与模型",
    "Agent 配置",
    "视觉增强",
    "智能路由",
    "容错与上游节点",
    "统计与诊断",
  ]);
  assert.equal(
    navigation.children.find((link) => link.textContent === "智能路由")
      .getAttribute("aria-current"),
    "page",
  );
  assert.ok(findText(root, "智能路由配置说明"));
  assert.ok(findText(root, "先过门槛，再比成本"));
  assert.ok(findText(root, "首次请求与后续追问"));
  assert.ok(findText(root, "跳过轻量任务分析"));
  assert.ok(findText(root, "贝叶斯算法在哪生效"));
  assert.ok(findText(root, "20 个等效样本"));
	assert.ok(findText(root, "正确性、完整性、指令遵循"));
	assert.ok(findText(root, "历史里的 shell_exec"));
  assert.equal(
    descendants(root).find((element) => element.textContent === "先看名词解释")
      .getAttribute("href"),
    "/_admin/help/glossary",
  );
  const images = descendants(root).filter((element) => element.tagName === "IMG");
  assert.deepEqual(images.map((image) => image.src), [
    "/_admin/assets/current/intelligent-routing-architecture.svg",
    "/_admin/assets/current/intelligent-routing-flow.svg",
    "/_admin/assets/current/intelligent-routing-learning-flow.svg",
  ]);
  assert.ok(images.every((image) => image.alt && image.loading === "lazy"));
  assert.ok(findText(root, "策略与 Route 字段"));
  assert.ok(findText(root, "当前质量估计"));
  assert.ok(findText(root, "质量不足，排除"));
  assert.ok(findText(root, "统一尝试预算"));
  assert.ok(findText(root, "不会假设缓存一定命中"));
  for (const link of descendants(root).filter((item) => item.tagName === "A")) {
    assert.match(link.getAttribute("href"), /^\/_admin\//);
  }
});

test("Help overview and every secondary topic render useful built-in content", (t) => {
  const root = installFakeDOM(t);
  for (const [topic, expected] of [
    ["overview", "推荐配置顺序"],
    ["glossary", "同一会话优先继续使用已经成功的模型"],
    ["profiles-models", "模型目录可以为空"],
    ["agents", "不会把输入的临时密钥保存"],
    ["vision", "未开启或模型为空时不会发送影子识图请求"],
    ["reliability", "第一条规则生效"],
    ["statistics", "实际费用才是完整已知"],
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
