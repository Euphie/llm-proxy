import assert from "node:assert/strict";
import test from "node:test";

import { api } from "./api.js";
import { bootstrap } from "./app.js";
import {
  aggregateGatewayPayload,
  providerAccountPayload,
  renderAggregateGatewaysPage,
} from "./aggregate-gateways.js";

test("Aggregate Gateway API methods use exact routes and CSRF", async (t) => {
  const calls = [];
  const originalDocument = globalThis.document;
  const originalFetch = globalThis.fetch;
  globalThis.document = { cookie: "llm_proxy_csrf=csrf-token" };
  globalThis.fetch = async (requestPath, options = {}) => {
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

  const provider = { slug: "openai", secret: "secret" };
  const gateway = { slug: "team", routes: [] };
  const key = { name: "local", enabled: true };
  await api.listProviderAccounts();
  await api.createProviderAccount(provider);
  await api.updateProviderAccount(7, provider);
  await api.listAggregateGateways();
  await api.createAggregateGateway(gateway);
  await api.updateAggregateGateway(9, gateway);
  await api.listAggregateGatewayKeys(9);
  await api.createAggregateGatewayKey(9, key);

  assert.deepEqual(calls, [
    {
      path: "/_admin/api/provider-accounts",
      method: "GET",
      body: undefined,
      csrf: null,
    },
    {
      path: "/_admin/api/provider-accounts",
      method: "POST",
      body: provider,
      csrf: "csrf-token",
    },
    {
      path: "/_admin/api/provider-accounts/7",
      method: "PUT",
      body: provider,
      csrf: "csrf-token",
    },
    {
      path: "/_admin/api/aggregate-gateways",
      method: "GET",
      body: undefined,
      csrf: null,
    },
    {
      path: "/_admin/api/aggregate-gateways",
      method: "POST",
      body: gateway,
      csrf: "csrf-token",
    },
    {
      path: "/_admin/api/aggregate-gateways/9",
      method: "PUT",
      body: gateway,
      csrf: "csrf-token",
    },
    {
      path: "/_admin/api/aggregate-gateways/9/keys",
      method: "GET",
      body: undefined,
      csrf: null,
    },
    {
      path: "/_admin/api/aggregate-gateways/9/keys",
      method: "POST",
      body: key,
      csrf: "csrf-token",
    },
  ]);
});

test("Aggregate Gateway payloads keep provider secrets write-only", () => {
	  assert.deepEqual(providerAccountPayload({
	    slug: " openai ",
	    display_name: " OpenAI ",
	    enabled: true,
	    protocol: "openai",
	    upstream: " https://api.openai.com ",
	    auth_header: "Authorization",
	    secret: " Bearer provider-secret ",
	    models: [{
	      model_id: " gpt-4o ",
	      display_name: " GPT-4o ",
	      enabled: true,
	    }],
	  }), {
	    slug: "openai",
	    display_name: "OpenAI",
	    enabled: true,
	    protocol: "openai",
	    upstream: "https://api.openai.com",
	    auth_header: "Authorization",
	    secret: "Bearer provider-secret",
	    models: [{
	      model_id: "gpt-4o",
	      display_name: "GPT-4o",
	      enabled: true,
	    }],
	  });

  assert.deepEqual(aggregateGatewayPayload({
    slug: " team ",
    display_name: " Team ",
    enabled: true,
    protocol: "openai",
    routes: [{
      public_model: " gpt-4o ",
      provider_account_id: "7",
      provider_model: " provider-gpt-4o ",
      enabled: true,
    }, {
      public_model: " claude-sonnet ",
      provider_account_id: "8",
      provider_model: " claude-3-5-sonnet-latest ",
      enabled: false,
    }],
  }), {
    slug: "team",
    display_name: "Team",
    enabled: true,
    protocol: "openai",
    routes: [{
      public_model: "gpt-4o",
      provider_account_id: 7,
      provider_model: "provider-gpt-4o",
      enabled: true,
    }, {
      public_model: "claude-sonnet",
      provider_account_id: 8,
      provider_model: "claude-3-5-sonnet-latest",
      enabled: false,
    }],
  });
});

test("Aggregate Gateway provider view manages provider accounts only", async (t) => {
  const root = installFakeDOM(t);
  const calls = [];

  renderAggregateGatewaysPage(root, aggregateFixture(), {
    saveProvider: async (payload) => calls.push(["provider", payload]),
  }, { view: "providers" });

  assert.deepEqual(sectionHeadings(root), ["供应商管理"]);
  assert.ok(findText(root, "OpenAI Primary"));
  assert.ok(findText(root, "Provider GPT-4o"));
  assert.equal(
    controls(root).some((control) => control.name === "provider_secret"),
    true,
  );
  assert.equal(
    controls(root).some((control) => control.name === "gateway_slug"),
    false,
  );

  const providerForm = formByData(root, "provider");
  assert.equal(providerForm.hidden, true);
  await buttonByText(root, "新增供应商").dispatch("click");
  assert.equal(providerForm.hidden, false);

  controlByName(root, "provider_slug").value = "anthropic-primary";
  controlByName(root, "provider_display_name").value = "Anthropic Primary";
  controlByName(root, "provider_protocol").value = "anthropic";
  controlByName(root, "provider_upstream").value = "https://api.anthropic.com";
  controlByName(root, "provider_auth_header").value = "x-api-key";
  controlByName(root, "provider_secret").value = "anthropic-secret";
  controlByNamePrefix(root, "provider_model_id_").value = "claude-3-5-sonnet-latest";
  controlByNamePrefix(root, "provider_model_display_name_").value = "Claude Sonnet";
  await providerForm.dispatch("submit");

  assert.deepEqual(calls, [
    ["provider", {
      slug: "anthropic-primary",
      display_name: "Anthropic Primary",
      enabled: true,
      protocol: "anthropic",
      upstream: "https://api.anthropic.com",
      auth_header: "x-api-key",
      secret: "anthropic-secret",
      models: [{
        model_id: "claude-3-5-sonnet-latest",
        display_name: "Claude Sonnet",
        enabled: true,
      }],
    }],
  ]);
});

test("Aggregate Gateway provider view explains invalid slugs before saving", async (t) => {
  const root = installFakeDOM(t);
  const calls = [];

  renderAggregateGatewaysPage(root, aggregateFixture(), {
    saveProvider: async (payload) => calls.push(payload),
  }, { view: "providers" });

  await buttonByText(root, "新增供应商").dispatch("click");
  const slug = controlByName(root, "provider_slug");
  assert.equal(slug.getAttribute("pattern"), "[a-z0-9][a-z0-9-]{0,62}");
  assert.equal(slug.getAttribute("maxlength"), "63");
  slug.value = "deepseek_vision";

  await formByData(root, "provider").dispatch("submit");

  assert.deepEqual(calls, []);
  assert.ok(findText(
    root,
    "Slug 只能使用小写字母、数字和连字符（-），长度为 1-63 位。",
  ));
  const dialog = descendants(root).find((element) =>
    element.className === "error-banner error-dialog"
  );
  assert.ok(dialog, "error dialog not found");
  assert.equal(dialog.getAttribute("role"), "alertdialog");
  assert.equal(dialog.getAttribute("aria-modal"), "true");
  await buttonByText(root, "×").dispatch("click");
  assert.equal(dialog.hidden, true);
});

test("Aggregate Gateway gateway view manages gateway routing only", async (t) => {
  const root = installFakeDOM(t);
  const calls = [];

  renderAggregateGatewaysPage(root, aggregateFixture(), {
    saveGateway: async (payload) => calls.push(["gateway", payload]),
  }, { view: "gateways" });

  assert.deepEqual(sectionHeadings(root), ["网关管理"]);
  assert.ok(findText(root, "gpt-4o"));
  assert.ok(findText(root, "provider-gpt-4o"));
  assert.ok(findText(root, "选择模型"));
  assert.equal(
    controls(root).some((control) => control.name === "provider_secret"),
    false,
  );
  assert.equal(
    controls(root).some((control) => control.name.startsWith("route_priority_")),
    false,
  );

  controlByName(root, "gateway_slug").value = "research";
  controlByName(root, "gateway_display_name").value = "Research Gateway";
  controlByName(root, "gateway_protocol").value = "openai";
  const first = controlByNamePrefix(root, "route_selected_");
  first.checked = true;
  await first.dispatch("change");
  controlByNamePrefix(root, "route_public_model_").value = "gpt-4o";
  const second = controlByNamePrefix(root, "route_selected_", 1);
  second.checked = true;
  await second.dispatch("change");
  controlByNamePrefix(root, "route_public_model_", 1).value = "gpt-4o-mini";
  await formByData(root, "gateway").dispatch("submit");

  assert.deepEqual(calls, [
    ["gateway", {
      slug: "research",
      display_name: "Research Gateway",
      enabled: true,
      protocol: "openai",
      routes: [{
        public_model: "gpt-4o",
        provider_account_id: 7,
        provider_model: "provider-gpt-4o",
        enabled: true,
      }, {
        public_model: "gpt-4o-mini",
        provider_account_id: 7,
        provider_model: "provider-gpt-4o-mini",
        enabled: true,
      }],
    }],
  ]);
});

test("Aggregate Gateway gateway view rejects duplicate public model names", async (t) => {
  const root = installFakeDOM(t);
  const calls = [];

  renderAggregateGatewaysPage(root, aggregateFixture(), {
    saveGateway: async (payload) => calls.push(payload),
  }, { view: "gateways" });

  controlByName(root, "gateway_slug").value = "research";
  controlByName(root, "gateway_display_name").value = "Research Gateway";
  const first = controlByNamePrefix(root, "route_selected_");
  first.checked = true;
  await first.dispatch("change");
  controlByNamePrefix(root, "route_public_model_").value = "shared-model";
  const second = controlByNamePrefix(root, "route_selected_", 1);
  second.checked = true;
  await second.dispatch("change");
  controlByNamePrefix(root, "route_public_model_", 1).value = "shared-model";

  await formByData(root, "gateway").dispatch("submit");

  assert.deepEqual(calls, []);
  assert.ok(findText(root, "对外模型 shared-model 已重复，请改成其它名称。"));
});

test("Aggregate Gateway gateway view preserves multiple routes while editing", async (t) => {
  const root = installFakeDOM(t);
  const calls = [];

  renderAggregateGatewaysPage(root, aggregateFixture(), {
    saveGateway: async (payload, id) => calls.push(["gateway", id, payload]),
  }, { view: "gateways", gatewayID: 11 });

  assert.equal(controlByName(root, "gateway_slug").value, "team");
  assert.equal(controlByName(root, "gateway_display_name").value, "Team Gateway");
  assert.deepEqual(routeCheckedValues(root, "route_selected_"), [true, true, false]);
  assert.deepEqual(routeControlValues(root, "route_public_model_"), [
    "gpt-4o",
    "gpt-4o-mini",
    "provider-gpt-5-mini",
  ]);
  await formByData(root, "gateway").dispatch("submit");

  assert.deepEqual(calls, [
    ["gateway", 11, {
      slug: "team",
      display_name: "Team Gateway",
      enabled: true,
      protocol: "openai",
      routes: [{
        public_model: "gpt-4o",
        provider_account_id: 7,
        provider_model: "provider-gpt-4o",
        enabled: true,
      }, {
        public_model: "gpt-4o-mini",
        provider_account_id: 7,
        provider_model: "provider-gpt-4o-mini",
        enabled: true,
      }],
    }],
  ]);
});

test("Aggregate Gateway list view links each gateway to its edit form", (t) => {
  const root = installFakeDOM(t);

  renderAggregateGatewaysPage(root, aggregateFixture(), {}, { view: "gateway-list" });

  assert.deepEqual(sectionHeadings(root), ["全部网关"]);
  assert.equal(
    linkByText(root, "新建网关").getAttribute("href"),
    "/_admin/aggregate-gateways/gateways",
  );
  assert.ok(findText(root, "Team Gateway"));
  assert.ok(findText(root, "Archive Gateway"));
  assert.ok(findText(root, "/gateways/team/v1"));
  assert.ok(findText(root, "gpt-4o"));
  assert.equal(
    linkByText(root, "编辑").getAttribute("href"),
    "/_admin/aggregate-gateways/gateways?id=11",
  );
  assert.equal(
    controls(root).some((control) => control.name === "gateway_slug"),
    false,
  );
});

test("Aggregate Gateway key view filters multiple keys by gateway and appends issued keys", async (t) => {
  const root = installFakeDOM(t);
  const calls = [];

  renderAggregateGatewaysPage(root, aggregateFixture(), {
    createKey: async (gatewayID, payload) => {
      calls.push(["key", gatewayID, payload]);
      return {
        id: 24,
        gateway_id: gatewayID,
        name: payload.name,
        prefix: "lgp_created",
        last_four: "wxyz",
        key: "lgp_demo_value",
        enabled: true,
      };
    },
  }, { view: "keys" });

  assert.deepEqual(sectionHeadings(root), ["秘钥管理"]);
  assert.ok(findText(root, "前缀"));
  assert.equal(hasText(root, "Prefix"), false);
  assert.ok(findText(root, "Live"));
  assert.ok(findText(root, "CI"));
  assert.equal(hasText(root, "Archive Token"), false);

  const keyForm = formByData(root, "key");
  assert.equal(keyForm.hidden, true);
  const gatewayID = controlByName(root, "key_gateway_id");
  gatewayID.value = "12";
  await gatewayID.dispatch("change");
  assert.ok(findText(root, "Archive Token"));
  assert.equal(hasText(root, "Live"), false);

  gatewayID.value = "11";
  await gatewayID.dispatch("change");
  await buttonByText(root, "添加秘钥").dispatch("click");
  assert.equal(keyForm.hidden, false);
  controlByName(root, "key_gateway_id").value = "11";
  controlByName(root, "key_name").value = "Staging";
  await keyForm.dispatch("submit");

  assert.deepEqual(calls, [
    ["key", 11, { name: "Staging", enabled: true, expires_at: "" }],
  ]);
  assert.equal(keyForm.hidden, true);
  assert.ok(findText(root, "Staging"));
  assert.ok(findText(root, "lgp_demo_value"));
});

test("authenticated app exposes Aggregate Gateway as grouped submenu pages", async (t) => {
  const root = installFakeDOM(t);
  let loadedProviders = 0;
  let loadedGateways = 0;
  const client = {
    session: async () => ({
      username: "admin",
      must_change_password: false,
    }),
    listProviderAccounts: async () => {
      loadedProviders += 1;
      return { providers: aggregateFixture().providers };
    },
    listAggregateGateways: async () => {
      loadedGateways += 1;
      return { gateways: aggregateFixture().gateways };
    },
    listAggregateGatewayKeys: async () => ({ keys: aggregateFixture().keys }),
  };

  await bootstrap({ root, client, path: "/_admin/aggregate-gateways" });

  assert.ok(findText(root, "聚合网关"));
  assert.ok(findText(root, "供应商管理"));
  assert.ok(findText(root, "网关管理"));
  assert.ok(findText(root, "秘钥管理"));
  assert.ok(findText(root, "Team Gateway"));
  assert.equal(
    controls(root).some((control) => control.name === "provider_secret"),
    false,
  );
  assert.equal(loadedProviders, 1);
  assert.equal(loadedGateways, 1);
});

test("authenticated app opens a selected gateway edit form without exposing its hidden path", async (t) => {
  const root = installFakeDOM(t);
  const client = {
    session: async () => ({
      username: "admin",
      must_change_password: false,
    }),
    listProviderAccounts: async () => ({ providers: aggregateFixture().providers }),
    listAggregateGateways: async () => ({ gateways: aggregateFixture().gateways }),
    listAggregateGatewayKeys: async () => ({ keys: aggregateFixture().keys }),
  };

  await bootstrap({ root, client, path: "/_admin/aggregate-gateways/gateways?id=11" });

  assert.equal(findAllTags(root, "H1")[0].textContent, "网关管理");
  assert.equal(sidebarLinkHrefs(root).includes("/_admin/aggregate-gateways/gateways"), false);
  assert.equal(
    linkByText(root, "网关管理").getAttribute("aria-current"),
    null,
  );
  assert.equal(controlByName(root, "gateway_slug").value, "team");
  assert.ok(findText(root, "更新网关"));
});

test("authenticated app returns to the gateway list without a success notice", async (t) => {
  const root = installFakeDOM(t);
  const updates = [];
  const client = {
    session: async () => ({
      username: "admin",
      must_change_password: false,
    }),
    listProviderAccounts: async () => ({ providers: aggregateFixture().providers }),
    listAggregateGateways: async () => ({ gateways: aggregateFixture().gateways }),
    listAggregateGatewayKeys: async () => ({ keys: aggregateFixture().keys }),
    updateAggregateGateway: async (id, payload) => updates.push([id, payload]),
  };

  await bootstrap({ root, client, path: "/_admin/aggregate-gateways/gateways?id=11" });
  await formByData(root, "gateway").dispatch("submit");

  assert.equal(updates.length, 1);
  assert.ok(findText(root, "全部网关"));
  assert.equal(hasText(root, "更新网关"), false);
  assert.equal(hasText(root, "网关更新成功。"), false);
  assert.equal(window.location.pathname, "/_admin/aggregate-gateways");
  assert.equal(linkByText(root, "网关管理").getAttribute("aria-current"), "page");
});

function aggregateFixture() {
  return {
    providers: [{
      id: 7,
      slug: "openai-primary",
      display_name: "OpenAI Primary",
      enabled: true,
      protocol: "openai",
      upstream: "https://provider.example",
      auth_header: "Authorization",
      has_secret: true,
      models: [{
        id: 31,
        provider_account_id: 7,
        model_id: "provider-gpt-4o",
        display_name: "Provider GPT-4o",
        enabled: true,
      }, {
        id: 32,
        provider_account_id: 7,
        model_id: "provider-gpt-4o-mini",
        display_name: "Provider GPT-4o Mini",
        enabled: true,
      }, {
        id: 33,
        provider_account_id: 7,
        model_id: "provider-gpt-5-mini",
        display_name: "Provider GPT-5 Mini",
        enabled: true,
      }],
    }],
    gateways: [{
      id: 11,
      slug: "team",
      display_name: "Team Gateway",
      enabled: true,
      protocol: "openai",
      routes: [{
        id: 13,
        gateway_id: 11,
        public_model: "gpt-4o",
        provider_account_id: 7,
        provider_model: "provider-gpt-4o",
        enabled: true,
      }, {
        id: 15,
        gateway_id: 11,
        public_model: "gpt-4o-mini",
        provider_account_id: 7,
        provider_model: "provider-gpt-4o-mini",
        enabled: true,
      }],
    }, {
      id: 12,
      slug: "archive",
      display_name: "Archive Gateway",
      enabled: true,
      protocol: "openai",
      routes: [{
        id: 14,
        gateway_id: 12,
        public_model: "gpt-4o-mini",
        provider_account_id: 7,
        provider_model: "provider-gpt-4o-mini",
        enabled: true,
      }],
    }],
    keys: [{
      id: 21,
      gateway_id: 11,
      name: "Live",
      prefix: "lgp_live",
      last_four: "abcd",
      enabled: true,
    }, {
      id: 22,
      gateway_id: 11,
      name: "CI",
      prefix: "lgp_ci",
      last_four: "efgh",
      enabled: true,
    }, {
      id: 23,
      gateway_id: 12,
      name: "Archive Token",
      prefix: "lgp_archive",
      last_four: "ijkl",
      enabled: true,
    }],
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

  async dispatch(name, eventInit = {}) {
    const event = {
      ...eventInit,
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
  const originalWindow = globalThis.window;
  const location = { pathname: "/_admin/profiles", search: "" };
  globalThis.document = {
    createElement: (name) => new FakeElement(name),
  };
  globalThis.window = {
    location,
    history: {
      replaceState: (_state, _title, path) => {
        const target = new URL(path, "http://localhost");
        location.pathname = target.pathname;
        location.search = target.search;
      },
    },
  };
  t.after(() => {
    globalThis.document = originalDocument;
    globalThis.window = originalWindow;
  });
  return root;
}

function descendants(root) {
  return [root, ...root.children.flatMap(descendants)];
}

function findAllTags(root, tagName) {
  return descendants(root).filter(
    (element) => element.tagName === tagName.toUpperCase(),
  );
}

function findText(root, text) {
  const element = descendants(root).find((candidate) =>
    candidate.textContent.includes(text)
  );
  assert.ok(element, `text ${text} not found`);
  return element;
}

function hasText(root, text) {
  return descendants(root).some((candidate) =>
    candidate.textContent.includes(text)
  );
}

function controls(root) {
  return descendants(root).filter((element) =>
    ["INPUT", "SELECT", "TEXTAREA"].includes(element.tagName)
  );
}

function controlByName(root, name) {
  const control = controls(root).find((element) => element.name === name);
  assert.ok(control, `control ${name} not found`);
  return control;
}

function controlsByNamePrefix(root, prefix) {
  const matches = controls(root).filter((element) => element.name.startsWith(prefix));
  assert.ok(matches.length > 0, `controls ${prefix} not found`);
  return matches;
}

function controlByNamePrefix(root, prefix, index = 0) {
  const control = controlsByNamePrefix(root, prefix)[index];
  assert.ok(control, `control ${prefix}[${index}] not found`);
  return control;
}

function routeControlValues(root, prefix) {
  return controlsByNamePrefix(root, prefix).map((control) => control.value);
}

function routeCheckedValues(root, prefix) {
  return controlsByNamePrefix(root, prefix).map((control) => control.checked);
}

function formByData(root, value) {
  const form = findAllTags(root, "FORM").find(
    (element) => element.getAttribute("data-aggregate-form") === value,
  );
  assert.ok(form, `form ${value} not found`);
  return form;
}

function linkByText(root, text) {
  const link = findAllTags(root, "A").find(
    (element) => element.textContent === text,
  );
  assert.ok(link, `link ${text} not found`);
  return link;
}

function buttonByText(root, text) {
  const button = findAllTags(root, "BUTTON").find(
    (element) => element.textContent === text,
  );
  assert.ok(button, `button ${text} not found`);
  return button;
}

function sidebarLinkTexts(root) {
  const navigation = findAllTags(root, "NAV")[0];
  assert.ok(navigation, "navigation not found");
  return descendants(navigation)
    .filter((element) => element.tagName === "A")
    .map((element) => element.textContent);
}

function sidebarLinkHrefs(root) {
  const navigation = findAllTags(root, "NAV")[0];
  assert.ok(navigation, "navigation not found");
  return descendants(navigation)
    .filter((element) => element.tagName === "A")
    .map((element) => element.getAttribute("href"));
}

function sectionHeadings(root) {
  return findAllTags(root, "H2").map((heading) => heading.textContent);
}
