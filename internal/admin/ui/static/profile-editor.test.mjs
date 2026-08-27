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

test("reliability keeps ordinary retries editable without upstream failover controls", (t) => {
  const root = installFakeDOM(t);
  renderProfileEditor(root, profileFixture(), {
    section: "reliability",
    defaultProfileID: 7,
    renderLegacy: legacySectionFixture,
  });

  assert.ok(findText(root, "容错规则"));
  assert.equal(findText(root, "上游节点容错"), undefined);
  assert.equal(findTextIncludes(root, "备用上游节点"), undefined);
});

test("Profile section navigation protects unsaved changes", async (t) => {
  const root = installFakeDOM(t);
  const confirmations = [];
  renderProfileEditor(root, profileFixture(), {
    section: "models",
    defaultProfileID: 7,
    renderLegacy: legacySectionFixture,
    confirmLeave: () => {
      confirmations.push("asked");
      return false;
    },
  });

  const form = descendants(root).find((element) => element.tagName === "FORM");
  await form.dispatch("input");
  const event = await linkByText(root, "连接").dispatch("click");
  assert.deepEqual(confirmations, ["asked"]);
  assert.equal(event.defaultPrevented, true);
});

test("routing section preserves strategy and evaluation catalog view state", (t) => {
  const root = installFakeDOM(t);
  const profile = profileFixture();
  profile.strategy_overview = { snapshot: { revision: 3 } };
  profile.strategy_editing_id = 11;
  profile.strategy_configuration_mode = "manual";
  profile.strategy_generation_notice = "推荐草稿已生成，可以直接调整后保存。";
  profile.evaluation_catalog = {
    revision: "catalog-revision",
    sources: [{ id: "livebench", name: "LiveBench" }],
    results: [],
  };
  let renderedDraft;
  let renderedActions;

  renderProfileEditor(root, profile, {
    section: "routing",
    defaultProfileID: 7,
    renderLegacy: (mount, draft, actions) => {
      renderedDraft = draft;
      renderedActions = actions;
      legacySectionFixture(mount);
    },
  });

  assert.deepEqual(renderedDraft.strategy_overview, profile.strategy_overview);
  assert.equal(renderedDraft.strategy_editing_id, 11);
  assert.equal(renderedDraft.strategy_configuration_mode, "manual");
  assert.equal(
    renderedDraft.strategy_generation_notice,
    "推荐草稿已生成，可以直接调整后保存。",
  );
  assert.deepEqual(renderedDraft.evaluation_catalog, profile.evaluation_catalog);
  assert.equal(renderedActions.editorSection, "routing");
});

test("v2 sections preserve the live Policy and model directory overviews", async (t) => {
  const profile = profileFixture();
  profile.config.version = 2;
  profile.config.models = [];
  profile.routing_policy_overview = {
    runtime_state: { revision: 12, model_catalog_revision: 4 },
    active: {
      id: 31,
      policy: {
        name: "policy-31",
        roles: {
          participants: ["deepseek"],
          strong_baseline_model: "deepseek",
          task_analyzer_model: "deepseek",
        },
        dynamic_optimization: {
          enabled: true,
          auto_update_policy: true,
          sample_rate_bps: 1000,
          daily_budget_micro_usd: 100000,
          reviewer_model: "deepseek",
          max_concurrency: 1,
          queue_capacity: 16,
          task_timeout: "1m",
        },
        routes: [{ id: "general", candidates: [{ model: "deepseek" }] }],
      },
    },
    history: [],
    automatic_update: {
      profile_id: 7,
      dirty: false,
      confirmation_count: 1,
      last_outcome: "waiting_confirmation",
      last_reason: "等待第 2/2 次一致证据窗口。",
    },
  };
  profile.model_directory_overview = {
    runtime_state: { revision: 12, model_catalog_revision: 4 },
    models: [{
      model_id: "deepseek",
      status: "available",
      status_reason: "",
      capability: { id: "deepseek" },
    }],
  };

  const routingRoot = installFakeDOM(t);
  let appliedPolicy = null;
  renderProfileEditor(routingRoot, profile, {
    section: "routing",
    defaultProfileID: 7,
    applyRoutingPolicy: async (_revision, policy) => { appliedPolicy = policy; },
  });
  assert.ok(findText(routingRoot, "当前策略概览"));
  assert.ok(findText(routingRoot, "policy-31"));
  assert.ok(findText(routingRoot, "#12"));
  assert.ok(findText(routingRoot, "困难任务基线"));
  assert.ok(findText(routingRoot, "智能生成策略"));
  assert.ok(findText(routingRoot, "手动调整"));
  assert.ok(findText(routingRoot, "等待第 2 次一致证据"));
  assert.equal(findText(routingRoot, "基本设置"), undefined);
  assert.equal(
    descendants(routingRoot).some((element) => element.className === "policy-json-editor"),
    false,
  );

  await findText(routingRoot, "手动调整").dispatch("click");
  assert.ok(findText(routingRoot, "1 模型与角色"));
  assert.ok(findText(routingRoot, "2 任务映射"));
  assert.ok(findText(routingRoot, "3 Route 与模型"));
  assert.ok(findText(routingRoot, "高级设置"));
  assert.ok(findText(routingRoot, "4 确认生效"));
  assert.ok(findText(routingRoot, "模型角色"));
  assert.equal(findText(routingRoot, "任务映射"), undefined);

  await findText(routingRoot, "2 任务映射").dispatch("click");
  assert.ok(findText(routingRoot, "任务映射"));
  assert.equal(findText(routingRoot, "模型角色"), undefined);

  await findText(routingRoot, "高级设置").dispatch("click");
  assert.ok(findText(routingRoot, "策略与分析"));
  assert.ok(findText(routingRoot, "Route 门槛与评分"));
  assert.ok(findText(routingRoot, "调用预算"));
  assert.ok(findText(routingRoot, "异步评测与风险"));
  const automaticUpdateLabel = findText(routingRoot, "评测可靠后自动更新线上策略");
  assert.ok(automaticUpdateLabel);
  assert.ok(findTextIncludes(routingRoot, "连续两次一致确认"));
  assert.ok(findTextIncludes(routingRoot, "证据缺失、不可靠或跌破门槛会立即撤销生产资格"));
  assert.ok(findText(routingRoot, "高级：查看原始 JSON"));
  const automaticUpdate = automaticUpdateLabel.parentNode.children[0];
  automaticUpdate.checked = true;
  await automaticUpdate.dispatch("change");
  await findText(routingRoot, "4 确认生效").dispatch("click");
  await findText(routingRoot, "保存并立即生效").dispatch("click");
  assert.equal(appliedPolicy.dynamic_optimization.auto_update_policy, true);

  const modelsRoot = new FakeElement("main");
  renderProfileEditor(modelsRoot, profile, { section: "models", defaultProfileID: 7 });
  assert.ok(findText(modelsRoot, "deepseek"));
  assert.ok(findText(modelsRoot, "修订 #4"));
  assert.ok(findText(modelsRoot, "当前线上用途：参与模型、困难任务基线、任务分析、Route general"));
  assert.ok(findText(modelsRoot, "批量录入模型"));
  assert.ok(findText(modelsRoot, "预览批量导入"));
  const batchInput = descendants(modelsRoot).find(
    (element) => element.name === "model_batch_ids",
  );
  assert.ok(batchInput);
  batchInput.value = "unknown-batch-model";
  await findText(modelsRoot, "预览批量导入").dispatch("click");
  assert.ok(findText(modelsRoot, "确认导入 1 个模型"));
  assert.ok(findText(modelsRoot, "没有可靠模板，请手动补充能力参数。"));
  assert.ok(findText(modelsRoot, "上下文窗口"));
  assert.ok(findText(modelsRoot, "支持工具调用"));
  assert.ok(findText(modelsRoot, "支持 Agent 工作流"));
  assert.ok(findText(modelsRoot, "输入价格（美元/百万 Token）"));
  assert.equal(
    descendants(modelsRoot).some(
      (element) => element.tagName === "TEXTAREA" && String(element.value).includes('"id"'),
    ),
    false,
  );
});

function legacySectionFixture(root) {
  const form = document.createElement("form");
  for (const heading of [
    "基础配置",
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
    this.listeners = new Map();
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

  addEventListener(name, listener) {
    const listeners = this.listeners.get(name) || [];
    listeners.push(listener);
    this.listeners.set(name, listeners);
  }

  async dispatch(name) {
    const event = {
      defaultPrevented: false,
      preventDefault() { this.defaultPrevented = true; },
    };
    for (const listener of this.listeners.get(name) || []) {
      await listener(event);
    }
    return event;
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
