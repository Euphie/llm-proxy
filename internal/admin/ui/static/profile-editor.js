import { profileDraft } from "./profile-draft.js";
import { profileReadiness } from "./profile-readiness.js";
import { renderProfileEditor as renderLegacyProfileEditor } from "./profiles.js";
import { profileSectionHref } from "./routes.js";
import {
  renderImmediateRoutingPolicy,
  renderProfileModelDirectory,
} from "./routing-policy-editor.js";

const sectionDefinitions = [
  ["overview", "概况"],
  ["connection", "连接"],
  ["models", "模型"],
  ["routing", "智能路由"],
  ["vision", "视觉增强"],
  ["reliability", "容错"],
  ["agents", "Agent 配置"],
];

const legacyHeadings = {
  connection: new Set(["基础配置"]),
  models: new Set(["模型能力"]),
  routing: new Set(["智能路由"]),
  vision: new Set(["视觉增强"]),
  reliability: new Set(["容错规则"]),
  agents: new Set(["配置生成"]),
};

export function renderProfileEditor(root, source, {
  section = "overview",
  defaultProfileID = 0,
  renderLegacy = renderLegacyProfileEditor,
  confirmLeave = defaultConfirmLeave,
  ...actions
} = {}) {
  const draft = profileDraft(source, defaultProfileID);
  draft.strategy_overview = source?.strategy_overview
    ? structuredClone(source.strategy_overview)
    : null;
  draft.strategy_editing_id = Number(source?.strategy_editing_id || 0);
  draft.strategy_configuration_mode = source?.strategy_configuration_mode === "manual"
    ? "manual"
    : "intelligent";
  draft.strategy_generation_notice = String(source?.strategy_generation_notice || "");
  draft.evaluation_catalog = source?.evaluation_catalog
    ? structuredClone(source.evaluation_catalog)
    : null;
  draft.routing_policy_overview = source?.routing_policy_overview
    ? structuredClone(source.routing_policy_overview)
    : null;
  draft.model_directory_overview = source?.model_directory_overview
    ? structuredClone(source.model_directory_overview)
    : null;
  const readiness = profileReadiness(draft, { saved: draft.id > 0 });
  const shell = element("div", "profile-detail-layout");
  const navigation = element("nav", "profile-section-nav");
  navigation.setAttribute("aria-label", "Profile 设置");
  const content = element("section", "profile-section-content stack");
  let editedForm = null;
  let dirtyState = null;

  for (const [id, label] of sectionDefinitions) {
    const link = textElement("a", label);
    link.setAttribute("href", profileSectionHref(draft.id, id));
    if (id === section) {
      link.setAttribute("aria-current", "page");
    }
    const status = readiness.sections[id];
    if (status && id !== "overview") {
      const badge = textElement(
        "span",
        statusLabel(status.state),
        `profile-section-state state-${status.state}`,
      );
      badge.setAttribute("aria-hidden", "true");
      link.append(badge);
    }
    navigation.append(link);
  }

  if (section === "overview") {
    content.append(profileOverview(draft, readiness));
  } else {
    dirtyState = { value: false };
    const guardedActions = {
      ...actions,
      save: async (payload) => {
        await actions.save?.(payload);
        dirtyState.value = false;
      },
      cancel: () => {
        if (dirtyState.value && !confirmLeave()) {
          return;
        }
        dirtyState.value = false;
        actions.cancel?.();
      },
    };
    const state = readiness.sections[section];
    if (state?.state === "blocked" || state?.state === "attention") {
      content.append(dependencyPanel(state));
    }
    if (Number(draft.config?.version) === 2 && section === "routing") {
      const policyEditor = renderImmediateRoutingPolicy(draft, {
        ...guardedActions,
        markDirty: () => { dirtyState.value = true; },
        clearDirty: () => { dirtyState.value = false; },
        applyRoutingPolicy: async (...args) => {
          await guardedActions.applyRoutingPolicy?.(...args);
          dirtyState.value = false;
        },
      });
      editedForm = policyEditor;
      content.append(policyEditor);
    } else if (Number(draft.config?.version) === 2 && section === "models") {
      const modelDirectory = renderProfileModelDirectory(draft, guardedActions);
      content.append(modelDirectory);
    } else {
      const legacyMount = renderLegacySection(draft, section, renderLegacy, guardedActions);
      editedForm = legacyMount.children[0] || null;
      content.append(legacyMount);
    }
  }

  if (editedForm) {
    protectUnsavedChanges(
      navigation,
      editedForm,
      dirtyState,
      confirmLeave,
    );
  }

  shell.append(navigation, content);
  root.replaceChildren(shell);
}

function renderLegacySection(
  draft,
  section,
  renderLegacy,
  actions,
) {
  const mount = element("div");
  renderLegacy(mount, draft, { ...actions, editorSection: section });
  const form = mount.children[0];
  if (!form) {
    throw new Error("Profile 编辑器未生成表单。");
  }
  const selectedHeadings = legacyHeadings[section] || new Set();
  for (const child of [...form.children]) {
    if (child.tagName !== "SECTION") {
      continue;
    }
    const heading = child.children.find?.((item) => item.tagName === "H2") ||
      child.children[0];
    if (!selectedHeadings.has(heading?.textContent)) {
      child.remove();
    }
  }
  form.className = `${form.className || ""} profile-section-form stack`.trim();
  return mount;
}

function profileOverview(draft, readiness) {
  const fragment = element("div", "stack");
  const status = element("section", "card profile-overview-hero stack");
  const header = element("div", "cluster section-heading");
  header.append(
    textElement("h2", draft.display_name || draft.slug),
    textElement(
      "span",
      draft.enabled ? "已启用" : "已停用",
      `status-pill ${draft.enabled ? "status-ready" : "status-disabled"}`,
    ),
  );
  status.append(
    header,
    textElement("code", `/${draft.slug}/v1/messages`),
    textElement(
      "p",
      readiness.state === "ready" ? "基础配置完整。" : "仍有依赖需要处理。",
      "muted",
    ),
  );
  const facts = element("dl", "profile-overview-facts");
  appendFact(facts, "协议", draft.config.protocol === "openai" ? "OpenAI" : "Anthropic");
  appendFact(facts, "上游", draft.config.upstream || "未设置");
  appendFact(facts, "模型", `${draft.config.models.length} 个`);
  appendFact(facts, "视觉增强", draft.config.vision.enabled ? "已启用" : "未启用");
  status.append(facts, linkElement("编辑连接", profileSectionHref(draft.id, "connection")));

  const capabilities = element("section", "profile-capability-grid");
  for (const [id, label] of sectionDefinitions.slice(2)) {
    const item = element("article", "card profile-capability-card stack");
    const sectionState = readiness.sections[id];
    item.append(
      textElement("h3", label),
      textElement("p", sectionDescription(id, sectionState), "muted"),
      linkElement("打开设置", profileSectionHref(draft.id, id)),
    );
    capabilities.append(item);
  }
  fragment.append(status, capabilities);
  return fragment;
}

function dependencyPanel(sectionState) {
  const panel = element("section", "card dependency-panel stack");
  panel.append(
    textElement("h2", "依赖未满足"),
    ...sectionState.reasons.map((reason) => textElement("p", reason, "muted")),
  );
  if (sectionState.action) {
    panel.append(linkElement(
      sectionState.action.label,
      sectionState.action.href,
      "button",
    ));
  }
  return panel;
}

function protectUnsavedChanges(navigation, form, dirtyState, confirmLeave) {
  const markDirty = () => { dirtyState.value = true; };
  form.addEventListener("input", markDirty);
  form.addEventListener("change", markDirty);
  for (const link of navigation.children) {
    link.addEventListener("click", (event) => {
      if (dirtyState.value && !confirmLeave()) {
        event.preventDefault();
      }
    });
  }
  if (typeof window !== "undefined") {
    window.addEventListener("beforeunload", (event) => {
      if (!dirtyState.value) {
        return;
      }
      event.preventDefault();
      event.returnValue = "";
    });
  }
}

function defaultConfirmLeave() {
  return typeof window === "undefined" || window.confirm("当前页面有未保存的修改，确定离开吗？");
}

function sectionDescription(section, state) {
  if (state?.reasons?.length) {
    return state.reasons[0];
  }
  return {
    models: "补充能力、上下文和价格信息。",
    routing: "按任务复杂度选择合适的模型。",
    vision: "为不支持视觉的主模型补充图片理解。",
    reliability: "管理同一 Upstream 的普通重试。",
    agents: "生成 Claude Code、Codex 或 OpenCode 配置。",
  }[section];
}

function statusLabel(state) {
  return {
    ready: "就绪",
    blocked: "需处理",
    disabled: "未启用",
    attention: "选填",
  }[state] || "";
}

function appendFact(list, term, description) {
  list.append(textElement("dt", term), textElement("dd", description));
}

function element(tag, className = "") {
  const value = document.createElement(tag);
  value.className = className;
  return value;
}

function textElement(tag, text, className = "") {
  const value = element(tag, className);
  value.textContent = text;
  return value;
}

function linkElement(text, href, className = "") {
  const value = textElement("a", text, className);
  value.setAttribute("href", href);
  return value;
}
