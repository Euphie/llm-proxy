import assert from "node:assert/strict";
import test from "node:test";

import { api } from "./api.js";
import { bootstrap } from "./app.js";
import {
  kindLabel,
  normalizeFilters,
  renderStatsPage,
  tokenTotal,
} from "./stats.js";
import {
  buildProfileExport,
  renderSystemPage,
} from "./system.js";

test("empty statistics filters are omitted", () => {
  assert.deepEqual(
    normalizeFilters({
      profile_id: "",
      protocol: " ",
      model: "",
      kind: "",
      from: "",
      to: "",
    }),
    {},
  );
});

test("statistics date-time filters become RFC3339 instants", () => {
  assert.deepEqual(
    normalizeFilters({
      from: "2026-07-29T10:30:00+08:00",
      to: "2026-07-29T12:45:00+08:00",
    }),
    {
      from: "2026-07-29T02:30:00.000Z",
      to: "2026-07-29T04:45:00.000Z",
    },
  );
});

test("token total excludes cache counters already represented in input", () => {
  assert.equal(
    tokenTotal({
      input_tokens: 10,
      output_tokens: 20,
      cache_read_tokens: 4,
      cache_creation_tokens: 5,
    }),
    30,
  );
});

test("request kinds have distinct user-facing labels", () => {
  assert.equal(kindLabel("main"), "主请求");
  assert.equal(kindLabel("vision"), "视觉预处理");
});

test("statistics and System API methods omit blanks and encode exact queries", async (t) => {
  const calls = [];
  const originalDocument = globalThis.document;
  const originalFetch = globalThis.fetch;
  globalThis.document = { cookie: "" };
  globalThis.fetch = async (path, options) => {
    calls.push([path, options.method]);
    return new Response("{}", {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  };
  t.after(() => {
    globalThis.document = originalDocument;
    globalThis.fetch = originalFetch;
  });

  await api.stats({
    profile_id: "7",
    protocol: "anthropic",
    model: "",
    kind: "vision",
    from: "2026-07-29T02:30:00.000Z",
    to: "",
  });
  await api.stats({});
  await api.routingTraces({ profile_id: "7", limit: 50 });
  await api.system();

  assert.deepEqual(calls, [
    [
      "/_admin/api/stats?profile_id=7&protocol=anthropic&kind=vision&from=2026-07-29T02%3A30%3A00.000Z",
      "GET",
    ],
    ["/_admin/api/stats", "GET"],
    ["/_admin/api/routing-traces?profile_id=7&limit=50", "GET"],
    ["/_admin/api/system", "GET"],
  ]);
});

test("statistics API trims real filters and omits whitespace-only filters", async (t) => {
  const calls = [];
  const originalDocument = globalThis.document;
  const originalFetch = globalThis.fetch;
  globalThis.document = { cookie: "" };
  globalThis.fetch = async (path) => {
    calls.push(path);
    return new Response("{}", {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  };
  t.after(() => {
    globalThis.document = originalDocument;
    globalThis.fetch = originalFetch;
  });

  await api.stats({
    profile_id: " \t ",
    protocol: " anthropic ",
    model: " sonnet / 4 ",
    kind: "\n",
  });
  await api.stats({ from: " ", to: "\t" });

  assert.deepEqual(calls, [
    "/_admin/api/stats?protocol=anthropic&model=sonnet+%2F+4",
    "/_admin/api/stats",
  ]);
});

test("statistics page exposes loading then exact summary and accessible tables", async (t) => {
  const root = installFakeDOM(t);
  let finish;
  const pending = new Promise((resolve) => {
    finish = resolve;
  });

  const rendered = renderStatsPage(root, {
    profiles: [
      { id: 1, display_name: "Coding", slug: "coding" },
    ],
    loadStats: async () => pending,
    loadRoutingTraces: async () => routingTraceFixture(),
    onUnauthorized: () => {},
  });

  assert.equal(findRole(root, "status").textContent, "正在加载统计数据…");
  finish(statsFixture());
  await rendered;

  assert.deepEqual(labelTexts(root), [
    "Profile",
    "协议",
    "模型",
    "请求类型",
    "开始时间",
    "结束时间",
  ]);
  assert.equal(controlByName(root, "from").type, "datetime-local");
  assert.equal(controlByName(root, "to").type, "datetime-local");
  for (const value of [
    "请求数：2",
    "输入 Token：10",
    "输出 Token：20",
    "Token 总计：30",
    "缓存读取 Token：4",
    "缓存创建 Token：5",
  ]) {
    assert.ok(findText(root, value), value);
  }
  assert.deepEqual(
    findAllTags(root, "CAPTION").map((caption) => caption.textContent),
    ["按日期", "按模型", "最近 Auto 路由"],
  );
  assert.ok(findText(root, "2026-07-29"));
  assert.ok(findText(root, "sonnet"));
  assert.ok(findText(root, "fast → strong · Target primary → region_b"));
  assert.ok(findText(root, "回答 2 / 辅助 1 / 模型切换 1 / Target 切换 1"));
  assert.ok(findText(root, "计划上限 $0.030126 / 已预留 $0.0155"));
});

test("statistics filters submit RFC3339 values and omit blanks", async (t) => {
  const root = installFakeDOM(t);
  const filters = [];
  const traceFilters = [];

  await renderStatsPage(root, {
    profiles: [
      { id: 7, display_name: "Coding", slug: "coding" },
    ],
    loadStats: async (current) => {
      filters.push(current);
      return statsFixture();
    },
    loadRoutingTraces: async (current) => {
      traceFilters.push(current);
      return [];
    },
    onUnauthorized: () => {},
  });

  controlByName(root, "profile_id").value = "7";
  controlByName(root, "protocol").value = "anthropic";
  controlByName(root, "model").value = "";
  controlByName(root, "kind").value = "vision";
  controlByName(root, "from").value = "2026-07-29T10:30";
  controlByName(root, "to").value = "";
  await findTag(root, "FORM").dispatch("submit");

  assert.equal(filters.length, 2);
  assert.deepEqual(filters[1], {
    profile_id: "7",
    protocol: "anthropic",
    kind: "vision",
    from: "2026-07-29T10:30:00.000Z",
  });
  assert.deepEqual(traceFilters[1], {
    profile_id: "7",
    from: "2026-07-29T10:30:00.000Z",
    limit: 100,
  });
});

test("statistics page announces empty and error states and delegates 401 recovery", async (t) => {
  const root = installFakeDOM(t);
  let unauthorized = 0;

  await renderStatsPage(root, {
    profiles: [],
    loadStats: async () => emptyStatsFixture(),
    onUnauthorized: () => {
      unauthorized += 1;
    },
  });
  assert.equal(findRole(root, "status").textContent, "暂无统计数据。");

  await renderStatsPage(root, {
    profiles: [],
    loadStats: async () => {
      throw new Error("统计暂不可用");
    },
    onUnauthorized: () => {
      unauthorized += 1;
    },
  });
  assert.equal(findRole(root, "alert").textContent, "统计暂不可用");

  await renderStatsPage(root, {
    profiles: [],
    loadStats: async () => {
      const error = new Error("expired");
      error.status = 401;
      throw error;
    },
    onUnauthorized: () => {
      unauthorized += 1;
    },
  });
  assert.equal(unauthorized, 1);
});

test("Profile export keeps only versioned portable configuration", () => {
  const exported = buildProfileExport({
    default_profile_id: 1,
    profiles: [
      {
        id: 1,
        slug: "coding",
        display_name: "Coding",
        enabled: true,
        config: {
          version: 1,
          protocol: "anthropic",
          upstream: "https://coding.example",
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
          requests: 9,
          input_tokens: 10,
          output_tokens: 20,
        },
        session_token: "must-not-export",
      },
    ],
  });

  assert.deepEqual(exported, {
    version: 1,
    profiles: [
      {
        slug: "coding",
        display_name: "Coding",
        enabled: true,
        default: true,
        config: {
          version: 1,
          protocol: "anthropic",
          upstream: "https://coding.example",
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
      },
    ],
  });
  assert.doesNotMatch(
    JSON.stringify(exported),
    /usage_30d|session_token|"id"/,
  );
});

test("System page renders exact safe values password change and sanitized export", async (t) => {
  const root = installFakeDOM(t);
  const downloads = [];
  const passwordChanges = [];

  await renderSystemPage(root, {
    loadSystem: async () => systemFixture(),
    loadProfiles: async () => ({
      default_profile_id: 7,
      profiles: [
        {
          id: 7,
          slug: "coding",
          display_name: "Coding",
          enabled: true,
          config: { version: 1 },
          usage_30d: { requests: 99 },
        },
      ],
    }),
    changePassword: async (...passwords) => {
      passwordChanges.push(passwords);
    },
    download: (filename, contents) => {
      downloads.push([filename, JSON.parse(contents)]);
    },
    onUnauthorized: () => {},
  });

  for (const value of [
    "版本：v1.2.3",
    "数据目录：/app/data",
    "数据库文件：llm-proxy.db",
    "数据库大小：12345 B",
    "Schema 版本：2",
    "默认 Profile ID：7",
    "必须修改密码：否",
  ]) {
    assert.ok(findText(root, value), value);
  }
  assert.doesNotMatch(
    descendants(root)
      .map((element) => element.textContent)
      .join(" "),
    /dsn|environment|session|username/i,
  );

  assert.deepEqual(
    labelTexts(root).filter((text) => text.includes("密码")),
    ["当前密码", "新密码", "确认新密码"],
  );
  controlByName(root, "current-password").value = "old-password";
  controlByName(root, "new-password").value = "new-password-123";
  controlByName(root, "confirm-password").value = "new-password-123";
  await findTag(root, "FORM").dispatch("submit");
  assert.deepEqual(passwordChanges, [
    ["old-password", "new-password-123"],
  ]);
  assert.equal(controlByName(root, "current-password").value, "");
  assert.equal(controlByName(root, "new-password").value, "");
  assert.equal(controlByName(root, "confirm-password").value, "");

  await buttonByText(root, "导出 Profiles").dispatch("click");
  assert.deepEqual(downloads, [
    [
      "llm-proxy-profiles-v1.json",
      {
        version: 1,
        profiles: [
          {
            slug: "coding",
            display_name: "Coding",
            enabled: true,
            default: true,
            config: { version: 1 },
          },
        ],
      },
    ],
  ]);
});

test("System page exposes loading and error states and delegates 401 recovery", async (t) => {
  const root = installFakeDOM(t);
  let finish;
  const pending = new Promise((resolve) => {
    finish = resolve;
  });
  const rendering = renderSystemPage(root, {
    loadSystem: async () => pending,
    loadProfiles: async () => ({ default_profile_id: 0, profiles: [] }),
    changePassword: async () => {},
    download: () => {},
    onUnauthorized: () => {},
  });
  assert.equal(findRole(root, "status").textContent, "正在加载系统信息…");
  finish(systemFixture());
  await rendering;

  await renderSystemPage(root, {
    loadSystem: async () => {
      throw new Error("系统信息暂不可用");
    },
    loadProfiles: async () => ({ default_profile_id: 0, profiles: [] }),
    changePassword: async () => {},
    download: () => {},
    onUnauthorized: () => {},
  });
  assert.equal(findRole(root, "alert").textContent, "系统信息暂不可用");

  let unauthorized = 0;
  await renderSystemPage(root, {
    loadSystem: async () => {
      const error = new Error("expired");
      error.status = 401;
      throw error;
    },
    loadProfiles: async () => ({ default_profile_id: 0, profiles: [] }),
    changePassword: async () => {},
    download: () => {},
    onUnauthorized: () => {
      unauthorized += 1;
    },
  });
  assert.equal(unauthorized, 1);
});

test("authenticated app routes the statistics path through the injected client", async (t) => {
  const root = installFakeDOM(t);
  const calls = [];

  await bootstrap({
    root,
    path: "/_admin/stats",
    client: {
      session: async () => ({
        username: "admin",
        must_change_password: false,
      }),
      listProfiles: async () => ({
        default_profile_id: 1,
        profiles: [
          { id: 1, display_name: "Coding", slug: "coding" },
        ],
      }),
      stats: async (filters) => {
        calls.push(filters);
        return statsFixture();
      },
    },
  });

  assert.equal(findAllTags(root, "H1")[0].textContent, "统计");
  assert.equal(linkByText(root, "统计").getAttribute("aria-current"), "page");
  assert.deepEqual(calls, [{}]);
  assert.ok(findText(root, "Token 总计：30"));
});

test("authenticated app routes the System path and recovers page-level 401", async (t) => {
  const root = installFakeDOM(t);

  await bootstrap({
    root,
    path: "/_admin/system",
    client: {
      session: async () => ({
        username: "admin",
        must_change_password: false,
      }),
      system: async () => systemFixture(),
      listProfiles: async () => ({
        default_profile_id: 0,
        profiles: [],
      }),
      changePassword: async () => {},
    },
  });

  assert.equal(findAllTags(root, "H1")[0].textContent, "系统");
  assert.equal(linkByText(root, "系统").getAttribute("aria-current"), "page");
  assert.ok(findText(root, "数据库大小：12345 B"));

  await bootstrap({
    root,
    path: "/_admin/stats",
    client: {
      session: async () => ({
        username: "admin",
        must_change_password: false,
      }),
      listProfiles: async () => ({
        default_profile_id: 0,
        profiles: [],
      }),
      stats: async () => {
        const error = new Error("expired");
        error.status = 401;
        throw error;
      },
    },
  });
  assert.ok(buttonByText(root, "登录"));
});

function statsFixture() {
  return {
    summary: {
      key: "total",
      requests: 2,
      input_tokens: 10,
      output_tokens: 20,
      cache_read_tokens: 4,
      cache_creation_tokens: 5,
      total_tokens: 30,
    },
    by_day: [
      {
        key: "2026-07-29",
        requests: 2,
        input_tokens: 10,
        output_tokens: 20,
        cache_read_tokens: 4,
        cache_creation_tokens: 5,
        total_tokens: 30,
      },
    ],
    by_model: [
      {
        key: "sonnet",
        requests: 2,
        input_tokens: 10,
        output_tokens: 20,
        cache_read_tokens: 4,
        cache_creation_tokens: 5,
        total_tokens: 30,
      },
    ],
  };
}

function emptyStatsFixture() {
  return {
    summary: {
      key: "total",
      requests: 0,
      input_tokens: 0,
      output_tokens: 0,
      cache_read_tokens: 0,
      cache_creation_tokens: 0,
      total_tokens: 0,
    },
    by_day: [],
    by_model: [],
  };
}

function routingTraceFixture() {
  return [{
    id: 1,
    created_at: "2026-07-29T02:30:00Z",
    profile_id: 7,
    profile_slug: "coding",
    protocol: "anthropic",
    path: "/v1/messages",
    strategy: "20260802-001",
    route: "balanced",
    task_type: "simple",
    risk: "normal",
    classification_source: "rule",
    initial_model: "fast",
    final_model: "strong",
    initial_target: "primary",
    final_target: "region_b",
    vision_mode: "native",
    status_code: 200,
    client_committed: true,
    answer_attempts: 2,
    auxiliary_calls: 1,
    total_outbound_calls: 3,
    model_switches: 1,
    target_switches: 1,
    planned_worst_case_cost_micro_usd: 30126,
    reserved_cost_micro_usd: 15500,
    elapsed_ms: 42,
  }];
}

function systemFixture() {
  return {
    version: "v1.2.3",
    data_dir: "/app/data",
    database_file: "llm-proxy.db",
    database_bytes: 12345,
    schema_version: 2,
    default_profile_id: 7,
    password_must_change: false,
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
    this.id = "";
    this.name = "";
    this.type = "";
    this.value = "";
    this.checked = false;
    this.required = false;
    this.disabled = false;
    this.hidden = false;
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
  const result = findAllTags(root, tagName)[0];
  assert.ok(result, `${tagName} not found`);
  return result;
}

function findText(root, text) {
  const result = descendants(root).find((element) =>
    element.textContent.includes(text),
  );
  assert.ok(result, `text ${text} not found`);
  return result;
}

function findRole(root, role) {
  const result = descendants(root).find(
    (element) => element.getAttribute("role") === role,
  );
  assert.ok(result, `role ${role} not found`);
  return result;
}

function controls(root) {
  return descendants(root).filter((element) =>
    ["INPUT", "SELECT", "TEXTAREA"].includes(element.tagName),
  );
}

function controlByName(root, name) {
  const result = controls(root).find((element) => element.name === name);
  assert.ok(result, `control ${name} not found`);
  return result;
}

function labelTexts(root) {
  return findAllTags(root, "LABEL").map((label) => label.textContent);
}

function buttonByText(root, text) {
  const result = findAllTags(root, "BUTTON").find(
    (button) => button.textContent === text,
  );
  assert.ok(result, `button ${text} not found`);
  return result;
}

function linkByText(root, text) {
  const result = findAllTags(root, "A").find(
    (link) => link.textContent === text,
  );
  assert.ok(result, `link ${text} not found`);
  return result;
}
