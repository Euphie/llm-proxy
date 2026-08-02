import { availableAgentAdapters, openConfigurationGenerator } from "./generator.js";
import { defaultProfileDraft, profilePayload } from "./profile-draft.js";
import { profileSectionHref } from "./routes.js";

const steps = [
  ["connection", "连接上游"],
  ["models", "模型（选填）"],
  ["agents", "Agent 配置"],
  ["verify", "验证请求"],
];

export function nextOnboardingStep({ profiles = [], requestCount = 0 } = {}) {
  if (profiles.length === 0) {
    return "connection";
  }
  if (Number(requestCount) > 0) {
    return "complete";
  }
  return "agents";
}

export async function renderOnboarding(root, {
  profiles: profileData = {},
  client = {},
  publicOrigin = currentOrigin(),
  navigate = defaultNavigate,
  openGenerator = openConfigurationGenerator,
  pollerFactory = createRequestPoller,
} = {}) {
  let profiles = [...(profileData.profiles || [])];
  let profile = selectProfile(profiles, profileData.default_profile_id);
  let requestCount = 0;
  let currentStep = "connection";
  let poller = null;
  let disposed = false;

  if (profile) {
    const stats = typeof client.stats === "function"
      ? await client.stats({ profile_id: profile.id })
      : { summary: { requests: 0 } };
    requestCount = Number(stats?.summary?.requests || 0);
    currentStep = nextOnboardingStep({ profiles, requestCount });
  }

  function render() {
    if (disposed) {
      return;
    }
    poller?.stop();
    poller = null;
    const page = element("div", "setup-flow stack");
    const header = element("header", "setup-header stack");
    header.append(
      textElement("p", "首次接入", "eyebrow"),
      textElement("h2", currentStep === "complete" ? "配置完成" : "让第一个 Agent 跑起来"),
      textElement("p", "整个过程不保存密钥；认证信息仍由 Agent 在请求时透传。", "muted"),
    );
    page.append(header, stepNavigation(currentStep));

    if (currentStep === "connection") {
      page.append(connectionPanel());
    } else if (currentStep === "models") {
      page.append(modelsPanel());
    } else if (currentStep === "agents") {
      page.append(agentsPanel());
    } else if (currentStep === "verify") {
      page.append(verifyPanel());
    } else {
      page.append(completePanel());
    }
    root.replaceChildren(page);
  }

  function connectionPanel() {
    const section = element("section", "card setup-panel stack");
    section.append(
      textElement("h3", "1. 连接上游"),
      textElement("p", "只填写转发所需的地址信息，不填写 API Key。", "muted"),
    );
    const form = element("form", "stack");
    const fields = element("div", "form-grid");
    const displayName = inputField(fields, "名称", "setup-display-name", "默认 Profile", true);
    const slug = inputField(fields, "Slug", "setup-slug", "default", true);
    const protocol = selectField(fields, "协议", "setup-protocol", [
      ["anthropic", "Anthropic"],
      ["openai", "OpenAI"],
    ]);
    const upstream = inputField(fields, "Upstream", "setup-upstream", "", true, "url");
    const alert = element("div", "error-banner");
    alert.setAttribute("role", "alert");
    alert.hidden = true;
    const submit = button("保存并继续", "button");
    submit.type = "submit";
    form.append(fields, alert, submit);
    form.addEventListener("submit", async (event) => {
      event.preventDefault();
      alert.hidden = true;
      try {
        const draft = defaultProfileDraft(protocol.value);
        draft.display_name = displayName.value;
        draft.slug = slug.value;
        draft.enabled = true;
        draft.make_default = profiles.length === 0;
        draft.config.upstream = upstream.value;
        const created = await client.createProfile(profilePayload(draft));
        profile = normalizeCreatedProfile(created, draft);
        profiles = [profile, ...profiles];
        currentStep = "models";
        render();
      } catch (error) {
        alert.textContent = error?.message || "保存失败，请检查填写内容。";
        alert.hidden = false;
      }
    });
    section.append(form);
    return section;
  }

  function modelsPanel() {
    const section = element("section", "card setup-panel stack");
    section.append(
      textElement("h3", "2. 模型（选填）"),
      textElement(
        "p",
        "模型信息用于智能路由、视觉能力判断和 Agent 上下文配置；只做基础转发时可以跳过。",
        "muted",
      ),
    );
    const actions = element("div", "cluster");
    actions.append(
      linkElement("前往录入模型", profileSectionHref(profile.id, "models"), "button button-secondary"),
    );
    const skip = button("跳过模型录入", "button");
    skip.addEventListener("click", () => {
      currentStep = "agents";
      render();
    });
    actions.append(skip);
    section.append(actions);
    return section;
  }

  function agentsPanel() {
    const section = element("section", "card setup-panel stack");
    section.append(
      textElement("h3", "3. 生成 Agent 配置"),
      textElement("p", "根据 Profile 协议生成可复制的客户端配置示例。", "muted"),
    );
    const fields = element("div", "form-grid");
    const adapter = selectField(
      fields,
      "Agent",
      "setup-agent",
      availableAgentAdapters(profile.config?.protocol || "anthropic"),
    );
    const actions = element("div", "cluster");
    const generate = button("生成配置", "button button-secondary");
    generate.addEventListener("click", () => {
      openGenerator(root, profile, { origin: publicOrigin, adapter: adapter.value });
    });
    const continueButton = button("配置完成，开始验证", "button");
    continueButton.addEventListener("click", () => {
      currentStep = "verify";
      render();
    });
    actions.append(generate, continueButton);
    section.append(fields, actions);
    return section;
  }

  function verifyPanel() {
    const section = element("section", "card setup-panel stack");
    const status = textElement(
      "p",
      `等待 /${profile.slug}/v1/… 收到首个真实请求。`,
      "verification-status",
    );
    status.setAttribute("role", "status");
    status.setAttribute("aria-live", "polite");
    const refresh = button("立即检查", "button button-secondary");
    const check = async () => {
      try {
        const stats = await client.stats({ profile_id: profile.id });
        if (Number(stats?.summary?.requests || 0) > 0) {
          currentStep = "complete";
          render();
          navigate("/_admin/");
          return true;
        }
        status.textContent = "尚未检测到请求，保持此页面打开会自动检查。";
      } catch (error) {
        status.textContent = error?.message || "检查失败，可稍后重试。";
      }
      return false;
    };
    refresh.addEventListener("click", check);
    section.append(
      textElement("h3", "4. 验证请求"),
      textElement("p", "从 Agent 发送一个真实请求，后台只检查统计是否增加。", "muted"),
      status,
      refresh,
    );
    poller = pollerFactory({
      load: () => client.stats({ profile_id: profile.id }),
      onDetected: () => {
        if (disposed) {
          return;
        }
        currentStep = "complete";
        render();
        navigate("/_admin/");
      },
      onError: (error) => {
        status.textContent = error?.message || "自动检查暂时失败，可手动重试。";
      },
    });
    void poller.start();
    return section;
  }

  function completePanel() {
    const section = element("section", "card setup-panel stack");
    section.append(
      textElement("h3", "已收到真实请求"),
      textElement("p", "Profile 已经可以使用，之后可按需启用模型、视觉和智能路由。", "muted"),
      linkElement("返回概览", "/_admin/", "button"),
    );
    return section;
  }

  render();
  return {
    dispose() {
      disposed = true;
      poller?.stop();
    },
  };
}

export function createRequestPoller({
  load,
  onDetected,
  onError = () => {},
  interval = 3000,
  documentRef = typeof document === "undefined" ? null : document,
  setTimeoutRef = setTimeout,
  clearTimeoutRef = clearTimeout,
} = {}) {
  let timer = 0;
  let stopped = false;
  let running = false;

  const schedule = () => {
    if (stopped || documentRef?.hidden) {
      return;
    }
    timer = setTimeoutRef(tick, interval);
  };
  const tick = async () => {
    if (stopped || running || documentRef?.hidden) {
      return;
    }
    running = true;
    try {
      const data = await load();
      if (Number(data?.summary?.requests || 0) > 0) {
        stopped = true;
        clearTimeoutRef(timer);
        await onDetected();
        return;
      }
    } catch (error) {
      onError(error);
    } finally {
      running = false;
    }
    schedule();
  };
  const onVisibilityChange = () => {
    if (stopped) {
      return;
    }
    if (documentRef?.hidden) {
      clearTimeoutRef(timer);
      timer = 0;
      return;
    }
    if (!running && !timer) {
      return tick();
    }
  };
  documentRef?.addEventListener?.("visibilitychange", onVisibilityChange);

  return {
    start: tick,
    refresh: tick,
    stop() {
      stopped = true;
      clearTimeoutRef(timer);
      timer = 0;
      documentRef?.removeEventListener?.("visibilitychange", onVisibilityChange);
    },
  };
}

function stepNavigation(currentStep) {
  const list = element("ol", "setup-progress");
  const currentIndex = steps.findIndex(([id]) => id === currentStep);
  steps.forEach(([id, label], index) => {
    const item = textElement("li", label);
    item.setAttribute(
      "data-state",
      index < currentIndex || currentStep === "complete"
        ? "complete"
        : id === currentStep ? "current" : "pending",
    );
    if (id === currentStep) {
      item.setAttribute("aria-current", "step");
    }
    list.append(item);
  });
  return list;
}

function normalizeCreatedProfile(created, draft) {
  const candidate = created?.profile || created;
  return {
    id: Number(candidate?.id || 0),
    slug: String(candidate?.slug ?? draft.slug),
    display_name: String(candidate?.display_name ?? draft.display_name),
    enabled: candidate?.enabled ?? true,
    config: candidate?.config || profilePayload(draft).config,
  };
}

function selectProfile(profiles, defaultProfileID) {
  return profiles.find((profile) => Number(profile.id) === Number(defaultProfileID)) ||
    profiles.find((profile) => profile.enabled) || profiles[0] || null;
}

function inputField(parent, label, name, value, required = false, type = "text") {
  const field = element("label", "field");
  field.append(textElement("span", label));
  const input = element("input");
  input.name = name;
  input.value = value;
  input.required = required;
  input.type = type;
  field.append(input);
  parent.append(field);
  return input;
}

function selectField(parent, label, name, choices) {
  const field = element("label", "field");
  field.append(textElement("span", label));
  const select = element("select");
  select.name = name;
  for (const [value, text] of choices) {
    const option = textElement("option", text);
    option.value = value;
    select.append(option);
  }
  select.value = choices[0]?.[0] || "";
  field.append(select);
  parent.append(field);
  return select;
}

function button(text, className) {
  const value = textElement("button", text, className);
  value.type = "button";
  return value;
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

function currentOrigin() {
  return typeof window === "undefined" ? "http://localhost:8080" : window.location.origin;
}

function defaultNavigate(href) {
  if (typeof window !== "undefined") {
    window.location.assign(href);
  }
}
