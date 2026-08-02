import assert from "node:assert/strict";
import test from "node:test";

import { renderProfileEditor } from "./profile-editor.js";

test("Profile shell exposes seven named sections and only mounts the selected content", (t) => {
  const root = installFakeDOM(t);
  renderProfileEditor(root, profileFixture(), {
    section: "models",
    defaultProfileID: 7,
    renderLegacy: legacySectionFixture,
  });

  const navigation = descendants(root).find(
    (element) => element.getAttribute("aria-label") === "Profile 设置",
  );
  assert.ok(navigation);
  assert.deepEqual(
    navigation.children.map((link) => link.textContent),
    ["概况", "连接", "模型", "智能路由", "视觉增强", "容错", "Agent 配置"],
  );
  assert.equal(linkByText(root, "模型").getAttribute("aria-current"), "page");
  assert.equal(linkByText(root, "模型").getAttribute("href"), "/_admin/profiles/7/models");
  assert.ok(findText(root, "模型能力"));
  assert.equal(findText(root, "基础配置"), undefined);
  assert.equal(findText(root, "智能路由"), linkByText(root, "智能路由"));
});

test("Profile shell shows written dependency reasons and direct actions", (t) => {
  const root = installFakeDOM(t);
  const profile = profileFixture();
  profile.config.models = [];
  renderProfileEditor(root, profile, {
    section: "routing",
    defaultProfileID: 7,
    renderLegacy: legacySectionFixture,
  });

  assert.ok(findTextIncludes(root, "先录入模型"));
  const action = linkByText(root, "录入模型");
  assert.equal(action.getAttribute("href"), "/_admin/profiles/7/models");
});

test("Profile overview summarizes capabilities without mounting the legacy form", (t) => {
  const root = installFakeDOM(t);
  let legacyCalls = 0;
  renderProfileEditor(root, profileFixture(), {
    section: "overview",
    defaultProfileID: 7,
    renderLegacy: () => {
      legacyCalls += 1;
    },
  });

  assert.equal(legacyCalls, 0);
  assert.ok(findText(root, "/coding/v1/messages"));
  assert.ok(findText(root, "Anthropic"));
  assert.ok(linkByText(root, "编辑连接"));
});

test("reliability keeps ordinary retries editable while Auto Target failover is unavailable", (t) => {
  const root = installFakeDOM(t);
  renderProfileEditor(root, profileFixture(), {
    section: "reliability",
    defaultProfileID: 7,
    renderLegacy: legacySectionFixture,
  });

  assert.ok(findText(root, "容错规则"));
  assert.equal(findText(root, "Target 容错"), undefined);
  assert.ok(findTextIncludes(root, "备用 Target"));
  assert.equal(linkByText(root, "配置智能路由").getAttribute("href"), "/_admin/profiles/7/routing");
});

function legacySectionFixture(root) {
  const form = document.createElement("form");
  for (const heading of [
    "基础配置",
    "Target 容错",
    "模型能力",
    "智能路由",
    "视觉增强",
    "容错规则",
    "配置生成",
  ]) {
    const section = document.createElement("section");
    const title = document.createElement("h2");
    title.textContent = heading;
    section.append(title);
    form.append(section);
  }
  const alert = document.createElement("div");
  alert.setAttribute("role", "alert");
  const footer = document.createElement("div");
  footer.className = "editor-actions";
  form.append(alert, footer);
  root.replaceChildren(form);
}

function profileFixture() {
  return {
    id: 7,
    slug: "coding",
    display_name: "Coding",
    enabled: true,
    config: {
      version: 1,
      protocol: "anthropic",
      upstream: "https://upstream.example",
      models: [{
        id: "strong",
        context_window: 128000,
        max_output_tokens: 16000,
        supports_vision: true,
        input_price_micro_usd_per_million: 1000000,
        output_price_micro_usd_per_million: 4000000,
      }],
      auto_routing: { enabled: false },
      vision: { enabled: false, model: "strong" },
      targets: [],
      overload_rules: [],
    },
  };
}

class FakeElement {
  constructor(tagName) {
    this.tagName = tagName.toUpperCase();
    this.children = [];
    this.parentNode = null;
    this.attributes = new Map();
    this.textContent = "";
    this.className = "";
  }

  append(...children) {
    for (const child of children) {
      child.parentNode = this;
      this.children.push(child);
    }
  }

  replaceChildren(...children) {
    this.children = [];
    this.append(...children);
  }

  remove() {
    if (!this.parentNode) return;
    this.parentNode.children = this.parentNode.children.filter((child) => child !== this);
    this.parentNode = null;
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
  globalThis.document = { createElement: (name) => new FakeElement(name) };
  t.after(() => { globalThis.document = originalDocument; });
  return new FakeElement("main");
}

function descendants(root) {
  return [root, ...root.children.flatMap(descendants)];
}

function findText(root, text) {
  return descendants(root).find((element) => element.textContent === text);
}

function findTextIncludes(root, text) {
  return descendants(root).find((element) => element.textContent.includes(text));
}

function linkByText(root, text) {
  const link = descendants(root).find(
    (element) => element.tagName === "A" && element.textContent === text,
  );
  assert.ok(link, `${text} link not found`);
  return link;
}
