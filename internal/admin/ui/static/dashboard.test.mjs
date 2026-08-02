import assert from "node:assert/strict";
import test from "node:test";

import { renderDashboard } from "./dashboard.js";

test("empty overview gives a single clear start action", async (t) => {
  const root = installFakeDOM(t);
  await renderDashboard(root, {
    session: { username: "admin", initialization_state: "profile_setup_required" },
    profiles: { default_profile_id: 0, profiles: [] },
    loadProfileStats: async () => {
      throw new Error("stats must not load without a Profile");
    },
  });

  assert.ok(findText(root, "欢迎使用 llm-proxy"));
  const start = linksByText(root, "开始配置");
  assert.equal(start.length, 1);
  assert.equal(start[0].getAttribute("href"), "/_admin/setup");
});

test("configured overview shows exact proxy path and next verification action", async (t) => {
  const root = installFakeDOM(t);
  const loaded = [];
  await renderDashboard(root, {
    session: { username: "admin", initialization_state: "ready" },
    profiles: {
      default_profile_id: 7,
      profiles: [profileFixture()],
    },
    loadProfileStats: async (profileId) => {
      loaded.push(profileId);
      return { summary: { requests: 0 } };
    },
  });

  assert.deepEqual(loaded, [7]);
  assert.ok(findText(root, "/coding/v1/messages"));
  assert.ok(findText(root, "等待首个请求"));
  assert.equal(
    linksByText(root, "生成 Agent 配置")[0].getAttribute("href"),
    "/_admin/profiles/7/agents",
  );
});

test("completed overview removes the large onboarding card", async (t) => {
  const root = installFakeDOM(t);
  await renderDashboard(root, {
    session: { username: "admin", initialization_state: "ready" },
    profiles: { default_profile_id: 7, profiles: [profileFixture()] },
    loadProfileStats: async () => ({ summary: { requests: 12 } }),
  });

  assert.equal(elementsByClass(root, "onboarding-card").length, 0);
  assert.ok(findText(root, "运行正常"));
  assert.ok(findText(root, "12 次请求"));
});

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
      models: [],
      auto_routing: { enabled: false },
      vision: { enabled: false, model: "sonnet" },
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
    this.listeners = new Map();
    this.textContent = "";
    this.className = "";
    this.hidden = false;
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

  setAttribute(name, value) {
    this.attributes.set(name, String(value));
  }

  getAttribute(name) {
    return this.attributes.get(name) ?? null;
  }

  addEventListener(name, listener) {
    this.listeners.set(name, listener);
  }
}

function installFakeDOM(t) {
  const originalDocument = globalThis.document;
  globalThis.document = {
    createElement: (name) => new FakeElement(name),
  };
  t.after(() => {
    globalThis.document = originalDocument;
  });
  return new FakeElement("main");
}

function descendants(root) {
  return [root, ...root.children.flatMap(descendants)];
}

function findText(root, text) {
  return descendants(root).find((element) => element.textContent === text);
}

function linksByText(root, text) {
  return descendants(root).filter(
    (element) => element.tagName === "A" && element.textContent === text,
  );
}

function elementsByClass(root, className) {
  return descendants(root).filter((element) =>
    String(element.className).split(/\s+/).includes(className),
  );
}
