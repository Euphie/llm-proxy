import assert from "node:assert/strict";
import test from "node:test";

import { api } from "./api.js";
import { bootstrap } from "./app.js";
import {
  addRetryRule,
  defaultProfileDraft,
  moveRetryRule,
  profileDraft,
  profilePayload,
  removeRetryRule,
  renderProfileEditor,
  renderProfileList,
} from "./profiles.js";

test("new Anthropic Profile contains explicit defaults", () => {
  const draft = defaultProfileDraft("anthropic");
  assert.equal(draft.config.version, 1);
  assert.equal(draft.config.protocol, "anthropic");
  assert.equal(draft.config.vision.model, "sonnet");
  assert.equal(draft.config.vision.timeout, "2m");
  assert.deepEqual(draft.config.overload_rules, []);
});

test("OpenAI draft disables vision", () => {
  const draft = defaultProfileDraft("openai");
  draft.config.vision.enabled = true;
  const payload = profilePayload({
    ...draft,
    slug: "openai",
    display_name: "OpenAI",
    upstream: "https://example.test",
  });
  assert.equal(payload.config.protocol, "openai");
  assert.equal(payload.config.upstream, "https://example.test");
  assert.equal(payload.config.vision.enabled, false);
});

test("payload preserves every configured field with numeric JSON types and retry order", () => {
  const draft = defaultProfileDraft("anthropic");
  draft.slug = "coding";
  draft.display_name = "Coding";
  draft.enabled = false;
  draft.make_default = true;
  draft.config.version = "1";
  draft.config.upstream = "https://upstream.example";
  draft.config.vision = {
    enabled: true,
    model: "vision-model",
    max_tokens: "4096",
    timeout: "75s",
    max_concurrency: "6",
    cache_ttl: "45m",
    cache_max_entries: "123",
    prompt: "Describe exactly.",
  };
  draft.config.overload_rules = [
    {
      status: "529",
      body_contains: "first",
      max_retries: "5",
      delay: "2s",
      jitter: "250ms",
    },
    {
      status: "429",
      body_contains: "second",
      max_retries: "3",
      delay: "1s",
      jitter: "100ms",
    },
  ];

  assert.deepEqual(profilePayload(draft), {
    slug: "coding",
    display_name: "Coding",
    enabled: false,
    make_default: true,
    config: {
      version: 1,
      protocol: "anthropic",
      upstream: "https://upstream.example",
      vision: {
        enabled: true,
        model: "vision-model",
        max_tokens: 4096,
        timeout: "75s",
        max_concurrency: 6,
        cache_ttl: "45m",
        cache_max_entries: 123,
        prompt: "Describe exactly.",
      },
      overload_rules: [
        {
          status: 529,
          body_contains: "first",
          max_retries: 5,
          delay: "2s",
          jitter: "250ms",
        },
        {
          status: 429,
          body_contains: "second",
          max_retries: 3,
          delay: "1s",
          jitter: "100ms",
        },
      ],
    },
  });
});

test("retry helpers add remove and move without implicit sorting", () => {
  const first = {
    status: 503,
    body_contains: "first",
    max_retries: 1,
    delay: "1s",
    jitter: "0s",
  };
  const second = {
    status: 429,
    body_contains: "second",
    max_retries: 2,
    delay: "2s",
    jitter: "0s",
  };

  const added = addRetryRule([first, second]);
  assert.deepEqual(added.slice(0, 2), [first, second]);
  assert.equal(added.length, 3);

  assert.deepEqual(removeRetryRule([first, second], 0), [second]);
  assert.deepEqual(moveRetryRule([first, second], 1, -1), [second, first]);
  assert.deepEqual(moveRetryRule([first, second], 0, 1), [second, first]);
  assert.deepEqual(moveRetryRule([first, second], 0, -1), [first, second]);
});

test("Profile API methods use exact routes, methods, JSON, and CSRF", async (t) => {
  const calls = [];
  const originalDocument = globalThis.document;
  const originalFetch = globalThis.fetch;
  globalThis.document = { cookie: "llm_proxy_csrf=csrf-token" };
  globalThis.fetch = async (requestPath, options) => {
    calls.push({
      path: requestPath,
      method: options.method,
      body: options.body === undefined ? undefined : JSON.parse(options.body),
      csrf: options.headers.get("X-CSRF-Token"),
    });
    return new Response("{}", {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  };
  t.after(() => {
    globalThis.document = originalDocument;
    globalThis.fetch = originalFetch;
  });

  const profile = { slug: "coding" };
  const copy = { slug: "coding-copy", display_name: "Coding Copy" };
  await api.listProfiles();
  await api.getProfile(7);
  await api.createProfile(profile);
  await api.updateProfile(7, profile);
  await api.copyProfile(7, copy);
  await api.deleteProfile(7);
  await api.deleteProfile(7, 12);
  await api.setDefaultProfile(12);

  assert.deepEqual(calls, [
    {
      path: "/_admin/api/profiles",
      method: "GET",
      body: undefined,
      csrf: null,
    },
    {
      path: "/_admin/api/profiles/7",
      method: "GET",
      body: undefined,
      csrf: null,
    },
    {
      path: "/_admin/api/profiles",
      method: "POST",
      body: profile,
      csrf: "csrf-token",
    },
    {
      path: "/_admin/api/profiles/7",
      method: "PUT",
      body: profile,
      csrf: "csrf-token",
    },
    {
      path: "/_admin/api/profiles/7/copy",
      method: "POST",
      body: copy,
      csrf: "csrf-token",
    },
    {
      path: "/_admin/api/profiles/7",
      method: "DELETE",
      body: {},
      csrf: "csrf-token",
    },
    {
      path: "/_admin/api/profiles/7?replacement_default_id=12",
      method: "DELETE",
      body: {},
      csrf: "csrf-token",
    },
    {
      path: "/_admin/api/default-profile",
      method: "PUT",
      body: { profile_id: 12 },
      csrf: "csrf-token",
    },
  ]);
});

test("empty Profile list exposes only the exact first-create action", async (t) => {
  const root = installFakeDOM(t);
  let creates = 0;

  renderProfileList(
    root,
    { default_profile_id: 0, profiles: [] },
    {
      create: () => {
        creates += 1;
      },
    },
  );

  assert.ok(findText(root, "代理请求仍不可用"));
  assert.deepEqual(buttonTexts(root), ["创建第一个 Profile"]);
  const create = buttonByText(root, "创建第一个 Profile");
  assert.ok(create.className.includes("button"));
  await create.dispatch("click");
  assert.equal(creates, 1);
});

test("Profile list shows identity badges usage and usable action hooks without interpreting names as markup", async (t) => {
  const root = installFakeDOM(t);
  const data = profileListFixture();
  const calls = [];

  renderProfileList(root, data, {
    edit: (profile) => calls.push(["edit", profile.id]),
    generate: (profile) => calls.push(["generate", profile.id]),
    copy: async (profile, body) =>
      calls.push(["copy", profile.id, body]),
    setDefault: async (profile) => calls.push(["default", profile.id]),
    toggle: async (profile) => calls.push(["toggle", profile.id]),
    delete: async (profile, replacement) =>
      calls.push(["delete", profile.id, replacement]),
  });

  assert.ok(findText(root, "Primary"));
  assert.ok(findText(root, "primary"));
  assert.ok(findText(root, "默认"));
  assert.ok(findText(root, "anthropic"));
  assert.ok(findText(root, "https://primary.example"));
  assert.ok(findText(root, "近 30 天请求：7"));
  assert.ok(findText(root, "Token：150"));
  assert.ok(findText(root, "已停用"));
  assert.ok(findText(root, "视觉增强"));
  assert.ok(findText(root, "<img src=x onerror=alert(1)>"));
  assert.equal(findAllTags(root, "IMG").length, 0);

  const primary = profileCard(root, "primary");
  const secondary = profileCard(root, "secondary");
  const disabled = profileCard(root, "disabled");
  assert.equal(buttonByText(primary, "设为默认").disabled, true);
  assert.equal(buttonByText(primary, "停用").disabled, true);
  assert.equal(buttonByText(disabled, "设为默认").disabled, true);

  await buttonByText(primary, "编辑").dispatch("click");
  await buttonByText(primary, "生成配置").dispatch("click");
  await buttonByText(secondary, "设为默认").dispatch("click");
  await buttonByText(secondary, "停用").dispatch("click");
  await buttonByText(disabled, "启用").dispatch("click");

  assert.deepEqual(calls, [
    ["edit", 1],
    ["generate", 1],
    ["default", 2],
    ["toggle", 2],
    ["toggle", 3],
  ]);
});

test("Profile generator opens as a dialog and returns to the unchanged list", async (t) => {
  const root = installFakeDOM(t);
  const data = profileListFixture();
  renderProfileList(root, data, {
    edit: () => {},
    generate: () => {},
    copy: async () => {},
    setDefault: async () => {},
    toggle: async () => {},
    delete: async () => {},
  });

  await buttonByText(profileCard(root, "primary"), "生成配置").dispatch(
    "click",
  );
  const dialog = openDialog(root);
  assert.ok(findText(dialog, "全局 ~/.claude/settings.json"));

  await buttonByText(dialog, "返回 Profiles").dispatch("click");
  assert.equal(findAllTags(root, "DIALOG").length, 0);
  assert.ok(profileCard(root, "primary"));
});

test("editor renders exact accessible labels and sections and submits every configured field", async (t) => {
  const root = installFakeDOM(t);
  const data = profileListFixture();
  const draft = profileDraft(data.profiles[0], data.default_profile_id);
  const saves = [];
  const generated = [];

  renderProfileEditor(root, draft, {
    save: async (payload) => saves.push(payload),
    cancel: () => {},
    generate: (current) => generated.push(current.slug),
  });

  assert.deepEqual(sectionHeadings(root), [
    "基础配置",
    "视觉增强",
    "容错规则",
    "配置生成",
  ]);
  for (const exactLabel of [
    "名称",
    "Slug",
    "协议",
    "Upstream",
    "设为默认 Profile",
  ]) {
    assert.ok(labelTexts(root).includes(exactLabel), exactLabel);
  }
  assert.equal(buttonByText(root, "保存 Profile").type, "submit");
  assert.ok(findAllTags(root, "DETAILS").length > 0);
  assert.equal(controlByName(root, "vision_max_tokens").type, "number");
  assert.equal(controlByName(root, "vision_max_concurrency").type, "number");
  assert.equal(controlByName(root, "vision_cache_max_entries").type, "number");
  assert.equal(controlByName(root, "enabled").checked, true);
  assert.equal(controlByName(root, "enabled").disabled, true);
  assert.equal(controlByName(root, "make_default").checked, true);
  assert.equal(controlByName(root, "make_default").disabled, true);
  assert.equal(controlByName(root, "vision_prompt").value, "Original prompt");
  assert.equal(
    controls(root).some(
      (control) =>
        control.type === "password" ||
        /api.?key|authorization|secret/i.test(control.name),
    ),
    false,
  );

  const slug = controlByName(root, "slug");
  slug.value = "primary-renamed";
  await slug.dispatch("input");
  const warning = findText(root, "修改 slug 会改变 Agent 使用的 Profile URL。");
  assert.equal(warning.hidden, false);

  controlByName(root, "vision_max_tokens").value = "8192";
  controlByName(root, "retry-0-status").value = "529";
  await findTag(root, "FORM").dispatch("submit");

  assert.equal(saves.length, 1);
  assert.equal(saves[0].slug, "primary-renamed");
  assert.equal(saves[0].config.vision.max_tokens, 8192);
  assert.equal(saves[0].config.vision.prompt, "Original prompt");
  assert.equal(saves[0].config.overload_rules[0].status, 529);
  assert.equal(typeof saves[0].config.overload_rules[0].max_retries, "number");

  await buttonByText(root, "生成配置").dispatch("click");
  assert.deepEqual(generated, ["primary-renamed"]);
});

test("editor Generate rejection stays inline without replacing the editor", async (t) => {
  const root = installFakeDOM(t);
  const data = profileListFixture();
  const draft = profileDraft(data.profiles[0], data.default_profile_id);
  renderProfileEditor(root, draft, {
    save: async () => {},
    cancel: () => {},
    generate: async () => {
      throw new Error("generator unavailable");
    },
  });
  const editor = findTag(root, "FORM");

  await buttonByText(root, "生成配置").dispatch("click");

  const alert = findClass(root, "error-banner");
  assert.equal(alert.getAttribute("role"), "alert");
  assert.equal(alert.hidden, false);
  assert.equal(alert.textContent, "generator unavailable");
  assert.equal(findTag(root, "FORM"), editor);
  assert.equal(findAllTags(root, "DIALOG").length, 0);
});

test("checking make_default enables and locks a disabled Profile", async (t) => {
  const root = installFakeDOM(t);
  const data = profileListFixture();
  const draft = profileDraft(data.profiles[2], data.default_profile_id);

  renderProfileEditor(root, draft, {
    save: async () => {},
    cancel: () => {},
    generate: () => {},
  });

  const enabled = controlByName(root, "enabled");
  const makeDefault = controlByName(root, "make_default");
  assert.equal(enabled.checked, false);
  assert.equal(enabled.disabled, false);

  makeDefault.checked = true;
  await makeDefault.dispatch("change");

  assert.equal(enabled.checked, true);
  assert.equal(enabled.disabled, true);
});

test("default submission cannot contain enabled false even with inconsistent controls", async (t) => {
  const root = installFakeDOM(t);
  const data = profileListFixture();
  const draft = profileDraft(data.profiles[2], data.default_profile_id);
  let saved;

  renderProfileEditor(root, draft, {
    save: async (payload) => {
      saved = payload;
    },
    cancel: () => {},
    generate: () => {},
  });

  controlByName(root, "make_default").checked = true;
  controlByName(root, "enabled").checked = false;
  await findTag(root, "FORM").dispatch("submit");

  assert.equal(saved.make_default, true);
  assert.equal(saved.enabled, true);
});

test("switching the editor to OpenAI hides and disables vision with an explicit note", async (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft("anthropic");
  draft.slug = "openai";
  draft.display_name = "OpenAI";
  draft.config.upstream = "https://openai.example";
  draft.config.vision.enabled = true;
  let saved;

  renderProfileEditor(root, draft, {
    save: async (payload) => {
      saved = payload;
    },
    cancel: () => {},
    generate: () => {},
  });

  const protocol = controlByName(root, "protocol");
  protocol.value = "openai";
  await protocol.dispatch("change");

  assert.equal(findClass(root, "vision-controls").hidden, true);
  assert.equal(
    findText(root, "视觉预处理目前需要 Anthropic 协议。").hidden,
    false,
  );
  assert.equal(controlByName(root, "vision_enabled").checked, false);
  assert.equal(controlByName(root, "vision_enabled").disabled, true);

  await findTag(root, "FORM").dispatch("submit");
  assert.equal(saved.config.protocol, "openai");
  assert.equal(saved.config.vision.enabled, false);
});

test("retry editor add remove up and down actions keep visible first-match order", async (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft();
  draft.config.overload_rules = [
    {
      status: 503,
      body_contains: "first",
      max_retries: 1,
      delay: "1s",
      jitter: "0s",
    },
    {
      status: 429,
      body_contains: "second",
      max_retries: 2,
      delay: "2s",
      jitter: "0s",
    },
  ];

  renderProfileEditor(root, draft, {
    save: async () => {},
    cancel: () => {},
    generate: () => {},
  });

  assert.deepEqual(retryBodies(root), ["first", "second"]);
  await buttonsByText(root, "上移")[1].dispatch("click");
  assert.deepEqual(retryBodies(root), ["second", "first"]);
  await buttonsByText(root, "下移")[0].dispatch("click");
  assert.deepEqual(retryBodies(root), ["first", "second"]);
  await buttonByText(root, "添加规则").dispatch("click");
  assert.equal(retryBodies(root).length, 3);
  await buttonsByText(root, "删除")[2].dispatch("click");
  assert.deepEqual(retryBodies(root), ["first", "second"]);
});

test("copy action uses a native dialog with editable non-secret fields", async (t) => {
  const root = installFakeDOM(t);
  const data = profileListFixture();
  const copies = [];
  renderProfileList(root, data, {
    edit: () => {},
    generate: () => {},
    copy: async (profile, body) => copies.push([profile.id, body]),
    setDefault: async () => {},
    toggle: async () => {},
    delete: async () => {},
  });

  await buttonByText(profileCard(root, "secondary"), "复制").dispatch("click");
  const dialog = openDialog(root);
  assert.equal(controlByName(dialog, "copy-display-name").value, "Secondary 副本");
  assert.equal(controlByName(dialog, "copy-slug").value, "secondary-copy");
  controlByName(dialog, "copy-display-name").value = "Secondary Clone";
  controlByName(dialog, "copy-slug").value = "secondary-clone";
  await findTag(dialog, "FORM").dispatch("submit");

  assert.deepEqual(copies, [
    [
      2,
      {
        display_name: "Secondary Clone",
        slug: "secondary-clone",
      },
    ],
  ]);
});

test("delete dialog blocks the only Profile and requires an enabled replacement for the default", async (t) => {
  const root = installFakeDOM(t);
  const only = profileFixture({ id: 9, slug: "only", display_name: "Only" });
  renderProfileList(
    root,
    { default_profile_id: 9, profiles: [only] },
    {
      edit: () => {},
      generate: () => {},
      copy: async () => {},
      setDefault: async () => {},
      toggle: async () => {},
      delete: async () => {
        throw new Error("must not delete");
      },
    },
  );
  assert.equal(buttonByText(root, "删除").disabled, true);
  assert.equal(findAllTags(root, "DIALOG").length, 0);

  const data = profileListFixture();
  const deletes = [];
  renderProfileList(root, data, {
    edit: () => {},
    generate: () => {},
    copy: async () => {},
    setDefault: async () => {},
    toggle: async () => {},
    delete: async (profile, replacementId) =>
      deletes.push([profile.id, replacementId]),
  });
  await buttonByText(profileCard(root, "primary"), "删除").dispatch("click");

  const dialog = openDialog(root);
  assert.ok(findText(dialog, "Primary"));
  assert.ok(findText(dialog, "primary"));
  const replacement = controlByName(dialog, "replacement_default_id");
  assert.deepEqual(
    replacement.children.map((option) => option.value),
    ["", "2"],
  );
  const submit = buttonByText(dialog, "删除 Profile");
  assert.equal(submit.disabled, true);
  replacement.value = "2";
  await replacement.dispatch("change");
  assert.equal(submit.disabled, false);
  await findTag(dialog, "FORM").dispatch("submit");
  assert.deepEqual(deletes, [[1, 2]]);
});

test("authenticated app creates a Profile only through the injected API client", async (t) => {
  const root = installFakeDOM(t);
  let listCalls = 0;
  const creates = [];
  const client = {
    session: async () => ({
      username: "admin",
      must_change_password: false,
    }),
    listProfiles: async () => {
      listCalls += 1;
      return { default_profile_id: 0, profiles: [] };
    },
    createProfile: async (payload) => {
      creates.push(payload);
    },
  };

  await bootstrap({ root, client });
  await buttonByText(root, "创建第一个 Profile").dispatch("click");
  assert.equal(controlByName(root, "enabled").checked, true);
  assert.equal(controlByName(root, "enabled").disabled, true);
  controlByName(root, "display_name").value = "New Profile";
  controlByName(root, "slug").value = "new-profile";
  controlByName(root, "upstream").value = "https://new.example";
  await findTag(root, "FORM").dispatch("submit");

  assert.equal(listCalls, 2);
  assert.equal(creates.length, 1);
  assert.equal(creates[0].display_name, "New Profile");
  assert.equal(creates[0].slug, "new-profile");
  assert.equal(creates[0].make_default, true);
  assert.equal(creates[0].config.upstream, "https://new.example");
  assert.equal(typeof creates[0].config.vision.max_tokens, "number");
});

function profileListFixture() {
  return {
    default_profile_id: 1,
    profiles: [
      profileFixture({
        id: 1,
        slug: "primary",
        display_name: "Primary",
        config: {
          version: 1,
          protocol: "anthropic",
          upstream: "https://primary.example",
          vision: {
            enabled: true,
            model: "vision-primary",
            max_tokens: 3072,
            timeout: "90s",
            max_concurrency: 5,
            cache_ttl: "20m",
            cache_max_entries: 200,
            prompt: "Original prompt",
          },
          overload_rules: [
            {
              status: 503,
              body_contains: "overloaded",
              max_retries: 4,
              delay: "2s",
              jitter: "200ms",
            },
          ],
        },
        usage_30d: {
          requests: 7,
          input_tokens: 100,
          output_tokens: 50,
        },
      }),
      profileFixture({
        id: 2,
        slug: "secondary",
        display_name: "Secondary",
        config: {
          version: 1,
          protocol: "anthropic",
          upstream: "https://secondary.example",
          vision: {
            enabled: false,
            model: "sonnet",
            max_tokens: 2048,
            timeout: "2m",
            max_concurrency: 4,
            cache_ttl: "30m",
            cache_max_entries: 512,
            prompt: "",
          },
          overload_rules: [],
        },
      }),
      profileFixture({
        id: 3,
        slug: "disabled",
        display_name: "<img src=x onerror=alert(1)>",
        enabled: false,
        config: {
          version: 1,
          protocol: "openai",
          upstream: "https://disabled.example",
          vision: {
            enabled: false,
            model: "sonnet",
            max_tokens: 2048,
            timeout: "2m",
            max_concurrency: 4,
            cache_ttl: "30m",
            cache_max_entries: 512,
            prompt: "",
          },
          overload_rules: [],
        },
      }),
    ],
  };
}

function profileFixture(overrides = {}) {
  const base = {
    id: 1,
    slug: "profile",
    display_name: "Profile",
    enabled: true,
    config: {
      version: 1,
      protocol: "anthropic",
      upstream: "https://profile.example",
      vision: {
        enabled: false,
        model: "sonnet",
        max_tokens: 2048,
        timeout: "2m",
        max_concurrency: 4,
        cache_ttl: "30m",
        cache_max_entries: 512,
        prompt: "",
      },
      overload_rules: [],
    },
    usage_30d: {
      requests: 0,
      input_tokens: 0,
      output_tokens: 0,
    },
  };
  return {
    ...base,
    ...overrides,
    config: overrides.config || base.config,
    usage_30d: overrides.usage_30d || base.usage_30d,
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
    this.classList = {
      values: new Set(),
      add: (...names) => {
        for (const name of names) {
          this.classList.values.add(name);
        }
      },
    };
    this.id = "";
    this.name = "";
    this.type = "";
    this.value = "";
    this.checked = false;
    this.required = false;
    this.disabled = false;
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
      defaultPrevented: false,
      preventDefault() {
        this.defaultPrevented = true;
      },
    };
    for (const listener of this.listeners.get(name) ?? []) {
      await listener(event);
    }
    return event;
  }

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

function findAllTags(root, tagName) {
  return descendants(root).filter(
    (element) => element.tagName === tagName.toUpperCase(),
  );
}

function findTag(root, tagName) {
  const element = findAllTags(root, tagName)[0];
  assert.ok(element, `${tagName} not found`);
  return element;
}

function findText(root, text) {
  const element = descendants(root).find((candidate) =>
    candidate.textContent.includes(text),
  );
  assert.ok(element, `text ${text} not found`);
  return element;
}

function findClass(root, className) {
  const element = descendants(root).find((candidate) =>
    candidate.className.split(/\s+/).includes(className),
  );
  assert.ok(element, `class ${className} not found`);
  return element;
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

function labelTexts(root) {
  return findAllTags(root, "LABEL").map((label) => label.textContent);
}

function buttonTexts(root) {
  return findAllTags(root, "BUTTON").map((button) => button.textContent);
}

function buttonsByText(root, text) {
  return findAllTags(root, "BUTTON").filter(
    (button) => button.textContent === text,
  );
}

function buttonByText(root, text) {
  const button = buttonsByText(root, text)[0];
  assert.ok(button, `button ${text} not found`);
  return button;
}

function sectionHeadings(root) {
  return findAllTags(root, "H2").map((heading) => heading.textContent);
}

function profileCard(root, slug) {
  const card = descendants(root).find(
    (element) =>
      element.className.split(/\s+/).includes("profile-card") &&
      descendants(element).some((child) => child.textContent === slug),
  );
  assert.ok(card, `Profile card ${slug} not found`);
  return card;
}

function retryBodies(root) {
  return controls(root)
    .filter((control) => /^retry-\d+-body_contains$/.test(control.name))
    .map((control) => control.value);
}

function openDialog(root) {
  const dialog = findAllTags(root, "DIALOG").find((candidate) => candidate.open);
  assert.ok(dialog, "open dialog not found");
  return dialog;
}
