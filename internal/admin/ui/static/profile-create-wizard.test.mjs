import assert from "node:assert/strict";
import test from "node:test";

import { renderProfileCreationWizard } from "./profile-create-wizard.js";
import { defaultProfileDraft } from "./profile-draft.js";

test("Profile creation wizard persists connection and models across four focused steps", async (t) => {
  const root = installFakeDOM(t);
  const createdPayloads = [];
  const updatedPayloads = [];
  const draft = defaultProfileDraft();
  draft.display_name = "Coding";
  draft.slug = "coding";
  draft.config.upstream = "https://upstream.example";

  await renderProfileCreationWizard(root, draft, {
    renderEditor: wizardEditorFixture,
    createProfile: async (payload) => {
      createdPayloads.push(structuredClone(payload));
      return { ...structuredClone(payload), id: 7 };
    },
    updateProfile: async (_id, payload) => {
      updatedPayloads.push(structuredClone(payload));
      return { ...structuredClone(payload), id: 7 };
    },
  });

  assert.deepEqual(stepLabels(root), ["连接上游", "录入模型", "智能路由", "完成"]);
  assert.deepEqual(sectionHeadings(root), ["基础配置"]);
  assert.equal(buttonByText(root, "退出向导").className, "button button-secondary");
  await findTag(root, "FORM").dispatch("submit");

  assert.equal(createdPayloads.length, 1);
  assert.deepEqual(sectionHeadings(root), ["模型能力"]);
  assert.equal(buttonByText(root, "暂不录入").className, "button button-secondary");
  await findTag(root, "FORM").dispatch("submit");

  assert.equal(updatedPayloads.length, 1);
  assert.deepEqual(sectionHeadings(root), ["智能路由"]);
  await buttonByText(root, "上一步").dispatch("click");
  assert.deepEqual(sectionHeadings(root), ["模型能力"]);
  await buttonByText(root, "上一步").dispatch("click");
  assert.deepEqual(sectionHeadings(root), ["基础配置"]);
  assert.equal(findText(root, "Coding").textContent, "Coding");
});

test("generated routing recommendation opens manual fields and saves before completion", async (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft();
  draft.display_name = "Coding";
  draft.slug = "coding";
  draft.config.upstream = "https://upstream.example";
  draft.config.models = [{ id: "fast" }, { id: "strong" }];
  const updates = [];
  const generationIntents = [];
  let completedProfile;

  await renderProfileCreationWizard(root, draft, {
    renderEditor: wizardEditorFixture,
    createProfile: async (payload) => ({ ...structuredClone(payload), id: 7 }),
    updateProfile: async (_id, payload) => {
      updates.push(structuredClone(payload));
      return { ...structuredClone(payload), id: 7 };
    },
    generateStrategy: async (_id, intent) => {
      generationIntents.push(intent);
      const strategy = structuredClone(draft.config.auto_routing.strategy);
      strategy.alias = "智能推荐 · 均衡";
      return {
        recommendation: {
          roles: {
            participants: ["fast", "strong"],
            strong_baseline_model: "strong",
            task_analyzer_model: "fast",
            reviewer_model: "strong",
          },
          config: strategy,
        },
      };
    },
    loadEvaluationCatalog: async () => ({ revision: "catalog", sources: [], results: [] }),
    complete: (profile) => { completedProfile = profile; },
  });

  await findTag(root, "FORM").dispatch("submit");
  await buttonByText(root, "暂不录入").dispatch("click");
  await buttonByText(root, "生成推荐").dispatch("click");

  assert.deepEqual(generationIntents, [{ objective: "balanced", participants: ["fast", "strong"] }]);
  assert.ok(findText(root, "推荐草稿已生成，可以直接调整后保存。"));
  assert.equal(controlByName(root, "fixture-strategy-alias").value, "智能推荐 · 均衡");
  controlByName(root, "fixture-strategy-alias").value = "人工调整";
  await findTag(root, "FORM").dispatch("submit");

  assert.equal(updates.at(-1).config.auto_routing.strategy.alias, "人工调整");
  assert.ok(findText(root, "/coding/v1/…"));
  assert.equal(linkByText(root, "打开 Profile 概况").href, "/_admin/profiles/7/overview");
  await buttonByText(root, "完成").dispatch("click");
  assert.equal(completedProfile.id, 7);
});

test("exiting after Profile creation keeps the persisted Profile", async (t) => {
  const root = installFakeDOM(t);
  const draft = defaultProfileDraft();
  draft.display_name = "Coding";
  draft.slug = "coding";
  draft.config.upstream = "https://upstream.example";
  let cancelled = 0;

  await renderProfileCreationWizard(root, draft, {
    renderEditor: wizardEditorFixture,
    createProfile: async (payload) => ({ ...structuredClone(payload), id: 7 }),
    updateProfile: async () => {},
    cancel: () => { cancelled += 1; },
  });
  await findTag(root, "FORM").dispatch("submit");
  await buttonByText(root, "退出向导").dispatch("click");

  assert.equal(cancelled, 1);
  assert.equal(buttonTexts(root).includes("删除 Profile"), false);
});

function wizardEditorFixture(root, source, actions) {
  const form = document.createElement("form");
  for (const heading of ["基础配置", "模型能力", "智能路由", "视觉增强", "容错规则", "配置生成"]) {
    const section = document.createElement("section");
    const title = document.createElement("h2");
    title.textContent = heading;
    section.append(title);
    if (heading === "基础配置") {
      const name = document.createElement("p");
      name.textContent = source.display_name;
      section.append(name);
    }
    if (heading === "智能路由") {
      const alias = document.createElement("input");
      alias.name = "fixture-strategy-alias";
      alias.value = source.config.auto_routing.strategy.alias;
      section.append(alias);
      const notice = document.createElement("p");
      notice.textContent = source.strategy_generation_notice || "";
      section.append(notice);
      const generate = button("生成推荐");
      generate.addEventListener("click", () => actions.generateStrategy?.({
        objective: "balanced",
        participants: source.config.models.map((model) => model.id),
      }));
      section.append(generate);
    }
    section.children = htmlCollection(section.children);
    form.append(section);
  }
  const alert = document.createElement("div");
  alert.setAttribute("role", "alert");
  alert.hidden = true;
  const footer = document.createElement("div");
  footer.className = "editor-actions";
  const save = button("保存 Profile");
  save.type = "submit";
  const cancel = button("返回列表");
  cancel.addEventListener("click", () => actions.cancel?.());
  footer.append(save, cancel);
  footer.children = htmlCollection(footer.children);
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const payload = structuredClone(source);
    const alias = descendants(form).find((item) => item.name === "fixture-strategy-alias");
    if (alias) {
      payload.config.auto_routing.enabled = true;
      payload.config.auto_routing.strategy.alias = alias.value;
    }
    await actions.save?.(payload);
  });
  form.append(alert, footer);
  root.replaceChildren(form);
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
    this.name = "";
    this.type = "";
    this.value = "";
    this.hidden = false;
  }

  append(...children) {
    for (const child of children) {
      child.parentNode = this;
      if (typeof this.children.appendItem === "function") {
        this.children.appendItem(child);
      } else {
        this.children.push(child);
      }
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
    if (name === "href") this.href = String(value);
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
    const event = { target: this, preventDefault() {} };
    for (const listener of this.listeners.get(name) || []) {
      await listener(event);
    }
  }
}

function installFakeDOM(t) {
  const originalDocument = globalThis.document;
  globalThis.document = { createElement: (name) => new FakeElement(name) };
  t.after(() => { globalThis.document = originalDocument; });
  return new FakeElement("main");
}

function button(text) {
  const control = document.createElement("button");
  control.type = "button";
  control.textContent = text;
  return control;
}

function descendants(root) {
  return [root, ...[...(root.children || [])].flatMap(descendants)];
}

function htmlCollection(items) {
  const values = [...items];
  const collection = {
    get length() {
      return values.length;
    },
    appendItem(item) {
      collection[values.length] = item;
      values.push(item);
    },
    *[Symbol.iterator]() {
      yield* values;
    },
  };
  values.forEach((item, index) => { collection[index] = item; });
  return collection;
}

function findTag(root, tagName) {
  const found = descendants(root).find((item) => item.tagName === tagName.toUpperCase());
  assert.ok(found, `${tagName} not found`);
  return found;
}

function findText(root, text) {
  const found = descendants(root).find((item) => item.textContent.includes(text));
  assert.ok(found, `text ${text} not found`);
  return found;
}

function buttonByText(root, text) {
  const found = descendants(root).find((item) => item.tagName === "BUTTON" && item.textContent === text);
  assert.ok(found, `button ${text} not found`);
  return found;
}

function buttonTexts(root) {
  return descendants(root).filter((item) => item.tagName === "BUTTON").map((item) => item.textContent);
}

function controlByName(root, name) {
  const found = descendants(root).find((item) => item.name === name);
  assert.ok(found, `control ${name} not found`);
  return found;
}

function linkByText(root, text) {
  const found = descendants(root).find((item) => item.tagName === "A" && item.textContent === text);
  assert.ok(found, `link ${text} not found`);
  return found;
}

function stepLabels(root) {
  return descendants(root)
    .filter((item) => String(item.className).split(/\s+/).includes("profile-create-step"))
    .map((item) => item.textContent);
}

function sectionHeadings(root) {
  return descendants(root).filter((item) => item.tagName === "H2").map((item) => item.textContent);
}
