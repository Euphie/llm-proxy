import assert from "node:assert/strict";
import test from "node:test";

import {
  createRequestPoller,
  nextOnboardingStep,
  renderOnboarding,
} from "./onboarding.js";

test("onboarding resumes from durable Profile and request facts", () => {
  assert.equal(nextOnboardingStep({ profiles: [], requestCount: 0 }), "connection");
  assert.equal(
    nextOnboardingStep({ profiles: [profileFixture()], requestCount: 0 }),
    "agents",
  );
  assert.equal(
    nextOnboardingStep({ profiles: [profileFixture()], requestCount: 1 }),
    "complete",
  );
});

test("onboarding renders four ordered steps and a secret-free connection form", async (t) => {
  const root = installFakeDOM(t);
  await renderOnboarding(root, {
    profiles: { default_profile_id: 0, profiles: [] },
    client: {},
  });

  const steps = findTag(root, "OL");
  assert.equal(steps.children.length, 4);
  assert.deepEqual(steps.children.map((item) => item.textContent), [
    "连接上游",
    "模型（选填）",
    "Agent 配置",
    "验证请求",
  ]);
  assert.equal(
    descendants(root).some(
      (element) => element.tagName === "INPUT" && element.type === "password",
    ),
    false,
  );
});

test("saved Profile resumes at protocol-filtered Agent choices", async (t) => {
  const root = installFakeDOM(t);
  const statsCalls = [];
  await renderOnboarding(root, {
    profiles: { default_profile_id: 7, profiles: [profileFixture()] },
    client: {
      stats: async (filters) => {
        statsCalls.push(filters);
        return { summary: { requests: 0 } };
      },
    },
  });

  assert.deepEqual(statsCalls, [{ profile_id: 7 }]);
  assert.deepEqual(
    controlByName(root, "setup-agent").children.map((option) => option.value),
    ["claude-code", "opencode-anthropic", "generic"],
  );
  assert.ok(buttonByText(root, "生成配置"));
  assert.ok(buttonByText(root, "配置完成，开始验证"));
});

test("request poller pauses while hidden and stops after detection", async () => {
  const scheduled = [];
  const cleared = [];
  const documentRef = visibilityDocument();
  let loads = 0;
  let detected = 0;
  const poller = createRequestPoller({
    documentRef,
    interval: 25,
    setTimeoutRef: (callback, delay) => {
      scheduled.push({ callback, delay });
      return scheduled.length;
    },
    clearTimeoutRef: (id) => cleared.push(id),
    load: async () => {
      loads += 1;
      return { summary: { requests: loads > 1 ? 1 : 0 } };
    },
    onDetected: () => {
      detected += 1;
    },
  });

  documentRef.hidden = true;
  await poller.start();
  assert.equal(loads, 0);
  assert.equal(scheduled.length, 0);

  documentRef.hidden = false;
  await documentRef.dispatch("visibilitychange");
  assert.equal(loads, 1);
  assert.equal(scheduled[0].delay, 25);
  await scheduled[0].callback();
  assert.equal(loads, 2);
  assert.equal(detected, 1);
  assert.equal(scheduled.length, 1);

  poller.stop();
  assert.ok(cleared.length >= 1);
});

test("request poller resumes when the page becomes hidden during a scheduled check", async () => {
  const scheduled = [];
  const documentRef = visibilityDocument();
  let loads = 0;
  const poller = createRequestPoller({
    documentRef,
    interval: 25,
    setTimeoutRef: (callback) => {
      scheduled.push(callback);
      return scheduled.length;
    },
    clearTimeoutRef: () => {},
    load: async () => {
      loads += 1;
      if (loads === 2) documentRef.hidden = true;
      return { summary: { requests: 0 } };
    },
    onDetected: () => {},
  });

  await poller.start();
  await scheduled[0]();
  documentRef.hidden = false;
  await documentRef.dispatch("visibilitychange");
  assert.equal(loads, 3);
  poller.stop();
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
    this.value = "";
    this.name = "";
    this.type = "";
    this.checked = false;
    this.disabled = false;
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
    const listeners = this.listeners.get(name) || [];
    listeners.push(listener);
    this.listeners.set(name, listeners);
  }

  async dispatch(name) {
    for (const listener of this.listeners.get(name) || []) {
      await listener({ preventDefault() {} });
    }
  }
}

function installFakeDOM(t) {
  const originalDocument = globalThis.document;
  globalThis.document = {
    hidden: false,
    createElement: (name) => new FakeElement(name),
  };
  t.after(() => {
    globalThis.document = originalDocument;
  });
  return new FakeElement("main");
}

function visibilityDocument() {
  const listeners = new Map();
  return {
    hidden: false,
    addEventListener(name, listener) {
      listeners.set(name, listener);
    },
    removeEventListener(name) {
      listeners.delete(name);
    },
    async dispatch(name) {
      await listeners.get(name)?.();
    },
  };
}

function descendants(root) {
  return [root, ...root.children.flatMap(descendants)];
}

function findTag(root, tagName) {
  const value = descendants(root).find(
    (element) => element.tagName === tagName.toUpperCase(),
  );
  assert.ok(value, `${tagName} not found`);
  return value;
}

function controlByName(root, name) {
  const value = descendants(root).find((element) => element.name === name);
  assert.ok(value, `${name} not found`);
  return value;
}

function buttonByText(root, text) {
  const value = descendants(root).find(
    (element) => element.tagName === "BUTTON" && element.textContent === text,
  );
  assert.ok(value, `${text} not found`);
  return value;
}
