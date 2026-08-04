import assert from "node:assert/strict";
import test from "node:test";

import { renderSystemPage } from "./system.js";

test("System page refreshes the catalog manually and renders potential strategy impact", async (t) => {
  const root = installFakeDOM(t);
  const oldCatalog = catalog(true, 3000000, "a");
  const newCatalog = catalog(false, 2000000, "d");
  const strategyCalls = [];
  await renderSystemPage(root, {
    loadSystem: async () => systemFixture(),
    loadProfiles: async () => ({ default_profile_id: 7, profiles: [profileFixture()] }),
    loadModelCatalog: async () => oldCatalog,
    refreshModelCatalog: async () => ({
      catalog: newCatalog,
      previous_source: oldCatalog.source,
      changed: true,
    }),
    loadStrategies: async (profileID) => {
      strategyCalls.push(profileID);
      return {
        strategies: [{
          id: 9,
          state: "draft",
          config: {
            name: "20260802-001",
            routes: [{ id: "balanced", candidates: [{ model: "strong" }] }],
          },
        }],
      };
    },
    changePassword: async () => {},
  });

  assert.ok(findText(root, "模型信息库"));
  assert.ok(findText(root, "1 个模型"));
  await buttonByText(root, "从 Models.dev 更新").dispatch("click");

  assert.deepEqual(strategyCalls, [7]);
  assert.ok(findText(root, "潜在影响报告"));
  assert.ok(findText(root, "高风险"));
  assert.ok(findText(root, "Coding"));
  assert.equal(
    links(root).some((link) => link.getAttribute("href") === "/_admin/profiles/7/vision"),
    true,
  );
  assert.ok(findTextContaining(root, "20260802-001"));
});

test("failed catalog refresh keeps the displayed active revision", async (t) => {
  const root = installFakeDOM(t);
  const oldCatalog = catalog(true, 3000000, "a");
  await renderSystemPage(root, {
    loadSystem: async () => systemFixture(),
    loadProfiles: async () => ({ profiles: [] }),
    loadModelCatalog: async () => oldCatalog,
    refreshModelCatalog: async () => {
      throw new Error("远程更新失败");
    },
    loadStrategies: async () => ({ strategies: [] }),
    changePassword: async () => {},
  });

  await buttonByText(root, "从 Models.dev 更新").dispatch("click");

  assert.ok(findText(root, "远程更新失败"));
  assert.ok(findText(root, `Revision：${"a".repeat(12)}`));
});

test("impact analysis failure does not roll back an activated catalog", async (t) => {
  const root = installFakeDOM(t);
  const oldCatalog = catalog(true, 3000000, "a");
  const newCatalog = catalog(false, 2000000, "d");
  await renderSystemPage(root, {
    loadSystem: async () => systemFixture(),
    loadProfiles: async () => ({ profiles: [profileFixture()] }),
    loadModelCatalog: async () => oldCatalog,
    refreshModelCatalog: async () => ({
      catalog: newCatalog,
      previous_source: oldCatalog.source,
      changed: true,
    }),
    loadStrategies: async () => {
      throw new Error("策略读取失败");
    },
    changePassword: async () => {},
  });

  await buttonByText(root, "从 Models.dev 更新").dispatch("click");

  assert.ok(findText(root, `Revision：${"d".repeat(12)}`));
  assert.ok(findTextContaining(root, "模型信息库已更新"));
  assert.ok(findTextContaining(root, "影响分析失败"));
});

function systemFixture() {
  return {
    version: "test",
    data_dir: "/data",
    database_file: "llm-proxy.db",
    database_bytes: 10,
    schema_version: 1,
    default_profile_id: 7,
    password_must_change: false,
  };
}

function catalog(supportsVision, price, digestSeed) {
  return {
    source: {
      name: "Models.dev",
      revision: digestSeed.repeat(64),
      modelsSha256: "b".repeat(64),
      providersSha256: "c".repeat(64),
      retrieved: "2026-08-02",
    },
    models: [{
      id: "strong",
      canonicalId: "acme/strong",
      apiIds: [],
      aliases: ["acme/strong"],
      compatibilityAliases: [],
      provider: "Acme",
      name: "Strong",
      family: "strong",
      context_window: 200000,
      max_output_tokens: 16000,
      supports_vision: supportsVision,
      supports_tools: true,
      input_price_micro_usd_per_million: price,
      output_price_micro_usd_per_million: 15000000,
      lifecycle: "stable",
      references: [],
    }],
  };
}

function profileFixture() {
  return {
    id: 7,
    slug: "coding",
    display_name: "Coding",
    config: {
      models: [{ id: "strong" }],
      vision: { enabled: true, model: "strong" },
      auto_routing: {
        enabled: true,
        participants: ["strong"],
        strong_baseline_model: "strong",
        task_analyzer_model: "strong",
        strategy: { routes: [] },
      },
      targets: [],
    },
  };
}

class FakeElement {
  constructor(tagName) {
    this.tagName = tagName.toUpperCase();
    this.children = [];
    this.attributes = new Map();
    this.listeners = new Map();
    this.textContent = "";
    this.className = "";
    this.value = "";
    this.type = "";
    this.hidden = false;
    this.disabled = false;
  }
  append(...children) { this.children.push(...children); }
  replaceChildren(...children) { this.children = [...children]; }
  setAttribute(name, value) { this.attributes.set(name, String(value)); }
  getAttribute(name) { return this.attributes.get(name) ?? null; }
  addEventListener(name, listener) {
    const listeners = this.listeners.get(name) || [];
    listeners.push(listener);
    this.listeners.set(name, listeners);
  }
  async dispatch(name) {
    for (const listener of this.listeners.get(name) || []) {
      await listener({ preventDefault() {} });
    }
  }
  click() {}
}

function installFakeDOM(t) {
  const originalDocument = globalThis.document;
  const originalURL = globalThis.URL;
  globalThis.document = { createElement: (name) => new FakeElement(name) };
  globalThis.URL = {
    createObjectURL: () => "blob:test",
    revokeObjectURL: () => {},
  };
  t.after(() => {
    globalThis.document = originalDocument;
    globalThis.URL = originalURL;
  });
  return new FakeElement("main");
}

function descendants(root) {
  return [root, ...root.children.flatMap(descendants)];
}

function findText(root, text) {
  return descendants(root).find((element) => element.textContent === text);
}

function findTextContaining(root, text) {
  return descendants(root).find((element) => element.textContent.includes(text));
}

function buttonByText(root, text) {
  const button = descendants(root).find(
    (element) => element.tagName === "BUTTON" && element.textContent === text,
  );
  assert.ok(button, `button ${text} not found`);
  return button;
}

function links(root) {
  return descendants(root).filter((element) => element.tagName === "A");
}
