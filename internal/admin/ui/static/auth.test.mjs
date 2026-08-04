import assert from "node:assert/strict";
import test from "node:test";

import {
  nextScreen,
  renderLogin,
  renderPasswordChange,
} from "./auth.js";
import {
  createWindowChrome,
  createWindowFrame,
} from "./chrome.js";

test("anonymous users see login", () => {
  assert.equal(nextScreen({ authenticated: false }), "login");
});

test("initial admin must change password", () => {
  assert.equal(
    nextScreen({
      authenticated: true,
      must_change_password: true,
    }),
    "password-change",
  );
});

test("initialized admin reaches overview", () => {
  assert.equal(
    nextScreen({
      authenticated: true,
      must_change_password: false,
    }),
    "overview",
  );
});

test("window chrome is decorative and has exactly three lights", (t) => {
  const root = installFakeDOM(t);
  const chrome = createWindowChrome("llm-proxy");
  root.append(chrome);

  assert.equal(chrome.className, "window-titlebar");
  const controls = chrome.children[0];
  assert.equal(controls.getAttribute("aria-hidden"), "true");
  assert.equal(controls.children.length, 3);
  assert.ok(
    controls.children.every((light) => light.tagName === "SPAN"),
  );
  assert.equal(
    controls.children.some(
      (light) => light.getAttribute("tabindex") !== null,
    ),
    false,
  );
});

test("window frame keeps caller content in a dedicated body", (t) => {
  const root = installFakeDOM(t);
  const content = document.createElement("p");
  content.textContent = "content";

  const frame = createWindowFrame({
    titleText: "llm-proxy",
    className: "auth-window",
    children: [content],
  });
  root.append(frame);

  assert.match(frame.className, /mac-window/);
  assert.match(frame.className, /auth-window/);
  assert.equal(frame.children[1].className, "mac-window-body");
  assert.equal(frame.children[1].children[0], content);
});

test("login renders the fixed accessible copy and submits transient credentials", async (t) => {
  const root = installFakeDOM(t);
  root.append(new FakeElement("p"));
  let submitted;

  renderLogin(root, {
    login: async (username, password) => {
      submitted = { username, password };
    },
  });

  assert.equal(root.children.length, 1);
  assert.equal(elementsByClass(root, "auth-stage").length, 1);
  assert.equal(elementsByClass(root, "auth-window").length, 1);
  assert.equal(elementsByClass(root, "window-control").length, 3);
  assert.equal(
    descendants(root).some((element) => element.tagName === "NAV"),
    false,
  );
  assert.equal(findText(root, "首次登录存在公网抢占风险"), undefined);
  assert.deepEqual(labelTexts(root), ["用户名", "密码"]);
  assert.equal(buttonByText(root, "登录").type, "submit");

  const username = inputByName(root, "username");
  const password = inputByName(root, "password");
  assert.equal(username.value, "admin");
  assert.equal(username.autocomplete, "username");
  assert.equal(password.value, "");
  assert.equal(password.type, "password");
  assert.equal(password.autocomplete, "current-password");
  assertLabelsInput(root, "用户名", username);
  assertLabelsInput(root, "密码", password);
  assert.equal(findTag(root, "FORM").getAttribute("action"), null);

  username.value = "admin";
  password.value = "login-secret";
  await findTag(root, "FORM").dispatch("submit");

  assert.deepEqual(submitted, {
    username: "admin",
    password: "login-secret",
  });
  assert.equal(password.value, "");
});

test("login API errors are announced without retaining the password", async (t) => {
  const root = installFakeDOM(t);
  renderLogin(root, {
    login: async () => {
      throw new Error("登录失败");
    },
  });
  inputByName(root, "password").value = "login-secret";

  await findTag(root, "FORM").dispatch("submit");

  const alert = findRole(root, "alert");
  assert.equal(alert.hidden, false);
  assert.equal(alert.textContent, "登录失败");
  assert.equal(inputByName(root, "password").value, "");
});

test("password change renders only exact fixed labels with empty password fields", (t) => {
  const root = installFakeDOM(t);
  renderPasswordChange(root, {
    changePassword: async () => {},
  });

  assert.equal(elementsByClass(root, "auth-stage").length, 1);
  assert.equal(elementsByClass(root, "auth-window").length, 1);
  assert.equal(elementsByClass(root, "window-control").length, 3);
  assert.equal(
    descendants(root).some((element) => element.tagName === "NAV"),
    false,
  );
  assert.ok(findText(root, "修改初始密码"));
  assert.deepEqual(labelTexts(root), [
    "当前密码",
    "新密码",
    "确认新密码",
  ]);
  assert.equal(buttonByText(root, "保存新密码").type, "submit");
  for (const name of [
    "current-password",
    "new-password",
    "confirm-password",
  ]) {
    const input = inputByName(root, name);
    assert.equal(input.type, "password");
    assert.equal(input.value, "");
    assert.equal(input.required, true);
  }
});

test("password validation counts Unicode characters instead of UTF-16 units", async (t) => {
  const root = installFakeDOM(t);
  const calls = [];
  renderPasswordChange(root, {
    changePassword: async (...credentials) => {
      calls.push(credentials);
    },
  });
  const form = findTag(root, "FORM");

  inputByName(root, "current-password").value = "admin";
  inputByName(root, "new-password").value = "😀".repeat(9);
  inputByName(root, "confirm-password").value = "😀".repeat(9);
  await form.dispatch("submit");
  assert.equal(calls.length, 0);
  assert.equal(findRole(root, "alert").hidden, false);

  inputByName(root, "current-password").value = "admin";
  inputByName(root, "new-password").value = "😀".repeat(10);
  inputByName(root, "confirm-password").value = "😀".repeat(10);
  await form.dispatch("submit");
  assert.deepEqual(calls, [["admin", "😀".repeat(10)]]);
  assert.equal(inputByName(root, "current-password").value, "");
  assert.equal(inputByName(root, "new-password").value, "");
  assert.equal(inputByName(root, "confirm-password").value, "");
});

test("password validation rejects a mismatched confirmation", async (t) => {
  const root = installFakeDOM(t);
  let calls = 0;
  renderPasswordChange(root, {
    changePassword: async () => {
      calls += 1;
    },
  });
  inputByName(root, "current-password").value = "admin";
  inputByName(root, "new-password").value = "安全密码一二三四五六七八";
  inputByName(root, "confirm-password").value = "安全密码一二三四五六七九";

  await findTag(root, "FORM").dispatch("submit");

  assert.equal(calls, 0);
  assert.equal(findRole(root, "alert").hidden, false);
});

test("bootstrap returns a 401 Session to a clean login screen", async (t) => {
  const root = installFakeDOM(t);
  const stale = new FakeElement("p");
  stale.textContent = "stale screen";
  root.append(stale);
  const bootstrap = await loadBootstrap();

  await bootstrap({
    root,
    client: {
      session: async () => {
        throw unauthorizedError();
      },
    },
  });

  assert.equal(findText(root, "stale screen"), undefined);
  assert.ok(buttonByText(root, "登录"));
  assert.deepEqual(labelTexts(root), ["用户名", "密码"]);
});

test("bootstrap keeps must-change users on the narrow password screen", async (t) => {
  const root = installFakeDOM(t);
  const bootstrap = await loadBootstrap();

  await bootstrap({
    root,
    client: {
      session: async () => ({
        username: "admin",
        must_change_password: true,
      }),
    },
  });

  assert.ok(findText(root, "修改初始密码"));
  assert.equal(descendants(root).some((element) => element.tagName === "NAV"), false);
});

test("authenticated bootstrap renders Overview as the default destination", async (t) => {
  const root = installFakeDOM(t);
  const bootstrap = await loadBootstrap();

  await bootstrap({
    root,
    client: {
      session: async () => ({
        username: "admin",
        must_change_password: false,
      }),
    },
  });

  assert.deepEqual(navigationTexts(root), ["概览", "Profiles", "统计", "系统", "帮助", "退出"]);
  assert.equal(elementsByClass(root, "desktop-stage").length, 1);
  assert.equal(elementsByClass(root, "app-window").length, 1);
  assert.equal(elementsByClass(root, "window-control").length, 3);
  assert.equal(
    elementsByClass(root, "window-controls")[0].getAttribute("aria-hidden"),
    "true",
  );
  assert.equal(elementsByClass(root, "app-sidebar").length, 1);
  assert.equal(findHeading(root, "概览").tagName, "H1");
  assert.equal(linkByText(root, "概览").getAttribute("href"), "/_admin/");
  assert.equal(linkByText(root, "概览").getAttribute("aria-current"), "page");
});

test("successful login enters only the mandatory password-change screen", async (t) => {
  const root = installFakeDOM(t);
  const bootstrap = await loadBootstrap();
  const calls = [];
  await bootstrap({
    root,
    client: {
      session: async () => {
        throw unauthorizedError();
      },
      login: async (username, password) => {
        calls.push([username, password]);
        return {
          username: "admin",
          must_change_password: true,
        };
      },
    },
  });

  inputByName(root, "username").value = "admin";
  inputByName(root, "password").value = "login-secret";
  await findTag(root, "FORM").dispatch("submit");

  assert.deepEqual(calls, [["admin", "login-secret"]]);
  assert.ok(findText(root, "修改初始密码"));
  assert.equal(descendants(root).some((element) => element.tagName === "NAV"), false);
});

test("password success refreshes the rotated Session before entering Overview", async (t) => {
  const root = installFakeDOM(t);
  const bootstrap = await loadBootstrap();
  const calls = [];
  let sessionCalls = 0;
  await bootstrap({
    root,
    client: {
      session: async () => {
        sessionCalls += 1;
        calls.push("session");
        return {
          username: "admin",
          must_change_password: sessionCalls === 1,
        };
      },
      changePassword: async (currentPassword, newPassword) => {
        calls.push(["change", currentPassword, newPassword]);
      },
    },
  });

  inputByName(root, "current-password").value = "admin";
  inputByName(root, "new-password").value = "安全密码一二三四五六七八";
  inputByName(root, "confirm-password").value = "安全密码一二三四五六七八";
  await findTag(root, "FORM").dispatch("submit");

  assert.deepEqual(calls, [
    "session",
    ["change", "admin", "安全密码一二三四五六七八"],
    "session",
  ]);
  assert.deepEqual(navigationTexts(root), ["概览", "Profiles", "统计", "系统", "帮助", "退出"]);
});

test("authenticated bootstrap renders a stable not-found page for an unknown path", async (t) => {
  const root = installFakeDOM(t);
  const bootstrap = await loadBootstrap();

  await bootstrap({
    root,
    path: "/_admin/unknown",
    client: {
      session: async () => ({
        username: "admin",
        must_change_password: false,
      }),
    },
  });

  assert.ok(findText(root, "页面不存在"));
  assert.equal(linkByText(root, "返回 Profiles").getAttribute("href"), "/_admin/profiles");
});

test("authenticated setup route opens the four-step first-run flow", async (t) => {
  const root = installFakeDOM(t);
  const bootstrap = await loadBootstrap();

  await bootstrap({
    root,
    path: "/_admin/setup",
    client: {
      session: async () => ({ username: "admin", must_change_password: false }),
      listProfiles: async () => ({ default_profile_id: 0, profiles: [] }),
    },
  });

  assert.equal(findHeading(root, "开始配置").tagName, "H1");
  assert.ok(findText(root, "让第一个 Agent 跑起来"));
  assert.equal(descendants(root).filter((element) => element.tagName === "OL")[0].children.length, 4);
});

test("fresh administrator enters setup directly from the admin root", async (t) => {
  const root = installFakeDOM(t);
  const bootstrap = await loadBootstrap();
  await bootstrap({
    root,
    path: "/_admin/",
    client: {
      session: async () => ({
        username: "admin",
        must_change_password: false,
        initialization_state: "profile_setup_required",
      }),
      listProfiles: async () => ({ default_profile_id: 0, profiles: [] }),
    },
  });

  assert.equal(findHeading(root, "开始配置").tagName, "H1");
  assert.ok(findText(root, "让第一个 Agent 跑起来"));
});

test("a 401 during password change clears the form and returns to login", async (t) => {
  const root = installFakeDOM(t);
  const bootstrap = await loadBootstrap();
  await bootstrap({
    root,
    client: {
      session: async () => ({
        username: "admin",
        must_change_password: true,
      }),
      changePassword: async () => {
        throw unauthorizedError();
      },
    },
  });

  inputByName(root, "current-password").value = "admin";
  inputByName(root, "new-password").value = "安全密码一二三四五六七八";
  inputByName(root, "confirm-password").value = "安全密码一二三四五六七八";
  await findTag(root, "FORM").dispatch("submit");

  assert.ok(buttonByText(root, "登录"));
  assert.equal(inputByName(root, "password").value, "");
});

test("authenticated logout returns to login without extra navigation", async (t) => {
  const root = installFakeDOM(t);
  const bootstrap = await loadBootstrap();
  let logoutCalls = 0;
  await bootstrap({
    root,
    client: {
      session: async () => ({
        username: "admin",
        must_change_password: false,
      }),
      logout: async () => {
        logoutCalls += 1;
      },
    },
  });

  await buttonByText(root, "退出").dispatch("click");

  assert.equal(logoutCalls, 1);
  assert.ok(buttonByText(root, "登录"));
});

test("bootstrap API failures render an accessible alert", async (t) => {
  const root = installFakeDOM(t);
  const bootstrap = await loadBootstrap();

  await bootstrap({
    root,
    client: {
      session: async () => {
        throw new Error("服务暂不可用");
      },
    },
  });

  const alert = findRole(root, "alert");
  assert.equal(elementsByClass(root, "auth-stage").length, 1);
  assert.equal(elementsByClass(root, "auth-window").length, 1);
  assert.equal(elementsByClass(root, "window-control").length, 3);
  assert.equal(alert.hidden, false);
  assert.equal(alert.textContent, "服务暂不可用");
});

class FakeElement {
  constructor(tagName) {
    this.tagName = tagName.toUpperCase();
    this.children = [];
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
    this.autocomplete = "";
    this.required = false;
    this.disabled = false;
    this.hidden = false;
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

  addEventListener(name, listener) {
    const listeners = this.listeners.get(name) ?? [];
    listeners.push(listener);
    this.listeners.set(name, listeners);
  }

  async dispatch(name) {
    const event = {
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
    querySelector: (selector) => (selector === "#app" ? root : null),
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
    if (originalLocalStorage === undefined) {
      delete globalThis.localStorage;
    } else {
      Object.defineProperty(globalThis, "localStorage", {
        configurable: true,
        value: originalLocalStorage,
      });
    }
    if (originalSessionStorage === undefined) {
      delete globalThis.sessionStorage;
    } else {
      Object.defineProperty(globalThis, "sessionStorage", {
        configurable: true,
        value: originalSessionStorage,
      });
    }
  });
  return root;
}

function descendants(root) {
  return [root, ...root.children.flatMap(descendants)];
}

function elementsByClass(root, className) {
  return descendants(root).filter((element) =>
    String(element.className ?? "")
      .split(/\s+/)
      .includes(className),
  );
}

function findTag(root, tagName) {
  const element = descendants(root).find(
    (candidate) => candidate.tagName === tagName,
  );
  assert.ok(element, `${tagName} not found`);
  return element;
}

function findText(root, text) {
  return descendants(root).find((element) =>
    element.textContent.includes(text),
  );
}

function findRole(root, role) {
  const element = descendants(root).find(
    (candidate) => candidate.getAttribute("role") === role,
  );
  assert.ok(element, `role=${role} not found`);
  return element;
}

function labelTexts(root) {
  return descendants(root)
    .filter((element) => element.tagName === "LABEL")
    .map((element) => element.textContent);
}

function inputByName(root, name) {
  const input = descendants(root).find(
    (element) => element.tagName === "INPUT" && element.name === name,
  );
  assert.ok(input, `input ${name} not found`);
  return input;
}

function buttonByText(root, text) {
  const button = descendants(root).find(
    (element) =>
      element.tagName === "BUTTON" && element.textContent === text,
  );
  assert.ok(button, `button ${text} not found`);
  return button;
}

function linkByText(root, text) {
  const link = descendants(root).find(
    (element) => element.tagName === "A" && element.textContent === text,
  );
  assert.ok(link, `link ${text} not found`);
  return link;
}

function assertLabelsInput(root, labelText, input) {
  const label = descendants(root).find(
    (element) =>
      element.tagName === "LABEL" && element.textContent === labelText,
  );
  assert.ok(label, `label ${labelText} not found`);
  assert.equal(label.getAttribute("for"), input.id);
}

let appImportSequence = 0;

async function loadBootstrap() {
  appImportSequence += 1;
  const module = await import(`./app.js?test=${appImportSequence}`);
  return module.bootstrap;
}

function unauthorizedError() {
  return Object.assign(new Error("未登录"), { status: 401 });
}

function navigationTexts(root) {
  const navigation = descendants(root).find(
    (element) => element.tagName === "NAV",
  );
  assert.ok(navigation, "navigation not found");
  return descendants(navigation)
    .filter(
      (element) =>
        element.tagName === "A" || element.tagName === "BUTTON",
    )
    .map((element) => element.textContent);
}

function findHeading(root, text) {
  const heading = descendants(root).find(
    (element) =>
      /^H[1-6]$/.test(element.tagName) && element.textContent === text,
  );
  assert.ok(heading, `heading ${text} not found`);
  return heading;
}
