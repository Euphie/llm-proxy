import assert from "node:assert/strict";
import test from "node:test";

import {
  claudeSettings,
  downloadText,
  openConfigurationGenerator,
  openAIBaseURL,
  openAIEnvironment,
  profileBaseURL,
  shellSnippet,
} from "./generator.js";

test("Profile base URL normalizes the public origin and appends the slug once", () => {
  assert.equal(
    profileBaseURL("https://proxy.example.com/", "coding"),
    "https://proxy.example.com/coding",
  );
  assert.equal(
    profileBaseURL("https://proxy.example.com/coding?ignored=yes", "coding"),
    "https://proxy.example.com/coding",
  );
});

test("Profile base URL rejects credentials", () => {
  assert.throws(
    () => profileBaseURL("https://admin:secret@proxy.example.com", "coding"),
    /credentials/i,
  );
});

test("OpenAI API base includes v1 below the Profile prefix", () => {
  assert.equal(
    openAIBaseURL("https://proxy.example.com/", "openai"),
    "https://proxy.example.com/openai/v1",
  );
});

test("Claude settings include only non-blank aliases and a selected placeholder", () => {
  const output = claudeSettings({
    baseURL: "https://proxy.example.com/coding",
    models: { sonnet: " Kimi-K2.5 ", haiku: " ", opus: "GLM-5" },
    authVariable: "ANTHROPIC_AUTH_TOKEN",
  });

  assert.deepEqual(output, {
    $schema: "https://json.schemastore.org/claude-code-settings.json",
    env: {
      ANTHROPIC_BASE_URL: "https://proxy.example.com/coding",
      ANTHROPIC_DEFAULT_SONNET_MODEL: "Kimi-K2.5",
      ANTHROPIC_DEFAULT_OPUS_MODEL: "GLM-5",
      ANTHROPIC_AUTH_TOKEN: "<SET_LOCALLY>",
    },
  });
});

test("choosing no authentication omits every secret placeholder", () => {
  assert.deepEqual(
    claudeSettings({
      baseURL: "https://proxy.example.com/coding",
      models: {},
      authVariable: "不生成",
    }).env,
    { ANTHROPIC_BASE_URL: "https://proxy.example.com/coding" },
  );
});

test("OpenAI environment uses the v1 base without inventing a credential", () => {
  assert.deepEqual(
    openAIEnvironment({
      baseURL: "https://proxy.example.com/openai/v1",
      authVariable: "不生成",
    }),
    { OPENAI_BASE_URL: "https://proxy.example.com/openai/v1" },
  );
});

test("Shell output safely quotes single quotes", () => {
  assert.equal(
    shellSnippet({ ANTHROPIC_BASE_URL: "https://example.test/a'b" }),
    `export ANTHROPIC_BASE_URL='https://example.test/a'"'"'b'`,
  );
});

test("downloadText creates, clicks, and revokes a browser object URL", (t) => {
  const originalDocument = globalThis.document;
  const originalURL = globalThis.URL;
  const originalBlob = globalThis.Blob;
  const calls = [];
  const anchor = {
    href: "",
    download: "",
    click() {
      calls.push(["click", this.href, this.download]);
    },
  };

  globalThis.document = {
    createElement(name) {
      assert.equal(name, "a");
      return anchor;
    },
  };
  globalThis.Blob = class FakeBlob {
    constructor(parts, options) {
      this.parts = parts;
      this.options = options;
    }
  };
  globalThis.URL = {
    createObjectURL(blob) {
      calls.push(["create", blob.parts, blob.options]);
      return "blob:generator-test";
    },
    revokeObjectURL(url) {
      calls.push(["revoke", url]);
    },
  };
  t.after(() => {
    globalThis.document = originalDocument;
    globalThis.URL = originalURL;
    globalThis.Blob = originalBlob;
  });

  downloadText("settings.json", "{}", "application/json");

  assert.deepEqual(calls, [
    ["create", ["{}"], { type: "application/json" }],
    ["click", "blob:generator-test", "settings.json"],
    ["revoke", "blob:generator-test"],
  ]);
});

test("Anthropic generator exposes exact scopes and temporary placeholder inputs", async (t) => {
  const root = installFakeDOM(t);
  const listMarker = new FakeElement("p");
  listMarker.textContent = "Profiles remain mounted";
  root.append(listMarker);

  const dialog = openConfigurationGenerator(
    root,
    {
      slug: "coding",
      display_name: "Coding",
      config: { protocol: "anthropic" },
    },
    { origin: "https://proxy.example.com/_admin/profiles" },
  );

  assert.equal(dialog.open, true);
  assert.equal(root.children[0], listMarker);
  for (const scope of [
    "全局 ~/.claude/settings.json",
    "项目共享 .claude/settings.json",
    "项目私有 .claude/settings.local.json",
    "Shell",
  ]) {
    assert.ok(buttonByText(dialog, scope));
  }
  for (const name of [
    "generator-public-origin",
    "generator-sonnet",
    "generator-haiku",
    "generator-opus",
    "generator-auth-variable",
  ]) {
    assert.ok(controlByName(dialog, name));
  }
  assert.deepEqual(
    controlByName(dialog, "generator-auth-variable").children.map(
      (option) => option.textContent,
    ),
    ["不生成", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"],
  );
  assert.equal(
    controls(dialog).some((control) => control.type === "password"),
    false,
  );

  const sonnet = controlByName(dialog, "generator-sonnet");
  sonnet.value = "kimi-k2.5";
  await sonnet.dispatch("input");
  const auth = controlByName(dialog, "generator-auth-variable");
  auth.value = "ANTHROPIC_AUTH_TOKEN";
  await auth.dispatch("change");
  assert.match(findClass(dialog, "generator-output").textContent, /kimi-k2\.5/);
  assert.match(
    findClass(dialog, "generator-output").textContent,
    /"<SET_LOCALLY>"/,
  );

  await buttonByText(dialog, "Shell").dispatch("click");
  assert.match(
    findClass(dialog, "generator-output").textContent,
    /export ANTHROPIC_BASE_URL='https:\/\/proxy\.example\.com\/coding'/,
  );
  assert.equal(buttonsByText(dialog, "复制").length, 1);
  assert.equal(buttonsByText(dialog, "下载").length, 1);

  await buttonByText(dialog, "返回 Profiles").dispatch("click");
  assert.equal(dialog.open, false);
  assert.deepEqual(root.children, [listMarker]);
});

test("OpenAI generator shows prefix, v1 base, and generic Shell outputs", (t) => {
  const root = installFakeDOM(t);
  const dialog = openConfigurationGenerator(
    root,
    {
      slug: "openai",
      display_name: "OpenAI",
      config: { protocol: "openai" },
    },
    { origin: "https://proxy.example.com/" },
  );

  assert.ok(findText(dialog, "Profile 前缀"));
  assert.ok(findText(dialog, "https://proxy.example.com/openai"));
  assert.ok(findText(dialog, "OpenAI API base"));
  assert.ok(findText(dialog, "https://proxy.example.com/openai/v1"));
  assert.match(
    findClass(dialog, "generator-output").textContent,
    /OPENAI_BASE_URL/,
  );
  assert.equal(
    descendants(dialog).some((element) =>
      element.textContent.includes("Claude Code"),
    ),
    false,
  );
  assert.equal(buttonsByText(dialog, "复制").length, 3);
  assert.equal(buttonsByText(dialog, "下载").length, 3);
});

class FakeElement {
  constructor(tagName) {
    this.tagName = tagName.toUpperCase();
    this.children = [];
    this.parentNode = null;
    this.attributes = new Map();
    this.listeners = new Map();
    this.textContent = "";
    this.className = "";
    this.name = "";
    this.type = "";
    this.value = "";
    this.hidden = false;
    this.open = false;
  }

  set innerHTML(_) {
    throw new Error("innerHTML is forbidden");
  }

  append(...children) {
    for (const child of children) {
      child.parentNode = this;
      this.children.push(child);
    }
  }

  replaceChildren(...children) {
    for (const child of this.children) {
      child.parentNode = null;
    }
    this.children = [];
    this.append(...children);
  }

  remove() {
    if (!this.parentNode) {
      return;
    }
    this.parentNode.children = this.parentNode.children.filter(
      (child) => child !== this,
    );
    this.parentNode = null;
  }

  setAttribute(name, value) {
    this.attributes.set(name, String(value));
  }

  getAttribute(name) {
    return this.attributes.get(name) ?? null;
  }

  addEventListener(name, listener) {
    const listeners = this.listeners.get(name) ?? [];
    listeners.push(listener);
    this.listeners.set(name, listeners);
  }

  async dispatch(name) {
    const event = {
      target: this,
      currentTarget: this,
      preventDefault() {},
    };
    for (const listener of this.listeners.get(name) ?? []) {
      await listener(event);
    }
  }

  click() {}

  showModal() {
    this.open = true;
  }

  close() {
    this.open = false;
  }
}

function installFakeDOM(t) {
  const root = new FakeElement("main");
  const originalDocument = globalThis.document;
  const originalLocalStorage = globalThis.localStorage;
  const originalSessionStorage = globalThis.sessionStorage;
  globalThis.document = {
    createElement: (name) => new FakeElement(name),
  };
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    get() {
      throw new Error("localStorage access is forbidden");
    },
  });
  Object.defineProperty(globalThis, "sessionStorage", {
    configurable: true,
    get() {
      throw new Error("sessionStorage access is forbidden");
    },
  });
  t.after(() => {
    globalThis.document = originalDocument;
    restoreGlobal("localStorage", originalLocalStorage);
    restoreGlobal("sessionStorage", originalSessionStorage);
  });
  return root;
}

function restoreGlobal(name, value) {
  if (value === undefined) {
    delete globalThis[name];
    return;
  }
  Object.defineProperty(globalThis, name, {
    configurable: true,
    value,
  });
}

function descendants(root) {
  return [root, ...root.children.flatMap(descendants)];
}

function controls(root) {
  return descendants(root).filter((element) =>
    ["INPUT", "SELECT", "TEXTAREA"].includes(element.tagName),
  );
}

function controlByName(root, name) {
  const control = controls(root).find((element) => element.name === name);
  assert.ok(control, `control ${name} not found`);
  return control;
}

function findText(root, text) {
  const found = descendants(root).find((element) =>
    element.textContent.includes(text),
  );
  assert.ok(found, `text ${text} not found`);
  return found;
}

function findClass(root, className) {
  const found = descendants(root).find((element) =>
    element.className.split(/\s+/).includes(className),
  );
  assert.ok(found, `class ${className} not found`);
  return found;
}

function buttonsByText(root, text) {
  return descendants(root).filter(
    (element) => element.tagName === "BUTTON" && element.textContent === text,
  );
}

function buttonByText(root, text) {
  const button = buttonsByText(root, text)[0];
  assert.ok(button, `button ${text} not found`);
  return button;
}
